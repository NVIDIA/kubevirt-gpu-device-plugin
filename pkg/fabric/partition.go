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

package fabric

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrPartitionNotFound is returned when no single-GPU fabric partition matches
// the physical GPU of a VF.
var ErrPartitionNotFound = errors.New("fabric: no single-GPU partition found for physical GPU")

// ErrNoMatchingPartition is returned by PartitionForModuleIDs when no defined
// fabric partition's GPU set exactly matches a requested multi-GPU set. On
// NVSwitch systems the partition layout is predefined (on an 8-GPU HGX: the
// all-8, the two 4-GPU, the four 2-GPU and the eight single-GPU sets), so an
// arbitrary set of GPUs can be activated as a single partition only if the
// fabric offers that exact set — the prerequisite for NVLink peer-to-peer
// between the member cards.
var ErrNoMatchingPartition = errors.New("fabric: no partition matches the requested GPU set")

// ModuleIDFunc returns the Fabric Manager physicalId (identical to the NVML
// module id, nvmlDeviceGetModuleId) of the physical GPU at the given PCI
// address. The address is the Physical Function BDF on SR-IOV systems.
type ModuleIDFunc func(pfPCIAddress string) (uint32, error)

// PFForVFFunc returns the parent Physical Function PCI address of a VF, i.e. the
// physical GPU that owns the VF (resolved from the VF's "physfn" sysfs symlink).
type PFForVFFunc func(vfPCIAddress string) (string, error)

// SingleGPUPartitionForModuleID returns the single-GPU fabric partition whose
// GPU has the given Fabric Manager physicalId (== NVML module id).
//
// Matching is done strictly by physicalId. It never assumes the partition list
// order, the GPU-info index, or PCI enumeration order corresponds to the
// physical id: on NVSwitch systems the FM physicalId (GPU module id) is
// independent of PCI bus ordering, so a positional match would bind a VF to the
// wrong GPU's partition. Multi-GPU partitions (P0=all-8, the 4-GPU and 2-GPU
// partitions) are skipped: this resolver answers the single-whole-card case
// only. A request covering several whole cards is matched to the multi-GPU
// partition spanning exactly those GPUs by PartitionForModuleIDs below.
//
// It returns ErrPartitionNotFound if no single-GPU partition has that
// physicalId, and an error if more than one does (which would indicate an
// inconsistent partition list).
func SingleGPUPartitionForModuleID(moduleID uint32, partitions []Partition) (Partition, error) {
	var match *Partition
	for i := range partitions {
		p := partitions[i]
		if !p.IsSingleGPU() {
			continue
		}
		if p.GPUs[0].PhysicalID != moduleID {
			continue
		}
		if match != nil {
			return Partition{}, fmt.Errorf("fabric: multiple single-GPU partitions (%d and %d) report physicalId %d",
				match.ID, p.ID, moduleID)
		}
		found := p
		match = &found
	}
	if match == nil {
		return Partition{}, fmt.Errorf("%w: physicalId %d", ErrPartitionNotFound, moduleID)
	}
	return *match, nil
}

// ResolvePartitionIDForVF resolves the single-GPU fabric partition id that a
// vGPU VF belongs to, by walking VF -> parent Physical Function -> FM physicalId
// (NVML module id) -> the matching single-GPU partition.
//
// pfForVF resolves the VF's parent PF PCI address (from sysfs). moduleID maps
// that PF to its FM physicalId (via NVML). partitions is the current supported
// partition list from Client.GetSupportedPartitions. Any of these failing is
// wrapped and returned so the caller can decide whether to fail the allocation
// or warn.
func ResolvePartitionIDForVF(vfPCIAddress string, pfForVF PFForVFFunc, moduleID ModuleIDFunc, partitions []Partition) (uint32, error) {
	pf, err := pfForVF(vfPCIAddress)
	if err != nil {
		return 0, fmt.Errorf("fabric: resolving parent PF of VF %s: %w", vfPCIAddress, err)
	}
	modID, err := moduleID(pf)
	if err != nil {
		return 0, fmt.Errorf("fabric: resolving module id of PF %s (VF %s): %w", pf, vfPCIAddress, err)
	}
	part, err := SingleGPUPartitionForModuleID(modID, partitions)
	if err != nil {
		return 0, fmt.Errorf("fabric: resolving partition for VF %s (PF %s, physicalId %d): %w",
			vfPCIAddress, pf, modID, err)
	}
	return part.ID, nil
}

// PartitionForModuleIDs returns the fabric partition whose set of physical GPU
// ids (physicalId == NVML module id) is exactly equal to the set of moduleIDs —
// same members, same cardinality — ignoring order and duplicate ids in the
// input. It is the multi-GPU generalisation of SingleGPUPartitionForModuleID:
// for a single id it returns that GPU's single-GPU partition; for N>1 it returns
// the one predefined partition spanning exactly those N GPUs, so a whole-card
// vGPU allocation of N cards can be bound to a single partition and get NVLink
// peer-to-peer between them.
//
// Matching is strictly by physicalId, never by list position or PCI order, for
// the same reason SingleGPUPartitionForModuleID matches that way: the FM
// physicalId is independent of PCI bus ordering.
//
// It returns ErrNoMatchingPartition if no partition's GPU set equals the given
// set (the requested set is not one the fabric offers as a partition), and an
// error if more than one partition matches (an inconsistent partition list).
// The caller is responsible for verifying that len(partition.GPUs) matches the
// number of distinct VFs it holds (one VF per GPU) before activating.
func PartitionForModuleIDs(moduleIDs []uint32, partitions []Partition) (Partition, error) {
	want := make(map[uint32]struct{}, len(moduleIDs))
	for _, id := range moduleIDs {
		want[id] = struct{}{}
	}
	if len(want) == 0 {
		return Partition{}, fmt.Errorf("%w: empty GPU set", ErrNoMatchingPartition)
	}

	var match *Partition
	for i := range partitions {
		p := partitions[i]
		if !gpuSetEquals(p.GPUs, want) {
			continue
		}
		if match != nil {
			return Partition{}, fmt.Errorf("fabric: multiple partitions (%d and %d) cover GPU set %s",
				match.ID, p.ID, formatModuleIDSet(want))
		}
		found := p
		match = &found
	}
	if match == nil {
		return Partition{}, fmt.Errorf("%w: %s", ErrNoMatchingPartition, formatModuleIDSet(want))
	}
	return *match, nil
}

// gpuSetEquals reports whether the partition's GPUs cover exactly the physicalId
// set want — every GPU is in want and every id in want is covered, with matching
// cardinality (so a superset or subset partition does not match).
func gpuSetEquals(gpus []GPUInfo, want map[uint32]struct{}) bool {
	if len(gpus) != len(want) {
		return false
	}
	seen := make(map[uint32]struct{}, len(gpus))
	for _, g := range gpus {
		if _, ok := want[g.PhysicalID]; !ok {
			return false
		}
		seen[g.PhysicalID] = struct{}{}
	}
	return len(seen) == len(want)
}

// formatModuleIDSet renders a physicalId set as a stable, sorted "{3, 4}" string
// for error messages.
func formatModuleIDSet(set map[uint32]struct{}) string {
	ids := make([]uint32, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
