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
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"kubevirt-gpu-device-plugin/pkg/fabricmanager"
)

const (
	DeviceNamespace   = "nvidia.com"
	connectionTimeout = 5 * time.Second
	vfioDevicePath    = "/dev/vfio"
	iommuDevicePath   = "/dev/iommu"
	gpuPrefix         = "PCI_RESOURCE_NVIDIA_COM"
	vgpuPrefix        = "MDEV_PCI_RESOURCE_NVIDIA_COM"
)

var (
	returnIommuMap      = getIommuMap
	returnBdfToIommuMap = getBdfToIommuMap

	pciModuleMappingPath = "/run/nvidia-fabricmanager/gpu-pci-module-mapping.json"
)

// isFabricManagerEnabled returns true if fabric manager integration is enabled
// via the ENABLE_FABRIC_MANAGER environment variable.
func isFabricManagerEnabled() bool {
	envVar := os.Getenv("ENABLE_FABRIC_MANAGER")
	enabled, _ := strconv.ParseBool(envVar)
	return enabled
}

// Implements the kubernetes device plugin API
type GenericDevicePlugin struct {
	devs             []*pluginapi.Device
	server           *grpc.Server
	socketPath       string
	stop             chan struct{} // this channel signals to stop the DP
	term             chan bool     // this channel detects kubelet restarts
	healthy          chan string
	unhealthy        chan string
	devicePath       string
	deviceName       string
	devsHealth       []*pluginapi.Device
	fmEnabled        bool
	partitionManager *fabricmanager.PartitionManager
}

