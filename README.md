# Managed BGP Peer Operator

Kubernetes operator that keeps **external BGP peering configuration** synchronized with cluster nodes matching a label selector. v1 targets **Azure Route Server** and replaces the interim DaemonSet/CronJob approach described in [aro-bgp/BGP_DESIGN.md](https://github.com/rh-mobb/aro-bgp/blob/main/BGP_DESIGN.md).

## Problem

FRR on OpenShift nodes auto-reconfigures when router nodes are replaced, but Azure Route Server peer entries are static. After a node swap the Route Server can retain a stale peer IP, breaking BGP until the remote side is updated manually.

This operator watches nodes selected by `spec.nodeSelector`, then creates, updates, and deletes Azure Route Server BGP peerings through the Azure SDK using Workload Identity.

## CRD: ManagedBGPPeer

Cluster-scoped resource (one per external BGP endpoint, e.g. one Azure Route Server):

```yaml
apiVersion: bgp.network.redhat.com/v1alpha1
kind: ManagedBGPPeer
metadata:
  name: azure-route-server
spec:
  suspended: false
  nodeSelector:
    matchLabels:
      bgp_router: "true"
  peerASN: 65001
  backend:
    type: AzureRouteServer
    azureRouteServer:
      subscriptionID: "<subscription-id>"
      resourceGroup: "<resource-group>"
      routeServerName: "<route-server-name>"
  reconcileIntervalSeconds: 60
  machineNamespace: openshift-machine-api
```

**Status conditions:** `Ready`, `Degraded`, `Progressing`, `Suspended`

**Out of scope:** FRR CR management, NIC src/dst check toggling, and node router label election remain separate workloads.

## Azure permissions

Create a custom role scoped to the Route Server with:

```json
{
  "Actions": [
    "Microsoft.Network/virtualHubs/bgpConnections/read",
    "Microsoft.Network/virtualHubs/bgpConnections/write"
  ]
}
```

Assign it to a dedicated Managed Identity federated to the operator ServiceAccount (not the `machine-api` identity).

## Deployment

### Build

```bash
make build
make docker-build IMG=<your-registry>/managed-bgp-peer-operator:latest
```

### Install on OpenShift (ARO)

1. Install the CRD:

```bash
kubectl apply -f config/crd/bases/bgp.network.redhat.com_managedbgppeers.yaml
```

2. Edit [deploy/operator.yaml](deploy/operator.yaml):
   - Set `azure.workload.identity/client-id` on the ServiceAccount
   - Set the operator container image

3. Apply the operator:

```bash
kubectl apply -f deploy/operator.yaml
```

4. Apply a `ManagedBGPPeer` CR (see [config/samples/v1alpha1_managedbgppeer.yaml](config/samples/v1alpha1_managedbgppeer.yaml)).

### Migration from DaemonSet

Once the operator is running and the CR is applied:

1. Verify Route Server peers appear in Azure and `ManagedBGPPeer.status.peers` lists the expected nodes.
2. Delete the interim `azure-route-server-peer-manager` DaemonSet.
3. Delete any orphan peer cleanup CronJob.
4. Remove the `azure-routeserver-config` ConfigMap; its values now live in the CR spec.

## Node replacement safety

On OpenShift clusters the operator registers a `preTerminate` lifecycle hook (`bgp.network.redhat.com/bgp-cleanup`) on BGP router Machines. When a Machine is deleted:

1. The hook blocks instance deletion until the operator removes the Azure peer.
2. The operator excludes terminating nodes from the desired peer set.
3. After peers are removed, the hook is released and Machine deletion proceeds.

On clusters without `machine.openshift.io`, hook logic is skipped automatically.

## Development

```bash
make test
make run
```

## Architecture

```
ManagedBGPPeer CR ──► Controller ──► Reconciler ──► PeerBackend
                         ▲                              │
                    Node/Machine                   Azure Route Server
                      watches                      (Virtual Hub BGP connections)
```

The `PeerBackend` interface allows future backends beyond Azure Route Server.
