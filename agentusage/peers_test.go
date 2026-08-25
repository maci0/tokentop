// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: AGPL-3.0-or-later

package agentusage

import (
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"
)

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

func TestSameEndpointTreatsLoopbackSpellingsAsOne(t *testing.T) {
	v4 := netip.MustParseAddrPort("127.0.0.1:11434")
	v6 := netip.MustParseAddrPort("[::1]:11434")
	other := netip.MustParseAddrPort("192.168.1.5:11434")
	wrongPort := netip.MustParseAddrPort("127.0.0.1:8080")

	if !sameEndpoint(v4, v6) {
		t.Error("loopback spellings are the same engine")
	}
	if sameEndpoint(v4, other) {
		t.Error("a remote host is not loopback")
	}
	if sameEndpoint(v4, wrongPort) {
		t.Error("a different port is a different engine")
	}
}

// TestConnectedToSeesARealConnection is the whole point of the file: an agent
// talking to an engine must be recognizable, so its tokens are not counted
// twice.
func TestConnectedToSeesARealConnection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/net/tcp")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			select {}
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	engine := netip.MustParseAddrPort(ln.Addr().String())
	pid := os.Getpid()
	if !ConnectedTo(pid, []netip.AddrPort{engine}) {
		t.Fatalf("a live connection to %s was not detected", engine)
	}

	// An endpoint nobody is talking to must not match, or every agent would
	// look engine-backed and report nothing.
	elsewhere := netip.MustParseAddrPort("127.0.0.1:1")
	if ConnectedTo(pid, []netip.AddrPort{elsewhere}) {
		t.Error("matched an endpoint with no connection")
	}
	if ConnectedTo(pid, nil) {
		t.Error("no endpoints means nothing to match")
	}
}

func TestPeersOnAnImpossiblePID(t *testing.T) {
	// Cannot tell is not the same as connected: a dead process must not be
	// reported as talking to anything.
	if got := Peers(-1); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if ConnectedTo(-1, []netip.AddrPort{netip.MustParseAddrPort("127.0.0.1:1")}) {
		t.Fatal("a dead process cannot hold a connection")
	}
}
