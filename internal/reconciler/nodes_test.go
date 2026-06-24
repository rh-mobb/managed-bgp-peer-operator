package reconciler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDiscoverRouterNodes(t *testing.T) {
	nodes := []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "router-a",
				Labels: map[string]string{"bgp_router": "true"},
			},
			Spec: corev1.NodeSpec{ProviderID: "azure:///subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/router-a"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.10"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "router-b",
				Labels: map[string]string{"bgp_router": "true"},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "worker-c",
				Labels: map[string]string{"node-role.kubernetes.io/worker": ""},
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
				Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.30"}},
			},
		},
	}

	cl := fake.NewClientBuilder().WithObjects(&nodes[0], &nodes[1], &nodes[2]).Build()
	cfg := &ReconcilerConfig{
		NodeSelector:      map[string]string{"bgp_router": "true"},
		RequireReadyNodes: true,
	}

	got, err := DiscoverRouterNodes(context.Background(), cl, cfg)
	if err != nil {
		t.Fatalf("DiscoverRouterNodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 router node, got %d", len(got))
	}
	if got[0].K8sName != "router-a" || got[0].IPAddress != "10.0.0.10" {
		t.Fatalf("unexpected router node: %+v", got[0])
	}
}

func TestExcludeTerminating(t *testing.T) {
	nodes := []RouterNode{
		{K8sName: "a", ProviderID: "azure:///subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/a", IPAddress: "10.0.0.1"},
		{K8sName: "b", ProviderID: "azure:///subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/b", IPAddress: "10.0.0.2"},
	}
	terminating := []TerminatingMachine{{ProviderID: nodes[1].ProviderID}}
	filtered := ExcludeTerminating(nodes, terminating)
	if len(filtered) != 1 || filtered[0].K8sName != "a" {
		t.Fatalf("unexpected filtered nodes: %+v", filtered)
	}
}

func TestProviderIDSet(t *testing.T) {
	nodes := []RouterNode{{ProviderID: "id-a"}, {ProviderID: ""}}
	set := ProviderIDSet(nodes)
	if len(set) != 1 {
		t.Fatalf("expected one provider ID, got %d", len(set))
	}
	if _, ok := set["id-a"]; !ok {
		t.Fatal("expected id-a in set")
	}
}
