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

import "testing"

func TestParseBDF(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    BDF
		wantErr bool
	}{
		{
			name: "full form VF",
			in:   "0000:41:00.4",
			want: BDF{Domain: 0x0000, Bus: 0x41, Device: 0x00, Function: 0x4},
		},
		{
			name: "non-zero domain and hex bus",
			in:   "0001:ca:1f.7",
			want: BDF{Domain: 0x1, Bus: 0xca, Device: 0x1f, Function: 0x7},
		},
		{
			name: "short form defaults domain to 0",
			in:   "41:00.4",
			want: BDF{Domain: 0, Bus: 0x41, Device: 0x00, Function: 0x4},
		},
		{
			name: "ARI function above 7",
			in:   "0000:41:00.a",
			want: BDF{Domain: 0, Bus: 0x41, Device: 0, Function: 0xa},
		},
		{
			name: "surrounding whitespace tolerated",
			in:   "  0000:41:00.4\n",
			want: BDF{Domain: 0, Bus: 0x41, Device: 0, Function: 0x4},
		},
		{name: "empty", in: "", wantErr: true},
		{name: "missing function", in: "0000:41:00", wantErr: true},
		{name: "too few colon fields", in: "00.4", wantErr: true},
		{name: "too many colon fields", in: "0:0:0:0.0", wantErr: true},
		{name: "non-hex bus", in: "0000:zz:00.4", wantErr: true},
		{name: "device out of range", in: "0000:41:20.0", wantErr: true},
		{name: "bus out of range", in: "0000:100:00.0", wantErr: true},
		{name: "empty function", in: "0000:41:00.", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBDF(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseBDF(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBDF(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseBDF(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBDFString(t *testing.T) {
	tests := []struct {
		in   BDF
		want string
	}{
		{BDF{0, 0x41, 0, 4}, "0000:41:00.4"},
		{BDF{1, 0xca, 0x1f, 7}, "0001:ca:1f.7"},
		{BDF{0, 0x41, 0, 0xa}, "0000:41:00.a"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("BDF%+v.String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseBDFRoundTrip(t *testing.T) {
	for _, s := range []string{"0000:41:00.4", "0001:ca:1f.7", "0000:81:00.5"} {
		b, err := ParseBDF(s)
		if err != nil {
			t.Fatalf("ParseBDF(%q): %v", s, err)
		}
		if got := b.String(); got != s {
			t.Errorf("round trip %q -> %+v -> %q", s, b, got)
		}
	}
}

func TestMustParseBDFPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustParseBDF did not panic on invalid input")
		}
	}()
	_ = MustParseBDF("not-a-bdf")
}
