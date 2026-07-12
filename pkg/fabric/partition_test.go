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
	"testing"
)

// hgxPartitions models the fmGetSupportedFabricPartitions output of an 8-GPU
// HGX box: P0 = all 8 GPUs, P1/P2 = 4-GPU, P3-P6 = 2-GPU, P7-P14 = single-GPU.
//
// Crucially, the single-GPU partitions' physicalIds are deliberately NOT in
// partition-index order and NOT in any PCI-like order: partition P7 owns
// physicalId 8, P8 owns 5, and so on. This pins the requirement that resolution
// matches on physicalId (GPU module id), never on list position or PCI order.
func hgxPartitions() []Partition {
	// partitionID -> physicalId for the eight single-GPU partitions, scrambled.
	single := []struct {
		partID uint32
		physID uint32
	}{
		{7, 8}, {8, 5}, {9, 1}, {10, 7}, {11, 3}, {12, 6}, {13, 2}, {14, 4},
	}

	parts := []Partition{
		// P0: all 8 GPUs.
		{ID: 0, GPUs: eightGPUs()},
		// P1, P2: 4-GPU partitions (only counts matter here).
		{ID: 1, GPUs: nGPUs(1, 4)},
		{ID: 2, GPUs: nGPUs(5, 4)},
		// P3-P6: 2-GPU partitions.
		{ID: 3, GPUs: nGPUs(1, 2)},
		{ID: 4, GPUs: nGPUs(3, 2)},
		{ID: 5, GPUs: nGPUs(5, 2)},
		{ID: 6, GPUs: nGPUs(7, 2)},
	}
	for _, s := range single {
		parts = append(parts, Partition{
			ID:   s.partID,
			GPUs: []GPUInfo{{PhysicalID: s.physID, UUID: fmt.Sprintf("GPU-%d", s.physID), PCIBusID: fmt.Sprintf("0000:%02x:00.0", 0x40+s.physID)}},
		})
	}
	return parts
}

func eightGPUs() []GPUInfo { return nGPUs(1, 8) }

func nGPUs(start, count uint32) []GPUInfo {
	g := make([]GPUInfo, 0, count)
	for i := uint32(0); i < count; i++ {
		g = append(g, GPUInfo{PhysicalID: start + i})
	}
	return g
}

func TestSingleGPUPartitionForModuleID(t *testing.T) {
	parts := hgxPartitions()
	// moduleId (physicalId) -> expected single-GPU partition id, per the
	// scrambled mapping in hgxPartitions.
	want := map[uint32]uint32{
		8: 7, 5: 8, 1: 9, 7: 10, 3: 11, 6: 12, 2: 13, 4: 14,
	}
	for moduleID, wantPart := range want {
		t.Run(fmt.Sprintf("module%d", moduleID), func(t *testing.T) {
			got, err := SingleGPUPartitionForModuleID(moduleID, parts)
			if err != nil {
				t.Fatalf("SingleGPUPartitionForModuleID(%d) error: %v", moduleID, err)
			}
			if got.ID != wantPart {
				t.Fatalf("SingleGPUPartitionForModuleID(%d) = partition %d, want %d", moduleID, got.ID, wantPart)
			}
			if !got.IsSingleGPU() {
				t.Fatalf("resolved partition %d is not single-GPU", got.ID)
			}
			if got.GPUs[0].PhysicalID != moduleID {
				t.Fatalf("resolved partition %d GPU physicalId = %d, want %d", got.ID, got.GPUs[0].PhysicalID, moduleID)
			}
		})
	}
}

func TestSingleGPUPartitionNotFound(t *testing.T) {
	parts := hgxPartitions()
	_, err := SingleGPUPartitionForModuleID(99, parts)
	if !errors.Is(err, ErrPartitionNotFound) {
		t.Fatalf("SingleGPUPartitionForModuleID(99) error = %v, want ErrPartitionNotFound", err)
	}
}

func TestSingleGPUPartitionIgnoresMultiGPU(t *testing.T) {
	// A physicalId that only appears inside multi-GPU partitions must not
	// resolve: only single-GPU partitions are eligible. Build a list where
	// physicalId 42 appears in an 8-GPU partition but no single-GPU one.
	parts := []Partition{
		{ID: 0, GPUs: []GPUInfo{{PhysicalID: 42}, {PhysicalID: 43}}},
		{ID: 7, GPUs: []GPUInfo{{PhysicalID: 1}}},
	}
	if _, err := SingleGPUPartitionForModuleID(42, parts); !errors.Is(err, ErrPartitionNotFound) {
		t.Fatalf("expected ErrPartitionNotFound for module id only in multi-GPU partition, got %v", err)
	}
}

func TestSingleGPUPartitionDuplicate(t *testing.T) {
	// Two single-GPU partitions claiming the same physicalId is inconsistent
	// and must be reported rather than silently picking one.
	parts := []Partition{
		{ID: 7, GPUs: []GPUInfo{{PhysicalID: 3}}},
		{ID: 8, GPUs: []GPUInfo{{PhysicalID: 3}}},
	}
	_, err := SingleGPUPartitionForModuleID(3, parts)
	if err == nil {
		t.Fatal("expected error on duplicate physicalId, got nil")
	}
	if errors.Is(err, ErrPartitionNotFound) {
		t.Fatalf("duplicate should not be reported as not-found: %v", err)
	}
}

