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

package fabricmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// SelectPreferredRequest contains the parameters for selecting preferred devices
// from fabric manager partitions. It replaces the kubelet plugin API request type
// with primitive fields to decouple partition orchestration from the plugin API.
type SelectPreferredRequest struct {
	AvailableDeviceIDs   []string
	MustIncludeDeviceIDs []string
	AllocationSize       int
}

// PartitionManager encapsulates fabric manager partition orchestration including
// partition selection, NUMA scoring, activation, and PCI-to-module translation.
type PartitionManager struct {
	mu            sync.Mutex
	client        Client
	pciToModuleID map[string]uint32
	moduleIDToPCI map[uint32]string
	deviceNUMAMap map[string]int64
}

// NewPartitionManager creates a new PartitionManager with the given client,
// PCI-to-module mappings, and device NUMA topology map.
func NewPartitionManager(
	client Client,
	pciToModuleID map[string]uint32,
	moduleIDToPCI map[uint32]string,
	deviceNUMAMap map[string]int64,
) *PartitionManager {
	return &PartitionManager{
		client:        client,
		pciToModuleID: pciToModuleID,
		moduleIDToPCI: moduleIDToPCI,
		deviceNUMAMap: deviceNUMAMap,
	}
}

// IsConnected returns true if the underlying fabric manager client is connected.
func (pm *PartitionManager) IsConnected() bool {
	return pm.client.IsConnected()
}

// Disconnect closes the connection to fabric manager.
func (pm *PartitionManager) Disconnect() error {
	return pm.client.Disconnect()
}

// GetPartitions retrieves all supported fabric partitions from the fabric manager.
func (pm *PartitionManager) GetPartitions(ctx context.Context) (*PartitionList, error) {
	return pm.client.GetPartitions(ctx)
}

// normalizePCIAddress normalizes a PCI BDF address to match the sysfs format
// used by the Linux kernel (4-digit lowercase hex domain). The mapping file
// produced by NVIDIA tooling may use an 8-digit domain and uppercase hex
// (e.g. "00000000:AB:00.0"), while sysfs uses "0000:ab:00.0".
func normalizePCIAddress(addr string) string {
	lower := strings.ToLower(addr)

	colonIdx := strings.Index(lower, ":")
	if colonIdx < 0 {
		return lower
	}

	domain := lower[:colonIdx]
	rest := lower[colonIdx:]

	domainVal, err := strconv.ParseUint(domain, 16, 32)
	if err != nil {
		return lower
	}

	if domainVal <= 0xFFFF {
		return fmt.Sprintf("%04x%s", domainVal, rest)
	}
	return fmt.Sprintf("%08x%s", domainVal, rest)
}

// LoadPCIModuleMapping reads the GPU PCI-to-module mapping JSON file produced
// by NVIDIA driver installation script. The file maps PCI BDF addresses to
// physical module IDs. Returns both forward (PCI->moduleID) and reverse
// (moduleID->PCI) maps. PCI addresses are normalized to the sysfs format
// (4-digit lowercase hex domain) to ensure consistent lookups.
func LoadPCIModuleMapping(path string) (map[string]uint32, map[uint32]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read PCI module mapping file: %w", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("failed to parse PCI module mapping JSON: %w", err)
	}

	pciToModule := make(map[string]uint32, len(raw))
	moduleToPCI := make(map[uint32]string, len(raw))

	for pciAddr, moduleIDStr := range raw {
		moduleID, err := strconv.ParseUint(moduleIDStr, 10, 32)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid module ID %q for PCI address %s: %w", moduleIDStr, pciAddr, err)
		}
		normalized := normalizePCIAddress(pciAddr)
		pciToModule[normalized] = uint32(moduleID)
		moduleToPCI[uint32(moduleID)] = normalized
	}

	return pciToModule, moduleToPCI, nil
}

