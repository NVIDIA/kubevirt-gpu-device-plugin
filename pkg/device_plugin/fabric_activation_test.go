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
	"strconv"
	"strings"
	"testing"

	"kubevirt-gpu-device-plugin/pkg/fabric"
)

type activateCall struct {
	partitionID uint32
	vfs         []string
}

// fakeFabricClient is a scriptable fabric.Client for testing the activator.
type fakeFabricClient struct {
	partitions []fabric.Partition
	getErr     error

	// active tracks which partitions are currently active, so
	// GetSupportedPartitions reflects the effect of ActivateWithVFs/Deactivate.
	active map[uint32]bool

	activateCalls   []activateCall
	deactivateCalls []uint32

	activateFn func(call int, partitionID uint32, vfs []fabric.BDF) error
}

func (f *fakeFabricClient) GetSupportedPartitions() ([]fabric.Partition, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make([]fabric.Partition, len(f.partitions))
	for i, p := range f.partitions {
		p.Active = p.Active || f.active[p.ID]
		out[i] = p
	}
	return out, nil
}

func (f *fakeFabricClient) ActivateWithVFs(partitionID uint32, vfs []fabric.BDF) error {
	idx := len(f.activateCalls)
	strs := make([]string, len(vfs))
	for i, v := range vfs {
		strs[i] = v.String()
	}
	f.activateCalls = append(f.activateCalls, activateCall{partitionID, strs})
	var ret error
	if f.activateFn != nil {
		ret = f.activateFn(idx, partitionID, vfs)
	}
	if ret == nil || errors.Is(ret, fabric.ErrInUse) {
		if f.active == nil {
			f.active = map[uint32]bool{}
		}
		f.active[partitionID] = true
	}
	return ret
}

func (f *fakeFabricClient) Deactivate(partitionID uint32) error {
	f.deactivateCalls = append(f.deactivateCalls, partitionID)
	if f.active != nil {
		f.active[partitionID] = false
	}
	return nil
}

func (f *fakeFabricClient) Close() error { return nil }

func (f *fakeFabricClient) lastActivate() []string {
	if len(f.activateCalls) == 0 {
		return nil
	}
	return f.activateCalls[len(f.activateCalls)-1].vfs
}

// testPartitions models two single-GPU partitions (11 -> physicalId 3,
// 14 -> physicalId 4) plus an 8-GPU partition, deliberately not in id order.
func testPartitions() []fabric.Partition {
	return []fabric.Partition{
		{ID: 0, GPUs: []fabric.GPUInfo{
			{PhysicalID: 1}, {PhysicalID: 2}, {PhysicalID: 3}, {PhysicalID: 4},
			{PhysicalID: 5}, {PhysicalID: 6}, {PhysicalID: 7}, {PhysicalID: 8},
		}},
		{ID: 11, GPUs: []fabric.GPUInfo{{PhysicalID: 3}}},
		{ID: 14, GPUs: []fabric.GPUInfo{{PhysicalID: 4}}},
	}
}

// testMultiPartitions is testPartitions plus a 2-GPU partition (id 20) covering
// physicalIds {3,4} — the GPUs behind PFs 0000:41:00.0 and 0000:81:00.0. Its GPU
// list is deliberately in [4,3] order so tests pin that activation orders the VFs
// to match the partition's GPU order, not the request order.
func testMultiPartitions() []fabric.Partition {
	return []fabric.Partition{
		{ID: 0, GPUs: []fabric.GPUInfo{
			{PhysicalID: 1}, {PhysicalID: 2}, {PhysicalID: 3}, {PhysicalID: 4},
			{PhysicalID: 5}, {PhysicalID: 6}, {PhysicalID: 7}, {PhysicalID: 8},
		}},
		{ID: 20, GPUs: []fabric.GPUInfo{{PhysicalID: 4}, {PhysicalID: 3}}},
		{ID: 11, GPUs: []fabric.GPUInfo{{PhysicalID: 3}}},
		{ID: 14, GPUs: []fabric.GPUInfo{{PhysicalID: 4}}},
	}
}

