/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	api "github.com/rh-mobb/managed-bgp-peer-operator/api/v1alpha1"
	"github.com/rh-mobb/managed-bgp-peer-operator/internal/reconciler"
)

// ManagedBGPPeerReconciler reconciles a ManagedBGPPeer object.
type ManagedBGPPeerReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	NewBackend reconciler.BackendFactory
}

// +kubebuilder:rbac:groups=bgp.network.redhat.com,resources=managedbgppeers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bgp.network.redhat.com,resources=managedbgppeers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bgp.network.redhat.com,resources=managedbgppeers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=machine.openshift.io,resources=machines,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

func (r *ManagedBGPPeerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var peer api.ManagedBGPPeer
	if err := r.Get(ctx, req.NamespacedName, &peer); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !peer.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &peer)
	}

	if !controllerutil.ContainsFinalizer(&peer, api.FinalizerName) {
		controllerutil.AddFinalizer(&peer, api.FinalizerName)
		if err := r.Update(ctx, &peer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	cfg, err := specToReconcilerConfig(&peer)
	if err != nil {
		return r.setDegraded(ctx, req.NamespacedName, "InvalidSpec", err)
	}

	if peer.Spec.Suspended {
		return r.handleSuspended(ctx, req.NamespacedName, cfg)
	}

	resumeFromSuspend := meta.IsStatusConditionTrue(peer.Status.Conditions, api.ConditionTypeSuspended)

	rec := &reconciler.Reconciler{
		Cfg:        cfg,
		Client:     r.Client,
		NewBackend: r.NewBackend,
	}
	res, err := rec.Reconcile(ctx)
	if err != nil {
		return r.setDegraded(ctx, req.NamespacedName, "ReconcileFailed", err)
	}

	log.Info("managed BGP peer reconcile completed",
		"routerNodes", res.NodesFound,
		"peersChanged", res.PeersChanged,
	)

	now := metav1.Now()
	statusPatch := func(latest *api.ManagedBGPPeer) {
		if resumeFromSuspend {
			meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
				Type:               api.ConditionTypeSuspended,
				Status:             metav1.ConditionFalse,
				Reason:             "Resumed",
				Message:            "Reconciliation resumed",
				ObservedGeneration: latest.Generation,
			})
		}

		latest.Status.ObservedGeneration = latest.Generation
		latest.Status.PeerCount = len(res.Peers)
		latest.Status.LastReconcileTime = &now
		latest.Status.Peers = nil
		for _, pr := range res.Peers {
			latest.Status.Peers = append(latest.Status.Peers, api.ManagedPeerStatus{
				NodeName:          pr.NodeName,
				PeerIP:            pr.PeerIP,
				PeerName:          pr.PeerName,
				ProvisioningState: pr.ProvisioningState,
				PeerBGPState:      pr.PeerBGPState,
			})
		}

		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeReady,
			Status:             metav1.ConditionTrue,
			Reason:             "ReconcileSucceeded",
			Message:            fmt.Sprintf("Reconciled %d BGP peers", len(res.Peers)),
			ObservedGeneration: latest.Generation,
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "ReconcileComplete",
			Message:            "Reconciliation finished",
			ObservedGeneration: latest.Generation,
		})
		meta.RemoveStatusCondition(&latest.Status.Conditions, api.ConditionTypeDegraded)
	}

	if err := r.patchStatus(ctx, req.NamespacedName, statusPatch); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: cfg.ReconcileInterval}, nil
}

