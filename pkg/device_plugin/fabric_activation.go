/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 *  * Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 *  * Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *  * Neither the name of NVIDIA CORPORATION nor the names of its
 *    contributors may be used to endorse or promote products derived
 *    from this software without specific prior written permission.
 *
 * THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS ``AS IS'' AND ANY
 * EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
 * PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT OWNER OR
 * CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
 * EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
 * PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
 * PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY
 * OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
 * (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
 * OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
 */

package device_plugin

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"

	"kubevirt-gpu-device-plugin/pkg/fabric"
)

// This file activates the NVLink fabric partition for a whole-card vGPU Virtual
// Function as part of the device plugin's Allocate lifecycle. On NVSwitch systems
// with Fabric Manager in FABRIC_MODE=2, a whole-card (NVLink-enabled) guest
// cannot initialise CUDA until its VF's fabric partition is activated through the
// FM SDK; otherwise cuInit fails with error 802. See
// docs/fabric-partition-activation.md and
// https://github.com/NVIDIA/kubevirt-gpu-device-plugin/issues/133.
//
// Model (validated on hardware):
//   - Fabric Manager's fmActivateFabricPartitionWithVFs takes ONE VF per GPU in
//     the partition. A vGPU VF's partition is the single-GPU partition of its
//     physical GPU, so activation always passes exactly one VF (numVfs=1).
//   - MIG-mode GPUs have NVLink disabled and are not in the fabric, so their VFs
//     need no activation and are skipped. Only whole-card (MIG-disabled) VFs are
//     activated. This assumes at most one whole-card vGPU per physical GPU (true
//     for typical HGX vGPU); time-sliced multiple whole-card vGPUs per NVLink GPU
//     would be a future extension.
//   - Activation happens in Allocate, before virt-launcher starts, so the fabric
//     is up from the guest's first CUDA init.

const (
	// envFabricActivation gates the feature: "auto" (default) enables activation
	// (a no-op on non-fabric systems); "off" disables it entirely.
	envFabricActivation = "FABRIC_PARTITION_ACTIVATION"
	// envFabricAddress overrides the Fabric Manager address (host:port TCP or a
	// Unix socket path). Defaults to fabric.DefaultTCPAddress.
	envFabricAddress = "FABRIC_MANAGER_ADDRESS"
	// envReconcileInterval sets the pod-resources reconcile period (a Go
	// duration, e.g. "60s"); "0" disables periodic reconciliation. Default 60s.
	envReconcileInterval = "FABRIC_RECONCILE_INTERVAL"
	// envFailMode selects what happens on a Fabric Manager error: "closed"
	// (default) fails the allocation; "open" logs and allows it. See failMode.
	envFailMode = "FABRIC_FAIL_MODE"
)

const defaultReconcileInterval = 60 * time.Second

// failMode decides how a Fabric Manager error (unreachable, query error, or
// activation failure) affects an allocation.
type failMode int

const (
	// failClosed fails the Allocate on any Fabric Manager error. No VM is handed
	// a GPU whose fabric partition could not be activated, so tenant isolation is
	// guaranteed — at the cost of blocking VM start on an FM outage. This is the
	// default. (Definitive non-fabric cases — not an SR-IOV VF, MIG-mode GPU,
	// FM_ST_NOT_SUPPORTED, no single-GPU partition — are still skipped, so the
	// plugin stays a no-op where activation does not apply.)
	failClosed failMode = iota
	// failOpen logs the error and allows the allocation. The VM starts and may
	// fail CUDA with error 802 until the fabric is up (a clear, recoverable
	// signal). Prefer for availability over guaranteed isolation.
	failOpen
)

func (m failMode) String() string {
	if m == failOpen {
		return "open"
	}
	return "closed"
}

// activateFabricForVF is called from Allocate. It defaults to a no-op so the
// plugin (and the existing Allocate tests) behave unchanged until fabric
// activation is enabled by initFabricActivation.
var activateFabricForVF = func(vfBDF string) error { return nil }

// fabricActivator activates whole-card vGPU fabric partitions and tracks, per
// partition, the VFs currently allocated to running pods so it can deactivate a
// partition once its VF is gone. Activation itself always uses a single VF; the
// tracked set exists only for the skip-if-active check and deactivate-on-empty.
type fabricActivator struct {
	mu sync.Mutex

	address  string
	failMode failMode

	reconcileInterval time.Duration

	// Injectable dependencies (overridden in tests).
	newClient    func(addr string) (fabric.Client, error)
	pfForVF      fabric.PFForVFFunc
	moduleID     fabric.ModuleIDFunc
	migMode      fabric.MigModeFunc
	podResources func() ([]allocatedDevice, error)

	client fabric.Client // lazily connected

	vfToPartition map[string]uint32              // VF BDF -> partition id (cached; topology is static)
	activeVFs     map[uint32]map[string]struct{} // partition id -> VFs allocated on it (for deactivate-on-empty)
	reconciled    bool                           // pod-resources reconcile has succeeded at least once
	unsupported   bool                           // FM definitively reports no fabric partitions
}

