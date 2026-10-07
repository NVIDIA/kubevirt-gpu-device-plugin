# NVLink fabric partition activation for vGPU VFs

## Problem

On NVSwitch systems (HGX H100/H200 and similar), the GPU NVLink fabric is brought up by the NVIDIA Fabric Manager (FM) daemon. When FM runs in `FABRIC_MODE=2` (SR-IOV vGPU multitenancy), bringing the fabric up host-side is not enough: each guest VM's Virtual Function (VF) must additionally have its fabric **partition** activated through the FM SDK before the guest can initialise CUDA. Until that happens the guest sees the GPU but `cuInit` fails with CUDA error `802` — "system not yet initialized".

Nothing in the KubeVirt GPU stack does this today. This is the open gap tracked upstream as [NVIDIA/kubevirt-gpu-device-plugin#133](https://github.com/NVIDIA/kubevirt-gpu-device-plugin/issues/133).

The fix was proven manually: with FM in `FABRIC_MODE=2`, calling `fmActivateFabricPartitionWithVFs(partitionId, vfList)` for a VM's VF makes guest CUDA work (`cuInit` returns `0`) live, with no guest reboot. The durable home for this is the device plugin's allocation lifecycle: `Allocate()` is the exact moment a VF is handed to a `virt-launcher` pod, and it knows the VF ↔ VM binding. On release the partition should be deactivated.

## Integration points in this plugin

The vendor-specific VFIO vGPU VFs (Ada/Hopper+ SR-IOV, mdev-less) are discovered by `createVfioVGpuMap` (`pkg/device_plugin/vfio_vgpu.go`) and — importantly — advertised and allocated through **`GenericDevicePlugin`**, the same code path as classic GPU passthrough, not through `GenericVGpuDevicePlugin` (which is the mdev path). This is set up in `createDevicePlugins` (`pkg/device_plugin/device_plugin.go`), where each vGPU profile in `vfioVGpuMap` becomes a `NewGenericDevicePlugin(profileName, ...)`.

The consequence for this feature:

- The activation hook is `GenericDevicePlugin.Allocate` (`pkg/device_plugin/generic_device_plugin.go`). Inside the `for _, bdf := range req.DevicesIds` loop, `bdf` **is the VF PCI BDF** (e.g. `0000:41:00.4`). So the VF address we need for `fmActivateFabricPartitionWithVFs` is available directly at allocation time — no extra lookup to recover it.
- The VF → parent Physical Function (PF) mapping is already read during discovery via each VF's `physfn` sysfs symlink (`vfio_vgpu.go`), and NVML is already used at discovery time (`resolveVgpuTypeNamesViaNVML`). So both inputs the resolver needs (PF address, NVML) are established patterns in this codebase.
- There is **no release/deallocate callback** in the Kubernetes device plugin API. Kubelet calls `Allocate` when a container is created; it never calls the plugin back when the container/pod goes away. This shapes the deactivation design (below).

### What the plugin does not already provide

- `Allocate` does not initialise NVML today (only discovery and the health check do). Resolving VF → FM `physicalId` needs NVML, so either the VF → partition map is computed once at discovery/startup and cached, or NVML is initialised on demand in the activation path. NVML `Init`/`Shutdown` are reference-counted, so an on-demand `Init`+`Shutdown` around the lookup composes safely with any NVML already open.
- `fmGetSupportedFabricPartitions` reports each partition's **active state and its GPUs**, but not the **VF** currently bound to an active partition. Since activation is single-VF and idempotent per `Allocate`, this does not matter for activation; the pod-resources reconciler covers deactivation.

## `pkg/fabric` — the FM SDK binding

`pkg/fabric` wraps the FM SDK (`libnvfm.so.1`, header API `nv_fm_agent`) through the official Go bindings, [`github.com/NVIDIA/go-nvfm`](https://github.com/NVIDIA/go-nvfm). The public surface:

```go
type Client interface {
    GetSupportedPartitions() ([]Partition, error)
    ActivateWithVFs(partitionID uint32, vfs []BDF) error
    Deactivate(partitionID uint32) error
    Close() error
}

func New(addr string) (Client, error) // fmLibInit + fmConnect
```

`New` maps to `nvfm.Init` + `nvfm.Connect`; `Close` maps to `handle.Disconnect` + `nvfm.Shutdown`. The proven call sequence (`fmLibInit` → `fmConnect` → `fmGetSupportedFabricPartitions` → `fmActivateFabricPartitionWithVFs` → `fmDeactivateFabricPartition` → `fmDisconnect` → `fmLibShutdown`) is expressed by this interface, with go-nvfm supplying each underlying call. `addr` is either the Unix socket (`DefaultUnixSocket = /var/run/nvidia-fabricmanager/socket`) or the TCP command interface (`DefaultTCPAddress = 127.0.0.1:6666`).

### Struct-version handling

FM's SDK structs are versioned: each has a leading `.version` field set to `MAKE_FM_PARAM_VERSION(type, ver) = sizeof(type) | (ver << 24)`, and FM rejects a mismatched struct with `FM_ST_VERSION_MISMATCH`. go-nvfm computes those versions from the exact structs its bindings compiled against (its `STRUCT_VERSION` reflection helper), never hardcoded, and initialises the `.version` field for us — it uses `fmConnectParams_t` (version 2) and `fmFabricPartitionList_t` (version 1), matching the SDK.

### Return-code → error mapping

Every `fmReturn_t` code from `nv_fm_types.h` is transcribed into the `Return` type with a message table. `Client` methods return an `*Error{Op, Code}` that unwraps to a sentinel for the codes the allocation logic branches on:

- `ErrInUse` (`FM_ST_IN_USE`, `FM_ST_PARTITION_ID_IN_USE`, ...) — partition already active; drives idempotent (re)activation.
- `ErrPartitionNotActive` (`FM_ST_UNINITIALIZED` from deactivate, `FM_ST_PARTITION_ID_NOT_IN_USE`) — deactivate of an inactive partition; makes deactivation idempotent.
- `ErrNotReadyYet` (`FM_ST_NOT_CONFIGURED`, `FM_ST_NOT_READY`) — FM still initialising; retry.
- `ErrFabricNotSupported` (`FM_ST_NOT_SUPPORTED`) — not an NVSwitch/`FABRIC_MODE=2` system; skip activation, don't fail hard.

### Why cgo (vs subprocess vs hand-rolled socket)

Options considered for talking to FM:

1. **cgo against `libnvfm` via the official go-nvfm bindings (chosen).** Direct, in-process, uses the same ABI NVIDIA ships and versions. `github.com/NVIDIA/go-nvfm` `dlopen`s `libnvfm.so` at runtime (as the vendored `go-nvml` does for `libnvidia-ml`) rather than linking it, and ships its own copy of the FM SDK headers, so NVIDIA's proprietary headers are not vendored into this repository. Cost: the image must ship a `libnvfm.so.1` whose ABI is compatible with the host FM daemon, and cgo must be enabled in the build (it already is). We point go-nvfm at the versioned SONAME `libnvfm.so.1` (the runtime image ships only that, not the unversioned `-dev` symlink).
2. **Subprocess `nvidia-smi` / a helper binary.** `nvidia-smi` can list and activate partitions, but shelling out per allocation is slower, has a coarse text/return-code interface, needs the tool present in a distroless image, and still couples to an NVIDIA binary's CLI contract. It buys nothing over cgo while being harder to error-handle precisely.
3. **Hand-rolled socket protocol to `127.0.0.1:6666` / the Unix socket.** FM's wire protocol is an internal, undocumented, unstable protobuf/RPC. Reimplementing it would be a large, fragile surface that breaks on any FM release. Rejected.

### Version coupling with the daemon

`libnvfm` and the FM daemon are version-coupled by the struct-version scheme. The public `nvidia-fabricmanager-dev` package (`580.126.20`) is ABI-compatible with a host running FM `580.159.01` — verified: `fmConnect` + read + write all work. Newer FM daemons should stay compatible as long as the struct versions we use are still accepted; a mismatch surfaces as `FM_ST_VERSION_MISMATCH` from `fmConnect`/`fmGetSupportedFabricPartitions`, which the error mapping reports clearly. The bundled `libnvfm.so.1` should be kept in the same major-version ballpark as the deployed driver/FM.

### Portability / build tags

The real binding (`fabric_cgo.go`) is `//go:build linux && cgo`. A portable stub (`fabric_stub.go`, `//go:build !(linux && cgo)`) returns `ErrUnsupported` so the whole module builds and unit-tests on developer machines and under `CGO_ENABLED=0`. The production image builds `CGO_ENABLED=1` on linux and gets the real binding. The pure resolver, BDF parsing, and error mapping have no cgo and are unit-tested everywhere.

## VF → partition resolver

A vGPU VF's fabric partition is the **single-GPU** partition of the VF's physical GPU. Resolution walks:

```text
VF BDF --(physfn sysfs)--> PF BDF --(NVML module id)--> FM physicalId --(partition list)--> single-GPU partitionId
```

The critical correctness point: **FM `physicalId` (== NVML module id) is not the PCI enumeration order.** On this 8-GPU box the supported partitions are `P0` (all 8), `P1`/`P2` (4-GPU), `P3`–`P6` (2-GPU), and `P7`–`P14` (single-GPU, `physicalId` 1..8), and the single-GPU partition index does **not** track PCI order. So resolution matches strictly on `physicalId`, never on list position or PCI bus order. `SingleGPUPartitionForModuleID` skips multi-GPU partitions, errors if no single-GPU partition has the id (`ErrPartitionNotFound`), and errors if two do (inconsistent list). The NVML module id comes from `nvmlDeviceGetModuleId` on the PF handle (`DeviceGetHandleByPciBusId`), which the FM types header documents as equal to `physicalId`.

The resolver takes injected `PFForVFFunc` and `ModuleIDFunc` so it is fully table-testable with fakes; the NVML-backed `ModuleIDFunc` is provided by `ModuleIDViaNVML`.

## Lifecycle design

### Activation (on `Allocate`)

`activateFabricForVF` runs for each `bdf` (VF) in a container request, inside `GenericDevicePlugin.Allocate`, after the device is validated and before the response is returned to kubelet — so the fabric partition is active before `virt-launcher` starts and the guest's first CUDA init succeeds. For a VF on physical GPU G:

```text
if not an SR-IOV VF (no physfn):          return   # classic passthrough
if G is MIG-mode (nvmlDeviceGetMigMode):  return   # MIG has NVLink off, no fabric partition
resolve G's single-GPU partition P
if P.isActive (from GetSupportedPartitions) and VF already tracked on P: return   # steady state
ActivateWithVFs(P, [VF])                  # exactly one VF; numVfs = 1
```

Two hardware facts, validated on a live NVSwitch host, shape this:

1. **`fmActivateFabricPartitionWithVFs` takes one VF per GPU in the partition.** A vGPU VF's partition is the single-GPU partition of its physical GPU, so activation always passes **exactly one VF** — the VF being allocated (`numVfs = 1`). Passing more than one (an earlier "union" of all VFs sharing the GPU) is rejected with `FM_ST_BADPARAM`.
2. **MIG-mode GPUs are not in the fabric.** MIG disables NVLink, so a MIG guest inits CUDA standalone (cuInit = 0) with the partition inactive. MIG VFs therefore need no activation and are skipped (detected with `nvmlDeviceGetMigMode` on the parent PF). Only whole-card (MIG-disabled) VFs are activated.

This assumes at most one whole-card vGPU per physical GPU — true for typical HGX vGPU (a whole-card profile consumes the whole GPU). Time-slicing multiple whole-card vGPUs onto one NVLink GPU would be a future extension; it does not occur here.

The `isActive` skip avoids re-activating an already-active partition on a plain re-`Allocate` (VM restart with the same VF). If the whole-card VM restarts with a **different** VF, the partition is active for the stale VF; `ensurePartitionActive` handles that by deactivating and reactivating for the current VF (the previous VM is gone, so there is no co-tenant to disturb).

### Deactivation (pod-resources reconciler)

The device plugin API has no release callback, so deactivation is driven conservatively by a periodic reconciler (`FABRIC_RECONCILE_INTERVAL`, default 60s) over the kubelet **pod-resources API** (`/var/lib/kubelet/pod-resources/kubelet.sock`, `List()`): it tracks which whole-card VFs are allocated to running pods, and once a partition has none left (its VM departed) it is deactivated. Partitions are never deactivated eagerly, and a stale VF lingering in the tracked set is harmless because activation only ever uses the single VF being allocated, never the set.

### FM connection lifecycle and endpoint

A **single long-lived `Client`** is created lazily on the first `Allocate` that needs it (so plugin startup does not depend on FM being up), cached for the process lifetime, and guarded by a mutex (`Allocate` runs concurrently across resources). If `New`/`fmConnect` fails in `auto` mode it is treated as "FM not up yet" and skipped for that allocation without caching, so a later allocation retries.

**Endpoint.** The FM daemon serves the `nv_fm_agent` command API on the TCP loopback interface (`127.0.0.1:6666`) by default; it only serves it on a Unix socket when `fabricmanager.cfg` sets `UNIX_SOCKET_PATH`. Connecting to the daemon's other (internal) Unix socket *succeeds* at `fmConnect` but the first `fmGetSupportedFabricPartitions` then returns `FM_ST_UNINITIALIZED`. So `FABRIC_MANAGER_ADDRESS` defaults to `127.0.0.1:6666`, and the pod needs **`hostNetwork: true`** to reach the host loopback (or a configured FM Unix socket mounted into the pod).

### Error handling — `FABRIC_FAIL_MODE`

What happens on a Fabric Manager error (unreachable, `fmGetSupportedFabricPartitions` error, or an activation failure) is controlled by `FABRIC_FAIL_MODE`:

- **`closed` (default)** — fail the `Allocate`. No VM is handed a GPU whose fabric partition could not be activated, so tenant isolation is guaranteed and activation failures are loud (the VM will not start). The cost is that an FM outage blocks VM start.
- **`open`** — log a warning and allow the allocation. The VM starts and may fail CUDA with `802` until the fabric is up (a clear, recoverable in-guest signal). Trades isolation for availability.

Either way, definitive **non-fabric** cases are never failed — they are simply skipped so the plugin is a no-op where activation does not apply: not an SR-IOV VF (no `physfn`), `FM_ST_NOT_SUPPORTED` (not NVSwitch / not `FABRIC_MODE=2`), or no single-GPU partition for the VF's GPU.

### Configuration

| Env var | Values | Default | Meaning |
| --- | --- | --- | --- |
| `FABRIC_PARTITION_ACTIVATION` | `auto` / `off` | `auto` | `auto`: enable activation (a no-op on non-fabric systems). `off`: disable entirely. |
| `FABRIC_FAIL_MODE` | `closed` / `open` | `closed` | `closed`: an FM error fails the `Allocate` (isolation guaranteed, loud failures). `open`: log and allow the allocation (availability over isolation; guest may be 802). |
| `FABRIC_MANAGER_ADDRESS` | `host:port` or unix path | `127.0.0.1:6666` | Fabric Manager command API endpoint. TCP needs `hostNetwork`; a unix path needs the socket mounted and FM configured (`UNIX_SOCKET_PATH`) to serve the agent API on it. |
| `FABRIC_RECONCILE_INTERVAL` | Go duration, `0` to disable | `60s` | Period of the pod-resources reconciler that deactivates a partition once its VF is gone. |

## Image / `libnvfm` bundling

The plugin builds `CGO_ENABLED=1` (already the case in `deployments/container/Dockerfile.distroless`); cgo is required to compile the go-nvfm bindings. go-nvfm `dlopen`s `libnvfm.so.1` at runtime, so the binary does not link it (`--unresolved-symbols=ignore-in-object-files`) — only the runtime image needs it on the loader path. The Dockerfile fetches a **pinned** `libnvfm.so.1` from the CUDA repository package at build time (`ARG FABRIC_MANAGER_VERSION`) and copies it into the distroless runtime image. Only the versioned shared object is needed — go-nvfm vendors the FM SDK headers, so NVIDIA's proprietary headers are neither required nor vendored into this repository.

**Version coupling.** `libnvfm` and the FM daemon are coupled by the struct-version scheme, so `FABRIC_MANAGER_VERSION` should match the deployed daemon's major version (a mismatch surfaces as `FM_ST_VERSION_MISMATCH` from `fmConnect`/`fmGetSupportedFabricPartitions`). The default is pinned to a version verified against the target host's daemon.

## Runtime requirements (DaemonSet)

For the plugin pod to activate partitions, the deployment must:

- Set **`hostNetwork: true`** so the plugin can reach the FM command API on `127.0.0.1:6666` (default `FABRIC_MANAGER_ADDRESS`). Alternatively, if FM is configured with `UNIX_SOCKET_PATH`, mount that socket and point `FABRIC_MANAGER_ADDRESS` at it instead of using `hostNetwork`.
- Mount the kubelet pod-resources socket directory, `hostPath: /var/lib/kubelet/pod-resources` → same path, so the reconciler can track allocated VFs and deactivate a partition once its VM departs.
- Run on nodes where FM is up in `FABRIC_MODE=2`.

## Host prerequisite: `FABRIC_MODE=2`

This feature only applies when FM runs in shared/vGPU multitenancy mode (`FABRIC_MODE=2`, `fabric-manager` configured for SR-IOV vGPU). On such a host FM exposes the supported fabric partitions and honours `fmActivateFabricPartitionWithVFs`. On a non-fabric or non-`FABRIC_MODE=2` host, `fmGetSupportedFabricPartitions`/activation report `FM_ST_NOT_SUPPORTED`, which the plugin treats as "no activation needed".

## MIG handling

MIG-mode GPUs disable NVLink and are not part of the NVSwitch fabric, so a MIG guest initialises CUDA standalone (`cuInit` = 0) with the partition inactive — validated on hardware. MIG VFs therefore need no fabric partition activation and are skipped (`nvmlDeviceGetMigMode` on the parent PF). Only whole-card (MIG-disabled) VFs are activated. This also means there is no multi-VF-per-partition case in practice: each single-GPU partition is activated for exactly one whole-card VF (`numVfs = 1`), matching what `fmActivateFabricPartitionWithVFs` expects (one VF per GPU). An earlier design that passed the union of all VFs sharing a GPU was rejected by Fabric Manager with `FM_ST_BADPARAM`; single-VF activation is the correct model.

## Status

- `pkg/fabric` binding (go-nvfm-backed cgo + stub), return-code mapping, BDF parsing, address-type helper, and the VF→partition resolver — implemented and unit-tested; the cgo binding is verified to compile against go-nvfm under linux + cgo.
- `Allocate` activation into `GenericDevicePlugin` (single-VF activation, MIG-skip, `isActive` steady-state skip, fail-mode) and the pod-resources reconciler for deactivation — implemented and unit-tested with a fake Fabric Manager client.
