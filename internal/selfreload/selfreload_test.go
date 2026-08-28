// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package selfreload

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

// writeExe creates a stand-in binary image.
func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Watch is the auto-reload half of self-update: `go build` renames a new
// image over the running one and the dashboard must notice exactly once,
// never for its own first sight of the file.
func TestWatchFiresOncePerReplace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		testWatchFiresOncePerReplace(t)
	})
}

func testWatchFiresOncePerReplace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "toktop")
	writeExe(t, exe, "v1")

	fired := make(chan struct{}, 1)
	ctx := t.Context()
	done := make(chan struct{})
	go func() { defer close(done); Watch(ctx, exe, 5*time.Millisecond, func() { fired <- struct{}{} }) }()

	// The initial stat and many polls later, nothing has changed: no fire,
	// and the watcher must still be running.
	select {
	case <-fired:
		t.Fatal("fired without any replacement")
	case <-done:
		t.Fatal("returned before any replacement")
	case <-time.After(150 * time.Millisecond):
	}

	// Replace the way selfupdate.install does: rename over the live file so
	// the inode changes under the same path.
	next := filepath.Join(dir, "next")
	writeExe(t, next, "v2 with a different size")
	if err := os.Rename(next, exe); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement never noticed")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after firing")
	}
}

// A binary that does not exist yet (or briefly disappears mid-rebuild) must
// not fire: the first successful stat becomes the baseline, it is not a
// change.
func TestWatchToleratesMissingBinary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "not-yet")

		fired := make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		watching := make(chan struct{})
		go func() {
			close(watching)
			Watch(ctx, exe, 5*time.Millisecond, func() { fired <- struct{}{} })
		}()
		<-watching
		time.Sleep(100 * time.Millisecond) // polls run against the absent file

		writeExe(t, exe, "first sighting") // still not a change: this is the baseline
		select {
		case <-fired:
			t.Fatal("fired on first successful stat")
		case <-time.After(150 * time.Millisecond):
		}

		cancel()
	})
}

// Cancellation must end the poll loop promptly instead of leaking a ticker
// goroutine for the life of the dashboard.
func TestWatchStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "toktop")
		writeExe(t, exe, "v1")

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); Watch(ctx, exe, time.Millisecond, func() {}) }()

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Watch outlived its context")
		}
	})
}

// statIdentity must distinguish two files that share a size and mtime: a
// rebuild can be fast enough to land in the same timestamp slot, so the
// inode is what tells the images apart.
func TestStatIdentityDistinguishesReplacedFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "a")
	next := filepath.Join(dir, "b")
	writeExe(t, old, "same size")
	writeExe(t, next, "same size")

	idOld, err := statIdentity(old)
	if err != nil {
		t.Fatal(err)
	}
	idNext, err := statIdentity(next)
	if err != nil {
		t.Fatal(err)
	}
	if idOld == idNext {
		t.Fatalf("distinct files share an identity: %+v", idOld)
	}
	if _, err := statIdentity(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("a missing file reported an identity")
	}
}
