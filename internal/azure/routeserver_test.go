package azure

import (
	"testing"

	"github.com/rh-mobb/managed-bgp-peer-operator/internal/backend"
)

func TestPeerSetEqual(t *testing.T) {
	left := buildDesiredSet([]backend.Peer{
		{Name: "node-a", PeerIP: "10.0.0.1", PeerASN: 65001},
	})
	right := buildPeerSet([]backend.ObservedPeer{
		{Peer: backend.Peer{Name: "node-a", PeerIP: "10.0.0.1", PeerASN: 65001}},
	})
	if !left.Equal(right) {
		t.Fatal("expected peer sets to be equal")
	}

	changed := buildDesiredSet([]backend.Peer{
		{Name: "node-a", PeerIP: "10.0.0.2", PeerASN: 65001},
	})
	if left.Equal(changed) {
		t.Fatal("expected peer sets to differ when IP changes")
	}
}

func TestFindCurrent(t *testing.T) {
	current := []backend.ObservedPeer{
		{Peer: backend.Peer{Name: "node-a", PeerIP: "10.0.0.1", PeerASN: 65001}},
	}
	if _, ok := findCurrent(current, "node-b"); ok {
		t.Fatal("expected node-b to be missing")
	}
	got, ok := findCurrent(current, "node-a")
	if !ok || got.PeerIP != "10.0.0.1" {
		t.Fatalf("unexpected peer: %+v ok=%v", got, ok)
	}
}