func (r *ManagedBGPPeerReconciler) handleDeletion(ctx context.Context, peer *api.ManagedBGPPeer) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(peer, api.FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("ManagedBGPPeer being deleted, running cleanup")
	cfg, err := specToReconcilerConfig(peer)
	if err != nil {
		log.Error(err, "invalid spec during deletion")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	rec := &reconciler.Reconciler{
		Cfg:        cfg,
		Client:     r.Client,
		NewBackend: r.NewBackend,
	}
	if err := rec.Cleanup(ctx); err != nil {
		log.Error(err, "cleanup failed, will retry")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	controllerutil.RemoveFinalizer(peer, api.FinalizerName)
	if err := r.Update(ctx, peer); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *ManagedBGPPeerReconciler) handleSuspended(ctx context.Context, key client.ObjectKey, cfg *reconciler.ReconcilerConfig) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var peer api.ManagedBGPPeer
	if err := r.Get(ctx, key, &peer); err != nil {
		return ctrl.Result{}, err
	}
	if meta.IsStatusConditionTrue(peer.Status.Conditions, api.ConditionTypeSuspended) {
		return ctrl.Result{}, nil
	}

	log.Info("ManagedBGPPeer suspended, running cleanup")
	rec := &reconciler.Reconciler{
		Cfg:        cfg,
		Client:     r.Client,
		NewBackend: r.NewBackend,
	}
	if err := rec.Cleanup(ctx); err != nil {
		return r.setDegraded(ctx, key, "SuspendCleanupFailed", err)
	}

	statusPatch := func(latest *api.ManagedBGPPeer) {
		latest.Status.ObservedGeneration = latest.Generation
		latest.Status.PeerCount = 0
		latest.Status.Peers = nil

		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeSuspended,
			Status:             metav1.ConditionTrue,
			Reason:             "Suspended",
			Message:            "External BGP peering is suspended; cleanup completed",
			ObservedGeneration: latest.Generation,
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             "Suspended",
			Message:            "External BGP peering is suspended",
			ObservedGeneration: latest.Generation,
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "Suspended",
			Message:            "Reconciliation paused",
			ObservedGeneration: latest.Generation,
		})
		meta.RemoveStatusCondition(&latest.Status.Conditions, api.ConditionTypeDegraded)
	}

	if err := r.patchStatus(ctx, key, statusPatch); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	r.Recorder.Event(&peer, corev1.EventTypeNormal, "Suspended", "External BGP peering suspended and cleanup completed")
	return ctrl.Result{}, nil
}

func (r *ManagedBGPPeerReconciler) setDegraded(ctx context.Context, key client.ObjectKey, reason string, reconcileErr error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Error(reconcileErr, "reconciliation degraded", "reason", reason)

	statusPatch := func(latest *api.ManagedBGPPeer) {
		latest.Status.ObservedGeneration = latest.Generation
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeDegraded,
			Status:             metav1.ConditionTrue,
			Reason:             reason,
			Message:            reconcileErr.Error(),
			ObservedGeneration: latest.Generation,
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeReady,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            reconcileErr.Error(),
			ObservedGeneration: latest.Generation,
		})
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            "Reconciliation failed",
			ObservedGeneration: latest.Generation,
		})
	}

	if statusErr := r.patchStatus(ctx, key, statusPatch); statusErr != nil {
		log.Error(statusErr, "failed to update degraded status")
		if apierrors.IsConflict(statusErr) {
			return ctrl.Result{Requeue: true}, reconcileErr
		}
	}

	var peer api.ManagedBGPPeer
	if err := r.Get(ctx, key, &peer); err != nil {
		return ctrl.Result{RequeueAfter: 60 * time.Second}, reconcileErr
	}
	cfg, cfgErr := specToReconcilerConfig(&peer)
	requeue := 60 * time.Second
	if cfgErr == nil {
		requeue = cfg.ReconcileInterval
	}
	return ctrl.Result{RequeueAfter: requeue}, reconcileErr
}

// patchStatus re-fetches the latest object before applying a status mutation to avoid conflicts.
func (r *ManagedBGPPeerReconciler) patchStatus(ctx context.Context, key client.ObjectKey, apply func(*api.ManagedBGPPeer)) error {
	var latest api.ManagedBGPPeer
	if err := r.Get(ctx, key, &latest); err != nil {
		return err
	}
	apply(&latest)
	return r.Status().Update(ctx, &latest)
}

func specToReconcilerConfig(peer *api.ManagedBGPPeer) (*reconciler.ReconcilerConfig, error) {
	spec := peer.Spec
	if spec.Backend.Type != api.BackendTypeAzureRouteServer {
		return nil, fmt.Errorf("unsupported backend type %q", spec.Backend.Type)
	}
	if spec.Backend.AzureRouteServer == nil {
		return nil, fmt.Errorf("backend.azureRouteServer is required for AzureRouteServer backend")
	}

	reconcileSeconds := spec.ReconcileIntervalSeconds
	if reconcileSeconds == 0 {
		reconcileSeconds = api.DefaultReconcileIntervalSeconds
	}
	debounceSeconds := spec.DebounceSeconds
	if debounceSeconds == 0 {
		debounceSeconds = api.DefaultDebounceSeconds
	}
	machineNamespace := spec.MachineNamespace
	if machineNamespace == "" {
		machineNamespace = api.DefaultMachineNamespace
	}

	az := spec.Backend.AzureRouteServer
	return &reconciler.ReconcilerConfig{
		Name:              peer.Name,
		NodeSelector:      spec.NodeSelector.MatchLabels,
		PeerASN:           spec.PeerASN,
		RequireReadyNodes: spec.IsRequireReadyNodesEnabled(),
		ReconcileInterval: time.Duration(reconcileSeconds) * time.Second,
		Debounce:          time.Duration(debounceSeconds) * time.Second,
		MachineNamespace:  machineNamespace,
		BackendType:       spec.Backend.Type,
		AzureRouteServer: reconciler.AzureRouteServerConfig{
			SubscriptionID:  az.SubscriptionID,
			ResourceGroup:   az.ResourceGroup,
			RouteServerName: az.RouteServerName,
		},
	}, nil
}

