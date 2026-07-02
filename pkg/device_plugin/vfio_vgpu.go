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

// This file discovers Nvidia vGPUs exposed through the vendor-specific VFIO
// framework used by Ada/Hopper+ GPUs (vGPU 17+) instead of mdev. Unlike
// mdev-backed vGPUs, these are SR-IOV Virtual Functions that stay bound to
// the "nvidia" host driver and are configured directly on the PCI function
// via sysfs, rather than through /sys/bus/mdev.
package device_plugin

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// nvidiaDriverName is the host driver that vGPU-capable VFs stay bound to
	// when using the vendor-specific VFIO framework (as opposed to vfio-pci
	// used for classic GPU passthrough)
	nvidiaDriverName = "nvidia"
	// vfioVGpuSysfsDir is the vendor-specific sysfs directory exposed under
	// each PCI function capable of vGPU (both the Physical Function and its
	// Virtual Functions)
	vfioVGpuSysfsDir = "nvidia"
	// currentVgpuTypeFile contains the numeric vGPU type id configured on a
	// VF, or "0" if no type has been configured yet
	currentVgpuTypeFile = "current_vgpu_type"
	// creatableVgpuTypesFile lists the vGPU types that can be created on a
	// function, one per line, formatted as "<id> : <name>"
	creatableVgpuTypesFile = "creatable_vgpu_types"
	// sriovTotalVfsFile is only present on SR-IOV Physical Functions
	sriovTotalVfsFile = "sriov_totalvfs"
	// physfnLink is the symlink present on every Virtual Function pointing
	// back at its parent Physical Function
	physfnLink = "physfn"
	// unconfiguredVgpuType is the current_vgpu_type value of a VF that has no
	// vGPU type configured
	unconfiguredVgpuType = "0"
)

// Matches creatable_vgpu_types entries such as "1428 : NVIDIA H200X-141C"
var creatableVgpuTypeRegexp = regexp.MustCompile(`^\s*([0-9]+)\s*:\s*(.+?)\s*$`)
var whitespaceRegexp = regexp.MustCompile(`\s+`)

// Key is the vGPU profile name (sanitized the same way as the mdev vGPU type
// name) and value is the list of Nvidia vGPU VFs of that profile, discovered
// through the vendor-specific VFIO framework
var vfioVGpuMap map[string][]NvidiaGpuDevice

var readVfioVgpuFile = readVfioVgpuFileFunc
var isPhysicalFunction = isPhysicalFunctionFunc

