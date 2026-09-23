- What is the correct behavior in the deletion of a `ManagedBGPPeer` event? Remove all associated peerings? Leave them? Make it configurable? -> The current logic is to delete all the peers, which takes quite a lot of time because Azure Route Server is quite slow and it appears to be a synchronous operation, so the `oc` CLI will also appear to hang while this is happening.

- If the name or path of the Azure Route server is incorrect when the `ManagedBGPPeer` is applied (i.e., Azure returns a `404`), it seems like the operator hangs and will not delete the bad peer:

```
2026-07-07T20:11:32Z INFO ManagedBGPPeer being deleted, running cleanup {"controller": "managedbgppeer", "controllerGroup": "bgp.network.redhat.com", "controllerKind": "ManagedBGPPeer", "ManagedBGPPeer": {"name":"azure-route-server"}, "namespace": "", "name": "azure-route-server", "reconcileID": "a0d20eb1-e162-4cd8-93b2-082dd652b018"}
2026-07-07T20:11:32Z ERROR cleanup failed, will retry {"controller": "managedbgppeer", "controllerGroup": "bgp.network.redhat.com", "controllerKind": "ManagedBGPPeer", "ManagedBGPPeer": {"name":"azure-route-server"}, "namespace": "", "name": "azure-route-server", "reconcileID": "a0d20eb1-e162-4cd8-93b2-082dd652b018", "error": "list route server peerings: GET https://management.azure.com/subscriptions/c5545383-1a94-45fe-b501-7ebdf43e5d7a/resourceGroups/aro-thhubbar-rg/providers/Microsoft.Network/virtualHubs/virt_bgp_route_server/bgpConnections\n--------------------------------------------------------------------------------\nRESPONSE 404: 404 Not Found\nERROR CODE: ResourceNotFound\n--------------------------------------------------------------------------------\n{\n \"error\": {\n \"code\": \"ResourceNotFound\",\n \"message\": \"The Resource 'Microsoft.Network/virtualHubs/virt_bgp_route_server' under resource group 'aro-thhubbar-rg' was not found. For more details please go to https://aka.ms/ARMReso...
github.com/rh-mobb/managed-bgp-peer-operator/internal/controller.(*ManagedBGPPeerReconciler).handleDeletion
/workspace/internal/controller/managedbgppeer_controller.go:185
github.com/rh-mobb/managed-bgp-peer-operator/internal/controller.(*ManagedBGPPeerReconciler).Reconcile
/workspace/internal/controller/managedbgppeer_controller.go:76
sigs.k8s.io/controller-runtime/pkg/internal/controller.(*Controller[...]).Reconcile
/workspace/.cache/go-mod/sigs.k8s.io/controller-runtime@v0.21.0/pkg/internal/controller/controller.go:119
sigs.k8s.io/controller-runtime/pkg/internal/controller.(*Controller[...]).reconcileHandler
/workspace/.cache/go-mod/sigs.k8s.io/controller-runtime@v0.21.0/pkg/internal/controller/controller.go:340
sigs.k8s.io/controller-runtime/pkg/internal/controller.(*Controller[...]).processNextWorkItem
/workspace/.cache/go-mod/sigs.k8s.io/controller-runtime@v0.21.0/pkg/internal/controller/controller.go:300
sigs.k8s.io/controller-runtime/pkg/internal/controller.(*Controller[...]).Start.func2.1
/workspace/.cache/go-mod/sigs.k8s.io/controller-runtime@v0.21.0/pkg/internal/controller/controller.go:202
```

...this needs to be caught and reflect an actual status rather than just returning the raw API error.

