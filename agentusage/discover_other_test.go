// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !linux && !darwin && !windows

package agentusage

import "testing"

func TestDiscoverReportsNothing(t *testing.T) {
	if got := Discover(); got != nil {
		t.Fatalf("Discover() = %v, want nil: this platform cannot read a process cwd", got)
	}
}
