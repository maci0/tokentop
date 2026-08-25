// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

// Package agentusage reads live token counts out of the transcripts agent CLIs
// already write to disk.
//
// Agents differ in what they print to stdout: some report token usage as they
// stream, some only at exit, some never. They agree on something else, though,
// which is that they keep a structured session transcript, and that transcript
// carries per-message usage with timestamps. Tailing it gives a live rate
// without root, without intercepting anyone's network traffic, and without
// asking the agent to behave differently.
//
// The design constraints that shape everything here:
//
//   - Only count what happened during this review. Transcripts are per session
//     and sessions outlive reviews (--continue-sessions reuses them), so the
//     watcher records where each file ended when it attached and reads only
//     what is appended after that.
//   - Attribute the transcript to the right review. Each adapter ties its
//     files to a working directory: recorded per record, read from the
//     session header, or implicit because the log lives inside the reviewed
//     directory itself (clanker), so the cwd is the key.
//   - Never invent a number. An agent whose transcript cannot be found, parsed,
//     or attributed simply reports nothing, and the dashboard shows no rate.
package agentusage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Sample is cumulative usage observed since the watcher attached. Thinking is
// the reasoning share of Output, which the agents report separately: it is what
// the model spent before it wrote anything the user sees.
type Sample struct {
	Output   int
	Thinking int
	Total    int
	// At is when the reading was taken, so successive samples make a rate.
	At time.Time
}

// Empty reports whether nothing has been observed yet.
func (s Sample) Empty() bool { return s.Output == 0 && s.Total == 0 }

// Rate returns tokens per second between two samples, and whether it could be
// computed at all. It never extrapolates: without two readings and a positive
// span there is no rate to report.
func Rate(prev, cur Sample) (float64, bool) {
	span := cur.At.Sub(prev.At).Seconds()
	if span <= 0 || cur.Output <= prev.Output {
		return 0, false
	}
	return float64(cur.Output-prev.Output) / span, true
}

// valueKind says how an adapter's numbers accumulate.
type valueKind uint8

const (
	// perMessage values are added up: each line carries one message's usage.
	perMessage valueKind = iota
	// cumulative values already include everything before them, so the
	// watcher subtracts the first value it sees.
	cumulative
)

// adapter knows one agent's transcript layout.
type adapter struct {
	// roots are directories to scan, given the review's working directory.
	// Most agents keep transcripts under $HOME and ignore it; agents that keep
	// them inside the project (clanker) use it.
	roots func(dir string) []string
	// suffix filters transcript files under a root by literal suffix match.
	suffix string
	// kind says how to combine the parsed values.
	kind valueKind
	// parse extracts usage and the recorded working directory from one line.
	// ok is false for lines that carry neither.
	parse func(line []byte) (v values, cwd string, ok bool)
	// sessionCwd reads the working directory from a session header line, for
	// agents whose usage records do not repeat it. nil when every usage line
	// carries its own cwd.
	sessionCwd func(line []byte) (string, bool)
}

// values is one record's usage numbers.
type values struct {
	output   int
	thinking int
	total    int
}

// maxSaneTokens bounds one counter a transcript line may contribute. Real
// usage never approaches it; anything larger is corruption or hostility, and
// reporting nothing beats displaying a lie (or overflowing the totals).
const maxSaneTokens = 1 << 40

// counter coerces a decoded transcript counter to its contribution: negative
// or absurd magnitudes read as absent, the same judgment asInt makes for the
// generic walker.
func counter(n int) int {
	if n < 0 || n > maxSaneTokens {
		return 0
	}
	return n
}

var adaptersMu sync.RWMutex

