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

package fabric_manager

import (
	"fmt"
	"log"
	"time"
)

// FMClient is the interface for Fabric Manager operations.
// Implementations can use either the native SDK (CGO) or CLI wrapper.
type FMClient interface {
	// Connect establishes connection to Fabric Manager daemon.
	Connect(address string, timeout time.Duration) error

	// DiscoverPartitions queries FM for all supported partitions.
	DiscoverPartitions() ([]Partition, error)

	// DiscoverPCIMapping queries FM/system for Physical ID to PCI BDF mapping.
	DiscoverPCIMapping() (map[int]string, error)

	// ActivatePartition activates a partition by ID.
	ActivatePartition(partitionID int) error

	// DeactivatePartition deactivates a partition by ID.
	DeactivatePartition(partitionID int) error

	// GetActivePartitions returns currently active partition IDs.
	GetActivePartitions() ([]int, error)
}

// FMError represents a Fabric Manager error.
type FMError struct {
	Code    int
	Message string
}

func (e *FMError) Error() string {
	return fmt.Sprintf("FM error %d: %s", e.Code, e.Message)
}

// PartitionManager combines FMClient with PartitionTree for full management.
type PartitionManager struct {
	client     FMClient
	tree       *PartitionTree
	reconciler *GPUReconciler
}

// NewPartitionManager creates a new partition manager.
func NewPartitionManager(client FMClient) *PartitionManager {
	return &PartitionManager{
		client: client,
		tree:   NewPartitionTree(),
	}
}

// Initialize connects to FM and discovers partitions.
func (m *PartitionManager) Initialize(fmAddress string, discoveredDevices []string) error {
	// Connect to FM
	if err := m.client.Connect(fmAddress, 5*time.Second); err != nil {
		return fmt.Errorf("failed to connect to FM: %w", err)
	}

	// Discover partitions
	partitions, err := m.client.DiscoverPartitions()
	if err != nil {
		return fmt.Errorf("failed to discover partitions: %w", err)
	}

	numGPUs := len(discoveredDevices)
	log.Printf("[FM] Discovered %d partitions from FM, filtering for %d GPUs", len(partitions), numGPUs)

	// Filter partitions to only include those with valid PhysicalIDs (1 to numGPUs inclusive)
	// FM uses 1-based Physical IDs: GPU 0 = Physical ID 1, GPU 7 = Physical ID 8
	var validPartitions []Partition
	for _, p := range partitions {
		valid := true
		for _, pid := range p.PhysicalIDs {
			if pid < 1 || pid > numGPUs {
				log.Printf("[FM] Partition %d skipped: contains invalid PhysicalID %d (valid range: 1-%d)", p.ID, pid, numGPUs)
				valid = false
				break
			}
		}
		if valid {
			validPartitions = append(validPartitions, p)
			log.Printf("[FM] Partition %d: %d GPUs, PhysicalIDs=%v", p.ID, p.NumGPUs, p.PhysicalIDs)
		}
	}

	log.Printf("[FM] %d valid partitions after filtering", len(validPartitions))

	// Build tree from valid partitions only
	if err := m.tree.BuildFromPartitions(validPartitions); err != nil {
		return fmt.Errorf("failed to build partition tree: %w", err)
	}

	// Discover PCI mapping
	pciMapping, err := m.client.DiscoverPCIMapping()
	if err != nil {
		return fmt.Errorf("failed to discover PCI mapping: %w", err)
	}

	// Check if PCI mapping is empty/dummy (common on H100) and fallback to discovered devices
	if len(pciMapping) > 0 && len(discoveredDevices) > 0 {
		// Check for dummy values (e.g. "00000000:00:00.0")
		isDummy := false
		for _, bdf := range pciMapping {
			if bdf == "00000000:00:00.0" || bdf == "" {
				isDummy = true
				break
			}
		}

		if isDummy {
			fmt.Printf("WARNING: FM returned dummy PCI BDFs. Falling back to manual mapping using %d discovered devices.\n", len(discoveredDevices))
			// Clear and rebuild mapping based on device index
			// FM uses 1-based Physical IDs: GPU 0 = Physical ID 1, GPU 7 = Physical ID 8
			pciMapping = make(map[int]string)
			for i, bdf := range discoveredDevices {
				pciMapping[i+1] = bdf // 1-based Physical IDs!
			}
		}
	} else if len(pciMapping) == 0 && len(discoveredDevices) > 0 {
		// No mapping from FM, use fallback
		fmt.Printf("WARNING: FM returned no PCI mapping. Falling back to manual mapping using %d discovered devices.\n", len(discoveredDevices))
		// FM uses 1-based Physical IDs
		pciMapping = make(map[int]string)
		for i, bdf := range discoveredDevices {
			pciMapping[i+1] = bdf // 1-based Physical IDs!
		}
	}

	for physicalID, pciBDF := range pciMapping {
		m.tree.SetPCIMapping(physicalID, pciBDF)
	}

	// Sync with FM's active partitions
	activeIDs, err := m.client.GetActivePartitions()
	if err != nil {
		return fmt.Errorf("failed to get active partitions: %w", err)
	}

	if len(activeIDs) > 0 {
		log.Printf("[FM] Found %d active partition(s) from FM: %v", len(activeIDs), activeIDs)
	}

	for _, id := range activeIDs {
		// Mark as active but with unknown pod (will reconcile later)
		if p, ok := m.tree.partitions[id]; ok {
			p.IsActive = true
			log.Printf("[FM] Marking partition %d as active (GPUs: %v)", id, p.PhysicalIDs)
		}
	}

	return nil
}

