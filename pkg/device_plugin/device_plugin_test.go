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
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func getFakeLinkDevicePlugin(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == "1" {
		if link == "driver" {
			return "vfio-pci", nil
		} else if link == "iommu_group" {
			return "io_1", nil
		}
	} else if deviceAddress == "2" {
		if link == "driver" {
			return "vfio-pci", nil
		} else if link == "iommu_group" {
			return "io_2", nil
		}
	} else if deviceAddress == "3" {
		if link == "driver" {
			return "vfio-pci", nil
		} else if link == "iommu_group" {
			return "io_3", nil
		}
	} else if deviceAddress == "5" {
		if link == "driver" {
			return "vfio-pci", nil
		}
	}
	return "", errors.New("Incorrect operation")
}

func getFakeIDFromFileDevicePlugin(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == "1" || deviceAddress == "4" || deviceAddress == "5" {
		if link == "vendor" {
			return "10de", nil
		} else if link == "device" {
			return "1b80", nil
		}
	} else if deviceAddress == "2" {
		if link == "vendor" {
			return "10de", nil
		} else if link == "device" {
			return "1b81", nil
		}
	} else if deviceAddress == "3" {
		if link == "vendor" {
			return "10de", nil
		}
	}
	return "", errors.New("Incorrect operation")
}

func fakeStartDevicePluginFunc(dp *GenericDevicePlugin) error {
	if dp.deviceName == "1b81" {
		return errors.New("Incorrect operation")
	}
	return nil
}

func getFakeVgpuIDFromFile(basePath string, deviceAddress string, property string) (string, error) {
	if deviceAddress == "1" || deviceAddress == "2" {
		return "vGPUId", nil
	} else if deviceAddress == "3" || deviceAddress == "4" {
		return "vGPUId1", nil
	}
	return "", errors.New("Incorrect operation")
}

func getFakeGpuIDforVpu(basePath string, deviceAddress string) (string, error) {
	if deviceAddress == "1" || deviceAddress == "2" || deviceAddress == "3" {
		return "GpuId", nil
	}
	return "", errors.New("Incorrect operation")
}

