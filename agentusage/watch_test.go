// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The fixtures below are the real record shapes, reduced to the fields this
// package reads. They were taken from live transcripts written by the agents
// themselves, so a format change shows up here as a failing test rather than
// as a silently missing number.

// jsonPath quotes a path the way a real transcript does. It matters on
// Windows, whose separator is JSON's escape character.
func jsonPath(p string) string {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func claudeLine(cwd string, out int) string {
	return `{"type":"assistant","cwd":` + jsonPath(cwd) + `,"timestamp":"2026-08-25T00:00:00.000Z",` +
		`"message":{"role":"assistant","usage":{"input_tokens":2,"cache_creation_input_tokens":100,` +
		`"cache_read_input_tokens":0,"output_tokens":` + strconv.Itoa(out) + `}}}`
}

func codexMeta(cwd string) string {
	return `{"type":"session_meta","payload":{"id":"x","cwd":` + jsonPath(cwd) + `,"cli_version":"1"}}`
}

func codexTokens(out, total int) string {
	return `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":` +
		`{"input_tokens":10,"output_tokens":` + strconv.Itoa(out) + `,"total_tokens":` + strconv.Itoa(total) + `}}}}`
}

// withStore points an adapter at a temporary transcript directory.
func withStore(t *testing.T, tool string) string {
	t.Helper()
	dir := t.TempDir()
	orig := adapters[tool]
	patched := orig
	patched.roots = func(string) []string { return []string{dir} }
	adapters[tool] = patched
	t.Cleanup(func() { adapters[tool] = orig })
	return dir
}

func append_(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeUsageIsSummedPerMessage(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl")

	w := Watch("claude", work, time.Now())
	if w == nil {
		t.Fatal("claude should be supported")
	}
	append_(t, path, claudeLine(work, 100), claudeLine(work, 250))
	w.poll(nil)
	if got := w.Sample().Output; got != 350 {
		t.Fatalf("output tokens %d, want 350", got)
	}
	// Only the new lines are counted on the next poll.
	append_(t, path, claudeLine(work, 50))
	w.poll(nil)
	if got := w.Sample().Output; got != 400 {
		t.Fatalf("output tokens %d, want 400", got)
	}
}

func TestClaudeIgnoresOtherProjects(t *testing.T) {
	store := withStore(t, "claude")
	work, other := t.TempDir(), t.TempDir()

	w := Watch("claude", work, time.Now())
	append_(t, filepath.Join(store, "mine.jsonl"), claudeLine(work, 10))
	append_(t, filepath.Join(store, "theirs.jsonl"), claudeLine(other, 9999))
	w.poll(nil)
	if got := w.Sample().Output; got != 10 {
		t.Fatalf("another project's tokens leaked in: %d", got)
	}
}

func TestExistingContentIsNotCounted(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "resumed.jsonl")
	// A session that already ran before this review started, as
	// --continue-sessions produces.
	append_(t, path, claudeLine(work, 5000))

	w := Watch("claude", work, time.Now())
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("counted an earlier review's tokens: %d", got)
	}
	append_(t, path, claudeLine(work, 42))
	w.poll(nil)
	if got := w.Sample().Output; got != 42 {
		t.Fatalf("output tokens %d, want 42", got)
	}
}

func TestCodexCumulativeValuesAreRebased(t *testing.T) {
	store := withStore(t, "codex")
	work := t.TempDir()
	path := filepath.Join(store, "rollout-1.jsonl")

	// A session that was already running before this review began: its earlier
	// usage belongs to whoever ran it, so it is the baseline.
	append_(t, path, codexMeta(work), codexTokens(1000, 5000))

	w := Watch("codex", work, time.Now())
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("baseline was counted as usage: %d", got)
	}
	append_(t, path, codexTokens(1750, 6200))
	w.poll(nil)
	if got := w.Sample().Output; got != 750 {
		t.Fatalf("output tokens %d, want 750 (1750 minus the 1000 baseline)", got)
	}
}

