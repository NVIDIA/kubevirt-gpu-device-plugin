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

package fabricmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockFMClient is a mock implementation of Client for testing.
type mockFMClient struct {
	partitions     *PartitionList
	err            error
	activatedID    uint32
	activateErr    error
	deactivatedIDs []uint32
	deactivateErr  error
	connected      bool
}

func (m *mockFMClient) Connect(ctx context.Context) error { return nil }
func (m *mockFMClient) Disconnect() error                 { return nil }
func (m *mockFMClient) IsConnected() bool                 { return m.connected }
func (m *mockFMClient) GetPartition(ctx context.Context, partitionID uint32) (*Partition, error) {
	return nil, nil
}

func (m *mockFMClient) ActivatePartition(ctx context.Context, req *ActivateRequest) error {
	m.activatedID = req.PartitionID
	return m.activateErr
}

func (m *mockFMClient) DeactivatePartition(ctx context.Context, partitionID uint32) error {
	if m.deactivateErr != nil {
		return m.deactivateErr
	}
	m.deactivatedIDs = append(m.deactivatedIDs, partitionID)
	return nil
}

func (m *mockFMClient) GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*Partition, error) {
	return nil, nil
}

func (m *mockFMClient) GetPartitions(ctx context.Context) (*PartitionList, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.partitions, nil
}

