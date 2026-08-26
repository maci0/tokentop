// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A final Poll must see a transcript that appeared after the watcher started,
// however briefly ago. A review can finish inside the rescan interval, and
// then the file holding everything it spent is younger than the cached
// listing: reusing that listing reports zero for the whole review.
func TestPollSeesATranscriptCreatedAfterTheWatchBegan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows
	work := t.TempDir()
	other := t.TempDir()

	// Another project's transcript, present before the watch starts: it is
	// what fills the cached listing, and it must not be all Poll ever sees.
	elsewhere := filepath.Join(home, ".claude", "projects", "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"assistant","cwd":` + jsonPath(other) + `,"message":{"usage":{"output_tokens":99999}}}` + "\n"
	if err := os.WriteFile(filepath.Join(elsewhere, "s.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	w := Watch("claude", work, time.Now())
	if w == nil {
		t.Fatal("claude is readable, so Watch must return a watcher")
	}

	mine := filepath.Join(home, ".claude", "projects", "mine")
	if err := os.MkdirAll(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	line = `{"type":"assistant","cwd":` + jsonPath(work) + `,"message":{"usage":{"output_tokens":42}}}` + "\n"
	if err := os.WriteFile(filepath.Join(mine, "s.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := w.Poll(); got.Output != 42 {
		t.Fatalf("final poll read %d output tokens, want the 42 this review spent", got.Output)
	}
}