// testPFForVF maps a VF to its PF: 0000:41:00.x -> 0000:41:00.0,
// 0000:81:00.x -> 0000:81:00.0. Addresses starting with "nopf" have no PF.
func testPFForVF(vf string) (string, error) {
	switch {
	case strings.HasPrefix(vf, "0000:41:00."):
		return "0000:41:00.0", nil
	case strings.HasPrefix(vf, "0000:81:00."):
		return "0000:81:00.0", nil
	}
	return "", fmt.Errorf("no physfn for %s", vf)
}

// testModuleID maps a PF to its FM physicalId: 0000:41:00.0 -> 3, 0000:81:00.0 -> 4.
func testModuleID(pf string) (uint32, error) {
	switch pf {
	case "0000:41:00.0":
		return 3, nil
	case "0000:81:00.0":
		return 4, nil
	}
	return 0, fmt.Errorf("unknown PF %s", pf)
}

// newTestActivator builds an activator wired to a fake client, empty
// pod-resources, and non-MIG GPUs, with the given fail mode.
func newTestActivator(fc *fakeFabricClient, connErr error, fm failMode) *fabricActivator {
	return &fabricActivator{
		address:  "test",
		failMode: fm,
		newClient: func(string) (fabric.Client, error) {
			if connErr != nil {
				return nil, connErr
			}
			return fc, nil
		},
		pfForVF:       testPFForVF,
		moduleID:      testModuleID,
		migMode:       func(string) (bool, error) { return false, nil },
		podResources:  func() ([][]allocatedDevice, error) { return nil, nil },
		vfToPartition: map[string]uint32{},
		activeVFs:     map[uint32]map[string]struct{}{},
	}
}

func inUseError() error {
	return &fabric.Error{Op: "fmActivateFabricPartitionWithVFs", Code: fabric.ErrInUseCode}
}

func nvlinkError() error {
	return &fabric.Error{Op: "fmActivateFabricPartitionWithVFs", Code: fabric.ErrNVLinkError}
}

func TestActivate_HappyPathSingleVF(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("want 1 activate call, got %d", len(fc.activateCalls))
	}
	got := fc.activateCalls[0]
	if got.partitionID != 11 || len(got.vfs) != 1 || got.vfs[0] != "0000:41:00.4" {
		t.Fatalf("want ActivateWithVFs(11, [0000:41:00.4]) numVfs=1, got %+v", got)
	}
	if len(fc.deactivateCalls) != 0 {
		t.Fatalf("did not expect deactivate calls, got %v", fc.deactivateCalls)
	}
}

// TestActivate_AlwaysSingleVF is the regression for the BADPARAM bug: even when
// the partition already has sibling VFs tracked (adopted from pod-resources),
// activation must pass EXACTLY the VF being allocated (numVfs=1), never the
// union — fmActivateFabricPartitionWithVFs takes one VF per GPU.
func TestActivate_AlwaysSingleVF(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	// Two VFs on the same physical GPU (partition 11) are allocated to running
	// pods (one VF each); both get adopted into the tracked set on reconcile.
	a.podResources = func() ([][]allocatedDevice, error) {
		return [][]allocatedDevice{
			{{resourceName: "nvidia.com/H200X", deviceID: "0000:41:00.4"}},
			{{resourceName: "nvidia.com/H200X", deviceID: "0000:41:00.5"}},
		}, nil
	}

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("want 1 activate call, got %d (%v)", len(fc.activateCalls), fc.activateCalls)
	}
	got := fc.lastActivate()
	if len(got) != 1 || got[0] != "0000:41:00.4" {
		t.Fatalf("activation must be a single VF (the allocated one), not the union; got %v", got)
	}
}

func TestActivate_MIGSkips(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	a.migMode = func(string) (bool, error) { return true, nil } // MIG-enabled GPU

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("MIG GPU activate must be a no-op, got %v", err)
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("MIG GPU must not touch Fabric Manager, got %v", fc.activateCalls)
	}
}

func TestActivate_MIGCheckErrorProceeds(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	a.migMode = func(string) (bool, error) { return false, errors.New("nvml hiccup") }

	// A MIG-mode check error must not skip a (possibly whole-card) VF.
	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("MIG-check error should proceed with activation, got %d calls", len(fc.activateCalls))
	}
}