func TestLoadPCIModuleMapping(t *testing.T) {
	t.Run("parses a valid mapping file", func(t *testing.T) {
		tmpDir := t.TempDir()
		mappingFile := filepath.Join(tmpDir, "mapping.json")
		content := `{
			"0000:3b:00.0": "0",
			"0000:86:00.0": "1",
			"0000:af:00.0": "2",
			"0000:d8:00.0": "3"
		}`
		if err := os.WriteFile(mappingFile, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write mapping file: %v", err)
		}

		pciToModule, moduleToPCI, err := LoadPCIModuleMapping(mappingFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pciToModule) != 4 {
			t.Errorf("expected 4 entries in pciToModule, got %d", len(pciToModule))
		}
		if len(moduleToPCI) != 4 {
			t.Errorf("expected 4 entries in moduleToPCI, got %d", len(moduleToPCI))
		}

		expectedPCI := map[string]uint32{
			"0000:3b:00.0": 0,
			"0000:86:00.0": 1,
			"0000:af:00.0": 2,
			"0000:d8:00.0": 3,
		}
		for addr, expected := range expectedPCI {
			if got := pciToModule[addr]; got != expected {
				t.Errorf("pciToModule[%s] = %d, want %d", addr, got, expected)
			}
		}

		expectedModule := map[uint32]string{
			0: "0000:3b:00.0",
			1: "0000:86:00.0",
			2: "0000:af:00.0",
			3: "0000:d8:00.0",
		}
		for id, expected := range expectedModule {
			if got := moduleToPCI[id]; got != expected {
				t.Errorf("moduleToPCI[%d] = %s, want %s", id, got, expected)
			}
		}
	})

	t.Run("normalizes 8-digit domain and uppercase hex to sysfs format", func(t *testing.T) {
		tmpDir := t.TempDir()
		mappingFile := filepath.Join(tmpDir, "mapping.json")
		content := `{
			"00000000:18:00.0": "2",
			"00000000:2A:00.0": "4",
			"00000000:3A:00.0": "1",
			"00000000:5D:00.0": "3",
			"00000000:9A:00.0": "6",
			"00000000:AB:00.0": "8",
			"00000000:BA:00.0": "5",
			"00000000:DB:00.0": "7"
		}`
		if err := os.WriteFile(mappingFile, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write mapping file: %v", err)
		}

		pciToModule, moduleToPCI, err := LoadPCIModuleMapping(mappingFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Keys should be normalized to 4-digit lowercase domain
		expectedPCI := map[string]uint32{
			"0000:18:00.0": 2,
			"0000:2a:00.0": 4,
			"0000:3a:00.0": 1,
			"0000:5d:00.0": 3,
			"0000:9a:00.0": 6,
			"0000:ab:00.0": 8,
			"0000:ba:00.0": 5,
			"0000:db:00.0": 7,
		}
		for addr, expected := range expectedPCI {
			got, ok := pciToModule[addr]
			if !ok {
				t.Errorf("pciToModule missing normalized key %s", addr)
				continue
			}
			if got != expected {
				t.Errorf("pciToModule[%s] = %d, want %d", addr, got, expected)
			}
		}

		// Reverse map values should also be normalized
		for moduleID, expectedAddr := range map[uint32]string{
			2: "0000:18:00.0",
			4: "0000:2a:00.0",
			1: "0000:3a:00.0",
			3: "0000:5d:00.0",
			6: "0000:9a:00.0",
			8: "0000:ab:00.0",
			5: "0000:ba:00.0",
			7: "0000:db:00.0",
		} {
			if got := moduleToPCI[moduleID]; got != expectedAddr {
				t.Errorf("moduleToPCI[%d] = %s, want %s", moduleID, got, expectedAddr)
			}
		}
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		_, _, err := LoadPCIModuleMapping(filepath.Join(tmpDir, "nonexistent.json"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to read PCI module mapping file") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		mappingFile := filepath.Join(tmpDir, "bad.json")
		if err := os.WriteFile(mappingFile, []byte("not json"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		_, _, err := LoadPCIModuleMapping(mappingFile)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to parse PCI module mapping JSON") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("returns error for invalid module ID value", func(t *testing.T) {
		tmpDir := t.TempDir()
		mappingFile := filepath.Join(tmpDir, "bad-id.json")
		content := `{"0000:3b:00.0": "not-a-number"}`
		if err := os.WriteFile(mappingFile, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		_, _, err := LoadPCIModuleMapping(mappingFile)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "invalid module ID") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestSelectPreferred(t *testing.T) {
	pciToModuleID := map[string]uint32{
		"0000:3b:00.0": 0,
		"0000:86:00.0": 1,
		"0000:af:00.0": 2,
		"0000:d8:00.0": 3,
	}
	moduleIDToPCI := map[uint32]string{
		0: "0000:3b:00.0",
		1: "0000:86:00.0",
		2: "0000:af:00.0",
		3: "0000:d8:00.0",
	}
	deviceNUMAMap := map[string]int64{
		"0000:3b:00.0": 0,
		"0000:86:00.0": 0,
		"0000:af:00.0": 1,
		"0000:d8:00.0": 1,
	}

	partitions := &PartitionList{
		NumPartitions:    2,
		MaxNumPartitions: 4,
		Partitions: []Partition{
			{
				PartitionID: 0,
				NumGPUs:     2,
				GPUs: []GPU{
					{PhysicalID: 0, PCIBusID: ""},
					{PhysicalID: 1, PCIBusID: ""},
				},
			},
			{
				PartitionID: 1,
				NumGPUs:     2,
				GPUs: []GPU{
					{PhysicalID: 2, PCIBusID: ""},
					{PhysicalID: 3, PCIBusID: ""},
				},
			},
		},
	}

	t.Run("selects partition matching available devices by PhysicalID", func(t *testing.T) {
		mock := &mockFMClient{connected: true, partitions: partitions}
		pm := NewPartitionManager(mock, pciToModuleID, moduleIDToPCI, deviceNUMAMap)

		preferred, err := pm.SelectPreferred(context.Background(), &SelectPreferredRequest{
			AvailableDeviceIDs: []string{
				"0000:3b:00.0", "0000:86:00.0", "0000:af:00.0", "0000:d8:00.0",
			},
			AllocationSize: 2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(preferred) != 2 {
			t.Fatalf("expected 2 preferred devices, got %d", len(preferred))
		}
		// Should pick partition 0 (NUMA node 0) since it has better locality
		expectedSet := map[string]struct{}{
			"0000:3b:00.0": {},
			"0000:86:00.0": {},
		}
		for _, dev := range preferred {
			if _, ok := expectedSet[dev]; !ok {
				t.Errorf("unexpected device in preferred list: %s", dev)
			}
		}
	})

	t.Run("respects must-include devices", func(t *testing.T) {
		mock := &mockFMClient{connected: true, partitions: partitions}
		pm := NewPartitionManager(mock, pciToModuleID, moduleIDToPCI, deviceNUMAMap)

		preferred, err := pm.SelectPreferred(context.Background(), &SelectPreferredRequest{
			AvailableDeviceIDs: []string{
				"0000:3b:00.0", "0000:86:00.0", "0000:af:00.0", "0000:d8:00.0",
			},
			MustIncludeDeviceIDs: []string{"0000:af:00.0"},
			AllocationSize:       2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(preferred) != 2 {
			t.Fatalf("expected 2 preferred devices, got %d", len(preferred))
		}
		// Must include 0000:af:00.0 (module 2), so partition 1 is selected
		found := map[string]bool{}
		for _, dev := range preferred {
			found[dev] = true
		}
		if !found["0000:af:00.0"] {
			t.Error("expected 0000:af:00.0 in preferred list")
		}
		if !found["0000:d8:00.0"] {
			t.Error("expected 0000:d8:00.0 in preferred list")
		}
	})

	t.Run("returns error when no partition matches", func(t *testing.T) {
		mock := &mockFMClient{connected: true, partitions: partitions}
		pm := NewPartitionManager(mock, pciToModuleID, moduleIDToPCI, deviceNUMAMap)

		_, err := pm.SelectPreferred(context.Background(), &SelectPreferredRequest{
			AvailableDeviceIDs: []string{"0000:3b:00.0"},
			AllocationSize:     2,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no fabric manager partition") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("filters out partitions when not all GPUs are available", func(t *testing.T) {
		mock := &mockFMClient{connected: true, partitions: partitions}
		pm := NewPartitionManager(mock, pciToModuleID, moduleIDToPCI, deviceNUMAMap)

		preferred, err := pm.SelectPreferred(context.Background(), &SelectPreferredRequest{
			// Only 3 devices available — partition 1 has modules 2,3 but module 3
			// (0000:d8:00.0) is not available, so only partition 0 qualifies
			AvailableDeviceIDs: []string{
				"0000:3b:00.0", "0000:86:00.0", "0000:af:00.0",
			},
			AllocationSize: 2,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(preferred) != 2 {
			t.Fatalf("expected 2 preferred devices, got %d", len(preferred))
		}
		expectedSet := map[string]struct{}{
			"0000:3b:00.0": {},
			"0000:86:00.0": {},
		}
		for _, dev := range preferred {
			if _, ok := expectedSet[dev]; !ok {
				t.Errorf("unexpected device in preferred list: %s", dev)
			}
		}
	})
}

func TestActivateForDevices(t *testing.T) {
	t.Run("finds and activates the correct partition by PhysicalID", func(t *testing.T) {
		mock := &mockFMClient{
			connected: true,
			partitions: &PartitionList{
				NumPartitions: 2,
				Partitions: []Partition{
					{
						PartitionID: 0,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
						},
					},
					{
						PartitionID: 1,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 2, PCIBusID: ""},
							{PhysicalID: 3, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:af:00.0": 2,
				"0000:d8:00.0": 3,
			},
			map[uint32]string{
				2: "0000:af:00.0",
				3: "0000:d8:00.0",
			},
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:af:00.0", "0000:d8:00.0"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if mock.activatedID != 1 {
			t.Errorf("expected partition 1 to be activated, got %d", mock.activatedID)
		}
	})

	t.Run("returns error when PCI address has no module mapping", func(t *testing.T) {
		pm := NewPartitionManager(nil, map[string]uint32{}, nil, nil)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:ff:00.0"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no module ID mapping") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("returns error when no partition matches the devices", func(t *testing.T) {
		mock := &mockFMClient{
			connected: true,
			partitions: &PartitionList{
				NumPartitions: 1,
				Partitions: []Partition{
					{
						PartitionID: 0,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:af:00.0": 2,
				"0000:d8:00.0": 3,
			},
			nil,
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:af:00.0", "0000:d8:00.0"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no partition of size") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("deactivates active partition containing allocated devices before activation", func(t *testing.T) {
		mock := &mockFMClient{
			connected: true,
			partitions: &PartitionList{
				NumPartitions: 2,
				Partitions: []Partition{
					{
						PartitionID: 0,
						IsActive:    true,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
						},
					},
					{
						PartitionID: 1,
						IsActive:    false,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 2, PCIBusID: ""},
							{PhysicalID: 3, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:3b:00.0": 0,
				"0000:86:00.0": 1,
			},
			map[uint32]string{
				0: "0000:3b:00.0",
				1: "0000:86:00.0",
			},
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:3b:00.0", "0000:86:00.0"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.deactivatedIDs) != 1 || mock.deactivatedIDs[0] != 0 {
			t.Errorf("expected partition 0 to be deactivated, got %v", mock.deactivatedIDs)
		}
		if mock.activatedID != 0 {
			t.Errorf("expected partition 0 to be activated, got %d", mock.activatedID)
		}
	})

	t.Run("does not deactivate inactive partitions", func(t *testing.T) {
		mock := &mockFMClient{
			connected: true,
			partitions: &PartitionList{
				NumPartitions: 2,
				Partitions: []Partition{
					{
						PartitionID: 0,
						IsActive:    false,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
						},
					},
					{
						PartitionID: 1,
						IsActive:    false,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 2, PCIBusID: ""},
							{PhysicalID: 3, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:af:00.0": 2,
				"0000:d8:00.0": 3,
			},
			map[uint32]string{
				2: "0000:af:00.0",
				3: "0000:d8:00.0",
			},
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:af:00.0", "0000:d8:00.0"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.deactivatedIDs) != 0 {
			t.Errorf("expected no deactivations, got %v", mock.deactivatedIDs)
		}
		if mock.activatedID != 1 {
			t.Errorf("expected partition 1 to be activated, got %d", mock.activatedID)
		}
	})

	t.Run("returns error when deactivation fails", func(t *testing.T) {
		mock := &mockFMClient{
			connected:     true,
			deactivateErr: fmt.Errorf("deactivation refused"),
			partitions: &PartitionList{
				NumPartitions: 1,
				Partitions: []Partition{
					{
						PartitionID: 0,
						IsActive:    true,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:3b:00.0": 0,
				"0000:86:00.0": 1,
			},
			nil,
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:3b:00.0", "0000:86:00.0"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to deactivate partition 0") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("does not deactivate active partition with no overlapping devices", func(t *testing.T) {
		mock := &mockFMClient{
			connected: true,
			partitions: &PartitionList{
				NumPartitions: 2,
				Partitions: []Partition{
					{
						PartitionID: 0,
						IsActive:    true,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
						},
					},
					{
						PartitionID: 1,
						IsActive:    false,
						NumGPUs:     2,
						GPUs: []GPU{
							{PhysicalID: 2, PCIBusID: ""},
							{PhysicalID: 3, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:af:00.0": 2,
				"0000:d8:00.0": 3,
			},
			map[uint32]string{
				2: "0000:af:00.0",
				3: "0000:d8:00.0",
			},
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:af:00.0", "0000:d8:00.0"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(mock.deactivatedIDs) != 0 {
			t.Errorf("expected no deactivations, got %v", mock.deactivatedIDs)
		}
		if mock.activatedID != 1 {
			t.Errorf("expected partition 1 to be activated, got %d", mock.activatedID)
		}
	})

	t.Run("rejects partition containing all devices but with mismatched size", func(t *testing.T) {
		mock := &mockFMClient{
			connected: true,
			partitions: &PartitionList{
				NumPartitions: 1,
				Partitions: []Partition{
					{
						PartitionID: 0,
						NumGPUs:     4,
						GPUs: []GPU{
							{PhysicalID: 0, PCIBusID: ""},
							{PhysicalID: 1, PCIBusID: ""},
							{PhysicalID: 2, PCIBusID: ""},
							{PhysicalID: 3, PCIBusID: ""},
						},
					},
				},
			},
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:3b:00.0": 0,
				"0000:86:00.0": 1,
			},
			nil,
			nil,
		)

		err := pm.ActivateForDevices(context.Background(), []string{"0000:3b:00.0", "0000:86:00.0"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "no partition of size 2") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("returns nil for empty device list", func(t *testing.T) {
		pm := NewPartitionManager(nil, nil, nil, nil)

		err := pm.ActivateForDevices(context.Background(), []string{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("serializes concurrent calls", func(t *testing.T) {
		mock := &sequencingMockFMClient{
			partitions: &PartitionList{
				NumPartitions: 2,
				Partitions: []Partition{
					{PartitionID: 0, NumGPUs: 2, GPUs: []GPU{{PhysicalID: 0}, {PhysicalID: 1}}},
					{PartitionID: 1, NumGPUs: 2, GPUs: []GPU{{PhysicalID: 2}, {PhysicalID: 3}}},
				},
			},
			delay: 10 * time.Millisecond,
		}

		pm := NewPartitionManager(mock,
			map[string]uint32{
				"0000:3b:00.0": 0, "0000:86:00.0": 1,
				"0000:af:00.0": 2, "0000:d8:00.0": 3,
			},
			map[uint32]string{
				0: "0000:3b:00.0", 1: "0000:86:00.0",
				2: "0000:af:00.0", 3: "0000:d8:00.0",
			},
			nil,
		)

		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = pm.ActivateForDevices(context.Background(), []string{"0000:3b:00.0", "0000:86:00.0"})
		}()
		go func() {
			defer wg.Done()
			errs[1] = pm.ActivateForDevices(context.Background(), []string{"0000:af:00.0", "0000:d8:00.0"})
		}()
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				t.Fatalf("goroutine %d returned error: %v", i, err)
			}
		}

		ops := mock.getOps()
		if len(ops) != 4 {
			t.Fatalf("expected 4 operations, got %d: %v", len(ops), ops)
		}

		// With serialization, operations are grouped per call:
		//   [GetPartitions, Activate(X), GetPartitions, Activate(Y)]
		// Without serialization, they would interleave:
		//   [GetPartitions, GetPartitions, Activate(X), Activate(Y)]
		for i := 0; i < len(ops)-1; i++ {
			if ops[i] == "GetPartitions" && ops[i+1] == "GetPartitions" {
				t.Errorf("detected interleaved operations (consecutive GetPartitions at positions %d-%d), want serialized sequences: %v", i, i+1, ops)
			}
		}
	})
}

// sequencingMockFMClient is a thread-safe mock that records the order of FM
// operations and supports an artificial delay in GetPartitions to widen the
// race window for concurrent calls.
type sequencingMockFMClient struct {
	mu         sync.Mutex
	ops        []string
	partitions *PartitionList
	delay      time.Duration
}

func (m *sequencingMockFMClient) Connect(ctx context.Context) error   { return nil }
func (m *sequencingMockFMClient) Disconnect() error                   { return nil }
func (m *sequencingMockFMClient) IsConnected() bool                   { return true }
func (m *sequencingMockFMClient) GetPartition(ctx context.Context, id uint32) (*Partition, error) {
	return nil, nil
}
func (m *sequencingMockFMClient) GetPartitionForDevices(ctx context.Context, ids []string) (*Partition, error) {
	return nil, nil
}

func (m *sequencingMockFMClient) GetPartitions(ctx context.Context) (*PartitionList, error) {
	m.mu.Lock()
	m.ops = append(m.ops, "GetPartitions")
	m.mu.Unlock()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.partitions, nil
}

func (m *sequencingMockFMClient) ActivatePartition(ctx context.Context, req *ActivateRequest) error {
	m.mu.Lock()
	m.ops = append(m.ops, fmt.Sprintf("Activate(%d)", req.PartitionID))
	m.mu.Unlock()
	return nil
}

func (m *sequencingMockFMClient) DeactivatePartition(ctx context.Context, id uint32) error {
	m.mu.Lock()
	m.ops = append(m.ops, fmt.Sprintf("Deactivate(%d)", id))
	m.mu.Unlock()
	return nil
}

func (m *sequencingMockFMClient) getOps() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.ops))
	copy(result, m.ops)
	return result
}
