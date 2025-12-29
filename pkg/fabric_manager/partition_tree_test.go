/*
 * Unit tests for Fabric Manager partition tree and allocation logic.
 * Covers all edge cases discovered during development:
 * - Partition hierarchy (parent/child blocking)
 * - Orphaned partition cleanup
 * - Partition availability checking
 */

package fabric_manager

import (
	"testing"
)

// createTestTree creates a standard 8-GPU partition tree for testing.
func createTestTree() *PartitionTree {
	tree := NewPartitionTree()

	// Partitions representing 8 GPU system with 4/2/1 hierarchy
	partitions := []Partition{
		{ID: 0, NumGPUs: 8, PhysicalIDs: []int{1, 2, 3, 4, 5, 6, 7, 8}},
		{ID: 1, NumGPUs: 4, PhysicalIDs: []int{1, 2, 3, 4}},
		{ID: 2, NumGPUs: 4, PhysicalIDs: []int{5, 6, 7, 8}},
		{ID: 3, NumGPUs: 2, PhysicalIDs: []int{1, 3}},
		{ID: 4, NumGPUs: 2, PhysicalIDs: []int{2, 4}},
		{ID: 5, NumGPUs: 2, PhysicalIDs: []int{5, 7}},
		{ID: 6, NumGPUs: 2, PhysicalIDs: []int{6, 8}},
		{ID: 7, NumGPUs: 1, PhysicalIDs: []int{1}},
		{ID: 8, NumGPUs: 1, PhysicalIDs: []int{2}},
	}

	if err := tree.BuildFromPartitions(partitions); err != nil {
		panic("Failed to build test tree: " + err.Error())
	}

	// Set up PCI mappings
	tree.SetPCIMapping(1, "0000:09:00.0")
	tree.SetPCIMapping(2, "0000:17:00.0")
	tree.SetPCIMapping(3, "0000:3b:00.0")
	tree.SetPCIMapping(4, "0000:44:00.0")
	tree.SetPCIMapping(5, "0000:87:00.0")
	tree.SetPCIMapping(6, "0000:90:00.0")
	tree.SetPCIMapping(7, "0000:b8:00.0")
	tree.SetPCIMapping(8, "0000:c1:00.0")

	return tree
}

// TestPartitionTreeBasicOperations tests tree construction and basic queries.
func TestPartitionTreeBasicOperations(t *testing.T) {
	tree := createTestTree()

	t.Run("GetAvailablePartitions_All4GPUAvailable", func(t *testing.T) {
		available := tree.GetAvailablePartitions(4)
		if len(available) != 2 {
			t.Errorf("Expected 2 available 4-GPU partitions, got %d", len(available))
		}
	})

	t.Run("GetAvailablePartitions_All2GPUAvailable", func(t *testing.T) {
		available := tree.GetAvailablePartitions(2)
		if len(available) != 4 {
			t.Errorf("Expected 4 available 2-GPU partitions, got %d", len(available))
		}
	})

	t.Run("GetGPUsForPartition", func(t *testing.T) {
		gpus, err := tree.GetGPUsForPartition(1)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(gpus) != 4 {
			t.Errorf("Expected 4 GPUs, got %d", len(gpus))
		}
	})
}

// TestPartitionActivationBlocking tests that activating a partition blocks parent/child.
func TestPartitionActivationBlocking(t *testing.T) {
	t.Run("ActivateChildBlocksParent", func(t *testing.T) {
		tree := createTestTree()

		// Activate partition 1 (4 GPUs: 1,2,3,4)
		if err := tree.MarkActive(1, "pod-1"); err != nil {
			t.Fatalf("Failed to mark partition 1 active: %v", err)
		}

		// Partition 0 (8 GPUs, parent of 1) should be blocked
		available := tree.GetAvailablePartitions(8)
		if len(available) != 0 {
			t.Errorf("Expected 0 available 8-GPU partitions (blocked by child), got %d", len(available))
		}

		// Partition 2 should still be available (sibling, not blocked)
		available = tree.GetAvailablePartitions(4)
		if len(available) != 1 {
			t.Errorf("Expected 1 available 4-GPU partition, got %d", len(available))
		}
	})

	t.Run("ActivateParentBlocksChildren", func(t *testing.T) {
		tree := createTestTree()

		// Activate partition 0 (8 GPUs, root)
		if err := tree.MarkActive(0, "pod-root"); err != nil {
			t.Fatalf("Failed to mark partition 0 active: %v", err)
		}

		// All 4-GPU and 2-GPU partitions should be blocked
		available4 := tree.GetAvailablePartitions(4)
		if len(available4) != 0 {
			t.Errorf("Expected 0 available 4-GPU partitions (blocked by parent), got %d", len(available4))
		}

		available2 := tree.GetAvailablePartitions(2)
		if len(available2) != 0 {
			t.Errorf("Expected 0 available 2-GPU partitions (blocked by parent), got %d", len(available2))
		}
	})
}