func TestCumulativeSessionStartedDuringTheReviewCountsInFull(t *testing.T) {
	store := withStore(t, "codex")
	work := t.TempDir()

	// Nothing exists yet: the agent creates its session after the watcher
	// attaches, so every token in it belongs to this review. Baselining it
	// would throw away the first reading, which on a short review is most of
	// the tokens and all of the early rate.
	w := Watch("codex", work, time.Now())
	path := filepath.Join(store, "rollout-new.jsonl")
	append_(t, path, codexMeta(work), codexTokens(400, 900))
	w.poll(nil)
	if got := w.Sample().Output; got != 400 {
		t.Fatalf("output tokens %d, want 400", got)
	}
	append_(t, path, codexTokens(1300, 2400))
	w.poll(nil)
	if got := w.Sample().Output; got != 1300 {
		t.Fatalf("output tokens %d, want 1300", got)
	}
}

func TestCodexIgnoresSessionsFromOtherDirectories(t *testing.T) {
	store := withStore(t, "codex")
	work, other := t.TempDir(), t.TempDir()

	w := Watch("codex", work, time.Now())
	append_(t, filepath.Join(store, "rollout-other.jsonl"), codexMeta(other), codexTokens(0, 0), codexTokens(9999, 12345))
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("another directory's session was counted: %d", got)
	}
}

