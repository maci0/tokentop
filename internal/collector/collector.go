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
	sysFn     func() core.SysSample
	procFn    func() []procs.Info
	procMu    sync.Mutex
	procCache []procs.Info

	mu         sync.Mutex
	histOut    map[string][]float64
	histIn     map[string][]float64
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
		histOut:    map[string][]float64{},
		histIn:     map[string][]float64{},
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
// stats merge local + remote readings).
func (c *Collector) SetSysFn(fn func() core.SysSample) { c.sysFn = fn }

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
	if c.sysFn != nil {
		sys := c.sysFn()
		snap.Sys = &sys
	}
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
			c.histOut[ps.Label] = appendRing(c.histOut[ps.Label], outPS)
			c.histIn[ps.Label] = appendRing(c.histIn[ps.Label], inPS)
		}
		ps.OutHist = append([]float64(nil), c.histOut[ps.Label]...)
		ps.InHist = append([]float64(nil), c.histIn[ps.Label]...)
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
	rawOut := maxF((m.OutTotal-pv.outTotal)/dt, 0) // clamp on counter reset
	rawIn := maxF((m.InTotal-pv.inTotal)/dt, 0)
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

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func ema(prev, raw float64) float64 { return prev*(1-emaAlpha) + raw*emaAlpha }

func appendRing(h []float64, v float64) []float64 {
	if len(h) >= core.HistoryLen {
		h = h[1:]
	}
	return append(h, v)
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

func kindOf(p provider.Provider) string {
	switch v := p.(type) {
	case *provider.OpenAICompat:
		return v.Kind()
	case interface{ Kind() string }:
		return v.Kind()
	default:
		return core.KindOllama
	}
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
