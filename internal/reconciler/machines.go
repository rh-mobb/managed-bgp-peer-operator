package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	api "github.com/rh-mobb/managed-bgp-peer-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MachineGroup    = "machine.openshift.io"
	MachineVersion  = "v1beta1"
	MachineKind     = "Machine"
	MachineResource = "machines"
)

var (
	machineGVK = schema.GroupVersionKind{
		Group:   MachineGroup,
		Version: MachineVersion,
		Kind:    MachineKind,
	}
	machineListGVK = schema.GroupVersionKind{
		Group:   MachineGroup,
		Version: MachineVersion,
		Kind:    MachineKind + "List",
	}
	azureProviderIDRe = regexp.MustCompile(`^azure:///subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\.Compute/virtualMachines/[^/]+$`)
)

// TerminatingMachine holds identity for a Machine being deleted with our hook.
type TerminatingMachine struct {
	Name       string
	Namespace  string
	ProviderID string
}

// FindTerminatingMachines manages preTerminate hooks on BGP router Machines.
// Returns nil, nil when the Machine API is unavailable.
func FindTerminatingMachines(ctx context.Context, c client.Client, cfg *ReconcilerConfig, routerProviderIDs map[string]struct{}) ([]TerminatingMachine, error) {
	var list unstructured.UnstructuredList
	list.SetGroupVersionKind(machineListGVK)
	if err := c.List(ctx, &list, client.InNamespace(cfg.MachineNamespace)); err != nil {
		if apierrors.IsNotFound(err) || isMachineAPIAbsent(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list machines: %w", err)
	}

	var terminating []TerminatingMachine
	for i := range list.Items {
		m := &list.Items[i]
		providerID, err := machineAzureProviderID(m)
		if err != nil || providerID == "" {
			continue
		}

		_, inRouterSet := routerProviderIDs[providerID]
		isDeleting := m.GetDeletionTimestamp() != nil
		hasHook := hasBGPLifecycleHook(m)

		switch {
		case inRouterSet && isDeleting && hasHook:
			terminating = append(terminating, TerminatingMachine{
				Name:       m.GetName(),
				Namespace:  m.GetNamespace(),
				ProviderID: providerID,
			})
		case inRouterSet && !isDeleting && !hasHook:
			if err := addLifecycleHook(ctx, c, m); err != nil {
				return nil, fmt.Errorf("add lifecycle hook to machine %s: %w", m.GetName(), err)
			}
		case !inRouterSet && hasHook:
			if err := removeLifecycleHook(ctx, c, m); err != nil {
				return nil, fmt.Errorf("remove lifecycle hook from machine %s: %w", m.GetName(), err)
			}
		}
	}
	return terminating, nil
}

// ReleaseMachines removes lifecycle hooks after BGP peers are deleted.
func ReleaseMachines(ctx context.Context, c client.Client, machines []TerminatingMachine) error {
	for _, tm := range machines {
		var m unstructured.Unstructured
		m.SetGroupVersionKind(machineGVK)
		if err := c.Get(ctx, client.ObjectKey{Name: tm.Name, Namespace: tm.Namespace}, &m); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("get machine %s/%s: %w", tm.Namespace, tm.Name, err)
		}
		if err := removeLifecycleHook(ctx, c, &m); err != nil {
			return fmt.Errorf("release lifecycle hook on machine %s: %w", tm.Name, err)
		}
	}
	return nil
}

func addLifecycleHook(ctx context.Context, c client.Client, m *unstructured.Unstructured) error {
	hooks := getPreTerminateHooks(m)
	for _, h := range hooks {
		hm, _ := h.(map[string]interface{})
		if hm["name"] == api.BGPLifecycleHookName {
			return nil
		}
	}
	hooks = append(hooks, map[string]interface{}{
		"name":  api.BGPLifecycleHookName,
		"owner": api.BGPLifecycleHookOwner,
	})
	return patchLifecycleHooks(ctx, c, m, hooks)
}

func removeLifecycleHook(ctx context.Context, c client.Client, m *unstructured.Unstructured) error {
	hooks := getPreTerminateHooks(m)
	var filtered []interface{}
	for _, h := range hooks {
		hm, _ := h.(map[string]interface{})
		if hm["name"] == api.BGPLifecycleHookName {
			continue
		}
		filtered = append(filtered, h)
	}
	if len(filtered) == len(hooks) {
		return nil
	}
	return patchLifecycleHooks(ctx, c, m, filtered)
}

func getPreTerminateHooks(m *unstructured.Unstructured) []interface{} {
	lh, _, _ := unstructured.NestedMap(m.Object, "spec", "lifecycleHooks")
	if lh == nil {
		return nil
	}
	pt, _ := lh["preTerminate"].([]interface{})
	return pt
}

func hasBGPLifecycleHook(m *unstructured.Unstructured) bool {
	for _, h := range getPreTerminateHooks(m) {
		hm, _ := h.(map[string]interface{})
		if hm["name"] == api.BGPLifecycleHookName {
			return true
		}
	}
	return false
}

type lifecycleHookPatch struct {
	Spec struct {
		LifecycleHooks struct {
			PreTerminate []lifecycleHookEntry `json:"preTerminate,omitempty"`
		} `json:"lifecycleHooks"`
	} `json:"spec"`
}

type lifecycleHookEntry struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

func patchLifecycleHooks(ctx context.Context, c client.Client, m *unstructured.Unstructured, hooks []interface{}) error {
	var p lifecycleHookPatch
	for _, h := range hooks {
		hm, _ := h.(map[string]interface{})
		name, _ := hm["name"].(string)
		owner, _ := hm["owner"].(string)
		p.Spec.LifecycleHooks.PreTerminate = append(p.Spec.LifecycleHooks.PreTerminate, lifecycleHookEntry{Name: name, Owner: owner})
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	patch := m.DeepCopy()
	return c.Patch(ctx, patch, client.RawPatch(types.MergePatchType, data))
}

func machineAzureProviderID(m *unstructured.Unstructured) (string, error) {
	pid, found, err := unstructured.NestedString(m.Object, "spec", "providerID")
	if err != nil || !found || pid == "" {
		return "", nil
	}
	if !azureProviderIDRe.MatchString(pid) {
		return "", nil
	}
	return pid, nil
}

func isMachineAPIAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") ||
		strings.Contains(msg, "no kind is registered") ||
		strings.Contains(msg, "the server could not find the requested resource")
}
