// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

// Package agentwatch reports token throughput for AI coding agents running on
// this machine.
//
// tokentop already accepts agent events pushed over HTTP by a harness that
// cooperates. This is the other half: agents that are simply running, with
// nobody pushing anything. It finds them, reads the token counts they already
// write to their own session transcripts, and feeds the same event stream, so
// a claude or codex working in a terminal shows up next to the engines.
//
// The reading is done by github.com/maci0/gauntlet-go/agentusage, which is the
// same code gauntlet uses for its dashboard. Its contract carries over: every
// number came from an agent that reported it, and an agent that reports
// nothing produces no rate rather than a zero.
package agentwatch

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/maci0/gauntlet-go/agentusage"

	"tokentop/internal/core"
)

// Recorder receives the events this watcher produces. It is satisfied by the
// collector, the same as the HTTP ingest path.
type Recorder interface {
	RecordAgent(ev core.AgentEvent)
}

// Defaults chosen so a monitor stays cheap: discovery is a /proc walk, and
// reading is a stat per transcript.
const (
	defaultDiscoverEvery = 3 * time.Second
	defaultReadEvery     = time.Second
)

// Watcher follows the agent processes on this machine.
type Watcher struct {
	rec           Recorder
	discoverEvery time.Duration
	readEvery     time.Duration

	mu      sync.Mutex
	tracked map[int]*tracked
}

// tracked is one agent process being followed.
type tracked struct {
	proc  agentusage.Process
	watch *agentusage.Watcher
	last  agentusage.Sample
}

// New returns a watcher feeding rec. Zero intervals take the defaults.
func New(rec Recorder, discoverEvery, readEvery time.Duration) *Watcher {
	if discoverEvery <= 0 {
		discoverEvery = defaultDiscoverEvery
	}
	if readEvery <= 0 {
		readEvery = defaultReadEvery
	}
	return &Watcher{
		rec: rec, discoverEvery: discoverEvery, readEvery: readEvery,
		tracked: map[int]*tracked{},
	}
}

// Run follows agents until the context is canceled.
func (w *Watcher) Run(ctx context.Context) {
	// Agent definitions let a user name agents tokentop was not built to know
	// (in-house wrappers, the pi family), including where they keep their
	// transcripts. A missing file is the normal case.
	_ = agentusage.LoadDefinitions(agentusage.DefinitionsPath())

	discover := time.NewTicker(w.discoverEvery)
	defer discover.Stop()
	read := time.NewTicker(w.readEvery)
	defer read.Stop()

	w.discover()
	for {
		select {
		case <-ctx.Done():
			return
		case <-discover.C:
			w.discover()
		case <-read.C:
			w.read()
		}
	}
}

// Tracked reports how many agent processes are currently followed, for a
// status line.
func (w *Watcher) Tracked() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.tracked)
}

// Following reports whether one process is currently followed.
func (w *Watcher) Following(pid int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.tracked[pid]
	return ok
}

// discover starts following new agent processes and forgets exited ones.
func (w *Watcher) discover() {
	found := agentusage.Discover()
	live := make(map[int]bool, len(found))

	w.mu.Lock()
	defer w.mu.Unlock()
	for _, p := range found {
		live[p.PID] = true
		if _, seen := w.tracked[p.PID]; seen {
			continue
		}
		// A watcher counts only what is written after it attaches, so an agent
		// already halfway through a task contributes from here on rather than
		// retroactively. That keeps the rate honest at the cost of the first
		// part of a session tokentop was not running for.
		watch := agentusage.Watch(p.Tool, p.Dir, time.Now())
		if watch == nil {
			continue // this agent keeps nothing readable
		}
		w.tracked[p.PID] = &tracked{proc: p, watch: watch}
	}
	for pid := range w.tracked {
		if !live[pid] {
			delete(w.tracked, pid)
		}
	}
}

// read takes one reading per tracked agent and reports what grew.
func (w *Watcher) read() {
	w.mu.Lock()
	snapshot := make([]*tracked, 0, len(w.tracked))
	for _, t := range w.tracked {
		snapshot = append(snapshot, t)
	}
	w.mu.Unlock()

	for _, t := range snapshot {
		cur := t.watch.Read()
		if cur.Empty() {
			continue
		}
		out := cur.Output - t.last.Output
		think := cur.Thinking - t.last.Thinking
		if out <= 0 && think <= 0 {
			continue // nothing new: silence is not an event
		}
		t.last = cur
		if w.rec == nil {
			continue
		}
		w.rec.RecordAgent(core.AgentEvent{
			At:           cur.At,
			Agent:        t.proc.Tool,
			Kind:         "turn",
			OutputTokens: int64(max(out, 0)),
			Note:         note(t.proc, think),
		})
	}
}

// note carries what the event cannot: where the agent is working, and how much
// of the output was reasoning when the agent says so.
func note(p agentusage.Process, thinking int) string {
	s := shortDir(p.Dir)
	if thinking > 0 {
		s += " · " + strconv.Itoa(thinking) + " reasoning"
	}
	return s
}

// shortDir keeps the last two path components, which is what identifies a
// checkout without filling the row.
func shortDir(dir string) string {
	cut := 0
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' {
			cut++
			if cut == 2 {
				return dir[i+1:]
			}
		}
	}
	return dir
}
