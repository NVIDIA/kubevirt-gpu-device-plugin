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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	DeviceNamespace   = "nvidia.com"
	connectionTimeout = 5 * time.Second
	vfioDevicePath    = "/dev/vfio"
	iommuDevicePath   = "/dev/iommu"
	egmClassPath      = "/sys/class/egm"
	deviceDir         = "/dev"
	gpuPrefix         = "PCI_RESOURCE_NVIDIA_COM"
	vgpuPrefix        = "MDEV_PCI_RESOURCE_NVIDIA_COM"
)

type EGMDeviceInfo struct {
	DevPath string
	GPUBDFs []string
}

var returnIommuMap = getIommuMap
var returnBdfToIommuMap = getBdfToIommuMap
var discoverEGMDevices = discoverEGMDevicesFunc

// Implements the kubernetes device plugin API
type GenericDevicePlugin struct {
	pluginapi.UnimplementedDevicePluginServer
	devs        []*pluginapi.Device
	devsMu      sync.RWMutex  // guards devs and each device's Health field
	refresh     chan struct{} // buffered(1): asks ListAndWatch to re-advertise the current device list
	rewatch     chan struct{} // buffered(1): asks healthCheck to add watches for newly added devices
	lifecycleMu sync.Mutex    // serializes Start/Stop so concurrent callers cannot race the server field or double-close done
	server      *grpc.Server
	socketPath  string
	stop        chan struct{} // this channel signals to stop the DP (whole-process shutdown, shared)
	done        chan struct{} // closed by Stop() to terminate this one plugin's goroutines
	term        chan bool     // this channel detects kubelet restarts
	healthy     chan string
	unhealthy   chan string
	devicePath  string
	deviceName  string
}

// Returns an initialized instance of GenericDevicePlugin
func NewGenericDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	log.Println("Devicename " + deviceName)
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kubevirt-%s.sock", deviceName)
	dpi := &GenericDevicePlugin{
		devs:       devices,
		refresh:    make(chan struct{}, 1),
		rewatch:    make(chan struct{}, 1),
		socketPath: serverSock,
		done:       make(chan struct{}),
		term:       make(chan bool, 1),
		healthy:    make(chan string),
		unhealthy:  make(chan string),
		deviceName: deviceName,
		devicePath: devicePath,
	}
	return dpi
}

// snapshotDevs returns an independent copy of the current device list, captured
// under devsMu. Each element is a fresh *pluginapi.Device whose mutable Health
// field is captured at snapshot time, so a caller may read or marshal it after
// releasing the lock while another goroutine mutates the live devices (via
// setDeviceHealth) or replaces the whole set (via applyDevices) without a race,
// even across overlapping ListAndWatch streams. The Topology pointer is shared
// because it is never mutated after a device is created.
func (dpi *GenericDevicePlugin) snapshotDevs() []*pluginapi.Device {
	dpi.devsMu.RLock()
	defer dpi.devsMu.RUnlock()
	out := make([]*pluginapi.Device, len(dpi.devs))
	for i, dev := range dpi.devs {
		// Device embeds protoimpl.MessageState and must not be copied by value.
		out[i] = &pluginapi.Device{ID: dev.ID, Health: dev.Health, Topology: dev.Topology}
	}
	return out
}

// applyDevices replaces the served device set (vGPU rediscovery observed the
// profile's VFs changed) and asks ListAndWatch to re-advertise and healthCheck
// to extend its watches. The Health of a device that persists across the change
// (same ID) is carried over so an already-unhealthy VF is not silently reset to
// Healthy; a newly added VF starts Healthy. The signals are non-blocking, so a
// caller (the single-threaded rediscovery loop) never blocks here even if
// kubelet has not opened a ListAndWatch stream for this plugin.
func (dpi *GenericDevicePlugin) applyDevices(newDevs []*pluginapi.Device) {
	dpi.devsMu.Lock()
	previousHealth := make(map[string]string, len(dpi.devs))
	for _, dev := range dpi.devs {
		previousHealth[dev.ID] = dev.Health
	}
	for _, dev := range newDevs {
		if health, ok := previousHealth[dev.ID]; ok {
			dev.Health = health
		}
	}
	dpi.devs = newDevs
	dpi.devsMu.Unlock()
	notify(dpi.refresh)
	notify(dpi.rewatch)
}

