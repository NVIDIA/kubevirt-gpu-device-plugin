/*
 * Copyright (c) 2019, NVIDIA CORPORATION. All rights reserved.
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
	"io/ioutil"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"gitlab.com/nvidia/cloud-native/go-nvlib/pkg/nvmdev"
	"gitlab.com/nvidia/cloud-native/go-nvlib/pkg/nvpci"
)

var deviceAddress1 = "1"
var deviceAddress2 = "2"
var deviceAddress3 = "3"
var deviceAddress4 = "4"
var deviceAddress5 = "5"
var deviceAddress6 = "6"
var deviceName = "1b80"
var deviceName1 = "1b81"
var vgpuDeviceName = "vGPUId"
var vgpuDeviceName1 = "vGPUId1"

func fakeStartDevicePluginFunc(dp *GenericDevicePlugin) error {
	if dp.deviceName == deviceName {
		return errors.New("Incorrect operation")
	}
	return nil
}

func fakeStartVgpuDevicePluginFunc(dp *GenericVGpuDevicePlugin) error {
	if dp.deviceName == vgpuDeviceName {
		return nil
	}
	return errors.New("Incorrect operation")
}

var _ = Describe("Device Plugin", func() {
	var workDir string
	var err error

	Context("createIommuDeviceMap() Tests", func() {

		It("", func() {
			lib := &nvpciInterfaceMock{
				GetAllDevicesFunc: func() ([]*nvpci.NvidiaPCIDevice, error) {
					devices := []*nvpci.NvidiaPCIDevice{
						&nvpci.NvidiaPCIDevice{
							Address:    "0000:3B:00.0",
							Device:     0x2331,
							IommuGroup: 60,
							Driver:     "vfio-pci",
						},
						&nvpci.NvidiaPCIDevice{
							Address:    "0000:86:00.0",
							Device:     0x2332,
							IommuGroup: -1,
							Driver:     "nvidia",
						},
					}
					return devices, nil
				},
			}
			startDevicePlugin = fakeStartDevicePluginFunc
			createIommuDeviceMap(lib)

			Expect(len(iommuMap)).To(Equal(1))
			iommuList, exists := iommuMap["60"]
			Expect(exists).To(Equal(true))
			Expect(iommuList[0].Address).To(Equal("0000:3B:00.0"))

			Expect(len(deviceMap)).To(Equal(1))
			deviceList, exists := deviceMap[0x2331]
			Expect(exists).To(Equal(true))
			Expect(deviceList[0]).To(Equal("60"))

			go createDevicePlugins()
			time.Sleep(3 * time.Second)
			stop <- struct{}{}

		})
	})

	Context("createVgpuIDMap() Tests", func() {

		It("", func() {
			lib := &nvmdevInterfaceMock{
				GetAllDevicesFunc: func() ([]*nvmdev.Device, error) {
					devices := []*nvmdev.Device{
						&nvmdev.Device{
							UUID:       "1",
							MDEVType:   "NVIDIA A40-48Q",
							IommuGroup: 60,
							Parent: &nvmdev.ParentDevice{
								NvidiaPCIDevice: &nvpci.NvidiaPCIDevice{
									Address: "0000:20:00.0",
								},
							},
						},
						&nvmdev.Device{
							UUID:       "2",
							MDEVType:   "NVIDIA A16-16Q",
							IommuGroup: 120,
							Parent: &nvmdev.ParentDevice{
								NvidiaPCIDevice: &nvpci.NvidiaPCIDevice{
									Address: "0000:60:00.0",
								},
							},
						},
					}
					return devices, nil
				},
			}

			startVgpuDevicePlugin = fakeStartVgpuDevicePluginFunc
			createVgpuIDMap(lib)

			Expect(len(gpuVgpuMap)).To(Equal(2))
			vgpuList, exists := gpuVgpuMap["0000:20:00.0"]
			Expect(exists).To(Equal(true))
			Expect(vgpuList[0]).To(Equal("1"))

			vgpuDevList, exists := vGpuMap["NVIDIA_A40-48Q"]
			Expect(exists).To(Equal(true))
			Expect(vgpuDevList[0].UUID).To(Equal("1"))

			go createDevicePlugins()
			time.Sleep(3 * time.Second)
			stop <- struct{}{}

		})
	})

	Context("getDeviceName() Tests", func() {

		BeforeEach(func() {
			workDir, err = ioutil.TempDir("", "pci-test")
			Expect(err).ToNot(HaveOccurred())
			message := []byte(`
8086  Intel Corporation
	2331  DH89xxCC Chap Counter
10de NVIDIA Corporation
	118a  GK104GL [GRID K520]
	118b  GK104GL [GRID K2 GeForce USM]
	118d  gk104gl [grid k520]
	118e gk104.gl [grid/k./520]
	2331 GH100 [H100 PCIe]
`)
			ioutil.WriteFile(filepath.Join(workDir, "pci.ids"), message, 0644)
		})

		It("Retrives correct device name from pci.ids file", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName, err := getDeviceName(0x118a)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceName).To(Equal("GK104GL_GRID_K520"))
		})

		It("Returns blank if the device id is not present", func() {
			deviceName, err := getDeviceName(0xabcd)
			Expect(err).To(HaveOccurred())
			Expect(deviceName).To(Equal(""))
		})

		It("Returns the device name from pci.ids in capital letters", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName, err := getDeviceName(0x118d)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceName).To(Equal("GK104GL_GRID_K520"))
		})

		It("Replaces / and . with _", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName, err := getDeviceName(0x118e)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceName).To(Equal("GK104_GL_GRID_K__520"))
		})

		It("Retrieves correct device name from pci.ids file even if another vendor's device shares device id", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName, err := getDeviceName(0x2331)
			Expect(err).ToNot(HaveOccurred())
			Expect(deviceName).To(Equal("GH100_H100_PCIE"))
		})
	})
})
