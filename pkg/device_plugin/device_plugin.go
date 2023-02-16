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
	"bufio"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/golang/glog"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

//Structure to hold details about Nvidia GPU Device
type NvidiaGpuDevice struct {
	addr string // PCI address of device
}

//Key is iommu group id and value is a list of gpu devices part of the iommu group
var iommuMap map[string][]NvidiaGpuDevice

//Keys are the distinct Nvidia GPU device ids present on system and value is the list of all iommu group ids which are of that device id
var deviceMap map[string][]string

//Key is vGPU Type and value is the list of Nvidia vGPUs of that type
var vGpuMap map[string][]NvidiaGpuDevice

// Key is the Nvidia GPU id and value is the list of associated vGPU ids
var gpuVgpuMap map[string][]string

var basePath = "/sys/bus/pci/devices"
var vGpuBasePath = "/sys/bus/mdev/devices"
var pciIdsFilePath = "/usr/pci.ids"
var readLink = readLinkFunc
var readIDFromFile = readIDFromFileFunc
var startDevicePlugin = startDevicePluginFunc
var readVgpuIDFromFile = readVgpuIDFromFileFunc
var readGpuIDForVgpu = readGpuIDForVgpuFunc
var startVgpuDevicePlugin = startVgpuDevicePluginFunc
var stop = make(chan struct{})

//
func InitiateDevicePlugin() {
	//Identifies GPUs and represents it in appropriate structures
	createIommuDeviceMap()
	//Identifies vGPUs and represents it in appropriate structures
	createVgpuIDMap()
	//Creates and starts device plugin
	createDevicePlugins()
}

//Starts gpu pass through and vGPU device plugin
func createDevicePlugins() {
	var devicePlugins []*GenericDevicePlugin
	var vGpuDevicePlugins []*GenericVGpuDevicePlugin
	var devs []*pluginapi.Device
	log.Printf("Iommu Map %s", iommuMap)
	log.Printf("Device Map %s", deviceMap)
	log.Println("vGPU Map ", vGpuMap)
	log.Println("GPU vGPU Map ", gpuVgpuMap)

	//Iterate over deivceMap to create device plugin for each type of GPU on the host
	for k, v := range deviceMap {
		devs = nil
		for _, dev := range v {
			devs = append(devs, &pluginapi.Device{
				ID:     dev,
				Health: pluginapi.Healthy,
			})
		}
		deviceName := getDeviceName(k)
		if deviceName == "" {
			log.Printf("Error: Could not find device name for device id: %s", k)
			deviceName = k
		}
		log.Printf("DP Name %s", deviceName)
		dp := NewGenericDevicePlugin(deviceName, "/sys/kernel/iommu_groups/", devs)
		err := startDevicePlugin(dp)
		if err != nil {
			log.Printf("Error starting %s device plugin: %v", dp.deviceName, err)
		} else {
			devicePlugins = append(devicePlugins, dp)
		}
	}
	//Iterate over vGpuMap to create device plugin for each type of vGPU on the host
	for k, v := range vGpuMap {
		devs = nil
		for _, dev := range v {
			devs = append(devs, &pluginapi.Device{
				ID:     dev.addr,
				Health: pluginapi.Healthy,
			})
		}
		deviceName := getDeviceName(k)
		if deviceName == "" {
			deviceName = k
		}
		log.Printf("DP Name %s", deviceName)
		dp := NewGenericVGpuDevicePlugin(deviceName, vGpuBasePath, devs)
		err := startVgpuDevicePlugin(dp)
		if err != nil {
			log.Printf("Error starting %s device plugin: %v", dp.deviceName, err)
		} else {
			vGpuDevicePlugins = append(vGpuDevicePlugins, dp)
		}
	}

	<-stop
	log.Printf("Shutting down device plugin controller")
	for _, v := range devicePlugins {
		v.Stop()
	}

	for _, v := range vGpuDevicePlugins {
		v.Stop()
	}

}

func startDevicePluginFunc(dp *GenericDevicePlugin) error {
	return dp.Start(stop)
}

func startVgpuDevicePluginFunc(dp *GenericVGpuDevicePlugin) error {
	return dp.Start(stop)
}

//Discovers all Nvidia GPUs which are loaded with VFIO-PCI driver and creates corresponding maps
func createIommuDeviceMap() {
	iommuMap = make(map[string][]NvidiaGpuDevice)
	deviceMap = make(map[string][]string)
	//Walk directory to discover pci devices
	filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing file path %q: %v\n", path, err)
			return err
		}
		if info.IsDir() {
			log.Println("Not a device, continuing")
			return nil
		}
		//Retrieve vendor for the device
		vendorID, err := readIDFromFile(basePath, info.Name(), "vendor")
		if err != nil {
			log.Println("Could not get vendor ID for device ", info.Name())
			return nil
		}

		//Nvidia vendor id is "10de". Proceed if vendor id is 10de
		if vendorID == "10de" {
			log.Println("Nvidia device ", info.Name())
			//Retrieve iommu group for the device
			driver, err := readLink(basePath, info.Name(), "driver")
			if err != nil {
				log.Println("Could not get driver for device ", info.Name())
				return nil
			}
			if driver == "vfio-pci" {
				iommuGroup, err := readLink(basePath, info.Name(), "iommu_group")
				if err != nil {
					log.Println("Could not get IOMMU Group for device ", info.Name())
					return nil
				}
				log.Println("Iommu Group " + iommuGroup)
				_, exists := iommuMap[iommuGroup]
				if !exists {
					deviceID, err := readIDFromFile(basePath, info.Name(), "device")
					if err != nil {
						log.Println("Could get deviceID for PCI address ", info.Name())
						return nil
					}
					log.Printf("Device Id %s", deviceID)
					deviceMap[deviceID] = append(deviceMap[deviceID], iommuGroup)
				}
				iommuMap[iommuGroup] = append(iommuMap[iommuGroup], NvidiaGpuDevice{info.Name()})
			}
		}
		return nil
	})
}

