// Package collector polls providers on an interval, derives token rates from
// monotonic counters, and fans snapshots out to the UI.
package collector

import (
	"context"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"tokentop/internal/core"
	"tokentop/internal/probe"
	"tokentop/internal/procs"
	"tokentop/internal/provider"
	"tokentop/internal/sysmon"
)

const (
	agentRingCap = 64
	probeRingCap = 128
	emaAlpha     = 0.35
)

type prevSample struct {
	at        time.Time
	outTotal  float64
	inTotal   float64
	outEMA    float64
	inEMA     float64
	hasTotals bool
}

type Collector struct {
	providers []provider.Provider
	interval  time.Duration
	procFn    func() []procs.Info
	procMu    sync.Mutex
	procCache []procs.Info

	sysMu       sync.Mutex // guards sysFn + sysCache
	sysFn       func() core.SysSample
	sysCache    *core.SysSample // last good sample from the background poller
	sysSampling sync.Mutex      // serializes sampling; vendor CLIs take seconds

	mu         sync.Mutex
	histOut    map[string]*timedRing
	histIn     map[string]*timedRing
	prev       map[string]prevSample
	lastModel  map[string]string // label -> model to probe
	ttftEngine map[string]float64
	kvPct      map[string]float64
	agents     []core.AgentEvent
	probes     []core.ProbeSample
	started    time.Time
}

func New(providers []provider.Provider, interval time.Duration) *Collector {
	if interval <= 0 {
		interval = time.Second
	}
	return &Collector{
		providers:  providers,
		interval:   interval,
		sysFn:      sysmon.Sample,
		procFn:     func() []procs.Info { return procSampler.Snapshot() },
		histOut:    map[string]*timedRing{},
		histIn:     map[string]*timedRing{},
		prev:       map[string]prevSample{},
		lastModel:  map[string]string{},
		ttftEngine: map[string]float64{},
		kvPct:      map[string]float64{},
		started:    time.Now(),
	}
}

// procSampler is the shared engine-process sampler; nil-safe when the
// platform has no process table access.
var procSampler = procs.NewSampler()

// SetSysFn overrides the host-vitals sampler (used for ssh targets whose
// stats merge local + remote readings). Call before Run.
func (c *Collector) SetSysFn(fn func() core.SysSample) {
	c.sysMu.Lock()
	c.sysFn = fn
	c.sysMu.Unlock()
}

// startSysPoller refreshes host vitals in the background; emit never blocks
// on it (GPU vendor CLIs can take seconds and would stall every frame).
func (c *Collector) startSysPoller(ctx context.Context) {
	go func() {
		t := time.NewTicker(c.interval)
		defer t.Stop()
		c.sampleSys(false) // first pass: skip if emit's cold path already sampled
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.sampleSys(true)
			}
		}
	}()
}

// sampleSys runs the vitals sampler. Concurrent callers serialize on
// sysSampling so slow tooling is never invoked twice at once; with force
// unset an already-warm cache short-circuits without sampling.
func (c *Collector) sampleSys(force bool) *core.SysSample {
	c.sysSampling.Lock()
	defer c.sysSampling.Unlock()
	c.sysMu.Lock()
	fn, cached := c.sysFn, c.sysCache
	c.sysMu.Unlock()
	if !force && (cached != nil || fn == nil) {
		return cached
	}
	if fn == nil {
		return nil
	}
	s := fn()
	c.sysMu.Lock()
	c.sysCache = &s
	c.sysMu.Unlock()
	return &s
}

// sysSnapshot returns the freshest vitals sample: a warm-cache read that
// never waits on sampling, falling back to one serialized sample before the
// first background refresh has landed.
func (c *Collector) sysSnapshot() *core.SysSample {
	c.sysMu.Lock()
	cached := c.sysCache
	c.sysMu.Unlock()
	if cached != nil {
		return cached
	}
	return c.sampleSys(false)
}

