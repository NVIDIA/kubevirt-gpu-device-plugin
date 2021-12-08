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
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1beta1"
)

const (
	DeviceNamespace   = "nvidia.com"
	connectionTimeout = 5 * time.Second
	vfioDevicePath    = "/dev/vfio"
	gpuType           = "VFIO-PCI"
	gpuPrefix         = "PCI_RESOURCE_NVIDIA_COM"
	vgpuPrefix        = "MDEV_PCI_RESOURCE_NVIDIA_COM"
)

var returnIommuMap = getIommuMap

//Implements the kubernetes device plugin API
type GenericDevicePlugin struct {
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

//Returns an initialized instance of GenericDevicePlugin
func NewGenericDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	log.Println("Devicename " + deviceName)
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)
	dpi := &GenericDevicePlugin{
		devs:       devices,
		socketPath: serverSock,
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
	}
	return dpi
}

func buildEnv(envList map[string][]string) map[string]string {
	env := map[string]string{}
	for key, devList := range envList {
		env[key] = strings.Join(devList, ",")
	}
	return env
}

func waitForGrpcServer(socketPath string, timeout time.Duration) error {
	conn, err := connect(socketPath, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// dial establishes the gRPC communication with the registered device plugin.
func connect(socketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	c, err := grpc.Dial(socketPath,
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithTimeout(timeout),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, err
	}

	return c, nil
}

// Start starts the gRPC server of the device plugin
func (dpi *GenericDevicePlugin) Start(stop chan struct{}) error {
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

	err = dpi.Register()
	if err != nil {
		log.Printf("[%s] Error registering with device plugin manager: %v", dpi.deviceName, err)
		return err
	}

	go dpi.server.Serve(sock)

	err = waitForGrpcServer(dpi.socketPath, connectionTimeout)
	if err != nil {
		// this err is returned at the end of the Start function
		log.Printf("[%s] Error connecting to GRPC server: %v", dpi.deviceName, err)
	}

	go dpi.healthCheck()

	log.Println(dpi.deviceName + " Device plugin server ready")

	return err
}

// Stop stops the gRPC server
func (dpi *GenericDevicePlugin) Stop() error {
	if dpi.server == nil {
		return nil
	}

	dpi.server.Stop()
	dpi.server = nil

	return dpi.cleanup()
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (dpi *GenericDevicePlugin) Register() error {
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
func (dpi *GenericDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

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
func (dpi *GenericDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Println("In allocate")
	responses := pluginapi.AllocateResponse{}
	envList := map[string][]string{}

	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		for _, iommuId := range req.DevicesIDs {
			devAddrs := []string{}

			returnedMap := returnIommuMap()
			//Retrieve the devices associated with a Iommu group
			nvDev := returnedMap[iommuId]
			for _, dev := range nvDev {
				iommuGroup, err := readLink(basePath, dev.addr, "iommu_group")
				if err != nil || iommuGroup != iommuId {
					log.Println("IommuGroup has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}
				vendorID, err := readIDFromFile(basePath, dev.addr, "vendor")
				if err != nil || vendorID != "10de" {
					log.Println("Vendor has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}

				devAddrs = append(devAddrs, dev.addr)

			}
			deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
				HostPath:      filepath.Join(vfioDevicePath, "vfio"),
				ContainerPath: filepath.Join(vfioDevicePath, "vfio"),
				Permissions:   "mrw",
			})
			deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
				HostPath:      filepath.Join(vfioDevicePath, iommuId),
				ContainerPath: filepath.Join(vfioDevicePath, iommuId),
				Permissions:   "mrw",
			})

			key := fmt.Sprintf("%s_%s", gpuPrefix, dpi.deviceName)
			if _, exists := envList[key]; !exists {
				envList[key] = []string{}
			}
			envList[key] = append(envList[key], devAddrs...)
		}
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

func (dpi *GenericDevicePlugin) cleanup() error {
	if err := os.Remove(dpi.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (dpi *GenericDevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}
	return options, nil
}

func (dpi *GenericDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

//Health check of GPU devices
func (dpi *GenericDevicePlugin) healthCheck() error {
	var pathDeviceMap = make(map[string]string)
	log.Printf("In health check")
	var path = dpi.devicePath
	var health = ""
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Unable to create fsnotify watcher: %v", err)
		return nil
	}
	defer watcher.Close()

	_, err = os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Unable to stat device: %v", err)
			return err
		}
	}

	for _, dev := range dpi.devs {
		log.Printf("Path %s", path)
		log.Printf("Dev %s", dev.ID)
		err = watcher.Add(filepath.Join(path, dev.ID))
		pathDeviceMap[filepath.Join(path, dev.ID)] = dev.ID
		if err != nil {
			log.Printf("Unable to add path to fsnotify watcher: %v", err)
		}
	}

	for {
		select {
		case <-dpi.stop:
			return nil
		case event := <-watcher.Events:
			log.Printf("health Event Op: %v", event.Op)
			log.Printf("health Event Name: %s", event.Name)
			v, ok := pathDeviceMap[event.Name]
			if ok {
				// Health in this case is if the device path actually exists
				if event.Op == fsnotify.Create {
					health = v
					dpi.healthy <- health
				} else if (event.Op == fsnotify.Remove) || (event.Op == fsnotify.Rename) {
					health = v
					dpi.unhealthy <- health
				}
			}
		}
	}
}
