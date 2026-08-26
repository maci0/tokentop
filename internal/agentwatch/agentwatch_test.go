// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentwatch

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maci0/toktop/agentusage"
	"github.com/maci0/toktop/internal/core"
)

// recorder collects what the watcher reports.
type recorder struct {
	mu     sync.Mutex
	events []core.AgentEvent
}

func (r *recorder) RecordAgent(ev core.AgentEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) all() []core.AgentEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]core.AgentEvent(nil), r.events...)
}

// TestWatchesARunningAgent is the whole feature in one test: a process that
// looks like claude is running somewhere, it writes tokens into its own
// transcript, and toktop reports them without anyone pushing anything.
func TestWatchesARunningAgent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("discovery reads /proc")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	work := t.TempDir()
	transcript := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(transcript, 0o755); err != nil {
		t.Fatal(err)
	}

	// A process named like the agent, working in the directory the transcript
	// claims. Nothing about it cooperates with toktop.
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Dir = work
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	rec := &recorder{}
	w := New(rec, nil, 200*time.Millisecond, 100*time.Millisecond)
	ctx := t.Context()
	go w.Run(ctx)

	// Give discovery a chance to find the process, then let the agent "spend".
	waitFor(t, 3*time.Second, func() bool { return w.Following(cmd.Process.Pid) })
	appendLine(t, filepath.Join(transcript, "s.jsonl"), usageLine(work, 120))
	appendLine(t, filepath.Join(transcript, "s.jsonl"), usageLine(work, 240))

	waitFor(t, 3*time.Second, func() bool { return len(rec.all()) > 0 })

	var total int64
	for _, ev := range rec.all() {
		if ev.Agent != "claude" {
			t.Fatalf("wrong agent: %+v", ev)
		}
		if ev.At.IsZero() {
			t.Fatalf("event without a timestamp cannot become a rate: %+v", ev)
		}
		total += ev.OutputTokens
	}
	if total != 360 {
		t.Fatalf("reported %d output tokens, want 360", total)
	}
}

// TestForgetsExitedAgents keeps the tracking table from growing for the life
// of the process.
func TestForgetsExitedAgents(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("discovery reads /proc")
	}
	t.Setenv("HOME", t.TempDir())

	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 0.6\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	w := New(&recorder{}, nil, 100*time.Millisecond, time.Hour)
	ctx := t.Context()
	go w.Run(ctx)

	// Scoped to this process: a developer machine usually has real agents
	// running, so a global count would never reach zero.
	pid := cmd.Process.Pid
	waitFor(t, 3*time.Second, func() bool { return w.Following(pid) })
	_, _ = cmd.Process.Wait()
	waitFor(t, 3*time.Second, func() bool { return !w.Following(pid) })
}

// TestSilentAgentProducesNoEvents is the honesty half: an agent that writes no
// usage must not appear as zero throughput.
func TestSilentAgentProducesNoEvents(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("discovery reads /proc")
	}
	t.Setenv("HOME", t.TempDir())

	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	rec := &recorder{}
	w := New(rec, nil, 100*time.Millisecond, 50*time.Millisecond)
	ctx := t.Context()
	go w.Run(ctx)

	time.Sleep(700 * time.Millisecond)
	// Only this test's directory matters: other agents may genuinely be
	// running on the machine and reporting real numbers.
	for _, ev := range rec.all() {
		if ev.Note == shortDir(cmd.Dir) {
			t.Fatalf("invented an event for a silent agent: %+v", ev)
		}
	}
}

