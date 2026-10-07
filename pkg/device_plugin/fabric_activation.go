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
	"sort"
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
//     the partition. A single whole card resolves to the single-GPU partition of
//     its physical GPU and is activated with exactly one VF (numVfs=1); several
//     whole cards allocated in one container request resolve to the single
//     multi-GPU partition spanning them and are activated with one VF per member
//     GPU (numVfs=N), in the partition's GPU order.
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

// activateFabricForVFs is called once per container request from Allocate with
// the whole set of VF BDFs in that request. It defaults to a no-op so the plugin
// (and the existing Allocate tests) behave unchanged until fabric activation is
// enabled by initFabricActivation. A single-VF request keeps the exact single-GPU
// activation behavior; a multi-VF request activates one multi-GPU partition
// spanning all of the request's whole cards (NVLink peer-to-peer between them).
var activateFabricForVFs = func(vfBDFs []string) error { return nil }

// preferredFabricVFSet is consulted by GetPreferredAllocation. Given the VF BDFs
// kubelet offers, the size it wants, and any it must include, it returns a subset
// whose GPUs form a defined fabric partition (so the resulting VM gets NVLink
// P2P), or (nil, false) to let the caller fall back to its default preference.
// It defaults to declining so preferred allocation is unchanged until fabric
// activation is enabled.
var preferredFabricVFSet = func(available, mustInclude []string, size int) ([]string, bool) { return nil, false }

// fabricActivator activates whole-card vGPU fabric partitions and tracks, per
// partition, the VFs currently allocated to running pods so it can deactivate a
// partition once all of its VFs are gone. A single-GPU partition is activated
// with one VF; a multi-GPU partition spanning N whole cards is activated with
// one VF per member GPU, in the partition's GPU order.
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
	podResources func() ([][]allocatedDevice, error)

	client fabric.Client // lazily connected

	vfToPartition map[string]uint32              // VF BDF -> single-GPU partition id (cached; topology is static)
	pfToModule    map[string]uint32              // PF BDF -> FM physicalId (cached; used by the multi-GPU + preference paths)
	topology      []fabric.Partition             // cached partition topology (GPU membership is static; for the preference path)
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
		podResources:      listAllocatedDeviceSetsViaPodResources,

		vfToPartition: map[string]uint32{},
		pfToModule:    map[string]uint32{},
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
	activateFabricForVFs = a.ActivateSet
	preferredFabricVFSet = a.PreferredVFSet
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
	return a.activateOneLocked(vfBDF)
}

