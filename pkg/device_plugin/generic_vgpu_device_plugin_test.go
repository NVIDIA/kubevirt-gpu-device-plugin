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
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

func getFakeVgpuIDFromFileGeneric(basePath string, deviceAddress string, link string) (string, error) {
	if deviceAddress == "1" {
		return "foo", nil
	} else if deviceAddress == "22" {
		return "2", nil
	} else {
		return "", errors.New("Incorrect operation")
	}
}

func fakeNvmlInit() error {
	return nil
}

func fakeNvmlGetDeviceCount() (uint, error) {
	return 1, nil
}

func fakeNvmlNewDeviceLite(idx uint) (*nvml.Device, error) {
	device := &nvml.Device{
		Path: "/dev/nvidia",
		PCI: nvml.PCIInfo{
			BusID: "busID",
		},
	}
	return device, nil
}

func fakeNvmlShutdown() error {
	return nil
}

func fakeWatchXIDsFunc(devs []*nvml.Device, xids chan<- *nvml.Device) {
	time.Sleep(5 * time.Second)
	xids <- devs[0]
}

func fakeGetGpuVgpuMap() map[string][]string {
	var gpuVgpuMap = make(map[string][]string)
	gpuVgpuMap["busID"] = append(gpuVgpuMap["busID"], "1")
	return gpuVgpuMap
}

var _ = Describe("Generic Device", func() {
	var workDir string
	var dpi *GenericVGpuDevicePlugin
	var stop chan struct{}

	BeforeEach(func() {
		workDir, err := os.MkdirTemp("", "kubevirt-test")
		Expect(err).ToNot(HaveOccurred())

		// create dummy vGPU devices
		var devs []*pluginapi.Device
		readVgpuIDFromFile = getFakeVgpuIDFromFile

		devs = append(devs, &pluginapi.Device{
			ID:     "1",
			Health: pluginapi.Healthy,
		})
		devs = append(devs, &pluginapi.Device{
			ID:     "2",
			Health: pluginapi.Healthy,
		})

		f, err := os.Create(filepath.Join(workDir, "1"))
		Expect(err).To(BeNil())
		f.Close()
		f, err = os.Create(filepath.Join(workDir, "2"))
		Expect(err).To(BeNil())
		f.Close()

		// create dummy device-plugin socket
		pluginsDir, err := os.MkdirTemp("", "kubelet-device-plugins")
		Expect(err).To(BeNil())
		socketPath := filepath.Join(pluginsDir, "kubevirt-test.sock")
		err = os.WriteFile(socketPath, []byte{}, 0755)
		Expect(err).To(BeNil())

		dpi = NewGenericVGpuDevicePlugin("vGPUId", workDir+"/", devs)
		stop = make(chan struct{})
		dpi.socketPath = socketPath
		dpi.stop = stop
		nvmlInit = fakeNvmlInit
		nvmlGetDeviceCount = fakeNvmlGetDeviceCount
		nvmlNewDeviceLite = fakeNvmlNewDeviceLite
		nvmlShutdown = fakeNvmlShutdown
		watchXIDs = fakeWatchXIDsFunc
		returnGpuVgpuMap = fakeGetGpuVgpuMap
	})

	AfterEach(func() {
		close(stop)
		os.RemoveAll(workDir)
	})

	It("Should stop device plugin without error", func() {
		err := dpi.Stop()
		Expect(err).To(BeNil())
	})

	It("Should allocate a device without error", func() {
		devs := []string{"1"}
		envKey := vgpuPrefix + "_vGPUId"
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs[envKey]).To(Equal("1"))
	})

	It("Should not allocate a device", func() {
		devs := []string{"3"}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs[vgpuPrefix]).To(Equal(""))
		Expect(responses.GetContainerResponses()[0].Devices[0].HostPath).To(Equal("/dev/vfio"))
	})

	/*
		It("Should monitor health of device node", func() {
			go dpi.healthCheck()
			Expect(dpi.devs[0].Health).To(Equal(pluginapi.Healthy))
			//time.Sleep(5 * time.Second)
			unhealthy := <-dpi.unhealthy
			Expect(unhealthy).To(Equal("1"))
		})
	*/

	It("Should list devices and then react to changes in the health of the devices", func() {

		fakeServer := &fakeDevicePluginListAndWatchServer{ServerStream: nil}
		fakeEmpty := &pluginapi.Empty{}
		go dpi.ListAndWatch(fakeEmpty, fakeServer)
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal("1"))
		Expect(devices[1].ID).To(Equal("2"))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))

		dpi.unhealthy <- "2"
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal("1"))
		Expect(devices[1].ID).To(Equal("2"))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Unhealthy))

		dpi.healthy <- "2"
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal("1"))
		Expect(devices[1].ID).To(Equal("2"))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))
	})

})
