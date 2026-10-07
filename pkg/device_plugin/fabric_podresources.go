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
	"context"
	"fmt"
	"strings"

	podresourcesv1 "k8s.io/kubelet/pkg/apis/podresources/v1"
)

// podResourcesSocket is the kubelet pod-resources API endpoint. Listing it tells
// us which device IDs (VF PCI BDFs, for our resources) are currently allocated
// to running pods — the only way to reconstruct per-partition VF membership
// after a device plugin restart, since Fabric Manager does not report the VF
// list of an active partition.
const podResourcesSocket = "/var/lib/kubelet/pod-resources/kubelet.sock"

// allocatedDevice is one device id currently allocated to a running pod, under
// a given resource name.
type allocatedDevice struct {
	resourceName string
	deviceID     string
}

// listAllocatedDeviceSetsViaPodResources returns the devices currently allocated
// to running pods, grouped per container, as reported by the kubelet
// pod-resources API. Each inner slice is one container's device set — for a
// KubeVirt VM that is the virt-launcher container, so the set is exactly the
// GPUs (VF PCI BDFs) of one VM. The grouping is what lets the fabric reconciler
// reconstruct a multi-GPU partition (a VM allocated N whole cards) after a
// device-plugin restart, since kubelet does not re-call Allocate for
// already-running pods and Fabric Manager does not report a partition's VF list.
// Only devices under the DeviceNamespace (nvidia.com/*) are relevant to fabric
// activation; callers filter as needed.
func listAllocatedDeviceSetsViaPodResources() ([][]allocatedDevice, error) {
	conn, err := connect(podResourcesSocket, connectionTimeout)
	if err != nil {
		return nil, fmt.Errorf("dialing pod-resources socket %s: %w", podResourcesSocket, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	client := podresourcesv1.NewPodResourcesListerClient(conn)
	resp, err := client.List(ctx, &podresourcesv1.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing pod resources: %w", err)
	}

	var sets [][]allocatedDevice
	for _, pod := range resp.GetPodResources() {
		for _, container := range pod.GetContainers() {
			var set []allocatedDevice
			for _, dev := range container.GetDevices() {
				for _, id := range dev.GetDeviceIds() {
					set = append(set, allocatedDevice{
						resourceName: dev.GetResourceName(),
						deviceID:     id,
					})
				}
			}
			if len(set) > 0 {
				sets = append(sets, set)
			}
		}
	}
	return sets, nil
}

// nvidiaResourcePrefix is the resource-name prefix of devices this plugin
// advertises (e.g. "nvidia.com/GPU_PASSTHROUGH_..." or a vGPU profile name).
var nvidiaResourcePrefix = DeviceNamespace + "/"

// isNvidiaResource reports whether a pod-resources resource name is one this
// plugin manages.
func isNvidiaResource(resourceName string) bool {
	return strings.HasPrefix(resourceName, nvidiaResourcePrefix)
}
