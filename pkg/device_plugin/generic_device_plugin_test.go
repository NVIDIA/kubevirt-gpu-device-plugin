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
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"kubevirt-gpu-device-plugin/pkg/fabricmanager"
)

var devices []*pluginapi.Device
var iommuGroup1 = "1"
var iommuGroup2 = "2"
var iommuGroup3 = "3"
var iommuGroup4 = "4"
var pciAddress1 = "11"
var pciAddress2 = "22"
var pciAddress3 = "33"
var pciAddress4 = "44"
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
	tempMap[iommuGroup1] = append(tempMap[iommuGroup1], NvidiaGpuDevice{addr: pciAddress1, numaNode: 0})
	tempMap[iommuGroup2] = append(tempMap[iommuGroup2], NvidiaGpuDevice{addr: pciAddress2, numaNode: 1})
	tempMap[iommuGroup3] = append(tempMap[iommuGroup3], NvidiaGpuDevice{addr: pciAddress3, numaNode: 2})
	return tempMap
}

func getFakeBdfToIommuMap() map[string]string {
	return map[string]string{
		pciAddress1: iommuGroup1,
		pciAddress2: iommuGroup2,
		pciAddress3: iommuGroup3,
	}
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
		returnBdfToIommuMap = getFakeBdfToIommuMap
		readLink = getFakeLink
		readIDFromFile = getFakeIDFromFile
		var devs []*pluginapi.Device
		workDir, err = os.MkdirTemp("", "kubevirt-test")
		Expect(err).ToNot(HaveOccurred())
		rootPath = workDir

		devicePath = path.Join(workDir, iommuGroup1)
		fileObj, err := os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()

		devicePath = path.Join(workDir, iommuGroup2)
		fileObj, err = os.Create(devicePath)
		Expect(err).ToNot(HaveOccurred())
		fileObj.Close()
		basePath = workDir

		devs = append(devs, &pluginapi.Device{
			ID:     pciAddress1,
			Health: pluginapi.Healthy,
		})
		devs = append(devs, &pluginapi.Device{
			ID:     pciAddress2,
			Health: pluginapi.Healthy,
		})
		dpi = NewGenericDevicePlugin("foo", workDir+"/", devs)
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
		devs := []string{pciAddress1}
		envKey := gpuPrefix + "_FOO"
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

	It("Should allocate a device without error with iommufd support", func() {
		Expect(os.MkdirAll(filepath.Join(workDir, "dev"), 0744)).To(Succeed())
		f, err := os.OpenFile(filepath.Join(workDir, "dev", "iommu"), os.O_RDONLY|os.O_CREATE, 0666)
		Expect(err).ToNot(HaveOccurred())
		f.Close()
		Expect(os.MkdirAll(filepath.Join(workDir, pciAddress1, "vfio-dev", "vfio3"), 0744)).To(Succeed())
		devs := []string{pciAddress1}
		envKey := gpuPrefix + "_FOO"
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).To(BeNil())
		Expect(responses.GetContainerResponses()[0].Envs[envKey]).To(Equal(pciAddress1))
		Expect(responses.GetContainerResponses()[0].Devices[0].HostPath).To(Equal("/dev/vfio/devices/vfio3"))
		Expect(responses.GetContainerResponses()[0].Devices[0].ContainerPath).To(Equal("/dev/vfio/devices/vfio3"))
		Expect(responses.GetContainerResponses()[0].Devices[0].Permissions).To(Equal("mrw"))
		Expect(responses.GetContainerResponses()[0].Devices[1].HostPath).To(Equal("/dev/vfio/vfio"))
		Expect(responses.GetContainerResponses()[0].Devices[1].ContainerPath).To(Equal("/dev/vfio/vfio"))
		Expect(responses.GetContainerResponses()[0].Devices[1].Permissions).To(Equal("mrw"))
		Expect(responses.GetContainerResponses()[0].Devices[2].HostPath).To(Equal("/dev/vfio/1"))
		Expect(responses.GetContainerResponses()[0].Devices[2].ContainerPath).To(Equal("/dev/vfio/1"))
		Expect(responses.GetContainerResponses()[0].Devices[2].Permissions).To(Equal("mrw"))
		Expect(responses.GetContainerResponses()[0].Devices[3].HostPath).To(Equal("/dev/iommu"))
		Expect(responses.GetContainerResponses()[0].Devices[3].ContainerPath).To(Equal("/dev/iommu"))
		Expect(responses.GetContainerResponses()[0].Devices[3].Permissions).To(Equal("mrw"))
	})

	It("Should not allocate a device and also throw an error", func() {
		devs := []string{pciAddress2}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, _ := dpi.Allocate(ctx, &requests)
		Expect(responses).To(BeNil())
	})

	It("Should not allocate a device and also throw an error", func() {
		devs := []string{pciAddress3}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, _ := dpi.Allocate(ctx, &requests)
		Expect(responses).To(BeNil())
	})

	It("Should not allocate a device if request contains unknown BDF", func() {
		devs := []string{pciAddress4}
		containerRequests := pluginapi.ContainerAllocateRequest{DevicesIDs: devs}
		requests := pluginapi.AllocateRequest{}
		requests.ContainerRequests = append(requests.ContainerRequests, &containerRequests)
		ctx := context.Background()
		responses, err := dpi.Allocate(ctx, &requests)
		Expect(err).ToNot(BeNil())
		Expect(responses).To(BeNil())
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
		Expect(devices[0].ID).To(Equal(pciAddress1))
		Expect(devices[1].ID).To(Equal(pciAddress2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))

		dpi.unhealthy <- pciAddress2
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(pciAddress1))
		Expect(devices[1].ID).To(Equal(pciAddress2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Unhealthy))

		dpi.healthy <- pciAddress2
		time.Sleep(1 * time.Second)
		Expect(devices[0].ID).To(Equal(pciAddress1))
		Expect(devices[1].ID).To(Equal(pciAddress2))
		Expect(devices[0].Health).To(Equal(pluginapi.Healthy))
		Expect(devices[1].Health).To(Equal(pluginapi.Healthy))
	})
})

