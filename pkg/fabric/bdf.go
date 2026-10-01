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
	"fmt"
	"strconv"
	"strings"
)

// BDF is a PCI device address broken into its Domain:Bus:Device.Function
// components, matching the fmPciDevice_t struct the Fabric Manager SDK expects
// for VF lists. All fields are the raw numeric values (not hex strings).
type BDF struct {
	Domain   uint32
	Bus      uint32
	Device   uint32
	Function uint32
}

// ParseBDF parses a Linux PCI address such as "0000:41:00.4" (the form used in
// sysfs and reported by NVML/kubelet) into a BDF. The domain, bus and device
// fields are hexadecimal; the function field is hexadecimal too (0-7 for
// non-ARI, up to 0xff for ARI). A missing domain (e.g. "41:00.4") defaults to
// domain 0, matching lspci's short form.
func ParseBDF(s string) (BDF, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return BDF{}, fmt.Errorf("fabric: empty PCI address")
	}

	// Split the function off first: "<...>.<function>".
	dot := strings.LastIndex(s, ".")
	if dot < 0 {
		return BDF{}, fmt.Errorf("fabric: invalid PCI address %q: missing '.function'", orig)
	}
	fnPart := s[dot+1:]
	rest := s[:dot]

	// rest is either "domain:bus:device" or "bus:device" (domain defaults to 0).
	parts := strings.Split(rest, ":")
	var domainPart, busPart, devicePart string
	switch len(parts) {
	case 3:
		domainPart, busPart, devicePart = parts[0], parts[1], parts[2]
	case 2:
		domainPart, busPart, devicePart = "0", parts[0], parts[1]
	default:
		return BDF{}, fmt.Errorf("fabric: invalid PCI address %q: want domain:bus:device.function", orig)
	}

	domain, err := parseHexField(domainPart, "domain", orig)
	if err != nil {
		return BDF{}, err
	}
	bus, err := parseHexField(busPart, "bus", orig)
	if err != nil {
		return BDF{}, err
	}
	device, err := parseHexField(devicePart, "device", orig)
	if err != nil {
		return BDF{}, err
	}
	function, err := parseHexField(fnPart, "function", orig)
	if err != nil {
		return BDF{}, err
	}

	if bus > 0xff {
		return BDF{}, fmt.Errorf("fabric: invalid PCI address %q: bus %#x out of range", orig, bus)
	}
	if device > 0x1f {
		return BDF{}, fmt.Errorf("fabric: invalid PCI address %q: device %#x out of range", orig, device)
	}
	if function > 0xff {
		return BDF{}, fmt.Errorf("fabric: invalid PCI address %q: function %#x out of range", orig, function)
	}

	return BDF{Domain: domain, Bus: bus, Device: device, Function: function}, nil
}

func parseHexField(field, name, orig string) (uint32, error) {
	if field == "" {
		return 0, fmt.Errorf("fabric: invalid PCI address %q: empty %s", orig, name)
	}
	v, err := strconv.ParseUint(field, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("fabric: invalid PCI address %q: bad %s: %w", orig, name, err)
	}
	return uint32(v), nil
}

// String renders the BDF back into the canonical Linux "0000:41:00.4" form.
func (b BDF) String() string {
	return fmt.Sprintf("%04x:%02x:%02x.%x", b.Domain, b.Bus, b.Device, b.Function)
}

// MustParseBDF is ParseBDF that panics on error. Intended for tests and for
// compile-time-constant addresses only.
func MustParseBDF(s string) BDF {
	b, err := ParseBDF(s)
	if err != nil {
		panic(err)
	}
	return b
}
