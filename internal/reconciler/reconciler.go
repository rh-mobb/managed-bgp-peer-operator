package reconciler

import (
	"context"
	"fmt"

	api "github.com/rh-mobb/managed-bgp-peer-operator/api/v1alpha1"
	"github.com/rh-mobb/managed-bgp-peer-operator/internal/azure"
	"github.com/rh-mobb/managed-bgp-peer-operator/internal/backend"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crlog "sigs.k8s.io/controller-runtime/pkg/log"
)

// BackendFactory constructs a backend from reconciler config.
type BackendFactory func(cfg *ReconcilerConfig) (backend.Backend, error)

// Reconciler orchestrates node discovery and external BGP peer reconciliation.
type Reconciler struct {
	Cfg        *ReconcilerConfig
	Client     client.Client
	NewBackend BackendFactory
}

// Reconcile runs the full reconciliation loop.
func (r *Reconciler) Reconcile(ctx context.Context) (ReconcileResult, error) {
	log := crlog.FromContext(ctx)
	var res ReconcileResult

	b, err := r.NewBackend(r.Cfg)
	if err != nil {
		return res, err
	}

	nodes, err := DiscoverRouterNodes(ctx, r.Client, r.Cfg)
	if err != nil {
		return res, err
	}
	res.NodesFound = len(nodes)

	terminating, err := FindTerminatingMachines(ctx, r.Client, r.Cfg, ProviderIDSet(nodes))
	if err != nil {
		return res, fmt.Errorf("machine lifecycle hooks: %w", err)
	}
	activeNodes := ExcludeTerminating(nodes, terminating)

	desired := make([]backend.Peer, 0, len(activeNodes))
	for _, n := range activeNodes {
		desired = append(desired, backend.Peer{
			Name:    n.K8sName,
			PeerIP:  n.IPAddress,
			PeerASN: r.Cfg.PeerASN,
		})
	}

	changed, err := b.ReconcilePeers(ctx, desired)
	if err != nil {
		return res, err
	}
	res.PeersChanged = changed
	if changed {
		log.Info("external BGP peers updated", "desiredCount", len(desired))
	}

	observed, err := b.ListPeers(ctx)
	if err != nil {
		return res, err
	}
	observedByName := make(map[string]backend.ObservedPeer, len(observed))
	for _, p := range observed {
		observedByName[p.Name] = p
	}

	for _, n := range activeNodes {
		pr := PeerResult{
			NodeName: n.K8sName,
			PeerIP:   n.IPAddress,
			PeerName: n.K8sName,
		}
		if obs, ok := observedByName[n.K8sName]; ok {
			pr.ProvisioningState = obs.ProvisioningState
			pr.PeerBGPState = obs.PeerBGPState
		}
		res.Peers = append(res.Peers, pr)
	}

	if len(terminating) > 0 {
		if err := ReleaseMachines(ctx, r.Client, terminating); err != nil {
			return res, fmt.Errorf("release machine hooks: %w", err)
		}
		log.Info("released preTerminate lifecycle hooks", "machines", len(terminating))
	}

	return res, nil
}

// Cleanup removes all managed external BGP peers.
func (r *Reconciler) Cleanup(ctx context.Context) error {
	b, err := r.NewBackend(r.Cfg)
	if err != nil {
		return err
	}
	return b.DeleteAllPeers(ctx)
}

// NewBackendFromConfig selects the backend implementation from config.
func NewBackendFromConfig(cfg *ReconcilerConfig) (backend.Backend, error) {
	switch cfg.BackendType {
	case api.BackendTypeAzureRouteServer:
		return azure.NewRouteServerBackendFromSpec(&api.AzureRouteServerSpec{
			SubscriptionID:  cfg.AzureRouteServer.SubscriptionID,
			ResourceGroup:   cfg.AzureRouteServer.ResourceGroup,
			RouteServerName: cfg.AzureRouteServer.RouteServerName,
		})
	default:
		return nil, fmt.Errorf("unsupported backend type %q", cfg.BackendType)
	}
}
