// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"net/netip"
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
// One implementation per platform: peers_linux.go reads the kernel's tables,
// peers_darwin.go asks lsof(8), and platforms where neither interface exists
// report nothing. Best effort throughout: an empty result means "cannot
// tell", which a caller should treat as "assume it is not the same engine"
// rather than as a statement about the process.

// parseLsofPeers reads remote endpoints out of `lsof -FnP -n` output, one
// field per line prefixed with its type letter. Connection lines carry both
// ends as "n127.0.0.1:52154->127.0.0.1:11434"; listening sockets and UDP
// endpoints have no "->" remote or none that parses, and are skipped.
func parseLsofPeers(out string) []netip.AddrPort {
	seen := map[netip.AddrPort]bool{}
	var peers []netip.AddrPort
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) < 2 || line[0] != 'n' {
			continue // process headers and non-name fields
		}
		_, remote, ok := strings.Cut(line[1:], "->")
		if !ok {
			continue // listening, or otherwise without a remote end
		}
		ap, err := netip.ParseAddrPort(remote)
		if err != nil {
			continue
		}
		if !seen[ap] {
			seen[ap] = true
			peers = append(peers, ap)
		}
	}
	return peers
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
