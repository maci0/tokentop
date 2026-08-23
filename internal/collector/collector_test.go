package collector

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"tokentop/internal/core"
	"tokentop/internal/provider"
)

type fakeProvider struct {
	label string
	m     *provider.Metrics
	err   error
}

func (f *fakeProvider) Label() string { return f.label }
func (f *fakeProvider) Addr() string  { return "fake://" + f.label }
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
	if len(r.vals) != core.HistoryLen {
		t.Fatalf("ring len = %d, want %d", len(r.vals), core.HistoryLen)
	}
	if r.vals[len(r.vals)-1] != float64(core.HistoryLen+9) {
		t.Fatal("newest sample lost")
	}
	// oldest timestamp must slide forward one cadence per evicted sample
	wantT0 := t0.Add(10 * time.Second)
	if !r.t0.Equal(wantT0) {
		t.Fatalf("t0 = %v, want %v", r.t0, wantT0)
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
	go func() { c.emit(ch); close(done) }()

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
	go func() { defer close(done); c.emit(ch) }()
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

func TestAgentEventRing(t *testing.T) {
	c := New(nil, time.Second)
	for i := 0; i < agentRingCap+5; i++ {
		c.RecordAgent(core.AgentEvent{At: time.Now(), Agent: "a"})
	}
	if len(c.agents) != agentRingCap {
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

// A provider failing its very first poll has no history rings yet; emit must
// still include it (with the error surfaced) instead of dereferencing nil.
func TestEmitSurvivesFirstPollError(t *testing.T) {
	fp := &fakeProvider{label: "dead", err: errors.New("connection refused")}
	ch := make(chan core.Snapshot, 1)
	c := New([]provider.Provider{fp}, time.Hour)
	done := make(chan struct{})
	go func() { defer close(done); c.emit(ch) }()
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
