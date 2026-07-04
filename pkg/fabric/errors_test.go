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
	"strings"
	"testing"
)

func TestErrorForSuccessIsNil(t *testing.T) {
	if err := errorFor("fmLibInit", Success); err != nil {
		t.Fatalf("errorFor(Success) = %v, want nil", err)
	}
}

func TestErrorForSentinelMapping(t *testing.T) {
	tests := []struct {
		name     string
		code     Return
		sentinel error
	}{
		{"in use", ErrInUseCode, ErrInUse},
		{"partition id in use", ErrPartitionIDInUse, ErrInUse},
		{"resource used this partition", ErrResourceUsedInThisPartition, ErrInUse},
		{"uninitialized -> not active", ErrUninitialized, ErrPartitionNotActive},
		{"partition id not in use -> not active", ErrPartitionIDNotInUse, ErrPartitionNotActive},
		{"not configured -> not ready", ErrNotConfigured, ErrNotReadyYet},
		{"not ready -> not ready", ErrNotReady, ErrNotReadyYet},
		{"not supported -> fabric unsupported", ErrNotSupported, ErrFabricNotSupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := errorFor("op", tt.code)
			if err == nil {
				t.Fatalf("errorFor(%d) = nil, want error", int32(tt.code))
			}
			if !errors.Is(err, tt.sentinel) {
				t.Fatalf("errorFor(%d) not errors.Is %v", int32(tt.code), tt.sentinel)
			}
		})
	}
}

func TestErrorForNoSentinelStillErrors(t *testing.T) {
	err := errorFor("fmActivateFabricPartitionWithVFs", ErrNVLinkError)
	if err == nil {
		t.Fatal("errorFor(ErrNVLinkError) = nil, want error")
	}
	// No sentinel maps to a hard NVLink error, but the concrete *Error must
	// still carry the op and code.
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("errorFor did not return *Error, got %T", err)
	}
	if fe.Code != ErrNVLinkError || fe.Op != "fmActivateFabricPartitionWithVFs" {
		t.Fatalf("unexpected *Error contents: %+v", fe)
	}
	// A non-matching sentinel must NOT match.
	if errors.Is(err, ErrInUse) {
		t.Fatal("ErrNVLinkError should not match ErrInUse")
	}
}

func TestErrorMessageContainsOpAndCode(t *testing.T) {
	err := errorFor("fmConnect", ErrConnectionNotValid)
	msg := err.Error()
	for _, want := range []string{"fmConnect", "no longer valid", "-9"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

func TestReturnStringUnknownCode(t *testing.T) {
	got := Return(-9999).String()
	if !strings.Contains(got, "-9999") {
		t.Errorf("Return(-9999).String() = %q, want it to contain the numeric code", got)
	}
}

func TestReturnStringKnownCodes(t *testing.T) {
	// Every declared sentinel code must have a message (no accidental gaps).
	codes := []Return{
		Success, ErrBadParam, ErrGenericError, ErrNotSupported, ErrUninitialized,
		ErrTimeout, ErrVersionMismatch, ErrInUseCode, ErrNotConfigured,
		ErrConnectionNotValid, ErrNVLinkError, ErrPartitionIDNotInUse, ErrNotReady,
		ErrPartitionRouteProgrammingErr,
	}
	for _, c := range codes {
		if _, ok := returnMessages[c]; !ok {
			t.Errorf("Return code %d has no message in returnMessages", int32(c))
		}
	}
}
