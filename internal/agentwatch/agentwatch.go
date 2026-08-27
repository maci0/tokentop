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
// The reading is done by github.com/maci0/toktop/agentusage, the same code
// gauntlet uses for its dashboard. Its contract carries over: every
// number came from an agent that reported it, and an agent that reports
// nothing produces no rate rather than a zero.
package agentwatch

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// its tokens counted by the engine already. Events are still recorded so the
// agent stays on the dashboard, with ViaEngine set so header and chart
// totals skip them: the engine is the closer, more complete source (it sees
// every client, including ones that keep no transcript).
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
	// it has one. Token deltas are still recorded so the agent stays on the
	// dashboard; ViaEngine on the event is what stops aggregates adding them
	// on top of the engine's own numbers.
	viaEngine string
	cancel    context.CancelFunc
	done      chan struct{}
}

// New returns a watcher feeding rec. A nil engines function means nothing is
// being measured elsewhere.
func New(rec Recorder, engines Engines) *Watcher {
	return &Watcher{
		rec: rec, engines: engines,
		discoverEvery: defaultDiscoverEvery, readEvery: defaultReadEvery,
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

	w.discover(ctx)
	for {
		select {
		case <-ctx.Done():
			w.stopAll()
			return
		case <-discover.C:
			w.discover(ctx)
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
func (w *Watcher) discover(ctx context.Context) {
	found := agentusage.Discover()
	live := make(map[int]bool, len(found))

	w.mu.Lock()
	var newProcs []agentusage.Process
	for _, p := range found {
		live[p.PID] = true
		if _, seen := w.tracked[p.PID]; !seen {
			newProcs = append(newProcs, p)
		}
	}
	var gone []*tracked
	for pid, t := range w.tracked {
		if !live[pid] {
			delete(w.tracked, pid)
			gone = append(gone, t)
		}
	}
	w.mu.Unlock()

	// Watch walks transcript stores; doing that under w.mu would stall
	// report() for agents already being followed. cancel is set before
	// the map insert so stopOne never observes a nil cancel.
	var started []*tracked
	var startCtx []context.Context
	for _, p := range newProcs {
		// A watcher counts only what is written after it attaches, so an agent
		// already halfway through a task contributes from here on rather than
		// retroactively. That keeps the rate honest at the cost of the first
		// part of a session toktop was not running for.
		watch := agentusage.Watch(p.Tool, p.Dir, time.Now())
		if watch == nil {
			continue // this agent keeps nothing readable
		}
		tctx, cancel := context.WithCancel(ctx)
		t := &tracked{proc: p, watch: watch, done: make(chan struct{}), cancel: cancel}
		w.mu.Lock()
		if _, seen := w.tracked[p.PID]; seen {
			w.mu.Unlock()
			cancel()
			continue
		}
		w.tracked[p.PID] = t
		w.mu.Unlock()
		started = append(started, t)
		startCtx = append(startCtx, tctx)
	}

	for i, t := range started {
		go func(t *tracked, tctx context.Context) {
			defer close(t.done)
			t.watch.Run(tctx, w.readEvery, func(s agentusage.Sample) {
				w.report(t, s)
			})
		}(t, startCtx[i])
	}
	for _, t := range gone {
		w.stopOne(t)
	}

	w.mu.Lock()
	pids := make([]int, 0, len(w.tracked))
	for pid := range w.tracked {
		pids = append(pids, pid)
	}
	w.mu.Unlock()

	// Re-checked every pass rather than once at discovery: an agent connects
	// to its engine after it starts, and may switch engines mid-session.
	// One table read covers every tracked pid; matching each agent against
	// each engine separately would reread /proc/net/tcp per pair.
	endpoints, labels := w.engineEndpoints()
	matched := agentusage.MatchingEndpoints(pids, endpoints)
	w.mu.Lock()
	for pid, t := range w.tracked {
		t.viaEngine = ""
		if ap, ok := matched[pid]; ok {
			for i, e := range endpoints {
				if e == ap {
					t.viaEngine = labels[i]
					break
				}
			}
		}
	}
	w.mu.Unlock()
}

func (w *Watcher) stopAll() {
	w.mu.Lock()
	gone := make([]*tracked, 0, len(w.tracked))
	for pid, t := range w.tracked {
		delete(w.tracked, pid)
		gone = append(gone, t)
	}
	w.mu.Unlock()
	for _, t := range gone {
		w.stopOne(t)
	}
}

func (w *Watcher) stopOne(t *tracked) {
	if t.cancel != nil {
		t.cancel()
	}
	if t.done != nil {
		<-t.done
	}
	if t.watch != nil {
		w.report(t, t.watch.Poll())
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
// do DNS on a timer. URLs that omit the port (http://127.0.0.1, https://…)
// use the scheme default: without it ParseAddrPort fails and an agent talking
// to that engine would not be labelled via, so its tokens would be counted
// twice.
func parseEngineAddr(addr string) (netip.AddrPort, string, bool) {
	host := addr
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		host = u.Host
		if u.Port() == "" {
			port := "80"
			if u.Scheme == "https" {
				port = "443"
			}
			host = net.JoinHostPort(u.Hostname(), port)
		}
	}
	ap, err := netip.ParseAddrPort(host)
	if err != nil {
		return netip.AddrPort{}, "", false
	}
	return ap, host, true
}

// read takes one reading per tracked agent and reports what grew.
// Tests drive it directly; the live path is Watcher.Run per agent, which
// already polls on readEvery without forcing a store walk each tick.
func (w *Watcher) read() {
	w.mu.Lock()
	snapshot := make([]*tracked, 0, len(w.tracked))
	for _, t := range w.tracked {
		snapshot = append(snapshot, t)
	}
	w.mu.Unlock()

	for _, t := range snapshot {
		w.report(t, t.watch.Poll())
	}
}

// report records growth since the last sample. Live Run callbacks and the
// test-driven read path both come through here so deltas stay consistent.
func (w *Watcher) report(t *tracked, cur agentusage.Sample) {
	if cur.Empty() {
		return
	}
	w.mu.Lock()
	out := cur.Output - t.last.Output
	think := cur.Thinking - t.last.Thinking
	prompt := cur.Input - t.last.Input
	if out <= 0 && think <= 0 && prompt <= 0 {
		w.mu.Unlock()
		return // nothing new: silence is not an event
	}
	t.last = cur
	proc := t.proc
	via := t.viaEngine
	rec := w.rec
	w.mu.Unlock()
	if rec == nil {
		return
	}
	rec.RecordAgent(core.AgentEvent{
		At:             cur.At,
		Agent:          proc.Tool,
		Kind:           "turn",
		PromptTokens:   int64(max(prompt, 0)),
		OutputTokens:   int64(max(out, 0)),
		ThinkingTokens: int64(max(think, 0)),
		ViaEngine:      via,
		Note:           note(proc, think, via),
	})
}

// note carries what the event cannot: where the agent is working, how much of
// the output was reasoning when the agent says so, and which monitored engine
// already counts this output when one does.
func note(p agentusage.Process, thinking int, via string) string {
	s := shortDir(p.Dir)
	if thinking > 0 {
		s += " · " + strconv.Itoa(thinking) + " reasoning"
	}
	if via != "" {
		s += " · counted by engine " + via
	}
	return s
}

// shortDir keeps the last two path components, which is what identifies a
// checkout without filling the row. A path under the operator's home is
// rewritten with ~ first so a username sitting in those last two components
// (home itself, or a project directly in it) never becomes the note.
// Separators are folded to '/' so a Windows path is shortened the same way
// as a Unix one.
func shortDir(dir string) string {
	if dir == "" {
		return ""
	}
	if stripped, ok := stripHome(dir); ok {
		dir = stripped
	}
	return lastTwoComponents(dir)
}

func stripHome(dir string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	cleanDir, cleanHome := resolvePath(dir), resolvePath(home)
	rel, err := filepath.Rel(cleanHome, cleanDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		return "~", true
	}
	return "~/" + filepath.ToSlash(rel), true
}

func resolvePath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func lastTwoComponents(dir string) string {
	dir = filepath.ToSlash(dir)
	cut := 0
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' || dir[i] == '\\' {
			cut++
			if cut == 2 {
				return dir[i+1:]
			}
		}
	}
	return dir
}
