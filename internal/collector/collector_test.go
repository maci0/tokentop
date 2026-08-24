package collector

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tokentop/internal/core"
	"tokentop/internal/provider"
)

type fakeProvider struct {
	label string
	addr  string // defaults to "fake://<label>"
	m     *provider.Metrics
	err   error
}

func (f *fakeProvider) Label() string { return f.label }
func (f *fakeProvider) Addr() string {
	if f.addr != "" {
		return f.addr
	}
	return "fake://" + f.label
}
func (f *fakeProvider) Poll(context.Context) (*provider.Metrics, error) {
	return f.m, f.err
}

func TestRatesDeriveAndSmooth(t *testing.T) {
	c := New(nil, time.Second)
	now := time.Now()

	out, in := c.rates("p", &provider.Metrics{OutTotal: 100}, now)
	if out != 0 || in != 0 {
		t.Fatalf("first sample must seed baseline, got %v/%v", out, in)
	}
	out, _ = c.rates("p", &provider.Metrics{OutTotal: 400}, now.Add(time.Second))
	if out < 104 || out > 106 { // raw rate 300 tok/s smoothed with alpha .35 from 0
		t.Fatalf("smoothed rate = %v", out)
	}
	out, _ = c.rates("p", &provider.Metrics{OutTotal: 700}, now.Add(2*time.Second))
	if out <= 105 || out >= 300 {
		t.Fatalf("second smoothing step = %v", out)
	}
}

func TestRateCounterResetClampsToZero(t *testing.T) {
	c := New(nil, time.Second)
	c.rates("p", &provider.Metrics{OutTotal: 1000}, time.Now())
	out, _ := c.rates("p", &provider.Metrics{OutTotal: 5}, time.Now().Add(time.Second))
	if out != 0 {
		t.Fatalf("counter reset produced %v, want 0", out)
	}
}

// A sample arriving with zero elapsed time cannot produce a rate: 0/0 is NaN
// and n/0 is +Inf, and the EMA would carry either into every later sample.
// The rate must hold its prior value and the baseline must stay put so the
// next real interval still accounts for the tokens seen at the dup timestamp.
func TestRatesZeroElapsedHoldsPriorRate(t *testing.T) {
	c := New(nil, time.Second)
	now := time.Now()
	c.rates("p", &provider.Metrics{OutTotal: 100}, now)
	out, _ := c.rates("p", &provider.Metrics{OutTotal: 400}, now.Add(time.Second))

	held, heldIn := c.rates("p", &provider.Metrics{OutTotal: 900}, now.Add(time.Second))
	if math.IsNaN(held) || math.IsInf(held, 0) {
		t.Fatalf("zero-elapsed sample produced %v", held)
	}
	if held != out || heldIn != 0 {
		t.Fatalf("zero-elapsed sample moved rates to %v/%v, want %v/0", held, heldIn, out)
	}

	next, _ := c.rates("p", &provider.Metrics{OutTotal: 1000}, now.Add(2*time.Second))
	if next <= held { // delta 100 over the full 1s window, not 700
		t.Fatalf("rate after held sample = %v, want > %v", next, held)
	}
}

// Engines that publish instantaneous tok/s gauges (SGLang, TRT-LLM) must feed
// the rate directly instead of counter deltas.
func TestRatesHonorDirectThroughput(t *testing.T) {
	c := New(nil, time.Second)
	c.rates("p", &provider.Metrics{OutTotal: 10}, time.Now())
	out, _ := c.rates("p", &provider.Metrics{OutTotal: 10, DirectOutPS: 300}, time.Now().Add(time.Second))
	if out < 104 || out > 106 { // ema(0, 300)
		t.Fatalf("direct rate = %v", out)
	}
}

func TestHistoryRingCap(t *testing.T) {
	r := &timedRing{}
	t0 := time.Now()
	for i := 0; i < core.HistoryLen+10; i++ {
		r.push(float64(i), t0.Add(time.Duration(i)*time.Second), time.Second)
	}
	vals := r.copy()
	if len(vals) != core.HistoryLen {
		t.Fatalf("ring len = %d, want %d", len(vals), core.HistoryLen)
	}
	if vals[len(vals)-1] != float64(core.HistoryLen+9) {
		t.Fatal("newest sample lost")
	}
	if vals[0] != 10 { // oldest slid off in order: 0..9 evicted, 10 is now first
		t.Fatal("ring order broken after wrap")
	}
	// oldest timestamp must slide forward one cadence per evicted sample
	wantT0 := t0.Add(10 * time.Second)
	if !r.t0.Equal(wantT0) {
		t.Fatalf("t0 = %v, want %v", r.t0, wantT0)
	}
}