func TestQwenUsageMetadataIsSummed(t *testing.T) {
	store := withStore(t, "qwen")
	work := t.TempDir()
	path := filepath.Join(store, "chat.jsonl")

	w := Watch("qwen", work, time.Now())
	if w == nil {
		t.Fatal("qwen should be supported")
	}
	// Thinking tokens are output tokens too, so both are counted.
	append_(t, path,
		`{"type":"assistant","cwd":`+jsonPath(work)+`,"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":205,"thoughtsTokenCount":82,"totalTokenCount":387}}`,
		`{"type":"user","cwd":`+jsonPath(work)+`,"message":{"role":"user"}}`,
		`{"type":"assistant","cwd":`+jsonPath(work)+`,"usageMetadata":{"candidatesTokenCount":13,"totalTokenCount":420}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 300 {
		t.Fatalf("output tokens %d, want 300 (205+82+13)", got)
	}
	// totalTokenCount is the context size of each request, not a per-message
	// cost, so the largest is the honest figure. Summing them would count the
	// same prompt once per turn.
	if got := w.Sample().Total; got != 420 {
		t.Fatalf("total tokens %d, want 420 (the largest context, not the sum)", got)
	}
}

func TestUnsupportedAgentYieldsNoWatcher(t *testing.T) {
	// gemini's transcripts on the machines checked carry no usage records.
	if Supported("gemini") {
		t.Fatal("gemini is not implemented yet and must not claim to be")
	}
	if w := Watch("gemini", t.TempDir(), time.Now()); w != nil {
		t.Fatal("expected no watcher for an unsupported agent")
	}
	// A nil watcher must be safe to use, so callers never branch.
	var nilW *Watcher
	nilW.Run(context.Background(), time.Millisecond, nil)
	if !nilW.Sample().Empty() {
		t.Fatal("nil watcher reported usage")
	}
}

// Rate is what the UI shows as tok/s for an agent. It must refuse to invent
// a number when there is no measurable interval or no growth: a stalled or
// restarted counter is silence, not zero throughput forever.
func TestRateNeedsPositiveSpanAndGrowth(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	cases := []struct {
		name   string
		prev   Sample
		cur    Sample
		want   float64
		wantOK bool
	}{
		{"growth over one second", Sample{Output: 100, At: t0}, Sample{Output: 350, At: t0.Add(time.Second)}, 250, true},
		{"same instant is not an interval", Sample{Output: 100, At: t0}, Sample{Output: 350, At: t0}, 0, false},
		{"clock went backwards", Sample{Output: 100, At: t0}, Sample{Output: 350, At: t0.Add(-time.Second)}, 0, false},
		{"counter did not move", Sample{Output: 100, At: t0}, Sample{Output: 100, At: t0.Add(time.Second)}, 0, false},
		{"counter reset to zero", Sample{Output: 100, At: t0}, Sample{}, 0, false},
	}
	for _, c := range cases {
		got, ok := Rate(c.prev, c.cur)
		if ok != c.wantOK || got != c.want {
			t.Errorf("%s: Rate(%+v, %+v) = %v,%v want %v,%v",
				c.name, c.prev, c.cur, got, ok, c.want, c.wantOK)
		}
	}
}

func TestRunReportsGrowth(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "live.jsonl")

	w := Watch("claude", work, time.Now())
	ctx := t.Context()

	got := make(chan Sample, 8)
	go w.Run(ctx, 20*time.Millisecond, func(s Sample) { got <- s })

	append_(t, path, claudeLine(work, 120))
	select {
	case s := <-got:
		if s.Output != 120 {
			t.Fatalf("reported %d, want 120", s.Output)
		}
	// Generous on purpose: this asserts that growth is reported at all, and a
	// loaded CI runner should not turn that into a failure.
	case <-time.After(10 * time.Second):
		t.Fatal("no usage reported")
	}
}

func TestDefinedAgentTranscriptsAreReadGenerically(t *testing.T) {
	// An agent gauntlet was never compiled to know about: its transcript is
	// readable as long as the records carry recognizable counters and a cwd.
	store := t.TempDir()
	work := t.TempDir()
	if err := RegisterSpec("piclone", Spec{Roots: []string{store}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adaptersMu.Lock()
		delete(adapters, "piclone")
		adaptersMu.Unlock()
	})
	if !Supported("piclone") {
		t.Fatal("a registered spec should make the agent supported")
	}

	w := Watch("piclone", work, time.Now())
	if w == nil {
		t.Fatal("expected a watcher")
	}
	path := filepath.Join(store, "session.jsonl")
	append_(t, path,
		`{"role":"assistant","cwd":`+jsonPath(work)+`,"usage":{"output_tokens":140,"reasoning_tokens":40}}`,
		`{"role":"assistant","cwd":`+jsonPath(work)+`,"usage":{"output_tokens":60}}`)
	w.poll(nil)
	if got := w.Sample(); got.Output != 200 || got.Thinking != 40 {
		t.Fatalf("got %+v, want output 200 and thinking 40", got)
	}
}

func TestDefinedAgentIgnoresOtherDirectories(t *testing.T) {
	store, work, other := t.TempDir(), t.TempDir(), t.TempDir()
	if err := RegisterSpec("piclone2", Spec{Roots: []string{store}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adaptersMu.Lock()
		delete(adapters, "piclone2")
		adaptersMu.Unlock()
	})
	w := Watch("piclone2", work, time.Now())
	append_(t, filepath.Join(store, "s.jsonl"),
		`{"role":"assistant","cwd":`+jsonPath(other)+`,"usage":{"output_tokens":5000}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("another directory's usage leaked in: %d", got)
	}
}

func TestRegisterSpecValidates(t *testing.T) {
	if err := RegisterSpec("", Spec{Roots: []string{"/tmp"}}); err == nil {
		t.Error("a nameless spec should be rejected")
	}
	if err := RegisterSpec("x", Spec{}); err == nil {
		t.Error("a spec with no roots should be rejected")
	}
}

func TestClankerLogIsReadFromTheProjectItself(t *testing.T) {
	// clanker writes state/token_stats.jsonl inside the repository it runs in,
	// one record per request and no cwd field: the location is the attribution.
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := Watch("clanker", work, time.Now())
	if w == nil {
		t.Fatal("clanker should be supported")
	}
	path := filepath.Join(work, "state", "token_stats.jsonl")
	append_(t, path,
		`{"ts":1786923403,"provider":"x","model":"y","prompt_tokens":32027,"completion_tokens":1663,"total_tokens":33690,"ok":true}`,
		`{"ts":1786923500,"provider":"x","model":"y","prompt_tokens":100,"completion_tokens":337,"total_tokens":437,"ok":true}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 2000 {
		t.Fatalf("output tokens %d, want 2000 (1663+337)", got)
	}
}

func TestDirPlaceholderInDefinedRoots(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, ".logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RegisterSpec("inproject", Spec{Roots: []string{"{dir}/.logs"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adaptersMu.Lock()
		delete(adapters, "inproject")
		adaptersMu.Unlock()
	})
	w := Watch("inproject", work, time.Now())
	append_(t, filepath.Join(work, ".logs", "usage.jsonl"),
		`{"usage":{"output_tokens":77}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 77 {
		t.Fatalf("output tokens %d, want 77", got)
	}
}

// GitHub Copilot CLI records the working directory once, in the session.start
// event, and the usage on later assistant.message events.
func TestCopilotCliEventsAreRead(t *testing.T) {
	store := withStore(t, "copilot")
	work := t.TempDir()
	path := copilotSession(t, store, "sess-1", work)

	w := Watch("copilot", work, time.Now())
	if w == nil {
		t.Fatal("copilot should be readable")
	}
	append_(t, path,
		`{"type":"assistant.message","id":"e1","data":{"messageId":"m1","usage":{"prompt_tokens":900,"completion_tokens":120,"total_tokens":1020}}}`,
		`{"type":"assistant.message","id":"e2","data":{"messageId":"m2","usage":{"prompt_tokens":940,"completion_tokens":80,"total_tokens":1100}}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 200 {
		t.Fatalf("output tokens %d, want 200 (120+80)", got)
	}
}

// Another project's Copilot session must not land in this review.
func TestCopilotCliIgnoresOtherProjects(t *testing.T) {
	store := withStore(t, "copilot")
	work, other := t.TempDir(), t.TempDir()
	mine := copilotSession(t, store, "mine", work)
	theirs := copilotSession(t, store, "theirs", other)

	w := Watch("copilot", work, time.Now())
	append_(t, mine, `{"type":"assistant.message","data":{"usage":{"completion_tokens":70}}}`)
	append_(t, theirs, `{"type":"assistant.message","data":{"usage":{"completion_tokens":5000}}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 70 {
		t.Fatalf("output tokens %d, want 70: another project's session leaked in", got)
	}
}

// A transcript caught between file creation and its header flush has no
// verdict yet: the empty first sight must not harden into a permanent
// "not mine" that silently drops the whole session.
func TestHeaderNotYetWrittenIsRetriedNotCached(t *testing.T) {
	store := withStore(t, "copilot")
	work := t.TempDir()
	path := filepath.Join(store, "sess-late", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	w := Watch("copilot", work, time.Now())
	append_(t, path) // create the file empty: the agent has not flushed its header
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("output tokens %d, want 0: an empty undecided file counts for nothing yet", got)
	}

	append_(t, path,
		`{"type":"session.start","id":"e0","data":{"sessionId":"s","context":{"cwd":`+jsonPath(work)+`}}}`,
		`{"type":"assistant.message","data":{"usage":{"completion_tokens":120}}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 120 {
		t.Fatalf("output tokens %d, want 120: a headerless first sight was cached as not ours", got)
	}
}

// A file deep enough to rule out a pending header is refused for good, even
// when usage records show up later: without a header there is nothing to
// attribute them to.
func TestHeaderlessFilePastScanCapIsRefusedDurably(t *testing.T) {
	store := withStore(t, "copilot")
	work, other := t.TempDir(), t.TempDir()
	path := filepath.Join(store, "sess-orphan", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	w := Watch("copilot", work, time.Now())
	for i := 0; i < ownerScanLines; i++ {
		append_(t, path, `{"type":"other.event"}`)
	}
	append_(t, path, `{"type":"assistant.message","data":{"usage":{"completion_tokens":999}}}`)
	w.poll(nil)

	// A late header naming this directory must not resurrect it either: the
	// refusal was earned, and re-reading history would misattribute it.
	append_(t, path,
		`{"type":"session.start","id":"e0","data":{"sessionId":"s","context":{"cwd":`+jsonPath(other)+`}}}`,
		`{"type":"assistant.message","data":{"usage":{"completion_tokens":40}}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("output tokens %d, want 0: a headerless file was credited to this review", got)
	}
}

// A session file that shrinks and is rewritten belongs to whoever its new
// header names: the owner verdict from the previous contents must not stick.
func TestTruncatedSessionRechecksOwner(t *testing.T) {
	store := withStore(t, "copilot")
	work, other := t.TempDir(), t.TempDir()
	path := copilotSession(t, store, "reused", other)

	w := Watch("copilot", work, time.Now())
	append_(t, path, `{"type":"assistant.message","data":{"usage":{"completion_tokens":5000}}}`)
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("output tokens %d, want 0: another project's session leaked in", got)
	}

	if err := os.WriteFile(path, []byte(
		`{"type":"session.start","id":"e0","data":{"sessionId":"s","context":{"cwd":`+jsonPath(work)+`}}}`+"\n"+
			`{"type":"assistant.message","data":{"usage":{"completion_tokens":70}}}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 70 {
		t.Fatalf("output tokens %d, want 70: a rewritten file kept the previous owner", got)
	}
}

// copilotSession starts one session directory with its session.start header
// and returns the events file to append to.
func copilotSession(t *testing.T, store, name, cwd string) string {
	t.Helper()
	dir := filepath.Join(store, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	append_(t, path, `{"type":"session.start","id":"e0","data":{"sessionId":"s","context":{"cwd":`+jsonPath(cwd)+`}}}`)
	return path
}

// appendRaw writes bytes verbatim; unlike append_ it adds no newline, so a
// test can stage torn or unterminated transcript lines.
func appendRaw(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// A record flushed in two chunks must be counted exactly once: the torn
// prefix waits for its remainder instead of being consumed and lost.
func TestTornRecordIsCountedOnceComplete(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl")

	w := Watch("claude", work, time.Now())
	rest := claudeLine(work, 250)
	half := len(rest) / 2
	appendRaw(t, path, claudeLine(work, 100)+"\n"+rest[:half])
	w.poll(nil)
	if got := w.Sample().Output; got != 100 {
		t.Fatalf("output tokens %d, want 100 (a torn record counts for nothing yet)", got)
	}
	appendRaw(t, path, rest[half:]+"\n")
	w.poll(nil)
	if got := w.Sample().Output; got != 350 {
		t.Fatalf("output tokens %d, want 350: completing a torn record lost it", got)
	}
}

// A complete final line without a trailing newline counts now and must not
// misalign the offset against records appended afterwards.
func TestFinalLineWithoutNewlineSurvivesGrowth(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl")

	w := Watch("claude", work, time.Now())
	appendRaw(t, path, claudeLine(work, 100))
	w.poll(nil)
	if got := w.Sample().Output; got != 100 {
		t.Fatalf("output tokens %d, want 100", got)
	}
	appendRaw(t, path, "\n"+claudeLine(work, 250))
	w.poll(nil)
	if got := w.Sample().Output; got != 350 {
		t.Fatalf("output tokens %d, want 350: growth after an unterminated line lost a record", got)
	}
}

// One record over the size cap is junk, but it must not stall every later
// record in the same file behind it.
func TestOversizedLineDoesNotStallFollowingRecords(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl")

	w := Watch("claude", work, time.Now())
	appendRaw(t, path, strings.Repeat("x", maxLineBytes+1)+"\n")
	appendRaw(t, path, claudeLine(work, 42))
	w.poll(nil)
	if got := w.Sample().Output; got != 42 {
		t.Fatalf("output tokens %d, want 42: an oversized line stalled the file", got)
	}
}

// A transcript that grows without its mtime moving must still be read. NTFS
// timestamps are coarse and Windows updates last-write lazily for an open
// handle, so two appends can share one stamp; skipping the second would drop
// every record it carried.
func TestGrowthIsReadWhenTheMtimeStandsStill(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "s.jsonl")
	append_(t, path, claudeLine(work, 5))

	w := Watch("claude", work, time.Now())
	w.poll(nil)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	append_(t, path, claudeLine(work, 42))
	// Put the timestamp back where the first read saw it, which is what a
	// coarse clock does on its own.
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 42 {
		t.Fatalf("output tokens %d, want 42: growth under an unchanged mtime was skipped", got)
	}
}

// A failed open must not mark the current size as processed: restoring
// access and polling again has to see the records that were already there.
func TestReadErrorIsNotCachedAsProcessed(t *testing.T) {
	store := withStore(t, "claude")
	work := t.TempDir()
	path := filepath.Join(store, "session.jsonl")

	w := Watch("claude", work, time.Now())
	append_(t, path, claudeLine(work, 100))
	if err := os.Chmod(path, 0); err != nil {
		t.Skip("chmod not supported")
	}
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		_ = os.Chmod(path, 0o644)
		t.Skip("process can read mode-0 files")
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 0 {
		t.Fatalf("output tokens %d, want 0: an unreadable file counted", got)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	w.poll(nil)
	if got := w.Sample().Output; got != 100 {
		t.Fatalf("output tokens %d, want 100: a failed read was cached as processed", got)
	}
}