func (r *ManagedBGPPeerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	machineObj := &unstructured.Unstructured{}
	machineObj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   reconciler.MachineGroup,
		Version: reconciler.MachineVersion,
		Kind:    reconciler.MachineKind,
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&api.ManagedBGPPeer{}, builder.WithPredicates(managedBGPPeerPredicate())).
		Watches(&corev1.Node{}, r.nodeEnqueueHandler(), builder.WithPredicates(nodeEventFilter{})).
		Watches(machineObj, r.nodeEnqueueHandler(), builder.WithPredicates(machineEventFilter{})).
		Named("managedbgppeer").
		Complete(r)
}

func (r *ManagedBGPPeerReconciler) nodeEnqueueHandler() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		log := logf.FromContext(ctx)
		var peerList api.ManagedBGPPeerList
		if err := r.List(ctx, &peerList); err != nil {
			log.Error(err, "failed to list ManagedBGPPeer resources for node watch")
			return nil
		}

		var requests []reconcile.Request
		for i := range peerList.Items {
			peer := &peerList.Items[i]
			if peer.Spec.Suspended {
				continue
			}
			sel, err := metav1.LabelSelectorAsSelector(&peer.Spec.NodeSelector)
			if err != nil {
				continue
			}
			if obj != nil && !sel.Matches(labels.Set(obj.GetLabels())) {
				continue
			}
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(peer)})
		}
		return requests
	})
}

type nodeEventFilter struct{}

func (nodeEventFilter) Create(e event.CreateEvent) bool   { return true }
func (nodeEventFilter) Delete(e event.DeleteEvent) bool   { return true }
func (nodeEventFilter) Generic(e event.GenericEvent) bool { return true }

func (nodeEventFilter) Update(e event.UpdateEvent) bool {
	oldN, ok1 := e.ObjectOld.(*corev1.Node)
	newN, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return true
	}
	if !reflect.DeepEqual(oldN.Labels, newN.Labels) {
		return true
	}
	if oldN.Spec.ProviderID != newN.Spec.ProviderID {
		return true
	}
	if !reflect.DeepEqual(oldN.Status.Addresses, newN.Status.Addresses) {
		return true
	}
	oldReady := nodeReady(oldN)
	newReady := nodeReady(newN)
	return oldReady != newReady
}

func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

type machineEventFilter struct{}

func (machineEventFilter) Create(_ event.CreateEvent) bool   { return true }
func (machineEventFilter) Delete(_ event.DeleteEvent) bool   { return true }
func (machineEventFilter) Generic(_ event.GenericEvent) bool { return false }

func (machineEventFilter) Update(e event.UpdateEvent) bool {
	oldU, ok1 := e.ObjectOld.(*unstructured.Unstructured)
	newU, ok2 := e.ObjectNew.(*unstructured.Unstructured)
	if !ok1 || !ok2 {
		return true
	}
	if oldU.GetDeletionTimestamp() == nil && newU.GetDeletionTimestamp() != nil {
		return true
	}
	oldHooks, _, _ := unstructured.NestedFieldNoCopy(oldU.Object, "spec", "lifecycleHooks")
	newHooks, _, _ := unstructured.NestedFieldNoCopy(newU.Object, "spec", "lifecycleHooks")
	return !reflect.DeepEqual(oldHooks, newHooks)
}

func managedBGPPeerPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool { return true },
		DeleteFunc: func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldObj, okOld := e.ObjectOld.(metav1.Object)
			newObj, okNew := e.ObjectNew.(metav1.Object)
			if !okOld || !okNew {
				return true
			}
			if oldObj.GetGeneration() != newObj.GetGeneration() {
				return true
			}
			return deletionTimestampChanged(oldObj, newObj)
		},
	}
}

func deletionTimestampChanged(oldObj, newObj metav1.Object) bool {
	oldDel := oldObj.GetDeletionTimestamp()
	newDel := newObj.GetDeletionTimestamp()
	if oldDel == nil && newDel == nil {
		return false
	}
	if oldDel == nil || newDel == nil {
		return true
	}
	return !oldDel.Equal(newDel)
}
