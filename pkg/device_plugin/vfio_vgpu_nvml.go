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

package device_plugin

import (
	"fmt"
	"reflect"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// resolveVgpuTypeNamesViaNVML resolves the vGPU types SUPPORTED by the
// physical card at pfAddress into a type-id-to-sanitized-name map via NVML.
// This is the last-resort resolution source: unlike creatable_vgpu_types,
// the supported list does not shrink as capacity is allocated, so it keeps
// working on a fully consumed card where every sysfs catalog is reduced to
// the header. Requires libnvidia-ml.so.1 to be loadable in the container
// (e.g. the host driver library directory mounted and present in the
// dynamic linker search path).
var resolveVgpuTypeNamesViaNVML = func(pfAddress string) (map[string]string, error) {
	return supportedVgpuTypeNames(nvml.New(), pfAddress)
}

func supportedVgpuTypeNames(nvmllib nvml.Interface, pfAddress string) (map[string]string, error) {
	ret := nvmllib.Init()
	if ret != nvml.SUCCESS && ret != nvml.ERROR_ALREADY_INITIALIZED {
		return nil, fmt.Errorf("error initializing NVML: %v", ret)
	}
	defer func() {
		_ = nvmllib.Shutdown()
	}()

	dev, ret := nvmllib.DeviceGetHandleByPciBusId(pfAddress)
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("error getting device handle for PCI address %s: %v", pfAddress, ret)
	}

	typeIDs, ret := dev.GetSupportedVgpus()
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("error getting supported vGPU types for %s: %v", pfAddress, ret)
	}

	names := make(map[string]string, len(typeIDs))
	for _, typeID := range typeIDs {
		id, err := numericVgpuTypeID(typeID)
		if err != nil {
			return nil, err
		}
		name, ret := typeID.GetName()
		if ret != nvml.SUCCESS {
			return nil, fmt.Errorf("error getting the name of vGPU type %s: %v", id, ret)
		}
		names[id] = whitespaceRegexp.ReplaceAllString(name, "_")
	}
	return names, nil
}

// numericVgpuTypeID renders an nvml.VgpuTypeId as the decimal string used by
// the sysfs vendor-specific VFIO files (current_vgpu_type,
// creatable_vgpu_types).
func numericVgpuTypeID(typeID nvml.VgpuTypeId) (string, error) {
	v := reflect.ValueOf(typeID)
	switch {
	case v.CanUint():
		return fmt.Sprintf("%d", v.Uint()), nil
	case v.CanInt():
		return fmt.Sprintf("%d", v.Int()), nil
	}
	return "", fmt.Errorf("unable to determine the numeric vGPU type id of %T", typeID)
}
