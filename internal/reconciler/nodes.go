package reconciler

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DiscoverRouterNodes lists nodes matching the configured selector with InternalIP.
func DiscoverRouterNodes(ctx context.Context, c client.Client, cfg *ReconcilerConfig) ([]RouterNode, error) {
	selector, err := nodeSelector(cfg)
	if err != nil {
		return nil, fmt.Errorf("node selector: %w", err)
	}

	var list corev1.NodeList
	if err := c.List(ctx, &list, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}

	var out []RouterNode
	for i := range list.Items {
		node := &list.Items[i]
		if cfg.RequireReadyNodes && !nodeReady(node) {
			continue
		}
		ip := internalIP(node)
		if ip == "" {
			continue
		}
		out = append(out, RouterNode{
			K8sName:    node.Name,
			ProviderID: node.Spec.ProviderID,
			IPAddress:  ip,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].K8sName < out[j].K8sName })
	return out, nil
}

func nodeSelector(cfg *ReconcilerConfig) (labels.Selector, error) {
	ls := &metav1.LabelSelector{
		MatchLabels: cfg.NodeSelector,
	}
	if len(cfg.NodeSelectorMatchExp) > 0 {
		ls.MatchExpressions = make([]metav1.LabelSelectorRequirement, 0, len(cfg.NodeSelectorMatchExp))
		for _, req := range cfg.NodeSelectorMatchExp {
			ls.MatchExpressions = append(ls.MatchExpressions, metav1.LabelSelectorRequirement{
				Key:      req.Key,
				Operator: metav1.LabelSelectorOperator(req.Operator),
				Values:   req.Values,
			})
		}
	}
	return metav1.LabelSelectorAsSelector(ls)
}

func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func internalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

// ProviderIDSet builds a set of node provider IDs.
func ProviderIDSet(nodes []RouterNode) map[string]struct{} {
	out := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		if n.ProviderID != "" {
			out[n.ProviderID] = struct{}{}
		}
	}
	return out
}

// ExcludeTerminating removes nodes whose provider ID matches a terminating machine.
func ExcludeTerminating(nodes []RouterNode, terminating []TerminatingMachine) []RouterNode {
	if len(terminating) == 0 {
		return nodes
	}
	skip := make(map[string]struct{}, len(terminating))
	for _, tm := range terminating {
		if tm.ProviderID != "" {
			skip[tm.ProviderID] = struct{}{}
		}
	}
	var out []RouterNode
	for _, n := range nodes {
		if _, found := skip[n.ProviderID]; found {
			continue
		}
		out = append(out, n)
	}
	return out
}
