package reconciler

import "time"

// ReconcilerConfig holds runtime configuration derived from a ManagedBGPPeer spec.
type ReconcilerConfig struct {
	Name                 string
	NodeSelector         map[string]string
	NodeSelectorMatchExp []NodeSelectorRequirement
	PeerASN              int64
	RequireReadyNodes    bool
	ReconcileInterval    time.Duration
	Debounce             time.Duration
	MachineNamespace     string
	BackendType          string
	AzureRouteServer     AzureRouteServerConfig
}

// AzureRouteServerConfig identifies the Azure Route Server target.
type AzureRouteServerConfig struct {
	SubscriptionID  string
	ResourceGroup   string
	RouteServerName string
}

// NodeSelectorRequirement mirrors a single match expression when used.
type NodeSelectorRequirement struct {
	Key      string
	Operator string
	Values   []string
}

// RouterNode is a Kubernetes node eligible for external BGP peering.
type RouterNode struct {
	K8sName    string
	ProviderID string
	IPAddress  string
}

// ReconcileResult summarizes one reconciliation pass.
type ReconcileResult struct {
	NodesFound   int
	PeersChanged bool
	Peers        []PeerResult
}

// PeerResult captures per-node peer reconciliation detail.
type PeerResult struct {
	NodeName          string
	PeerIP            string
	PeerName          string
	ProvisioningState string
	PeerBGPState      string
}
