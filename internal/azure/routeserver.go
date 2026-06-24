package azure

import (
	"context"
	"fmt"
	"sort"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"

	api "github.com/rh-mobb/managed-bgp-peer-operator/api/v1alpha1"
	"github.com/rh-mobb/managed-bgp-peer-operator/internal/backend"
)

// RouteServerBackend manages Azure Route Server BGP peerings.
type RouteServerBackend struct {
	ResourceGroup   string
	RouteServerName string
	ListClient      *armnetwork.VirtualHubBgpConnectionsClient
	MutateClient    *armnetwork.VirtualHubBgpConnectionClient
}

type peerKey struct {
	name    string
	peerIP  string
	peerASN int64
}

type peerSet map[peerKey]struct{}

// NewRouteServerBackendFromSpec builds a backend from a ManagedBGPPeer Azure spec.
func NewRouteServerBackendFromSpec(spec *api.AzureRouteServerSpec) (*RouteServerBackend, error) {
	if spec == nil {
		return nil, fmt.Errorf("azureRouteServer spec is required")
	}
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure credential: %w", err)
	}
	factory, err := armnetwork.NewClientFactory(spec.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure network client factory: %w", err)
	}
	return &RouteServerBackend{
		ResourceGroup:   spec.ResourceGroup,
		RouteServerName: spec.RouteServerName,
		ListClient:      factory.NewVirtualHubBgpConnectionsClient(),
		MutateClient:    factory.NewVirtualHubBgpConnectionClient(),
	}, nil
}

// ListPeers returns current BGP connections on the Route Server.
func (b *RouteServerBackend) ListPeers(ctx context.Context) ([]backend.ObservedPeer, error) {
	var out []backend.ObservedPeer
	pager := b.ListClient.NewListPager(b.ResourceGroup, b.RouteServerName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list route server peerings: %w", err)
		}
		for _, conn := range page.Value {
			if conn == nil || conn.Name == nil {
				continue
			}
			peer := backend.ObservedPeer{
				Peer: backend.Peer{
					Name: *conn.Name,
				},
			}
			if conn.Properties != nil {
				if conn.Properties.PeerIP != nil {
					peer.PeerIP = *conn.Properties.PeerIP
				}
				if conn.Properties.PeerAsn != nil {
					peer.PeerASN = *conn.Properties.PeerAsn
				}
				if conn.Properties.ProvisioningState != nil {
					peer.ProvisioningState = string(*conn.Properties.ProvisioningState)
				}
				if conn.Properties.ConnectionState != nil {
					peer.PeerBGPState = string(*conn.Properties.ConnectionState)
				}
			}
			out = append(out, peer)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReconcilePeers creates, updates, or deletes peerings to match desired.
func (b *RouteServerBackend) ReconcilePeers(ctx context.Context, desired []backend.Peer) (bool, error) {
	current, err := b.ListPeers(ctx)
	if err != nil {
		return false, err
	}

	desiredSorted := append([]backend.Peer(nil), desired...)
	sort.Slice(desiredSorted, func(i, j int) bool { return desiredSorted[i].Name < desiredSorted[j].Name })

	if buildPeerSet(current).Equal(buildDesiredSet(desiredSorted)) {
		return false, nil
	}

	desiredByName := make(map[string]backend.Peer, len(desiredSorted))
	for _, p := range desiredSorted {
		desiredByName[p.Name] = p
	}

	changed := false
	for name, want := range desiredByName {
		cur, ok := findCurrent(current, name)
		if !ok || cur.PeerIP != want.PeerIP || cur.PeerASN != want.PeerASN {
			if err := b.createOrUpdate(ctx, want); err != nil {
				return changed, err
			}
			changed = true
		}
	}

	for _, cur := range current {
		if _, keep := desiredByName[cur.Name]; keep {
			continue
		}
		if err := b.delete(ctx, cur.Name); err != nil {
			return changed, err
		}
		changed = true
	}

	return changed, nil
}

// DeleteAllPeers removes all BGP connections on the Route Server.
func (b *RouteServerBackend) DeleteAllPeers(ctx context.Context) error {
	current, err := b.ListPeers(ctx)
	if err != nil {
		return err
	}
	for _, p := range current {
		if err := b.delete(ctx, p.Name); err != nil {
			return err
		}
	}
	return nil
}

func (b *RouteServerBackend) createOrUpdate(ctx context.Context, peer backend.Peer) error {
	params := armnetwork.BgpConnection{
		Properties: &armnetwork.BgpConnectionProperties{
			PeerAsn: to.Ptr(peer.PeerASN),
			PeerIP:  to.Ptr(peer.PeerIP),
		},
	}
	poller, err := b.MutateClient.BeginCreateOrUpdate(ctx, b.ResourceGroup, b.RouteServerName, peer.Name, params, nil)
	if err != nil {
		return fmt.Errorf("create/update peering %q: %w", peer.Name, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("wait for peering %q: %w", peer.Name, err)
	}
	return nil
}

func (b *RouteServerBackend) delete(ctx context.Context, name string) error {
	poller, err := b.MutateClient.BeginDelete(ctx, b.ResourceGroup, b.RouteServerName, name, nil)
	if err != nil {
		return fmt.Errorf("delete peering %q: %w", name, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("wait for delete peering %q: %w", name, err)
	}
	return nil
}

func findCurrent(current []backend.ObservedPeer, name string) (backend.ObservedPeer, bool) {
	for _, p := range current {
		if p.Name == name {
			return p, true
		}
	}
	return backend.ObservedPeer{}, false
}

func buildDesiredSet(peers []backend.Peer) peerSet {
	s := make(peerSet, len(peers))
	for _, p := range peers {
		s[peerKey{p.Name, p.PeerIP, p.PeerASN}] = struct{}{}
	}
	return s
}

func buildPeerSet(peers []backend.ObservedPeer) peerSet {
	s := make(peerSet, len(peers))
	for _, p := range peers {
		s[peerKey{p.Name, p.PeerIP, p.PeerASN}] = struct{}{}
	}
	return s
}

func (s peerSet) Equal(other peerSet) bool {
	if len(s) != len(other) {
		return false
	}
	for k := range s {
		if _, ok := other[k]; !ok {
			return false
		}
	}
	return true
}
