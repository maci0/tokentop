// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !linux && !darwin

package agentusage

import "net/netip"

// Peers lists the TCP endpoints a process is connected to.
//
// An empty result means "cannot tell", which a caller should treat as
// "assume it is not the same engine" rather than as a statement about the
// process. Platforms other than Linux and macOS report nothing: Windows
// would need GetExtendedTcpTable plus owner-PID rows, which this package
// has no native binding for yet.
func Peers(int) []netip.AddrPort { return nil }