// TestPartitionDeactivation tests marking partitions as inactive.
func TestPartitionDeactivation(t *testing.T) {
	t.Run("DeactivateReleasesParent", func(t *testing.T) {
		tree := createTestTree()

		// Activate partition 1
		tree.MarkActive(1, "pod-1")

		// Partition 0 blocked
		available := tree.GetAvailablePartitions(8)
		if len(available) != 0 {
			t.Errorf("Expected 0 available 8-GPU partitions, got %d", len(available))
		}

		// Deactivate partition 1
		tree.MarkInactive(1)

		// Partition 0 should now be available
		available = tree.GetAvailablePartitions(8)
		if len(available) != 1 {
			t.Errorf("Expected 1 available 8-GPU partition after deactivation, got %d", len(available))
		}
	})

	t.Run("GetPartitionForPodAfterDeactivation", func(t *testing.T) {
		tree := createTestTree()

		tree.MarkActive(1, "pod-test")

		// Pod should be tracked
		partitionID, found := tree.GetPartitionForPod("pod-test")
		if !found || partitionID != 1 {
			t.Errorf("Expected partition 1 for pod, got %d (found=%v)", partitionID, found)
		}

		// Deactivate
		tree.MarkInactive(1)

		// Pod should no longer be tracked
		_, found = tree.GetPartitionForPod("pod-test")
		if found {
			t.Errorf("Expected pod to be untracked after deactivation")
		}
	})
}

// TestFindPartitionForGPUs tests finding partitions by GPU BDFs.
func TestFindPartitionForGPUs(t *testing.T) {
	t.Run("FindPartitionByExactGPUs", func(t *testing.T) {
		tree := createTestTree()
		gpus := []string{"0000:09:00.0", "0000:17:00.0", "0000:3b:00.0", "0000:44:00.0"}
		partition, err := tree.FindPartitionForGPUs(gpus)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if partition == nil {
			t.Fatalf("Expected to find partition, got nil")
		}
		if partition.ID != 1 {
			t.Errorf("Expected partition 1, got %d", partition.ID)
		}
	})

	t.Run("FindPartitionBySubsetGPUs", func(t *testing.T) {
		tree := createTestTree()
		// Just 2 GPUs should find the 2-GPU partition
		gpus := []string{"0000:09:00.0", "0000:3b:00.0"}
		partition, err := tree.FindPartitionForGPUs(gpus)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if partition == nil {
			t.Fatalf("Expected to find partition, got nil")
		}
		// Should find partition 3 (GPUs 1,3)
		if partition.ID != 3 {
			t.Errorf("Expected partition 3, got %d", partition.ID)
		}
	})
}

// TestGetActivePartitions tests retrieving currently active partitions.
func TestGetActivePartitions(t *testing.T) {
	t.Run("NoActivePartitions", func(t *testing.T) {
		tree := createTestTree()
		active := tree.GetActivePartitions()
		if len(active) != 0 {
			t.Errorf("Expected 0 active partitions, got %d", len(active))
		}
	})

	t.Run("MultipleActivePartitions", func(t *testing.T) {
		tree := createTestTree()
		tree.MarkActive(3, "pod-1") // 2 GPUs
		tree.MarkActive(4, "pod-2") // 2 GPUs (sibling of 3)
		tree.MarkActive(2, "pod-3") // 4 GPUs

		active := tree.GetActivePartitions()
		if len(active) != 3 {
			t.Errorf("Expected 3 active partitions, got %d", len(active))
		}
	})
}

// TestSiblingConflicts tests that sibling partitions don't block each other.
func TestSiblingConflicts(t *testing.T) {
	t.Run("SiblingsDontBlockEachOther", func(t *testing.T) {
		tree := createTestTree()

		// Activate partition 3 (2 GPUs: 1,3)
		tree.MarkActive(3, "pod-a")

		// Partition 4 (2 GPUs: 2,4) should still be available
		available := tree.GetAvailablePartitions(2)

		foundP4 := false
		for _, p := range available {
			if p.ID == 4 {
				foundP4 = true
				break
			}
		}
		if !foundP4 {
			t.Errorf("Expected partition 4 to be available (sibling of 3)")
		}
	})
}

// TestOrphanedPartitionDetection tests detecting orphaned partitions
// that are active but not assigned to any pod.
func TestOrphanedPartitionDetection(t *testing.T) {
	t.Run("DetectOrphanedPartition", func(t *testing.T) {
		tree := createTestTree()

		// Manually mark partition as active (simulating FM sync)
		tree.partitions[1].IsActive = true

		// GetActivePartitions should return it
		active := tree.GetActivePartitions()
		if len(active) != 1 || active[0] != 1 {
			t.Errorf("Expected partition 1 in active list")
		}

		// But GetPartitionForPod should not find it (no pod tracking)
		_, found := tree.GetPartitionForPod("any-pod")
		if found {
			t.Errorf("Expected no pod tracking for orphaned partition")
		}
	})
}