func TestActivate_SkipIfCoveredIsZeroTouch(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatal(err)
	}
	// Re-Allocate of the same VF (VM restart) with the partition now active
	// must not touch Fabric Manager.
	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatal(err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("re-Allocate of a covered+active VF must skip, got %d calls", len(fc.activateCalls))
	}
}

func TestActivate_VFChangedReactivates(t *testing.T) {
	// Partition 14 is already active (for the whole-card VM's old VF). The VM
	// restarts with a new VF; the plugin must deactivate and reactivate for it.
	fc := &fakeFabricClient{partitions: testPartitions(), active: map[uint32]bool{14: true}}
	fc.activateFn = func(call int, _ uint32, _ []fabric.BDF) error {
		if call == 0 {
			return inUseError() // already active for the old VF
		}
		return nil
	}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("0000:81:00.5"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(fc.activateCalls) != 2 {
		t.Fatalf("want 2 activate calls (retry after deactivate), got %d", len(fc.activateCalls))
	}
	if len(fc.deactivateCalls) != 1 || fc.deactivateCalls[0] != 14 {
		t.Fatalf("want one Deactivate(14), got %v", fc.deactivateCalls)
	}
	if got := fc.lastActivate(); len(got) != 1 || got[0] != "0000:81:00.5" {
		t.Fatalf("reactivation must be the single new VF, got %v", got)
	}
}

func TestActivate_DistinctGPUsDistinctPartitions(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("0000:41:00.4"); err != nil { // -> partition 11
		t.Fatal(err)
	}
	if err := a.Activate("0000:81:00.4"); err != nil { // -> partition 14
		t.Fatal(err)
	}
	if fc.activateCalls[0].partitionID != 11 || fc.activateCalls[1].partitionID != 14 {
		t.Fatalf("expected partitions 11 then 14, got %d then %d",
			fc.activateCalls[0].partitionID, fc.activateCalls[1].partitionID)
	}
}

func TestActivate_NonSRIOVSkips(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("nopf-device"); err != nil {
		t.Fatalf("Activate non-VF should be a no-op, got %v", err)
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("non-VF must not touch Fabric Manager, got %v", fc.activateCalls)
	}
}

func TestActivate_FabricUnsupportedDisables(t *testing.T) {
	fc := &fakeFabricClient{
		getErr: &fabric.Error{Op: "fmGetSupportedFabricPartitions", Code: fabric.ErrNotSupported},
	}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("unsupported system should skip, got %v", err)
	}
	if !a.unsupported {
		t.Fatal("activator should mark itself unsupported")
	}
	fc.getErr = nil
	if err := a.Activate("0000:41:00.5"); err != nil {
		t.Fatal(err)
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("unsupported activator must not activate, got %v", fc.activateCalls)
	}
}

func TestActivate_PartitionNotFoundSkips(t *testing.T) {
	fc := &fakeFabricClient{partitions: []fabric.Partition{{ID: 11, GPUs: []fabric.GPUInfo{{PhysicalID: 3}}}}}
	a := newTestActivator(fc, nil, failClosed)
	a.moduleID = func(string) (uint32, error) { return 5, nil } // no single-GPU partition for id 5

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("missing partition should skip, got %v", err)
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("must not activate when no partition found, got %v", fc.activateCalls)
	}
}

// TestActivate_AdoptedButInactivePartitionActivates: a VF adopted from
// pod-resources whose partition is NOT active must still be activated (cutover
// from an earlier registration), with a single VF.
func TestActivate_AdoptedButInactivePartitionActivates(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()} // all partitions inactive
	a := newTestActivator(fc, nil, failClosed)
	a.podResources = func() ([][]allocatedDevice, error) {
		return [][]allocatedDevice{{{resourceName: "nvidia.com/H200X", deviceID: "0000:41:00.4"}}}, nil
	}

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(fc.activateCalls) != 1 || fc.activateCalls[0].partitionID != 11 {
		t.Fatalf("adopted VF on an inactive partition must be activated once on 11, got %v", fc.activateCalls)
	}
	if got := fc.lastActivate(); len(got) != 1 {
		t.Fatalf("activation must be a single VF, got %v", got)
	}
}

