// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

//go:build sqlite

package agentusage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// opencodeDB builds a session store shaped like the real one and points the
// source at it.
func opencodeDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE session (id text PRIMARY KEY, directory text NOT NULL)`,
		`CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL,
			time_created integer NOT NULL, data text NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func addSession(t *testing.T, path, id, dir string) {
	t.Helper()
	exec(t, path, `INSERT INTO session (id, directory) VALUES (?, ?)`, id, dir)
}

func addMessage(t *testing.T, path, id, session string, at time.Time, data string) {
	t.Helper()
	exec(t, path, `INSERT INTO message (id, session_id, time_created, data) VALUES (?, ?, ?, ?)`,
		id, session, at.UnixMilli(), data)
}

func exec(t *testing.T, path, stmt string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt, args...); err != nil {
		t.Fatal(err)
	}
}

// withOpenCodeDB points the source at a test database for one test.
func withOpenCodeDB(t *testing.T, path string) {
	t.Helper()
	registerSource("opencode", openCodeDBSource{path: path})
	t.Cleanup(func() { registerSource("opencode", nil) })
}

func TestOpenCodeDBCountsThisReviewOnly(t *testing.T) {
	path := opencodeDB(t)
	work, other := t.TempDir(), t.TempDir()
	addSession(t, path, "s-mine", work)
	addSession(t, path, "s-theirs", other)

	start := time.Now()
	// Everything an earlier review spent in the same directory.
	addMessage(t, path, "m0", "s-mine", start.Add(-time.Hour),
		`{"role":"assistant","tokens":{"output":9000,"reasoning":100,"total":50000}}`)
	withOpenCodeDB(t, path)

	w := Watch("opencode", work, start)
	if w == nil {
		t.Fatal("opencode should be readable once the database is enabled")
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("counted an earlier review's tokens: %d", got)
	}

	addMessage(t, path, "m1", "s-mine", start.Add(time.Second),
		`{"role":"assistant","tokens":{"output":324,"reasoning":52,"total":131769}}`)
	addMessage(t, path, "m2", "s-mine", start.Add(2*time.Second),
		`{"role":"assistant","tokens":{"output":120,"reasoning":8,"total":131900}}`)
	// A concurrent review in another directory, which must not be counted.
	addMessage(t, path, "m3", "s-theirs", start.Add(time.Second),
		`{"role":"assistant","tokens":{"output":7777,"total":900000}}`)

	w.poll(nil)
	s := w.Sample()
	if s.Output != 444 {
		t.Fatalf("output tokens %d, want 444 (324+120)", s.Output)
	}
	if s.Thinking != 60 {
		t.Fatalf("thinking tokens %d, want 60 (52+8)", s.Thinking)
	}
	if s.Total != 131900 {
		t.Fatalf("total %d, want the largest context reported, 131900", s.Total)
	}
}

// A missing database is an ordinary state (no opencode on this machine), not
// an error, and must never invent a number.
func TestOpenCodeDBWithoutAStoreReportsNothing(t *testing.T) {
	withOpenCodeDB(t, filepath.Join(t.TempDir(), "absent.db"))
	w := Watch("opencode", t.TempDir(), time.Now())
	if w == nil {
		t.Fatal("the source is registered, so a watcher is expected")
	}
	w.poll(nil)
	if got := w.Sample(); !got.Empty() {
		t.Fatalf("invented usage from a missing database: %+v", got)
	}
}

func TestReadOnlyDSNEscapesURISyntax(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/home/user/.local/share/opencode/opencode.db",
			"file:/home/user/.local/share/opencode/opencode.db?mode=ro"},
		{"/home/mar{c}o#witz/db", "file:/home/mar{c}o%23witz/db?mode=ro"},
		{"/tmp/what?now/db", "file:/tmp/what%3Fnow/db?mode=ro"},
		{"/tmp/100%20done/db", "file:/tmp/100%2520done/db?mode=ro"},
		{`C:\Users\mw\.local\share\opencode.db`, `file:C:\Users\mw\.local\share\opencode.db?mode=ro`},
	} {
		if got := readOnlyDSN(tc.path); got != tc.want {
			t.Errorf("readOnlyDSN(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// A home directory may contain characters that carry URI syntax in a SQLite
// file: DSN; those paths must still open and read as themselves.
func TestOpenCodeDBReadsPathWithURISyntaxCharacters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "50%.db")
	exec(t, path, `CREATE TABLE session (id text PRIMARY KEY, directory text NOT NULL)`)
	exec(t, path, `CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL,
		time_created integer NOT NULL, data text NOT NULL)`)

	dir := t.TempDir()
	addSession(t, path, "s", dir)
	start := time.Now()
	addMessage(t, path, "m1", "s", start.Add(time.Second),
		`{"role":"assistant","tokens":{"output":42}}`)
	withOpenCodeDB(t, path)

	w := Watch("opencode", dir, start)
	if w == nil {
		t.Fatal("the source is registered, so a watcher is expected")
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 42 {
		t.Fatalf("output tokens %d, want 42: the URI-syntax path was misread", got)
	}
}

func TestEnableOpenCodeDBReportsAvailability(t *testing.T) {
	if !EnableOpenCodeDB(false) {
		t.Fatal("a build with -tags sqlite can read the database")
	}
	if Supported("opencode") {
		t.Fatal("disabled means unreadable")
	}
	if !EnableOpenCodeDB(true) {
		t.Fatal("enabling should report the same availability")
	}
	t.Cleanup(func() { EnableOpenCodeDB(false) })
	if !Supported("opencode") {
		t.Fatal("enabled means readable")
	}
}

// An agent records the directory it was started in, which on macOS is a
// symlink to the one filepath.EvalSymlinks reports. Both spellings must count.
func TestOpenCodeDBMatchesUnresolvedDirectories(t *testing.T) {
	path := opencodeDB(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	addSession(t, path, "s", link) // the session names the path as given
	withOpenCodeDB(t, path)

	start := time.Now()
	w := Watch("opencode", link, start)
	addMessage(t, path, "m1", "s", start.Add(time.Second),
		`{"role":"assistant","tokens":{"output":42}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 42 {
		t.Fatalf("output tokens %d, want 42: the unresolved spelling was missed", got)
	}
}
