// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build sqlite

package agentusage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver: no cgo, so cross-compilation still works
)

// registerSource makes an agent readable through a source instead of files.
// Passing nil removes it, which is how the runtime switch turns one off.
func registerSource(tool string, s any) {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	if s == nil {
		delete(sources, tool)
		return
	}
	sources[tool] = s
}

const (
	// dbQueryTimeout bounds a single read against an agent database. The
	// dashboard polls several times a second; a hung or recovering store
	// must not freeze it. A timed-out read keeps the last sample.
	dbQueryTimeout = time.Second
	// dbBusyTimeout is how long a reader waits for a writer. Failing the
	// read keeps the last sample rather than inventing a zero.
	dbBusyTimeout = 250 * time.Millisecond
)

// readOnlyDSN builds the read-only URI DSN for a session database. SQLite
// parses file: URIs itself, so the three characters that carry URI syntax must
// travel percent-encoded inside the path, as its own documentation requires;
// anything else, Windows drive letters and separators included, passes through
// untouched. A raw % would fail the parse outright, and ? or # would end the
// filename early, either way reading nothing.
//
// The query parameters pin the connection so a live store cannot be written:
// mode=ro is the file-open flag, _query_only rejects write statements, and
// _defensive turns off the SQL-level knobs that can rewrite the file. A short
// busy timeout waits out a writer instead of failing on the first lock.
func readOnlyDSN(path string) string {
	var b strings.Builder
	b.WriteString("file:")
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '%':
			b.WriteString("%25")
		case '?':
			b.WriteString("%3F")
		case '#':
			b.WriteString("%23")
		default:
			b.WriteByte(path[i])
		}
	}
	fmt.Fprintf(&b, "?mode=ro&_query_only=1&_busy_timeout=%d&_defensive=1",
		dbBusyTimeout.Milliseconds())
	return b.String()
}

// openReadOnly opens a session database for a single short read. The pool is
// one connection: SQLite serializes writers, and this process never writes.
func openReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}
