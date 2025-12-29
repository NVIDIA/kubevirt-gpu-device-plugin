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
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	fm "kubevirt-gpu-device-plugin/pkg/fabric_manager"
)

const (
	DeviceNamespace   = "nvidia.com"
	connectionTimeout = 5 * time.Second
	vfioDevicePath    = "/dev/vfio"
	iommuDevicePath   = "/dev/iommu"
	gpuPrefix         = "PCI_RESOURCE_NVIDIA_COM"
	vgpuPrefix        = "MDEV_PCI_RESOURCE_NVIDIA_COM"
)

var returnIommuMap = getIommuMap
var returnBdfToIommuMap = getBdfToIommuMap

// Implements the kubernetes device plugin API
type GenericDevicePlugin struct {
	devs         []*pluginapi.Device
	server       *grpc.Server
	socketPath   string
	stop         chan struct{} // this channel signals to stop the DP
	term         chan bool     // this channel detects kubelet restarts
	healthy      chan string
	unhealthy    chan string
	devicePath   string
	deviceName   string
	devsHealth   []*pluginapi.Device
	partitionMgr *fm.PartitionManager // Fabric Manager partition manager
}

// Returns an initialized instance of GenericDevicePlugin
func NewGenericDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	log.Println("Devicename " + deviceName)
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)
	dpi := &GenericDevicePlugin{
		devs:       devices,
		socketPath: serverSock,
		term:       make(chan bool, 1),
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
	}
	return dpi
}