// mockFMClient is a mock implementation of fabricmanager.Client for testing.
type mockFMClient struct {
	partitions  *fabricmanager.PartitionList
	err         error
	activatedID uint32
	activateErr error
	connected   bool
}

func (m *mockFMClient) Connect(ctx context.Context) error { return nil }
func (m *mockFMClient) Disconnect() error                 { return nil }
func (m *mockFMClient) IsConnected() bool                 { return m.connected }
func (m *mockFMClient) GetPartition(ctx context.Context, partitionID uint32) (*fabricmanager.Partition, error) {
	return nil, nil
}
func (m *mockFMClient) ActivatePartition(ctx context.Context, req *fabricmanager.ActivateRequest) error {
	m.activatedID = req.PartitionID
	return m.activateErr
}
func (m *mockFMClient) DeactivatePartition(ctx context.Context, partitionID uint32) error {
	return nil
}
func (m *mockFMClient) GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*fabricmanager.Partition, error) {
	return nil, nil
}
func (m *mockFMClient) GetPartitions(ctx context.Context) (*fabricmanager.PartitionList, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.partitions, nil
}

var _ = Describe("GetPreferredAllocation() FM Tests", func() {
	buildDevice := func(id string, node int64) *pluginapi.Device {
		return &pluginapi.Device{
			ID:     id,
			Health: pluginapi.Healthy,
			Topology: &pluginapi.TopologyInfo{
				Nodes: []*pluginapi.NUMANode{
					{ID: node},
				},
			},
		}
	}

	// pciToPhysical maps test PCI device names to unique physical module IDs.
	pciToPhysical := map[string]uint32{
		"gpu0": 0, "gpu1": 1, "gpu2": 2, "gpu3": 3,
	}

	// buildModuleMaps returns pciToModuleID and moduleIDToPCI maps for the test devices.
	buildModuleMaps := func() (map[string]uint32, map[uint32]string) {
		forward := make(map[string]uint32)
		reverse := make(map[uint32]string)
		for pci, mod := range pciToPhysical {
			forward[pci] = mod
			reverse[mod] = pci
		}
		return forward, reverse
	}

	// buildDeviceNUMAMap returns a map from device ID to NUMA node for the given devices.
	buildDeviceNUMAMap := func(devs []*pluginapi.Device) map[string]int64 {
		m := make(map[string]int64, len(devs))
		for _, dev := range devs {
			if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
				m[dev.ID] = dev.Topology.Nodes[0].ID
			}
		}
		return m
	}

	buildPartition := func(id uint32, pciBusIDs ...string) fabricmanager.Partition {
		gpus := make([]fabricmanager.GPU, len(pciBusIDs))
		for i, bdf := range pciBusIDs {
			gpus[i] = fabricmanager.GPU{PCIBusID: bdf, PhysicalID: pciToPhysical[bdf]}
		}
		return fabricmanager.Partition{
			PartitionID: id,
			NumGPUs:     uint32(len(pciBusIDs)),
			GPUs:        gpus,
		}
	}

	It("selects the single matching FM partition", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 1,
				Partitions: []fabricmanager.Partition{
					buildPartition(1, "gpu0", "gpu1"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
			buildDevice("gpu2", 1),
			buildDevice("gpu3", 1),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					AvailableDeviceIDs: []string{"gpu0", "gpu1", "gpu2", "gpu3"},
					AllocationSize:     2,
				},
			},
		}

		resp, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.ContainerResponses).To(HaveLen(1))
		Expect(resp.ContainerResponses[0].DeviceIDs).To(Equal([]string{"gpu0", "gpu1"}))
	})

	It("picks the partition with fewest NUMA nodes", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 2,
				Partitions: []fabricmanager.Partition{
					// Partition 1: GPUs span NUMA 0 and 1
					buildPartition(1, "gpu0", "gpu2"),
					// Partition 2: GPUs both on NUMA 1
					buildPartition(2, "gpu2", "gpu3"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
			buildDevice("gpu2", 1),
			buildDevice("gpu3", 1),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					AvailableDeviceIDs: []string{"gpu0", "gpu1", "gpu2", "gpu3"},
					AllocationSize:     2,
				},
			},
		}

		resp, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.ContainerResponses).To(HaveLen(1))
		// Partition 2 is preferred (both GPUs on NUMA 1, single node)
		Expect(resp.ContainerResponses[0].DeviceIDs).To(Equal([]string{"gpu2", "gpu3"}))
	})

	It("tie-breaks by lowest NUMA node ID when distinct node count is equal", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 2,
				Partitions: []fabricmanager.Partition{
					// Partition 1: GPUs both on NUMA 1
					buildPartition(1, "gpu2", "gpu3"),
					// Partition 2: GPUs both on NUMA 0
					buildPartition(2, "gpu0", "gpu1"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
			buildDevice("gpu2", 1),
			buildDevice("gpu3", 1),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					AvailableDeviceIDs: []string{"gpu0", "gpu1", "gpu2", "gpu3"},
					AllocationSize:     2,
				},
			},
		}

		resp, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.ContainerResponses).To(HaveLen(1))
		// Partition 2 is preferred (NUMA 0 < NUMA 1)
		Expect(resp.ContainerResponses[0].DeviceIDs).To(Equal([]string{"gpu0", "gpu1"}))
	})

	It("returns error when no partition matches the requested size", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 1,
				Partitions: []fabricmanager.Partition{
					// Partition has 4 GPUs, but we request 2
					buildPartition(1, "gpu0", "gpu1", "gpu2", "gpu3"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
			buildDevice("gpu2", 1),
			buildDevice("gpu3", 1),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					AvailableDeviceIDs: []string{"gpu0", "gpu1", "gpu2", "gpu3"},
					AllocationSize:     2,
				},
			},
		}

		_, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no fabric manager partition of size 2"))
	})

	It("returns error when must-include device is not in any matching partition", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 1,
				Partitions: []fabricmanager.Partition{
					buildPartition(1, "gpu0", "gpu1"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
			buildDevice("gpu2", 1),
			buildDevice("gpu3", 1),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					AvailableDeviceIDs:   []string{"gpu0", "gpu1", "gpu2", "gpu3"},
					MustIncludeDeviceIDs: []string{"gpu2"},
					AllocationSize:       2,
				},
			},
		}

		_, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no fabric manager partition of size 2"))
	})

	It("skips partitions with unavailable GPUs", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 2,
				Partitions: []fabricmanager.Partition{
					// Partition 1 has gpu1 which is not available
					buildPartition(1, "gpu0", "gpu1"),
					// Partition 2 has both GPUs available
					buildPartition(2, "gpu2", "gpu3"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
			buildDevice("gpu2", 1),
			buildDevice("gpu3", 1),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					// gpu1 is NOT in the available list
					AvailableDeviceIDs: []string{"gpu0", "gpu2", "gpu3"},
					AllocationSize:     2,
				},
			},
		}

		resp, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.ContainerResponses).To(HaveLen(1))
		// Partition 2 is the only candidate
		Expect(resp.ContainerResponses[0].DeviceIDs).To(Equal([]string{"gpu2", "gpu3"}))
	})

	It("places must-include devices first in the result", func() {
		mock := &mockFMClient{
			partitions: &fabricmanager.PartitionList{
				NumPartitions: 1,
				Partitions: []fabricmanager.Partition{
					buildPartition(1, "gpu0", "gpu1"),
				},
			},
		}
		fwd, rev := buildModuleMaps()
		devs := []*pluginapi.Device{
			buildDevice("gpu0", 0),
			buildDevice("gpu1", 0),
		}
		dpi := &GenericDevicePlugin{
			deviceName:       "test",
			partitionManager: fabricmanager.NewPartitionManager(mock, fwd, rev, buildDeviceNUMAMap(devs)),
			devs:             devs,
		}

		request := &pluginapi.PreferredAllocationRequest{
			ContainerRequests: []*pluginapi.ContainerPreferredAllocationRequest{
				{
					AvailableDeviceIDs:   []string{"gpu0", "gpu1"},
					MustIncludeDeviceIDs: []string{"gpu1"},
					AllocationSize:       2,
				},
			},
		}

		resp, err := dpi.GetPreferredAllocation(context.Background(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.ContainerResponses).To(HaveLen(1))
		// gpu1 (must-include) comes first, then gpu0
		Expect(resp.ContainerResponses[0].DeviceIDs).To(Equal([]string{"gpu1", "gpu0"}))
	})
})