// setDeviceHealth updates a single device's Health under devsMu.
func (dpi *GenericDevicePlugin) setDeviceHealth(id string, health string) {
	dpi.devsMu.Lock()
	defer dpi.devsMu.Unlock()
	for _, dev := range dpi.devs {
		if dev.ID == id {
			dev.Health = health
		}
	}
}

// notify performs a non-blocking send on a buffered(1) signal channel: it
// records that a refresh is pending without ever blocking the caller, coalescing
// with any already-pending signal.
func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func buildEnv(envList map[string][]string) map[string]string {
	env := map[string]string{}
	for key, devList := range envList {
		env[key] = strings.Join(devList, ",")
	}
	return env
}

func appendDeviceSpec(deviceSpecs *[]*pluginapi.DeviceSpec, seen map[string]struct{}, hostPath string) {
	if _, exists := seen[hostPath]; exists {
		return
	}
	*deviceSpecs = append(*deviceSpecs, &pluginapi.DeviceSpec{
		HostPath:      hostPath,
		ContainerPath: hostPath,
		Permissions:   "mrw",
	})
	seen[hostPath] = struct{}{}
}

func discoverEGMDevicesFunc() ([]EGMDeviceInfo, error) {
	egmClassDir := filepath.Join(rootPath, strings.TrimPrefix(egmClassPath, "/"))
	entries, err := os.ReadDir(egmClassDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	devices := make([]EGMDeviceInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "egm") {
			continue
		}
		gpuRaw, err := os.ReadFile(filepath.Join(egmClassDir, name, "gpu_devices"))
		if err != nil {
			log.Printf("warning: skipping EGM device %s: unable to read gpu_devices: %v", name, err)
			continue
		}
		gpuBDFs := strings.Fields(string(gpuRaw))
		if len(gpuBDFs) == 0 {
			continue
		}
		devPath := filepath.Join(deviceDir, name)
		if _, err := os.Stat(filepath.Join(rootPath, strings.TrimPrefix(devPath, "/"))); err != nil {
			log.Printf("warning: skipping EGM device %s: device node %s is not accessible: %v", name, devPath, err)
			continue
		}
		devices = append(devices, EGMDeviceInfo{DevPath: devPath, GPUBDFs: gpuBDFs})
	}

	slices.SortFunc(devices, func(a, b EGMDeviceInfo) int {
		return strings.Compare(a.DevPath, b.DevPath)
	})
	return devices, nil
}