// SetPartitionManager sets the Fabric Manager partition manager for NVLink support.
// If set, the plugin will use partition-aware GPU allocation.
func (dpi *GenericDevicePlugin) SetPartitionManager(mgr *fm.PartitionManager) {
	dpi.partitionMgr = mgr
	log.Printf("[%s] Fabric Manager partition support enabled", dpi.deviceName)
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
func (dpi *GenericDevicePlugin) Stop() error {
	if dpi.server == nil {
		return nil
	}

	// Send terminate signal to ListAndWatch()
	dpi.term <- true

	dpi.server.Stop()
	dpi.server = nil

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

	// ========== FABRIC MANAGER PARTITION ACTIVATION (NVLink support) ==========
	// Activate FM partition for allocated GPUs to enable NVLink
	if dpi.partitionMgr != nil && len(reqs.ContainerRequests) > 0 {
		// Collect all allocated GPU BDFs
		var allocatedGPUs []string
		for _, req := range reqs.ContainerRequests {
			allocatedGPUs = append(allocatedGPUs, req.DevicesIDs...)
		}

		if len(allocatedGPUs) > 0 {
			log.Printf("[%s] Activating FM partition for GPUs: %v", dpi.deviceName, allocatedGPUs)

			// Generate a unique pod UID for tracking
			// Note: The Device Plugin API does not provide real pod UID in Allocate request.
			// This placeholder UID is used for reconciler to track GPU assignments.
			podUID := fmt.Sprintf("pod-%d", time.Now().UnixNano())

			err := dpi.partitionMgr.ActivatePartitionForGPUs(allocatedGPUs, podUID)
			if err != nil {
				log.Printf("[%s] WARNING: Failed to activate FM partition: %v", dpi.deviceName, err)
				log.Printf("[%s] Allocation will proceed but NVLink may not be active", dpi.deviceName)
				// Don't fail allocation - let VM start, NVLink might work with manual activation
				// In production, you may want to return error here:
				// return nil, fmt.Errorf("failed to activate FM partition: %w", err)
			} else {
				log.Printf("[%s] FM partition activated successfully for NVLink", dpi.deviceName)
				// Track allocation for reconciler to detect pod deletion
				dpi.partitionMgr.TrackAllocation(podUID, allocatedGPUs)
			}
		}
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
//
// PARTITION-FIRST SELECTION:
// When Fabric Manager is enabled, ALL allocated GPUs must come from a SINGLE partition.
// Partitions are ranked by NUMA locality (fewer NUMA nodes = better).
// If no valid partition is available, allocation will fail.
func (dpi *GenericDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	log.Printf("[%s] GetPreferredAllocation called with %d container request(s)", dpi.deviceName, len(in.ContainerRequests))

	// IMPORTANT: Reconcile BEFORE allocation to clean up orphaned partitions
	// This fixes race condition where old pod's partition is still marked active
	if dpi.partitionMgr != nil {
		dpi.partitionMgr.ReconcileNow()
	}

	response := &pluginapi.PreferredAllocationResponse{}

	for idx, req := range in.ContainerRequests {
		log.Printf("[%s] Container request %d: Available devices=%v, MustInclude=%v, AllocationSize=%d",
			dpi.deviceName, idx, req.AvailableDeviceIDs, req.MustIncludeDeviceIDs, req.AllocationSize)

		// Build a map of device ID to NUMA node from our device list
		deviceToNUMA := make(map[string]int64)
		for _, dev := range dpi.devs {
			if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
				deviceToNUMA[dev.ID] = dev.Topology.Nodes[0].ID
			}
		}

		// Build set of available devices for quick lookup
		availableSet := make(map[string]bool)
		for _, d := range req.AvailableDeviceIDs {
			availableSet[d] = true
		}

		var preferredDevices []string

		// ========== PARTITION-FIRST SELECTION (NVLink support) ==========
		if dpi.partitionMgr != nil {
			log.Printf("[%s] Using partition-first selection for NVLink support", dpi.deviceName)
			tree := dpi.partitionMgr.GetTree()

			// Get all available partitions of the requested size
			availablePartitions := tree.GetAvailablePartitions(int(req.AllocationSize))
			log.Printf("[%s] Found %d partition(s) of size %d", dpi.deviceName, len(availablePartitions), req.AllocationSize)

			// Filter to partitions where ALL GPUs are available
			type scoredPartition struct {
				partition *fm.Partition
				gpus      []string
				numaNodes map[int64]bool
				numaCount int
			}
			var candidates []scoredPartition

			for _, p := range availablePartitions {
				gpus, err := tree.GetGPUsForPartition(p.ID)
				if err != nil {
					log.Printf("[%s] Error getting GPUs for partition %d: %v", dpi.deviceName, p.ID, err)
					continue
				}

				// Check if all partition GPUs are available
				allAvailable := true
				for _, gpu := range gpus {
					if !availableSet[gpu] {
						allAvailable = false
						break
					}
				}
				if !allAvailable {
					log.Printf("[%s] Partition %d skipped: not all GPUs available", dpi.deviceName, p.ID)
					continue
				}

				// Check MustInclude constraint: all MustInclude devices must be in this partition
				if len(req.MustIncludeDeviceIDs) > 0 {
					gpuSet := make(map[string]bool)
					for _, g := range gpus {
						gpuSet[g] = true
					}
					allIncluded := true
					for _, mustInclude := range req.MustIncludeDeviceIDs {
						if !gpuSet[mustInclude] {
							allIncluded = false
							break
						}
					}
					if !allIncluded {
						log.Printf("[%s] Partition %d skipped: MustInclude devices not in partition", dpi.deviceName, p.ID)
						continue
					}
				}

				// Calculate NUMA locality score (fewer unique NUMA nodes = better)
				numaNodes := make(map[int64]bool)
				for _, gpu := range gpus {
					if node, ok := deviceToNUMA[gpu]; ok {
						numaNodes[node] = true
					}
				}

				candidates = append(candidates, scoredPartition{
					partition: p,
					gpus:      gpus,
					numaNodes: numaNodes,
					numaCount: len(numaNodes),
				})
				log.Printf("[%s] Partition %d is a candidate (GPUs: %v, NUMA nodes: %d)",
					dpi.deviceName, p.ID, gpus, len(numaNodes))
			}

			if len(candidates) == 0 {
				// FM is enabled but no valid partition found - this is a HARD ERROR
				return nil, fmt.Errorf("[%s] no available partition of size %d with all GPUs available - cannot proceed without valid NVLink partition",
					dpi.deviceName, req.AllocationSize)
			}

			// Sort by NUMA count (ascending), then by partition ID for determinism
			sort.Slice(candidates, func(i, j int) bool {
				if candidates[i].numaCount != candidates[j].numaCount {
					return candidates[i].numaCount < candidates[j].numaCount
				}
				return candidates[i].partition.ID < candidates[j].partition.ID
			})

			best := candidates[0]
			log.Printf("[%s] Selected partition %d with %d NUMA node(s): %v",
				dpi.deviceName, best.partition.ID, best.numaCount, best.gpus)
			preferredDevices = best.gpus[:int(req.AllocationSize)]
		}

		// ========== FALLBACK: NUMA-ONLY SELECTION (only when FM is disabled) ==========
		if len(preferredDevices) == 0 && dpi.partitionMgr == nil {
			log.Printf("[%s] Using NUMA-based selection (FM disabled)", dpi.deviceName)

			// Group available devices by NUMA node
			numaToDevices := make(map[int64][]string)
			var nodeOrder []int64
			nodeSeen := make(map[int64]struct{})
			for _, deviceID := range req.AvailableDeviceIDs {
				node := int64(-1)
				if n, ok := deviceToNUMA[deviceID]; ok {
					node = n
				}
				numaToDevices[node] = append(numaToDevices[node], deviceID)
				if _, ok := nodeSeen[node]; !ok {
					nodeOrder = append(nodeOrder, node)
					nodeSeen[node] = struct{}{}
				}
			}

			preferredSet := make(map[string]struct{})
			addDevice := func(deviceID string) {
				if _, exists := preferredSet[deviceID]; exists {
					return
				}
				preferredSet[deviceID] = struct{}{}
				preferredDevices = append(preferredDevices, deviceID)
			}

			// Add MustInclude devices first
			for _, deviceID := range req.MustIncludeDeviceIDs {
				addDevice(deviceID)
			}

			// Try to fill from single NUMA node
			for _, node := range nodeOrder {
				if len(numaToDevices[node]) >= int(req.AllocationSize) {
					for _, deviceID := range numaToDevices[node] {
						if len(preferredDevices) >= int(req.AllocationSize) {
							break
						}
						addDevice(deviceID)
					}
					break
				}
			}

			// If still not enough, take from any available
			if len(preferredDevices) < int(req.AllocationSize) {
				for _, deviceID := range req.AvailableDeviceIDs {
					if len(preferredDevices) >= int(req.AllocationSize) {
						break
					}
					addDevice(deviceID)
				}
			}
		}

		// Log final selection with NUMA info
		var numaNodes []int64
		for _, devID := range preferredDevices {
			if node, ok := deviceToNUMA[devID]; ok {
				numaNodes = append(numaNodes, node)
			}
		}
		log.Printf("[%s] Preferred allocation for container %d: %v (NUMA nodes: %v)",
			dpi.deviceName, idx, preferredDevices, numaNodes)

		response.ContainerResponses = append(response.ContainerResponses,
			&pluginapi.ContainerPreferredAllocationResponse{
				DeviceIDs: preferredDevices,
			})
	}

	return response, nil
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
