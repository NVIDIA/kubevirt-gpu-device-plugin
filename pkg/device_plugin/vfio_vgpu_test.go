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
	"errors"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var vfioPfAddress = "0000:41:00.0"
var vfioVfAddress1 = "0000:41:00.4"
var vfioVfAddress2 = "0000:41:00.5"
var vfioVfAddress3 = "0000:41:00.6"
var vfioVfAddress4 = "0000:41:00.7"
var vfioPfAddress2 = "0000:81:00.0"
var vfioVfAddress5 = "0000:81:00.4"
var vfioVgpuTypeName1 = "NVIDIA H200X-141C"
var vfioVgpuTypeName2 = "NVIDIA H200X-1-18C"
var vfioVgpuResourceName1 = "NVIDIA_H200X-141C"
var vfioVgpuResourceName2 = "NVIDIA_H200X-1-18C"

var _ = Describe("Vendor-specific VFIO vGPU", func() {
	var workDir string
	var linkDir string
	var err error
	var origBasePath string

	type fakeDeviceOptions struct {
		vendor         string
		driver         string
		iommuGroup     string
		numaNode       string
		physicalFn     bool
		physfn         string // parent Physical Function address, for a VF
		currentType    string
		creatableTypes string
	}

	createFakeDevice := func(address string, options fakeDeviceOptions) {
		deviceDir := filepath.Join(linkDir, address)
		Expect(os.MkdirAll(deviceDir, 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(deviceDir, "vendor"), []byte(options.vendor+"\n"), 0644)).To(Succeed())
		Expect(os.Symlink(filepath.Join(linkDir, "drivers", options.driver), filepath.Join(deviceDir, "driver"))).To(Succeed())
		if options.iommuGroup != "" {
			Expect(os.Symlink(filepath.Join(linkDir, "iommu_groups", options.iommuGroup), filepath.Join(deviceDir, "iommu_group"))).To(Succeed())
		}
		if options.numaNode != "" {
			Expect(os.WriteFile(filepath.Join(deviceDir, "numa_node"), []byte(options.numaNode+"\n"), 0644)).To(Succeed())
		}
		if options.physicalFn {
			Expect(os.WriteFile(filepath.Join(deviceDir, "sriov_totalvfs"), []byte("32\n"), 0644)).To(Succeed())
		}
		if options.physfn != "" {
			Expect(os.Symlink(filepath.Join(workDir, options.physfn), filepath.Join(deviceDir, "physfn"))).To(Succeed())
		}
		if options.currentType != "" || options.creatableTypes != "" {
			Expect(os.MkdirAll(filepath.Join(deviceDir, "nvidia"), 0755)).To(Succeed())
		}
		if options.currentType != "" {
			Expect(os.WriteFile(filepath.Join(deviceDir, "nvidia", "current_vgpu_type"), []byte(options.currentType+"\n"), 0644)).To(Succeed())
		}
		if options.creatableTypes != "" {
			Expect(os.WriteFile(filepath.Join(deviceDir, "nvidia", "creatable_vgpu_types"), []byte(options.creatableTypes), 0644)).To(Succeed())
		}
		Expect(os.Symlink(deviceDir, filepath.Join(workDir, address))).To(Succeed())
	}

	BeforeEach(func() {
		readLink = readLinkFunc
		readIDFromFile = readIDFromFileFunc
		readNUMANode = readNUMANodeFunc
		readVfioVgpuFile = readVfioVgpuFileFunc
		isPhysicalFunction = isPhysicalFunctionFunc
		// NVML is unavailable in unit tests; resolve nothing by default.
		resolveVgpuTypeNamesViaNVML = func(pfAddress string) (map[string]string, error) {
			return map[string]string{}, nil
		}

		linkDir, err = os.MkdirTemp("", "vfio-vgpu-link")
		Expect(err).ToNot(HaveOccurred())
		workDir, err = os.MkdirTemp("", "vfio-vgpu-test")
		Expect(err).ToNot(HaveOccurred())
		origBasePath = basePath
		basePath = workDir
	})

	AfterEach(func() {
		basePath = origBasePath
		_ = os.RemoveAll(workDir)
		_ = os.RemoveAll(linkDir)
	})

	Context("mergeCreatableVgpuTypes() Tests", func() {
		It("Parses id and name pairs and sanitizes names", func() {
			vgpuTypeNames := make(map[string]string)
			mergeCreatableVgpuTypes(vgpuTypeNames, "1428 : NVIDIA H200X-141C\n1414 : NVIDIA H200X-1-18C\n")
			Expect(vgpuTypeNames).To(HaveLen(2))
			Expect(vgpuTypeNames["1428"]).To(Equal(vfioVgpuResourceName1))
			Expect(vgpuTypeNames["1414"]).To(Equal(vfioVgpuResourceName2))
		})

		It("Ignores lines that do not match the id and name format", func() {
			vgpuTypeNames := make(map[string]string)
			mergeCreatableVgpuTypes(vgpuTypeNames, "ID : vGPU Name\n\ngarbage\n1428 : NVIDIA H200X-141C\n")
			Expect(vgpuTypeNames).To(HaveLen(1))
			Expect(vgpuTypeNames["1428"]).To(Equal(vfioVgpuResourceName1))
		})

		It("Keeps the first name when merged entries conflict", func() {
			vgpuTypeNames := make(map[string]string)
			mergeCreatableVgpuTypes(vgpuTypeNames, "1428 : NVIDIA H200X-141C\n")
			mergeCreatableVgpuTypes(vgpuTypeNames, "1428 : NVIDIA CONFLICTING-NAME\n")
			Expect(vgpuTypeNames).To(HaveLen(1))
			Expect(vgpuTypeNames["1428"]).To(Equal(vfioVgpuResourceName1))
		})

		It("Handles empty content", func() {
			vgpuTypeNames := make(map[string]string)
			mergeCreatableVgpuTypes(vgpuTypeNames, "")
			Expect(vgpuTypeNames).To(BeEmpty())
		})
	})

	Context("readVfioVgpuFileFunc() Tests", func() {
		It("Reads a trimmed value without error", func() {
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				currentType: "1428",
			})
			value, err := readVfioVgpuFileFunc(basePath, vfioVfAddress1, "current_vgpu_type")
			Expect(err).To(BeNil())
			Expect(value).To(Equal("1428"))
		})

		It("Returns an error for a missing file", func() {
			value, err := readVfioVgpuFileFunc(basePath, vfioVfAddress1, "current_vgpu_type")
			Expect(err).ShouldNot(BeNil())
			Expect(value).To(Equal(""))
		})
	})

	Context("isPhysicalFunctionFunc() Tests", func() {
		It("Identifies a Physical Function by sriov_totalvfs", func() {
			createFakeDevice(vfioPfAddress, fakeDeviceOptions{
				vendor:     "0x10de",
				driver:     "nvidia",
				physicalFn: true,
			})
			Expect(isPhysicalFunctionFunc(basePath, vfioPfAddress)).To(BeTrue())
		})

		It("Does not identify a Virtual Function as a Physical Function", func() {
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor: "0x10de",
				driver: "nvidia",
			})
			Expect(isPhysicalFunctionFunc(basePath, vfioVfAddress1)).To(BeFalse())
		})
	})

	Context("createVfioVGpuMap() Tests", func() {
		BeforeEach(func() {
			iommuMap = nil
			bdfToIommuMap = nil
		})

		It("Discovers configured VFs and groups them by vGPU type name", func() {
			createFakeDevice(vfioPfAddress, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physicalFn:     true,
				creatableTypes: "1428 : " + vfioVgpuTypeName1 + "\n1414 : " + vfioVgpuTypeName2 + "\n",
			})
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				iommuGroup:  "71",
				numaNode:    "0",
				currentType: "1428",
			})
			createFakeDevice(vfioVfAddress2, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress,
				iommuGroup:     "72",
				numaNode:       "1",
				currentType:    "1414",
				creatableTypes: "1414 : " + vfioVgpuTypeName2 + "\n",
			})
			// Unconfigured VF
			createFakeDevice(vfioVfAddress3, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				iommuGroup:  "73",
				numaNode:    "0",
				currentType: "0",
			})
			// VF configured with a type that no creatable list resolves
			createFakeDevice(vfioVfAddress4, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				iommuGroup:  "74",
				numaNode:    "0",
				currentType: "9999",
			})
			// GPU passthrough device handled by the vfio-pci discovery
			createFakeDevice("0000:01:00.0", fakeDeviceOptions{
				vendor:     "0x10de",
				driver:     "vfio-pci",
				iommuGroup: "10",
			})
			// Device from another vendor
			createFakeDevice("0000:02:00.0", fakeDeviceOptions{
				vendor: "0x8086",
				driver: "nvidia",
			})

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(HaveLen(2))
			Expect(vfioVGpuMap[vfioVgpuResourceName1]).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1][0].addr).To(Equal(vfioVfAddress1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1][0].numaNode).To(Equal(int64(0)))
			Expect(vfioVGpuMap[vfioVgpuResourceName2]).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName2][0].addr).To(Equal(vfioVfAddress2))
			Expect(vfioVGpuMap[vfioVgpuResourceName2][0].numaNode).To(Equal(int64(1)))

			Expect(bdfToIommuMap[vfioVfAddress1]).To(Equal("71"))
			Expect(bdfToIommuMap[vfioVfAddress2]).To(Equal("72"))
			Expect(iommuMap["71"][0].addr).To(Equal(vfioVfAddress1))
			Expect(iommuMap["72"][0].addr).To(Equal(vfioVfAddress2))

			Expect(bdfToIommuMap).ToNot(HaveKey(vfioPfAddress))
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress3))
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress4))
		})

		It("Resolves a reduced-catalog type via the parent PF's NVML, never another card's catalog", func() {
			// Target card is fully consumed: its creatable list is reduced to
			// the header, so the card's own catalog cannot resolve the type.
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress,
				iommuGroup:     "71",
				numaNode:       "0",
				currentType:    "1428",
				creatableTypes: "ID    : vGPU Name\n",
			})
			// A free function on ANOTHER card maps the SAME numeric id to a
			// DIFFERENT profile. The target card contributed no value, so this
			// entry would look "unambiguous" to a host-wide merge, but numeric
			// ids are only card-local: it must never be borrowed.
			createFakeDevice(vfioVfAddress5, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress2,
				iommuGroup:     "81",
				numaNode:       "0",
				currentType:    "0",
				creatableTypes: "1428 : NVIDIA OTHER-NAME\n",
			})
			// NVML is the authoritative per-PF source and reports the correct
			// profile for the target card's id 1428.
			resolveVgpuTypeNamesViaNVML = func(pfAddress string) (map[string]string, error) {
				if pfAddress == vfioPfAddress {
					return map[string]string{"1428": vfioVgpuResourceName1}, nil
				}
				return map[string]string{}, nil
			}

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1]).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1][0].addr).To(Equal(vfioVfAddress1))
			// The other card's mapping for id 1428 was never borrowed.
			Expect(vfioVGpuMap).ToNot(HaveKey("NVIDIA_OTHER-NAME"))
		})

		It("Resolves a type via NVML when no sysfs catalog lists it", func() {
			// Every function on the host is consumed: all catalogs reduced.
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress,
				iommuGroup:     "71",
				numaNode:       "0",
				currentType:    "1428",
				creatableTypes: "ID    : vGPU Name\n",
			})
			resolvedPFs := []string{}
			resolveVgpuTypeNamesViaNVML = func(pfAddress string) (map[string]string, error) {
				resolvedPFs = append(resolvedPFs, pfAddress)
				return map[string]string{"1428": vfioVgpuResourceName1}, nil
			}

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1]).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1][0].addr).To(Equal(vfioVfAddress1))
			Expect(resolvedPFs).To(Equal([]string{vfioPfAddress}))
		})

		It("Skips the VF when NVML resolution fails too", func() {
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress,
				iommuGroup:     "71",
				numaNode:       "0",
				currentType:    "1428",
				creatableTypes: "ID    : vGPU Name\n",
			})
			resolveVgpuTypeNamesViaNVML = func(pfAddress string) (map[string]string, error) {
				return nil, errors.New("nvml unavailable")
			}

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(BeEmpty())
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress1))
		})

		It("Skips a reduced-catalog VF when NVML is unavailable, never borrowing another card's catalog", func() {
			// Target card is fully consumed: its creatable list is reduced to
			// the header.
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress,
				iommuGroup:     "71",
				numaNode:       "0",
				currentType:    "1428",
				creatableTypes: "ID    : vGPU Name\n",
			})
			// Another card maps the same numeric id to a DIFFERENT profile.
			// With NVML unavailable, the target VF must be skipped rather than
			// advertised under this other card's (card-local) mapping.
			createFakeDevice(vfioVfAddress5, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress2,
				iommuGroup:     "81",
				numaNode:       "0",
				currentType:    "0",
				creatableTypes: "1428 : NVIDIA OTHER-NAME\n",
			})
			resolveVgpuTypeNamesViaNVML = func(pfAddress string) (map[string]string, error) {
				return nil, errors.New("nvml unavailable")
			}

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(BeEmpty())
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress1))
		})

		It("Resolves a type name from a sibling unconfigured VF", func() {
			// Configured VF with an empty creatable list
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				iommuGroup:  "71",
				numaNode:    "0",
				currentType: "1428",
			})
			// Sibling unconfigured VF still lists the creatable types
			createFakeDevice(vfioVfAddress2, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physfn:         vfioPfAddress,
				iommuGroup:     "72",
				numaNode:       "0",
				currentType:    "0",
				creatableTypes: "1428 : " + vfioVgpuTypeName1 + "\n",
			})

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1][0].addr).To(Equal(vfioVfAddress1))
		})

		It("Skips configured VFs when no creatable list resolves their type", func() {
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				iommuGroup:  "71",
				numaNode:    "0",
				currentType: "1428",
			})

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(BeEmpty())
		})

		It("Skips a VF whose parent Physical Function cannot be determined", func() {
			// VF with no physfn symlink at all (readLink fails)
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				iommuGroup:     "71",
				numaNode:       "0",
				currentType:    "1428",
				creatableTypes: "1428 : " + vfioVgpuTypeName1 + "\n",
			})

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(BeEmpty())
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress1))
		})

		It("Scopes vGPU type ids per physical card so colliding ids on different cards resolve to distinct profiles", func() {
			// Card 1: PF + one configured VF using type id 50 for profile 1
			createFakeDevice(vfioPfAddress, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physicalFn:     true,
				creatableTypes: "50 : " + vfioVgpuTypeName1 + "\n",
			})
			createFakeDevice(vfioVfAddress1, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress,
				iommuGroup:  "71",
				numaNode:    "0",
				currentType: "50",
			})
			// Card 2: a different physical card that happens to reuse type id
			// 50 for a completely different profile
			createFakeDevice(vfioPfAddress2, fakeDeviceOptions{
				vendor:         "0x10de",
				driver:         "nvidia",
				physicalFn:     true,
				creatableTypes: "50 : " + vfioVgpuTypeName2 + "\n",
			})
			createFakeDevice(vfioVfAddress5, fakeDeviceOptions{
				vendor:      "0x10de",
				driver:      "nvidia",
				physfn:      vfioPfAddress2,
				iommuGroup:  "75",
				numaNode:    "0",
				currentType: "50",
			})

			createVfioVGpuMap()

			Expect(vfioVGpuMap).To(HaveLen(2))
			Expect(vfioVGpuMap[vfioVgpuResourceName1]).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName1][0].addr).To(Equal(vfioVfAddress1))
			Expect(vfioVGpuMap[vfioVgpuResourceName2]).To(HaveLen(1))
			Expect(vfioVGpuMap[vfioVgpuResourceName2][0].addr).To(Equal(vfioVfAddress5))
		})
	})

	Context("createDevicePlugins() vfio vGPU Tests", func() {
		var startedPlugins []string

		BeforeEach(func() {
			startedPlugins = nil
			startDevicePlugin = func(dp *GenericDevicePlugin) error {
				startedPlugins = append(startedPlugins, dp.deviceName)
				return nil
			}
			startVgpuDevicePlugin = func(dp *GenericVGpuDevicePlugin) error {
				return nil
			}

			deviceMap = make(map[string][]NvidiaGpuDevice)
			iommuMap = make(map[string][]NvidiaGpuDevice)
			bdfToIommuMap = make(map[string]string)
			gpuVgpuMap = make(map[string][]string)
			vGpuMap = make(map[string][]NvidiaGpuDevice)
			vfioVGpuMap = map[string][]NvidiaGpuDevice{
				vfioVgpuResourceName1: {{addr: vfioVfAddress1, numaNode: 0}},
				vfioVgpuResourceName2: {{addr: vfioVfAddress2, numaNode: 0}},
			}
		})

		AfterEach(func() {
			startDevicePlugin = startDevicePluginFunc
			startVgpuDevicePlugin = startVgpuDevicePluginFunc
			deviceMap = nil
			iommuMap = nil
			bdfToIommuMap = nil
			gpuVgpuMap = nil
			vGpuMap = nil
			vfioVGpuMap = nil
		})

		It("Starts a PCI-style device plugin per discovered vGPU profile", func() {
			go createDevicePlugins()
			time.Sleep(1 * time.Second)
			stop <- struct{}{}

			Expect(startedPlugins).To(ConsistOf(vfioVgpuResourceName1, vfioVgpuResourceName2))
		})
	})
})