// startProcPoller refreshes the process table in the background; emit never
// blocks on it (Windows CIM enumeration takes seconds).
func (c *Collector) startProcPoller(ctx context.Context) {
	if c.procFn == nil {
		return
	}
	go func() {
		t := time.NewTicker(c.interval)
		defer t.Stop()
		refresh := func() {
			if infos := c.procFn(); infos != nil {
				c.procMu.Lock()
				c.procCache = infos
				c.procMu.Unlock()
			}
		}
		refresh()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refresh()
			}
		}
	}()
}

// procSnapshot returns the latest cached engine processes.
func (c *Collector) procSnapshot() []procs.Info {
	c.procMu.Lock()
	defer c.procMu.Unlock()
	return c.procCache
}

// Run polls until ctx is cancelled, emitting one Snapshot per interval.
func (c *Collector) Run(ctx context.Context, out chan<- core.Snapshot) {
	c.startProcPoller(ctx)
	c.startSysPoller(ctx)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	c.emit(out) // immediate first frame
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.emit(out)
		}
	}
}

func (c *Collector) emit(out chan<- core.Snapshot) {
	snap := core.Snapshot{At: time.Now(), Uptime: time.Since(c.started)}

	type result struct {
		idx int
		m   *provider.Metrics
		err error
	}
	results := make([]result, len(c.providers))
	var wg sync.WaitGroup
	for i, p := range c.providers {
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(context.Background(), provider.PollTimeout)
			defer cancel()
			m, err := p.Poll(pctx)
			results[i] = result{i, m, err}
		}(i, p)
	}
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	snap.Agents = append([]core.AgentEvent(nil), c.agents...)
	snap.Probes = append([]core.ProbeSample(nil), c.probes...)
	snap.Sys = c.sysSnapshot()
	byPort := map[int]procs.Info{}
	for _, p := range c.procSnapshot() {
		port := p.PortHint
		if port == 0 && p.Engine != "" {
			port = p.DefPort
		}
		if port > 0 {
			if _, dup := byPort[port]; !dup {
				byPort[port] = p
			}
		}
	}

	now := time.Now()
	for _, r := range results {
		p := c.providers[r.idx]
		ps := core.ProviderSnapshot{
			Label: p.Label(),
			Kind:  kindOf(p),
			Addr:  p.Addr(),
		}
		if r.err != nil {
			ps.Err = r.err.Error()
		} else {
			ps.OK = true
			ps.Models = r.m.Models
			ps.Running = r.m.Running
			ps.Waiting = r.m.Waiting
			ps.TTFTms = r.m.TTFTms
			if port := urlPort(p.Addr()); port > 0 {
				if proc, ok := byPort[port]; ok {
					ps.PID, ps.ProcRSS, ps.ProcCPU = proc.PID, proc.RSS, proc.CPUPct
				}
			}
			if r.m.Version != "" {
				ps.Version = r.m.Version
			}
			if r.m.HasKV {
				ps.KVPct = r.m.KVPct
				c.kvPct[ps.Label] = ps.KVPct
			} else {
				ps.KVPct = c.kvPct[ps.Label]
			}
			if len(r.m.Models) > 0 {
				c.lastModel[ps.Label] = r.m.Models[0].Name
			}
			outPS, inPS := c.rates(ps.Label, r.m, now)
			ps.OutTokPS = outPS
			ps.InTokPS = inPS
			c.ring(c.histOut, ps.Label).push(outPS, now, c.interval)
			c.ring(c.histIn, ps.Label).push(inPS, now, c.interval)
		}
		// ring() (not a bare map index): a provider whose first poll failed
		// has no history yet, and indexing the map there would deref nil.
		outR, inR := c.ring(c.histOut, ps.Label), c.ring(c.histIn, ps.Label)
		ps.OutHist, ps.OutT0 = outR.copy(), outR.t0
		ps.InHist, ps.InT0 = inR.copy(), inR.t0
		snap.Providers = append(snap.Providers, ps)
	}
	out <- snap
}

