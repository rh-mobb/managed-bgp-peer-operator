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
	}

	cfg, err := specToReconcilerConfig(&peer)
	if err != nil {
		return r.setDegraded(ctx, &peer, "InvalidSpec", err)
	}

	if peer.Spec.Suspended {
		return r.handleSuspended(ctx, &peer, cfg)
	}

	if meta.IsStatusConditionTrue(peer.Status.Conditions, api.ConditionTypeSuspended) {
		meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
			Type:               api.ConditionTypeSuspended,
			Status:             metav1.ConditionFalse,
			Reason:             "Resumed",
			Message:            "Reconciliation resumed",
			ObservedGeneration: peer.Generation,
		})
	}

	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeProgressing,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciling",
		Message:            "Reconciliation in progress",
		ObservedGeneration: peer.Generation,
	})
	peer.Status.ObservedGeneration = peer.Generation
	if err := r.Status().Update(ctx, &peer); err != nil {
		return ctrl.Result{}, err
	}

	rec := &reconciler.Reconciler{
		Cfg:        cfg,
		Client:     r.Client,
		NewBackend: r.NewBackend,
	}
	res, err := rec.Reconcile(ctx)
	if err != nil {
		return r.setDegraded(ctx, &peer, "ReconcileFailed", err)
	}

	log.Info("managed BGP peer reconcile completed",
		"routerNodes", res.NodesFound,
		"peersChanged", res.PeersChanged,
	)

	now := metav1.Now()
	peer.Status.ObservedGeneration = peer.Generation
	peer.Status.PeerCount = len(res.Peers)
	peer.Status.LastReconcileTime = &now
	peer.Status.Peers = nil
	for _, pr := range res.Peers {
		peer.Status.Peers = append(peer.Status.Peers, api.ManagedPeerStatus{
			NodeName:          pr.NodeName,
			PeerIP:            pr.PeerIP,
			PeerName:          pr.PeerName,
			ProvisioningState: pr.ProvisioningState,
			PeerBGPState:      pr.PeerBGPState,
		})
	}

	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSucceeded",
		Message:            fmt.Sprintf("Reconciled %d BGP peers", len(res.Peers)),
		ObservedGeneration: peer.Generation,
	})
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeProgressing,
		Status:             metav1.ConditionFalse,
		Reason:             "ReconcileComplete",
		Message:            "Reconciliation finished",
		ObservedGeneration: peer.Generation,
	})
	meta.RemoveStatusCondition(&peer.Status.Conditions, api.ConditionTypeDegraded)

	if err := r.Status().Update(ctx, &peer); err != nil {
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

func (r *ManagedBGPPeerReconciler) handleSuspended(ctx context.Context, peer *api.ManagedBGPPeer, cfg *reconciler.ReconcilerConfig) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
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
		return r.setDegraded(ctx, peer, "SuspendCleanupFailed", err)
	}

	peer.Status.ObservedGeneration = peer.Generation
	peer.Status.PeerCount = 0
	peer.Status.Peers = nil

	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeSuspended,
		Status:             metav1.ConditionTrue,
		Reason:             "Suspended",
		Message:            "External BGP peering is suspended; cleanup completed",
		ObservedGeneration: peer.Generation,
	})
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Suspended",
		Message:            "External BGP peering is suspended",
		ObservedGeneration: peer.Generation,
	})
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeProgressing,
		Status:             metav1.ConditionFalse,
		Reason:             "Suspended",
		Message:            "Reconciliation paused",
		ObservedGeneration: peer.Generation,
	})
	meta.RemoveStatusCondition(&peer.Status.Conditions, api.ConditionTypeDegraded)

	if err := r.Status().Update(ctx, peer); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Event(peer, corev1.EventTypeNormal, "Suspended", "External BGP peering suspended and cleanup completed")
	return ctrl.Result{}, nil
}

func (r *ManagedBGPPeerReconciler) setDegraded(ctx context.Context, peer *api.ManagedBGPPeer, reason string, err error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Error(err, "reconciliation degraded", "reason", reason)

	peer.Status.ObservedGeneration = peer.Generation
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeDegraded,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            err.Error(),
		ObservedGeneration: peer.Generation,
	})
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            err.Error(),
		ObservedGeneration: peer.Generation,
	})
	meta.SetStatusCondition(&peer.Status.Conditions, metav1.Condition{
		Type:               api.ConditionTypeProgressing,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            "Reconciliation failed",
		ObservedGeneration: peer.Generation,
	})

	if statusErr := r.Status().Update(ctx, peer); statusErr != nil {
		log.Error(statusErr, "failed to update degraded status")
	}

	cfg, cfgErr := specToReconcilerConfig(peer)
	requeue := 60 * time.Second
	if cfgErr == nil {
		requeue = cfg.ReconcileInterval
	}
	return ctrl.Result{RequeueAfter: requeue}, err
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
		For(&api.ManagedBGPPeer{}).
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
