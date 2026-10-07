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
// linux+cgo builds and drives the Fabric Manager SDK ("nv_fm_agent" / libnvfm)
// through the official Go bindings, github.com/NVIDIA/go-nvfm, instead of a
// hand-written cgo transcription of the FM ABI. go-nvfm dlopen's libnvfm at
// runtime (it does not link it at build time), so no proprietary FM headers are
// vendored into this repository. See docs/fabric-partition-activation.md.

package fabric

import (
	"fmt"
	"strings"
	"sync"

	"github.com/NVIDIA/go-nvfm/pkg/nvfm"
)

// defaultConnectTimeoutMs bounds each fmConnect attempt. go-nvfm defaults to
// 1000ms; keep the more generous 5s the plugin has always used so a briefly
// busy Fabric Manager daemon is not treated as unreachable.
const defaultConnectTimeoutMs = 5000

// nvfmLibraryName is the shared object go-nvfm dlopen's. The runtime image ships
// only the versioned SONAME libnvfm.so.1 (the unversioned libnvfm.so symlink
// lives in the -dev package, which is not installed in the distroless image), so
// point go-nvfm at the versioned name explicitly. go-nvfm's default is the
// unversioned "libnvfm.so".
const nvfmLibraryName = "libnvfm.so.1"

// setLibraryOnce sets go-nvfm's package-level library path exactly once, before
// the first Init, so the process-wide singleton is pointed at the SONAME we
// bundle. SetLibraryOptions errors if the library is already initialised, which
// is why it must run before any Init call.
var (
	setLibraryOnce sync.Once
	setLibraryErr  error
)

// nvfmClient is the go-nvfm-backed Client. Not safe for concurrent use; the
// mutex only guards Close against a concurrent in-flight call so the handle is
// not used after Disconnect.
type nvfmClient struct {
	mu     sync.Mutex
	handle nvfm.Handle
	closed bool
}

// toReturn converts a go-nvfm return code to this package's Return. The two
// enums share identical numeric values (both mirror fmReturn_enum), so a direct
// conversion preserves the sentinel mapping in errors.go (ErrInUse,
// ErrPartitionNotActive, ErrFabricNotSupported, ...).
func toReturn(r nvfm.Return) Return {
	return Return(int32(r))
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

	setLibraryOnce.Do(func() {
		setLibraryErr = nvfm.SetLibraryOptions(nvfm.WithLibraryPath(nvfmLibraryName))
	})
	if setLibraryErr != nil {
		return nil, fmt.Errorf("fabric: New: setting libnvfm path: %w", setLibraryErr)
	}

	// nvfm.Init is reference counted on the process-wide library singleton, so
	// concurrent clients share a single fmLibInit and the library stays loaded
	// until the last Close. fmLibInit runs exactly once (refcount 0 -> 1), so the
	// FM_ST_IN_USE case the old hand-rolled binding had to tolerate cannot occur.
	if ret := nvfm.Init(); ret != nvfm.SUCCESS {
		return nil, errorFor("fmLibInit", toReturn(ret))
	}

	connectOpt := nvfm.WithAddress(addr)
	if isUnixAddress(addr) {
		connectOpt = nvfm.WithUnixSocket(addr)
	}

	handle, ret := nvfm.Connect(connectOpt, nvfm.WithTimeoutMs(defaultConnectTimeoutMs))
	if ret != nvfm.SUCCESS {
		// Balance the Init above so a failed connect does not leak the library
		// init reference.
		_ = nvfm.Shutdown()
		return nil, errorFor("fmConnect", toReturn(ret))
	}

	return &nvfmClient{handle: handle}, nil
}

// GetSupportedPartitions implements Client.
func (c *nvfmClient) GetSupportedPartitions() ([]Partition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("fabric: GetSupportedPartitions: client closed")
	}

	list, ret := c.handle.GetSupportedFabricPartitions()
	if err := errorFor("fmGetSupportedFabricPartitions", toReturn(ret)); err != nil {
		return nil, err
	}

	num := int(list.NumPartitions)
	partitions := make([]Partition, 0, num)
	for i := 0; i < num; i++ {
		pi := list.PartitionInfo[i]
		numGpus := int(pi.NumGpus)
		gpus := make([]GPUInfo, 0, numGpus)
		for j := 0; j < numGpus; j++ {
			gi := pi.GpuInfo[j]
			gpus = append(gpus, GPUInfo{
				PhysicalID: gi.PhysicalId,
				UUID:       int8CStringToGo(gi.Uuid[:]),
				PCIBusID:   int8CStringToGo(gi.PciBusId[:]),
			})
		}
		partitions = append(partitions, Partition{
			ID:     pi.PartitionId,
			Active: pi.IsActive != 0,
			GPUs:   gpus,
		})
	}
	return partitions, nil
}

// ActivateWithVFs implements Client.
func (c *nvfmClient) ActivateWithVFs(partitionID uint32, vfs []BDF) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("fabric: ActivateWithVFs: client closed")
	}
	if len(vfs) == 0 {
		return fmt.Errorf("fabric: ActivateWithVFs: no VFs supplied for partition %d", partitionID)
	}

	devices := make([]nvfm.PciDevice, len(vfs))
	for i, v := range vfs {
		devices[i] = nvfm.PciDevice{
			Domain:   v.Domain,
			Bus:      v.Bus,
			Device:   v.Device,
			Function: v.Function,
		}
	}

	ret := c.handle.ActivateFabricPartitionWithVFs(nvfm.FabricPartitionId(partitionID), devices)
	return errorFor("fmActivateFabricPartitionWithVFs", toReturn(ret))
}

// Deactivate implements Client.
func (c *nvfmClient) Deactivate(partitionID uint32) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("fabric: Deactivate: client closed")
	}
	ret := c.handle.DeactivateFabricPartition(nvfm.FabricPartitionId(partitionID))
	return errorFor("fmDeactivateFabricPartition", toReturn(ret))
}

// Close implements Client (fmDisconnect + fmLibShutdown). Safe to call twice.
func (c *nvfmClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true

	disconnectRet := c.handle.Disconnect()
	shutdownRet := nvfm.Shutdown()

	if err := errorFor("fmDisconnect", toReturn(disconnectRet)); err != nil {
		return err
	}
	return errorFor("fmLibShutdown", toReturn(shutdownRet))
}

// int8CStringToGo converts a NUL-terminated C char buffer represented as a Go
// []int8 (the layout go-nvfm uses for the fixed-size uuid / pciBusId fields)
// into a Go string, stopping at the first NUL.
func int8CStringToGo(b []int8) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	buf := make([]byte, n)
	for i := 0; i < n; i++ {
		buf[i] = byte(b[i])
	}
	return string(buf)
}
