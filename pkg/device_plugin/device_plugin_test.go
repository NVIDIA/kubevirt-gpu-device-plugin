/*
 * Copyright (c) 2019-2023, NVIDIA CORPORATION. All rights reserved.
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

func getFakeLinkDevicePlugin(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == deviceAddress1 {
		if link == "driver" {
			return "vfio-pci", nil
		} else if link == "iommu_group" {
			return "io_1", nil
		}
	} else if deviceAddress == deviceAddress2 {
		if link == "driver" {
			return "vfio-pci", nil
		} else if link == "iommu_group" {
			return "io_2", nil
		}
	} else if deviceAddress == deviceAddress3 {
		if link == "driver" {
			return "vfio-pci", nil
		} else if link == "iommu_group" {
			return "io_3", nil
		}
	} else if deviceAddress == deviceAddress5 {
		if link == "driver" {
			return "vfio-pci", nil
		}
	}
	return "", errors.New("Incorrect operation")
}

func getFakeIDFromFileDevicePlugin(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == deviceAddress1 || deviceAddress == deviceAddress4 || deviceAddress == deviceAddress5 {
		if link == "vendor" {
			return nvVendorID, nil
		} else if link == "device" {
			return deviceName, nil
		}
	} else if deviceAddress == deviceAddress2 {
		if link == "vendor" {
			return nvVendorID, nil
		} else if link == "device" {
			return deviceName1, nil
		}
	} else if deviceAddress == deviceAddress3 {
		if link == "vendor" {
			return nvVendorID, nil
		}
	}
	return "", errors.New("Incorrect operation")
}

func fakeStartDevicePluginFunc(dp *GenericDevicePlugin) error {
	if dp.deviceName == deviceName {
		return errors.New("Incorrect operation")
	}
	return nil
}

func getFakeVgpuIDFromFile(basePath string, deviceAddress string, property string) (string, error) {
	if deviceAddress == deviceAddress1 || deviceAddress == deviceAddress2 {
		return vgpuDeviceName, nil
	} else if deviceAddress == deviceAddress3 || deviceAddress == deviceAddress4 {
		return vgpuDeviceName1, nil
	}
	return "", errors.New("Incorrect operation")
}

func getFakeGpuIDforVpu(basePath string, deviceAddress string) (string, error) {
	if deviceAddress == deviceAddress1 || deviceAddress == deviceAddress2 || deviceAddress == deviceAddress3 {
		return "GpuId", nil
	}
	return "", errors.New("Incorrect operation")
}

func fakeStartVgpuDevicePluginFunc(dp *GenericVGpuDevicePlugin) error {
	if dp.deviceName == vgpuDeviceName {
		return nil
	}
	return errors.New("Incorrect operation")
}

var _ = Describe("Device Plugin", func() {
	var workDir string
	var linkDir string
	var err error

	Context("readLinkFunc() Tests", func() {
		BeforeEach(func() {
			linkDir, err = os.MkdirTemp("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			os.Mkdir(linkDir+"/vfio-pci", 0755)

			workDir, err = os.MkdirTemp("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())

			os.Mkdir(workDir+"/"+deviceAddress1, 0755)

			os.Symlink(linkDir+"/vfio-pci", filepath.Join(workDir, deviceAddress1, "driver"))

		})

		It("Read driver with out error", func() {
			driverID, err := readLinkFunc(workDir, deviceAddress1, "driver")
			Expect(err).To(BeNil())
			Expect(driverID).To(Equal("vfio-pci"))

		})

		It("Read driver from a missing location to throw error", func() {
			driverID, err := readLinkFunc(workDir, deviceAddress1, "iommu_group")
			Expect(err).ShouldNot(BeNil())
			Expect(driverID).To(Equal(""))

		})
	})

	Context("readIDFromFileFunc() Tests", func() {
		BeforeEach(func() {
			workDir, err = os.MkdirTemp("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			err = os.Mkdir(workDir+"/1", 0755)
			Expect(err).ToNot(HaveOccurred())
			err = os.WriteFile(filepath.Join(workDir, deviceAddress1, "vendor"), []byte("0x10de"), 0644)
			Expect(err).ToNot(HaveOccurred())
		})

		It("Read driver with out error", func() {
			driverID, err := readIDFromFileFunc(workDir, deviceAddress1, "vendor")
			Expect(err).To(BeNil())
			Expect(driverID).To(Equal(nvVendorID))
		})

		It("Read driver from a missing location to throw error", func() {
			driverID, err := readIDFromFileFunc(workDir, deviceAddress1, "iommu_group")
			Expect(err).ShouldNot(BeNil())
			Expect(driverID).To(Equal(""))
		})
	})

	Context("readVgpuIDFromFile() Tests", func() {
		BeforeEach(func() {
			readVgpuIDFromFile = readVgpuIDFromFileFunc
			workDir, err = os.MkdirTemp("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			err = os.Mkdir(workDir+"/1", 0755)
			Expect(err).ToNot(HaveOccurred())
			err = os.WriteFile(filepath.Join(workDir, deviceAddress1, "name"), []byte("GRID P100X-1B"), 0644)
			Expect(err).ToNot(HaveOccurred())
		})

		It("Read vgpu id with out error", func() {
			gpuID, err := readVgpuIDFromFile(workDir, deviceAddress1, "name")
			Expect(err).To(BeNil())
			Expect(gpuID).To(Equal("GRID_P100X-1B"))
		})

		It("Read vgpu id from a missing location to throw error", func() {
			gpuID, err := readVgpuIDFromFile(workDir, deviceAddress1, "error")
			Expect(err).ShouldNot(BeNil())
			Expect(gpuID).To(Equal(""))
		})

	})

	Context("readGpuIDForVgpu() Tests", func() {
		BeforeEach(func() {
			linkDir, err = os.MkdirTemp("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			err = os.Mkdir(linkDir+"/vfio-pci", 0755)
			Expect(err).ToNot(HaveOccurred())

			workDir, err = os.MkdirTemp("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())

			err = os.Mkdir(workDir+"/1", 0755)
			Expect(err).ToNot(HaveOccurred())

			err = os.Symlink(linkDir+"/vfio-pci", filepath.Join(workDir, deviceAddress1, "driver"))
			Expect(err).ToNot(HaveOccurred())

		})

		It("Read gpu id corresponding to Vgpu with out error", func() {
			driverID, err := readGpuIDForVgpu(workDir, "1/driver")
			Expect(err).To(BeNil())
			Expect(driverID).To(Equal(filepath.Base(linkDir)))
		})

		It("Read gpu id from a missing location to throw error", func() {
			gpuID, err := readGpuIDForVgpu(workDir, "1/error")
			Expect(err).ShouldNot(BeNil())
			Expect(gpuID).To(Equal(""))
		})

	})

	Context("createIommuDeviceMap() Tests", func() {

		BeforeEach(func() {
			linkDir, err = os.MkdirTemp("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			workDir, err = os.MkdirTemp("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			basePath = workDir
			os.Mkdir(filepath.Join(linkDir, deviceAddress1), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress2), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress3), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress4), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress5), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress6), 0755)

			os.Symlink(filepath.Join(linkDir, deviceAddress1), filepath.Join(workDir, deviceAddress1))
			os.Symlink(filepath.Join(linkDir, deviceAddress2), filepath.Join(workDir, deviceAddress2))
			os.Symlink(filepath.Join(linkDir, deviceAddress3), filepath.Join(workDir, deviceAddress3))
			os.Symlink(filepath.Join(linkDir, deviceAddress4), filepath.Join(workDir, deviceAddress4))
			os.Symlink(filepath.Join(linkDir, deviceAddress5), filepath.Join(workDir, deviceAddress5))
			os.Symlink(filepath.Join(linkDir, deviceAddress6), filepath.Join(workDir, deviceAddress6))

		})

		It("When RegisterAll set to default false", func() {
			readLink = getFakeLinkDevicePlugin
			readIDFromFile = getFakeIDFromFileDevicePlugin
			startDevicePlugin = fakeStartDevicePluginFunc
			createIommuDeviceMap()

			iommuList := iommuMap["io_1"]
			Expect(iommuList[0].addr).To(Equal("1"))
			deviceList := deviceMap["1b80"]
			Expect(deviceList[0]).To(Equal("io_1"))

			go createDevicePlugins()
			time.Sleep(3 * time.Second)
			stop <- struct{}{}

		})
		It("When RegisterAll = true", func() {
			readLink = getFakeLinkDevicePlugin
			readIDFromFile = getFakeIDFromFileDevicePlugin
			startDevicePlugin = fakeStartDevicePluginFunc
			RegisterAll = true
			createIommuDeviceMap()

			iommuList := iommuMap["io_1|1"]
			Expect(iommuList[0].addr).To(Equal("1"))
			deviceList := deviceMap["1b80"]
			Expect(deviceList[0]).To(Equal("io_1|1"))

			go createDevicePlugins()
			time.Sleep(3 * time.Second)
			stop <- struct{}{}

		})
	})

	Context("createVgpuIDMap() Tests", func() {

		BeforeEach(func() {
			linkDir, err = os.MkdirTemp("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			workDir, err = os.MkdirTemp("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			vGpuBasePath = workDir
			os.Mkdir(filepath.Join(linkDir, deviceAddress1), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress2), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress3), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress4), 0755)
			os.Mkdir(filepath.Join(linkDir, deviceAddress5), 0755)

			os.Symlink(filepath.Join(linkDir, deviceAddress1), filepath.Join(workDir, deviceAddress1))
			os.Symlink(filepath.Join(linkDir, deviceAddress2), filepath.Join(workDir, deviceAddress2))
			os.Symlink(filepath.Join(linkDir, deviceAddress3), filepath.Join(workDir, deviceAddress3))
			os.Symlink(filepath.Join(linkDir, deviceAddress4), filepath.Join(workDir, deviceAddress4))
			os.Symlink(filepath.Join(linkDir, deviceAddress5), filepath.Join(workDir, deviceAddress5))

		})

		It("", func() {
			readVgpuIDFromFile = getFakeVgpuIDFromFile
			readGpuIDForVgpu = getFakeGpuIDforVpu
			startVgpuDevicePlugin = fakeStartVgpuDevicePluginFunc

			createVgpuIDMap()

			gpuList := gpuVgpuMap["GpuId"]
			Expect(gpuList[0]).To(Equal(deviceAddress1))
			vGpuList := vGpuMap["vGPUId"]
			Expect(vGpuList[0].addr).To(Equal(deviceAddress1))

			go createDevicePlugins()
			time.Sleep(3 * time.Second)
			stop <- struct{}{}

		})
	})

	Context("getDeviceName() Tests", func() {

		BeforeEach(func() {
			workDir, err = os.MkdirTemp("", "pci-test")
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
			err = os.WriteFile(filepath.Join(workDir, "pci.ids"), message, 0644)
			Expect(err).ToNot(HaveOccurred())
		})

		It("Retrives correct device name from pci.ids file", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName := getDeviceName("118a")
			Expect(deviceName).To(Equal("GK104GL_GRID_K520"))
		})

		It("Returns blank if the device id is not present", func() {
			deviceName := getDeviceName("abcd")
			Expect(deviceName).To(Equal(""))
		})

		It("Returns blank if error reading the pci.ids file", func() {
			pciIdsFilePath = filepath.Join(workDir, "fake")
			deviceName := getDeviceName("118c")
			Expect(deviceName).To(Equal(""))
		})

		It("Returns the device name from pci.ids in capital letters", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName := getDeviceName("118d")
			Expect(deviceName).To(Equal("GK104GL_GRID_K520"))
		})

		It("Replaces / and . with _", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName := getDeviceName("118e")
			Expect(deviceName).To(Equal("GK104_GL_GRID_K__520"))
		})

		It("Retrieves correct device name from pci.ids file even if another vendor's device shares device id", func() {
			pciIdsFilePath = filepath.Join(workDir, "pci.ids")
			deviceName := getDeviceName("2331")
			Expect(deviceName).To(Equal("GH100_H100_PCIE"))
		})
	})
})