func TestResolvePartitionIDForVF(t *testing.T) {
	parts := hgxPartitions()

	// VF 0000:41:00.4 lives on PF 0000:41:00.0, which is module id 3 -> P11.
	pfForVF := func(vf string) (string, error) {
		switch vf {
		case "0000:41:00.4":
			return "0000:41:00.0", nil
		case "0000:81:00.5":
			return "0000:81:00.0", nil
		}
		return "", fmt.Errorf("unknown VF %s", vf)
	}
	moduleID := func(pf string) (uint32, error) {
		switch pf {
		case "0000:41:00.0":
			return 3, nil // -> partition 11
		case "0000:81:00.0":
			return 4, nil // -> partition 14
		}
		return 0, fmt.Errorf("unknown PF %s", pf)
	}

	tests := []struct {
		vf       string
		wantPart uint32
	}{
		{"0000:41:00.4", 11},
		{"0000:81:00.5", 14},
	}
	for _, tt := range tests {
		got, err := ResolvePartitionIDForVF(tt.vf, pfForVF, moduleID, parts)
		if err != nil {
			t.Fatalf("ResolvePartitionIDForVF(%s) error: %v", tt.vf, err)
		}
		if got != tt.wantPart {
			t.Fatalf("ResolvePartitionIDForVF(%s) = %d, want %d", tt.vf, got, tt.wantPart)
		}
	}
}

func TestPartitionForModuleIDs(t *testing.T) {
	parts := hgxPartitions()
	tests := []struct {
		name     string
		moduleID []uint32
		wantPart uint32
	}{
		{"single-gpu module 3", []uint32{3}, 11},
		{"2-gpu pair {1,2}", []uint32{1, 2}, 3},
		{"2-gpu pair {3,4}", []uint32{3, 4}, 4},
		{"2-gpu pair unordered {8,7}", []uint32{8, 7}, 6},
		{"4-gpu {1,2,3,4}", []uint32{1, 2, 3, 4}, 1},
		{"4-gpu {5,6,7,8}", []uint32{5, 6, 7, 8}, 2},
		{"all eight", []uint32{1, 2, 3, 4, 5, 6, 7, 8}, 0},
		{"duplicate ids collapse to the single-GPU set", []uint32{3, 3}, 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PartitionForModuleIDs(tt.moduleID, parts)
			if err != nil {
				t.Fatalf("PartitionForModuleIDs(%v) error: %v", tt.moduleID, err)
			}
			if got.ID != tt.wantPart {
				t.Fatalf("PartitionForModuleIDs(%v) = partition %d, want %d", tt.moduleID, got.ID, tt.wantPart)
			}
		})
	}
}

func TestPartitionForModuleIDsNoMatch(t *testing.T) {
	parts := hgxPartitions()
	// {2,3} is a valid pair of GPUs, but the fabric only offers {1,2}, {3,4},
	// {5,6}, {7,8} as 2-GPU partitions — {2,3} spans two of them, so no single
	// partition covers it.
	if _, err := PartitionForModuleIDs([]uint32{2, 3}, parts); !errors.Is(err, ErrNoMatchingPartition) {
		t.Fatalf("PartitionForModuleIDs({2,3}) error = %v, want ErrNoMatchingPartition", err)
	}
	// A 3-GPU set has no partition at all in this layout.
	if _, err := PartitionForModuleIDs([]uint32{1, 2, 3}, parts); !errors.Is(err, ErrNoMatchingPartition) {
		t.Fatalf("PartitionForModuleIDs({1,2,3}) error = %v, want ErrNoMatchingPartition", err)
	}
	// An unknown id.
	if _, err := PartitionForModuleIDs([]uint32{99}, parts); !errors.Is(err, ErrNoMatchingPartition) {
		t.Fatalf("PartitionForModuleIDs({99}) error = %v, want ErrNoMatchingPartition", err)
	}
	// Empty input.
	if _, err := PartitionForModuleIDs(nil, parts); !errors.Is(err, ErrNoMatchingPartition) {
		t.Fatalf("PartitionForModuleIDs(nil) error = %v, want ErrNoMatchingPartition", err)
	}
}

func TestPartitionForModuleIDsDuplicateMatch(t *testing.T) {
	// Two partitions covering the same GPU set is an inconsistent list and must
	// be reported, not silently resolved to one of them.
	parts := []Partition{
		{ID: 3, GPUs: nGPUs(1, 2)},
		{ID: 9, GPUs: nGPUs(1, 2)},
	}
	_, err := PartitionForModuleIDs([]uint32{1, 2}, parts)
	if err == nil {
		t.Fatal("expected error on duplicate matching partitions, got nil")
	}
	if errors.Is(err, ErrNoMatchingPartition) {
		t.Fatalf("duplicate match should not be reported as no-match: %v", err)
	}
}

func TestResolvePartitionIDForVFErrors(t *testing.T) {
	parts := hgxPartitions()
	okPF := func(string) (string, error) { return "0000:41:00.0", nil }
	okModule := func(string) (uint32, error) { return 3, nil }

	t.Run("pf lookup fails", func(t *testing.T) {
		want := errors.New("physfn missing")
		_, err := ResolvePartitionIDForVF("vf", func(string) (string, error) { return "", want }, okModule, parts)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want wrapping %v", err, want)
		}
	})

	t.Run("module id lookup fails", func(t *testing.T) {
		want := errors.New("nvml down")
		_, err := ResolvePartitionIDForVF("vf", okPF, func(string) (uint32, error) { return 0, want }, parts)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want wrapping %v", err, want)
		}
	})

	t.Run("no matching partition", func(t *testing.T) {
		_, err := ResolvePartitionIDForVF("vf", okPF, func(string) (uint32, error) { return 99, nil }, parts)
		if !errors.Is(err, ErrPartitionNotFound) {
			t.Fatalf("error = %v, want ErrPartitionNotFound", err)
		}
	})
}
