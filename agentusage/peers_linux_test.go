// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build linux

package agentusage

import "testing"

func TestParseHexAddrPort(t *testing.T) {
	cases := map[string]string{
		// The kernel writes each 32-bit word little endian: 0100007F is
		// 127.0.0.1, and 1F90 is port 8080.
		"0100007F:1F90":                         "127.0.0.1:8080",
		"0100007F:2CAA":                         "127.0.0.1:11434",
		"00000000:0050":                         "0.0.0.0:80",
		"00000000000000000000000001000000:1F90": "[::1]:8080",
	}
	for in, want := range cases {
		got, ok := parseHexAddrPort(in)
		if !ok {
			t.Errorf("%s: not parsed", in)
			continue
		}
		if got.String() != want {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{"", "nocolon", "ZZ:1F90", "0100007F:ZZZZ", "01:1F90"} {
		if _, ok := parseHexAddrPort(bad); ok {
			t.Errorf("%q should not parse", bad)
		}
	}
}