func TestReconcile_PrunesAndDeactivatesEmptied(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	// Two whole-card VFs on partition 11 (contrived; exercises tracking).
	for _, vf := range []string{"0000:41:00.4", "0000:41:00.5"} {
		if err := a.Activate(vf); err != nil {
			t.Fatal(err)
		}
	}

	// One VM departs: pod-resources reports only .4. Reconcile prunes .5, keeps 11.
	a.podResources = func() ([][]allocatedDevice, error) {
		return [][]allocatedDevice{{{resourceName: "nvidia.com/H200X", deviceID: "0000:41:00.4"}}}, nil
	}
	a.Reconcile()
	if _, ok := a.activeVFs[11]["0000:41:00.5"]; ok {
		t.Fatal("departed VF .5 should be pruned")
	}
	if len(fc.deactivateCalls) != 0 {
		t.Fatalf("must not deactivate a non-empty partition, got %v", fc.deactivateCalls)
	}

	// The last VF departs: reconcile deactivates the empty partition.
	a.podResources = func() ([][]allocatedDevice, error) { return nil, nil }
	a.Reconcile()
	if _, ok := a.activeVFs[11]; ok {
		t.Fatal("empty partition should be dropped from tracking")
	}
	if len(fc.deactivateCalls) != 1 || fc.deactivateCalls[0] != 11 {
		t.Fatalf("want Deactivate(11) once the partition is empty, got %v", fc.deactivateCalls)
	}
}

func TestActivate_CachesPartitionResolution(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	moduleCalls := 0
	a.moduleID = func(pf string) (uint32, error) {
		moduleCalls++
		return testModuleID(pf)
	}

	for i := 0; i < 3; i++ {
		if err := a.Activate("0000:41:00.4"); err != nil {
			t.Fatal(err)
		}
	}
	if moduleCalls != 1 {
		t.Fatalf("VF->partition resolution should be cached (1 module id lookup), got %d", moduleCalls)
	}
}

func TestActivate_FailClosedActivationErrorFails(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	fc.activateFn = func(int, uint32, []fabric.BDF) error { return nvlinkError() }
	a := newTestActivator(fc, nil, failClosed)

	if err := a.Activate("0000:41:00.4"); err == nil {
		t.Fatal("fail-closed must propagate an activation error to fail the allocation")
	}
}

func TestActivate_FailOpenActivationErrorAllows(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()}
	fc.activateFn = func(int, uint32, []fabric.BDF) error { return nvlinkError() }
	a := newTestActivator(fc, nil, failOpen)

	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("fail-open must allow the allocation on an activation error, got %v", err)
	}
}

func TestActivate_FailClosedConnectErrorFails(t *testing.T) {
	a := newTestActivator(nil, errors.New("no daemon"), failClosed)
	if err := a.Activate("0000:41:00.4"); err == nil {
		t.Fatal("fail-closed must fail the allocation when FM is unreachable")
	}
}

func TestActivate_FailOpenConnectErrorAllows(t *testing.T) {
	a := newTestActivator(nil, errors.New("no daemon"), failOpen)
	if err := a.Activate("0000:41:00.4"); err != nil {
		t.Fatalf("fail-open should allow the allocation on connect failure, got %v", err)
	}
}

func TestNewFabricActivator_Disabled(t *testing.T) {
	t.Setenv(envFabricActivation, "off")
	if _, ok := newFabricActivator(); ok {
		t.Fatal("FABRIC_PARTITION_ACTIVATION=off must disable the activator")
	}
}

func TestNewFabricActivator_Config(t *testing.T) {
	t.Setenv(envFabricActivation, "auto")
	t.Setenv(envFabricAddress, "127.0.0.1:6666")
	t.Setenv(envReconcileInterval, "30s")
	t.Setenv(envFailMode, "open")
	a, ok := newFabricActivator()
	if !ok {
		t.Fatal("expected activator to be enabled")
	}
	if a.address != "127.0.0.1:6666" {
		t.Errorf("address not parsed, got %q", a.address)
	}
	if a.reconcileInterval.String() != "30s" {
		t.Errorf("reconcile interval not parsed, got %s", a.reconcileInterval)
	}
	if a.failMode != failOpen {
		t.Errorf("fail mode not parsed, got %s", a.failMode)
	}
}

