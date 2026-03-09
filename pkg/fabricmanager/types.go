//go:build nvfm

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
	"time"

	"kubevirt-gpu-device-plugin/pkg/nvfm"
)

// GPU represents a GPU device within a fabric partition.
type GPU struct {
	PhysicalID          uint32
	UUID                string
	PCIBusID            string
	NumNVLinksAvailable uint32
	MaxNumNVLinks       uint32
	NVLinkLineRateMBps  uint32
}

// Partition represents a fabric partition with its associated GPUs.
type Partition struct {
	PartitionID uint32
	IsActive    bool
	NumGPUs     uint32
	GPUs        []GPU

	// Client-side fields for management
	LastActivated   time.Time
	LastDeactivated time.Time
	ActivationCount int
}

// PartitionList contains information about all supported fabric partitions.
type PartitionList struct {
	NumPartitions    uint32
	MaxNumPartitions uint32
	Partitions       []Partition
}

// ActivateRequest contains parameters for activating a fabric partition.
type ActivateRequest struct {
	PartitionID uint32
	VFDevices   []PCIDevice // Optional VF devices to activate with the partition
}

// PCIDevice represents a PCI device (for VF activation).
type PCIDevice struct {
	Domain   uint32
	Bus      uint32
	Device   uint32
	Function uint32
}

// fromNVFMGPU converts an nvfm.GPUInfo to a GPU.
func fromNVFMGPU(nvfmGPU nvfm.GPUInfo) GPU {
	return GPU{
		PhysicalID:          nvfmGPU.PhysicalID,
		UUID:                nvfmGPU.UUID,
		PCIBusID:            nvfmGPU.PCIBusID,
		NumNVLinksAvailable: nvfmGPU.NumNVLinksAvailable,
		MaxNumNVLinks:       nvfmGPU.MaxNumNVLinks,
		NVLinkLineRateMBps:  nvfmGPU.NVLinkLineRateMBps,
	}
}

// fromNVFMPartition converts an nvfm.PartitionInfo to a Partition.
func fromNVFMPartition(nvfmPartition nvfm.PartitionInfo) Partition {
	gpus := make([]GPU, len(nvfmPartition.GPUs))
	for i, nvfmGPU := range nvfmPartition.GPUs {
		gpus[i] = fromNVFMGPU(nvfmGPU)
	}

	return Partition{
		PartitionID: nvfmPartition.PartitionID,
		IsActive:    nvfmPartition.IsActive,
		NumGPUs:     nvfmPartition.NumGPUs,
		GPUs:        gpus,
	}
}

// fromNVFMPartitionList converts an nvfm.FabricPartitionList to a PartitionList.
func fromNVFMPartitionList(nvfmList *nvfm.FabricPartitionList) *PartitionList {
	partitions := make([]Partition, len(nvfmList.Partitions))
	for i, nvfmPartition := range nvfmList.Partitions {
		partitions[i] = fromNVFMPartition(nvfmPartition)
	}

	return &PartitionList{
		NumPartitions:    nvfmList.NumPartitions,
		MaxNumPartitions: nvfmList.MaxNumPartitions,
		Partitions:       partitions,
	}
}

// toNVFMPCIDevice converts a PCIDevice to an nvfm.PCIDevice.
func toNVFMPCIDevice(device PCIDevice) nvfm.PCIDevice {
	return nvfm.PCIDevice{
		Domain:   device.Domain,
		Bus:      device.Bus,
		Device:   device.Device,
		Function: device.Function,
	}
}

// toNVFMPCIDevices converts a slice of PCIDevice to a slice of nvfm.PCIDevice.
func toNVFMPCIDevices(devices []PCIDevice) []nvfm.PCIDevice {
	nvfmDevices := make([]nvfm.PCIDevice, len(devices))
	for i, device := range devices {
		nvfmDevices[i] = toNVFMPCIDevice(device)
	}
	return nvfmDevices
}

