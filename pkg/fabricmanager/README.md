# Fabric Manager Client

This package provides a high-level client for managing NVIDIA Fabric Manager partitions. It is designed to integrate with the KubeVirt GPU device plugin to coordinate GPU device allocations with fabric manager partition management.

## Overview

The Fabric Manager Client abstracts the low-level `nvfm` package (NVIDIA Fabric Manager SDK CGO bindings) and provides:

- High-level partition management operations
- Connection handling with retry logic
- Structured error handling
- Thread-safe operations for concurrent device allocation

## Architecture

```
┌─────────────────────┐
│   Device Plugin     │
│   (Allocate/Free)   │
└─────────┬───────────┘
          │
          │ Uses
          │
┌─────────▼───────────┐
│ Fabric Manager      │  ← This package
│ Client              │
└─────────┬───────────┘
          │
          │ Uses
          │
┌─────────▼───────────┐
│   nvfm Package      │  ← FM SDK bindings
│   (CGO bindings)    │
└─────────────────────┘
```

## Basic Usage

### Creating a Client

```go
package main

import (
    "context"

    "kubevirt-gpu-device-plugin/pkg/fabricmanager"
)

func main() {
    // Use default configuration
    client := fabricmanager.NewClient(nil)

    // Or customize configuration
    config := &fabricmanager.Config{
        AddressInfo: "localhost:6666",
        AddressType: AddressTypeInet,
        TimeoutMs:   10000,
        MaxRetries:  5,
        Debug:       true,
    }
    client = fabricmanager.NewClient(config)

    ctx := context.Background()

    // Connect to fabric manager
    if err := client.Connect(ctx); err != nil {
        log.Fatalf("Failed to connect: %v", err)
    }
    defer client.Disconnect()
}
```

### Managing Partitions

```go
// Get all available partitions
partitions, err := client.GetPartitions(ctx)
if err != nil {
    return err
}

// Find partition for specific GPU devices
deviceIDs := []string{"0000:01:00.0", "0000:02:00.0"}
partition, err := client.GetPartitionForDevices(ctx, deviceIDs)
if err != nil {
    return err
}

// Activate a partition
req := &fabricmanager.ActivateRequest{
    PartitionID: partition.PartitionID,
}
if err := client.ActivatePartition(ctx, req); err != nil {
    return err
}

// Deactivate when done
if err := client.DeactivatePartition(ctx, partition.PartitionID); err != nil {
    return err
}
```

## Device Plugin Integration

### Integration Points

The fabric manager client should be integrated at these points in the device plugin lifecycle:

1. **Initialization**: Create and connect the fabric manager client
2. **Device Allocation** (`Allocate()` method): Activate appropriate partition
3. **Device Cleanup**: Deactivate partitions when devices are released
4. **Shutdown**: Disconnect from fabric manager

### Example Integration

```go
// In device_plugin/generic_device_plugin.go

import (
    "kubevirt-gpu-device-plugin/pkg/fabricmanager"
)

type GenericDevicePlugin struct {
    // ... existing fields ...
    fmClient fabricmanager.Client
}

func NewGenericDevicePlugin(deviceName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
    // ... existing code ...

    // Initialize fabric manager client
    fmClient := fabricmanager.NewClient(nil)

    dpi := &GenericDevicePlugin{
        // ... existing fields ...
        fmClient: fmClient,
    }
    return dpi
}

func (dpi *GenericDevicePlugin) Start(stop chan struct{}) error {
    // ... existing start logic ...

    // Connect to fabric manager
    ctx := context.Background()
    if err := dpi.fmClient.Connect(ctx); err != nil {
        log.Printf("[%s] Warning: Could not connect to fabric manager: %v", dpi.deviceName, err)
        // Continue without fabric manager - graceful degradation
    }

    // ... rest of start logic ...
}

func (dpi *GenericDevicePlugin) Stop() error {
    // ... existing stop logic ...

    // Disconnect from fabric manager
    if dpi.fmClient != nil {
        dpi.fmClient.Disconnect()
    }

    return dpi.cleanup()
}

func (dpi *GenericDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
    // ... existing allocation logic ...

    // Extract device IDs from the request
    var allDeviceIDs []string
    for _, req := range reqs.ContainerRequests {
        allDeviceIDs = append(allDeviceIDs, req.DevicesIDs...)
    }

    // Activate fabric partition if fabric manager is available
    if dpi.fmClient != nil && dpi.fmClient.IsConnected() {
        if err := dpi.activateFabricPartition(ctx, allDeviceIDs); err != nil {
            log.Fatalf("[%s]: Failed to activate fabric manager partition: %v", dpi.deviceName, err)
        }
    }

    // ... rest of allocation logic ...
}

func (dpi *GenericDevicePlugin) activateFabricPartition(ctx context.Context, deviceIDs []string) error {
    if len(deviceIDs) == 0 {
        return nil
    }

    // Find appropriate partition for the devices
    partition, err := dpi.fmClient.GetPartitionForDevices(ctx, deviceIDs)
    if err != nil {
        if fabricmanager.IsPermanent(err) {
            return err // Don't retry permanent errors
        }
        return err
    }

    // Activate the partition
    req := &fabricmanager.ActivateRequest{
        PartitionID: partition.PartitionID,
    }

    return dpi.fmClient.ActivatePartition(ctx, req)
}
```

### Error Handling Strategy

The client provides structured error handling to help with integration decisions:

```go
// Check if an error is retryable
if fabricmanager.IsRetryable(err) {
    // Implement retry logic
    return retryOperation()
}

// Check if an error is permanent
if fabricmanager.IsPermanent(err) {
    // Don't retry, handle gracefully
    log.Printf("Permanent error, continuing without fabric manager: %v", err)
    return nil
}
```

## Configuration

### Environment Variables

The fabric manager client can be configured via environment variables:

- `FABRIC_MANAGER_ADDRESS`: Address of fabric manager instance
- `FABRIC_MANAGER_TIMEOUT`: Connection timeout in milliseconds
- `FABRIC_MANAGER_MAX_RETRIES`: Maximum number of connection retries

### Build Tags

The client depends on the `nvfm` package which uses build tags:

- Build with `nvfm` tag when fabric manager libraries are available
- Build without tag for stub implementations

```bash
# Build with fabric manager support
go build -tags=nvfm ./cmd/main.go

# Build without fabric manager support (stub)
go build ./cmd/main.go
```

## Thread Safety

The client is thread-safe and can be used concurrently from multiple goroutines. Internal operations are protected by read-write mutexes.

## Testing

The package includes comprehensive unit tests. For integration testing with the device plugin:

```go
// Mock the fabric manager client for testing
type mockFabricManagerClient struct {
    partitions map[string]*fabricmanager.Partition
}

func (m *mockFabricManagerClient) GetPartitionForDevices(ctx context.Context, deviceIDs []string) (*fabricmanager.Partition, error) {
    // Mock implementation
}
```

## Dependencies

- `kubevirt-gpu-device-plugin/pkg/nvfm`: NVIDIA Fabric Manager SDK CGO bindings
- Standard library packages: `context`, `sync`, `time`, `fmt`, `errors`

## License

Copyright (c) 2026, NVIDIA CORPORATION. See license header in source files.