func TestShortDirKeepsTheIdentifyingPart(t *testing.T) {
	cases := map[string]string{
		"/home/dev/src/project": "src/project",
		"/home/dev/project":     "dev/project",
		"project":               "project",
		"/":                     "/",
	}
	for in, want := range cases {
		if got := shortDir(in); got != want {
			t.Errorf("shortDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// The path is quoted the way a real transcript quotes it: on Windows the
// separator is JSON's escape character, so a spliced-in path is not JSON.
func usageLine(cwd string, out int) string {
	quoted, err := json.Marshal(cwd)
	if err != nil {
		panic(err)
	}
	return `{"type":"assistant","cwd":` + string(quoted) +
		`,"message":{"usage":{"output_tokens":` + strconv.Itoa(out) + `}}}`
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", limit)
}

// TestEngineTakesPrecedence is the double-counting guard: an agent generating
// through an engine toktop already measures must not add its tokens on top
// of the engine's. It stays visible, with a note saying where its output is
// being counted.
func TestEngineTakesPrecedence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("connection attribution reads /proc")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Something that looks like a local inference engine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	engine := "http://" + ln.Addr().String()

	work := t.TempDir()
	transcript := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(transcript, 0o755); err != nil {
		t.Fatal(err)
	}

	// An agent that holds a connection to that engine while it works.
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nexec 3<>/dev/tcp/127.0.0.1/" + port(t, ln) + "\nsleep 5\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", bin) // /dev/tcp is a bash feature
	cmd.Dir = work
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	rec := &recorder{}
	w := New(rec, func() []string { return []string{engine} },
		150*time.Millisecond, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	waitFor(t, 3*time.Second, func() bool { return w.Following(cmd.Process.Pid) })
	// Let one discovery pass classify the connection before it spends.
	time.Sleep(400 * time.Millisecond)
	appendLine(t, filepath.Join(transcript, "s.jsonl"), usageLine(work, 500))

	waitFor(t, 3*time.Second, func() bool { return len(rec.all()) > 0 })
	for _, ev := range rec.all() {
		if ev.OutputTokens != 0 {
			t.Fatalf("counted tokens the engine already reports: %+v", ev)
		}
		if ev.Kind != "note" || !strings.Contains(ev.Note, "counted by engine") {
			t.Fatalf("the agent should stay visible with an explanation: %+v", ev)
		}
	}
}

func port(t *testing.T, ln net.Listener) string {
	t.Helper()
	_, p, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestEngineNoteEmittedOncePerEngine pins the difference between state and
// events: an agent generating through the same engine reads as one note no
// matter how many readings pass, and a switch of engines says so once. A
// fresh note per reading would flood the retained feed (AgentHistoryLen
// rows) and evict every event carrying real numbers.
func TestEngineNoteEmittedOncePerEngine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on windows
	work := t.TempDir()
	transcript := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(transcript, 0o755); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	w := New(rec, nil, time.Hour, time.Hour)
	tr := &tracked{
		proc:  agentusage.Process{PID: 1, Tool: "claude", Dir: work},
		watch: agentusage.Watch("claude", work, time.Now()),
	}
	if tr.watch == nil {
		t.Fatal("no claude adapter")
	}
	w.tracked[tr.proc.PID] = tr

	appendLine(t, filepath.Join(transcript, "s.jsonl"), usageLine(work, 100))
	tr.viaEngine = "http://127.0.0.1:11434"
	w.read() // first growth through this engine: attributed once

	appendLine(t, filepath.Join(transcript, "s.jsonl"), usageLine(work, 150))
	w.read() // same engine again: attribution is unchanged, no new note

	appendLine(t, filepath.Join(transcript, "s.jsonl"), usageLine(work, 180))
	tr.viaEngine = "http://127.0.0.1:8080"
	w.read() // engine changed: said once

	evs := rec.all()
	if len(evs) != 2 {
		t.Fatalf("got %d events, want one note per engine: %+v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Kind != "note" || !strings.Contains(ev.Note, "counted by engine") {
			t.Fatalf("expected an attribution note, got: %+v", ev)
		}
		if ev.OutputTokens != 0 {
			t.Fatalf("counted tokens the engine already reports: %+v", ev)
		}
	}
	if !strings.Contains(evs[0].Note, "11434") || !strings.Contains(evs[1].Note, "8080") {
		t.Fatalf("notes name the wrong engines: %q, %q", evs[0].Note, evs[1].Note)
	}
}
