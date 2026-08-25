// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !sqlite

package agentusage

import (
	"testing"
	"time"
)

// Without the driver linked in, asking for opencode's database must say so
// rather than quietly reporting nothing forever.
func TestOpenCodeDBNeedsTheSqliteBuild(t *testing.T) {
	if EnableOpenCodeDB(true) {
		t.Fatal("a build without -tags sqlite claimed it could read the database")
	}
	if Supported("opencode") {
		t.Fatal("opencode is not readable in this build")
	}
	if w := Watch("opencode", t.TempDir(), time.Now()); w != nil {
		t.Fatal("a watcher was built for an unreadable agent")
	}
}
