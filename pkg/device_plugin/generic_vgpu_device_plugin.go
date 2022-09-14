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
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"strings"

	"github.com/NVIDIA/gpu-monitoring-tools/bindings/go/nvml"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

var returnGpuVgpuMap = getGpuVgpuMap
var watchXIDs = watchXIDsFunc
var nvmlInit = nvml.Init
var nvmlGetDeviceCount = nvml.GetDeviceCount
var nvmlNewDeviceLite = nvml.NewDeviceLite
var nvmlShutdown = nvml.Shutdown

//Implements the kubernetes device plugin API
type GenericVGpuDevicePlugin struct {
	devs       []*pluginapi.Device
	server     *grpc.Server
	socketPath string
	stop       chan struct{}
	healthy    chan string
	unhealthy  chan string
	devicePath string
	deviceName string
	devsHealth []*pluginapi.Device
}

func NewGenericVGpuDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericVGpuDevicePlugin {
	log.Println("Devicename " + deviceName)
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)
	dpi := &GenericVGpuDevicePlugin{
		devs:       devices,
		socketPath: serverSock,
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
	}
	return dpi
}

// Start starts the gRPC server of the device plugin
func (dpi *GenericVGpuDevicePlugin) Start(stop chan struct{}) error {
	if dpi.server != nil {
		return fmt.Errorf("gRPC server already started")
	}

	dpi.stop = stop

	err := dpi.cleanup()
	if err != nil {
		return err
	}

	sock, err := net.Listen("unix", dpi.socketPath)
	if err != nil {
		log.Printf("[%s] Error creating GRPC server socket: %v", dpi.deviceName, err)
		return err
	}

	dpi.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(dpi.server, dpi)

	go dpi.server.Serve(sock)

	err = waitForGrpcServer(dpi.socketPath, connectionTimeout)
	if err != nil {
		// this err is returned at the end of the Start function
		log.Printf("[%s] Error connecting to GRPC server: %v", dpi.deviceName, err)
	}

	err = dpi.Register()
	if err != nil {
		log.Printf("[%s] Error registering with device plugin manager: %v", dpi.deviceName, err)
		return err
	}

	go dpi.healthCheck()

	log.Println(dpi.deviceName + " Device plugin server ready")

	return err
}

// Stop stops the gRPC server
func (dpi *GenericVGpuDevicePlugin) Stop() error {
	if dpi.server == nil {
		return nil
	}

	dpi.server.Stop()
	dpi.server = nil

	return dpi.cleanup()
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (dpi *GenericVGpuDevicePlugin) Register() error {
	conn, err := connect(pluginapi.KubeletSocket, connectionTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(dpi.socketPath),
		ResourceName: fmt.Sprintf("%s/%s", DeviceNamespace, dpi.deviceName),
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

//ListAndWatch lists devices and update that list according to the health status
func (dpi *GenericVGpuDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})

	for {
		select {
		case unhealthy := <-dpi.unhealthy:
			log.Printf("In watch unhealthy")
			for _, dev := range dpi.devs {
				if unhealthy == dev.ID {
					dev.Health = pluginapi.Unhealthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})
		case healthy := <-dpi.healthy:
			log.Printf("In watch healthy")
			for _, dev := range dpi.devs {
				if healthy == dev.ID {
					dev.Health = pluginapi.Healthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})
		case <-dpi.stop:
			return nil
		}
	}
}

//Performs pre allocation checks and allocates a devices based on the request
func (dpi *GenericVGpuDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Println("In allocate")
	responses := pluginapi.AllocateResponse{}

	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		envList := map[string][]string{}

		for _, str := range req.DevicesIDs {
			vGpuID, err := readVgpuIDFromFile(vGpuBasePath, str, "mdev_type/name")
			if err != nil || vGpuID != dpi.deviceName {
				log.Println("Could not get vGPU type identifier for device ", str)
				continue
			}

			key := fmt.Sprintf("%s_%s", vgpuPrefix, dpi.deviceName)
			if _, exists := envList[key]; !exists {
				envList[key] = []string{}
			}
			envList[key] = append(envList[key], str)
		}
		deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
			HostPath:      vfioDevicePath,
			ContainerPath: vfioDevicePath,
			Permissions:   "mrw",
		})

		envs := buildEnv(envList)
		log.Printf("Allocated devices %s", envs)
		response := pluginapi.ContainerAllocateResponse{
			Envs:    envs,
			Devices: deviceSpecs,
		}

		responses.ContainerResponses = append(responses.ContainerResponses, &response)
	}

	return &responses, nil
}