//Discovers all Nvidia vGPUs configured on a node and creates corresponding maps
func createVgpuIDMap() {
	vGpuMap = make(map[string][]NvidiaGpuDevice)
	gpuVgpuMap = make(map[string][]string)
	//Walk directory to discover vGPU devices
	filepath.Walk(vGpuBasePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error accessing file path %q: %v\n", path, err)
			return err
		}
		if info.IsDir() {
			log.Println("Not a device, continuing")
			return nil
		}
		//Read vGPU type name
		vGpuID, err := readVgpuIDFromFile(vGpuBasePath, info.Name(), "mdev_type/name")
		if err != nil {
			log.Println("Could not get vGPU type identifier for device ", info.Name())
			return nil
		}
		//Retrieve the gpu ID for this vGPU
		gpuID, err := readGpuIDForVgpu(vGpuBasePath, info.Name())
		if err != nil {
			log.Println("Could not get vGPU type identifier for device ", info.Name())
			return nil
		}
		log.Printf("Gpu id is %s", gpuID)
		log.Printf("Vgpu id is %s", vGpuID)
		gpuVgpuMap[gpuID] = append(gpuVgpuMap[gpuID], info.Name())
		vGpuMap[vGpuID] = append(vGpuMap[vGpuID], NvidiaGpuDevice{info.Name()})
		return nil
	})
}

//Read a file to retrieve ID
func readIDFromFileFunc(basePath string, deviceAddress string, property string) (string, error) {
	data, err := ioutil.ReadFile(filepath.Join(basePath, deviceAddress, property))
	if err != nil {
		glog.Errorf("Could not read %s for device %s: %s", property, deviceAddress, err)
		return "", err
	}
	id := strings.Trim(string(data[2:]), "\n")
	return id, nil
}

//Read a file link
func readLinkFunc(basePath string, deviceAddress string, link string) (string, error) {
	path, err := os.Readlink(filepath.Join(basePath, deviceAddress, link))
	if err != nil {
		glog.Errorf("Could not read link %s for device %s: %s", link, deviceAddress, err)
		return "", err
	}
	_, file := filepath.Split(path)
	return file, nil
}

//Read vGPU type name from the corresponding file
func readVgpuIDFromFileFunc(basePath string, deviceAddress string, property string) (string, error) {
	reg, _ := regexp.Compile("\\s+")
	data, err := ioutil.ReadFile(filepath.Join(basePath, deviceAddress, property))
	if err != nil {
		glog.Errorf("Could not read %s for device %s: %s", property, deviceAddress, err)
		return "", err
	}
	str := strings.Trim(string(data[:]), "\n")
	str = reg.ReplaceAllString(str, "_") // Replace all spaces with underscore
	return str, nil
}

//Read GPU id for a specific vGPU
func readGpuIDForVgpuFunc(basePath string, deviceAddress string) (string, error) {
	path, err := os.Readlink(filepath.Join(basePath, deviceAddress))
	if err != nil {
		glog.Errorf("Could not read link for device %s: %s", deviceAddress, err)
		return "", err
	}
	splitStr := strings.Split(path, "/")
	length := len(splitStr)
	return strings.Trim(splitStr[length-2], "\n"), nil

}

func getIommuMap() map[string][]NvidiaGpuDevice {
	return iommuMap
}

func getGpuVgpuMap() map[string][]string {
	return gpuVgpuMap
}

func getDeviceName(deviceID string) string {
	deviceName := ""
	file, err := os.Open(pciIdsFilePath)
	if err != nil {
		log.Printf("Error opening pci ids file %s", pciIdsFilePath)
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, deviceID) {
			splits := strings.Split(line, deviceID)
			if len(splits) != 2 {
				log.Printf("Error processing pci.ids file at line: %s", line)
				return deviceName
			}
			// fix matching
			if splits[0] != "" {
				continue
			}
			deviceName = strings.TrimSpace(splits[1])
			deviceName = strings.ToUpper(deviceName)
			deviceName = strings.Replace(deviceName, "/", "_", -1)
			deviceName = strings.Replace(deviceName, ".", "_", -1)
			reg, _ := regexp.Compile("\\s+")
			deviceName = reg.ReplaceAllString(deviceName, "_") // Replace all spaces with underscore
			reg, _ = regexp.Compile("[^a-zA-Z0-9_.]+")
			deviceName = reg.ReplaceAllString(deviceName, "") // Removes any char other than alphanumeric and underscore
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading pci ids file %s", err)
	}
	return deviceName
}
