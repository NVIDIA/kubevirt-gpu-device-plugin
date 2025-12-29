/*
 * Copyright (c) 2024, NVIDIA CORPORATION. All rights reserved.
 *
 * GPU Reconciler monitors kubelet's PodResources API to detect pod deletions
 * and deactivate FM partitions when VMs are terminated.
 */

package fabric_manager

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	podresourcesapi "k8s.io/kubelet/pkg/apis/podresources/v1"
)

const (
	// Kubelet PodResources socket path
	podResourcesSocket = "/var/lib/kubelet/pod-resources/kubelet.sock"

	// Default reconcile interval (fast polling for responsive partition cleanup)
	defaultReconcileInterval = 1 * time.Second
)

// GPUReconciler monitors pod-GPU assignments and deactivates orphaned partitions.
type GPUReconciler struct {
	mu                sync.Mutex
	manager           *PartitionManager
	resourceName      string // e.g., "nvidia.com/GH100_H100_SXM5_80GB"
	stopCh            chan struct{}
	reconcileInterval time.Duration

	// Track which GPUs are currently assigned (BDF -> podUID)
	gpuToPod map[string]string
}

// NewGPUReconciler creates a new GPU reconciler.
func NewGPUReconciler(manager *PartitionManager, resourceName string) *GPUReconciler {
	return &GPUReconciler{
		manager:           manager,
		resourceName:      resourceName,
		stopCh:            make(chan struct{}),
		reconcileInterval: defaultReconcileInterval,
		gpuToPod:          make(map[string]string),
	}
}

// Start begins the reconciliation loop.
func (r *GPUReconciler) Start() {
	log.Printf("[GPUReconciler] Starting reconciler for resource %s, interval %v", r.resourceName, r.reconcileInterval)

	// Perform startup reconciliation to clean up orphaned partitions
	r.reconcileOnStartup()

	go r.reconcileLoop()
}

// reconcileOnStartup deactivates any partitions that are active in FM but not assigned to running pods.
func (r *GPUReconciler) reconcileOnStartup() {
	log.Printf("[GPUReconciler] Performing startup reconciliation...")

	// Get current pod-GPU assignments from kubelet
	currentAssignments, err := r.getPodResourceAssignments()
	if err != nil {
		log.Printf("[GPUReconciler] Failed to get pod resources during startup: %v", err)
		return
	}

	// Build set of GPUs currently assigned to pods
	assignedGPUs := make(map[string]bool)
	for _, gpus := range currentAssignments {
		for _, gpu := range gpus {
			assignedGPUs[gpu] = true
		}
	}
	log.Printf("[GPUReconciler] Found %d GPUs currently assigned to pods", len(assignedGPUs))

	// Get active partition IDs from FM and check if their GPUs are assigned
	activePartitionIDs := r.manager.tree.GetActivePartitions()
	log.Printf("[GPUReconciler] Found %d active partition(s) in FM", len(activePartitionIDs))

	for _, partitionID := range activePartitionIDs {
		// Get GPUs for this partition
		gpus, err := r.manager.tree.GetGPUsForPartition(partitionID)
		if err != nil {
			log.Printf("[GPUReconciler] Failed to get GPUs for partition %d: %v", partitionID, err)
			continue
		}

		// Check if any GPU in this partition is assigned to a pod
		partitionInUse := false
		for _, gpu := range gpus {
			if assignedGPUs[gpu] {
				partitionInUse = true
				break
			}
		}

		if !partitionInUse {
			log.Printf("[GPUReconciler] Partition %d (GPUs: %v) is orphaned, deactivating...", partitionID, gpus)
			if err := r.manager.client.DeactivatePartition(partitionID); err != nil {
				log.Printf("[GPUReconciler] Failed to deactivate orphaned partition %d: %v", partitionID, err)
			} else {
				r.manager.tree.MarkInactive(partitionID)
				log.Printf("[GPUReconciler] Successfully deactivated orphaned partition %d", partitionID)
			}
		} else {
			log.Printf("[GPUReconciler] Partition %d (GPUs: %v) is in use by a pod", partitionID, gpus)
		}
	}

	log.Printf("[GPUReconciler] Startup reconciliation complete")
}

// Stop stops the reconciler.
func (r *GPUReconciler) Stop() {
	close(r.stopCh)
}

