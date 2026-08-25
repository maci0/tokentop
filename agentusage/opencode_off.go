// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build !sqlite

package agentusage

// setOpenCodeDB reports that this build has no SQLite driver linked in, so
// opencode's session database cannot be read however loudly it is asked for.
func setOpenCodeDB(bool) bool { return false }
