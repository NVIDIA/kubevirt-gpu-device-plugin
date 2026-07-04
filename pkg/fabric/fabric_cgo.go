//go:build linux && cgo

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

// This file is the real Fabric Manager binding. It is compiled only for
// linux+cgo builds and links against libnvfm.so.1 (shipped by the
// nvidia-fabricmanager package). The C declarations below are a self-contained
// transcription of the public "nv_fm_agent" ABI (from nv_fm_agent.h /
// nv_fm_types.h); NVIDIA's proprietary headers are intentionally NOT vendored
// into this repository. The struct layouts and the version-number scheme
// (MAKE_FM_PARAM_VERSION = sizeof(type) | version<<24) are part of the stable
// ABI and are validated against the daemon by Fabric Manager itself
// (FM_ST_VERSION_MISMATCH on drift).

package fabric

/*
#cgo LDFLAGS: -l:libnvfm.so.1

#include <stdlib.h>
#include <string.h>

// ---- Constants (nv_fm_types.h) --------------------------------------------
#define FABRIC_MAX_STR_LENGTH               256
#define FABRIC_UUID_BUFFER_SIZE             80
#define FABRIC_DEVICE_PCI_BUS_ID_BUFFER_SIZE 32
#define FABRIC_MAX_NUM_GPUS                 16
#define FABRIC_MAX_FABRIC_PARTITIONS        64

// Versioned struct id: low bytes = sizeof, high byte = struct version.
#define FABRIC_MAKE_PARAM_VERSION(typeName, ver) \
    (unsigned int)(sizeof(typeName) | ((ver) << 24))

// ---- Handle / scalar typedefs ---------------------------------------------
typedef void *fmHandle_t;
typedef unsigned int fmFabricPartitionId_t;

// ---- Address types (nvFmApiAddrTypes) -------------------------------------
enum fabricAddrTypes {
    FABRIC_ADDR_TYPE_UNKNOWN = 0,
    FABRIC_ADDR_TYPE_INET    = 1,
    FABRIC_ADDR_TYPE_UNIX    = 2,
    FABRIC_ADDR_TYPE_VSOCK   = 3,
};

// ---- fmConnectParams_v2 ----------------------------------------------------
typedef struct {
    unsigned int version;
    char         addressInfo[FABRIC_MAX_STR_LENGTH];
    unsigned int timeoutMs;
    unsigned int addressIsUnixSocket;
    enum fabricAddrTypes addressType;
} fmConnectParams_v2;
#define FABRIC_CONNECT_PARAMS_VERSION FABRIC_MAKE_PARAM_VERSION(fmConnectParams_v2, 2)

// ---- fmPciDevice_t ---------------------------------------------------------
typedef struct {
    unsigned int domain;
    unsigned int bus;
    unsigned int device;
    unsigned int function;
} fmPciDevice_t;

// ---- Partition info structs ------------------------------------------------
typedef struct {
    unsigned int physicalId;
    char         uuid[FABRIC_UUID_BUFFER_SIZE];
    char         pciBusId[FABRIC_DEVICE_PCI_BUS_ID_BUFFER_SIZE];
    unsigned int numNvLinksAvailable;
    unsigned int maxNumNvLinks;
    unsigned int nvlinkLineRateMBps;
} fmFabricPartitionGpuInfo_t;

typedef struct {
    fmFabricPartitionId_t      partitionId;
    unsigned int               isActive;
    unsigned int               numGpus;
    fmFabricPartitionGpuInfo_t gpuInfo[FABRIC_MAX_NUM_GPUS];
} fmFabricPartitionInfo_t;

typedef struct {
    unsigned int            version;
    unsigned int            numPartitions;
    unsigned int            maxNumPartitions;
    fmFabricPartitionInfo_t partitionInfo[FABRIC_MAX_FABRIC_PARTITIONS];
} fmFabricPartitionList_v2;
#define FABRIC_PARTITION_LIST_VERSION FABRIC_MAKE_PARAM_VERSION(fmFabricPartitionList_v2, 1)

// ---- libnvfm entry points (from nv_fm_agent.h) -----------------------------
extern int fmLibInit(void);
extern int fmLibShutdown(void);
extern int fmConnect(fmConnectParams_v2 *connectParams, fmHandle_t *pFmHandle);
extern int fmDisconnect(fmHandle_t pFmHandle);
extern int fmGetSupportedFabricPartitions(fmHandle_t pFmHandle, fmFabricPartitionList_v2 *pList);
extern int fmActivateFabricPartitionWithVFs(fmHandle_t pFmHandle, fmFabricPartitionId_t partitionId, fmPciDevice_t *vfList, unsigned int numVfs);
extern int fmDeactivateFabricPartition(fmHandle_t pFmHandle, fmFabricPartitionId_t partitionId);

// ---- Thin C helpers (keep sizeof/version arithmetic in C) ------------------

// fabric_connect fills a versioned fmConnectParams_v2 and connects. isUnix
// selects the Unix-socket address type, otherwise INET (TCP).
static int fabric_connect(const char *addr, int isUnix, unsigned int timeoutMs, fmHandle_t *handle) {
    fmConnectParams_v2 params;
    memset(&params, 0, sizeof(params));
    params.version = FABRIC_CONNECT_PARAMS_VERSION;
    strncpy(params.addressInfo, addr, FABRIC_MAX_STR_LENGTH - 1);
    params.addressInfo[FABRIC_MAX_STR_LENGTH - 1] = '\0';
    params.timeoutMs = timeoutMs;
    params.addressIsUnixSocket = isUnix ? 1u : 0u;
    params.addressType = isUnix ? FABRIC_ADDR_TYPE_UNIX : FABRIC_ADDR_TYPE_INET;
    return fmConnect(&params, handle);
}

static unsigned int fabric_partition_list_version(void) {
    return FABRIC_PARTITION_LIST_VERSION;
}
*/
import "C"

