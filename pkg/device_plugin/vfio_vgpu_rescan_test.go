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

package device_plugin

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// capturingLASServer is a race-safe fake DevicePlugin_ListAndWatchServer that
// records every advertised device list, so a test can drive a live
// ListAndWatch and assert what it sends.
type capturingLASServer struct {
	grpc.ServerStream
	mu   sync.Mutex
	sent [][]*pluginapi.Device
}

func (s *capturingLASServer) Send(m *pluginapi.ListAndWatchResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m.Devices)
	return nil
}

func (s *capturingLASServer) lastAddrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return nil
	}
	addrs := []string{}
	for _, d := range s.sent[len(s.sent)-1] {
		addrs = append(addrs, d.ID)
	}
	return addrs
}

var _ = Describe("Vendor-specific VFIO vGPU rediscovery", func() {
	Context("vfioVGpuRescanInterval() Tests", func() {
		var origValue string
		var origSet bool

		BeforeEach(func() {
			origValue, origSet = os.LookupEnv(vfioVGpuRescanIntervalEnv)
		})

		AfterEach(func() {
			if origSet {
				Expect(os.Setenv(vfioVGpuRescanIntervalEnv, origValue)).To(Succeed())
			} else {
				Expect(os.Unsetenv(vfioVGpuRescanIntervalEnv)).To(Succeed())
			}
		})

		It("Disables rediscovery when the env var is unset", func() {
			Expect(os.Unsetenv(vfioVGpuRescanIntervalEnv)).To(Succeed())
			Expect(vfioVGpuRescanInterval()).To(Equal(time.Duration(0)))
		})

		It("Disables rediscovery when the env var is empty or whitespace", func() {
			Expect(os.Setenv(vfioVGpuRescanIntervalEnv, "   ")).To(Succeed())
			Expect(vfioVGpuRescanInterval()).To(Equal(time.Duration(0)))
		})

		It("Honors a valid duration above the minimum", func() {
			Expect(os.Setenv(vfioVGpuRescanIntervalEnv, "30s")).To(Succeed())
			Expect(vfioVGpuRescanInterval()).To(Equal(30 * time.Second))
		})

		It("Disables rediscovery for an unparseable value", func() {
			Expect(os.Setenv(vfioVGpuRescanIntervalEnv, "not-a-duration")).To(Succeed())
			Expect(vfioVGpuRescanInterval()).To(Equal(time.Duration(0)))
		})

		It("Disables rediscovery for a non-positive value", func() {
			Expect(os.Setenv(vfioVGpuRescanIntervalEnv, "-5s")).To(Succeed())
			Expect(vfioVGpuRescanInterval()).To(Equal(time.Duration(0)))
		})

		It("Clamps a too-small positive value up to the minimum", func() {
			Expect(os.Setenv(vfioVGpuRescanIntervalEnv, "1s")).To(Succeed())
			Expect(vfioVGpuRescanInterval()).To(Equal(vfioVGpuRescanMinInterval))
		})
	})

	Context("rescanVfioVGpus() Tests", func() {
		var origDiscover func() []resolvedVfioVF
		var origStart func(*GenericDevicePlugin) error
		var origStop func(*GenericDevicePlugin) error
		var origUpdate func(*GenericDevicePlugin, []*pluginapi.Device)

		var started []string
		var stopped []string
		var updated map[string][]*pluginapi.Device

		// deviceAddrs extracts the PCI addresses a plugin device list advertises.
		deviceAddrs := func(devs []*pluginapi.Device) []string {
			addrs := make([]string, 0, len(devs))
			for _, d := range devs {
				addrs = append(addrs, d.ID)
			}
			return addrs
		}

		// seedRunning publishes an initial resolved set into the shared maps and
		// registers a running plugin per profile, mirroring the startup path.
		seedRunning := func(resolved []resolvedVfioVF) {
			discoverVfioVGpus = func() []resolvedVfioVF { return resolved }
			createVfioVGpuMap()
			vfioPluginsMu.Lock()
			vfioPlugins = map[string]*GenericDevicePlugin{}
			for name, devices := range profilesFromResolved(resolved) {
				vfioPlugins[name] = NewGenericDevicePlugin(name, vfioDevicePathPrefix, buildVfioDevices(devices))
			}
			vfioPluginsMu.Unlock()
		}

		BeforeEach(func() {
			origDiscover = discoverVfioVGpus
			origStart = startDevicePlugin
			origStop = stopDevicePlugin
			origUpdate = updateDevicePlugin

			started = nil
			stopped = nil
			updated = map[string][]*pluginapi.Device{}

			startDevicePlugin = func(dp *GenericDevicePlugin) error {
				started = append(started, dp.deviceName)
				return nil
			}
			stopDevicePlugin = func(dp *GenericDevicePlugin) error {
				stopped = append(stopped, dp.deviceName)
				return nil
			}
			updateDevicePlugin = func(dp *GenericDevicePlugin, devs []*pluginapi.Device) {
				updated[dp.deviceName] = devs
			}

			mapsMu.Lock()
			iommuMap = map[string][]NvidiaGpuDevice{}
			bdfToIommuMap = map[string]string{}
			vfioVGpuMap = map[string][]NvidiaGpuDevice{}
			vfioIommuBDFs = map[string]string{}
			mapsMu.Unlock()
			vfioPluginsMu.Lock()
			vfioPlugins = map[string]*GenericDevicePlugin{}
			vfioPluginsMu.Unlock()
		})

		AfterEach(func() {
			discoverVfioVGpus = origDiscover
			startDevicePlugin = origStart
			stopDevicePlugin = origStop
			updateDevicePlugin = origUpdate

			mapsMu.Lock()
			iommuMap = nil
			bdfToIommuMap = nil
			vfioVGpuMap = nil
			vfioIommuBDFs = map[string]string{}
			mapsMu.Unlock()
			vfioPluginsMu.Lock()
			vfioPlugins = map[string]*GenericDevicePlugin{}
			vfioPluginsMu.Unlock()
		})

		It("Does nothing when the rescan observes no change", func() {
			resolved := []resolvedVfioVF{
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
			}
			seedRunning(resolved)
			discoverVfioVGpus = func() []resolvedVfioVF { return resolved }

			rescanVfioVGpus()

			Expect(started).To(BeEmpty())
			Expect(stopped).To(BeEmpty())
			Expect(updated).To(BeEmpty())
			Expect(vfioPlugins).To(HaveKey(vfioVgpuResourceName1))
		})

		It("Pushes an updated device list when a VF is added within an existing resource", func() {
			seedRunning([]resolvedVfioVF{
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
			})
			discoverVfioVGpus = func() []resolvedVfioVF {
				return []resolvedVfioVF{
					{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
					{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress3}, iommuGroup: "73"},
				}
			}

			rescanVfioVGpus()

			Expect(started).To(BeEmpty())
			Expect(stopped).To(BeEmpty())
			Expect(updated).To(HaveKey(vfioVgpuResourceName1))
			Expect(deviceAddrs(updated[vfioVgpuResourceName1])).To(ConsistOf(vfioVfAddress1, vfioVfAddress3))
			// The new VF must be allocatable the moment it is advertised.
			Expect(bdfToIommuMap[vfioVfAddress3]).To(Equal("73"))
			Expect(iommuMap["73"]).To(HaveLen(1))
		})

		It("Pushes an updated device list when a VF is removed within an existing resource", func() {
			seedRunning([]resolvedVfioVF{
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress3}, iommuGroup: "73"},
			})
			discoverVfioVGpus = func() []resolvedVfioVF {
				return []resolvedVfioVF{
					{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
				}
			}

			rescanVfioVGpus()

			Expect(started).To(BeEmpty())
			Expect(stopped).To(BeEmpty())
			Expect(updated).To(HaveKey(vfioVgpuResourceName1))
			Expect(deviceAddrs(updated[vfioVgpuResourceName1])).To(ConsistOf(vfioVfAddress1))
			// The removed VF must no longer be allocatable.
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress3))
			Expect(iommuMap).ToNot(HaveKey("73"))
		})

		It("Starts a new device plugin when a new profile appears", func() {
			seedRunning([]resolvedVfioVF{
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
			})
			discoverVfioVGpus = func() []resolvedVfioVF {
				return []resolvedVfioVF{
					{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
					{profileName: vfioVgpuResourceName2, device: NvidiaGpuDevice{addr: vfioVfAddress2}, iommuGroup: "72"},
				}
			}

			rescanVfioVGpus()

			Expect(started).To(ConsistOf(vfioVgpuResourceName2))
			Expect(stopped).To(BeEmpty())
			Expect(updated).To(BeEmpty())
			Expect(vfioPlugins).To(HaveKey(vfioVgpuResourceName1))
			Expect(vfioPlugins).To(HaveKey(vfioVgpuResourceName2))
			Expect(bdfToIommuMap[vfioVfAddress2]).To(Equal("72"))
		})

		It("Stops the device plugin when a profile disappears", func() {
			seedRunning([]resolvedVfioVF{
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
				{profileName: vfioVgpuResourceName2, device: NvidiaGpuDevice{addr: vfioVfAddress2}, iommuGroup: "72"},
			})
			discoverVfioVGpus = func() []resolvedVfioVF {
				return []resolvedVfioVF{
					{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
				}
			}

			rescanVfioVGpus()

			Expect(started).To(BeEmpty())
			Expect(stopped).To(ConsistOf(vfioVgpuResourceName2))
			Expect(updated).To(BeEmpty())
			Expect(vfioPlugins).To(HaveKey(vfioVgpuResourceName1))
			Expect(vfioPlugins).ToNot(HaveKey(vfioVgpuResourceName2))
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress2))
			Expect(iommuMap).ToNot(HaveKey("72"))
		})

		It("Leaves GPU-passthrough iommu entries untouched when a vGPU profile is removed", func() {
			seedRunning([]resolvedVfioVF{
				{profileName: vfioVgpuResourceName1, device: NvidiaGpuDevice{addr: vfioVfAddress1}, iommuGroup: "71"},
			})
			// A passthrough GPU registered by createIommuDeviceMap, which the vGPU
			// discovery does not own (absent from vfioIommuBDFs).
			passthroughAddr := "0000:01:00.0"
			mapsMu.Lock()
			iommuMap["10"] = append(iommuMap["10"], NvidiaGpuDevice{addr: passthroughAddr})
			bdfToIommuMap[passthroughAddr] = "10"
			mapsMu.Unlock()

			discoverVfioVGpus = func() []resolvedVfioVF { return nil }

			rescanVfioVGpus()

			Expect(stopped).To(ConsistOf(vfioVgpuResourceName1))
			Expect(bdfToIommuMap[passthroughAddr]).To(Equal("10"))
			Expect(iommuMap["10"]).To(HaveLen(1))
			Expect(bdfToIommuMap).ToNot(HaveKey(vfioVfAddress1))
		})
	})

	Context("GenericDevicePlugin.applyDevices() Tests", func() {
		It("Preserves the health of devices that persist across an update", func() {
			dp := NewGenericDevicePlugin("test-vgpu", vfioDevicePathPrefix, []*pluginapi.Device{
				{ID: vfioVfAddress1, Health: pluginapi.Unhealthy},
				{ID: vfioVfAddress2, Health: pluginapi.Healthy},
			})

			// A rescan rebuilds every device as Healthy and drops vfioVfAddress2.
			dp.applyDevices([]*pluginapi.Device{
				{ID: vfioVfAddress1, Health: pluginapi.Healthy},
				{ID: vfioVfAddress3, Health: pluginapi.Healthy},
			})

			health := map[string]string{}
			for _, d := range dp.snapshotDevs() {
				health[d.ID] = d.Health
			}
			Expect(health).To(HaveLen(2))
			// The still-present VF keeps its prior unhealthy state.
			Expect(health[vfioVfAddress1]).To(Equal(pluginapi.Unhealthy))
			// The newly added VF starts healthy.
			Expect(health[vfioVfAddress3]).To(Equal(pluginapi.Healthy))
			// The removed VF is gone.
			Expect(health).ToNot(HaveKey(vfioVfAddress2))
		})

		It("Signals a refresh and rewatch without blocking when no consumer is running", func() {
			dp := NewGenericDevicePlugin("test-vgpu", vfioDevicePathPrefix, nil)

			// No ListAndWatch/healthCheck goroutine is draining refresh/rewatch.
			// Reaching the assertions proves neither applyDevices call blocked;
			// the buffered(1) signals coalesce rather than accumulate or block.
			dp.applyDevices([]*pluginapi.Device{
				{ID: vfioVfAddress1, Health: pluginapi.Healthy},
			})
			dp.applyDevices([]*pluginapi.Device{
				{ID: vfioVfAddress1, Health: pluginapi.Healthy},
				{ID: vfioVfAddress2, Health: pluginapi.Healthy},
			})

			Expect(dp.refresh).To(HaveLen(1))
			Expect(dp.rewatch).To(HaveLen(1))
			ids := []string{}
			for _, d := range dp.snapshotDevs() {
				ids = append(ids, d.ID)
			}
			Expect(ids).To(ConsistOf(vfioVfAddress1, vfioVfAddress2))
		})
	})

	Context("GenericDevicePlugin teardown Tests", func() {
		It("Terminates the healthCheck goroutine when the plugin is stopped", func() {
			// When rediscovery removes a profile it calls Stop() on that one
			// plugin while the shared process stop channel stays open. The
			// plugin's healthCheck goroutine must still terminate; without a
			// per-plugin done signal it would block forever on watcher.Events,
			// leaking the goroutine and its fsnotify watcher.
			tmpDir, err := os.MkdirTemp("", "vgpu-healthcheck")
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = os.RemoveAll(tmpDir) }()

			dp := NewGenericDevicePlugin("test-vgpu-hc", vfioDevicePathPrefix, nil)
			// Point the watched paths at a real directory so healthCheck's setup
			// succeeds and it reaches its select loop rather than returning early.
			dp.socketPath = filepath.Join(tmpDir, "test-vgpu-hc.sock")
			dp.devicePath = tmpDir
			dp.stop = make(chan struct{})

			finished := make(chan struct{})
			go func() {
				defer GinkgoRecover()
				_ = dp.healthCheck()
				close(finished)
			}()

			// Closing done is exactly what Stop() does for a removed profile.
			close(dp.done)
			Eventually(finished, "3s").Should(BeClosed())
		})

		It("Stop is safe to call concurrently without panicking", func() {
			tmpDir, err := os.MkdirTemp("", "vgpu-stop")
			Expect(err).ToNot(HaveOccurred())
			defer func() { _ = os.RemoveAll(tmpDir) }()

			// Rediscovery's Stop() and a healthCheck-driven restart's Stop() can
			// target the same plugin at once. Without serialization both pass the
			// server-nil guard and double-close done -> panic. Many iterations
			// with a release barrier make the narrow window reliably reproduce.
			for i := 0; i < 500; i++ {
				dp := NewGenericDevicePlugin("test-vgpu-stop", vfioDevicePathPrefix, nil)
				dp.socketPath = filepath.Join(tmpDir, "test-vgpu-stop.sock")
				dp.stop = make(chan struct{})
				dp.server = grpc.NewServer()

				start := make(chan struct{})
				var wg sync.WaitGroup
				for g := 0; g < 2; g++ {
					wg.Add(1)
					go func() {
						defer GinkgoRecover()
						defer wg.Done()
						<-start
						_ = dp.Stop()
					}()
				}
				close(start)
				wg.Wait()

				Expect(dp.server).To(BeNil())
			}
		})

		It("Re-advertises the updated device list through ListAndWatch after applyDevices", func() {
			dp := NewGenericDevicePlugin("test-vgpu-law", vfioDevicePathPrefix,
				buildVfioDevices([]NvidiaGpuDevice{{addr: vfioVfAddress1}}))
			dp.stop = make(chan struct{})

			server := &capturingLASServer{}
			go func() {
				defer GinkgoRecover()
				_ = dp.ListAndWatch(&pluginapi.Empty{}, server)
			}()

			// The initial advertise carries the starting device set.
			Eventually(server.lastAddrs, "2s").Should(ConsistOf(vfioVfAddress1))

			// Rediscovery adds a VF to this profile; ListAndWatch must consume
			// the refresh signal and re-send the updated list.
			dp.applyDevices(buildVfioDevices([]NvidiaGpuDevice{
				{addr: vfioVfAddress1},
				{addr: vfioVfAddress3},
			}))
			Eventually(server.lastAddrs, "2s").Should(ConsistOf(vfioVfAddress1, vfioVfAddress3))

			close(dp.stop)
		})
	})
})
