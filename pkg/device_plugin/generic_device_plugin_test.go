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
	"context"
	"errors"
	"os"
	"path"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

var devices []*pluginapi.Device
var iommuGroup1 = "1"
var iommuGroup2 = "2"
var iommuGroup3 = "3"
var iommuGroup4 = "4"
var pciAddress1 = "11"
var pciAddress2 = "22"
var pciAddress3 = "33"
var nvVendorID = "10de"

type fakeDevicePluginListAndWatchServer struct {
	grpc.ServerStream
}

func (x *fakeDevicePluginListAndWatchServer) Send(m *pluginapi.ListAndWatchResponse) error {
	devices = m.Devices
	return nil
}

func getFakeIommuMap() map[string][]NvidiaGpuDevice {

	var tempMap map[string][]NvidiaGpuDevice = make(map[string][]NvidiaGpuDevice)
	tempMap[iommuGroup1] = append(tempMap[iommuGroup1], NvidiaGpuDevice{pciAddress1})
	tempMap[iommuGroup2] = append(tempMap[iommuGroup2], NvidiaGpuDevice{pciAddress2})
	tempMap[iommuGroup3] = append(tempMap[iommuGroup3], NvidiaGpuDevice{pciAddress3})
	return tempMap
}

func getFakeLink(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == pciAddress1 {
		return iommuGroup1, nil
	} else if deviceAddress == pciAddress2 {
		return iommuGroup2, nil
	} else {
		return "", errors.New("Incorrect operation")
	}
}

func getFakeIDFromFile(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == pciAddress1 {
		return nvVendorID, nil
	}
	return "", errors.New("Incorrect operation")

}

var _ = Describe("Generic Device", func() {
	var workDir string
	var err error
	var dpi *GenericDevicePlugin
	var stop chan struct{}
	var devicePath string

	BeforeEach(func() {
		returnIommuMap = getFakeIommuMap
		readLink = getFakeLink
		readIDFromFile = getFakeIDFromFile
		var devs []*pluginapi.Device
		workDir, err = os.MkdirTemp("", "kubevirt-test")
		Expect(err).ToNot(HaveOccurred())

		devicePath = path.Join(workDir, iommuGroup1)
		fileObj, err := os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()

		devicePath = path.Join(workDir, iommuGroup2)
		fileObj, err = os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()

		devs = append(devs, &pluginapi.Device{
			ID:     iommuGroup1,
			Health: pluginapi.Healthy,
		})
		devs = append(devs, &pluginapi.Device{
			ID:     iommuGroup2,
			Health: pluginapi.Healthy,
		})
		dpi = NewGenericDevicePlugin("foo", workDir+"/", devs, &Config{})
		stop = make(chan struct{})
		dpi.stop = stop

	})

	AfterEach(func() {
		close(stop)
		os.RemoveAll(workDir)
	})

	It("Should register a new device without error", func() {
		err := dpi.Stop()

		Expect(err).To(BeNil())
	})

	It("Should allocate a device without error", func() {
		devs := []string{iommuGroup1}
		envKey := gpuPrefix + "_foo"
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs[envKey]).To(Equal(pciAddress1))
		Expect(responses.GetContainerResponses()[0].Devices[0].HostPath).To(Equal("/dev/vfio/vfio"))
		Expect(responses.GetContainerResponses()[0].Devices[0].ContainerPath).To(Equal("/dev/vfio/vfio"))
		Expect(responses.GetContainerResponses()[0].Devices[0].Permissions).To(Equal("mrw"))
		Expect(responses.GetContainerResponses()[0].Devices[1].HostPath).To(Equal("/dev/vfio/1"))
		Expect(responses.GetContainerResponses()[0].Devices[1].ContainerPath).To(Equal("/dev/vfio/1"))
		Expect(responses.GetContainerResponses()[0].Devices[1].Permissions).To(Equal("mrw"))
	})

	It("Should not allocate a device and also throw an error", func() {
		devs := []string{iommuGroup2}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, _ := dpi.Allocate(ctx, &requests)
		Expect(responses).To(BeNil())
	})

	It("Should not allocate a device and also throw an error", func() {
		devs := []string{iommuGroup3}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, _ := dpi.Allocate(ctx, &requests)
		Expect(responses).To(BeNil())
	})

	It("Should not allocate a device but not throw an error", func() {
		devs := []string{iommuGroup4}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs[gpuPrefix]).To(Equal(""))
	})

	It("Should monitor health of device node", func() {
		go dpi.healthCheck()
		Expect(dpi.devs[0].Health).To(Equal(pluginapi.Healthy))

		time.Sleep(1 * time.Second)
		By("Removing a (fake) device node")
		os.Remove(devicePath)

		By("Creating a new (fake) device node")
		fileObj, err := os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()
	})

	It("Should list devices and then react to changes in the health of the devices", func() {

		fakeServer := &fakeDevicePluginListAndWatchServer{ServerStream: nil}
		fakeEmpty := &pluginapi.Empty{}
		go dpi.ListAndWatch(fakeEmpty, fakeServer)
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(iommuGroup1))
		Expect(devices[1].ID).To(Equal(iommuGroup2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))

		dpi.unhealthy <- iommuGroup2
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(iommuGroup1))
		Expect(devices[1].ID).To(Equal(iommuGroup2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Unhealthy))

		dpi.healthy <- iommuGroup2
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(iommuGroup1))
		Expect(devices[1].ID).To(Equal(iommuGroup2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))
	})
})
