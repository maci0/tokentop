// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

// Package agentwatch reports token throughput for AI coding agents running on
// this machine.
//
// toktop already accepts agent events pushed over HTTP by a harness that
// cooperates. This is the other half: agents that are simply running, with
// nobody pushing anything. It finds them, reads the token counts they already
// write to their own session transcripts, and feeds the same event stream, so
// a claude or codex working in a terminal shows up next to the engines.
//
// The reading is done by github.com/maci0/gauntlet/agentusage, which is the
// same code gauntlet uses for its dashboard. Its contract carries over: every
// number came from an agent that reported it, and an agent that reports
// nothing produces no rate rather than a zero.
package agentwatch

import (
	"context"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/maci0/toktop/agentusage"

	"github.com/maci0/toktop/internal/core"
)

// Recorder receives the events this watcher produces. It is satisfied by the
// collector, the same as the HTTP ingest path.
type Recorder interface {
	RecordAgent(ev core.AgentEvent)
}

// Engines reports the endpoints toktop is already measuring, as the URLs the
// providers advertise. An agent generating through one of those engines has
// its tokens counted by the engine already, so this watcher must not add them
// again: the engine is the closer, more complete source (it sees every client,
// including ones that keep no transcript).
type Engines func() []string

// Defaults chosen so a monitor stays cheap: discovery is a /proc walk, and
// reading is a stat per transcript.
const (
	defaultDiscoverEvery = 3 * time.Second
	defaultReadEvery     = time.Second
)

// Watcher follows the agent processes on this machine.
type Watcher struct {
	rec           Recorder
	engines       Engines
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
	// viaEngine names the monitored engine this agent generates through, when
	// it has one. Its tokens are then the engine's to report.
	viaEngine string
}

// New returns a watcher feeding rec. Zero intervals take the defaults, and a
// nil engines function means nothing is being measured elsewhere.
func New(rec Recorder, engines Engines, discoverEvery, readEvery time.Duration) *Watcher {
	if discoverEvery <= 0 {
		discoverEvery = defaultDiscoverEvery
	}
	if readEvery <= 0 {
		readEvery = defaultReadEvery
	}
	return &Watcher{
		rec: rec, engines: engines,
		discoverEvery: discoverEvery, readEvery: readEvery,
		tracked: map[int]*tracked{},
	}
}

// Run follows agents until the context is canceled. Agent definitions
// (~/.gauntlet/agents.json, via agentusage.LoadDefinitions) are the caller's
// to load before Run: a malformed file must be reported where the operator
// can see it, not swallowed inside a goroutine behind the alt screen.
func (w *Watcher) Run(ctx context.Context) {
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
		// part of a session toktop was not running for.
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

	// Re-checked every pass rather than once at discovery: an agent connects
	// to its engine after it starts, and may switch engines mid-session.
	endpoints, labels := w.engineEndpoints()
	for pid, t := range w.tracked {
		t.viaEngine = ""
		for i, e := range endpoints {
			if agentusage.ConnectedTo(pid, []netip.AddrPort{e}) {
				t.viaEngine = labels[i]
				break
			}
		}
	}
}

// engineEndpoints parses the monitored engines' advertised URLs into addresses
// that can be compared against a process's open connections.
func (w *Watcher) engineEndpoints() ([]netip.AddrPort, []string) {
	if w.engines == nil {
		return nil, nil
	}
	raw := w.engines()
	eps := make([]netip.AddrPort, 0, len(raw))
	labels := make([]string, 0, len(raw))
	for _, addr := range raw {
		ap, label, ok := parseEngineAddr(addr)
		if !ok {
			continue
		}
		eps = append(eps, ap)
		labels = append(labels, label)
	}
	return eps, labels
}

// parseEngineAddr turns "http://127.0.0.1:11434" into an endpoint and a label.
// A hostname that is not an address cannot be compared against a connection
// table, so it is skipped rather than resolved: resolving would make a monitor
// do DNS on a timer.
func parseEngineAddr(addr string) (netip.AddrPort, string, bool) {
	host := addr
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		host = u.Host
	}
	ap, err := netip.ParseAddrPort(host)
	if err != nil {
		return netip.AddrPort{}, "", false
	}
	return ap, host, true
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
		if t.viaEngine != "" {
			// The engine is already reporting these tokens. Saying so keeps
			// the agent visible without counting its output twice.
			w.rec.RecordAgent(core.AgentEvent{
				At:    cur.At,
				Agent: t.proc.Tool,
				Kind:  "note",
				Note:  shortDir(t.proc.Dir) + " · counted by engine " + t.viaEngine,
			})
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
