// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !linux && !darwin

package agentusage

import "net/netip"

// Peers reports nothing where no documented interface lists a process's
// connections (Windows would need GetExtendedTcpTable plus owner-PID rows,
// which this package has no native binding for yet). Callers treat the empty
// result as "cannot tell" and assume the agent is not engine-backed.
func Peers(int) []netip.AddrPort { return nil }