import (
	"fmt"
	"strings"
	"sync"
	"unsafe"
)

// defaultConnectTimeoutMs bounds each fmConnect attempt.
const defaultConnectTimeoutMs = 5000

// cgoClient is the libnvfm-backed Client. Not safe for concurrent use; the mutex
// only guards Close against a concurrent in-flight call so the handle is not
// used after fmDisconnect.
type cgoClient struct {
	mu     sync.Mutex
	handle C.fmHandle_t
	closed bool
}

// New initialises the Fabric Manager API library (fmLibInit) and connects
// (fmConnect) to the daemon at addr. addr is either a Unix socket path (a value
// starting with '/', e.g. DefaultUnixSocket) or a "host:port" TCP address (e.g.
// DefaultTCPAddress). The returned Client must be Closed to release the
// connection and shut the library down.
func New(addr string) (Client, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("fabric: New: empty address")
	}

	// fmLibInit is process-global. FM_ST_IN_USE means it was already
	// initialised elsewhere, which is fine for our single-client use. Only shut
	// the library back down on failure if this call is the one that initialised
	// it, so we do not tear down another component's initialisation.
	initRet := Return(C.fmLibInit())
	if initRet != Success && initRet != ErrInUseCode {
		return nil, errorFor("fmLibInit", initRet)
	}
	weInitialised := initRet == Success

	isUnix := 0
	if isUnixAddress(addr) {
		isUnix = 1
	}

	caddr := C.CString(addr)
	defer C.free(unsafe.Pointer(caddr))

	var handle C.fmHandle_t
	ret := Return(C.fabric_connect(caddr, C.int(isUnix), C.uint(defaultConnectTimeoutMs), &handle))
	if ret != Success {
		// Best-effort library shutdown so a failed connect does not leak the
		// global init we performed.
		if weInitialised {
			C.fmLibShutdown()
		}
		return nil, errorFor("fmConnect", ret)
	}

	return &cgoClient{handle: handle}, nil
}