// ActivateForDevices handles fabric partition activation for the given devices.
// It converts PCI addresses to module IDs and matches against partition GPU PhysicalIDs,
// since the FM SDK returns empty PCIBusID values in partition data.
func (pm *PartitionManager) ActivateForDevices(ctx context.Context, deviceIDs []string) error {
	if len(deviceIDs) == 0 {
		return nil
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	deviceModuleIDs := make(map[uint32]struct{}, len(deviceIDs))
	for _, pciAddr := range deviceIDs {
		moduleID, ok := pm.pciToModuleID[pciAddr]
		if !ok {
			return fmt.Errorf("no module ID mapping for PCI address %s", pciAddr)
		}
		deviceModuleIDs[moduleID] = struct{}{}
	}

	partitions, err := pm.client.GetPartitions(ctx)
	if err != nil {
		return fmt.Errorf("failed to get fabric partitions: %w", err)
	}

	var matchedPartition *Partition
	for i, partition := range partitions.Partitions {
		if int(partition.NumGPUs) != len(deviceIDs) {
			continue
		}
		matched := 0
		for _, gpu := range partition.GPUs {
			if _, ok := deviceModuleIDs[gpu.PhysicalID]; ok {
				matched++
			}
		}
		if matched == len(deviceIDs) {
			matchedPartition = &partitions.Partitions[i]
			break
		}
	}

	if matchedPartition == nil {
		return fmt.Errorf("no partition of size %d found containing all devices %v", len(deviceIDs), deviceIDs)
	}

	// Deactivate any active partitions that contain any of the requested devices.
	// A partition must be inactive before it can be activated, and different
	// partition configurations may reference the same physical GPU.
	for _, partition := range partitions.Partitions {
		if !partition.IsActive {
			continue
		}
		for _, gpu := range partition.GPUs {
			if _, ok := deviceModuleIDs[gpu.PhysicalID]; ok {
				log.Printf("Deactivating active partition %d before activation", partition.PartitionID)
				if err := pm.client.DeactivatePartition(ctx, partition.PartitionID); err != nil {
					return fmt.Errorf("failed to deactivate partition %d: %w", partition.PartitionID, err)
				}
				break
			}
		}
	}

	req := &ActivateRequest{
		PartitionID: matchedPartition.PartitionID,
	}

	if err := pm.client.ActivatePartition(ctx, req); err != nil {
		return fmt.Errorf("failed to activate partition %d: %w", matchedPartition.PartitionID, err)
	}

	return nil
}

// SelectPreferred selects preferred devices based on fabric manager partitions.
// It finds FM partitions whose size exactly matches the allocation size and whose GPUs are
// all available, then picks the partition with the best NUMA locality (fewest distinct NUMA
// nodes, tie-broken by lowest NUMA node ID).
//
// Because the FM SDK returns empty PCIBusID values in partition GPU data, this function
// uses the PCI-to-module-ID mapping loaded at startup to translate between PCI addresses
// (used by kubelet) and physical module IDs (used by FM partitions via GPU.PhysicalID).
func (pm *PartitionManager) SelectPreferred(
	ctx context.Context,
	req *SelectPreferredRequest,
) ([]string, error) {
	partitions, err := pm.client.GetPartitions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get fabric partitions: %w", err)
	}

	availableModuleIDs := pciAddrsToModuleIDSet(req.AvailableDeviceIDs, pm.pciToModuleID)
	mustIncludeModuleIDs := pciAddrsToModuleIDSet(req.MustIncludeDeviceIDs, pm.pciToModuleID)

	if len(availableModuleIDs) != len(req.AvailableDeviceIDs) {
		log.Printf("WARNING: PCI-to-module translation: only %d/%d available devices resolved to module IDs",
			len(availableModuleIDs), len(req.AvailableDeviceIDs))
	}

	var candidates []partitionCandidate
	for _, partition := range partitions.Partitions {
		if int(partition.NumGPUs) != req.AllocationSize {
			continue
		}
		if !allPartitionGPUsAvailable(partition, availableModuleIDs) {
			continue
		}
		if !partitionContainsAllModuleIDs(partition, mustIncludeModuleIDs) {
			continue
		}

		distinctNodes, minNUMANode := computeNUMALocality(partition, pm.moduleIDToPCI, pm.deviceNUMAMap)
		candidates = append(candidates, partitionCandidate{
			partition:     partition,
			distinctNodes: distinctNodes,
			minNUMANode:   minNUMANode,
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no fabric manager partition of size %d found for available devices",
			req.AllocationSize)
	}

	sortCandidatesByNUMALocality(candidates)

	best := candidates[0]
	log.Printf("Selected FM partition %d (NUMA nodes: %d, min NUMA: %d) from %d candidates",
		best.partition.PartitionID, best.distinctNodes, best.minNUMANode, len(candidates))

	return pm.buildPreferredDeviceList(best.partition, req), nil
}

// buildPreferredDeviceList constructs the ordered list of preferred device PCI
// addresses from the selected partition, placing must-include devices first and
// filling the remainder from available devices in request order.
func (pm *PartitionManager) buildPreferredDeviceList(
	partition Partition,
	req *SelectPreferredRequest,
) []string {
	partitionPCISet := make(map[string]struct{}, len(partition.GPUs))
	for _, gpu := range partition.GPUs {
		if pciAddr, ok := pm.moduleIDToPCI[gpu.PhysicalID]; ok {
			partitionPCISet[pciAddr] = struct{}{}
		}
	}

	var preferred []string
	added := make(map[string]struct{})

	for _, id := range req.MustIncludeDeviceIDs {
		if _, exists := added[id]; exists {
			continue
		}
		added[id] = struct{}{}
		preferred = append(preferred, id)
	}

	for _, id := range req.AvailableDeviceIDs {
		if len(preferred) >= req.AllocationSize {
			break
		}
		if _, exists := added[id]; exists {
			continue
		}
		if _, inPartition := partitionPCISet[id]; !inPartition {
			continue
		}
		added[id] = struct{}{}
		preferred = append(preferred, id)
	}

	return preferred
}

// partitionCandidate holds a partition and its NUMA locality score for ranking.
type partitionCandidate struct {
	partition     Partition
	distinctNodes int
	minNUMANode   int64
}

// pciAddrsToModuleIDSet converts a slice of PCI addresses to a set of module IDs
// using the provided PCI-to-module mapping.
func pciAddrsToModuleIDSet(addrs []string, pciToModuleID map[string]uint32) map[uint32]struct{} {
	result := make(map[uint32]struct{}, len(addrs))
	for _, addr := range addrs {
		if moduleID, ok := pciToModuleID[addr]; ok {
			result[moduleID] = struct{}{}
		}
	}
	return result
}

// allPartitionGPUsAvailable returns true if every GPU in the partition has its
// PhysicalID present in the available set.
func allPartitionGPUsAvailable(partition Partition, available map[uint32]struct{}) bool {
	for _, gpu := range partition.GPUs {
		if _, ok := available[gpu.PhysicalID]; !ok {
			return false
		}
	}
	return true
}

// partitionContainsAllModuleIDs returns true if every module ID in required is
// present among the partition's GPU PhysicalIDs.
func partitionContainsAllModuleIDs(partition Partition, required map[uint32]struct{}) bool {
	partitionIDs := make(map[uint32]struct{}, len(partition.GPUs))
	for _, gpu := range partition.GPUs {
		partitionIDs[gpu.PhysicalID] = struct{}{}
	}
	for id := range required {
		if _, ok := partitionIDs[id]; !ok {
			return false
		}
	}
	return true
}

// computeNUMALocality calculates the NUMA locality score for a partition by
// translating GPU PhysicalIDs to PCI addresses and looking up their NUMA nodes.
// Returns the number of distinct NUMA nodes and the lowest NUMA node ID (-1 if
// no topology info is available).
func computeNUMALocality(
	partition Partition,
	moduleIDToPCI map[uint32]string,
	deviceToNUMA map[string]int64,
) (distinctNodes int, minNUMANode int64) {
	numaNodes := make(map[int64]struct{})
	minNUMANode = -1
	for _, gpu := range partition.GPUs {
		node := int64(-1)
		if pciAddr, ok := moduleIDToPCI[gpu.PhysicalID]; ok {
			if n, ok := deviceToNUMA[pciAddr]; ok {
				node = n
			}
		}
		numaNodes[node] = struct{}{}
		if node >= 0 && (minNUMANode == -1 || node < minNUMANode) {
			minNUMANode = node
		}
	}
	return len(numaNodes), minNUMANode
}

// sortCandidatesByNUMALocality sorts candidates preferring fewest distinct NUMA
// nodes first, then lowest NUMA node ID as tiebreaker. Candidates without
// topology info (minNUMANode == -1) sort last.
func sortCandidatesByNUMALocality(candidates []partitionCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distinctNodes != candidates[j].distinctNodes {
			return candidates[i].distinctNodes < candidates[j].distinctNodes
		}
		mi, mj := candidates[i].minNUMANode, candidates[j].minNUMANode
		if mi == -1 && mj != -1 {
			return false
		}
		if mj == -1 && mi != -1 {
			return true
		}
		return mi < mj
	})
}
