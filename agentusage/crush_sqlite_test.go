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
// Each value is {completion_tokens, prompt_tokens, updated_at}.
func crushDB(t *testing.T, dir string, sessions map[string][3]int64) {
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
			`INSERT INTO sessions (id, completion_tokens, prompt_tokens, updated_at) VALUES (?, ?, ?, ?)`,
			id, v[0], v[1], v[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func putCrushSession(t *testing.T, dir, id string, output, input, updatedAt int64) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, ".crush", "crush.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO sessions (id, completion_tokens, prompt_tokens, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET completion_tokens = excluded.completion_tokens,
		     prompt_tokens = excluded.prompt_tokens,
		     updated_at = excluded.updated_at`,
		id, output, input, updatedAt); err != nil {
		t.Fatal(err)
	}
}

// What crush spent on a review is what it wrote while the review ran: rows
// from before it belong to whatever ran earlier.
func TestCrushSourceCountsOnlyThisReview(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][3]int64{
		"earlier": {5000, 8000, since.Add(-time.Hour).UnixMilli()},
		"during":  {1200, 400, since.Add(time.Minute).UnixMilli()},
		"also":    {300, 100, since.Add(2 * time.Minute).UnixMilli()},
	})

	v, ok := (crushDBSource{}).read([]string{dir}, since)
	if !ok || v.output != 1500 {
		t.Fatalf("read %+v (ok=%v), want 1500 output tokens", v, ok)
	}
	if v.input != 500 {
		t.Fatalf("input %d, want 500 prompt tokens written during the review", v.input)
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
	crushDB(t, dir, map[string][3]int64{
		"millis":      {10, 0, since.Add(time.Minute).UnixMilli()},
		"seconds":     {20, 0, since.Add(time.Minute).Unix()},
		"old seconds": {40, 0, since.Add(-time.Hour).Unix()},
		"old millis":  {80, 0, since.Add(-time.Hour).UnixMilli()},
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
	crushDB(t, root, map[string][3]int64{"s": {42, 0, since.Add(time.Second).UnixMilli()}})
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
	crushDB(t, dir, map[string][3]int64{"s": {100, 0, since.Add(time.Second).UnixMilli()}})
	if v, _ := (crushDBSource{}).read([]string{dir, dir + string(filepath.Separator)}, since); v.output != 100 {
		t.Fatalf("output %d, want 100 counted once", v.output)
	}
}

// A tree crush has never run in reports nothing, which is not an error.
func TestCrushSourceWithoutADatabase(t *testing.T) {
	if v, ok := (crushDBSource{}).read([]string{t.TempDir()}, time.Now()); ok || v.output != 0 || v.input != 0 {
		t.Fatalf("read %+v (ok=%v) from a tree with no crush database", v, ok)
	}
}

// Watch routes crush through this source, which is what makes the counts
// reach a caller without it knowing where they came from. Tokens already in
// the database at attach belong to an earlier review, the same rule the file
// adapters apply, so the session is written after Watch.
func TestWatchUsesTheCrushSource(t *testing.T) {
	dir := t.TempDir()
	crushDB(t, dir, map[string][3]int64{})
	w := Watch("crush", dir, time.Now())
	if w == nil {
		t.Fatal("crush is readable in this build, so Watch must return a watcher")
	}
	putCrushSession(t, dir, "s", 7, 3, time.Now().Add(time.Second).UnixMilli())
	got := w.Poll()
	if got.Output != 7 || got.Input != 3 {
		t.Fatalf("watcher read %+v, want 7 output and 3 prompt tokens", got)
	}
	if !Supported("crush") {
		t.Fatal("Supported must agree that crush is readable here")
	}
}

// completion_tokens is cumulative for a session's life. A session that
// already had tokens when the watcher attached must contribute only what it
// adds afterwards, or a continued crush dumps its history into this review.
func TestCrushWatchCountsOnlyGrowthAfterAttach(t *testing.T) {
	dir := t.TempDir()
	crushDB(t, dir, map[string][3]int64{
		"s": {5000, 2000, time.Now().Add(-time.Hour).UnixMilli()},
	})
	w := Watch("crush", dir, time.Now())
	if w == nil {
		t.Fatal("crush is readable in this build, so Watch must return a watcher")
	}
	w.poll(nil)
	if got := w.Sample(); got.Output != 0 || got.Input != 0 {
		t.Fatalf("counted tokens from before attach: %+v", got)
	}
	putCrushSession(t, dir, "s", 5100, 2300, time.Now().Add(time.Second).UnixMilli())
	w.poll(nil)
	got := w.Sample()
	if got.Output != 100 {
		t.Fatalf("output %d, want the 100 generated after attach", got.Output)
	}
	if got.Input != 300 {
		t.Fatalf("input %d, want the 300 prompt tokens billed after attach", got.Input)
	}
}

// A store that cannot be read at attach must not be treated as empty: the
// first successful poll would otherwise count every continued session's
// history as growth. Snapshot then, and count only what is added after.
func TestCrushWatchRetriesFailedAttachSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".crush", "crush.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := Watch("crush", dir, time.Now())
	if w == nil {
		t.Fatal("crush is readable in this build, so Watch must return a watcher")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	crushDB(t, dir, map[string][3]int64{
		"s": {5000, 0, time.Now().Add(-time.Hour).UnixMilli()},
	})
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("counted tokens from a store that was unreadable at attach: %d", got)
	}
	putCrushSession(t, dir, "s", 5100, 0, time.Now().Add(time.Second).UnixMilli())
	w.poll(nil)
	if got := w.Sample().Output; got != 100 {
		t.Fatalf("output %d, want the 100 generated after the store became readable", got)
	}
}

// An unreadable store at attach must not become an empty baseline. Replacing
// the file with a real database that already has tokens would otherwise
// dump that history into this review the first time it could be read.
func TestCrushWatchDoesNotCountHistoryWhenAttachBaselineFails(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, ".crush", "crush.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := Watch("crush", dir, time.Now())
	if w == nil {
		t.Fatal("crush is readable in this build, so Watch must return a watcher")
	}
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	crushDB(t, dir, map[string][3]int64{
		"s": {5000, 0, time.Now().Add(-time.Hour).UnixMilli()},
	})
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("counted tokens from before a successful attach: %d", got)
	}
	putCrushSession(t, dir, "s", 5100, 0, time.Now().Add(time.Second).UnixMilli())
	w.poll(nil)
	if got := w.Sample().Output; got != 100 {
		t.Fatalf("output %d, want the 100 generated after the baseline landed", got)
	}
}

// A review's directory is watched under every spelling it can be recorded
// under, and on macOS that always includes a symlinked one. All of them
// address a single database, whose sessions must be summed once.
func TestCrushSourceSumsOneDatabaseOnce(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	crushDB(t, dir, map[string][3]int64{"s": {7, 0, since.Add(time.Second).UnixMilli()}})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	v, ok := (crushDBSource{}).read([]string{dir, link}, since)
	if !ok || v.output != 7 {
		t.Fatalf("read %+v (ok=%v) through two spellings of one database", v, ok)
	}
}

// Prompt can grow while completion stays put (a follow-up that only billed
// context). Watch must still report Sample.Input, not treat the session as idle.
func TestCrushWatchCountsPromptOnlyGrowth(t *testing.T) {
	dir := t.TempDir()
	crushDB(t, dir, map[string][3]int64{
		"s": {40, 100, time.Now().Add(-time.Hour).UnixMilli()},
	})
	w := Watch("crush", dir, time.Now())
	if w == nil {
		t.Fatal("crush is readable in this build, so Watch must return a watcher")
	}
	putCrushSession(t, dir, "s", 40, 180, time.Now().Add(time.Second).UnixMilli())
	got := w.Poll()
	if got.Output != 0 {
		t.Fatalf("output %d, want 0: completion did not grow", got.Output)
	}
	if got.Input != 80 {
		t.Fatalf("input %d, want the 80 prompt tokens billed after attach", got.Input)
	}
	if got.Empty() {
		t.Fatal("prompt-only growth must not look like an empty sample")
	}
}
