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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionTypeReady       = "Ready"
	ConditionTypeDegraded    = "Degraded"
	ConditionTypeProgressing = "Progressing"
	ConditionTypeSuspended   = "Suspended"

	FinalizerName = "bgp.network.redhat.com/cleanup"

	DefaultReconcileIntervalSeconds = 60
	DefaultDebounceSeconds          = 5
	DefaultMachineNamespace         = "openshift-machine-api"

	BackendTypeAzureRouteServer = "AzureRouteServer"

	BGPLifecycleHookName  = "bgp.network.redhat.com/bgp-cleanup"
	BGPLifecycleHookOwner = "ManagedBGPPeer"
)

// ManagedBGPPeerSpec defines the desired state of ManagedBGPPeer.
type ManagedBGPPeerSpec struct {
	// Suspended triggers cleanup of managed external peers and pauses reconciliation.
	// +optional
	Suspended bool `json:"suspended,omitempty"`

	// NodeSelector selects cluster nodes whose InternalIP addresses become BGP peers.
	// +kubebuilder:validation:Required
	NodeSelector metav1.LabelSelector `json:"nodeSelector"`

	// PeerASN is the autonomous system number of the cluster-side BGP speaker (e.g. FRR).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4294967295
	PeerASN int64 `json:"peerASN"`

	// Backend describes how to reach and authenticate with the external BGP system.
	// +kubebuilder:validation:Required
	Backend BackendSpec `json:"backend"`

	// RequireReadyNodes skips nodes that are not in the Ready condition.
	// +optional
	// +kubebuilder:default=true
	RequireReadyNodes *bool `json:"requireReadyNodes,omitempty"`

	// ReconcileIntervalSeconds is the interval between reconciliation passes.
	// +optional
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=5
	ReconcileIntervalSeconds int `json:"reconcileIntervalSeconds,omitempty"`

	// DebounceSeconds is reserved for future event batching.
	// +optional
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	DebounceSeconds int `json:"debounceSeconds,omitempty"`

	// MachineNamespace is where OpenShift Machine objects are managed.
	// +optional
	// +kubebuilder:default="openshift-machine-api"
	MachineNamespace string `json:"machineNamespace,omitempty"`
}

// BackendSpec selects the external BGP backend implementation.
type BackendSpec struct {
	// Type identifies the backend implementation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=AzureRouteServer
	Type string `json:"type"`

	// AzureRouteServer configures Azure Route Server peering via Virtual Hub BGP connections.
	// +optional
	AzureRouteServer *AzureRouteServerSpec `json:"azureRouteServer,omitempty"`
}

// AzureRouteServerSpec identifies an Azure Route Server (Virtual Hub) to manage.
type AzureRouteServerSpec struct {
	// SubscriptionID is the Azure subscription containing the Route Server.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	SubscriptionID string `json:"subscriptionID"`

	// ResourceGroup is the resource group containing the Route Server.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ResourceGroup string `json:"resourceGroup"`

	// RouteServerName is the Azure Route Server (Virtual Hub) resource name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RouteServerName string `json:"routeServerName"`
}

// ManagedPeerStatus describes one managed external BGP peer.
type ManagedPeerStatus struct {
	// NodeName is the Kubernetes node name used as the Azure peering name.
	NodeName string `json:"nodeName"`

	// PeerIP is the node InternalIP configured on the remote side.
	PeerIP string `json:"peerIP"`

	// PeerName is the name of the external BGP connection resource.
	PeerName string `json:"peerName,omitempty"`

	// ProvisioningState reflects the Azure provisioning state when available.
	// +optional
	ProvisioningState string `json:"provisioningState,omitempty"`

	// PeerBGPState reflects the observed BGP session state when available.
	// +optional
	PeerBGPState string `json:"peerBGPState,omitempty"`
}

// ManagedBGPPeerStatus defines the observed state of ManagedBGPPeer.
type ManagedBGPPeerStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the resource's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PeerCount is the number of active managed peers.
	// +optional
	PeerCount int `json:"peerCount,omitempty"`

	// Peers lists the current managed peer details.
	// +optional
	Peers []ManagedPeerStatus `json:"peers,omitempty"`

	// LastReconcileTime is the timestamp of the last successful reconciliation.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspended`
// +kubebuilder:printcolumn:name="Peers",type=integer,JSONPath=`.status.peerCount`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ManagedBGPPeer is the Schema for the managedbgppeers API.
type ManagedBGPPeer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedBGPPeerSpec   `json:"spec,omitempty"`
	Status ManagedBGPPeerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ManagedBGPPeerList contains a list of ManagedBGPPeer.
type ManagedBGPPeerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedBGPPeer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ManagedBGPPeer{}, &ManagedBGPPeerList{})
}

// IsRequireReadyNodesEnabled returns whether only Ready nodes are selected (default true).
func (s *ManagedBGPPeerSpec) IsRequireReadyNodesEnabled() bool {
	if s.RequireReadyNodes == nil {
		return true
	}
	return *s.RequireReadyNodes
}
