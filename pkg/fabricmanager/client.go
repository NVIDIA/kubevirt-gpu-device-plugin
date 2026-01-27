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
	"fmt"
	"sync"
	"time"

	"kubevirt-gpu-device-plugin/pkg/nvfm"
)

// Handle interface abstracts the NVFM handle operations for testing.
type Handle interface {
	GetSupportedFabricPartitions() (*nvfm.FabricPartitionList, error)
	ActivateFabricPartition(partitionID uint32) error
	ActivateFabricPartitionWithVFs(partitionID uint32, vfs []nvfm.PCIDevice) error
	DeactivateFabricPartition(partitionID uint32) error
	Disconnect() error
}

// Client provides a high-level interface for managing fabric manager partitions
// used by the device plugin to coordinate GPU allocations with fabric manager.
type Client interface {
	// Connect establishes connection to the fabric manager.
	Connect(ctx context.Context) error

	// Disconnect closes the connection to fabric manager.
	Disconnect() error

	// IsConnected returns true if connected to fabric manager.
	IsConnected() bool

	// GetPartitions retrieves all supported fabric partitions.
	GetPartitions(ctx context.Context) (*PartitionList, error)

	// GetPartition retrieves information about a specific partition.
	GetPartition(ctx context.Context, partitionID uint32) (*Partition, error)

	// ActivatePartition activates a fabric partition for the given devices.
	ActivatePartition(ctx context.Context, req *ActivateRequest) error

	// DeactivatePartition deactivates a fabric partition.
	DeactivatePartition(ctx context.Context, partitionID uint32) error

	// GetPartitionForDevices finds the appropriate partition for the given GPU devices.
	GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*Partition, error)
}

// AddressType represents the address type for fabric manager connections.
type AddressType int

const (
	// AddressTypeInet represents TCP/IP connections.
	AddressTypeInet AddressType = iota
	// AddressTypeUnix represents Unix domain socket connections.
	AddressTypeUnix
	// AddressTypeVsock represents VSOCK connections.
	AddressTypeVsock
)

// Config contains configuration options for the fabric manager client.
type Config struct {
	// Address information for connecting to fabric manager.
	AddressInfo string

	// Address type (INET, UNIX, VSOCK).
	AddressType AddressType

	// Connection timeout in milliseconds.
	TimeoutMs uint32

	// Number of connection retry attempts.
	MaxRetries int

	// Delay between retry attempts.
	RetryDelay time.Duration

	// Enable debug logging.
	Debug bool
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *Config {
	return &Config{
		AddressInfo: "localhost:6666", // Default fabric manager address
		AddressType: AddressTypeInet,
		TimeoutMs:   5000,
		MaxRetries:  3,
		RetryDelay:  time.Second * 2,
		Debug:       false,
	}
}

// client is the concrete implementation of the Client interface.
type client struct {
	config    *Config
	handle    Handle
	connected bool
	mutex     sync.RWMutex
}

// NewClient creates a new fabric manager client with the given configuration.
func NewClient(config *Config) Client {
	if config == nil {
		config = DefaultConfig()
	}

	return &client{
		config:    config,
		connected: false,
	}
}

// toNVFMAddressType converts fabricmanager.AddressType to nvfm.AddressType.
func toNVFMAddressType(addrType AddressType) nvfm.AddressType {
	switch addrType {
	case AddressTypeInet:
		return nvfm.AddressTypeInet
	case AddressTypeUnix:
		return nvfm.AddressTypeUnix
	case AddressTypeVsock:
		return nvfm.AddressTypeVsock
	default:
		return nvfm.AddressTypeInet // fallback to default
	}
}

// Connect establishes connection to the fabric manager.
func (c *client) Connect(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.connected {
		return nil
	}

	// Initialize fabric manager library
	if err := nvfm.Init(); err != nil {
		return newClientError("connect", err, "failed to initialize fabric manager library")
	}

	connectParams := nvfm.ConnectParams{
		AddressInfo: c.config.AddressInfo,
		AddressType: toNVFMAddressType(c.config.AddressType),
		TimeoutMs:   c.config.TimeoutMs,
	}

	var handle *nvfm.Handle
	var err error

	// Retry connection with exponential backoff
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		handle, err = nvfm.Connect(connectParams)
		if err == nil {
			break
		}

		if attempt < c.config.MaxRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.config.RetryDelay * time.Duration(attempt+1)):
			}
		}
	}

	if err != nil {
		nvfm.Shutdown()
		return newClientError("connect", err, fmt.Sprintf("failed after %d attempts", c.config.MaxRetries+1))
	}

	c.handle = handle
	c.connected = true

	return nil
}

