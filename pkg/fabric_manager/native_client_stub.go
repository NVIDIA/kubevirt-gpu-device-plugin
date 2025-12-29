//go:build !linux
// +build !linux

/*
 * Copyright (c) 2024, NVIDIA CORPORATION. All rights reserved.
 *
 * Stub implementation of NativeFMClient for non-Linux platforms (macOS, Windows).
 * This allows the code to compile for development/testing on non-Linux systems.
 * All operations return "not supported" errors.
 */

package fabric_manager

import (
	"fmt"
	"time"
)

// NativeFMClient is a stub implementation for non-Linux platforms.
type NativeFMClient struct{}

// NewNativeFMClient creates a new native FM client (stub).
func NewNativeFMClient() *NativeFMClient {
	return &NativeFMClient{}
}

// Connect returns not supported error on non-Linux.
func (c *NativeFMClient) Connect(address string, timeout time.Duration) error {
	return fmt.Errorf("FM SDK not available on this platform (non-Linux)")
}

// DiscoverPartitions returns not supported error on non-Linux.
func (c *NativeFMClient) DiscoverPartitions() ([]Partition, error) {
	return nil, fmt.Errorf("FM SDK not available on this platform (non-Linux)")
}

// DiscoverPCIMapping returns not supported error on non-Linux.
func (c *NativeFMClient) DiscoverPCIMapping() (map[int]string, error) {
	return nil, fmt.Errorf("FM SDK not available on this platform (non-Linux)")
}

// ActivatePartition returns not supported error on non-Linux.
func (c *NativeFMClient) ActivatePartition(partitionID int) error {
	return fmt.Errorf("FM SDK not available on this platform (non-Linux)")
}

// DeactivatePartition returns not supported error on non-Linux.
func (c *NativeFMClient) DeactivatePartition(partitionID int) error {
	return fmt.Errorf("FM SDK not available on this platform (non-Linux)")
}

// GetActivePartitions returns not supported error on non-Linux.
func (c *NativeFMClient) GetActivePartitions() ([]int, error) {
	return nil, fmt.Errorf("FM SDK not available on this platform (non-Linux)")
}

// Ensure NativeFMClient implements FMClient
var _ FMClient = (*NativeFMClient)(nil)