var adapters = map[string]adapter{
	"claude": {
		roots:  func(string) []string { return []string{home(".claude", "projects")} },
		suffix: ".jsonl",
		kind:   perMessage,
		parse:  parseClaude,
	},
	// qwen-code keeps per-project chat transcripts with Gemini-style
	// usageMetadata on each assistant message.
	"qwen": {
		roots:  func(string) []string { return []string{home(".qwen", "projects")} },
		suffix: ".jsonl",
		kind:   perMessage,
		parse:  parseQwen,
	},
	// dsh (DeepSeek Harness) writes one session log per run under
	// ~/.dsh/sessions/--<normalized-cwd>--/<id>/session.jsonl, with the cwd in
	// an opening header record. The default spelling is zstd-compressed and
	// unreadable here, which is why the launcher must ask dsh for
	// compression: none.
	"dsh": {
		roots:      func(string) []string { return []string{home(".dsh", "sessions")} },
		suffix:     ".jsonl",
		kind:       perMessage,
		parse:      parseGeneric,
		sessionCwd: genericSessionCwd,
	},

	// clanker keeps its own token log inside the repository it runs in, one
	// record per request, so the review directory is the attribution.
	"clanker": {
		roots:  func(dir string) []string { return []string{filepath.Join(dir, "state")} },
		suffix: "token_stats.jsonl",
		kind:   perMessage,
		parse:  parseGeneric,
	},
	// GitHub Copilot CLI appends one event per line to
	// ~/.copilot/session-state/<session>/events.jsonl. Only the opening
	// session.start record carries the working directory (under
	// data.context.cwd); the assistant.message records carry OpenAI-shaped
	// usage, which the generic parser already reads.
	"copilot": {
		roots:      func(string) []string { return []string{home(".copilot", "session-state")} },
		suffix:     "events.jsonl",
		kind:       perMessage,
		parse:      parseGeneric,
		sessionCwd: genericSessionCwd,
	},
	"codex": {
		roots:      func(string) []string { return []string{home(".codex", "sessions")} },
		suffix:     ".jsonl",
		kind:       cumulative,
		parse:      parseCodex,
		sessionCwd: codexSessionCwd,
	},
}

// Spec describes where a defined agent keeps its transcripts, so live usage
// works for agents gauntlet was not compiled to know about (pi and the CLIs
// built on it, in-house wrappers). The records are parsed generically: any
// JSONL whose objects carry recognizable token counters works, and one whose
// objects do not simply reports nothing.
type Spec struct {
	// Roots are directories to search, with ~ expanded.
	Roots []string `json:"roots"`
	// Suffix filters transcript files (default ".jsonl").
	Suffix string `json:"suffix,omitempty"`
	// Cumulative says the counters already include everything before them, so
	// the first value seen becomes a baseline. Default is per message.
	Cumulative bool `json:"cumulative,omitempty"`

	// HeaderCwd says the working directory appears once in a session header
	// rather than on every record, so ownership is decided from the head of
	// the file. Without it, a transcript whose usage lines carry no cwd is
	// attributed by location alone.
	HeaderCwd bool `json:"header_cwd,omitempty"`
}

// RegisterSpec adds a transcript adapter for a defined agent.
func RegisterSpec(tool string, spec Spec) error {
	if tool == "" {
		return errors.New("usage spec needs an agent name")
	}
	if len(spec.Roots) == 0 {
		return fmt.Errorf("usage spec for %q has no roots", tool)
	}
	// {dir} lets a definition point at a store inside the reviewed project,
	// the way clanker keeps its own.
	patterns := append([]string(nil), spec.Roots...)
	rootsFor := func(dir string) []string {
		out := make([]string, 0, len(patterns))
		for _, r := range patterns {
			out = append(out, expandHome(strings.ReplaceAll(r, "{dir}", dir)))
		}
		return out
	}
	suffix := spec.Suffix
	if suffix == "" {
		suffix = ".jsonl"
	}
	kind := perMessage
	if spec.Cumulative {
		kind = cumulative
	}
	ad := adapter{
		roots:  rootsFor,
		suffix: suffix,
		kind:   kind,
		parse:  parseGeneric,
	}
	if spec.HeaderCwd {
		ad.sessionCwd = genericSessionCwd
	}
	adaptersMu.Lock()
	adapters[tool] = ad
	adaptersMu.Unlock()
	return nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if dir, err := os.UserHomeDir(); err == nil {
			return filepath.Join(dir, p[2:])
		}
	}
	return p
}