// StartReconciler starts the GPU reconciler that monitors for pod deletions.
// resourceName is the device plugin resource name (e.g., "nvidia.com/GH100_H100_SXM5_80GB").
func (m *PartitionManager) StartReconciler(resourceName string) {
	if m.reconciler != nil {
		log.Printf("[FM] Reconciler already started")
		return
	}
	m.reconciler = NewGPUReconciler(m, resourceName)
	m.reconciler.Start()
	log.Printf("[FM] Started GPU reconciler for resource %s", resourceName)
}

// TrackAllocation records a GPU allocation for reconciliation.
func (m *PartitionManager) TrackAllocation(podUID string, gpuBDFs []string) {
	if m.reconciler != nil {
		m.reconciler.TrackAllocation(podUID, gpuBDFs)
	}
}

// ReconcileNow performs an immediate synchronous reconciliation.
// Call this before allocation to ensure orphaned partitions are cleaned up.
func (m *PartitionManager) ReconcileNow() {
	if m.reconciler != nil {
		m.reconciler.ReconcileNow()
	}
}

// GetTree returns the partition tree.
func (m *PartitionManager) GetTree() *PartitionTree {
	return m.tree
}

// DeallocatePartition deactivates the partition for a pod.
func (m *PartitionManager) DeallocatePartition(podUID string) error {
	partitionID, ok := m.tree.GetPartitionForPod(podUID)
	if !ok {
		return nil // No partition for this pod
	}

	// Deactivate in FM
	if err := m.client.DeactivatePartition(partitionID); err != nil {
		return fmt.Errorf("failed to deactivate partition %d: %w", partitionID, err)
	}

	// Mark as inactive in tree
	return m.tree.MarkInactive(partitionID)
}

// ActivatePartitionForGPUs activates the partition containing specific GPUs.
// All GPUs must belong to a single partition - multi-partition allocation is NOT supported.
// If no single partition contains all GPUs, an error is returned.
func (m *PartitionManager) ActivatePartitionForGPUs(pciBDFs []string, podUID string) error {
	partition, err := m.tree.FindPartitionForGPUs(pciBDFs)
	if err != nil {
		return err
	}

	log.Printf("[FM] Activating partition %d for %d GPUs (PhysicalIDs: %v)", partition.ID, len(pciBDFs), partition.PhysicalIDs)

	// Activate in FM
	if err := m.client.ActivatePartition(partition.ID); err != nil {
		return fmt.Errorf("failed to activate partition %d: %w", partition.ID, err)
	}

	// Mark as active in tree
	if err := m.tree.MarkActive(partition.ID, podUID); err != nil {
		_ = m.client.DeactivatePartition(partition.ID)
		return err
	}

	log.Printf("[FM] Successfully activated partition %d", partition.ID)
	return nil
}
