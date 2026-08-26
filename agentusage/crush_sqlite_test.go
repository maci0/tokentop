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

	_ "modernc.org/sqlite"
)

// crushDB writes a database shaped like crush's own, with the sessions given.
func crushDB(t *testing.T, dir string, sessions map[string][2]int64) {
	t.Helper()
	path := filepath.Join(dir, ".crush", "crush.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for id, v := range sessions {
		if _, err := db.Exec(
			`INSERT INTO sessions (id, completion_tokens, updated_at) VALUES (?, ?, ?)`,
			id, v[0], v[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// What crush spent on a review is what it wrote while the review ran: rows
// from before it belong to whatever ran earlier.
func TestCrushSourceCountsOnlyThisReview(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][2]int64{
		"earlier": {5000, since.Add(-time.Hour).UnixMilli()},
		"during":  {1200, since.Add(time.Minute).UnixMilli()},
		"also":    {300, since.Add(2 * time.Minute).UnixMilli()},
	})

	v, ok := (crushDBSource{}).read([]string{dir}, since)
	if !ok || v.output != 1500 {
		t.Fatalf("read %+v (ok=%v), want 1500 output tokens", v, ok)
	}
	if v.thinking != 0 {
		t.Fatalf("thinking %d: crush reports no reasoning split, so it must claim none", v.thinking)
	}
}

// The rows carry two units: crush writes milliseconds, and the table's own
// update trigger writes seconds. Both must count, or a review reads zero.
func TestCrushSourceAcceptsSecondsAndMilliseconds(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][2]int64{
		"millis":      {10, since.Add(time.Minute).UnixMilli()},
		"seconds":     {20, since.Add(time.Minute).Unix()},
		"old seconds": {40, since.Add(-time.Hour).Unix()},
		"old millis":  {80, since.Add(-time.Hour).UnixMilli()},
	})
	if v, ok := (crushDBSource{}).read([]string{dir}, since); !ok || v.output != 30 {
		t.Fatalf("read %+v (ok=%v), want the 30 written during the review in either unit", v, ok)
	}
}

// crush resolves the project root, so a worktree under the project reads the
// project's database rather than reporting nothing.
func TestCrushSourceFindsTheProjectDatabase(t *testing.T) {
	root := t.TempDir()
	since := time.Now()
	crushDB(t, root, map[string][2]int64{"s": {42, since.Add(time.Second).UnixMilli()}})
	sub := filepath.Join(root, "worktrees", "sec-review")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if v, ok := (crushDBSource{}).read([]string{sub}, since); !ok || v.output != 42 {
		t.Fatalf("read %+v (ok=%v), want the project database's 42", v, ok)
	}
}

// Two spellings of one directory are one database, not two readings of it.
func TestCrushSourceCountsOneDatabaseOnce(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][2]int64{"s": {100, since.Add(time.Second).UnixMilli()}})
	if v, _ := (crushDBSource{}).read([]string{dir, dir + string(filepath.Separator)}, since); v.output != 100 {
		t.Fatalf("output %d, want 100 counted once", v.output)
	}
}

// A tree crush has never run in reports nothing, which is not an error.
func TestCrushSourceWithoutADatabase(t *testing.T) {
	if v, ok := (crushDBSource{}).read([]string{t.TempDir()}, time.Now()); ok || v.output != 0 {
		t.Fatalf("read %+v (ok=%v) from a tree with no crush database", v, ok)
	}
}

// Watch routes crush through this source, which is what makes the counts
// reach a caller without it knowing where they came from.
func TestWatchUsesTheCrushSource(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][2]int64{"s": {7, since.Add(time.Second).UnixMilli()}})
	w := Watch("crush", dir, since)
	if w == nil {
		t.Fatal("crush is readable in this build, so Watch must return a watcher")
	}
	if got := w.Poll(); got.Output != 7 {
		t.Fatalf("watcher read %+v, want 7 output tokens", got)
	}
	if !Supported("crush") {
		t.Fatal("Supported must agree that crush is readable here")
	}
}

// A review's directory is watched under every spelling it can be recorded
// under, and on macOS that always includes a symlinked one. All of them
// address a single database, whose sessions must be summed once.
func TestCrushSourceSumsOneDatabaseOnce(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][2]int64{"s": {7, since.Add(time.Second).UnixMilli()}})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	v, ok := (crushDBSource{}).read([]string{dir, link}, since)
	if !ok || v.output != 7 {
		t.Fatalf("read %+v (ok=%v) through two spellings of one database", v, ok)
	}
}
