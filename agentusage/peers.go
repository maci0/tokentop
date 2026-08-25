// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: AGPL-3.0-or-later

package agentusage

import (
	"bufio"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Peers lists the TCP endpoints a process is connected to.
//
// It exists to answer one question a token monitor has to get right: is this
// agent generating through an engine that is already being measured? An agent
// pointed at a local llama.cpp or vLLM produces tokens the engine reports too,
// so counting both doubles the number. The connection is the evidence, and it
// needs no configuration or guesswork about model names.
//
// Linux only, and best effort: an empty result means "cannot tell", which a
// caller should treat as "assume it is not the same engine" rather than as a
// statement about the process.
func Peers(pid int) []netip.AddrPort {
	inodes := socketInodes(pid)
	if len(inodes) == 0 {
		return nil
	}
	byInode := map[uint64]netip.AddrPort{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		readTCPTable(table, inodes, byInode)
	}
	out := make([]netip.AddrPort, 0, len(byInode))
	for _, ap := range byInode {
		out = append(out, ap)
	}
	return out
}

// socketInodes collects the socket inodes a process holds open.
func socketInodes(pid int) map[uint64]bool {
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // exited, or another user's process
	}
	out := make(map[uint64]bool, len(entries))
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		// "socket:[12345]" is the only shape that matters here.
		rest, ok := strings.CutPrefix(target, "socket:[")
		if !ok {
			continue
		}
		num, ok := strings.CutSuffix(rest, "]")
		if !ok {
			continue
		}
		if inode, err := strconv.ParseUint(num, 10, 64); err == nil {
			out[inode] = true
		}
	}
	return out
}

// readTCPTable pulls the remote address of every connection whose inode the
// caller cares about. The kernel's table is fixed-width text: local address,
// remote address, state, and further along, the inode.
func readTCPTable(path string, want map[uint64]bool, into map[uint64]netip.AddrPort) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// sl local rem st tx:rx retr uid timeout inode
		if len(fields) < 10 {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || !want[inode] {
			continue
		}
		if ap, ok := parseHexAddrPort(fields[2]); ok {
			into[inode] = ap
		}
	}
}

// parseHexAddrPort decodes the kernel's "0100007F:1F90" spelling, which is the
// address in native byte order followed by the port.
func parseHexAddrPort(s string) (netip.AddrPort, bool) {
	host, port, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	// Each 32-bit word is little endian on the platforms this runs on, so the
	// bytes of every word are reversed to get network order.
	for i := 0; i+4 <= len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(p)), true
}

// ConnectedTo reports whether a process holds a connection to any of the given
// endpoints, which is how a monitor decides that an agent's tokens are already
// being counted somewhere else.
//
// Endpoints are matched on port plus address, with loopback spellings treated
// as equal: an engine advertised as 127.0.0.1:11434 and a connection to
// ::1:11434 are the same engine.
func ConnectedTo(pid int, endpoints []netip.AddrPort) bool {
	if len(endpoints) == 0 {
		return false
	}
	peers := Peers(pid)
	for _, p := range peers {
		for _, e := range endpoints {
			if sameEndpoint(p, e) {
				return true
			}
		}
	}
	return false
}

func sameEndpoint(a, b netip.AddrPort) bool {
	if a.Port() != b.Port() {
		return false
	}
	x, y := a.Addr().Unmap(), b.Addr().Unmap()
	if x == y {
		return true
	}
	return x.IsLoopback() && y.IsLoopback()
}
