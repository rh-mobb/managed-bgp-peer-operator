package backend

import (
	"context"
	"sort"
	"testing"
)

type mockBackend struct {
	peers   []ObservedPeer
	changed bool
}

func (m *mockBackend) ListPeers(_ context.Context) ([]ObservedPeer, error) {
	out := append([]ObservedPeer(nil), m.peers...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *mockBackend) ReconcilePeers(_ context.Context, desired []Peer) (bool, error) {
	m.changed = true
	m.peers = nil
	for _, p := range desired {
		m.peers = append(m.peers, ObservedPeer{Peer: p})
	}
	return true, nil
}

func (m *mockBackend) DeleteAllPeers(_ context.Context) error {
	m.peers = nil
	return nil
}

func TestMockBackendReconcile(t *testing.T) {
	mock := &mockBackend{}
	_, err := mock.ReconcilePeers(context.Background(), []Peer{
		{Name: "node-a", PeerIP: "10.0.0.1", PeerASN: 65001},
	})
	if err != nil {
		t.Fatalf("ReconcilePeers: %v", err)
	}
	peers, err := mock.ListPeers(context.Background())
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	if len(peers) != 1 || peers[0].Name != "node-a" {
		t.Fatalf("unexpected peers: %+v", peers)
	}
}
