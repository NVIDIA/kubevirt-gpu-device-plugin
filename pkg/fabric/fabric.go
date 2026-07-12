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

// Package fabric wraps the NVIDIA Fabric Manager (FM) SDK ("nv_fm_agent" /
// libnvfm) so that the device plugin can activate the NVLink fabric partition
// that a vGPU Virtual Function belongs to.
//
// On NVSwitch systems (HGX H100/H200, ...) the GPU NVLink fabric is brought up
// by the Fabric Manager daemon. When FM runs in FABRIC_MODE=2 (SR-IOV vGPU
// multitenancy), each guest VM's VF additionally needs its fabric *partition*
// activated through the FM SDK before the guest can initialise CUDA; otherwise
// cuInit fails with CUDA error 802 ("system not yet initialized"). See
// https://github.com/NVIDIA/kubevirt-gpu-device-plugin/issues/133.
//
// The package is split so that the platform-independent surface (this file plus
// bdf.go and partition.go) builds and unit-tests on any OS, while the actual
// libnvfm binding lives behind a build tag:
//
//   - fabric_cgo.go  (//go:build linux && cgo) drives libnvfm through the
//     official Go bindings, github.com/NVIDIA/go-nvfm (which dlopen's libnvfm at
//     runtime; cgo is required to compile it).
//   - fabric_stub.go (//go:build !(linux && cgo)) returns ErrUnsupported so the
//     rest of the tree still builds on developer machines and CGO_ENABLED=0.
package fabric

import "strings"

// DefaultUnixSocket is the Unix domain socket the Fabric Manager daemon listens
// on by default. Connecting over the socket avoids depending on the TCP command
// interface being enabled.
const DefaultUnixSocket = "/var/run/nvidia-fabricmanager/socket"

// DefaultTCPAddress is the default TCP command interface of the Fabric Manager
// daemon (FM_CMD_BIND_INTERFACE:FM_CMD_PORT_NUMBER). This is where FM serves the
// nv_fm_agent command API by default; the Unix socket only serves it when
// fabricmanager.cfg sets UNIX_SOCKET_PATH.
const DefaultTCPAddress = "127.0.0.1:6666"

// isUnixAddress reports whether addr denotes a Unix socket path (a value
// starting with '/') rather than a "host:port" TCP endpoint. It selects the
// Fabric Manager connection address type: a leading '/' means the Unix domain
// socket type, otherwise the TCP/INET type.
func isUnixAddress(addr string) bool {
	return strings.HasPrefix(strings.TrimSpace(addr), "/")
}

// GPUInfo describes a physical GPU that belongs to a fabric partition, as
// reported by fmGetSupportedFabricPartitions. Mirrors fmFabricPartitionGpuInfo_t.
type GPUInfo struct {
	// PhysicalID is the GPU's physical id as known to Fabric Manager. Per the
	// FM SDK this is the same value as the GPU Module ID reported by NVML
	// (nvmlDeviceGetModuleId). It is NOT the PCI enumeration order and must not
	// be assumed to match it.
	PhysicalID uint32
	// UUID is the GPU UUID (e.g. "GPU-xxxxxxxx-...").
	UUID string
	// PCIBusID is the PCI BDF of the physical GPU (its Physical Function on
	// SR-IOV systems), as formatted by Fabric Manager.
	PCIBusID string
}

// Partition is a supported fabric partition reported by Fabric Manager. Mirrors
// fmFabricPartitionInfo_t.
type Partition struct {
	// ID is the unique partition id used with the activate/deactivate calls.
	ID uint32
	// Active reports whether Fabric Manager currently has this partition
	// activated.
	Active bool
	// GPUs are the physical GPUs assigned to this partition.
	GPUs []GPUInfo
}

// IsSingleGPU reports whether the partition covers exactly one physical GPU.
// A single whole-card vGPU VF maps to the single-GPU partition of its physical
// GPU; several whole cards allocated together map to the multi-GPU partition
// spanning exactly those GPUs (see PartitionForModuleIDs).
func (p Partition) IsSingleGPU() bool {
	return len(p.GPUs) == 1
}

// Client is the subset of the Fabric Manager SDK that the device plugin needs
// to activate and deactivate vGPU fabric partitions. A Client wraps a single
// connection to the Fabric Manager daemon; it is not safe for concurrent use
// and callers must serialise access (the device plugin does so from Allocate).
type Client interface {
	// GetSupportedPartitions returns every fabric partition Fabric Manager
	// supports on this system, including each partition's active state and the
	// physical GPUs it covers (fmGetSupportedFabricPartitions).
	GetSupportedPartitions() ([]Partition, error)

	// ActivateWithVFs activates the given partition, binding it to the supplied
	// VF PCI devices (fmActivateFabricPartitionWithVFs). The order of vfs must
	// correspond to the physical GPUs of the partition; for a single-GPU
	// partition this is a single VF. Activating an already-active partition
	// returns an error wrapping ErrInUse.
	ActivateWithVFs(partitionID uint32, vfs []BDF) error

	// Deactivate deactivates a previously activated partition
	// (fmDeactivateFabricPartition). Deactivating a partition that is not
	// active returns an error wrapping ErrPartitionNotActive.
	Deactivate(partitionID uint32) error

	// Close disconnects from the Fabric Manager daemon (fmDisconnect) and shuts
	// the FM API library down (fmLibShutdown). It is safe to call Close more
	// than once.
	Close() error
}