func TestNewFabricActivator_FailModeDefaultClosed(t *testing.T) {
	t.Setenv(envFabricActivation, "auto")
	t.Setenv(envFailMode, "")
	a, ok := newFabricActivator()
	if !ok {
		t.Fatal("expected enabled")
	}
	if a.failMode != failClosed {
		t.Errorf("default fail mode must be closed, got %s", a.failMode)
	}
}

// --- Multi-GPU (multi-VF) activation ---------------------------------------

// TestActivateSet_SingleVFIdenticalToActivate: a one-element set must drive the
// exact single-GPU path (partition 11, one VF), byte-identical to Activate.
func TestActivateSet_SingleVFIdenticalToActivate(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.ActivateSet([]string{"0000:41:00.4"}); err != nil {
		t.Fatalf("ActivateSet single: %v", err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("want 1 activate call, got %d", len(fc.activateCalls))
	}
	got := fc.activateCalls[0]
	if got.partitionID != 11 || len(got.vfs) != 1 || got.vfs[0] != "0000:41:00.4" {
		t.Fatalf("single-VF ActivateSet must match the single path, got %+v", got)
	}
}

// TestActivateSet_MultiGPUMatchingSet: two whole cards forming a defined
// partition activate that one partition, with one VF per GPU, ordered to the
// partition's GPU order.
func TestActivateSet_MultiGPUMatchingSet(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err != nil {
		t.Fatalf("ActivateSet: %v", err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("want 1 activate call (one multi-GPU partition), got %d: %v", len(fc.activateCalls), fc.activateCalls)
	}
	got := fc.activateCalls[0]
	if got.partitionID != 20 {
		t.Fatalf("want partition 20 (the {3,4} pair), got %d", got.partitionID)
	}
	// Partition 20's GPUs are [physId 4, physId 3]; the VF list must follow that
	// order: [module-4 VF, module-3 VF] = [0000:81:00.4, 0000:41:00.4].
	if len(got.vfs) != 2 || got.vfs[0] != "0000:81:00.4" || got.vfs[1] != "0000:41:00.4" {
		t.Fatalf("VF order must match the partition's GPU order [phys4, phys3], got %v", got.vfs)
	}
	if len(fc.deactivateCalls) != 0 {
		t.Fatalf("did not expect deactivate calls, got %v", fc.deactivateCalls)
	}
	if a.activeVFs[20] == nil || len(a.activeVFs[20]) != 2 {
		t.Fatalf("both VFs must be tracked on partition 20, got %v", a.activeVFs[20])
	}
}

// TestActivateSet_MultiGPUSteadyStateSkips: re-Allocate of the same multi-card
// set with the partition already active must not touch Fabric Manager.
func TestActivateSet_MultiGPUSteadyStateSkips(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	req := []string{"0000:41:00.4", "0000:81:00.4"}
	if err := a.ActivateSet(req); err != nil {
		t.Fatal(err)
	}
	if err := a.ActivateSet(req); err != nil {
		t.Fatal(err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("re-Allocate of a covered+active multi-GPU set must skip, got %d calls", len(fc.activateCalls))
	}
}

// TestActivateSet_NoMatchingPartitionFailClosed: a multi-card set with no defined
// partition fails the allocation under fail-closed, without touching FM.
func TestActivateSet_NoMatchingPartitionFailClosed(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()} // no {3,4} pair
	a := newTestActivator(fc, nil, failClosed)

	err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"})
	if err == nil {
		t.Fatal("fail-closed must reject a multi-card set with no matching partition")
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("must not activate when no partition matches (fail-closed), got %v", fc.activateCalls)
	}
}

// TestActivateSet_NoMatchingPartitionFailOpen: with no matching partition,
// fail-open activates each card's single-GPU partition so CUDA still works.
func TestActivateSet_NoMatchingPartitionFailOpen(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()} // single-GPU 11 and 14, no pair
	a := newTestActivator(fc, nil, failOpen)

	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err != nil {
		t.Fatalf("fail-open must allow the allocation via per-GPU fallback, got %v", err)
	}
	if len(fc.activateCalls) != 2 {
		t.Fatalf("want 2 single-GPU activations in per-GPU fallback, got %d: %v", len(fc.activateCalls), fc.activateCalls)
	}
	gotParts := map[uint32]bool{}
	for _, c := range fc.activateCalls {
		if len(c.vfs) != 1 {
			t.Fatalf("per-GPU fallback must activate one VF per call, got %v", c.vfs)
		}
		gotParts[c.partitionID] = true
	}
	if !gotParts[11] || !gotParts[14] {
		t.Fatalf("want single-GPU partitions 11 and 14 activated, got %v", gotParts)
	}
}

// TestActivateSet_MIGCardExcludedCollapsesToSingle: a MIG card mixed into the
// request is skipped; if only one whole card remains, it is a single-GPU
// activation.
func TestActivateSet_MIGCardExcludedCollapsesToSingle(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	a.migMode = func(pf string) (bool, error) { return pf == "0000:81:00.0", nil } // module-4 card is MIG

	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err != nil {
		t.Fatalf("ActivateSet: %v", err)
	}
	if len(fc.activateCalls) != 1 {
		t.Fatalf("want 1 activate (single whole card after MIG excluded), got %d: %v", len(fc.activateCalls), fc.activateCalls)
	}
	got := fc.activateCalls[0]
	if got.partitionID != 11 || len(got.vfs) != 1 || got.vfs[0] != "0000:41:00.4" {
		t.Fatalf("want single-GPU partition 11 for the one whole card, got %+v", got)
	}
}

// TestActivateSet_AllMIGIsNoOp: a multi-card set that is entirely MIG activates
// nothing.
func TestActivateSet_AllMIGIsNoOp(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	a.migMode = func(string) (bool, error) { return true, nil }

	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err != nil {
		t.Fatalf("all-MIG set must be a no-op, got %v", err)
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("all-MIG set must not touch Fabric Manager, got %v", fc.activateCalls)
	}
}

// TestReconcile_MultiGPUDeactivatesAfterVMDeparts: a multi-GPU partition stays
// active while its VM runs (its container reports the full VF set) and is
// deactivated exactly once, when the VM departs.
func TestReconcile_MultiGPUDeactivatesAfterVMDeparts(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err != nil {
		t.Fatal(err)
	}
	if len(a.activeVFs[20]) != 2 {
		t.Fatalf("partition 20 should track both VFs, got %v", a.activeVFs[20])
	}

	// VM still running: its virt-launcher container reports both cards (kubelet
	// lists a container's devices atomically). Partition 20 must stay active.
	a.podResources = func() ([][]allocatedDevice, error) {
		return [][]allocatedDevice{{
			{resourceName: "nvidia.com/H200X", deviceID: "0000:41:00.4"},
			{resourceName: "nvidia.com/H200X", deviceID: "0000:81:00.4"},
		}}, nil
	}
	a.Reconcile()
	if len(a.activeVFs[20]) != 2 {
		t.Fatalf("multi-GPU partition must stay active while its VM runs, got %v", a.activeVFs[20])
	}
	if len(fc.deactivateCalls) != 0 {
		t.Fatalf("must not deactivate a running VM's partition, got %v", fc.deactivateCalls)
	}

	// The VM departs: its container is gone. Reconcile deactivates partition 20
	// exactly once (all its VFs released).
	a.podResources = func() ([][]allocatedDevice, error) { return nil, nil }
	a.Reconcile()
	if _, ok := a.activeVFs[20]; ok {
		t.Fatal("multi-GPU partition should be dropped once its VM departs")
	}
	if len(fc.deactivateCalls) != 1 || fc.deactivateCalls[0] != 20 {
		t.Fatalf("want a single Deactivate(20) after the VM departs, got %v", fc.deactivateCalls)
	}
}

// TestReconcile_AdoptsMultiGPUPartitionAfterRestart: with in-memory state empty
// (as after a device-plugin restart), reconcile reconstructs a multi-GPU
// partition from the VM's container device set, so it can later be deactivated.
func TestReconcile_AdoptsMultiGPUPartitionAfterRestart(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions(), active: map[uint32]bool{20: true}}
	a := newTestActivator(fc, nil, failClosed)
	a.podResources = func() ([][]allocatedDevice, error) {
		return [][]allocatedDevice{{
			{resourceName: "nvidia.com/H200X", deviceID: "0000:41:00.4"},
			{resourceName: "nvidia.com/H200X", deviceID: "0000:81:00.4"},
		}}, nil
	}

	a.Reconcile()
	if len(a.activeVFs[20]) != 2 {
		t.Fatalf("reconcile must adopt the running VM's cards onto multi-GPU partition 20, got %v", a.activeVFs[20])
	}
	if len(fc.deactivateCalls) != 0 {
		t.Fatalf("a running VM must not be deactivated, got %v", fc.deactivateCalls)
	}
}