// parseGeneric reads usage out of an unknown JSONL record by key, the same way
// the stream parser does. It is what makes a defined agent's transcript
// readable without a bespoke adapter.
func parseGeneric(line []byte) (values, string, bool) {
	ev, ok := parseJSON(line)
	if !ok || !ev.Usage.Has() {
		return values{}, "", false
	}
	return values{
		output:   ev.Usage.Output,
		thinking: ev.Usage.Thinking,
		total:    ev.Usage.Total,
	}, ev.Cwd, true
}

// genericSessionCwd finds the working directory in a session header, whatever
// the record is called: the first line that names one wins.
func genericSessionCwd(line []byte) (string, bool) {
	ev, ok := parseJSON(line)
	if !ok || ev.Cwd == "" {
		return "", false
	}
	return ev.Cwd, true
}

// Supported reports whether live usage can be read for an agent.
func Supported(tool string) bool {
	if _, ok := sourceFor(tool); ok {
		return true
	}
	adaptersMu.RLock()
	_, ok := adapters[tool]
	adaptersMu.RUnlock()
	if ok {
		return true
	}
	// A definition that names a transcript root is readable even before a
	// watcher has been built for it, and callers ask this to decide whether a
	// rate is possible at all.
	spec, defined := definedSpec(tool)
	return defined && len(spec.Roots) > 0
}

func home(parts ...string) string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{dir}, parts...)...)
}

// Watcher tails one agent's transcripts for one review.
type Watcher struct {
	// src is set for agents whose usage is not in files (opencode). When it
	// is, every field below that describes file state is unused.
	src usageSource
	// dirs are the spellings a source matches against, for agents that record
	// the directory they were started in rather than its resolved form.
	dirs    []string
	ad      adapter
	tool    string // the agent being followed
	dir     string // the working directory usage is attributed to
	since   time.Time
	offsets map[string]int64 // file -> bytes already accounted for
	// preexisting marks transcripts that were already on disk when the watcher
	// attached. Their earlier content belongs to another review; a file that
	// appears afterwards belongs entirely to this one.
	preexisting map[string]bool
	mtimes      map[string]int64 // file -> mtime when last read, to skip idle files
	owner       map[string]bool  // file -> belongs to this review (cached)
	cached      []string         // candidate files, refreshed on an interval
	scanned     time.Time
	// Cumulative adapters need a baseline per file: usage recorded before the
	// watcher attached belongs to an earlier review.
	base      map[string]int
	baseThink map[string]int
	seen      map[string]values // file -> this review's contribution
	total     map[string]int

	// pollMu serializes reads: the ticker goroutine and a caller's final
	// synchronous Poll both walk the same offsets and counters.
	pollMu sync.Mutex

	mu     sync.Mutex
	sample Sample
}

// Tool is the agent this watcher follows.
func (w *Watcher) Tool() string {
	if w == nil {
		return ""
	}
	return w.tool
}

// Dir is the working directory this watcher attributes usage to.
func (w *Watcher) Dir() string {
	if w == nil {
		return ""
	}
	return w.dir
}