// Discovers all Nvidia vGPUs exposed through the vendor-specific VFIO
// framework and creates the corresponding maps. These vGPUs are SR-IOV VFs
// that stay bound to the "nvidia" driver and expose their vGPU state under
// /sys/bus/pci/devices/<address>/nvidia/, unlike mdev-backed vGPUs which are
// discovered under /sys/bus/mdev/devices by createVgpuIDMap.
//
// A VF is considered a configured vGPU when current_vgpu_type is set to a
// non-zero value. Its profile name is resolved by matching that type id
// against the creatable_vgpu_types catalog. On a configured VF this catalog
// is frequently empty or reduced to the active type only, so this function
// merges the catalog from every scanned function of the same physical card
// (the card's Physical Function and every one of its Virtual Functions,
// configured or not) before resolving names, converging as long as at least
// one function of that card still reports its full list - typically true
// because a freshly enumerated PF or an unconfigured sibling VF always does.
// The catalog is scoped per physical card (keyed by the Physical Function's
// PCI address, found via each VF's "physfn" symlink) rather than merged
// globally across the whole host, because vGPU type ids are only unique
// within one physical card - the same numeric id can mean a different
// profile on a different card, even of the same GPU model. The corner case
// of every function of a card being simultaneously configured with a
// reduced catalog that omits a given type id is not handled here; if this
// is observed in practice, a cached per-card catalog (refreshed once at
// startup) or an NVML-based lookup would be the natural fallback.
//
// Discovered VFs are added to the shared iommuMap and bdfToIommuMap used by
// GenericDevicePlugin, so Allocate and the health check for these devices
// reuse the exact same PCI/IOMMU-group contract as classic GPU passthrough -
// no vendor-VFIO-specific Allocate logic is required.
func createVfioVGpuMap() {
	vfioVGpuMap = make(map[string][]NvidiaGpuDevice)
	// iommuMap and bdfToIommuMap are shared with the GPU passthrough
	// discovery (createIommuDeviceMap) and consumed by GenericDevicePlugin.
	// Initialize them here too so this discovery also works standalone, e.g.
	// in tests that call it directly.
	if iommuMap == nil {
		iommuMap = make(map[string][]NvidiaGpuDevice)
	}
	if bdfToIommuMap == nil {
		bdfToIommuMap = make(map[string]string)
	}

	type vfioVGpuVF struct {
		device     NvidiaGpuDevice
		typeID     string
		iommuGroup string
		pfAddress  string
	}
	var vfs []vfioVGpuVF
	// Keyed by the owning Physical Function's PCI address - see the name
	// resolution note in the function-level comment above.
	vgpuTypeNamesByPF := make(map[string]map[string]string)

	//Walk directory to discover vGPU-capable PCI functions
	walkErr := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing file path %q: %v\n", path, err)
			return err
		}
		if info.IsDir() {
			return nil
		}
		vendorID, err := readIDFromFile(basePath, info.Name(), "vendor")
		if err != nil || vendorID != nvidiaVendorID {
			return nil
		}
		driver, err := readLink(basePath, info.Name(), "driver")
		if err != nil || driver != nvidiaDriverName {
			return nil
		}

		isPF := isPhysicalFunction(basePath, info.Name())
		pfAddress := info.Name()
		if !isPF {
			physfn, err := readLink(basePath, info.Name(), physfnLink)
			if err != nil {
				log.Printf("Could not determine parent Physical Function for VF %s: %v", info.Name(), err)
				return nil
			}
			pfAddress = physfn
		}

		// Merge this function's creatable vGPU types into its own card's
		// catalog before deciding whether it is itself a configured VF.
		if data, err := readVfioVgpuFile(basePath, info.Name(), creatableVgpuTypesFile); err == nil {
			if vgpuTypeNamesByPF[pfAddress] == nil {
				vgpuTypeNamesByPF[pfAddress] = make(map[string]string)
			}
			mergeCreatableVgpuTypes(vgpuTypeNamesByPF[pfAddress], data)
		}
		if isPF {
			return nil
		}

		typeID, err := readVfioVgpuFile(basePath, info.Name(), currentVgpuTypeFile)
		if err != nil {
			// Not a vGPU-capable function, e.g. a GPU used directly by the
			// host driver without any vGPU VFs enabled
			return nil
		}
		if typeID == "" || typeID == unconfiguredVgpuType {
			log.Printf("Skipping unconfigured vGPU VF %s", info.Name())
			return nil
		}
		iommuGroup, err := readLink(basePath, info.Name(), "iommu_group")
		if err != nil {
			log.Println("Could not get IOMMU Group for device ", info.Name())
			return nil
		}
		numaNode, err := readNUMANode(basePath, info.Name())
		if err != nil {
			log.Printf("Could not get NUMA node for device %s: %v. Defaulting to NUMA node 0", info.Name(), err)
			numaNode = 0
		}
		vfs = append(vfs, vfioVGpuVF{
			device:     NvidiaGpuDevice{addr: info.Name(), numaNode: numaNode},
			typeID:     typeID,
			iommuGroup: iommuGroup,
			pfAddress:  pfAddress,
		})
		return nil
	})
	if walkErr != nil {
		log.Printf("Error discovering vendor-specific VFIO vGPU VFs: %v", walkErr)
	}

	// Host-wide catalog fallback: a fully consumed card reduces the
	// creatable_vgpu_types list of EVERY one of its functions (down to the
	// header), so the card's own catalog cannot resolve the types of its
	// already-configured VFs. Merge all per-card catalogs into a host-wide
	// one, tracking numeric ids that different cards resolve to different
	// names — those stay ambiguous and are never guessed.
	globalTypeNames := make(map[string]string)
	ambiguousTypeIDs := make(map[string]bool)
	for _, catalog := range vgpuTypeNamesByPF {
		for typeID, name := range catalog {
			if existing, ok := globalTypeNames[typeID]; ok {
				if existing != name {
					ambiguousTypeIDs[typeID] = true
				}
				continue
			}
			globalTypeNames[typeID] = name
		}
	}

	for _, vf := range vfs {
		vGpuName, ok := vgpuTypeNamesByPF[vf.pfAddress][vf.typeID]
		if !ok {
			if name, found := globalTypeNames[vf.typeID]; found && !ambiguousTypeIDs[vf.typeID] {
				log.Printf("Resolved vGPU type %s on VF %s via the host-wide catalog (the card's own catalog is reduced)", vf.typeID, vf.device.addr)
				vGpuName = name
			} else {
				log.Printf("Error: could not resolve the name of vGPU type %s configured on VF %s (parent PF %s), skipping device", vf.typeID, vf.device.addr, vf.pfAddress)
				continue
			}
		}
		vfioVGpuMap[vGpuName] = append(vfioVGpuMap[vGpuName], vf.device)
		iommuMap[vf.iommuGroup] = append(iommuMap[vf.iommuGroup], vf.device)
		bdfToIommuMap[vf.device.addr] = vf.iommuGroup
	}
}

// mergeCreatableVgpuTypes parses creatable_vgpu_types content and merges its
// "<id> : <name>" entries into vgpuTypeNames. Lines that do not match the
// format are ignored. vGPU type names are sanitized the same way as mdev type
// names (readVgpuIDFromFileFunc) so that both vGPU frameworks expose
// identically formatted resource names for a profile of the same name.
func mergeCreatableVgpuTypes(vgpuTypeNames map[string]string, data string) {
	for _, line := range strings.Split(data, "\n") {
		match := creatableVgpuTypeRegexp.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		typeID := match[1]
		vGpuName := whitespaceRegexp.ReplaceAllString(match[2], "_") // Replace all spaces with underscore
		if existing, ok := vgpuTypeNames[typeID]; ok {
			if existing != vGpuName {
				log.Printf("Error: conflicting names for vGPU type %s: %s and %s, keeping %s", typeID, existing, vGpuName, existing)
			}
			continue
		}
		vgpuTypeNames[typeID] = vGpuName
	}
}

// readVfioVgpuFileFunc reads a file from the vendor-specific sysfs directory
// of a vGPU-capable PCI function.
func readVfioVgpuFileFunc(basePath string, deviceAddress string, fileName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(basePath, deviceAddress, vfioVGpuSysfsDir, fileName))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// isPhysicalFunctionFunc returns true if a device is an SR-IOV Physical
// Function. Only Physical Functions expose the sriov_totalvfs attribute.
func isPhysicalFunctionFunc(basePath string, deviceAddress string) bool {
	_, err := os.Stat(filepath.Join(basePath, deviceAddress, sriovTotalVfsFile))
	return err == nil
}