// TestActivateSet_MultiFailClosedConnectErrorFails: a multi-card request must
// fail the allocation under fail-closed when Fabric Manager is unreachable.
func TestActivateSet_MultiFailClosedConnectErrorFails(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, errors.New("no daemon"), failClosed)
	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err == nil {
		t.Fatal("fail-closed must fail a multi-card allocation when FM is unreachable")
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("must not activate anything on a connect failure, got %v", fc.activateCalls)
	}
}

// TestActivateSet_MultiFailOpenConnectErrorAllows: fail-open allows a multi-card
// allocation when FM is unreachable, activating nothing.
func TestActivateSet_MultiFailOpenConnectErrorAllows(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, errors.New("no daemon"), failOpen)
	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err != nil {
		t.Fatalf("fail-open must allow a multi-card allocation on connect failure, got %v", err)
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("must not activate anything on a connect failure, got %v", fc.activateCalls)
	}
}

// TestActivateSet_TwoVFsSameGPUFallsBack: two VFs resolving to the same physical
// GPU cannot form a one-VF-per-GPU partition, so under fail-closed the allocation
// fails (routed through the no-match fallback) rather than activating a partition
// with a duplicate GPU.
func TestActivateSet_TwoVFsSameGPUFallsBack(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	// 0000:41:00.4 and 0000:41:00.5 both live on PF 0000:41:00.0 -> module id 3.
	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:41:00.5"}); err == nil {
		t.Fatal("two VFs on the same GPU must not form a multi-GPU partition under fail-closed")
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("must not activate a partition with a duplicate GPU, got %v", fc.activateCalls)
	}
}

