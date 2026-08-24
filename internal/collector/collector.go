// Package collector polls providers on an interval, derives token rates from
// monotonic counters, and fans snapshots out to the UI.
package collector

import (
	"context"
	"net"
	"net/url"
	"sort"
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
	emaAlpha = 0.35
)

type prevSample struct {
	at       time.Time
	outTotal float64
	inTotal  float64
	outEMA   float64
	inEMA    float64
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

	mu        sync.Mutex
	histOut   map[string]*timedRing
	histIn    map[string]*timedRing
	prev      map[string]prevSample
	lastModel map[string]string // endpoint -> model to probe
	kvPct     map[string]float64
	agents    []core.AgentEvent
	probes    []core.ProbeSample
	started   time.Time
	baseCtx   context.Context // set by Run; bounds ad-hoc probes past shutdown
}

func New(providers []provider.Provider, interval time.Duration) *Collector {
	if interval <= 0 {
		interval = time.Second
	}
	return &Collector{
		providers: providers,
		interval:  interval,
		sysFn:     sysmon.Sample,
		procFn:    func() []procs.Info { return procSampler.Snapshot() },
		histOut:   map[string]*timedRing{},
		histIn:    map[string]*timedRing{},
		prev:      map[string]prevSample{},
		lastModel: map[string]string{},
		kvPct:     map[string]float64{},
		started:   time.Now(),
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
	c.mu.Lock()
	c.baseCtx = ctx
	c.mu.Unlock()
	c.startProcPoller(ctx)
	c.startSysPoller(ctx)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	c.emit(ctx, out) // immediate first frame
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.emit(ctx, out)
		}
	}
}

func (c *Collector) emit(ctx context.Context, out chan<- core.Snapshot) {
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
		// Per-provider state is keyed by endpoint, not display label (see
		// providerKey): labels repeat across instances of the same engine
		// kind, and shared baselines or histories would mix their counters.
		key := providerKey(p)
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
				c.kvPct[key] = ps.KVPct
			} else {
				ps.KVPct = c.kvPct[key]
			}
			if len(r.m.Models) > 0 {
				c.lastModel[key] = r.m.Models[0].Name
			}
			outPS, inPS := c.rates(key, r.m, now)
			ps.OutTokPS = outPS
			ps.InTokPS = inPS
			c.ring(c.histOut, key).push(outPS, now, c.interval)
			c.ring(c.histIn, key).push(inPS, now, c.interval)
		}
		// ring() (not a bare map index): a provider whose first poll failed
		// has no history yet, and indexing the map there would deref nil.
		outR, inR := c.ring(c.histOut, key), c.ring(c.histIn, key)
		ps.OutHist, ps.OutT0 = outR.copy(), outR.t0
		ps.InHist, ps.InT0 = inR.copy(), inR.t0
		snap.Providers = append(snap.Providers, ps)
	}
	c.mu.Unlock()
	// Send outside the critical section: a stalled consumer must neither pin
	// emit past cancellation nor freeze RecordAgent/RecordProbe/ProbeAll
	// behind c.mu while this send waits for buffer space.
	select {
	case out <- snap:
	case <-ctx.Done():
	}
}

// rates derives smoothed tok/s deltas since the previous sample.
func (c *Collector) rates(label string, m *provider.Metrics, now time.Time) (outPS, inPS float64) {
	pv, had := c.prev[label]
	if !had {
		c.prev[label] = prevSample{at: now, outTotal: m.OutTotal, inTotal: m.InTotal}
		return 0, 0
	}
	dt := now.Sub(pv.at).Seconds()
	if dt <= 0 {
		// Zero elapsed time cannot yield a rate: 0/0 is NaN and n/0 is
		// +Inf, and either would poison this EMA and every later sample
		// derived from it. Hold the prior rate; keep the older baseline so
		// the next real interval accounts for these tokens too.
		return pv.outEMA, pv.inEMA
	}
	rawOut := max((m.OutTotal-pv.outTotal)/dt, 0) // clamp on counter reset
	rawIn := max((m.InTotal-pv.inTotal)/dt, 0)
	if m.DirectOutPS > 0 { // trust the engine's own tok/s gauge when present
		rawOut = m.DirectOutPS
	}
	outPS = ema(pv.outEMA, rawOut)
	inPS = ema(pv.inEMA, rawIn)
	c.prev[label] = prevSample{
		at: now, outTotal: m.OutTotal, inTotal: m.InTotal,
		outEMA: outPS, inEMA: inPS,
	}
	return outPS, inPS
}