func (dpi *GenericVGpuDevicePlugin) cleanup() error {
	if err := os.Remove(dpi.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (dpi *GenericVGpuDevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}
	return options, nil
}

func (dpi *GenericVGpuDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

// GetPreferredAllocation is for compatible with new DevicePluginServer API for DevicePlugin service. It has not been implemented in kubevrit-gpu-device-plugin
func (dpi *GenericVGpuDevicePlugin) GetPreferredAllocation(ctx context.Context, request *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	// TODO
	// returns a preferred set of devices to allocate
	// from a list of available ones. The resulting preferred allocation is not
	// guaranteed to be the allocation ultimately performed by the
	// devicemanager. It is only designed to help the devicemanager make a more
	// informed allocation decision when possible.
	return nil, nil
}

//Health check of vGPU devices
func (dpi *GenericVGpuDevicePlugin) healthCheck() error {
	log.Printf("In health check")
	log.Println("Loading NVML")
	if err := nvmlInit(); err != nil {
		log.Printf("Failed to initialize NVML: %s.", err)
		return err
	}

	defer func() { log.Println("Shutdown of NVML returned:", nvmlShutdown()) }()
	devs := getDevices()

	xids := make(chan *nvml.Device)
	go watchXIDs(devs, xids)

	for {
		select {
		case <-dpi.stop:
			return nil
		case dev := <-xids:
			returnedMap := returnGpuVgpuMap()
			vGpuIDList := returnedMap[dev.PCI.BusID]
			for _, id := range vGpuIDList {
				dpi.unhealthy <- id
			}
		}
	}

	return nil
}

func getDevices() []*nvml.Device {
	n, err := nvmlGetDeviceCount()
	if err != nil {
		log.Panicln("Fatal:", err)
	}
	var devs []*nvml.Device
	for i := uint(0); i < n; i++ {
		d, err := nvmlNewDeviceLite(i)
		if err != nil {
			log.Panicln("Fatal:", err)
		}
		log.Println("devices UUID :", d.UUID)
		log.Println("Device ID :", d.PCI.BusID)
		devs = append(devs, d)
	}
	return devs
}

func watchXIDsFunc(devs []*nvml.Device, xids chan<- *nvml.Device) {
	eventSet := nvml.NewEventSet()
	defer nvml.DeleteEventSet(eventSet)

	for _, d := range devs {
		err := nvml.RegisterEventForDevice(eventSet, nvml.XidCriticalError, d.UUID)
		if err != nil && strings.HasSuffix(err.Error(), "Not Supported") {
			log.Printf("Warning: %s is too old to support healthchecking: %s. Marking it unhealthy.", d.UUID, err)

			xids <- d
			continue
		}

		if err != nil {
			log.Panicln("Fatal:", err)
		}
	}

	for {
		select {
		default:
		}

		e, err := nvml.WaitForEvent(eventSet, 5000)
		if err != nil && e.Etype != nvml.XidCriticalError {
			continue
		}

		// FIXME: formalize the full list and document it.
		// http://docs.nvidia.com/deploy/xid-errors/index.html#topic_4
		// Application errors: the GPU should still be healthy
		if e.Edata == 31 || e.Edata == 43 || e.Edata == 45 {
			continue
		}

		if e.UUID == nil || len(*e.UUID) == 0 {
			// All devices are unhealthy
			for _, d := range devs {
				xids <- d
			}
			continue
		}

		for _, d := range devs {
			if d.UUID == *e.UUID {
				xids <- d
			}
		}
	}
}