// newFabricActivator builds an activator from the environment, or returns
// (nil, false) when the feature is disabled.
func newFabricActivator() (*fabricActivator, bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envFabricActivation))) {
	case "off", "false", "0", "disable", "disabled":
		return nil, false
	}

	// Default to the Fabric Manager TCP command interface. The FM daemon serves
	// the nv_fm_agent command API on 127.0.0.1:6666 by default; its Unix socket
	// only serves that API when fabricmanager.cfg sets UNIX_SOCKET_PATH, so the
	// TCP endpoint is the more reliable default (reaching host loopback needs
	// hostNetwork on the pod). Override with FABRIC_MANAGER_ADDRESS.
	address := strings.TrimSpace(os.Getenv(envFabricAddress))
	if address == "" {
		address = fabric.DefaultTCPAddress
	}

	interval := defaultReconcileInterval
	if v := strings.TrimSpace(os.Getenv(envReconcileInterval)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			interval = d
		} else {
			log.Printf("fabric: invalid %s=%q, using default %s", envReconcileInterval, v, defaultReconcileInterval)
		}
	}

	fm := failClosed
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envFailMode))) {
	case "open", "fail-open":
		fm = failOpen
	case "", "closed", "fail-closed":
		fm = failClosed
	default:
		log.Printf("fabric: unknown %s=%q, using default %s", envFailMode, os.Getenv(envFailMode), failClosed)
	}

	nvmllib := nvml.New()
	return &fabricActivator{
		address:           address,
		failMode:          fm,
		reconcileInterval: interval,
		newClient:         fabric.New,
		pfForVF:           func(vfBDF string) (string, error) { return readLink(basePath, vfBDF, physfnLink) },
		moduleID:          fabric.ModuleIDViaNVML(nvmllib),
		migMode:           fabric.MigEnabledViaNVML(nvmllib),
		podResources:      listAllocatedDevicesViaPodResources,

		vfToPartition: map[string]uint32{},
		activeVFs:     map[uint32]map[string]struct{}{},
	}, true
}

// initFabricActivation wires the activator into the Allocate seam and starts the
// pod-resources reconcile loop. Call once during plugin start, after discovery.
func initFabricActivation() {
	a, ok := newFabricActivator()
	if !ok {
		log.Printf("fabric: partition activation disabled (%s=off)", envFabricActivation)
		return
	}
	activateFabricForVF = a.Activate
	log.Printf("fabric: partition activation enabled (address=%s, reconcile=%s, failMode=%s)",
		a.address, a.reconcileInterval, a.failMode)

	if a.reconcileInterval > 0 {
		go a.reconcileLoop(stop)
	}
}

// ensureClient lazily connects to Fabric Manager. Connection is deferred to the
// first use so plugin start does not depend on FM being up yet.
func (a *fabricActivator) ensureClient() (fabric.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	c, err := a.newClient(a.address)
	if err != nil {
		return nil, err
	}
	a.client = c
	return c, nil
}

