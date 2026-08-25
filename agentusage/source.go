// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"sync"
	"time"
)

// usageSource reads usage for one review from somewhere other than an
// appendable transcript file. It exists for opencode, which keeps its sessions
// in a SQLite database rather than JSONL.
//
// The contract is the same as everywhere else here: report what the agent
// recorded for this directory since this moment, or report nothing.
//
// dirs holds the spellings the review's directory can appear under, resolved
// and unresolved, because an agent records whichever it was started with and
// on macOS every temporary path is a symlink to another.
type usageSource interface {
	read(dirs []string, since time.Time) (values, bool)
}

var (
	sourcesMu sync.RWMutex
	sources   = map[string]usageSource{}
)

func sourceFor(tool string) (usageSource, bool) {
	sourcesMu.RLock()
	defer sourcesMu.RUnlock()
	s, ok := sources[tool]
	return s, ok
}

// EnableOpenCodeDB turns reading of opencode's SQLite session store on or off.
//
// It is gated twice on purpose. The build tag `sqlite` decides whether the
// database driver is linked in at all, since it is a large dependency for one
// agent, and this switch decides whether a program that has it actually opens
// the operator's session database. Neither gate implies the other.
//
// It reports whether this build can read it: false means the binary was
// compiled without `-tags sqlite`, and nothing was enabled.
func EnableOpenCodeDB(on bool) bool { return setOpenCodeDB(on) }