// TestActivateSet_MultiModuleIDErrorFails: a module-id resolution failure for a
// whole card in a multi-card request is a hard error, mirroring the single path.
func TestActivateSet_MultiModuleIDErrorFails(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	a.moduleID = func(pf string) (uint32, error) {
		if pf == "0000:81:00.0" {
			return 0, errors.New("nvml module id failure")
		}
		return testModuleID(pf)
	}
	if err := a.ActivateSet([]string{"0000:41:00.4", "0000:81:00.4"}); err == nil {
		t.Fatal("a module-id resolution failure for a whole card must fail the allocation")
	}
	if len(fc.activateCalls) != 0 {
		t.Fatalf("must not activate when a member's GPU cannot be resolved, got %v", fc.activateCalls)
	}
}

// --- GetPreferredAllocation partition alignment ----------------------------

func TestPreferredVFSet_PartitionAligned(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	set, ok := a.PreferredVFSet([]string{"0000:41:00.4", "0000:81:00.4"}, nil, 2)
	if !ok {
		t.Fatal("expected a partition-aligned preferred set")
	}
	if len(set) != 2 {
		t.Fatalf("want 2 VFs, got %v", set)
	}
	want := map[string]bool{"0000:41:00.4": true, "0000:81:00.4": true}
	for _, vf := range set {
		if !want[vf] {
			t.Fatalf("unexpected VF %s in preferred set %v", vf, set)
		}
	}
}