func ema(prev, raw float64) float64 { return prev*(1-emaAlpha) + raw*emaAlpha }

// timedRing is a value history carrying the wall-clock time of its oldest
// sample so charts can place every point on an absolute time axis.
//
// The backing buffer is allocated once at HistoryLen and reused head-first:
// after warm-up push never allocates or copies, where sliding a slice
// (vals = vals[1:]) would realloc on every push for the life of the ring.
type timedRing struct {
	buf  []float64 // fixed capacity HistoryLen, samples in insertion order
	head int       // counts fills while filling; then indexes the oldest element
	t0   time.Time
}

func (r *timedRing) push(v float64, now time.Time, interval time.Duration) {
	if r.buf == nil { // one reservation for the ring's whole life
		r.buf = make([]float64, 0, core.HistoryLen)
	}
	if len(r.buf) < core.HistoryLen { // filling: keep appending in order
		r.buf = append(r.buf, v)
	} else {
		if r.head == core.HistoryLen { // just filled: oldest lives at index 0
			r.head = 0
		}
		r.buf[r.head] = v // overwrite the oldest sample
		r.head++
		if r.head == core.HistoryLen {
			r.head = 0
		}
	}
	// Anchor hist[0] to this push instead of sliding it one interval per
	// sample: pushes are not guaranteed evenly spaced (a scrape may take up
	// to PollTimeout, and a stalled emit lets ticks coalesce), and a slid t0
	// would drift behind wall-clock time forever, shifting the whole chart
	// axis into the past. Even spacing reproduces the slide exactly.
	r.t0 = now.Add(-time.Duration(len(r.buf)-1) * interval)
}

// copy returns the samples in insertion order (oldest first), detached from
// the ring so snapshots stay stable across later pushes.
func (r *timedRing) copy() []float64 {
	if len(r.buf) == 0 {
		return nil
	}
	out := make([]float64, len(r.buf))
	n := copy(out, r.buf[r.head:])
	copy(out[n:], r.buf[:r.head])
	return out
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
	if len(c.agents) > core.AgentHistoryLen {
		c.agents = c.agents[len(c.agents)-core.AgentHistoryLen:]
	}
}

// RecordProbe stores a probe sample. Probes complete concurrently and can
// finish out of launch order, but every consumer (probe charts, the "last"
// readout) assumes newest-last ordering: keep the ring sorted by timestamp.
func (c *Collector) RecordProbe(s core.ProbeSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.probes = append(c.probes, s)
	sort.SliceStable(c.probes, func(i, j int) bool {
		return c.probes[i].At.Before(c.probes[j].At)
	})
	if len(c.probes) > core.ProbeHistoryLen {
		c.probes = c.probes[len(c.probes)-core.ProbeHistoryLen:]
	}
}

// providerKey is the per-provider state key: the endpoint when known, else
// the display label. Endpoints are unique per instance; labels repeat across
// instances of the same engine kind.
func providerKey(p provider.Provider) string {
	if addr := p.Addr(); addr != "" {
		return addr
	}
	return p.Label()
}

// ProbeAll launches one probe against every known backend, asynchronously.
// Probes ride the Run context so shutdown cancels in-flight generations
// instead of leaving them running for the client's full timeout.
func (c *Collector) ProbeAll() {
	c.mu.Lock()
	var targets []probe.Request
	for _, p := range c.providers {
		if model := c.lastModel[providerKey(p)]; model != "" {
			targets = append(targets, probe.Request{Kind: kindOf(p), Base: p.Addr(), Model: model})
		}
	}
	ctx := c.baseCtx
	c.mu.Unlock()
	if ctx == nil { // probed before Run: nothing bounds these but the client timeout
		ctx = context.Background()
	}
	for _, t := range targets {
		go func(t probe.Request) {
			c.RecordProbe(probe.Run(ctx, t))
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