// Activate ensures the fabric partition of the VF's physical GPU is active for
// this VF. It is a no-op for non-SR-IOV devices (classic passthrough GPUs have
// no physfn), for MIG-mode GPUs (NVLink off, not in the fabric), and for
// non-fabric systems.
func (a *fabricActivator) Activate(vfBDF string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.unsupported {
		return nil
	}

	// Classic passthrough GPUs (whole physical functions) have no physfn; only
	// SR-IOV VFs do. A missing physfn means this is not a vGPU VF.
	pf, err := a.pfForVF(vfBDF)
	if err != nil {
		log.Printf("fabric: %s has no parent PF (not an SR-IOV VF); skipping fabric activation", vfBDF)
		return nil
	}

	// MIG-mode GPUs have NVLink disabled and are not part of the NVSwitch fabric,
	// so their VFs need no partition activation. Only whole-card VFs proceed. If
	// the MIG mode cannot be determined, proceed rather than risk skipping a
	// whole-card VF (a single-VF activation is harmless on a MIG GPU anyway).
	if mig, err := a.migMode(pf); err != nil {
		log.Printf("fabric: could not determine MIG mode of PF %s (%v); proceeding with activation", pf, err)
	} else if mig {
		log.Printf("fabric: skipping MIG-mode GPU (PF %s) for VF %s; MIG has NVLink off, no fabric partition needed", pf, vfBDF)
		return nil
	}

	client, err := a.ensureClient()
	if err != nil {
		return a.onFMError(vfBDF, "connecting to Fabric Manager", err)
	}

	// Fetch the current partition list once: it drives partition resolution and
	// the real isActive check (FM reports each partition's active state).
	partitions, err := client.GetSupportedPartitions()
	if err != nil {
		if errors.Is(err, fabric.ErrFabricNotSupported) {
			a.unsupported = true
			log.Printf("fabric: Fabric Manager reports no fabric partitions (not NVSwitch / not FABRIC_MODE=2); disabling activation")
			return nil
		}
		return a.onFMError(vfBDF, "listing fabric partitions", err)
	}

	// Keep the tracked set fresh (departed VMs pruned, empty partitions
	// deactivated) at least once before we consult it.
	if !a.reconciled {
		if rerr := a.reconcile(client, partitions); rerr == nil {
			a.reconciled = true
		} else {
			log.Printf("fabric: pod-resources reconcile not yet available (%v); proceeding", rerr)
		}
	}

	partitionID, err := a.resolvePartitionID(partitions, vfBDF, pf)
	if err != nil {
		if errors.Is(err, fabric.ErrPartitionNotFound) {
			log.Printf("fabric: no single-GPU partition for VF %s (PF %s); skipping: %v", vfBDF, pf, err)
			return nil
		}
		return fmt.Errorf("fabric: resolving partition for VF %s: %w", vfBDF, err)
	}

	set := a.activeVFs[partitionID]
	if set == nil {
		set = map[string]struct{}{}
		a.activeVFs[partitionID] = set
	}
	_, alreadyCovered := set[vfBDF]

	// Steady state: the partition is genuinely active and already tracks this VF
	// (e.g. kubelet re-running Allocate after a VM restart). No FM call needed.
	if alreadyCovered && a.partitionActive(partitions, partitionID) {
		log.Printf("fabric: partition %d already active and covers VF %s; skipping activation (steady state)", partitionID, vfBDF)
		return nil
	}
	set[vfBDF] = struct{}{}

	// Activate with EXACTLY this VF. fmActivateFabricPartitionWithVFs takes one
	// VF per GPU; a single-GPU partition takes one VF. numVfs is always 1.
	vf, err := fabric.ParseBDF(vfBDF)
	if err != nil {
		return fmt.Errorf("fabric: parsing VF address %q: %w", vfBDF, err)
	}
	vfList := []fabric.BDF{vf}
	log.Printf("fabric: activating partition %d for VF %s (vfList=[%s], numVfs=%d)", partitionID, vfBDF, vf, len(vfList))
	if err := a.ensurePartitionActive(client, partitionID, vfList, !alreadyCovered); err != nil {
		return a.onFMError(vfBDF, fmt.Sprintf("activating partition %d", partitionID), err)
	}
	log.Printf("fabric: partition %d active for VF %s (1 VF(s))", partitionID, vfBDF)
	return nil
}

// onFMError applies the configured failMode to a Fabric Manager error: fail-closed
// returns a wrapped error (fails the allocation); fail-open logs a warning and
// returns nil (allows the allocation, guest may be CUDA 802 until the fabric is
// up). op describes the failed operation, e.g. "activating partition 14".
func (a *fabricActivator) onFMError(vfBDF, op string, err error) error {
	if a.failMode == failClosed {
		return fmt.Errorf("fabric: %s for VF %s: %w", op, vfBDF, err)
	}
	log.Printf("fabric: WARNING: %s for VF %s: %v; allowing the allocation "+
		"(fail-open; guest may fail CUDA with error 802 until the fabric partition is up)", op, vfBDF, err)
	return nil
}

// resolvePartitionID maps a VF to its single-GPU fabric partition id from a
// supplied partition list, caching the result (VF -> PF -> FM physicalId/NVML
// module id -> single-GPU partition).
func (a *fabricActivator) resolvePartitionID(partitions []fabric.Partition, vfBDF, pf string) (uint32, error) {
	if id, ok := a.vfToPartition[vfBDF]; ok {
		return id, nil
	}
	moduleID, err := a.moduleID(pf)
	if err != nil {
		return 0, fmt.Errorf("resolving module id of PF %s: %w", pf, err)
	}
	part, err := fabric.SingleGPUPartitionForModuleID(moduleID, partitions)
	if err != nil {
		return 0, err
	}
	a.vfToPartition[vfBDF] = part.ID
	return part.ID, nil
}

