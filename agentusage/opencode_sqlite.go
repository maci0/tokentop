// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build sqlite

package agentusage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// opencode keeps its sessions in SQLite rather than the JSONL every other
// agent here writes: ~/.local/share/opencode/opencode.db, with one row per
// message and the usage in a JSON column.
//
//	session.directory  the working directory, which is the attribution key
//	message.data       {"role":"assistant","tokens":{"output":324,"reasoning":52,…}}
//	message.time_created  milliseconds, which bounds the review
//
// The database is opened read-only for each reading and closed again, so a
// long-lived dashboard never holds a handle on a database the agent is
// writing. A query costs single-digit milliseconds even on a store tens of
// gigabytes large, because it is answered through the (session_id,
// time_created) index rather than a scan.
type openCodeDBSource struct{ path string }

// registerSource makes an agent readable through a source instead of files.
// Passing nil removes it, which is how the runtime switch turns one off.
func registerSource(tool string, s usageSource) {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	if s == nil {
		delete(sources, tool)
		return
	}
	sources[tool] = s
}

func setOpenCodeDB(on bool) bool {
	if !on {
		registerSource("opencode", nil)
		return true
	}
	registerSource("opencode", openCodeDBSource{path: openCodeDBPath()})
	return true
}

// openCodeDBPath locates the session database, honoring XDG_DATA_HOME the way
// opencode itself does.
func openCodeDBPath() string {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "opencode", "opencode.db")
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ".local", "share", "opencode", "opencode.db")
}

// usageQuery sums what this directory's sessions spent after a point in time.
// Cached reads are deliberately absent: they are cache hits, not billed
// prompt. tokens.input, when present, is billed prompt and is summed.
// Only assistant messages carry tokens; other roles are prompts. The directory
// list is spliced in by usageQueryFor, since SQL has no placeholder for a set.
const usageQuery = `
	SELECT
		COALESCE(SUM(json_extract(m.data, '$.tokens.output')), 0),
		COALESCE(SUM(json_extract(m.data, '$.tokens.reasoning')), 0),
		COALESCE(MAX(json_extract(m.data, '$.tokens.total')), 0),
		COALESCE(SUM(json_extract(m.data, '$.tokens.input')), 0)
	FROM message m
	JOIN session s ON s.id = m.session_id
	WHERE s.directory IN (%s) AND m.time_created > ?
	  AND json_extract(m.data, '$.role') = 'assistant'`

// usageQueryFor builds the query for n directory spellings. Only the number of
// placeholders varies: every value still travels as a bound parameter.
func usageQueryFor(n int) string {
	return fmt.Sprintf(usageQuery, strings.TrimSuffix(strings.Repeat("?,", n), ","))
}

func (o openCodeDBSource) read(dirs []string, since time.Time) (values, bool) {
	if o.path == "" || len(dirs) == 0 {
		return values{}, false
	}
	// mode=ro leaves the database alone; a missing or unreadable one is an
	// answer ("nothing to report"), not an error worth surfacing, since most
	// machines running this have no opencode at all.
	db, err := openReadOnly(o.path)
	if err != nil {
		return values{}, false
	}
	defer db.Close()

	args := make([]any, 0, len(dirs)+1)
	for _, d := range dirs {
		args = append(args, d)
	}
	args = append(args, since.UnixMilli())

	ctx, cancel := context.WithTimeout(context.Background(), dbQueryTimeout)
	defer cancel()

	var out, thinking, total, input sql.NullInt64
	row := db.QueryRowContext(ctx, usageQueryFor(len(dirs)), args...)
	if err := row.Scan(&out, &thinking, &total, &input); err != nil {
		return values{}, false
	}
	v := values{
		output:   counter64(out.Int64),
		thinking: counter64(thinking.Int64),
		total:    counter64(total.Int64),
		input:    counter64(input.Int64),
	}
	return v, v.output > 0 || v.total > 0 || v.input > 0
}