// Watch starts reading usage for one agent working in one directory. It
// returns nil when that agent keeps no readable transcript, which callers
// should treat as "no rate available" rather than an error. Files that
// already exist are read from their current end, so a session resumed from
// an earlier review contributes only what it adds from now on.
func Watch(tool, dir string, since time.Time) *Watcher {
	if src, ok := sourceFor(tool); ok {
		return &Watcher{src: src, tool: tool, dir: resolveDir(dir), dirs: dirSpellings(dir), since: since}
	}
	adaptersMu.RLock()
	ad, ok := adapters[tool]
	adaptersMu.RUnlock()
	if !ok {
		// A defined agent carries its own transcript location. Reading it here
		// means live tokens work for it everywhere the definition is loaded,
		// with nothing to wire up at the call site.
		if spec, defined := definedSpec(tool); defined {
			if err := RegisterSpec(tool, spec); err != nil {
				return nil
			}
			adaptersMu.RLock()
			ad, ok = adapters[tool]
			adaptersMu.RUnlock()
		}
		if !ok {
			return nil
		}
	}
	w := &Watcher{
		ad: ad, tool: tool, dir: resolveDir(dir), since: since,
		offsets: map[string]int64{}, preexisting: map[string]bool{}, mtimes: map[string]int64{},
		owner: map[string]bool{}, base: map[string]int{}, baseThink: map[string]int{},
		seen: map[string]values{}, total: map[string]int{},
	}
	// Record where existing files end before anything is counted.
	for _, path := range w.candidates() {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		w.offsets[path] = fi.Size()
		w.preexisting[path] = true
		if ad.kind == cumulative {
			// Cumulative counters only make sense against what the session had
			// already spent. That value is in the bytes being skipped, so it is
			// read once here; without it the first record after attaching would
			// become the baseline and this review would measure zero forever.
			w.seedBaseline(path)
		}
	}
	// The seeding walk serves attach bookkeeping, not reads: leave the listing
	// unstamped so the first poll re-walks and sees sessions created between
	// attach and then. From that poll on, empty and non-empty listings share
	// the same rescanEvery freshness window.
	w.scanned = time.Time{}
	return w
}

// resolveDir is the form a working directory is compared in: absolute, with
// symlinks resolved, since that is what agents record.
func resolveDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs
}

