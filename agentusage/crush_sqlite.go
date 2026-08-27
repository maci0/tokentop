// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build sqlite

package agentusage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"
)

// crush keeps its sessions in SQLite like opencode, but inside the project it
// is working on rather than under $HOME: `.crush/crush.db`, at the project
// root crush resolves (the git root, the way its own config discovery does).
//
//	sessions.completion_tokens  what the model generated, which is output here
//	sessions.prompt_tokens      input, which this package never counts
//	sessions.updated_at         when the row last changed, which bounds the review
//
// That last column carries two units. The schema comments call it
// milliseconds, and crush writes milliseconds from Go, but the table's own
// update trigger writes `strftime('%s','now')`, which is seconds: a row can
// hold either depending on who touched it last. The query normalizes per row
// rather than trusting the comment, because a mismatch here does not read
// slightly wrong, it reads zero.
//
// The only JSONL crush writes is `.crush/logs/crush.log`, and it carries no
// counters, so there is nothing for the file adapters to tail.
//
// The database being inside the reviewed tree makes the directory the query
// bound on its own: there is no cross-project store to filter, so a session
// written while the review ran is that review's. It is registered without an
// opt-in switch for the same reason, unlike opencode's operator-wide store.
type crushDBSource struct{}

func init() { registerSource("crush", crushDBSource{}) }

const (
	// crushMaxWalkUp bounds the search for the project root, so a review of a
	// directory outside any project cannot walk to /.
	crushMaxWalkUp = 16
)

// read sums what crush recorded for this directory since the review began.
func (crushDBSource) read(dirs []string, since time.Time) (values, bool) {
	seen := map[string]bool{}
	var v values
	for _, dir := range dirs {
		path := crushDBPath(dir)
		if path == "" || seen[path] {
			continue // two spellings of one directory share one database
		}
		seen[path] = true
		if out, ok := readCrushDB(path, since); ok {
			v.output += out
		}
	}
	return v, v.output > 0
}

// crushDBPath finds the database crush would use for a directory, walking up
// to the project root as crush does. It returns "" when there is none, which
// is the common case: most machines have never run crush. The path is
// returned with symlinks resolved, so the spellings one directory is watched
// under (on macOS always more than one) name it identically.
func crushDBPath(dir string) string {
	cur, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for range crushMaxWalkUp {
		candidate := filepath.Join(cur, ".crush", "crush.db")
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
				return resolved
			}
			return candidate
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
	return ""
}

// crushUsageQuery sums the generated tokens of every session written since a
// point in time. Prompt tokens are input, which this package never reports.
const crushUsageQuery = `
	SELECT COALESCE(SUM(completion_tokens), 0)
	FROM sessions
	WHERE (CASE WHEN updated_at > 100000000000 THEN updated_at ELSE updated_at * 1000 END) >= ?`

func readCrushDB(path string, since time.Time) (int, bool) {
	// mode=ro leaves the database alone; a missing or unreadable one is an
	// answer ("nothing to report"), not an error worth surfacing.
	db, err := openReadOnly(path)
	if err != nil {
		return 0, false
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var out sql.NullInt64
	if err := db.QueryRowContext(ctx, crushUsageQuery, since.UnixMilli()).Scan(&out); err != nil {
		return 0, false
	}
	n := counter64(out.Int64)
	if n <= 0 {
		return 0, false
	}
	return n, true
}

const crushSessionsQuery = `
	SELECT id, completion_tokens
	FROM sessions`

// sessions returns each database's current per-session completion tokens.
// Watch snapshots this at attach so a continued session contributes only
// what it adds afterwards, matching the file adapters. A missing or
// unreadable store is not an error: most trees have never run crush.
func (crushDBSource) sessions(dirs []string) (map[string]map[string]int64, bool) {
	seen := map[string]bool{}
	out := map[string]map[string]int64{}
	for _, dir := range dirs {
		path := crushDBPath(dir)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		sess, ok := readCrushSessions(path)
		if !ok {
			return nil, false
		}
		out[path] = sess
	}
	return out, true
}

func readCrushSessions(path string) (map[string]int64, bool) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, false
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, crushSessionsQuery)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var id string
		var n sql.NullInt64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, false
		}
		if v := counter64(n.Int64); v > 0 {
			out[id] = int64(v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false
	}
	return out, true
}
