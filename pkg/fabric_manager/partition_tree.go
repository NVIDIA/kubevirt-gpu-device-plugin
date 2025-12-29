/*
 * Copyright (c) 2024, NVIDIA CORPORATION. All rights reserved.
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

// Package fabric_manager provides integration with NVIDIA Fabric Manager
// for GPU partition management in multi-tenant NVSwitch environments.
package fabric_manager

import (
	"fmt"
	"log"
	"sort"
)

// Partition represents a GPU fabric partition from Fabric Manager.
// Partitions form a tree structure where larger partitions contain smaller ones.
type Partition struct {
	ID          int   // FM partition ID
	PhysicalIDs []int // GPU physical IDs in this partition
	NumGPUs     int   // Number of GPUs
	IsActive    bool  // Currently activated
	Parent      int   // Parent partition ID (-1 if root)
	Children    []int // Child partition IDs
}

// PartitionTree represents the hierarchical structure of GPU partitions.
// The tree is auto-discovered from Fabric Manager, not hardcoded.
type PartitionTree struct {
	partitions     map[int]*Partition // ID -> Partition
	physicalToPCI  map[int]string     // Physical ID -> PCI BDF
	pciToPhysical  map[string]int     // PCI BDF -> Physical ID
	activeByPod    map[string]int     // Pod UID -> Partition ID
	partitionByPod map[int]string     // Partition ID -> Pod UID (reverse)
}

// NewPartitionTree creates an empty partition tree.
// Call DiscoverPartitions() to populate from Fabric Manager.
func NewPartitionTree() *PartitionTree {
	return &PartitionTree{
		partitions:     make(map[int]*Partition),
		physicalToPCI:  make(map[int]string),
		pciToPhysical:  make(map[string]int),
		activeByPod:    make(map[string]int),
		partitionByPod: make(map[int]string),
	}
}

// BuildFromPartitions builds the tree from a list of discovered partitions.
// It automatically determines parent-child relationships based on GPU containment.
func (t *PartitionTree) BuildFromPartitions(partitions []Partition) error {
	// Store all partitions
	for i := range partitions {
		p := partitions[i]
		p.Parent = -1 // Will be computed
		p.Children = nil
		t.partitions[p.ID] = &p
	}

	// Build parent-child relationships based on GPU set containment
	// A partition P is parent of partition C if:
	// 1. P.GPUs is a superset of C.GPUs
	// 2. P.NumGPUs > C.NumGPUs
	// 3. No other partition Q exists where P ⊃ Q ⊃ C

	ids := t.sortedPartitionIDs()

	for _, childID := range ids {
		child := t.partitions[childID]
		var bestParent *Partition

		for _, parentID := range ids {
			if parentID == childID {
				continue
			}
			parent := t.partitions[parentID]

			// Parent must have more GPUs
			if parent.NumGPUs <= child.NumGPUs {
				continue
			}

			// Parent must contain all child's GPUs
			if !t.isSuperset(parent.PhysicalIDs, child.PhysicalIDs) {
				continue
			}

			// Find the smallest containing parent (immediate parent)
			if bestParent == nil || parent.NumGPUs < bestParent.NumGPUs {
				bestParent = parent
			}
		}

		if bestParent != nil {
			child.Parent = bestParent.ID
			bestParent.Children = append(bestParent.Children, childID)
		}
	}

	return nil
}

// SetPCIMapping sets the Physical ID to PCI BDF mapping.
// This mapping is discovered from GPU hardware, not hardcoded.
func (t *PartitionTree) SetPCIMapping(physicalID int, pciBDF string) {
	t.physicalToPCI[physicalID] = pciBDF
	t.pciToPhysical[pciBDF] = physicalID
}

// FindPartitionForGPUs finds the best partition for a set of GPUs (by PCI BDF).
// Returns the partition with exactly matching GPUs, or error if none exists.
func (t *PartitionTree) FindPartitionForGPUs(pciBDFs []string) (*Partition, error) {
	// Convert PCI BDFs to physical IDs
	physicalIDs := make([]int, 0, len(pciBDFs))
	for _, bdf := range pciBDFs {
		pid, ok := t.pciToPhysical[bdf]
		if !ok {
			return nil, fmt.Errorf("unknown PCI device: %s", bdf)
		}
		physicalIDs = append(physicalIDs, pid)
	}

	// Find partition with exact match
	for _, p := range t.partitions {
		if t.setsEqual(p.PhysicalIDs, physicalIDs) {
			return p, nil
		}
	}

	return nil, fmt.Errorf("no partition found for GPUs: %v", physicalIDs)
}

// GetAvailablePartitions returns partitions that can be activated
// (not already active and no parent/child conflict).
func (t *PartitionTree) GetAvailablePartitions(numGPUs int) []*Partition {
	var available []*Partition

	for _, p := range t.partitions {
		if p.NumGPUs != numGPUs {
			continue
		}
		if t.isPartitionAvailable(p.ID) {
			available = append(available, p)
		} else {
			// Debug: log why partition is not available
			reason := "unknown"
			if p.IsActive {
				reason = "already active"
			} else if parent := t.partitions[p.Parent]; parent != nil && parent.IsActive {
				reason = fmt.Sprintf("parent partition %d is active", p.Parent)
			} else if t.hasActiveDescendant(p.ID) {
				reason = "has active descendant"
			}
			log.Printf("[FM] Partition %d (%d GPUs) not available: %s", p.ID, p.NumGPUs, reason)
		}
	}

	// Sort by partition ID for deterministic ordering
	sort.Slice(available, func(i, j int) bool {
		return available[i].ID < available[j].ID
	})

	return available
}

// GetGPUsForPartition returns PCI BDFs for a partition.
func (t *PartitionTree) GetGPUsForPartition(partitionID int) ([]string, error) {
	p, ok := t.partitions[partitionID]
	if !ok {
		return nil, fmt.Errorf("partition %d not found", partitionID)
	}

	bdfs := make([]string, 0, len(p.PhysicalIDs))
	for _, pid := range p.PhysicalIDs {
		bdf, ok := t.physicalToPCI[pid]
		if !ok {
			return nil, fmt.Errorf("no PCI mapping for physical ID %d", pid)
		}
		bdfs = append(bdfs, bdf)
	}

	return bdfs, nil
}

// MarkActive marks a partition as active for a pod.
func (t *PartitionTree) MarkActive(partitionID int, podUID string) error {
	p, ok := t.partitions[partitionID]
	if !ok {
		return fmt.Errorf("partition %d not found", partitionID)
	}

	p.IsActive = true
	t.activeByPod[podUID] = partitionID
	t.partitionByPod[partitionID] = podUID

	return nil
}

// MarkInactive marks a partition as inactive.
func (t *PartitionTree) MarkInactive(partitionID int) error {
	p, ok := t.partitions[partitionID]
	if !ok {
		return fmt.Errorf("partition %d not found", partitionID)
	}

	p.IsActive = false

	// Remove pod mapping
	if podUID, ok := t.partitionByPod[partitionID]; ok {
		delete(t.activeByPod, podUID)
		delete(t.partitionByPod, partitionID)
	}

	return nil
}

// GetActivePartitions returns all currently active partition IDs.
func (t *PartitionTree) GetActivePartitions() []int {
	var active []int
	for id, p := range t.partitions {
		if p.IsActive {
			active = append(active, id)
		}
	}
	return active
}

// GetPartitionForPod returns the partition ID for a pod.
func (t *PartitionTree) GetPartitionForPod(podUID string) (int, bool) {
	id, ok := t.activeByPod[podUID]
	return id, ok
}

// isPartitionAvailable checks if a partition can be activated.
func (t *PartitionTree) isPartitionAvailable(partitionID int) bool {
	p := t.partitions[partitionID]
	if p == nil || p.IsActive {
		return false
	}

	// Check if any ancestor is active (would conflict)
	current := p.Parent
	for current != -1 {
		ancestor := t.partitions[current]
		if ancestor != nil && ancestor.IsActive {
			return false
		}
		if ancestor != nil {
			current = ancestor.Parent
		} else {
			break
		}
	}

	// Check if any descendant is active (would conflict)
	if t.hasActiveDescendant(partitionID) {
		return false
	}

	return true
}

// hasActiveDescendant checks if any child partition is active (recursively).
func (t *PartitionTree) hasActiveDescendant(partitionID int) bool {
	p := t.partitions[partitionID]
	if p == nil {
		return false
	}

	for _, childID := range p.Children {
		child := t.partitions[childID]
		if child != nil && child.IsActive {
			return true
		}
		if t.hasActiveDescendant(childID) {
			return true
		}
	}

	return false
}

// isSuperset checks if a is a superset of b.
func (t *PartitionTree) isSuperset(a, b []int) bool {
	set := make(map[int]bool)
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		if !set[v] {
			return false
		}
	}
	return true
}

// setsEqual checks if two int slices contain the same elements.
func (t *PartitionTree) setsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := make([]int, len(a))
	bCopy := make([]int, len(b))
	copy(aCopy, a)
	copy(bCopy, b)
	sort.Ints(aCopy)
	sort.Ints(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}

// sortedPartitionIDs returns partition IDs sorted by NumGPUs (ascending).
func (t *PartitionTree) sortedPartitionIDs() []int {
	ids := make([]int, 0, len(t.partitions))
	for id := range t.partitions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return t.partitions[ids[i]].NumGPUs < t.partitions[ids[j]].NumGPUs
	})
	return ids
}