// rates derives smoothed tok/s deltas since the previous sample.
func (c *Collector) rates(label string, m *provider.Metrics, now time.Time) (outPS, inPS float64) {
	pv, had := c.prev[label]
	if !had || !pv.hasTotals {
		c.prev[label] = prevSample{at: now, outTotal: m.OutTotal, inTotal: m.InTotal, hasTotals: true}
		return 0, 0
	}
	dt := now.Sub(pv.at).Seconds()
	rawOut := max((m.OutTotal-pv.outTotal)/dt, 0) // clamp on counter reset
	rawIn := max((m.InTotal-pv.inTotal)/dt, 0)
	if m.DirectOutPS > 0 { // trust the engine's own tok/s gauge when present
		rawOut = m.DirectOutPS
	}
	outPS = ema(pv.outEMA, rawOut)
	inPS = ema(pv.inEMA, rawIn)
	c.prev[label] = prevSample{
		at: now, outTotal: m.OutTotal, inTotal: m.InTotal,
		outEMA: outPS, inEMA: inPS, hasTotals: true,
	}
	return outPS, inPS
}

func ema(prev, raw float64) float64 { return prev*(1-emaAlpha) + raw*emaAlpha }

// timedRing is a value history carrying the wall-clock time of its oldest
// sample so charts can place every point on an absolute time axis.
type timedRing struct {
	vals []float64
	t0   time.Time
}

func (r *timedRing) push(v float64, now time.Time, interval time.Duration) {
	if len(r.vals) >= core.HistoryLen {
		r.vals = r.vals[1:]
		if !r.t0.IsZero() {
			r.t0 = r.t0.Add(interval) // oldest sample slid off
		}
	}
	if r.t0.IsZero() {
		r.t0 = now
	}
	r.vals = append(r.vals, v)
}

func (r *timedRing) copy() []float64 {
	return append([]float64(nil), r.vals...)
}

func (c *Collector) ring(m map[string]*timedRing, label string) *timedRing {
	r, ok := m[label]
	if !ok {
		r = &timedRing{}
		m[label] = r
	}
	return r
}

// RecordAgent stores an agent event (called from the ingest server).
func (c *Collector) RecordAgent(ev core.AgentEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents = append(c.agents, ev)
	if len(c.agents) > agentRingCap {
		c.agents = c.agents[len(c.agents)-agentRingCap:]
	}
}

// RecordProbe stores a probe sample.
func (c *Collector) RecordProbe(s core.ProbeSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probes = append(c.probes, s)
	if len(c.probes) > probeRingCap {
		c.probes = c.probes[len(c.probes)-probeRingCap:]
	}
}

// ProbeAll launches one probe against every known backend, asynchronously.
func (c *Collector) ProbeAll() {
	c.mu.Lock()
	var targets []probe.Request
	for _, p := range c.providers {
		if model := c.lastModel[p.Label()]; model != "" {
			targets = append(targets, probe.Request{Kind: kindOf(p), Base: p.Addr(), Model: model})
		}
	}
	c.mu.Unlock()
	for _, t := range targets {
		go func(t probe.Request) {
			c.RecordProbe(probe.Run(context.Background(), t))
		}(t)
	}
}

// kindOf reports a provider's engine kind; providers without one are ollama.
func kindOf(p provider.Provider) string {
	if k, ok := p.(interface{ Kind() string }); ok {
		return k.Kind()
	}
	return core.KindOllama
}

// urlPort extracts the TCP port from a backend URL.
func urlPort(addr string) int {
	u, err := url.Parse(addr)
	if err != nil {
		return 0
	}
	if _, port, err := net.SplitHostPort(u.Host); err == nil {
		if p, err := strconv.Atoi(port); err == nil {
			return p
		}
	}
	switch u.Scheme {
	case "https":
		return 443
	default:
		return 80
	}
}
