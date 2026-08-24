package demo

import (
	"context"
	"reflect"
	"testing"
	"time"

	"tokentop/internal/core"
)

func collectOne(s *Source) core.Snapshot {
	ch := make(chan core.Snapshot, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx, ch)
	return <-ch
}

func TestDeterministicPerSeed(t *testing.T) {
	a := collectOne(NewSource(10*time.Millisecond, 7))
	b := collectOne(NewSource(10*time.Millisecond, 7))
	if len(a.Providers) == 0 || len(a.Providers) != len(b.Providers) {
		t.Fatalf("provider count mismatch: %d vs %d", len(a.Providers), len(b.Providers))
	}
	for i := range a.Providers {
		pa, pb := a.Providers[i], b.Providers[i]
		// The first frame carries no wall-clock state: everything a seed
		// controls must match exactly, across the whole history not just
		// sample zero.
		if pa.Label != pb.Label || pa.OutTokPS != pb.OutTokPS || pa.InTokPS != pb.InTokPS ||
			pa.KVPct != pb.KVPct || pa.Running != pb.Running || pa.Waiting != pb.Waiting ||
			!reflect.DeepEqual(pa.OutHist, pb.OutHist) || !reflect.DeepEqual(pa.InHist, pb.InHist) {
			t.Fatalf("seeded sources diverged at provider %d:\n%+v\n%+v", i, pa, pb)
		}
	}
	if a.Sys.MemUsed != b.Sys.MemUsed || a.Sys.Load1 != b.Sys.Load1 ||
		!reflect.DeepEqual(a.Sys.GPUs, b.Sys.GPUs) {
		t.Fatalf("seeded vitals diverged:\n%+v\n%+v", a.Sys, b.Sys)
	}
}

func TestSysSamplePresent(t *testing.T) {
	snap := collectOne(NewSource(10*time.Millisecond, 5))
	if snap.Sys == nil {
		t.Fatal("demo snapshot missing Sys")
	}
	sys := snap.Sys
	if sys.MemTotal == 0 || sys.MemUsed > sys.MemTotal {
		t.Errorf("implausible memory: used=%d total=%d", sys.MemUsed, sys.MemTotal)
	}
	var haveCPU bool
	for _, tr := range sys.Temps {
		c := float64(tr.MilliC) / 1000
		if c < 20 || c > 120 {
			t.Errorf("implausible temp: %+v", tr)
		}
		haveCPU = haveCPU || !tr.IsGPU
	}
	if !haveCPU {
		t.Errorf("expected a CPU sensor, got %+v", sys.Temps)
	}
	if len(sys.GPUs) == 0 {
		t.Fatal("expected synthesized GPUs")
	}
	for _, g := range sys.GPUs {
		switch {
		case g.MilliC != 0 && (g.MilliC < 20000 || g.MilliC > 120000):
			t.Errorf("implausible GPU temp: %+v", g)
		case g.MemTotal > 0 && g.MemUsed > g.MemTotal:
			t.Errorf("implausible VRAM use: %+v", g)
		case g.UtilPct < 0 || g.UtilPct > 100:
			t.Errorf("implausible util: %+v", g)
		case g.PowerW < 0 || g.PowerW > 1200:
			t.Errorf("implausible power: %+v", g)
		}
	}
}

func TestProbeAllProducesSamples(t *testing.T) {
	s := NewSource(10*time.Millisecond, 3)
	s.ProbeAll()
	if len(s.probes) != len(s.backends) {
		t.Fatalf("probes = %d, want %d", len(s.probes), len(s.backends))
	}
	for _, p := range s.probes {
		if !p.OK || p.TokPS <= 0 || p.TTFTms <= 0 {
			t.Fatalf("implausible probe: %+v", p)
		}
	}
}

func TestSnapshotCarriesAgentsAndProbes(t *testing.T) {
	s := NewSource(5*time.Millisecond, 11)
	ch := make(chan core.Snapshot, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go s.Run(ctx, ch)

	var got core.Snapshot
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case got = <-ch:
		default:
			time.Sleep(time.Millisecond)
			continue
		}
		if len(got.Agents) > 0 && len(got.Probes) > 0 {
			return // success
		}
	}
	t.Fatal("no agent/probe activity within deadline")
}

// Sub-second poll intervals must still anchor t0 correctly; the whole-second
// truncation used previously pinned t0 and skewed the time axis.
func TestAdvanceT0SubSecondCadence(t *testing.T) {
	start := time.Now()
	if t0 := anchorT0(1, start, 500*time.Millisecond); !t0.Equal(start) {
		t.Fatalf("first-sample t0 = %v, want %v", t0, start)
	}
	next := anchorT0(core.HistoryLen, start.Add(500*time.Millisecond), 500*time.Millisecond)
	want := start.Add(500 * time.Millisecond).Add(-time.Duration(core.HistoryLen-1) * 500 * time.Millisecond)
	if !next.Equal(want) {
		t.Fatalf("wrapped t0 = %v, want %v", next, want)
	}
}

// A stalled consumer coalesces ticks, so frames arrive more than one cadence
// apart. Re-anchoring must place the newest sample at its true timestamp
// instead of letting the axis lag wall-clock time by every lost interval.
func TestAnchorT0RecoversFromGap(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	got := anchorT0(4, now, time.Second)
	if !got.Equal(now.Add(-3 * time.Second)) {
		t.Fatalf("t0 after gap = %v, want %v", got, now.Add(-3*time.Second))
	}
	if got := anchorT0(0, now, time.Second); !got.IsZero() {
		t.Fatalf("empty history t0 = %v, want zero", got)
	}
}

// Run drives the source from one goroutine while RecordAgent and ProbeAll
// arrive from others (ingest handlers, UI prober); hammer that exact mix so
// -race can prove the single s.mu contract holds.
func TestSourceConcurrentAccess(t *testing.T) {
	s := NewSource(2*time.Millisecond, 9)
	ch := make(chan core.Snapshot, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() { defer close(runDone); s.Run(ctx, ch) }()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for i := 0; i < 200; i++ {
			s.RecordAgent(core.AgentEvent{At: time.Now(), Agent: "x"})
			s.ProbeAll()
			time.Sleep(time.Millisecond)
		}
	}()

	<-workerDone
	cancel()
	for { // keep draining until Run has noticed cancellation and returned
		select {
		case <-ch:
		case <-runDone:
			return
		}
	}
}