// Once warm, push must stop allocating: the ring reuses one fixed buffer for
// its lifetime instead of sliding a slice (which reallocs on every push).
func TestHistoryRingDoesNotGrowBuffer(t *testing.T) {
	r := &timedRing{}
	for i := 0; i < core.HistoryLen*3; i++ {
		r.push(float64(i), time.Unix(int64(i), 0), time.Second)
	}
	if cap(r.buf) != core.HistoryLen {
		t.Fatalf("buffer capacity = %d, want exactly %d", cap(r.buf), core.HistoryLen)
	}
	vals := r.copy()
	if want := float64(core.HistoryLen * 2); vals[0] != want {
		t.Fatalf("oldest sample = %v, want %v", vals[0], want)
	}
}

func TestEmitSnapshotShape(t *testing.T) {
	fp := &fakeProvider{label: "testprov", m: &provider.Metrics{
		Models: []core.ModelInfo{{Name: "m1"}}, Running: 2,
	}}
	ch := make(chan core.Snapshot, 1)
	c := New([]provider.Provider{fp}, time.Hour)
	c.sysFn = func() core.SysSample {
		return core.SysSample{MemTotal: 100, MemUsed: 50, Load1: 0.5,
			Temps: []core.TempReading{{Label: "package", MilliC: 45000}}}
	}
	done := make(chan struct{})
	go func() { c.emit(context.Background(), ch); close(done) }()

	select {
	case snap := <-ch:
		if snap.Sys == nil || snap.Sys.MemUsed != 50 || len(snap.Sys.Temps) != 1 {
			t.Fatalf("sys sample missing from snapshot: %+v", snap.Sys)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("emit did not produce a snapshot")
	}
	<-done
}

// Host-vitals sampling happens off the emit path: the background poller
// fills the cache, and a warm emit must never invoke the (potentially
// seconds-slow) sampler itself.
func TestEmitUsesCachedSysSample(t *testing.T) {
	c := New(nil, time.Hour)
	var calls atomic.Int32
	c.SetSysFn(func() core.SysSample {
		calls.Add(1)
		return core.SysSample{MemTotal: 7}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.startSysPoller(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for c.sysSnapshot() == nil {
		if time.Now().After(deadline) {
			t.Fatal("background poller never cached a sample")
		}
		time.Sleep(time.Millisecond)
	}
	before := calls.Load()

	ch := make(chan core.Snapshot, 1)
	done := make(chan struct{})
	go func() { defer close(done); c.emit(context.Background(), ch) }()
	var snap core.Snapshot
	select {
	case snap = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("emit did not produce a snapshot")
	}
	<-done
	if snap.Sys == nil || snap.Sys.MemTotal != 7 {
		t.Fatalf("cached sys sample missing from snapshot: %+v", snap.Sys)
	}
	if got := calls.Load(); got != before {
		t.Fatalf("emit invoked the sampler inline (%d -> %d calls)", before, got)
	}
}

// Run is the production loop: it must start the background proc+sys pollers,
// emit one snapshot per interval (including the immediate first frame) and
// stop promptly on ctx cancellation without stranding the emit goroutine.
func TestRunEmitsUntilCancel(t *testing.T) {
	fp := &fakeProvider{label: "run", m: &provider.Metrics{
		OutTotal: 1, Models: []core.ModelInfo{{Name: "m"}},
	}}
	ch := make(chan core.Snapshot)
	ctx, cancel := context.WithCancel(context.Background())
	c := New([]provider.Provider{fp}, 5*time.Millisecond)
	c.SetSysFn(func() core.SysSample { return core.SysSample{MemTotal: 9} })

	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx, ch) }()

	for i := 0; i < 2; i++ {
		select {
		case snap := <-ch:
			if len(snap.Providers) != 1 || snap.Providers[0].Label != "run" {
				t.Fatalf("snapshot %d = %+v", i, snap.Providers)
			}
			if snap.Sys == nil || snap.Sys.MemTotal != 9 {
				t.Fatalf("snapshot %d missing sys vitals: %+v", i, snap.Sys)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run stopped emitting snapshots")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestAgentEventRing(t *testing.T) {
	c := New(nil, time.Second)
	for i := 0; i < core.AgentHistoryLen+5; i++ {
		c.RecordAgent(core.AgentEvent{At: time.Now(), Agent: "a"})
	}
	if len(c.agents) != core.AgentHistoryLen {
		t.Fatalf("agent ring = %d", len(c.agents))
	}
	if c.agents[len(c.agents)-1].At.Before(c.agents[0].At) {
		t.Fatal("ring order broken")
	}
}

func TestProbeAllSkipsWhenNoModelKnown(t *testing.T) {
	c := New([]provider.Provider{&fakeProvider{label: "x"}}, time.Second)
	c.ProbeAll() // must not panic or block; no model known yet
	if len(c.probes) != 0 {
		t.Fatalf("unexpected probes: %d", len(c.probes))
	}
}

// Probes complete concurrently and can finish out of launch order; the
// retained ring must still be chronological or the probe chart anchors its
// window on a stale sample and drops newer ones.
func TestRecordProbeKeepsChronologicalOrder(t *testing.T) {
	c := New(nil, time.Second)
	base := time.Now()
	order := []time.Duration{time.Second, 5 * time.Second, 2 * time.Second, 0, 9 * time.Second}
	for _, d := range order {
		c.RecordProbe(core.ProbeSample{At: base.Add(d), TokPS: 1})
	}
	for i := 1; i < len(c.probes); i++ {
		if c.probes[i].At.Before(c.probes[i-1].At) {
			t.Fatalf("probe ring not sorted at %d: %v", i, c.probes)
		}
	}
	if !c.probes[len(c.probes)-1].At.Equal(base.Add(9 * time.Second)) {
		t.Fatal("newest probe is not last")
	}
}

// Two instances of the same engine kind share a display label ("llama.cpp"
// for every llama.cpp server); their rate baselines and histories must be
// keyed by endpoint so counters never mix across engines.
func TestPerProviderStateKeyedByEndpoint(t *testing.T) {
	m1 := &provider.Metrics{OutTotal: 100, Models: []core.ModelInfo{{Name: "m"}}}
	m2 := &provider.Metrics{OutTotal: 500, Models: []core.ModelInfo{{Name: "m"}}}
	ch := make(chan core.Snapshot, 1)
	c := New([]provider.Provider{
		&fakeProvider{label: core.KindLlamaCPP, addr: "http://127.0.0.1:8080", m: m1},
		&fakeProvider{label: core.KindLlamaCPP, addr: "http://127.0.0.1:8081", m: m2},
	}, time.Second)

	get := func() map[string]float64 {
		c.emit(context.Background(), ch)
		snap := <-ch
		out := map[string]float64{}
		for _, p := range snap.Providers {
			out[p.Addr] = p.OutTokPS
		}
		return out
	}

	get()             // seed both baselines
	m1.OutTotal = 200 // only engine :8080 generated tokens since emit #1

	rates := get()
	if rates["http://127.0.0.1:8080"] <= 10 {
		t.Fatalf(":8080 rate = %v, want > 10 (its own counter moved)", rates)
	}
	if rates["http://127.0.0.1:8081"] != 0 {
		t.Fatalf(":8081 rate = %v, want 0 (its counter did not move)", rates)
	}
}

// A provider failing its very first poll has no history rings yet; emit must
// still include it (with the error surfaced) instead of dereferencing nil.
func TestEmitSurvivesFirstPollError(t *testing.T) {
	fp := &fakeProvider{label: "dead", err: errors.New("connection refused")}
	ch := make(chan core.Snapshot, 1)
	c := New([]provider.Provider{fp}, time.Hour)
	done := make(chan struct{})
	go func() { defer close(done); c.emit(context.Background(), ch) }()
	select {
	case snap := <-ch:
		if len(snap.Providers) != 1 {
			t.Fatalf("providers = %d, want 1", len(snap.Providers))
		}
		p := snap.Providers[0]
		if p.OK || p.Err == "" {
			t.Fatalf("expected failed provider with error, got ok=%v err=%q", p.OK, p.Err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("emit did not produce a snapshot")
	}
	<-done
}

// The ingest handlers, the UI prober and the emit loop all touch the
// collector's shared maps concurrently in production; hammer them together so
// -race can prove the locking holds.
func TestConcurrentRecordProbeEmit(t *testing.T) {
	fp := &fakeProvider{label: "c", m: &provider.Metrics{
		OutTotal: 5, Models: []core.ModelInfo{{Name: "m"}},
	}}
	ch := make(chan core.Snapshot, 1)
	c := New([]provider.Provider{fp}, time.Hour)
	c.SetSysFn(func() core.SysSample { return core.SysSample{MemTotal: 1} })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)

	go func() { // ingest server handlers appending events and probes
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.RecordAgent(core.AgentEvent{At: time.Now(), Agent: "a"})
				c.RecordProbe(core.ProbeSample{At: time.Now(), OK: true})
				time.Sleep(time.Millisecond)
			}
		}
	}()
	go func() { // UI 'p' keypresses; ProbeAll reads lastModel under mu
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.ProbeAll()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	emitDone := make(chan struct{})
	go func() { // poll loop emitting snapshots
		defer wg.Done()
		defer close(emitDone)
		for {
			select {
			case <-stop:
				return
			default:
				c.emit(context.Background(), ch)
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	for { // drain until the emit goroutine has exited its final send
		select {
		case <-ch:
		case <-emitDone:
			wg.Wait()
			return
		}
	}
}

// A consumer stalled on a full channel must not pin c.mu: agent-event
// recording (ingest HTTP handlers) and probe recording stay live while emit
// waits to deliver a snapshot. Regression for sending under the lock.
func TestEmitBlockedSendDoesNotPinMu(t *testing.T) {
	col := New(nil, time.Hour) // no providers: emit parks on the send at once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan core.Snapshot) // unbuffered: the send blocks until consumed
	go col.emit(ctx, ch)

	time.Sleep(100 * time.Millisecond) // emit is now parked on the blocked send

	done := make(chan struct{})
	go func() {
		col.RecordAgent(core.AgentEvent{Agent: "liveness-probe"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RecordAgent blocked while emit was parked on a channel send")
	}
}