// egmPathsForAllocatedGPUs returns EGM device paths only when ALL GPUs
// associated with an EGM device are present in the allocated BDF list.
// This prevents injecting a shared EGM device when only a subset of its
// GPUs are allocated (e.g. 1 of 2 GPUs on a 1C2G socket), which would
// cause conflicts if the remaining GPUs are allocated to a different VM.
func egmPathsForAllocatedGPUs(allocatedBDFs []string, egmDevices []EGMDeviceInfo) []string {
	allocated := make(map[string]struct{}, len(allocatedBDFs))
	for _, bdf := range allocatedBDFs {
		allocated[strings.ToLower(strings.TrimSpace(bdf))] = struct{}{}
	}
	paths := make([]string, 0)
	for _, egm := range egmDevices {
		allPresent := true
		for _, gpu := range egm.GPUBDFs {
			if _, ok := allocated[strings.ToLower(strings.TrimSpace(gpu))]; !ok {
				allPresent = false
				break
			}
		}
		if allPresent {
			paths = append(paths, egm.DevPath)
		}
	}
	slices.Sort(paths)
	return paths
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
	// Serialize the whole server lifecycle with Stop so a concurrent Stop
	// (rediscovery removing this profile) and a restart cannot race the server
	// field. The network calls below are bounded by connectionTimeout, and Stop
	// is infrequent, so holding the lock across them is acceptable.
	dpi.lifecycleMu.Lock()
	defer dpi.lifecycleMu.Unlock()

	if dpi.server != nil {
		return fmt.Errorf("gRPC server already started")
	}

	dpi.stop = stop
	// Fresh per-plugin termination channel for this Start/Stop cycle so a
	// restart (see restart()) rearms it after a previous Stop closed it.
	dpi.done = make(chan struct{})

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

// Stop stops the gRPC server. It is safe to call concurrently and repeatedly:
// lifecycleMu plus the server-nil guard make a second call a no-op, so the
// close(dpi.done) happens exactly once even when rediscovery's Stop and a
// healthCheck-driven restart target the same plugin at once.
func (dpi *GenericDevicePlugin) Stop() error {
	dpi.lifecycleMu.Lock()
	defer dpi.lifecycleMu.Unlock()

	if dpi.server == nil {
		return nil
	}

	// Terminate this plugin's healthCheck goroutine before the socket is
	// removed, so it does not observe the removal and try to restart the
	// plugin. Unlike dpi.stop (shared, closed only at process shutdown),
	// dpi.done is per-plugin, so this also cleanly stops a single plugin that
	// rediscovery removes because its vGPU profile is no longer configured.
	close(dpi.done)

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

	devs := dpi.snapshotDevs()
	log.Printf("[%s] ListAndWatch called, sending %d devices:", dpi.deviceName, len(devs))
	for _, dev := range devs {
		numaNodes := "nil"
		if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
			numaNodes = fmt.Sprintf("%d", dev.Topology.Nodes[0].ID)
		}
		log.Printf("  Device ID=%s, Health=%s, NUMA=%s", dev.ID, dev.Health, numaNodes)
	}

	s.Send(&pluginapi.ListAndWatchResponse{Devices: devs})

	for {
		select {
		case unhealthy := <-dpi.unhealthy:
			log.Printf("In watch unhealthy")
			dpi.setDeviceHealth(unhealthy, pluginapi.Unhealthy)
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.snapshotDevs()})
		case healthy := <-dpi.healthy:
			log.Printf("In watch healthy")
			dpi.setDeviceHealth(healthy, pluginapi.Healthy)
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.snapshotDevs()})
		case <-dpi.refresh:
			// vGPU rediscovery replaced this profile's device set via
			// applyDevices; re-advertise the current list.
			log.Printf("[%s] Device list updated by rediscovery, re-advertising", dpi.deviceName)
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.snapshotDevs()})
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
		log.Printf("[%s] Container request %d: DeviceIDs=%v", dpi.deviceName, i, req.DevicesIds)
	}
	log.Printf("[%s] This means kubelet passed Topology Manager admission!", dpi.deviceName)

	responses := pluginapi.AllocateResponse{}
	envList := map[string][]string{}
	iommufdSupported, err := supportsIOMMUFD()
	if err != nil {
		return nil, fmt.Errorf("could not determine iommufd support: %w", err)
	}
	egmDevices, err := discoverEGMDevices()
	if err != nil {
		log.Printf("[%s] Warning: unable to discover EGM devices, continuing without EGM mounts: %v", dpi.deviceName, err)
		egmDevices = nil
	}
	for _, req := range reqs.ContainerRequests {
		deviceSpecs := make([]*pluginapi.DeviceSpec, 0)
		seenDeviceSpecs := make(map[string]struct{})
		returnedMap := returnIommuMap()
		bdfToIommu := returnBdfToIommuMap()
		for _, bdf := range req.DevicesIds {
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

				if dev.addr == bdf {
					requestedDeviceFound = true
				}
				if iommufdSupported {
					vfiodev, err := readVFIODev(basePath, dev.addr)
					if err != nil {
						return nil, fmt.Errorf("could not determine iommufd device for device %s: %v", dev.addr, err)
					}
					appendDeviceSpec(&deviceSpecs, seenDeviceSpecs, filepath.Join(vfioDevicePath, "devices", vfiodev))
				}
			}
			if !requestedDeviceFound {
				return nil, fmt.Errorf("invalid allocation request: unknown device: %s", bdf)
			}
			// Expose only the requested device's address in this resource's env.
			// KubeVirt assigns these addresses positionally to the consuming
			// slots, so advertising the other functions of the IOMMU group lets a
			// sibling occupy a slot meant for another requested device. The full
			// group is still bound via the vfio DeviceSpecs below, and each
			// function is its own resource, so a sibling can be requested directly.
			devAddrs = append(devAddrs, bdf)

			appendDeviceSpec(&deviceSpecs, seenDeviceSpecs, filepath.Join(vfioDevicePath, "vfio"))
			appendDeviceSpec(&deviceSpecs, seenDeviceSpecs, filepath.Join(vfioDevicePath, iommuId))
			if iommufdSupported {
				appendDeviceSpec(&deviceSpecs, seenDeviceSpecs, iommuDevicePath)
			}

			key := fmt.Sprintf("%s_%s", gpuPrefix, strings.ToUpper(dpi.deviceName))
			if _, exists := envList[key]; !exists {
				envList[key] = []string{}
			}
			envList[key] = append(envList[key], devAddrs...)
		}
		egmPaths := egmPathsForAllocatedGPUs(req.DevicesIds, egmDevices)
		for _, egmPath := range egmPaths {
			appendDeviceSpec(&deviceSpecs, seenDeviceSpecs, egmPath)
		}
		if len(egmPaths) > 0 {
			log.Printf("[%s] EGM devices injected: %v", dpi.deviceName, egmPaths)
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
func (dpi *GenericDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	log.Printf("[%s] GetPreferredAllocation called with %d container request(s)", dpi.deviceName, len(in.ContainerRequests))

	response := &pluginapi.PreferredAllocationResponse{}

	for idx, req := range in.ContainerRequests {
		log.Printf("[%s] Container request %d: Available devices=%v, MustInclude=%v, AllocationSize=%d",
			dpi.deviceName, idx, req.AvailableDeviceIDs, req.MustIncludeDeviceIDs, req.AllocationSize)

		// Build a map of device ID to NUMA node from our device list
		deviceToNUMA := make(map[string]int64)
		for _, dev := range dpi.snapshotDevs() {
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

	// refreshWatches (re)derives the device-path watches from the currently
	// served devices. It is called once at start and again whenever vGPU
	// rediscovery signals dpi.rewatch, so a VF added to this profile while the
	// plugin is running gets its IOMMU-group path watched too. pathDeviceMap is
	// rebuilt from scratch each call so it only ever maps a path to the device
	// IDs currently served from it; watchedPaths is cumulative, so a watch left
	// behind for a path no longer in use is harmless: an event on it finds no
	// entry in the rebuilt pathDeviceMap and is ignored.
	refreshWatches := func() {
		bdfToIommu := returnBdfToIommuMap()
		clear(pathDeviceMap)
		for _, dev := range dpi.snapshotDevs() {
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
			if err := watcher.Add(devicePath); err != nil {
				log.Printf("%s: Unable to add device path to fsnotify watcher: %v", method, err)
				continue
			}
			log.Printf(" Adding Watcher to Path : %v", devicePath)
			watchedPaths[devicePath] = struct{}{}
		}
	}
	refreshWatches()

	// Capture the termination channel for this Start/Stop cycle. A later Start
	// (restart) installs a fresh dpi.done for its own healthCheck goroutine.
	done := dpi.done

	// sendHealth delivers a health transition to ListAndWatch, but gives up if
	// the plugin is being torn down. dpi.healthy/dpi.unhealthy are unbuffered
	// and only ListAndWatch receives them; without this a send would block
	// forever once ListAndWatch has exited (e.g. Stop() sent term first),
	// stranding this goroutine and its watcher. It returns false when the
	// plugin is stopping so the caller returns.
	sendHealth := func(ch chan string, id string) bool {
		select {
		case ch <- id:
			return true
		case <-done:
			return false
		case <-dpi.stop:
			return false
		}
	}

	for {
		select {
		case <-dpi.stop:
			return nil
		case <-done:
			return nil
		case <-dpi.rewatch:
			refreshWatches()
		case event := <-watcher.Events:
			if deviceIDs, ok := pathDeviceMap[event.Name]; ok {
				// Health in this case is if the device path actually exists
				if event.Op == fsnotify.Create {
					for _, id := range deviceIDs {
						if !sendHealth(dpi.healthy, id) {
							return nil
						}
					}
				} else if (event.Op == fsnotify.Remove) || (event.Op == fsnotify.Rename) {
					log.Printf("%s: Marking device unhealthy: %s", method, event.Name)
					for _, id := range deviceIDs {
						if !sendHealth(dpi.unhealthy, id) {
							return nil
						}
					}
				}
			} else if event.Name == dpi.socketPath && event.Op == fsnotify.Remove {
				// If this plugin is being torn down (its own Stop removed the
				// socket, closing done first), do not resurrect it - just exit.
				// This also keeps a rediscovery-driven Stop from racing a
				// restart into recreating a profile that is no longer configured.
				select {
				case <-done:
					return nil
				default:
				}
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
