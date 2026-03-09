//go:build nvfm

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
	"errors"
	"testing"
	"time"

	"kubevirt-gpu-device-plugin/pkg/nvfm"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected *Config
	}{
		{
			name:   "with nil config",
			config: nil,
			expected: &Config{
				AddressInfo: "localhost:6666",
				AddressType: AddressTypeInet,
				TimeoutMs:   5000,
				MaxRetries:  3,
				RetryDelay:  time.Second * 2,
				Debug:       false,
			},
		},
		{
			name: "with custom config",
			config: &Config{
				AddressInfo: "custom:8080",
				AddressType: AddressTypeUnix,
				TimeoutMs:   10000,
				MaxRetries:  5,
				RetryDelay:  time.Second * 3,
				Debug:       true,
			},
			expected: &Config{
				AddressInfo: "custom:8080",
				AddressType: AddressTypeUnix,
				TimeoutMs:   10000,
				MaxRetries:  5,
				RetryDelay:  time.Second * 3,
				Debug:       true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config).(*client)

			if client.config.AddressInfo != tt.expected.AddressInfo {
				t.Errorf("expected AddressInfo %s, got %s", tt.expected.AddressInfo, client.config.AddressInfo)
			}
			if client.config.AddressType != tt.expected.AddressType {
				t.Errorf("expected AddressType %v, got %v", tt.expected.AddressType, client.config.AddressType)
			}
			if client.config.TimeoutMs != tt.expected.TimeoutMs {
				t.Errorf("expected TimeoutMs %d, got %d", tt.expected.TimeoutMs, client.config.TimeoutMs)
			}
			if client.config.MaxRetries != tt.expected.MaxRetries {
				t.Errorf("expected MaxRetries %d, got %d", tt.expected.MaxRetries, client.config.MaxRetries)
			}
			if client.config.RetryDelay != tt.expected.RetryDelay {
				t.Errorf("expected RetryDelay %v, got %v", tt.expected.RetryDelay, client.config.RetryDelay)
			}
			if client.config.Debug != tt.expected.Debug {
				t.Errorf("expected Debug %v, got %v", tt.expected.Debug, client.config.Debug)
			}

			if client.connected {
				t.Error("expected client to not be connected initially")
			}
		})
	}
}

func TestClient_IsConnected(t *testing.T) {
	client := NewClient(nil).(*client)

	if client.IsConnected() {
		t.Error("expected client to not be connected initially")
	}

	client.connected = true
	if !client.IsConnected() {
		t.Error("expected client to be connected after setting connected=true")
	}
}

func TestClient_GetPartitions_NotConnected(t *testing.T) {
	client := NewClient(nil)
	ctx := context.Background()

	_, err := client.GetPartitions(ctx)

	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

func TestClient_GetPartition_NotFound(t *testing.T) {
	client := NewClient(nil).(*client)
	client.connected = true
	client.handle = &mockHandle{
		partitions: &nvfm.FabricPartitionList{
			NumPartitions:    1,
			MaxNumPartitions: 4,
			Partitions: []nvfm.PartitionInfo{
				{
					PartitionID: 1,
					IsActive:    false,
					NumGPUs:     2,
				},
			},
		},
	}

	ctx := context.Background()

	_, err := client.GetPartition(ctx, 999)

	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		t.Errorf("expected ClientError, got %T", err)
	} else if !errors.Is(clientErr.Err, ErrPartitionNotFound) {
		t.Errorf("expected ErrPartitionNotFound, got %v", clientErr.Err)
	}
}

func TestClient_ActivatePartition_InvalidRequest(t *testing.T) {
	client := NewClient(nil).(*client)
	client.connected = true

	ctx := context.Background()

	err := client.ActivatePartition(ctx, nil)

	var clientErr *ClientError
	if !errors.As(err, &clientErr) {
		t.Errorf("expected ClientError, got %T", err)
	} else if !errors.Is(clientErr.Err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", clientErr.Err)
	}
}

func TestClient_GetPartitionForDevices_NoDevices(t *testing.T) {
	client := NewClient(nil)
	ctx := context.Background()

	_, err := client.GetPartitionForDevices(ctx, []string{})

	if !errors.Is(err, ErrNoDevicesProvided) {
		t.Errorf("expected ErrNoDevicesProvided, got %v", err)
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "timeout error",
			err:      nvfm.Timeout,
			expected: true,
		},
		{
			name:     "connection not valid error",
			err:      nvfm.ConnectionNotValid,
			expected: true,
		},
		{
			name:     "not ready error",
			err:      nvfm.NotReady,
			expected: true,
		},
		{
			name:     "bad param error",
			err:      nvfm.BadParam,
			expected: false,
		},
		{
			name:     "wrapped timeout error",
			err:      &ClientError{Op: "test", Err: nvfm.Timeout},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsRetryable(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestIsPermanent(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "bad param error",
			err:      nvfm.BadParam,
			expected: true,
		},
		{
			name:     "not supported error",
			err:      nvfm.NotSupported,
			expected: true,
		},
		{
			name:     "not connected error",
			err:      ErrNotConnected,
			expected: true,
		},
		{
			name:     "partition not found error",
			err:      ErrPartitionNotFound,
			expected: true,
		},
		{
			name:     "timeout error",
			err:      nvfm.Timeout,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPermanent(tt.err)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// mockHandle is a mock implementation of nvfm.Handle for testing
type mockHandle struct {
	partitions *nvfm.FabricPartitionList
	activated  map[uint32]bool
}

func (m *mockHandle) GetSupportedFabricPartitions() (*nvfm.FabricPartitionList, error) {
	if m.partitions == nil {
		return nil, nvfm.GenericError
	}
	return m.partitions, nil
}

func (m *mockHandle) ActivateFabricPartition(partitionID uint32) error {
	if m.activated == nil {
		m.activated = make(map[uint32]bool)
	}
	m.activated[partitionID] = true
	return nil
}

func (m *mockHandle) ActivateFabricPartitionWithVFs(partitionID uint32, vfs []nvfm.PCIDevice) error {
	return m.ActivateFabricPartition(partitionID)
}

func (m *mockHandle) DeactivateFabricPartition(partitionID uint32) error {
	if m.activated == nil {
		m.activated = make(map[uint32]bool)
	}
	m.activated[partitionID] = false
	return nil
}

func (m *mockHandle) Disconnect() error {
	return nil
}


