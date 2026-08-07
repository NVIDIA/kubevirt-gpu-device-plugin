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

package fabric

import (
	"errors"
	"fmt"
)

// Return is a Fabric Manager API return code (fmReturn_t). The zero value is
// Success. Values mirror the fmReturn_enum in nv_fm_types.h exactly.
type Return int32

// Fabric Manager return codes (fmReturn_enum from nv_fm_types.h).
const (
	Success                         Return = 0
	ErrBadParam                     Return = -1
	ErrGenericError                 Return = -2
	ErrNotSupported                 Return = -3
	ErrUninitialized                Return = -4
	ErrTimeout                      Return = -5
	ErrVersionMismatch              Return = -6
	ErrInUseCode                    Return = -7
	ErrNotConfigured                Return = -8
	ErrConnectionNotValid           Return = -9
	ErrNVLinkError                  Return = -10
	ErrResourceBad                  Return = -11
	ErrResourceInUse                Return = -12
	ErrResourceNotInUse             Return = -13
	ErrResourceExhausted            Return = -14
	ErrResourceNotReady             Return = -15
	ErrPartitionExists              Return = -16
	ErrPartitionIDInUse             Return = -17
	ErrPartitionIDNotInUse          Return = -18
	ErrPartitionNameInUse           Return = -19
	ErrPartitionNameNotInUse        Return = -20
	ErrPartitionIDNameMismatch      Return = -21
	ErrNotReady                     Return = -22
	ErrResourceUsedInThisPartition  Return = -23
	ErrResourceUsedInAnotherPart    Return = -24
	ErrPartitionMiswiredTrunks      Return = -25
	ErrPartitionInsufficientTrunks  Return = -26
	ErrPartitionMissingSwitches     Return = -27
	ErrPartitionNetworkConfigError  Return = -28
	ErrPartitionRouteProgrammingErr Return = -29
)

// returnMessages maps every known Return code to a human-readable message. Kept
// in one table so String() and the Error type stay in sync.
var returnMessages = map[Return]string{
	Success:                         "success",
	ErrBadParam:                     "a supplied argument is invalid",
	ErrGenericError:                 "a generic, unspecified error",
	ErrNotSupported:                 "the requested operation/feature is not supported",
	ErrUninitialized:                "object is in an undefined/uninitialized state",
	ErrTimeout:                      "the requested operation timed out",
	ErrVersionMismatch:              "version mismatch between the library and the daemon",
	ErrInUseCode:                    "the resource is in use",
	ErrNotConfigured:                "setting not configured (Fabric Manager may still be initializing)",
	ErrConnectionNotValid:           "the connection to the Fabric Manager instance is no longer valid",
	ErrNVLinkError:                  "the operation failed due to an NVLink error",
	ErrResourceBad:                  "a referenced resource does not exist",
	ErrResourceInUse:                "a referenced resource is already in use",
	ErrResourceNotInUse:             "a referenced resource is not in use",
	ErrResourceExhausted:            "a resource could not be allocated",
	ErrResourceNotReady:             "a resource is not ready to be used",
	ErrPartitionExists:              "partition already created",
	ErrPartitionIDInUse:             "partition id already used by another partition",
	ErrPartitionIDNotInUse:          "partition id could not be found",
	ErrPartitionNameInUse:           "partition name already used by another partition",
	ErrPartitionNameNotInUse:        "partition name could not be found",
	ErrPartitionIDNameMismatch:      "partition id and name refer to different partitions",
	ErrNotReady:                     "Fabric Manager is not ready to serve requests",
	ErrResourceUsedInThisPartition:  "resource is already in use in this partition",
	ErrResourceUsedInAnotherPart:    "resource is already in use in another partition",
	ErrPartitionMiswiredTrunks:      "partition has miswired trunks",
	ErrPartitionInsufficientTrunks:  "partition has insufficient trunks",
	ErrPartitionMissingSwitches:     "partition has missing switches",
	ErrPartitionNetworkConfigError:  "partition has a network configuration error",
	ErrPartitionRouteProgrammingErr: "partition has a route programming error",
}

// String returns a human-readable description of the return code.
func (r Return) String() string {
	if msg, ok := returnMessages[r]; ok {
		return msg
	}
	return fmt.Sprintf("unknown fabric manager error (%d)", int32(r))
}

// Sentinel errors for the return codes the device plugin's allocation logic
// branches on. Compare with errors.Is against the error returned by a Client
// method, e.g. errors.Is(err, fabric.ErrInUse) to detect an already-active
// partition during idempotent (re)activation.
var (
	// ErrInUse indicates the partition is already active (FM_ST_IN_USE),
	// returned by ActivateWithVFs when the partition was not deactivated first.
	ErrInUse = errors.New("fabric partition already active")
	// ErrPartitionNotActive indicates a deactivate targeted a partition that
	// is not currently active. Fabric Manager reports this as FM_ST_UNINITIALIZED
	// (see fmDeactivateFabricPartition) or FM_ST_PARTITION_ID_NOT_IN_USE.
	ErrPartitionNotActive = errors.New("fabric partition not active")
	// ErrNotReadyYet indicates Fabric Manager is still initializing and the
	// call should be retried (FM_ST_NOT_CONFIGURED / FM_ST_NOT_READY).
	ErrNotReadyYet = errors.New("fabric manager not ready")
	// ErrFabricNotSupported indicates the system/mode does not support fabric
	// partitions (FM_ST_NOT_SUPPORTED), e.g. FM is not in FABRIC_MODE=2 or the
	// platform has no NVSwitch. Activation should be skipped, not failed hard.
	ErrFabricNotSupported = errors.New("fabric partitions not supported on this system")
)

// Error is a Fabric Manager API error carrying the operation that failed and
// the underlying return code. It unwraps to the relevant sentinel (ErrInUse,
// ErrPartitionNotActive, ...) so callers can branch with errors.Is.
type Error struct {
	// Op is the FM API call that failed, e.g. "fmActivateFabricPartitionWithVFs".
	Op string
	// Code is the return code fmReturn_t the call produced.
	Code Return
}

func (e *Error) Error() string {
	return fmt.Sprintf("fabric: %s: %s (code %d)", e.Op, e.Code.String(), int32(e.Code))
}

// Unwrap maps the return code to a sentinel error so errors.Is works for the
// codes the allocation logic cares about. Codes without a sentinel unwrap to
// nil, and errors.Is still matches on the concrete *Error if the caller wants.
func (e *Error) Unwrap() error {
	switch e.Code {
	case ErrInUseCode, ErrPartitionIDInUse, ErrResourceUsedInThisPartition, ErrResourceUsedInAnotherPart:
		return ErrInUse
	case ErrUninitialized, ErrPartitionIDNotInUse, ErrResourceNotInUse:
		return ErrPartitionNotActive
	case ErrNotConfigured, ErrNotReady, ErrResourceNotReady:
		return ErrNotReadyYet
	case ErrNotSupported:
		return ErrFabricNotSupported
	default:
		return nil
	}
}

// errorFor converts an FM return code into an error, returning nil for Success.
func errorFor(op string, code Return) error {
	if code == Success {
		return nil
	}
	return &Error{Op: op, Code: code}
}