// Returns an initialized instance of GenericDevicePlugin
func NewGenericDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	log.Println("Devicename " + deviceName)
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)

	fmEnabled := isFabricManagerEnabled()
	log.Printf("[%s] Fabric manager integration enabled: %t", deviceName, fmEnabled)

	dpi := &GenericDevicePlugin{
		devs:       devices,
		socketPath: serverSock,
		term:       make(chan bool, 1),
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
		fmEnabled:  fmEnabled,
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
	ctx, _ := context.WithTimeout(context.Background(), timeout)
	c, err := grpc.DialContext(ctx, socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			if deadline, ok := ctx.Deadline(); ok {
				return net.DialTimeout("unix", addr, time.Until(deadline))
			}
			return net.DialTimeout("unix", addr, connectionTimeout)
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

	// Attempt fabric manager connection if enabled
	if dpi.fmEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		fmConfig := &fabricmanager.Config{
			AddressInfo: "/run/nvidia-fabricmanager/fmpm.sock",
			AddressType: fabricmanager.AddressTypeUnix,
			TimeoutMs:   5000,
			MaxRetries:  3,
			RetryDelay:  time.Second * 2,
			Debug:       false,
		}
		fmClient := fabricmanager.NewClient(fmConfig)

		if err := fmClient.Connect(ctx); err != nil {
			log.Printf("WARNING: Fabric manager enabled but connection failed: %v", err)
			log.Print("Falling back to legacy device plugin mode")
		} else {
			log.Print("Fabric manager connected successfully")

			// Load PCI-to-module mapping
			pciToModule, moduleToPCI, mapErr := fabricmanager.LoadPCIModuleMapping(pciModuleMappingPath)
			if mapErr != nil {
				log.Printf("WARNING: Failed to load PCI module mapping: %v", mapErr)
				log.Print("Falling back to legacy device plugin mode")
			} else {
				log.Printf("Loaded PCI-to-module mapping with %d entries", len(pciToModule))

				// Build device NUMA map from device list
				deviceNUMAMap := make(map[string]int64, len(dpi.devs))
				for _, dev := range dpi.devs {
					if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
						deviceNUMAMap[dev.ID] = dev.Topology.Nodes[0].ID
					}
				}

				dpi.partitionManager = fabricmanager.NewPartitionManager(
					fmClient, pciToModule, moduleToPCI, deviceNUMAMap,
				)

				// List and log partition information
				partitions, err := dpi.partitionManager.GetPartitions(ctx)
				if err != nil {
					log.Printf("WARNING: Failed to retrieve fabric partitions: %v", err)
				} else {
					log.Printf("INFO: Discovered %d fabric partitions (max: %d):", partitions.NumPartitions, partitions.MaxNumPartitions)

					for _, partition := range partitions.Partitions {
						status := "inactive"
						if partition.IsActive {
							status = "active"
						}
						log.Printf("INFO: Partition ID %d: %s, GPUs: %d", partition.PartitionID, status, partition.NumGPUs)

						for _, gpu := range partition.GPUs {
							log.Printf("INFO:   GPU %d: NVLinks=%d/%d", gpu.PhysicalID, gpu.NumNVLinksAvailable, gpu.MaxNumNVLinks)
						}
					}
				}
			}
		}
	}

	sock, err := net.Listen("unix", dpi.socketPath)
	if err != nil {
		log.Printf("Error creating GRPC server socket: %v", err)
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
func (dpi *GenericDevicePlugin) Stop() error {
	if dpi.server == nil {
		return nil
	}

	// Send terminate signal to ListAndWatch()
	dpi.term <- true

	dpi.server.Stop()
	dpi.server = nil

	// Disconnect from fabric manager if connected
	if dpi.partitionManager != nil {
		if err := dpi.partitionManager.Disconnect(); err != nil {
			log.Printf("[%s] WARNING: Error disconnecting from fabric manager: %v",
				dpi.deviceName, err)
		} else {
			log.Printf("[%s] Fabric manager disconnected successfully", dpi.deviceName)
		}
		dpi.partitionManager = nil
	}

	return dpi.cleanup()
}

// Restarts DP server
func (dpi *GenericDevicePlugin) restart() error {
	log.Printf("Restarting %s device plugin server", dpi.deviceName)
	if dpi.server == nil {
		return fmt.Errorf("grpc server instance not found for %s", dpi.deviceName)
	}

	dpi.Stop()

	// Create new instance of a grpc server
	var stop = make(chan struct{})
	return dpi.Start(stop)
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
	log.Printf("[%s] Successfully registered with kubelet for resource: %s/%s (endpoint: %s)",
		dpi.deviceName, DeviceNamespace, dpi.deviceName, path.Base(dpi.socketPath))
	return nil
}

// ListAndWatch lists devices and update that list according to the health status
func (dpi *GenericDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	log.Printf("[%s] ListAndWatch called, sending %d devices:", dpi.deviceName, len(dpi.devs))
	for _, dev := range dpi.devs {
		numaNodes := "nil"
		if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
			numaNodes = fmt.Sprintf("%d", dev.Topology.Nodes[0].ID)
		}
		log.Printf("  Device ID=%s, Health=%s, NUMA=%s", dev.ID, dev.Health, numaNodes)
	}

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
		case <-dpi.term:
			return nil
		}
	}
}