// partitionActive reports whether the partition with the given id is reported as
// active in the supplied Fabric Manager partition list.
func (a *fabricActivator) partitionActive(partitions []fabric.Partition, partitionID uint32) bool {
	for i := range partitions {
		if partitions[i].ID == partitionID {
			return partitions[i].Active
		}
	}
	return false
}

// ensurePartitionActive activates partitionID for vfSet (a single VF).
// It is idempotent: if the partition is already active for a DIFFERENT VF (e.g.
// the whole-card VM restarted with a new VF) and this is a new VF, it
// deactivates and reactivates with the new VF.
func (a *fabricActivator) ensurePartitionActive(client fabric.Client, partitionID uint32, vfSet []fabric.BDF, isNewVF bool) error {
	err := client.ActivateWithVFs(partitionID, vfSet)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fabric.ErrInUse) {
		return err
	}
	// Already active. If this VF was already covered, nothing to do.
	if !isNewVF {
		return nil
	}
	// The partition is active for a stale VF (whole-card VM restarted with a new
	// VF); the previous VM is gone, so deactivate and reactivate for the new VF.
	log.Printf("fabric: partition %d already active for a different VF; reactivating for the current VF", partitionID)
	if derr := client.Deactivate(partitionID); derr != nil && !errors.Is(derr, fabric.ErrPartitionNotActive) {
		return derr
	}
	return client.ActivateWithVFs(partitionID, vfSet)
}

// reconcileLoop periodically prunes departed VFs and deactivates partitions
// whose VMs have all gone, using the kubelet pod-resources API. This is the
// (conservative) driver for deactivation, since the device plugin API has no
// release hook.
func (a *fabricActivator) reconcileLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(a.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			a.Reconcile()
		}
	}
}

// Reconcile runs one pod-resources reconciliation pass under the lock.
func (a *fabricActivator) Reconcile() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsupported {
		return
	}
	client, err := a.ensureClient()
	if err != nil {
		log.Printf("fabric: reconcile skipped, Fabric Manager unreachable: %v", err)
		return
	}
	partitions, err := client.GetSupportedPartitions()
	if err != nil {
		if errors.Is(err, fabric.ErrFabricNotSupported) {
			a.unsupported = true
		}
		log.Printf("fabric: reconcile skipped, cannot list partitions: %v", err)
		return
	}
	if err := a.reconcile(client, partitions); err == nil {
		a.reconciled = true
	}
}

// reconcile updates the tracked per-partition VF set from the VFs the kubelet
// pod-resources API reports as allocated to running pods, and deactivates any
// partition left with no VFs (its VM departed). MIG VFs and VFs without a
// single-GPU partition are ignored (they are never activated). Must be called
// with the lock held.
func (a *fabricActivator) reconcile(client fabric.Client, partitions []fabric.Partition) error {
	devices, err := a.podResources()
	if err != nil {
		return err
	}

	// Desired per-partition VF set from currently running pods (whole-card VFs
	// only; MIG VFs are not activated so are irrelevant to deactivation).
	desired := map[uint32]map[string]struct{}{}
	for _, dev := range devices {
		if !isNvidiaResource(dev.resourceName) {
			continue
		}
		pf, err := a.pfForVF(dev.deviceID)
		if err != nil {
			continue // not an SR-IOV VF
		}
		if mig, err := a.migMode(pf); err == nil && mig {
			continue // MIG GPU: never activated, so not tracked for deactivation
		}
		partitionID, err := a.resolvePartitionID(partitions, dev.deviceID, pf)
		if err != nil {
			continue // no single-GPU partition
		}
		if desired[partitionID] == nil {
			desired[partitionID] = map[string]struct{}{}
		}
		desired[partitionID][dev.deviceID] = struct{}{}
	}

	// Adopt currently-allocated VFs into the tracked state without activating.
	for partitionID, set := range desired {
		if a.activeVFs[partitionID] == nil {
			a.activeVFs[partitionID] = map[string]struct{}{}
		}
		for vf := range set {
			a.activeVFs[partitionID][vf] = struct{}{}
		}
	}

	// Prune departed VFs; deactivate a partition once it has none left.
	for partitionID, set := range a.activeVFs {
		for vf := range set {
			if _, ok := desired[partitionID][vf]; !ok {
				delete(set, vf)
			}
		}
		if len(set) == 0 {
			delete(a.activeVFs, partitionID)
			if derr := client.Deactivate(partitionID); derr != nil && !errors.Is(derr, fabric.ErrPartitionNotActive) {
				log.Printf("fabric: reconcile could not deactivate empty partition %d: %v", partitionID, derr)
			} else {
				log.Printf("fabric: reconcile deactivated empty partition %d (no VFs remain)", partitionID)
			}
		}
	}
	return nil
}