// reconcileLoop periodically checks for orphaned partitions.
func (r *GPUReconciler) reconcileLoop() {
	ticker := time.NewTicker(r.reconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.reconcile()
		case <-r.stopCh:
			log.Printf("[GPUReconciler] Stopping reconciler")
			return
		}
	}
}

// ReconcileNow performs an immediate synchronous reconciliation.
// Call this before allocation to ensure orphaned partitions are cleaned up.
func (r *GPUReconciler) ReconcileNow() {
	r.reconcile()
}

// reconcile checks current pod-GPU assignments and deactivates orphaned partitions.
func (r *GPUReconciler) reconcile() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Get current pod-GPU assignments from kubelet
	currentAssignments, err := r.getPodResourceAssignments()
	if err != nil {
		log.Printf("[GPUReconciler] Failed to get pod resources: %v", err)
		return
	}

	// Build set of currently assigned GPUs
	currentGPUs := make(map[string]string) // BDF -> podName
	for podName, gpus := range currentAssignments {
		for _, gpu := range gpus {
			currentGPUs[gpu] = podName
		}
	}

	// Find pods that have lost their GPUs (collect unique pods first)
	deletedPods := make(map[string][]string) // podName -> list of GPUs that were lost
	for gpu, oldPodName := range r.gpuToPod {
		if _, stillAssigned := currentGPUs[gpu]; !stillAssigned {
			deletedPods[oldPodName] = append(deletedPods[oldPodName], gpu)
		}
	}

	// Deactivate partition ONCE per deleted pod
	for podName, gpus := range deletedPods {
		// Check if partition is tracked by pod name in our tree
		partitionID, trackedByPod := r.manager.tree.GetPartitionForPod(podName)

		if trackedByPod {
			// Pod was tracked - use DeallocatePartition which handles FM + tree
			if err := r.manager.DeallocatePartition(podName); err != nil {
				log.Printf("[GPUReconciler] Failed to deallocate partition %d for pod %s: %v", partitionID, podName, err)
			} else {
				log.Printf("[GPUReconciler] Deactivated partition %d for pod %s (GPUs: %v)", partitionID, podName, gpus)
			}
		} else {
			// Pod was NOT tracked by name - find partition by GPUs and deactivate directly
			partition, err := r.manager.tree.FindPartitionForGPUs(gpus)
			if err != nil || partition == nil {
				log.Printf("[GPUReconciler] Pod %s not tracked and partition not found for GPUs %v", podName, gpus)
				continue
			}
			partitionID = partition.ID

			// Deactivate directly via FM
			log.Printf("[GPUReconciler] Pod %s not tracked, deactivating partition %d directly", podName, partitionID)
			if err := r.manager.client.DeactivatePartition(partitionID); err != nil {
				log.Printf("[GPUReconciler] Failed to deactivate partition %d: %v", partitionID, err)
			} else {
				r.manager.tree.MarkInactive(partitionID)
				log.Printf("[GPUReconciler] Deactivated partition %d for pod %s (GPUs: %v)", partitionID, podName, gpus)
			}
		}
	}

	// Update tracking
	r.gpuToPod = currentGPUs
}

// getPodResourceAssignments queries kubelet's PodResources API.
func (r *GPUReconciler) getPodResourceAssignments() (map[string][]string, error) {
	// Connect to kubelet's PodResources socket
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, podResourcesSocket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return net.DialTimeout("unix", addr, 5*time.Second)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to PodResources socket: %w", err)
	}
	defer conn.Close()

	client := podresourcesapi.NewPodResourcesListerClient(conn)

	resp, err := client.List(ctx, &podresourcesapi.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to list pod resources: %w", err)
	}

	// Extract GPU assignments for our resource type
	assignments := make(map[string][]string) // podUID -> []BDFs

	for _, podResource := range resp.PodResources {
		podUID := podResource.Name // Actually this is pod name, not UID

		for _, container := range podResource.Containers {
			for _, device := range container.Devices {
				if device.ResourceName == r.resourceName {
					// Device IDs are the GPU BDFs
					assignments[podUID] = append(assignments[podUID], device.DeviceIds...)
				}
			}
		}
	}

	return assignments, nil
}

// TrackAllocation records a new GPU allocation.
// Call this from Allocate() to track pod-GPU mappings.
func (r *GPUReconciler) TrackAllocation(podUID string, gpuBDFs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, gpu := range gpuBDFs {
		r.gpuToPod[gpu] = podUID
	}
	log.Printf("[GPUReconciler] Tracking allocation: pod %s -> GPUs %v", podUID, gpuBDFs)
}