// Disconnect closes the connection to fabric manager.
func (c *client) Disconnect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if !c.connected {
		return nil
	}

	var err error
	if c.handle != nil {
		err = c.handle.Disconnect()
		c.handle = nil
	}

	nvfm.Shutdown()
	c.connected = false

	return err
}

// IsConnected returns true if connected to fabric manager.
func (c *client) IsConnected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.connected
}

// GetPartitions retrieves all supported fabric partitions.
func (c *client) GetPartitions(ctx context.Context) (*PartitionList, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if !c.connected {
		return nil, ErrNotConnected
	}

	nvfmPartitions, err := c.handle.GetSupportedFabricPartitions()
	if err != nil {
		return nil, newClientError("get_partitions", err, "")
	}

	return fromNVFMPartitionList(nvfmPartitions), nil
}

// GetPartition retrieves information about a specific partition.
func (c *client) GetPartition(ctx context.Context, partitionID uint32) (*Partition, error) {
	partitions, err := c.GetPartitions(ctx)
	if err != nil {
		return nil, err
	}

	for _, partition := range partitions.Partitions {
		if partition.PartitionID == partitionID {
			return &partition, nil
		}
	}

	return nil, newClientError("get_partition", ErrPartitionNotFound, fmt.Sprintf("partition ID: %d", partitionID))
}

// ActivatePartition activates a fabric partition for the given devices.
func (c *client) ActivatePartition(ctx context.Context, req *ActivateRequest) error {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if !c.connected {
		return ErrNotConnected
	}

	if req == nil {
		return newClientError("activate_partition", ErrInvalidRequest, "activation request cannot be nil")
	}

	var err error
	if len(req.VFDevices) > 0 {
		nvfmVFs := toNVFMPCIDevices(req.VFDevices)
		err = c.handle.ActivateFabricPartitionWithVFs(req.PartitionID, nvfmVFs)
	} else {
		err = c.handle.ActivateFabricPartition(req.PartitionID)
	}

	if err != nil {
		return newClientError("activate_partition", err, fmt.Sprintf("partition ID: %d", req.PartitionID))
	}

	return nil
}

// DeactivatePartition deactivates a fabric partition.
func (c *client) DeactivatePartition(ctx context.Context, partitionID uint32) error {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if !c.connected {
		return ErrNotConnected
	}

	err := c.handle.DeactivateFabricPartition(partitionID)
	if err != nil {
		return newClientError("deactivate_partition", err, fmt.Sprintf("partition ID: %d", partitionID))
	}

	return nil
}

// GetPartitionForDevices finds the appropriate partition for the given GPU devices
// This method analyzes the device IDs (PCI BDF addresses) and finds a partition
// that contains GPUs matching those addresses.
func (c *client) GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*Partition, error) {
	if len(deviceIDs) == 0 {
		return nil, ErrNoDevicesProvided
	}

	partitions, err := c.GetPartitions(ctx)
	if err != nil {
		return nil, err
	}

	// Convert device IDs to a set for faster lookup
	deviceSet := make(map[string]struct{})
	for _, deviceID := range deviceIDs {
		deviceSet[deviceID] = struct{}{}
	}

	// Find partitions that contain all requested devices
	for _, partition := range partitions.Partitions {
		matchedDevices := 0

		for _, gpu := range partition.GPUs {
			if _, exists := deviceSet[gpu.PCIBusID]; exists {
				matchedDevices++
			}
		}

		// If all requested devices are found in this partition, return it
		if matchedDevices == len(deviceIDs) {
			return &partition, nil
		}
	}

	return nil, newClientError("get_partition_for_devices", ErrPartitionNotAvailable, fmt.Sprintf("devices: %v", deviceIDs))
}