func TestPreferredVFSet_SingleDeviceDefers(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	if _, ok := a.PreferredVFSet([]string{"0000:41:00.4"}, nil, 1); ok {
		t.Fatal("single-device request must defer to the default preference")
	}
}

func TestPreferredVFSet_NoAlignedSetDefers(t *testing.T) {
	fc := &fakeFabricClient{partitions: testPartitions()} // only single-GPU + all-8
	a := newTestActivator(fc, nil, failClosed)
	if _, ok := a.PreferredVFSet([]string{"0000:41:00.4", "0000:81:00.4"}, nil, 2); ok {
		t.Fatal("no matching 2-GPU partition must defer to the default preference")
	}
}

func TestPreferredVFSet_UnsupportedDefers(t *testing.T) {
	fc := &fakeFabricClient{getErr: &fabric.Error{Op: "fmGetSupportedFabricPartitions", Code: fabric.ErrNotSupported}}
	a := newTestActivator(fc, nil, failClosed)
	if _, ok := a.PreferredVFSet([]string{"0000:41:00.4", "0000:81:00.4"}, nil, 2); ok {
		t.Fatal("an unsupported system must defer to the default preference")
	}
}

func TestPreferredVFSet_MustIncludeHonored(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)

	set, ok := a.PreferredVFSet([]string{"0000:41:00.4", "0000:81:00.4"}, []string{"0000:81:00.4"}, 2)
	if !ok {
		t.Fatal("expected an aligned set covering the must-include device")
	}
	found := false
	for _, vf := range set {
		if vf == "0000:81:00.4" {
			found = true
		}
	}
	if !found {
		t.Fatalf("must-include VF absent from preferred set %v", set)
	}
}

func TestPreferredVFSet_MustIncludeUnresolvableDefers(t *testing.T) {
	fc := &fakeFabricClient{partitions: testMultiPartitions()}
	a := newTestActivator(fc, nil, failClosed)
	// A must-include device with no parent PF cannot be guaranteed in any
	// partition-aligned set, so the preference defers to the default.
	if _, ok := a.PreferredVFSet([]string{"0000:41:00.4", "0000:81:00.4"}, []string{"nopf-device"}, 2); ok {
		t.Fatal("an unresolvable must-include device must defer to the default preference")
	}
}

// TestPreferredVFSet_PrefersLessFragmenting: among candidate size-2 partitions,
// prefer the one that does not break up a larger still-available partition.
func TestPreferredVFSet_PrefersLessFragmenting(t *testing.T) {
	parts := []fabric.Partition{
		{ID: 30, GPUs: []fabric.GPUInfo{{PhysicalID: 1}, {PhysicalID: 2}}},
		{ID: 31, GPUs: []fabric.GPUInfo{{PhysicalID: 3}, {PhysicalID: 4}}},
		{ID: 40, GPUs: []fabric.GPUInfo{{PhysicalID: 1}, {PhysicalID: 2}, {PhysicalID: 5}, {PhysicalID: 6}}},
	}
	fc := &fakeFabricClient{partitions: parts}
	a := newTestActivator(fc, nil, failClosed)
	// VF "vfN" -> PF "pfN" -> module N.
	a.pfForVF = func(vf string) (string, error) { return "pf" + strings.TrimPrefix(vf, "vf"), nil }
	a.moduleID = func(pf string) (uint32, error) {
		n, err := strconv.Atoi(strings.TrimPrefix(pf, "pf"))
		if err != nil {
			return 0, err
		}
		return uint32(n), nil
	}

	// P30={1,2} fragments the available 4-GPU P40; P31={3,4} does not.
	set, ok := a.PreferredVFSet([]string{"vf1", "vf2", "vf3", "vf4", "vf5", "vf6"}, nil, 2)
	if !ok {
		t.Fatal("expected a partition-aligned set")
	}
	got := map[string]bool{}
	for _, vf := range set {
		got[vf] = true
	}
	if len(set) != 2 || !got["vf3"] || !got["vf4"] {
		t.Fatalf("expected the less-fragmenting pair {vf3,vf4} (partition 31), got %v", set)
	}
}
