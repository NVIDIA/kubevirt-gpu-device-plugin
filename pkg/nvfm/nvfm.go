//go:build nvfm

/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
 *
 * This file provides the CGO bindings to the NVIDIA Fabric Manager SDK. The
 * Fabric Manager SDK is a shared library, i.e. a set of C/C++ APIs (SDK), and
 * the corresponding header files. The library and APIs are used to interface
 * with FM when FM runs in the shared NVSwitch and vGPU multi-tenant modes to
 * query, activate, and deactivate GPU partitions.
 *
 *   https://docs.nvidia.com/datacenter/tesla/fabric-manager-user-guide/index.html#fabric-manager-sdk
 *
 * Requirements:
 * - libnvfm.so library installed, it provides /usr/include/{nv_fm_agent.h,nv_fm_types.h}
 * - to be linked with the flag: -lnvfm
 */

package nvfm

/*
#cgo CPPFLAGS: -I/usr/include
#cgo LDFLAGS: -L/usr/lib -lnvfm

#include <stdlib.h>
#include "nv_fm_agent.h"
#include "nv_fm_types.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Return represents a Fabric Manager API return code.
type Return int32

const (
	Success                        Return = C.FM_ST_SUCCESS
	BadParam                       Return = C.FM_ST_BADPARAM
	GenericError                   Return = C.FM_ST_GENERIC_ERROR
	NotSupported                   Return = C.FM_ST_NOT_SUPPORTED
	Uninitialized                  Return = C.FM_ST_UNINITIALIZED
	Timeout                        Return = C.FM_ST_TIMEOUT
	VersionMismatch                Return = C.FM_ST_VERSION_MISMATCH
	InUse                          Return = C.FM_ST_IN_USE
	NotConfigured                  Return = C.FM_ST_NOT_CONFIGURED
	ConnectionNotValid             Return = C.FM_ST_CONNECTION_NOT_VALID
	NVLinkError                    Return = C.FM_ST_NVLINK_ERROR
	ResourceBad                    Return = C.FM_ST_RESOURCE_BAD
	ResourceInUse                  Return = C.FM_ST_RESOURCE_IN_USE
	ResourceNotInUse               Return = C.FM_ST_RESOURCE_NOT_IN_USE
	ResourceExhausted              Return = C.FM_ST_RESOURCE_EXHAUSTED
	ResourceNotReady               Return = C.FM_ST_RESOURCE_NOT_READY
	PartitionExists                Return = C.FM_ST_PARTITION_EXISTS
	PartitionIDInUse               Return = C.FM_ST_PARTITION_ID_IN_USE
	PartitionIDNotInUse            Return = C.FM_ST_PARTITION_ID_NOT_IN_USE
	PartitionNameInUse             Return = C.FM_ST_PARTITION_NAME_IN_USE
	PartitionNameNotInUse          Return = C.FM_ST_PARTITION_NAME_NOT_IN_USE
	PartitionIDNameMismatch        Return = C.FM_ST_PARTITION_ID_NAME_MISMATCH
	NotReady                       Return = C.FM_ST_NOT_READY
	ResourceUsedInThisPartition    Return = C.FM_ST_RESOURCE_USED_IN_THIS_PARTITION
	ResourceUsedInOtherPartition   Return = C.FM_ST_RESOURCE_USED_IN_ANOTHER_PARTITION
	PartitionMiswiredTrunks        Return = C.FM_ST_PARTITION_MISWIRED_TRUNKS
	PartitionInsufficientTrunks    Return = C.FM_ST_PARTITION_INSUFFICIENT_TRUNKS
	PartitionMissingSwitches       Return = C.FM_ST_PARTITION_MISSING_SWITCHES
	PartitionNetworkConfigError    Return = C.FM_ST_PARTITION_NETWORK_CONFIG_ERROR
	PartitionRouteProgrammingError Return = C.FM_ST_PARTITION_ROUTE_PROGRAMMING_ERROR
)

// Error returns the error representation of a Return value.
func (r Return) Error() string {
	switch r {
	case Success:
		return "success"
	case BadParam:
		return "bad parameter"
	case GenericError:
		return "generic error"
	case NotSupported:
		return "not supported"
	case Uninitialized:
		return "uninitialized"
	case Timeout:
		return "timeout"
	case VersionMismatch:
		return "version mismatch"
	case InUse:
		return "in use"
	case NotConfigured:
		return "not configured"
	case ConnectionNotValid:
		return "connection not valid"
	case NVLinkError:
		return "nvlink error"
	case ResourceBad:
		return "resource bad"
	case ResourceInUse:
		return "resource in use"
	case ResourceNotInUse:
		return "resource not in use"
	case ResourceExhausted:
		return "resource exhausted"
	case ResourceNotReady:
		return "resource not ready"
	case PartitionExists:
		return "partition exists"
	case PartitionIDInUse:
		return "partition ID in use"
	case PartitionIDNotInUse:
		return "partition ID not in use"
	case PartitionNameInUse:
		return "partition name in use"
	case PartitionNameNotInUse:
		return "partition name not in use"
	case PartitionIDNameMismatch:
		return "partition ID name mismatch"
	case NotReady:
		return "not ready"
	case ResourceUsedInThisPartition:
		return "resource used in this partition"
	case ResourceUsedInOtherPartition:
		return "resource used in other partition"
	case PartitionMiswiredTrunks:
		return "partition miswired trunks"
	case PartitionInsufficientTrunks:
		return "partition insufficient trunks"
	case PartitionMissingSwitches:
		return "partition missing switches"
	case PartitionNetworkConfigError:
		return "partition network config error"
	case PartitionRouteProgrammingError:
		return "partition route programming error"
	default:
		return fmt.Sprintf("unknown return code: %d", int32(r))
	}
}

// Handle represents a Fabric Manager API handle.
type Handle struct {
	handle C.fmHandle_t
}

// AddressType represents the address type for connections.
type AddressType int32

const (
	AddressTypeUnknown AddressType = C.NV_FM_API_ADDR_TYPE_UNKNOWN
	AddressTypeInet    AddressType = C.NV_FM_API_ADDR_TYPE_INET
	AddressTypeUnix    AddressType = C.NV_FM_API_ADDR_TYPE_UNIX
	AddressTypeVsock   AddressType = C.NV_FM_API_ADDR_TYPE_VSOCK
)

// ConnectParams contains connection parameters for Fabric Manager.
type ConnectParams struct {
	AddressInfo string
	TimeoutMs   uint32
	AddressType AddressType
}

// PCIDevice represents PCI device information.
type PCIDevice struct {
	Domain   uint32
	Bus      uint32
	Device   uint32
	Function uint32
}

// GPUInfo contains information about a GPU in a fabric partition.
type GPUInfo struct {
	PhysicalID          uint32
	UUID                string
	PCIBusID            string
	NumNVLinksAvailable uint32
	MaxNumNVLinks       uint32
	NVLinkLineRateMBps  uint32
}

// PartitionInfo contains information about a fabric partition.
type PartitionInfo struct {
	PartitionID uint32
	IsActive    bool
	NumGPUs     uint32
	GPUs        []GPUInfo
}

// FabricPartitionList contains information about all supported fabric partitions.
type FabricPartitionList struct {
	NumPartitions    uint32
	MaxNumPartitions uint32
	Partitions       []PartitionInfo
}

// Init initializes the Fabric Manager API library.
func Init() error {
	ret := C.fmLibInit()
	if ret != C.FM_ST_SUCCESS {
		return Return(ret)
	}
	return nil
}

// Shutdown shuts down the Fabric Manager API library.
func Shutdown() error {
	ret := C.fmLibShutdown()
	if ret != C.FM_ST_SUCCESS {
		return Return(ret)
	}
	return nil
}

// Connect connects to a Fabric Manager instance.
func Connect(params ConnectParams) (*Handle, error) {
	if len(params.AddressInfo) >= C.FM_MAX_STR_LENGTH {
		return nil, errors.New("address info too long")
	}

	var connectParams C.fmConnectParams_t
	connectParams.version = C.fmConnectParams_version
	connectParams.timeoutMs = C.uint(params.TimeoutMs)
	connectParams.addressType = uint32(params.AddressType)
	if params.AddressType == AddressTypeUnix {
		connectParams.addressIsUnixSocket = 1
	} else {
		connectParams.addressIsUnixSocket = 0
	}

	// Copy address info
	cAddressInfo := C.CString(params.AddressInfo)
	defer C.free(unsafe.Pointer(cAddressInfo))

	// Use C.strncpy equivalent
	for i, ch := range params.AddressInfo {
		if i >= C.FM_MAX_STR_LENGTH-1 {
			break
		}
		connectParams.addressInfo[i] = C.char(ch)
	}
	connectParams.addressInfo[len(params.AddressInfo)] = 0

	handle := &Handle{}
	ret := C.fmConnect(&connectParams, &handle.handle)

	if ret != C.FM_ST_SUCCESS {
		return nil, Return(ret)
	}

	return handle, nil
}

// Disconnect disconnects from the Fabric Manager instance.
func (h *Handle) Disconnect() error {
	ret := C.fmDisconnect(h.handle)
	if ret != C.FM_ST_SUCCESS {
		return Return(ret)
	}
	return nil
}

// GetSupportedFabricPartitions retrieves all supported fabric partitions.
func (h *Handle) GetSupportedFabricPartitions() (*FabricPartitionList, error) {
	var cPartitions C.fmFabricPartitionList_t
	cPartitions.version = C.fmFabricPartitionList_version

	ret := C.fmGetSupportedFabricPartitions(h.handle, &cPartitions)
	if ret != C.FM_ST_SUCCESS {
		return nil, Return(ret)
	}

	partitions := &FabricPartitionList{
		NumPartitions:    uint32(cPartitions.numPartitions),
		MaxNumPartitions: uint32(cPartitions.maxNumPartitions),
		Partitions:       make([]PartitionInfo, cPartitions.numPartitions),
	}

	for i := uint32(0); i < partitions.NumPartitions; i++ {
		cPartition := cPartitions.partitionInfo[i]
		partition := PartitionInfo{
			PartitionID: uint32(cPartition.partitionId),
			IsActive:    cPartition.isActive != 0,
			NumGPUs:     uint32(cPartition.numGpus),
			GPUs:        make([]GPUInfo, cPartition.numGpus),
		}

		for j := uint32(0); j < partition.NumGPUs; j++ {
			cGPU := cPartition.gpuInfo[j]
			partition.GPUs[j] = GPUInfo{
				PhysicalID:          uint32(cGPU.physicalId),
				UUID:                C.GoString(&cGPU.uuid[0]),
				PCIBusID:            C.GoString(&cGPU.pciBusId[0]),
				NumNVLinksAvailable: uint32(cGPU.numNvLinksAvailable),
				MaxNumNVLinks:       uint32(cGPU.maxNumNvLinks),
				NVLinkLineRateMBps:  uint32(cGPU.nvlinkLineRateMBps),
			}
		}

		partitions.Partitions[i] = partition
	}

	return partitions, nil
}

// ActivateFabricPartition activates a fabric partition.
func (h *Handle) ActivateFabricPartition(partitionID uint32) error {
	ret := C.fmActivateFabricPartition(h.handle, C.fmFabricPartitionId_t(partitionID))
	if ret != C.FM_ST_SUCCESS {
		return Return(ret)
	}
	return nil
}

// ActivateFabricPartitionWithVFs activates a fabric partition with VFs.
func (h *Handle) ActivateFabricPartitionWithVFs(partitionID uint32, vfs []PCIDevice) error {
	if len(vfs) == 0 {
		return h.ActivateFabricPartition(partitionID)
	}

	cVFs := make([]C.fmPciDevice_t, len(vfs))
	for i, vf := range vfs {
		cVFs[i] = C.fmPciDevice_t{
			domain:   C.uint(vf.Domain),
			bus:      C.uint(vf.Bus),
			device:   C.uint(vf.Device),
			function: C.uint(vf.Function),
		}
	}

	ret := C.fmActivateFabricPartitionWithVFs(
		h.handle,
		C.fmFabricPartitionId_t(partitionID),
		&cVFs[0],
		C.uint(len(vfs)),
	)
	if ret != C.FM_ST_SUCCESS {
		return Return(ret)
	}
	return nil
}

// DeactivateFabricPartition deactivates a fabric partition.
func (h *Handle) DeactivateFabricPartition(partitionID uint32) error {
	ret := C.fmDeactivateFabricPartition(h.handle, C.fmFabricPartitionId_t(partitionID))
	if ret != C.FM_ST_SUCCESS {
		return Return(ret)
	}
	return nil
}
