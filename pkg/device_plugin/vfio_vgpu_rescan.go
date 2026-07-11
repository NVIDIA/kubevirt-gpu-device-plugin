/*
 * Copyright (c) 2026, NVIDIA CORPORATION. All rights reserved.
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

// This file implements optional periodic rediscovery of vendor-specific VFIO
// vGPU profiles. The startup scan (createVfioVGpuMap) reads each Virtual
// Function's configured profile exactly once, so a profile created or changed
// after the plugin starts is not advertised until the pod restarts. When
// enabled through the VFIO_VGPU_RESCAN_INTERVAL env var, a background ticker
// re-runs the same discovery and reconciles the running device plugins with the
// freshly observed profiles.
//
// A ticker is used rather than an fsnotify/inotify watch on sysfs because
// inotify does not fire reliably for the attribute writes that reconfigure a
// vGPU (current_vgpu_type, creatable_vgpu_types are kernel-backed synthetic
// files, not regular files), and SR-IOV VF creation/removal moves whole device
// directories in ways that are awkward to watch correctly. A full periodic
// rescan reuses the exact discovery already validated at startup.
package device_plugin

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"

	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	// vfioVGpuRescanIntervalEnv names the env var that enables and paces vGPU
	// profile rediscovery. Its value is a Go duration string (for example
	// "30s"). When unset, empty or non-positive, rediscovery is disabled and
	// the plugin keeps its historical scan-once-at-startup behavior.
	vfioVGpuRescanIntervalEnv = "VFIO_VGPU_RESCAN_INTERVAL"
	// vfioVGpuRescanMinInterval is the smallest honored rescan interval. A
	// configured value below this floor is clamped up so a typo cannot make the
	// plugin walk sysfs (and possibly NVML) in a tight loop.
	vfioVGpuRescanMinInterval = 5 * time.Second
	// vfioDevicePathPrefix is the device path passed to every vGPU
	// GenericDevicePlugin, matching the classic GPU-passthrough contract.
	vfioDevicePathPrefix = "/dev/vfio/"
)

// vfioPlugins tracks the running vendor-specific VFIO vGPU device plugins by
// their resource (profile) name so rediscovery can add, update and remove them
// individually. It is guarded by vfioPluginsMu.
var (
	vfioPlugins   = map[string]*GenericDevicePlugin{}
	vfioPluginsMu sync.Mutex
)

// stopDevicePlugin and updateDevicePlugin are indirected through variables so
// tests can observe the rediscovery reconciliation without a running gRPC
// server or ListAndWatch loop.
var (
	stopDevicePlugin   = func(dp *GenericDevicePlugin) error { return dp.Stop() }
	updateDevicePlugin = func(dp *GenericDevicePlugin, devs []*pluginapi.Device) {
		dp.applyDevices(devs)
	}
)

// buildVfioDevices renders discovered VFs into the kubelet device list served
// by a GenericDevicePlugin, carrying NUMA topology like the startup path.
func buildVfioDevices(gpuDevices []NvidiaGpuDevice) []*pluginapi.Device {
	devs := make([]*pluginapi.Device, 0, len(gpuDevices))
	for _, gpuDev := range gpuDevices {
		devs = append(devs, &pluginapi.Device{
			ID:     gpuDev.addr,
			Health: pluginapi.Healthy,
			Topology: &pluginapi.TopologyInfo{
				Nodes: []*pluginapi.NUMANode{
					{ID: gpuDev.numaNode},
				},
			},
		})
	}
	return devs
}

// vfioVGpuRescanInterval reads and validates the configured rescan interval.
// It returns 0 (disabled) when the env var is unset, empty, unparseable or
// non-positive, and clamps a too-small positive value up to the minimum.
func vfioVGpuRescanInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(vfioVGpuRescanIntervalEnv))
	if raw == "" {
		return 0
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("Ignoring invalid %s=%q: %v; vGPU profile rediscovery stays disabled", vfioVGpuRescanIntervalEnv, raw, err)
		return 0
	}
	if interval <= 0 {
		return 0
	}
	if interval < vfioVGpuRescanMinInterval {
		log.Printf("Clamping %s=%s up to the %s minimum to avoid hammering sysfs and NVML", vfioVGpuRescanIntervalEnv, interval, vfioVGpuRescanMinInterval)
		interval = vfioVGpuRescanMinInterval
	}
	return interval
}

// startVfioVGpuRediscovery launches the rediscovery ticker when the operator
// opts in. With the feature off it logs the disabled state and returns, so the
// startup path stays exactly as it was before this feature.
func startVfioVGpuRediscovery(stop chan struct{}) {
	interval := vfioVGpuRescanInterval()
	if interval <= 0 {
		log.Printf("Vendor-specific VFIO vGPU profile rediscovery disabled (set %s to a positive Go duration such as 30s to enable)", vfioVGpuRescanIntervalEnv)
		return
	}
	log.Printf("Vendor-specific VFIO vGPU profile rediscovery enabled, rescanning every %s", interval)
	go rescanVfioVGpuLoop(interval, stop)
}

// rescanVfioVGpuLoop rescans on every tick until stop is signaled.
func rescanVfioVGpuLoop(interval time.Duration, stop chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			rescanVfioVGpus()
		}
	}
}

// rescanVfioVGpus re-runs discovery and reconciles the running vGPU device
// plugins with the observed profiles:
//   - a profile whose set of VFs changed has its device list pushed through the
//     existing ListAndWatch loop,
//   - a newly observed profile gets a new device plugin started,
//   - a profile that no longer has any configured VF has its plugin stopped.
//
// The shared iommuMap and bdfToIommuMap are updated before any plugin change is
// advertised so Allocate accepts a new VF the moment kubelet sees it.
func rescanVfioVGpus() {
	resolved := discoverVfioVGpus()
	newProfiles := profilesFromResolved(resolved)

	mapsMu.Lock()
	publishVfioVGpusLocked(resolved)
	mapsMu.Unlock()

	vfioPluginsMu.Lock()
	defer vfioPluginsMu.Unlock()

	// Start new plugins and update existing ones whose composition changed.
	for name, devices := range newProfiles {
		pluginDevs := buildVfioDevices(devices)
		if dp, ok := vfioPlugins[name]; ok {
			if vfioDevicesChanged(dp, devices) {
				log.Printf("vGPU profile %s composition changed, now serving %d VF(s)", name, len(devices))
				updateDevicePlugin(dp, pluginDevs)
			}
			continue
		}
		log.Printf("New vGPU profile %s discovered with %d VF(s), starting device plugin", name, len(devices))
		dp := NewGenericDevicePlugin(name, vfioDevicePathPrefix, pluginDevs)
		if err := startDevicePlugin(dp); err != nil {
			log.Printf("Error starting device plugin for new vGPU profile %s: %v", name, err)
			continue
		}
		vfioPlugins[name] = dp
	}

	// Stop plugins for profiles that no longer have any configured VF.
	for name, dp := range vfioPlugins {
		if _, ok := newProfiles[name]; ok {
			continue
		}
		log.Printf("vGPU profile %s no longer configured, stopping device plugin", name)
		if err := stopDevicePlugin(dp); err != nil {
			log.Printf("Error stopping device plugin for removed vGPU profile %s: %v", name, err)
		}
		delete(vfioPlugins, name)
	}
}

// profilesFromResolved groups resolved VFs by their profile (resource) name.
func profilesFromResolved(resolved []resolvedVfioVF) map[string][]NvidiaGpuDevice {
	profiles := make(map[string][]NvidiaGpuDevice)
	for _, vf := range resolved {
		profiles[vf.profileName] = append(profiles[vf.profileName], vf.device)
	}
	return profiles
}

// vfioDevicesChanged reports whether the set of VFs (by PCI address and NUMA
// node) a plugin currently advertises differs from the freshly discovered set.
func vfioDevicesChanged(dp *GenericDevicePlugin, devices []NvidiaGpuDevice) bool {
	current := dp.snapshotDevs()
	if len(current) != len(devices) {
		return true
	}
	want := make(map[string]int64, len(devices))
	for _, d := range devices {
		want[d.addr] = d.numaNode
	}
	for _, dev := range current {
		numa := int64(-1)
		if dev.Topology != nil && len(dev.Topology.Nodes) > 0 {
			numa = dev.Topology.Nodes[0].ID
		}
		wantNuma, ok := want[dev.ID]
		if !ok || wantNuma != numa {
			return true
		}
	}
	return false
}