// GetSupportedPartitions implements Client.
func (c *cgoClient) GetSupportedPartitions() ([]Partition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fabric: GetSupportedPartitions: client closed")
	}

	// The partition list struct is ~130 KiB; allocate it on the C heap rather
	// than the goroutine stack.
	size := C.size_t(unsafe.Sizeof(C.fmFabricPartitionList_v2{}))
	list := (*C.fmFabricPartitionList_v2)(C.malloc(size))
	if list == nil {
		return nil, fmt.Errorf("fabric: GetSupportedPartitions: out of memory")
	}
	defer C.free(unsafe.Pointer(list))
	C.memset(unsafe.Pointer(list), 0, size)
	list.version = C.fabric_partition_list_version()

	ret := Return(C.fmGetSupportedFabricPartitions(c.handle, list))
	if err := errorFor("fmGetSupportedFabricPartitions", ret); err != nil {
		return nil, err
	}

	num := int(list.numPartitions)
	partitions := make([]Partition, 0, num)
	for i := 0; i < num; i++ {
		pi := list.partitionInfo[i]
		numGpus := int(pi.numGpus)
		gpus := make([]GPUInfo, 0, numGpus)
		for j := 0; j < numGpus; j++ {
			gi := pi.gpuInfo[j]
			gpus = append(gpus, GPUInfo{
				PhysicalID: uint32(gi.physicalId),
				UUID:       C.GoString((*C.char)(unsafe.Pointer(&gi.uuid[0]))),
				PCIBusID:   C.GoString((*C.char)(unsafe.Pointer(&gi.pciBusId[0]))),
			})
		}
		partitions = append(partitions, Partition{
			ID:     uint32(pi.partitionId),
			Active: pi.isActive != 0,
			GPUs:   gpus,
		})
	}
	return partitions, nil
}

// ActivateWithVFs implements Client.
func (c *cgoClient) ActivateWithVFs(partitionID uint32, vfs []BDF) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("fabric: ActivateWithVFs: client closed")
	}
	if len(vfs) == 0 {
		return fmt.Errorf("fabric: ActivateWithVFs: no VFs supplied for partition %d", partitionID)
	}

	n := len(vfs)
	elemSize := unsafe.Sizeof(C.fmPciDevice_t{})
	arr := (*C.fmPciDevice_t)(C.malloc(C.size_t(uintptr(n) * elemSize)))
	if arr == nil {
		return fmt.Errorf("fabric: ActivateWithVFs: out of memory")
	}
	defer C.free(unsafe.Pointer(arr))

	cVFs := unsafe.Slice(arr, n)
	for i, v := range vfs {
		cVFs[i].domain = C.uint(v.Domain)
		cVFs[i].bus = C.uint(v.Bus)
		cVFs[i].device = C.uint(v.Device)
		cVFs[i].function = C.uint(v.Function)
	}

	ret := Return(C.fmActivateFabricPartitionWithVFs(
		c.handle,
		C.fmFabricPartitionId_t(partitionID),
		arr,
		C.uint(n),
	))
	return errorFor("fmActivateFabricPartitionWithVFs", ret)
}

// Deactivate implements Client.
func (c *cgoClient) Deactivate(partitionID uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("fabric: Deactivate: client closed")
	}
	ret := Return(C.fmDeactivateFabricPartition(c.handle, C.fmFabricPartitionId_t(partitionID)))
	return errorFor("fmDeactivateFabricPartition", ret)
}

// Close implements Client (fmDisconnect + fmLibShutdown). Safe to call twice.
func (c *cgoClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true

	disconnectRet := Return(C.fmDisconnect(c.handle))
	shutdownRet := Return(C.fmLibShutdown())

	if err := errorFor("fmDisconnect", disconnectRet); err != nil {
		return err
	}
	return errorFor("fmLibShutdown", shutdownRet)
}