// dirSpellings lists the paths a review's directory can be recorded under:
// the resolved one, and the one the caller passed if it differs. On macOS
// every temporary directory is reached through a symlink, and an agent records
// whichever spelling it was started with; there the list also carries the
// other Unicode normalization forms of each spelling, since the file system
// treats them as one directory while agents record either.
func dirSpellings(dir string) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	spellings := []string{abs}
	if resolved := resolveDir(dir); resolved != abs {
		spellings = append(spellings, resolved)
	}
	var out []string
	for _, s := range spellings {
		for _, v := range dirVariants(s) {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// baselineTailBytes bounds the seed read. Cumulative values only grow, so the
// last one in the file is the baseline, and the tail always holds it.
const baselineTailBytes = 256 << 10

// seedBaseline records what a pre-existing session had already spent.
func (w *Watcher) seedBaseline(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	if fi.Size() > baselineTailBytes {
		if _, err := f.Seek(fi.Size()-baselineTailBytes, 0); err != nil {
			return
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		v, _, ok := w.ad.parse(sc.Bytes())
		if !ok {
			continue
		}
		w.base[path] = max(w.base[path], v.output)
		w.baseThink[path] = max(w.baseThink[path], v.thinking)
	}
}

// Run polls until the context is canceled, calling onChange whenever the
// observed usage grows. It is meant to run in its own goroutine.
func (w *Watcher) Run(ctx context.Context, every time.Duration, onChange func(Sample)) {
	if w == nil {
		return
	}
	if every <= 0 {
		every = pollEvery
	}
	// A first read straight away: an agent that reports early should show a
	// rate early, rather than waiting out a tick.
	w.poll(onChange)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			w.poll(onChange) // one last read, so the tail of a run is not lost
			return
		case <-t.C:
			w.poll(onChange)
		}
	}
}

// Poll reads whatever the transcripts have gained since the last read and
// returns the total. Callers use it for a final synchronous read once the
// agent has exited, since the last records land after the process is gone.
func (w *Watcher) Poll() Sample {
	if w == nil {
		return Sample{}
	}
	// Force a fresh walk: this is the caller's last chance to see a session
	// file created seconds ago, and a run short enough to finish inside
	// rescanEvery would otherwise report nothing at all. Clearing the stamp is
	// what forces it; the listing itself is what candidates rewrites. The cost
	// is one walk per final read, not one per periodic poll: Run's ticker goes
	// through poll, which still reuses the cached listing.
	w.pollMu.Lock()
	w.scanned = time.Time{}
	w.pollMu.Unlock()
	w.poll(nil)
	return w.Sample()
}

// Sample returns the usage observed so far.
func (w *Watcher) Sample() Sample {
	if w == nil {
		return Sample{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sample
}

func (w *Watcher) poll(onChange func(Sample)) {
	w.pollMu.Lock()
	defer w.pollMu.Unlock()
	var out, thinking, total int
	if w.src != nil {
		// A source reports this review's usage in full each time, so there is
		// no per-file bookkeeping to fold in.
		v, ok := w.src.read(w.dirs, w.since)
		if !ok {
			return
		}
		out, thinking, total = v.output, v.thinking, v.total
	} else {
		for _, path := range w.candidates() {
			w.readNew(path)
		}
		for _, v := range w.seen {
			out += v.output
			thinking += v.thinking
		}
		for _, v := range w.total {
			total += v
		}
	}
	w.mu.Lock()
	grew := out > w.sample.Output || total > w.sample.Total || thinking > w.sample.Thinking
	if grew {
		w.sample = Sample{Output: out, Thinking: thinking, Total: total, At: time.Now()}
	}
	s := w.sample
	w.mu.Unlock()
	if grew && onChange != nil {
		onChange(s)
	}
}

// pollEvery is how often a transcript is re-read. It bounds how stale a live
// rate can be, so it is tighter than the directory rescan: reading one growing
// file is cheap, walking a store of thousands is not.
const pollEvery = 250 * time.Millisecond

// rescanEvery bounds how often the transcript store is walked. Session stores
// hold thousands of files and new ones appear rarely, so walking on every poll
// would cost far more than reading the one file that is growing.
const rescanEvery = time.Second

// candidates lists transcript files recent enough to belong to this review,
// reusing the previous walk for a few seconds. An empty result is cached too:
// until something matches, the store is walked once per rescanEvery rather
// than on every poll, and a session created in between surfaces when the
// window expires, the same bound a non-empty listing already works under.
func (w *Watcher) candidates() []string {
	if !w.scanned.IsZero() && time.Since(w.scanned) < rescanEvery {
		return w.cached
	}
	cutoff := w.since.Add(-2 * time.Minute)
	var out []string
	for _, root := range w.ad.roots(w.dir) {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(path, w.ad.suffix) {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				return nil
			}
			out = append(out, path)
			return nil
		})
	}
	w.cached, w.scanned = out, time.Now()
	return out
}

// readNew consumes the bytes appended to one transcript since the last poll.
//
// Polling runs four times a second over every recent transcript, and most of
// them are idle, so the mtime check happens on a plain stat: an untouched file
// costs one syscall instead of open+stat+close.
//
// Newline-terminated lines are always counted; a trailing fragment without
// its final newline is counted too, but only once it parses in full, and
// only then are its bytes committed to the offset. Half-written records are
// therefore re-read whole next poll instead of being silently lost, and a
// read error mid-file retries the whole span without having credited
// anything.
func (w *Watcher) readNew(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	mt := fi.ModTime().UnixNano()
	if w.mtimes[path] == mt {
		return // untouched since the last read
	}
	w.mtimes[path] = mt
	mine, decided := w.owns(path)
	if !decided {
		// No verdict yet: commit no offset. A file created after this review
		// started belongs to it in full once its header appears, and skipping
		// now would discard exactly those bytes; a pre-existing file's prefix
		// predates the review, so advancing past it stays safe.
		if w.preexisting[path] {
			w.offsets[path] = fi.Size()
		}
		return
	}
	if !mine {
		w.offsets[path] = fi.Size() // keep skipping it cheaply
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	off := w.offsets[path]
	if fi.Size() < off {
		off = 0 // truncated or rotated: start over
	}
	if fi.Size() == off {
		return
	}
	if _, err := f.Seek(off, 0); err != nil {
		return
	}

	// Collect first, fold in afterwards: a record may only be counted
	// together with the offset that skips it.
	var recs []values
	var (
		line    []byte
		discard bool
		pos     = off
		// complete is the offset just past the last newline seen; it is the
		// only value safe to commit.
		complete = off
	)
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		chunk, rerr := br.ReadSlice('\n')
		pos += int64(len(chunk))
		if rerr == bufio.ErrBufferFull {
			if !discard {
				line = append(line, chunk...)
				if len(line) > maxLineBytes {
					// One record larger than the cap cannot parse; drop it
					// and resume at the next newline rather than stalling
					// every later record in this file behind it.
					discard = true
					line = line[:0]
				}
			}
			continue
		}
		if rerr == nil {
			if !discard {
				line = append(line, chunk...)
				l := line[:len(line)-1] // drop the newline
				if n := len(l); n > 0 && l[n-1] == '\r' {
					l = l[:n-1]
				}
				recs = w.collect(recs, l)
			}
			line = line[:0]
			complete = pos
			continue
		}
		if errors.Is(rerr, io.EOF) {
			// A trailing fragment is either a complete record whose writer
			// omitted the final newline, or half of one mid-write. It counts
			// only when it parses in full: a torn prefix stays uncommitted
			// and is re-read whole next poll instead of being lost.
			if !discard && len(chunk) > 0 {
				line = append(line, chunk...)
				if _, _, ok := w.ad.parse(line); ok {
					recs = w.collect(recs, line)
					complete = pos
				}
			}
			break
		}
		return // read failed: nothing counted, offset unchanged, retried next poll
	}
	for _, v := range recs {
		w.applyRecord(path, v)
	}
	w.offsets[path] = complete
}

// maxLineBytes bounds one transcript record: a single JSONL line larger than
// this is junk no parser here accepts.
const maxLineBytes = 8 << 20

// collect parses one complete line and appends its values when it carries
// usage belonging to this review.
func (w *Watcher) collect(recs []values, line []byte) []values {
	v, cwd, ok := w.ad.parse(line)
	if !ok {
		return recs
	}
	// A transcript that names a different working directory belongs to
	// another review, or another project entirely.
	if cwd != "" && !w.sameDir(cwd) {
		return recs
	}
	return append(recs, v)
}

// applyRecord folds one counted record into the per-file bookkeeping.
func (w *Watcher) applyRecord(path string, v values) {
	switch w.ad.kind {
	case perMessage:
		cur := w.seen[path]
		cur.output += v.output
		cur.thinking += v.thinking
		w.seen[path] = cur
		// Output accrues per message; a "total" on a per-message record is
		// the context size at that point, so summing it would be
		// meaningless. The largest one seen is the honest figure.
		w.total[path] = max(w.total[path], v.total)
	case cumulative:
		if _, have := w.base[path]; !have {
			// Attaching mid-session, everything before now belongs to an
			// earlier review, so the first value seen is the baseline. A
			// session that only appeared after the review started is ours
			// in full, and baselining it would throw the first reading
			// away, which is most of a short review.
			if w.preexisting[path] {
				w.base[path] = v.output
				w.baseThink[path] = v.thinking
			} else {
				w.base[path], w.baseThink[path] = 0, 0
			}
		}
		cur := w.seen[path]
		if d := v.output - w.base[path]; d > cur.output {
			cur.output = d
		}
		if d := v.thinking - w.baseThink[path]; d > cur.thinking {
			cur.thinking = d
		}
		w.seen[path] = cur
		if v.total > w.total[path] {
			w.total[path] = v.total
		}
	}
}

// ownerScanLines bounds the header search. These formats carry the working
// directory in the opening record, so a file this deep without one has none.
const ownerScanLines = 20

// owns reports whether a transcript belongs to this review. For agents whose
// usage lines repeat the cwd there is nothing to decide here; for the others
// the session header at the top of the file is read once and cached.
//
// The second return value says whether the verdict is final. A file caught
// between creation and its header flush yields no verdict yet: caching a
// refusal there would blackhole a session that in fact belongs to this
// review, so the decision is retried on a later poll instead.
func (w *Watcher) owns(path string) (mine, decided bool) {
	if w.ad.sessionCwd == nil {
		return true, true // decided per line instead
	}
	if mine, known := w.owner[path]; known {
		return mine, true
	}
	f, err := os.Open(path)
	if err != nil {
		return false, false // transient: retry next poll
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	lines := 0
	for lines < ownerScanLines && sc.Scan() {
		if cwd, ok := w.ad.sessionCwd(sc.Bytes()); ok {
			mine := w.sameDir(cwd)
			w.owner[path] = mine
			return mine, true
		}
		lines++
	}
	if lines < ownerScanLines {
		// The file ended before the cap: the header may simply not be
		// written yet. Stay undecided and uncached.
		return false, false
	}
	// The scan cap was reached with no header anywhere in it, so there is
	// nothing to wait for: refuse durably rather than credit another
	// project's tokens to this review.
	w.owner[path] = false
	return false, true
}

func (w *Watcher) sameDir(cwd string) bool {
	if sameSpelling(cwd, w.dir) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	return err == nil && sameSpelling(resolved, w.dir)
}

// parseClaude reads one line of a Claude Code transcript. Assistant messages
// carry per-message usage, so the values are added up. A negative counter is
// not a measurement: it is clamped to absent, the same rule the generic
// walker applies, so a corrupted or hostile line cannot subtract from a total.
func parseClaude(line []byte) (values, string, bool) {
	var rec struct {
		Type    string `json:"type"`
		Cwd     string `json:"cwd"`
		Message struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				Details                  struct {
					ThinkingTokens int `json:"thinking_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Type != "assistant" {
		return values{}, "", false
	}
	u := rec.Message.Usage
	out := counter(u.OutputTokens)
	in := counter(u.InputTokens)
	if out == 0 && in == 0 {
		return values{}, "", false
	}
	sum := satAdd(satAdd(in, out),
		satAdd(counter(u.CacheReadInputTokens), counter(u.CacheCreationInputTokens)))
	return values{output: out, thinking: counter(u.Details.ThinkingTokens), total: sum}, rec.Cwd, true
}

// parseQwen reads one line of a qwen-code chat transcript. Usage is recorded
// per assistant message, and thinking tokens are output tokens too.
func parseQwen(line []byte) (values, string, bool) {
	var rec struct {
		Type  string `json:"type"`
		Cwd   string `json:"cwd"`
		Usage struct {
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Type != "assistant" {
		return values{}, "", false
	}
	u := rec.Usage
	total := counter(u.TotalTokenCount)
	cand := counter(u.CandidatesTokenCount)
	if total == 0 && cand == 0 {
		return values{}, "", false
	}
	thoughts := counter(u.ThoughtsTokenCount)
	return values{
		output:   satAdd(cand, thoughts),
		thinking: thoughts,
		total:    total,
	}, rec.Cwd, true
}

// codexSessionCwd reads the working directory from a codex session header.
func codexSessionCwd(line []byte) (string, bool) {
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Type != "session_meta" {
		return "", false
	}
	if rec.Payload.Cwd == "" {
		return "", false
	}
	return rec.Payload.Cwd, true
}

// parseCodex reads one line of a codex rollout. Its token_count events carry
// the session total, so the values are absolute.
func parseCodex(line []byte) (values, string, bool) {
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Cwd  string `json:"cwd"`
			Info struct {
				TotalTokenUsage struct {
					OutputTokens          int `json:"output_tokens"`
					ReasoningOutputTokens int `json:"reasoning_output_tokens"`
					TotalTokens           int `json:"total_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return values{}, "", false
	}
	// session_meta names the directory; token_count carries the numbers.
	if rec.Payload.Type == "token_count" {
		u := rec.Payload.Info.TotalTokenUsage
		total := counter(u.TotalTokens)
		out := counter(u.OutputTokens)
		if total == 0 && out == 0 {
			return values{}, "", false
		}
		return values{
			output:   out,
			thinking: counter(u.ReasoningOutputTokens),
			total:    total,
		}, "", true
	}
	return values{}, "", false
}

// satAdd sums two non-negative counters, saturating instead of wrapping: a
// transcript with absurd counts must read as enormous, never as negative.
func satAdd(a, b int) int {
	s := a + b
	if s < 0 {
		return math.MaxInt
	}
	return s
}