// Performs pre allocation checks and allocates a devices based on the request
func (dpi *GenericDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Printf("[%s] ========== ALLOCATE CALLED ==========", dpi.deviceName)
	log.Printf("[%s] Allocate() called with %d container request(s)", dpi.deviceName, len(reqs.ContainerRequests))
	for i, req := range reqs.ContainerRequests {
		log.Printf("[%s] Container request %d: DeviceIDs=%v", dpi.deviceName, i, req.DevicesIDs)
	}
	log.Printf("[%s] This means kubelet passed Topology Manager admission!", dpi.deviceName)

	responses := pluginapi.AllocateResponse{}
	envList := map[string][]string{}
	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		return nil, fmt.Errorf("could not determine iommufd support: %w", err)
	}
	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		returnedMap := returnIommuMap()
		bdfToIommu := returnBdfToIommuMap()
		for _, bdf := range req.DevicesIDs {
			iommuId, ok := bdfToIommu[bdf]
			if !ok {
				return nil, fmt.Errorf("invalid allocation request: unknown device: %s", bdf)
			}
			devAddrs := []string{}
			nvDev := returnedMap[iommuId]
			if len(nvDev) == 0 {
				return nil, fmt.Errorf("invalid allocation request: unknown device: %s", bdf)
			}
			requestedDeviceFound := false
			for _, dev := range nvDev {
				iommuGroup, err := readLink(basePath, dev.addr, "iommu_group")
				if err != nil || iommuGroup != iommuId {
					log.Println("IommuGroup has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}
				vendorID, err := readIDFromFile(basePath, dev.addr, "vendor")
				if err != nil || vendorID != nvidiaVendorID {
					log.Println("Vendor has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}

				devAddrs = append(devAddrs, dev.addr)
				if dev.addr == bdf {
					requestedDeviceFound = true
				}
				if iommufdSupported {
					vfiodev, err := readVFIODev(basePath, dev.addr)
					if err != nil {
						return nil, fmt.Errorf("could not determine iommufd device for device %s: %v", dev.addr, err)
					}
					deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
						HostPath:      filepath.Join(vfioDevicePath, "devices", vfiodev),
						ContainerPath: filepath.Join(vfioDevicePath, "devices", vfiodev),
						Permissions:   "mrw",
					})
				}
			}
			if !requestedDeviceFound {
				return nil, fmt.Errorf("invalid allocation request: unknown device: %s", bdf)
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
			if iommufdSupported {
				deviceSpecs = append(deviceSpecs, &pluginapi.DeviceSpec{
					HostPath:      iommuDevicePath,
					ContainerPath: iommuDevicePath,
					Permissions:   "mrw",
				})
			}

			key := fmt.Sprintf("%s_%s", gpuPrefix, strings.ToUpper(dpi.deviceName))
			if _, exists := envList[key]; !exists {
				envList[key] = []string{}
			}
			envList[key] = append(envList[key], devAddrs...)
		}
		envs := buildEnv(envList)
		log.Printf("[%s] Allocated devices - Envs: %v, DeviceSpecs count: %d", dpi.deviceName, envs, len(deviceSpecs))
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
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: true,
	}
	return options, nil
}

func (dpi *GenericDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

// GetPreferredAllocation returns a preferred set of devices to allocate
// from a list of available ones. This helps the Topology Manager make
// topology-aware allocation decisions based on NUMA affinity.
// When fabric manager is active, it prefers devices that form a complete
// FM partition of the exact requested size with the best NUMA locality.
func (dpi *GenericDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	log.Printf("[%s] GetPreferredAllocation called with %d container request(s)", dpi.deviceName, len(in.ContainerRequests))

	response := &pluginapi.PreferredAllocationResponse{}

	// Build device-to-NUMA map for logging
	deviceToNUMA := make(map[string]int64)
	for _, dev := range dpi.devs {
		if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
			deviceToNUMA[dev.ID] = dev.Topology.Nodes[0].ID
		}
	}

	for idx, req := range in.ContainerRequests {
		log.Printf("[%s] Container request %d: Available devices=%v, MustInclude=%v, AllocationSize=%d",
			dpi.deviceName, idx, req.AvailableDeviceIDs, req.MustIncludeDeviceIDs, req.AllocationSize)

		var preferredDevices []string
		var err error

		if dpi.partitionManager != nil {
			preferredDevices, err = dpi.partitionManager.SelectPreferred(ctx, &fabricmanager.SelectPreferredRequest{
				AvailableDeviceIDs:   req.AvailableDeviceIDs,
				MustIncludeDeviceIDs: req.MustIncludeDeviceIDs,
				AllocationSize:       int(req.AllocationSize),
			})
		} else {
			preferredDevices, err = dpi.preferDevicesByNUMA(req)
		}
		if err != nil {
			return nil, err
		}

		log.Printf("[%s] Preferred allocation for container %d: %v (NUMA nodes: %v)",
			dpi.deviceName, idx, preferredDevices, func() []int64 {
				var nodes []int64
				for _, devID := range preferredDevices {
					if node, ok := deviceToNUMA[devID]; ok {
						nodes = append(nodes, node)
					}
				}
				return nodes
			}())

		response.ContainerResponses = append(response.ContainerResponses,
			&pluginapi.ContainerPreferredAllocationResponse{
				DeviceIDs: preferredDevices,
			})
	}

	return response, nil
}

// preferDevicesByNUMA selects preferred devices based on NUMA node locality.
// It prefers devices from the same NUMA node and falls back to kubelet-provided order.
func (dpi *GenericDevicePlugin) preferDevicesByNUMA(
	req *pluginapi.ContainerPreferredAllocationRequest,
) ([]string, error) {
	// Build a map of device ID to NUMA node from our device list
	deviceToNUMA := make(map[string]int64)
	for _, dev := range dpi.devs {
		if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
			deviceToNUMA[dev.ID] = dev.Topology.Nodes[0].ID
		}
	}
	getNUMANode := func(deviceID string) int64 {
		if node, ok := deviceToNUMA[deviceID]; ok {
			return node
		}
		return -1
	}

	// Group available devices by NUMA node while preserving iteration order
	numaToDevices := make(map[int64][]string)
	var nodeOrder []int64
	nodeSeen := make(map[int64]struct{})
	for _, deviceID := range req.AvailableDeviceIDs {
		numaNode := getNUMANode(deviceID)
		numaToDevices[numaNode] = append(numaToDevices[numaNode], deviceID)
		if _, ok := nodeSeen[numaNode]; !ok {
			nodeOrder = append(nodeOrder, numaNode)
			nodeSeen[numaNode] = struct{}{}
		}
	}

	// Prefer devices from the same NUMA node
	var preferredDevices []string
	preferredSet := make(map[string]struct{})
	selectedPerNode := make(map[int64]int)
	addDevice := func(deviceID string) {
		if _, exists := preferredSet[deviceID]; exists {
			return
		}
		preferredSet[deviceID] = struct{}{}
		numaNode := getNUMANode(deviceID)
		selectedPerNode[numaNode]++
		preferredDevices = append(preferredDevices, deviceID)
	}

	// Always place must-include devices first
	selectedNodeOrder := []int64{}
	selectedNodeSeen := make(map[int64]struct{})
	for _, deviceID := range req.MustIncludeDeviceIDs {
		if _, exists := preferredSet[deviceID]; exists {
			continue
		}
		addDevice(deviceID)
		numaNode := getNUMANode(deviceID)
		if _, ok := selectedNodeSeen[numaNode]; !ok {
			selectedNodeOrder = append(selectedNodeOrder, numaNode)
			selectedNodeSeen[numaNode] = struct{}{}
		}
	}

	if len(preferredDevices) > int(req.AllocationSize) {
		return nil, fmt.Errorf("number of MustIncludeDeviceIDs (%d) exceeds allocation size (%d)",
			len(preferredDevices), req.AllocationSize)
	}

	// First, try to satisfy the request from a single NUMA node (including already selected devices)
	if len(preferredDevices) < int(req.AllocationSize) {
		targetNode := int64(-1)
		var candidateNodes []int64
		candidateNodes = append(candidateNodes, selectedNodeOrder...)
		for _, node := range nodeOrder {
			if _, seen := selectedNodeSeen[node]; seen {
				continue
			}
			candidateNodes = append(candidateNodes, node)
		}

		for _, numaNode := range candidateNodes {
			availableOnNode := 0
			for _, deviceID := range numaToDevices[numaNode] {
				if _, exists := preferredSet[deviceID]; !exists {
					availableOnNode++
				}
			}
			totalOnNode := selectedPerNode[numaNode] + availableOnNode
			if totalOnNode >= int(req.AllocationSize) {
				log.Printf("[%s] Selecting NUMA node %d (have %d selected, %d available) to satisfy %d devices",
					dpi.deviceName, numaNode, selectedPerNode[numaNode], availableOnNode, req.AllocationSize)
				targetNode = numaNode
				break
			}
		}

		if targetNode != -1 {
			for _, deviceID := range numaToDevices[targetNode] {
				if len(preferredDevices) >= int(req.AllocationSize) {
					break
				}
				addDevice(deviceID)
			}
		}
	}

	// If we couldn't fill the request from a single NUMA node, fall back to the kubelet-provided order
	if len(preferredDevices) < int(req.AllocationSize) {
		log.Printf("[%s] Using kubelet-provided device order to satisfy remaining slots (need %d more)",
			dpi.deviceName, int(req.AllocationSize)-len(preferredDevices))
		for _, deviceID := range req.AvailableDeviceIDs {
			if len(preferredDevices) >= int(req.AllocationSize) {
				break
			}
			addDevice(deviceID)
		}
	}

	return preferredDevices, nil
}

