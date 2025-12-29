//go:build linux
// +build linux

/*
 * Copyright (c) 2024, NVIDIA CORPORATION. All rights reserved.
 *
 * This file provides CGO bindings to the NVIDIA Fabric Manager SDK.
 * The FM SDK is a C library (libnvfm) that provides APIs for partition management.
 *
 * Build requirements:
 * - libnvfm-dev package installed (provides /usr/include/nv_fm_agent.h)
 * - Link with -lnvfm
 */

package fabric_manager

/*
#cgo CFLAGS: -I/usr/include
#cgo LDFLAGS: -lnvfm

#include <stdlib.h>
#include <string.h>
#include "nv_fm_agent.h"
*/
import "C"

import (
	"fmt"
	"sync"
	"time"
)

// NativeFMClient implements FMClient using the native FM C SDK via CGO.
type NativeFMClient struct {
	mu        sync.Mutex
	handle    C.fmHandle_t
	connected bool
	address   string
}

// NewNativeFMClient creates a new native FM client.
func NewNativeFMClient() *NativeFMClient {
	return &NativeFMClient{}
}

// Connect establishes connection to Fabric Manager daemon.
func (c *NativeFMClient) Connect(address string, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil // Already connected
	}

	// Initialize FM library
	ret := C.fmLibInit()
	if ret != C.FM_ST_SUCCESS {
		return &FMError{Code: int(ret), Message: "fmLibInit failed"}
	}

	// Prepare connection parameters
	var params C.fmConnectParams_t
	params.version = C.fmConnectParams_version
	params.timeoutMs = C.uint(timeout.Milliseconds())
	params.addressIsUnixSocket = 0 // Use TCP/IP
	params.addressType = 1         // NV_FM_API_ADDR_TYPE_INET (API v2)

	// Copy address to C struct (e.g., "127.0.0.1" for localhost)
	addrBytes := []byte(address)
	if len(addrBytes) > 255 {
		addrBytes = addrBytes[:255]
	}
	for i, b := range addrBytes {
		params.addressInfo[i] = C.char(b)
	}
	params.addressInfo[len(addrBytes)] = 0

	// Connect to FM daemon
	ret = C.fmConnect(&params, &c.handle)
	if ret != C.FM_ST_SUCCESS {
		C.fmLibShutdown()
		return &FMError{Code: int(ret), Message: fmt.Sprintf("fmConnect to %s failed", address)}
	}

	c.connected = true
	c.address = address
	return nil
}

// DiscoverPartitions queries FM for all supported partitions.
func (c *NativeFMClient) DiscoverPartitions() ([]Partition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected to FM")
	}

	var partitionList C.fmFabricPartitionList_t
	partitionList.version = C.fmFabricPartitionList_version

	ret := C.fmGetSupportedFabricPartitions(c.handle, &partitionList)
	if ret != C.FM_ST_SUCCESS {
		return nil, &FMError{Code: int(ret), Message: "fmGetSupportedFabricPartitions failed"}
	}

	// Convert C struct to Go
	partitions := make([]Partition, 0, partitionList.numPartitions)
	for i := C.uint(0); i < partitionList.numPartitions; i++ {
		info := partitionList.partitionInfo[i]

		physicalIDs := make([]int, 0, info.numGpus)
		for j := C.uint(0); j < info.numGpus; j++ {
			physicalIDs = append(physicalIDs, int(info.gpuInfo[j].physicalId))
		}

		partitions = append(partitions, Partition{
			ID:          int(info.partitionId),
			PhysicalIDs: physicalIDs,
			NumGPUs:     int(info.numGpus),
			IsActive:    info.isActive != 0,
		})
	}

	return partitions, nil
}

// DiscoverPCIMapping discovers Physical ID to PCI BDF mapping.
// Note: On H100+, FM typically returns empty PCI BDF - use nvidia-smi for mapping.
func (c *NativeFMClient) DiscoverPCIMapping() (map[int]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected to FM")
	}

	var partitionList C.fmFabricPartitionList_t
	partitionList.version = C.fmFabricPartitionList_version

	ret := C.fmGetSupportedFabricPartitions(c.handle, &partitionList)
	if ret != C.FM_ST_SUCCESS {
		return nil, &FMError{Code: int(ret), Message: "fmGetSupportedFabricPartitions failed"}
	}

	mapping := make(map[int]string)
	for i := C.uint(0); i < partitionList.numPartitions; i++ {
		info := partitionList.partitionInfo[i]
		for j := C.uint(0); j < info.numGpus; j++ {
			gpu := info.gpuInfo[j]
			physicalID := int(gpu.physicalId)
			pciBusID := C.GoString(&gpu.pciBusId[0])

			if pciBusID != "" && mapping[physicalID] == "" {
				mapping[physicalID] = pciBusID
			}
		}
	}

	return mapping, nil
}

// ActivatePartition activates a partition by ID.
func (c *NativeFMClient) ActivatePartition(partitionID int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return fmt.Errorf("not connected to FM")
	}

	ret := C.fmActivateFabricPartition(c.handle, C.fmFabricPartitionId_t(partitionID))
	if ret != C.FM_ST_SUCCESS {
		return &FMError{Code: int(ret), Message: fmt.Sprintf("fmActivateFabricPartition(%d) failed", partitionID)}
	}

	return nil
}

// DeactivatePartition deactivates a partition by ID.
func (c *NativeFMClient) DeactivatePartition(partitionID int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return fmt.Errorf("not connected to FM")
	}

	ret := C.fmDeactivateFabricPartition(c.handle, C.fmFabricPartitionId_t(partitionID))
	if ret != C.FM_ST_SUCCESS {
		return &FMError{Code: int(ret), Message: fmt.Sprintf("fmDeactivateFabricPartition(%d) failed", partitionID)}
	}

	return nil
}

// GetActivePartitions returns currently active partition IDs.
// Note: We track this locally since fmGetActivatedFabricPartitions may not be available.
// in all FM SDK versions.
func (c *NativeFMClient) GetActivePartitions() ([]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected to FM")
	}

	// Query partition list and return those marked active
	var partitionList C.fmFabricPartitionList_t
	partitionList.version = C.fmFabricPartitionList_version

	ret := C.fmGetSupportedFabricPartitions(c.handle, &partitionList)
	if ret != C.FM_ST_SUCCESS {
		return nil, &FMError{Code: int(ret), Message: "fmGetSupportedFabricPartitions failed"}
	}

	var activeIDs []int
	for i := C.uint(0); i < partitionList.numPartitions; i++ {
		info := partitionList.partitionInfo[i]
		if info.isActive != 0 {
			activeIDs = append(activeIDs, int(info.partitionId))
		}
	}

	return activeIDs, nil
}

// Ensure NativeFMClient implements FMClient
var _ FMClient = (*NativeFMClient)(nil)
