//go:build !nvfm

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
	"time"
)

var ErrNotSupported = errors.New("fabricmanager: package built without nvfm support")

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

// Client provides a stub interface when nvfm support is not available.
type Client interface {
	Connect(ctx context.Context) error
	Disconnect() error
	IsConnected() bool
	GetPartitions(ctx context.Context) (*PartitionList, error)
	GetPartition(ctx context.Context, partitionID uint32) (*Partition, error)
	ActivatePartition(ctx context.Context, req *ActivateRequest) error
	DeactivatePartition(ctx context.Context, partitionID uint32) error
	GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*Partition, error)
}

// Config contains configuration options for the fabric manager client.
type Config struct {
	AddressInfo string
	AddressType AddressType
	TimeoutMs   uint32
	MaxRetries  int
	RetryDelay  time.Duration
	Debug       bool
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *Config {
	return &Config{
		AddressInfo: "localhost:6666",
		AddressType: AddressTypeInet,
		TimeoutMs:   5000,
		MaxRetries:  3,
		RetryDelay:  time.Second * 2,
		Debug:       false,
	}
}

// Stub types
type (
	GPU struct {
		PhysicalID          uint32
		UUID                string
		PCIBusID            string
		NumNVLinksAvailable uint32
		MaxNumNVLinks       uint32
		NVLinkLineRateMBps  uint32
	}

	Partition struct {
		PartitionID     uint32
		IsActive        bool
		NumGPUs         uint32
		GPUs            []GPU
		LastActivated   time.Time
		LastDeactivated time.Time
		ActivationCount int
	}

	PartitionList struct {
		NumPartitions    uint32
		MaxNumPartitions uint32
		Partitions       []Partition
	}

	ActivateRequest struct {
		PartitionID uint32
		VFDevices   []PCIDevice
	}

	PCIDevice struct {
		Domain   uint32
		Bus      uint32
		Device   uint32
		Function uint32
	}

	ClientError struct {
		Op      string
		Err     error
		Details string
	}
)

func (e *ClientError) Error() string {
	if e.Details != "" {
		return "fabricmanager: " + e.Op + " failed: " + e.Err.Error() + " (" + e.Details + ")"
	}
	return "fabricmanager: " + e.Op + " failed: " + e.Err.Error()
}

func (e *ClientError) Unwrap() error {
	return e.Err
}

var (
	ErrNotConnected          = errors.New("not connected to fabric manager")
	ErrPartitionNotFound     = errors.New("partition not found")
	ErrNoDevicesProvided     = errors.New("no device IDs provided")
	ErrInvalidRequest        = errors.New("invalid request")
	ErrPartitionNotAvailable = errors.New("no partition available for devices")
)

// Stub client implementation
type stubClient struct{}

func NewClient(config *Config) Client {
	return &stubClient{}
}

func (c *stubClient) Connect(ctx context.Context) error                                 { return ErrNotSupported }
func (c *stubClient) Disconnect() error                                                { return ErrNotSupported }
func (c *stubClient) IsConnected() bool                                                { return false }
func (c *stubClient) GetPartitions(ctx context.Context) (*PartitionList, error)       { return nil, ErrNotSupported }
func (c *stubClient) GetPartition(ctx context.Context, partitionID uint32) (*Partition, error) { return nil, ErrNotSupported }
func (c *stubClient) ActivatePartition(ctx context.Context, req *ActivateRequest) error { return ErrNotSupported }
func (c *stubClient) DeactivatePartition(ctx context.Context, partitionID uint32) error { return ErrNotSupported }
func (c *stubClient) GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*Partition, error) { return nil, ErrNotSupported }

func IsRetryable(err error) bool {
	return false
}

func IsPermanent(err error) bool {
	return true
}