// Health check of GPU devices
func (dpi *GenericDevicePlugin) healthCheck() error {
	method := fmt.Sprintf("healthCheck(%s)", dpi.deviceName)
	log.Printf("%s: invoked", method)
	var pathDeviceMap = make(map[string][]string)
	var path = dpi.devicePath
	var watchedPaths = make(map[string]struct{})

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("%s: Unable to create fsnotify watcher: %v", method, err)
		return err
	}
	defer watcher.Close()

	err = watcher.Add(filepath.Dir(dpi.socketPath))
	if err != nil {
		log.Printf("%s: Unable to add device plugin socket path to fsnotify watcher: %v", method, err)
		return err
	}

	_, err = os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("%s: Unable to stat device: %v", method, err)
			return err
		}
	}

	bdfToIommu := returnBdfToIommuMap()
	for _, dev := range dpi.devs {
		iommuID, ok := bdfToIommu[dev.ID]
		if !ok {
			log.Printf("%s: Unable to determine IOMMU group for device %s", method, dev.ID)
			continue
		}
		devicePath := filepath.Join(path, iommuID)
		pathDeviceMap[devicePath] = append(pathDeviceMap[devicePath], dev.ID)
		if _, already := watchedPaths[devicePath]; already {
			continue
		}
		err = watcher.Add(devicePath)
		if err != nil {
			log.Printf("%s: Unable to add device path to fsnotify watcher: %v", method, err)
			return err
		}
		log.Printf(" Adding Watcher to Path : %v", devicePath)
		watchedPaths[devicePath] = struct{}{}
	}

	for {
		select {
		case <-dpi.stop:
			return nil
		case event := <-watcher.Events:
			if deviceIDs, ok := pathDeviceMap[event.Name]; ok {
				// Health in this case is if the device path actually exists
				if event.Op == fsnotify.Create {
					for _, id := range deviceIDs {
						dpi.healthy <- id
					}
				} else if (event.Op == fsnotify.Remove) || (event.Op == fsnotify.Rename) {
					log.Printf("%s: Marking device unhealthy: %s", method, event.Name)
					for _, id := range deviceIDs {
						dpi.unhealthy <- id
					}
				}
			} else if event.Name == dpi.socketPath && event.Op == fsnotify.Remove {
				// Watcher event for removal of socket file
				log.Printf("%s: Socket path for GPU device was removed, kubelet likely restarted", method)
				// Trigger restart of the DP servers
				if err := dpi.restart(); err != nil {
					log.Printf("%s: Unable to restart server %v", method, err)
					return err
				}
				log.Printf("%s: Successfully restarted %s device plugin server. Terminating.", method, dpi.deviceName)
				return nil
			}
		}
	}
}

func supportsIOMMUFD() (bool, error) {
	_, err := os.Stat(filepath.Join(rootPath, iommuDevicePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
func readVFIODev(basePath string, deviceAddress string) (string, error) {
	content, err := os.ReadDir(filepath.Join(basePath, deviceAddress, "vfio-dev"))
	if err != nil {
		return "", err
	}
	for _, c := range content {
		if !c.IsDir() {
			continue
		}
		if strings.HasPrefix(c.Name(), "vfio") {
			return c.Name(), nil
		}
	}
	return "", fmt.Errorf("no iommufd device found")
}