func fakeStartVgpuDevicePluginFunc(dp *GenericVGpuDevicePlugin) error {
	if dp.deviceName == "vGPUId" {
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
			linkDir, err = ioutil.TempDir("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			os.Mkdir(linkDir+"/vfio-pci", 0755)

			workDir, err = ioutil.TempDir("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())

			os.Mkdir(workDir+"/1", 0755)

			os.Symlink(linkDir+"/vfio-pci", filepath.Join(workDir, "1", "driver"))

		})

		It("Read driver with out error", func() {
			driverID, err := readLinkFunc(workDir, "1", "driver")
			Expect(err).To(BeNil())
			Expect(driverID).To(Equal("vfio-pci"))

		})

		It("Read driver from a missing location to throw error", func() {
			driverID, err := readLinkFunc(workDir, "1", "iommu_group")
			Expect(err).ShouldNot(BeNil())
			Expect(driverID).To(Equal(""))

		})
	})

	Context("readIDFromFileFunc() Tests", func() {
		BeforeEach(func() {
			workDir, err = ioutil.TempDir("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			os.Mkdir(workDir+"/1", 0755)
			ioutil.WriteFile(filepath.Join(workDir, "1", "vendor"), []byte("0x10de"), 0644)
		})

		It("Read driver with out error", func() {
			driverID, err := readIDFromFileFunc(workDir, "1", "vendor")
			Expect(err).To(BeNil())
			Expect(driverID).To(Equal("10de"))
		})

		It("Read driver from a missing location to throw error", func() {
			driverID, err := readIDFromFileFunc(workDir, "1", "iommu_group")
			Expect(err).ShouldNot(BeNil())
			Expect(driverID).To(Equal(""))
		})
	})

	Context("readVgpuIDFromFile() Tests", func() {
		BeforeEach(func() {
			readVgpuIDFromFile = readVgpuIDFromFileFunc
			workDir, err = ioutil.TempDir("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			os.Mkdir(workDir+"/1", 0755)
			ioutil.WriteFile(filepath.Join(workDir, "1", "name"), []byte("GRID P100X-1B"), 0644)
		})

		It("Read vgpu id with out error", func() {
			gpuID, err := readVgpuIDFromFile(workDir, "1", "name")
			Expect(err).To(BeNil())
			Expect(gpuID).To(Equal("P100X-1B"))
		})

		It("Read vgpu id from a missing location to throw error", func() {
			gpuID, err := readVgpuIDFromFile(workDir, "1", "error")
			Expect(err).ShouldNot(BeNil())
			Expect(gpuID).To(Equal(""))
		})

	})

	Context("readGpuIDForVgpu() Tests", func() {
		BeforeEach(func() {
			linkDir, err = ioutil.TempDir("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			os.Mkdir(linkDir+"/vfio-pci", 0755)

			workDir, err = ioutil.TempDir("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())

			os.Mkdir(workDir+"/1", 0755)

			os.Symlink(linkDir+"/vfio-pci", filepath.Join(workDir, "1", "driver"))

		})

		It("Read gpu id corresponding to Vgpu with out error", func() {
			driverID, err := readGpuIDForVgpu(workDir, "1/driver")
			splitStr := strings.Split(linkDir, "/")
			Expect(err).To(BeNil())
			Expect(driverID).To(Equal(splitStr[2]))
		})

		It("Read gpu id from a missing location to throw error", func() {
			gpuID, err := readGpuIDForVgpu(workDir, "1/error")
			Expect(err).ShouldNot(BeNil())
			Expect(gpuID).To(Equal(""))
		})

	})

	Context("createIommuDeviceMap() Tests", func() {

		BeforeEach(func() {
			linkDir, err = ioutil.TempDir("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			workDir, err = ioutil.TempDir("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			basePath = workDir
			os.Mkdir(filepath.Join(linkDir, "1"), 0755)
			os.Mkdir(filepath.Join(linkDir, "2"), 0755)
			os.Mkdir(filepath.Join(linkDir, "3"), 0755)
			os.Mkdir(filepath.Join(linkDir, "4"), 0755)
			os.Mkdir(filepath.Join(linkDir, "5"), 0755)
			os.Mkdir(filepath.Join(linkDir, "6"), 0755)

			os.Symlink(filepath.Join(linkDir, "1"), filepath.Join(workDir, "1"))
			os.Symlink(filepath.Join(linkDir, "2"), filepath.Join(workDir, "2"))
			os.Symlink(filepath.Join(linkDir, "3"), filepath.Join(workDir, "3"))
			os.Symlink(filepath.Join(linkDir, "4"), filepath.Join(workDir, "4"))
			os.Symlink(filepath.Join(linkDir, "5"), filepath.Join(workDir, "5"))
			os.Symlink(filepath.Join(linkDir, "6"), filepath.Join(workDir, "6"))

		})

		It("", func() {
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
	})

	Context("createVgpuIDMap() Tests", func() {

		BeforeEach(func() {
			linkDir, err = ioutil.TempDir("", "dp-test")
			Expect(err).ToNot(HaveOccurred())

			workDir, err = ioutil.TempDir("", "kubevirt-test")
			Expect(err).ToNot(HaveOccurred())
			vGpuBasePath = workDir
			os.Mkdir(filepath.Join(linkDir, "1"), 0755)
			os.Mkdir(filepath.Join(linkDir, "2"), 0755)
			os.Mkdir(filepath.Join(linkDir, "3"), 0755)
			os.Mkdir(filepath.Join(linkDir, "4"), 0755)
			os.Mkdir(filepath.Join(linkDir, "5"), 0755)

			os.Symlink(filepath.Join(linkDir, "1"), filepath.Join(workDir, "1"))
			os.Symlink(filepath.Join(linkDir, "2"), filepath.Join(workDir, "2"))
			os.Symlink(filepath.Join(linkDir, "3"), filepath.Join(workDir, "3"))
			os.Symlink(filepath.Join(linkDir, "4"), filepath.Join(workDir, "4"))
			os.Symlink(filepath.Join(linkDir, "5"), filepath.Join(workDir, "5"))

		})

		It("", func() {
			readVgpuIDFromFile = getFakeVgpuIDFromFile
			readGpuIDForVgpu = getFakeGpuIDforVpu
			startVgpuDevicePlugin = fakeStartVgpuDevicePluginFunc

			createVgpuIDMap()

			gpuList := gpuVgpuMap["GpuId"]
			Expect(gpuList[0]).To(Equal("1"))
			vGpuList := vGpuMap["vGPUId"]
			Expect(vGpuList[0].addr).To(Equal("1"))

			go createDevicePlugins()
			time.Sleep(3 * time.Second)
			stop <- struct{}{}

		})
	})

	Context("getDeviceName() Tests", func() {

		BeforeEach(func() {
			workDir, err = ioutil.TempDir("", "pci-test")
			Expect(err).ToNot(HaveOccurred())
			message := []byte("118a  GK104GL [GRID K520] \n 118b  GK104GL [GRID K2 GeForce USM] \n 118c  GK104 [GRID 118c NVS USM]")
			ioutil.WriteFile(filepath.Join(workDir, "pci.ids"), message, 0644)
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

		It("Returns blank if the device name is not correctly formatted", func() {
			deviceName := getDeviceName("118c")
			Expect(deviceName).To(Equal(""))
		})

		It("Returns blank if error reading the pci.ids file", func() {
			pciIdsFilePath = filepath.Join(workDir, "fake")
			deviceName := getDeviceName("118c")
			Expect(deviceName).To(Equal(""))
		})
	})
})
