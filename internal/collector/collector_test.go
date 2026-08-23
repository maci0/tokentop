package collector

import (
	"context"
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
	h := []float64{}
	for i := 0; i < core.HistoryLen+10; i++ {
		h = appendRing(h, float64(i))
	}
	if len(h) != core.HistoryLen {
		t.Fatalf("ring len = %d, want %d", len(h), core.HistoryLen)
	}
	if h[len(h)-1] != float64(core.HistoryLen+9) {
		t.Fatal("newest sample lost")
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
