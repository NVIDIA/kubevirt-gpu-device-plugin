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
	"errors"
	"fmt"

	"kubevirt-gpu-device-plugin/pkg/nvfm"
)

var (
	// ErrNotConnected indicates the client is not connected to fabric manager.
	ErrNotConnected = errors.New("not connected to fabric manager")

	// ErrPartitionNotFound indicates the requested partition was not found.
	ErrPartitionNotFound = errors.New("partition not found")

	// ErrNoDevicesProvided indicates no device IDs were provided.
	ErrNoDevicesProvided = errors.New("no device IDs provided")

	// ErrInvalidRequest indicates an invalid request parameter.
	ErrInvalidRequest = errors.New("invalid request")

	// ErrPartitionNotAvailable indicates the partition is not available for the requested devices.
	ErrPartitionNotAvailable = errors.New("no partition available for devices")
)

// ClientError wraps fabric manager errors with additional context.
type ClientError struct {
	Op      string // Operation that failed
	Err     error  // Underlying error
	Details string // Additional details
}

func (e *ClientError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("fabricmanager: %s failed: %s (%s)", e.Op, e.Err.Error(), e.Details)
	}
	return fmt.Sprintf("fabricmanager: %s failed: %s", e.Op, e.Err.Error())
}

func (e *ClientError) Unwrap() error {
	return e.Err
}

// newClientError creates a new ClientError.
func newClientError(op string, err error, details string) error {
	return &ClientError{
		Op:      op,
		Err:     err,
		Details: details,
	}
}

// IsRetryable checks if an error is retryable.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		err = clientErr.Err
	}

	// Check nvfm errors that are retryable
	if nvfmErr, ok := err.(nvfm.Return); ok {
		switch nvfmErr {
		case nvfm.Timeout:
			return true
		case nvfm.ConnectionNotValid:
			return true
		case nvfm.NotReady:
			return true
		case nvfm.ResourceNotReady:
			return true
		default:
			return false
		}
	}

	return false
}

// IsPermanent checks if an error is permanent and should not be retried.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}

	var clientErr *ClientError
	if errors.As(err, &clientErr) {
		err = clientErr.Err
	}

	// Check nvfm errors that are permanent
	if nvfmErr, ok := err.(nvfm.Return); ok {
		switch nvfmErr {
		case nvfm.BadParam:
			return true
		case nvfm.NotSupported:
			return true
		case nvfm.VersionMismatch:
			return true
		case nvfm.NotConfigured:
			return true
		default:
			return false
		}
	}

	// Check client-side errors
	if errors.Is(err, ErrNotConnected) ||
		errors.Is(err, ErrPartitionNotFound) ||
		errors.Is(err, ErrNoDevicesProvided) ||
		errors.Is(err, ErrInvalidRequest) ||
		errors.Is(err, ErrPartitionNotAvailable) {
		return true
	}

	return false
}
