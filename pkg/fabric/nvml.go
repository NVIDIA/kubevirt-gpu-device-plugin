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
	"fmt"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// MigModeFunc reports whether the physical GPU at pfPCIAddress has MIG mode
// enabled. A MIG-enabled GPU has NVLink disabled and does not participate in the
// NVSwitch fabric, so its VFs need no fabric partition activation.
type MigModeFunc func(pfPCIAddress string) (bool, error)

// MigEnabledViaNVML returns a MigModeFunc backed by NVML (nvmlDeviceGetMigMode).
// A GPU that does not support MIG reports "not enabled" (nil error).
func MigEnabledViaNVML(nvmllib nvml.Interface) MigModeFunc {
	return func(pfPCIAddress string) (bool, error) {
		if ret := nvmllib.Init(); ret != nvml.SUCCESS && ret != nvml.ERROR_ALREADY_INITIALIZED {
			return false, fmt.Errorf("fabric: initializing NVML: %v", ret)
		}
		defer func() { _ = nvmllib.Shutdown() }()

		dev, ret := nvmllib.DeviceGetHandleByPciBusId(pfPCIAddress)
		if ret != nvml.SUCCESS {
			return false, fmt.Errorf("fabric: getting NVML handle for PF %s: %v", pfPCIAddress, ret)
		}
		current, _, ret := dev.GetMigMode()
		if ret == nvml.ERROR_NOT_SUPPORTED {
			return false, nil // GPU has no MIG support -> treat as non-MIG (whole card)
		}
		if ret != nvml.SUCCESS {
			return false, fmt.Errorf("fabric: getting MIG mode for PF %s: %v", pfPCIAddress, ret)
		}
		return current == nvml.DEVICE_MIG_ENABLE, nil
	}
}

// ModuleIDViaNVML returns a ModuleIDFunc backed by NVML. It resolves a physical
// GPU's Fabric Manager physicalId (== NVML module id) from its PCI address.
//
// nvmllib is the go-nvml library handle (nvml.New()). The returned function
// initialises NVML on each call (tolerating an already-initialised library) and
// shuts it back down, mirroring how the vendor-VFIO vGPU discovery resolves
// names via NVML; NVML Init/Shutdown are reference counted so this composes with
// any NVML already held open elsewhere in the process.
//
// go-nvml loads libnvidia-ml.so.1 via dlopen at call time, so this file builds
// on any platform; it only functions where the NVIDIA driver library is present
// (as it is inside the device plugin container).
func ModuleIDViaNVML(nvmllib nvml.Interface) ModuleIDFunc {
	return func(pfPCIAddress string) (uint32, error) {
		if ret := nvmllib.Init(); ret != nvml.SUCCESS && ret != nvml.ERROR_ALREADY_INITIALIZED {
			return 0, fmt.Errorf("fabric: initializing NVML: %v", ret)
		}
		defer func() { _ = nvmllib.Shutdown() }()

		dev, ret := nvmllib.DeviceGetHandleByPciBusId(pfPCIAddress)
		if ret != nvml.SUCCESS {
			return 0, fmt.Errorf("fabric: getting NVML handle for PF %s: %v", pfPCIAddress, ret)
		}
		moduleID, ret := dev.GetModuleId()
		if ret != nvml.SUCCESS {
			return 0, fmt.Errorf("fabric: getting module id for PF %s: %v", pfPCIAddress, ret)
		}
		if moduleID < 0 {
			return 0, fmt.Errorf("fabric: NVML returned negative module id %d for PF %s", moduleID, pfPCIAddress)
		}
		return uint32(moduleID), nil
	}
}