// activateOneLocked is the single-VF activation body: it resolves the VF's
// single-GPU fabric partition and activates it with exactly that one VF
// (numVfs=1). The caller must hold a.mu. This path is unchanged from the
// single-VF design and is what a one-device Allocate request drives.
func (a *fabricActivator) activateOneLocked(vfBDF string) error {
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

// resolvedVF is a whole-card VF paired with its parent PF and FM physicalId
// (NVML module id). Used by the multi-GPU activation and reconcile paths.
type resolvedVF struct {
	vf       string
	pf       string
	moduleID uint32
}

// ActivateSet activates the fabric partition(s) for one container request's whole
// set of VF BDFs. It is the seam Allocate calls once per request. A one-VF
// request is byte-identical to Activate (the single-GPU path). A request whose
// whole cards number N>1 is activated as a single multi-GPU partition spanning
// exactly those cards, so the guest gets NVLink peer-to-peer between them.
func (a *fabricActivator) ActivateSet(vfBDFs []string) error {
	switch len(vfBDFs) {
	case 0:
		return nil
	case 1:
		// Preserve the exact single-VF path, including its logging and its
		// unsupported / non-SR-IOV / MIG / partition-not-found skips.
		return a.Activate(vfBDFs[0])
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.activateMultiLocked(vfBDFs)
}

// activateMultiLocked handles an Allocate request carrying more than one VF. It
// filters the request to its whole-card VFs (skipping non-SR-IOV devices and
// MIG-mode GPUs, exactly like the single path), then:
//   - 0 whole cards: nothing to do.
//   - 1 whole card: defer to the single-GPU path.
//   - N>1 whole cards forming a defined partition: activate that one partition
//     with one VF per member GPU (NVLink P2P between the cards).
//   - N>1 whole cards with no matching partition: fall back per FABRIC_FAIL_MODE.
//
// The caller must hold a.mu.
func (a *fabricActivator) activateMultiLocked(vfBDFs []string) error {
	if a.unsupported {
		return nil
	}

	// Filter to whole-card VFs first (physfn + MIG checks only, no Fabric Manager
	// contact), so a request that is entirely MIG / passthrough is decided without
	// connecting to FM — matching the single-VF path, which skips those before it
	// ever connects. This keeps such requests a no-op even when FM is down.
	whole := a.filterWholeCardVFs(vfBDFs)
	switch len(whole) {
	case 0:
		// All request devices were MIG / passthrough: nothing to activate.
		return nil
	case 1:
		// After filtering, only one whole card remains: single-GPU activation.
		return a.activateOneLocked(whole[0].vf)
	}

	client, err := a.ensureClient()
	if err != nil {
		return a.onFMErrorSet(vfBDFs, "connecting to Fabric Manager", err)
	}

	partitions, err := client.GetSupportedPartitions()
	if err != nil {
		if errors.Is(err, fabric.ErrFabricNotSupported) {
			a.unsupported = true
			log.Printf("fabric: Fabric Manager reports no fabric partitions (not NVSwitch / not FABRIC_MODE=2); disabling activation")
			return nil
		}
		return a.onFMErrorSet(vfBDFs, "listing fabric partitions", err)
	}

	if !a.reconciled {
		if rerr := a.reconcile(client, partitions); rerr == nil {
			a.reconciled = true
		} else {
			log.Printf("fabric: pod-resources reconcile not yet available (%v); proceeding", rerr)
		}
	}

	// Resolve each whole card's FM physicalId (NVML). A resolution failure is a
	// hard error, as in the single-VF path.
	moduleIDs := make([]uint32, len(whole))
	for i := range whole {
		modID, err := a.moduleIDCached(whole[i].pf)
		if err != nil {
			return fmt.Errorf("fabric: resolving module id of PF %s (VF %s): %w", whole[i].pf, whole[i].vf, err)
		}
		whole[i].moduleID = modID
		moduleIDs[i] = modID
	}

	partition, err := fabric.PartitionForModuleIDs(moduleIDs, partitions)
	if err != nil || len(partition.GPUs) != len(whole) {
		// No defined partition spans exactly this set (or two VFs share one GPU,
		// so it cannot be one-VF-per-GPU): fall back per fail mode.
		return a.fallbackNoMatchLocked(whole, moduleIDs, partitions, err)
	}

	// Order the VFs so vfList[i] corresponds to partition.GPUs[i], as
	// fmActivateFabricPartitionWithVFs requires (one VF per physical GPU, in the
	// partition's GPU order).
	byModule := make(map[uint32]string, len(whole))
	for _, r := range whole {
		byModule[r.moduleID] = r.vf
	}
	orderedVFs := make([]fabric.BDF, 0, len(partition.GPUs))
	for _, g := range partition.GPUs {
		vfBDF, ok := byModule[g.PhysicalID]
		if !ok {
			// Should not happen: PartitionForModuleIDs matched the set exactly.
			return a.fallbackNoMatchLocked(whole, moduleIDs, partitions,
				fmt.Errorf("fabric: partition %d GPU physicalId %d has no VF in the request", partition.ID, g.PhysicalID))
		}
		b, perr := fabric.ParseBDF(vfBDF)
		if perr != nil {
			return fmt.Errorf("fabric: parsing VF address %q: %w", vfBDF, perr)
		}
		orderedVFs = append(orderedVFs, b)
	}

	// Track every member VF on the partition; deactivation waits for the last.
	set := a.activeVFs[partition.ID]
	if set == nil {
		set = map[string]struct{}{}
		a.activeVFs[partition.ID] = set
	}
	allCovered := true
	for _, r := range whole {
		if _, ok := set[r.vf]; !ok {
			allCovered = false
			break
		}
	}
	if allCovered && a.partitionActive(partitions, partition.ID) {
		log.Printf("fabric: partition %d already active and covers all %d VFs; skipping activation (steady state)", partition.ID, len(whole))
		return nil
	}
	for _, r := range whole {
		set[r.vf] = struct{}{}
	}

	vfStrs := make([]string, len(orderedVFs))
	for i, b := range orderedVFs {
		vfStrs[i] = b.String()
	}
	log.Printf("fabric: activating multi-GPU partition %d for %d whole-card VFs %v (one VF per GPU, numVfs=%d)",
		partition.ID, len(whole), vfStrs, len(orderedVFs))
	if err := a.ensurePartitionActive(client, partition.ID, orderedVFs, !allCovered); err != nil {
		return a.onFMErrorSet(vfBDFs, fmt.Sprintf("activating multi-GPU partition %d", partition.ID), err)
	}
	log.Printf("fabric: multi-GPU partition %d active for %d VFs", partition.ID, len(orderedVFs))
	return nil
}

// filterWholeCardVFs filters a request's VF BDFs to its whole-card VFs using
// physfn + MIG checks only — no Fabric Manager or module-id resolution — so a
// request that is entirely MIG / passthrough is decided without connecting to
// FM. Non-SR-IOV devices (no physfn) and MIG-mode GPUs (NVLink off, not on the
// fabric) are skipped, exactly as the single path skips them; a MIG-check error
// is treated as "proceed" (include the VF) rather than risk dropping a whole
// card. moduleID is left unset; the caller resolves it once the set is known to
// hold more than one whole card.
func (a *fabricActivator) filterWholeCardVFs(vfBDFs []string) []resolvedVF {
	var whole []resolvedVF
	for _, vf := range vfBDFs {
		pf, err := a.pfForVF(vf)
		if err != nil {
			log.Printf("fabric: %s has no parent PF (not an SR-IOV VF); excluding from multi-GPU activation", vf)
			continue
		}
		if mig, err := a.migMode(pf); err != nil {
			log.Printf("fabric: could not determine MIG mode of PF %s (%v); including VF %s in multi-GPU activation", pf, err, vf)
		} else if mig {
			log.Printf("fabric: skipping MIG-mode GPU (PF %s) for VF %s; MIG has NVLink off, no fabric partition needed", pf, vf)
			continue
		}
		whole = append(whole, resolvedVF{vf: vf, pf: pf})
	}
	return whole
}

// fallbackNoMatchLocked handles a multi-VF request whose whole cards do not form
// a single defined fabric partition. Fail-closed fails the allocation, naming the
// requested GPU set and the multi-GPU partitions the fabric does offer. Fail-open
// activates each card's own single-GPU partition, so CUDA works in the guest
// without cross-card NVLink peer-to-peer. Caller must hold a.mu.
func (a *fabricActivator) fallbackNoMatchLocked(whole []resolvedVF, moduleIDs []uint32, partitions []fabric.Partition, cause error) error {
	vfs := make([]string, len(whole))
	for i, r := range whole {
		vfs[i] = r.vf
	}
	if a.failMode == failClosed {
		return fmt.Errorf("fabric: no defined fabric partition spans exactly the %d requested whole-card GPUs "+
			"(physicalIds %v, VFs %v); the NVSwitch layout offers %s; refusing to hand out cards without a matching "+
			"NVLink partition (set FABRIC_FAIL_MODE=open to allocate them as isolated single-GPU partitions instead): %w",
			len(whole), moduleIDs, vfs, describeMultiGPUPartitions(partitions), cause)
	}
	log.Printf("fabric: WARNING: no partition spans exactly the requested cards (physicalIds %v, VFs %v); "+
		"fail-open: activating each card's single-GPU partition (CUDA works, no cross-card NVLink P2P)", moduleIDs, vfs)
	for _, r := range whole {
		if err := a.activateOneLocked(r.vf); err != nil {
			return err
		}
	}
	return nil
}

// describeMultiGPUPartitions renders the multi-GPU partitions the fabric offers,
// each as "P<id>={physicalIds}", for the fail-closed error message. Single-GPU
// partitions are omitted (a multi-VF request needs a multi-GPU partition).
func describeMultiGPUPartitions(partitions []fabric.Partition) string {
	var parts []string
	for _, p := range partitions {
		if len(p.GPUs) < 2 {
			continue
		}
		ids := make([]uint32, len(p.GPUs))
		for i, g := range p.GPUs {
			ids[i] = g.PhysicalID
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		strs := make([]string, len(ids))
		for i, id := range ids {
			strs[i] = fmt.Sprintf("%d", id)
		}
		parts = append(parts, fmt.Sprintf("P%d={%s}", p.ID, strings.Join(strs, ",")))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// moduleIDCached resolves a PF's FM physicalId (NVML module id), caching the
// result since the PF -> physicalId mapping is static for the machine's
// lifetime. Used by the multi-GPU and preferred-allocation paths; the single
// path caches at the partition level via vfToPartition.
func (a *fabricActivator) moduleIDCached(pf string) (uint32, error) {
	if a.pfToModule == nil {
		a.pfToModule = map[string]uint32{}
	}
	if id, ok := a.pfToModule[pf]; ok {
		return id, nil
	}
	id, err := a.moduleID(pf)
	if err != nil {
		return 0, err
	}
	a.pfToModule[pf] = id
	return id, nil
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

// onFMErrorSet is onFMError for the multi-VF path: fail-closed returns the error
// (fails the allocation for the whole request); fail-open logs and allows it.
func (a *fabricActivator) onFMErrorSet(vfBDFs []string, op string, err error) error {
	if a.failMode == failClosed {
		return fmt.Errorf("fabric: %s for VF set %v: %w", op, vfBDFs, err)
	}
	log.Printf("fabric: WARNING: %s for VF set %v: %v; allowing the allocation "+
		"(fail-open; guest may fail CUDA with error 802 until the fabric partition is up)", op, vfBDFs, err)
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

// ensurePartitionActive activates partitionID for vfSet — one VF per member GPU
// (a single VF for a single-GPU partition, N ordered VFs for a multi-GPU one).
// It is idempotent: if the partition is already active for a DIFFERENT VF set
// (e.g. the whole-card VM restarted with new VFs) and this is a new activation,
// it deactivates and reactivates with the supplied VF set.
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

// reconcile updates the tracked per-partition VF set from the whole-card VFs the
// kubelet pod-resources API reports as allocated to running pods, and deactivates
// any partition whose VFs have all departed. Devices are grouped per container
// (one VM), so a multi-GPU partition — a VM allocated N whole cards — is
// reconstructed as a single partition owning N VFs. That grouping matters after a
// device-plugin restart: kubelet does not re-Allocate running pods, and Fabric
// Manager does not report an active partition's VF list, so the pod-resources
// view is the only way to rebuild membership. MIG VFs and VFs without a fabric
// partition are ignored (they are never activated). Must be called with a.mu held.
//
// Deactivation semantics: kubelet lists each container's devices atomically and
// completely, so a running multi-GPU VM always reports its full VF set. The
// reconciler recomputes the desired partition membership from the current
// pod-resources snapshot each pass and deactivates a partition once no running
// container maps a VF onto it — i.e. its VM departed and all of its VFs are
// released. There is no sub-container partial-release state to handle: a
// partition's tracked set is only emptied when the whole VM is gone.
func (a *fabricActivator) reconcile(client fabric.Client, partitions []fabric.Partition) error {
	sets, err := a.podResources()
	if err != nil {
		return err
	}

	// desired: partition id -> its member VFs, reconstructed per VM, mirroring the
	// activation decision (one multi-GPU partition, or per-GPU single partitions).
	desired := map[uint32]map[string]struct{}{}
	for _, set := range sets {
		whole := a.wholeCardVFsFromDevices(set)
		for pid, vfs := range a.planPartitionsForSet(whole, partitions) {
			if desired[pid] == nil {
				desired[pid] = map[string]struct{}{}
			}
			for _, vf := range vfs {
				desired[pid][vf] = struct{}{}
			}
		}
	}

	// Adopt currently-allocated VFs into the tracked state without activating
	// (an earlier Allocate activated them, possibly before a plugin restart).
	for pid, set := range desired {
		if a.activeVFs[pid] == nil {
			a.activeVFs[pid] = map[string]struct{}{}
		}
		for vf := range set {
			a.activeVFs[pid][vf] = struct{}{}
		}
	}

	// Prune departed VFs per partition; deactivate a partition once it has none
	// left. A running VM always reports its full VF set, so a multi-GPU
	// partition's set is only emptied when its VM departs.
	for pid, set := range a.activeVFs {
		for vf := range set {
			if _, ok := desired[pid][vf]; !ok {
				delete(set, vf)
			}
		}
		if len(set) == 0 {
			delete(a.activeVFs, pid)
			if derr := client.Deactivate(pid); derr != nil && !errors.Is(derr, fabric.ErrPartitionNotActive) {
				log.Printf("fabric: reconcile could not deactivate empty partition %d: %v", pid, derr)
			} else {
				log.Printf("fabric: reconcile deactivated empty partition %d (no VFs remain)", pid)
			}
		}
	}
	return nil
}

// wholeCardVFsFromDevices filters a container's device list to its whole-card
// vGPU VFs (nvidia resources, SR-IOV, non-MIG) and resolves each to its PF and
// physicalId. Devices that are not whole-card VFs are dropped — they are never
// fabric-activated.
func (a *fabricActivator) wholeCardVFsFromDevices(devices []allocatedDevice) []resolvedVF {
	var whole []resolvedVF
	for _, dev := range devices {
		if !isNvidiaResource(dev.resourceName) {
			continue
		}
		pf, err := a.pfForVF(dev.deviceID)
		if err != nil {
			continue // not an SR-IOV VF
		}
		if mig, err := a.migMode(pf); err == nil && mig {
			continue // MIG GPU: never activated
		}
		modID, err := a.moduleIDCached(pf)
		if err != nil {
			continue // cannot resolve; never activated on our watch
		}
		whole = append(whole, resolvedVF{vf: dev.deviceID, pf: pf, moduleID: modID})
	}
	return whole
}

// planPartitionsForSet maps one VM's whole-card VF set to the partition(s) it was
// bound to, mirroring the activation decision so reconcile tracks exactly what
// Activate created: a single multi-GPU partition when the set matches one
// (one VF per member GPU), otherwise each VF on its own single-GPU partition
// (the per-GPU / fail-open shape, and the natural mapping for a one-card VM).
func (a *fabricActivator) planPartitionsForSet(whole []resolvedVF, partitions []fabric.Partition) map[uint32][]string {
	out := map[uint32][]string{}
	if len(whole) == 0 {
		return out
	}
	if len(whole) > 1 {
		moduleIDs := make([]uint32, len(whole))
		for i, r := range whole {
			moduleIDs[i] = r.moduleID
		}
		if p, err := fabric.PartitionForModuleIDs(moduleIDs, partitions); err == nil && len(p.GPUs) == len(whole) {
			vfs := make([]string, len(whole))
			for i, r := range whole {
				vfs[i] = r.vf
			}
			out[p.ID] = vfs
			return out
		}
		// No matching multi-GPU partition: fall through to per-GPU single ones.
	}
	for _, r := range whole {
		p, err := fabric.SingleGPUPartitionForModuleID(r.moduleID, partitions)
		if err != nil {
			continue // no single-GPU partition; never activated
		}
		out[p.ID] = append(out[p.ID], r.vf)
	}
	return out
}

// PreferredVFSet implements the GetPreferredAllocation preference for the
// whole-card vGPU resource. When kubelet asks for `size` devices (size >= 2) from
// the offered VFs, it returns a subset whose GPUs exactly form a defined fabric
// partition, so the resulting VM can get NVLink peer-to-peer between the cards;
// among candidate partitions it prefers ones that fragment the fewest larger
// still-available partitions. It returns (nil, false) — deferring to the caller's
// default preference — for single-device requests, when fabric activation is
// unsupported or unreachable, or when no partition-aligned set can satisfy the
// request (including all must-include devices).
func (a *fabricActivator) PreferredVFSet(available, mustInclude []string, size int) ([]string, bool) {
	if size < 2 || len(available) < size {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unsupported {
		return nil, false
	}

	partitions, ok := a.topologyLocked()
	if !ok {
		return nil, false
	}

	// Map each offered whole-card VF to its GPU physicalId (skip non-SR-IOV and
	// MIG VFs — they cannot be whole-card partition members).
	vfByModule := map[uint32]string{}
	availableModules := map[uint32]struct{}{}
	for _, vf := range available {
		pf, err := a.pfForVF(vf)
		if err != nil {
			continue
		}
		if mig, err := a.migMode(pf); err == nil && mig {
			continue
		}
		modID, err := a.moduleIDCached(pf)
		if err != nil {
			continue
		}
		vfByModule[modID] = vf
		availableModules[modID] = struct{}{}
	}

	// physicalIds of the must-include VFs; the chosen partition must cover them.
	mustModules := map[uint32]struct{}{}
	for _, vf := range mustInclude {
		pf, err := a.pfForVF(vf)
		if err != nil {
			return nil, false // cannot guarantee inclusion; defer to default
		}
		modID, err := a.moduleIDCached(pf)
		if err != nil {
			return nil, false
		}
		mustModules[modID] = struct{}{}
	}

	// Candidate partitions: exactly `size` GPUs, all offered, covering the
	// must-include GPUs. Score each by how many larger available partitions it
	// would fragment (lower is better).
	type candidate struct {
		partition fabric.Partition
		fragments int
	}
	var best *candidate
	for i := range partitions {
		p := partitions[i]
		if len(p.GPUs) != size {
			continue
		}
		if !partitionGPUsAvailable(p, availableModules) {
			continue
		}
		if !partitionCoversModules(p, mustModules) {
			continue
		}
		c := candidate{partition: p, fragments: countFragmentedLargerPartitions(p, partitions, availableModules)}
		if best == nil || c.fragments < best.fragments || (c.fragments == best.fragments && c.partition.ID < best.partition.ID) {
			cc := c
			best = &cc
		}
	}
	if best == nil {
		return nil, false
	}

	out := make([]string, 0, size)
	for _, g := range best.partition.GPUs {
		vf, ok := vfByModule[g.PhysicalID]
		if !ok {
			return nil, false // shouldn't happen after partitionGPUsAvailable
		}
		out = append(out, vf)
	}
	return out, true
}

// topologyLocked returns the fabric partition topology (GPU membership is static
// for the machine's lifetime), fetching and caching it on first use. It is
// best-effort: any Fabric Manager error (including an unsupported system) returns
// ok=false so the preference path silently defers to the default. Caller holds a.mu.
func (a *fabricActivator) topologyLocked() ([]fabric.Partition, bool) {
	if a.topology != nil {
		return a.topology, true
	}
	client, err := a.ensureClient()
	if err != nil {
		return nil, false
	}
	partitions, err := client.GetSupportedPartitions()
	if err != nil {
		if errors.Is(err, fabric.ErrFabricNotSupported) {
			a.unsupported = true
		}
		return nil, false
	}
	a.topology = partitions
	return partitions, true
}

// partitionGPUsAvailable reports whether every GPU of partition p is in the
// offered set.
func partitionGPUsAvailable(p fabric.Partition, available map[uint32]struct{}) bool {
	for _, g := range p.GPUs {
		if _, ok := available[g.PhysicalID]; !ok {
			return false
		}
	}
	return true
}

// partitionCoversModules reports whether partition p's GPUs include every
// physicalId in modules (trivially true when modules is empty).
func partitionCoversModules(p fabric.Partition, modules map[uint32]struct{}) bool {
	if len(modules) == 0 {
		return true
	}
	have := make(map[uint32]struct{}, len(p.GPUs))
	for _, g := range p.GPUs {
		have[g.PhysicalID] = struct{}{}
	}
	for m := range modules {
		if _, ok := have[m]; !ok {
			return false
		}
	}
	return true
}

// countFragmentedLargerPartitions counts partitions strictly larger than p that
// are fully available AND overlap p's GPUs: choosing p would break each of them
// for a future larger request, so a lower count is preferred.
func countFragmentedLargerPartitions(p fabric.Partition, partitions []fabric.Partition, available map[uint32]struct{}) int {
	pset := make(map[uint32]struct{}, len(p.GPUs))
	for _, g := range p.GPUs {
		pset[g.PhysicalID] = struct{}{}
	}
	n := 0
	for _, q := range partitions {
		if len(q.GPUs) <= len(p.GPUs) {
			continue
		}
		if !partitionGPUsAvailable(q, available) {
			continue
		}
		for _, g := range q.GPUs {
			if _, ok := pset[g.PhysicalID]; ok {
				n++
				break
			}
		}
	}
	return n
}
