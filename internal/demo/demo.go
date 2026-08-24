// Package demo simulates a small inference fleet so tokentop has something to
// show without any real backends. Deterministic per seed.
package demo

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"tokentop/internal/core"
)

type backend struct {
	label, kind, addr, model string
	outBase, inBase          float64
	burstEvery               int // seconds between load bursts
}

// Source emits plausible Snapshots on ch once per tick.
type Source struct {
	interval time.Duration
	backends []backend
	rng      *rand.Rand
	mu       sync.Mutex

	start   time.Time
	t       float64
	histOut map[string][]float64
	histIn  map[string][]float64
	t0      map[string]time.Time // first-sample timestamp per label
	kv      []float64
	nextEv  time.Time
	nextPr  time.Time

	memPct  float64
	swapPct float64
	agents  []core.AgentEvent
	probes  []core.ProbeSample
}

func NewSource(interval time.Duration, seed int64) *Source {
	if interval <= 0 {
		interval = time.Second
	}
	return &Source{
		interval: interval,
		rng:      rand.New(rand.NewSource(seed)),
		backends: []backend{
			{label: "ollama", kind: core.KindOllama, addr: "http://127.0.0.1:11434", model: "llama3.1:8b-instruct-q4_K_M",
				outBase: 38, inBase: 120, burstEvery: 17},
			{label: "vllm-a100", kind: core.KindVLLM, addr: "http://127.0.0.1:8000", model: "Qwen/Qwen2.5-32B-Instruct-AWQ",
				outBase: 210, inBase: 900, burstEvery: 11},
			{label: "sglang-h200", kind: core.KindSGLang, addr: "http://127.0.0.1:30000", model: "deepseek-ai/DeepSeek-R1-Distill-Llama-70B-FP8",
				outBase: 340, inBase: 1500, burstEvery: 13},
			{label: "trt-llm", kind: core.KindTRTLLM, addr: "http://127.0.0.1:8001", model: "meta-llama/Llama-3.3-70B-Instruct-engine",
				outBase: 260, inBase: 1100, burstEvery: 19},
			{label: "mlx-studio", kind: core.KindMLX, addr: "http://127.0.0.1:1234", model: "mlx-community/Qwen2.5-Coder-14B-4bit",
				outBase: 46, inBase: 180, burstEvery: 23},
		},
		histOut: map[string][]float64{},
		histIn:  map[string][]float64{},
		t0:      map[string]time.Time{},
		kv:      make([]float64, 5),
		memPct:  52,
		swapPct: 14,
	}
}

var agentNames = []string{"coder-agent", "ops-agent", "research-agent", "swarm-07"}
var evKinds = []string{"turn", "tool", "tool", "note", "error"}
var notes = []string{
	"planning patch series",
	"shell(git status)",
	"summarizing diff",
	"retry after 429",
	"writing tests",
	"browser(search docs)",
	"final answer composed",
}

// Run blocks until ctx is done.
func (s *Source) Run(ctx context.Context, ch chan<- core.Snapshot) {
	s.start = time.Now()
	s.nextEv = s.start.Add(2 * s.interval)
	s.nextPr = s.start.Add(4 * s.interval)
	tick := time.NewTicker(s.interval)
	defer tick.Stop()
	now := s.start
	for {
		s.frame(now)
		snap := s.snapshot(now)
		select { // a stalled consumer must not pin the goroutine past cancel
		case ch <- snap:
		case <-ctx.Done():
			return
		}
		select {
		case <-ctx.Done():
			return
		case now = <-tick.C:
		}
	}
}

// frame mutates every simulated channel (rng, histories, vitals). It takes
// the same lock as the externally callable RecordAgent/ProbeAll/snapshot:
// Run drives it from one goroutine, but probes and agent events arrive from
// the UI goroutine, and rand.Rand is not safe for concurrent use.
func (s *Source) frame(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.After(s.nextEv) {
		s.genEvent(now)
		s.nextEv = now.Add(time.Duration(2+s.rng.Intn(5)) * s.interval)
	}
	if now.After(s.nextPr) {
		s.genProbe(now)
		s.nextPr = now.Add(time.Duration(6+s.rng.Intn(6)) * s.interval)
	}
	s.t += s.interval.Seconds()
	for i, b := range s.backends {
		wave := math.Sin(s.t/9+float64(i)*1.7)*0.25 + 1
		burst := 0.0
		if int(s.t)%b.burstEvery < 3 {
			burst = b.outBase * 1.6
		}
		jitter := 1 + s.rng.NormFloat64()*0.06
		out := clamp((b.outBase*wave+burst)*jitter, 0, 4000)
		in := clamp((b.inBase*wave+burst*4)*jitter, 0, 20000)
		s.histOut[b.label] = ring(s.histOut[b.label], out)
		s.histIn[b.label] = ring(s.histIn[b.label], in)
		s.t0[b.label] = advanceT0(s.t0[b.label], len(s.histOut[b.label]), now, s.interval)

		target := 45 + 40*math.Sin(s.t/14+float64(i)) + s.rng.NormFloat64()*3
		s.kv[i] += (clamp(target, 3, 99) - s.kv[i]) * 0.15
	}

	memTarget := 50 + 18*math.Sin(s.t/19) + s.rng.NormFloat64()*2
	s.memPct += (clamp(memTarget, 20, 95) - s.memPct) * 0.12
	swapTarget := 10 + 8*math.Sin(s.t/31+1) + s.rng.NormFloat64()
	s.swapPct += (clamp(swapTarget, 0, 60) - s.swapPct) * 0.08
}

// sysSample builds host vitals consistent with the simulated fleet.
func (s *Source) sysSample() core.SysSample {
	const GiB = 1 << 30
	sys := core.SysSample{
		CPUModel:  "Simulated EPYC 9754 (demo)",
		MemTotal:  384 * GiB,
		SwapTotal: 8 * GiB,
		Load1:     clamp(1.2+math.Abs(math.Sin(s.t/21))*4+s.rng.NormFloat64()*0.1, 0, 64),
	}
	sys.Load5 = sys.Load1 * 0.85
	sys.Load15 = sys.Load1 * 0.7
	sys.MemUsed = uint64(float64(sys.MemTotal) * s.memPct / 100)
	sys.SwapUsed = uint64(float64(sys.SwapTotal) * s.swapPct / 100)
	cpu := 58 + 16*math.Sin(s.t/23) + s.rng.NormFloat64()*2
	sys.Temps = []core.TempReading{
		{Label: "package", MilliC: int(cpu * 1000)},
		{Label: "nvme0", MilliC: int((41 + 6*math.Abs(math.Sin(s.t/30))) * 1000)},
	}
	gpuBase := 62 + 20*math.Abs(math.Sin(s.t/16))
	a100 := core.GPUDevice{
		Vendor: "nvidia", Index: 0, Name: "A100-SXM4-80GB",
		MilliC: int(gpuBase * 1000), MemTotal: 80 * GiB,
		MemUsed: uint64(float64(80*GiB) * clamp(45+30*math.Sin(s.t/12), 5, 99) / 100),
		UtilPct: clamp(55+40*math.Sin(s.t/9), 0, 100),
		PowerW:  280 + 120*math.Abs(math.Sin(s.t/14)),
	}
	h200 := a100
	h200.Index, h200.Name, h200.MemTotal = 1, "H200-SXM-141GB", 141*GiB
	h200.MemUsed = uint64(float64(h200.MemTotal) * clamp(50+28*math.Sin(s.t/10+2), 5, 99) / 100)
	mi210 := core.GPUDevice{
		Vendor: "amd", Index: 0, Name: "MI210",
		MilliC: int((gpuBase + 6) * 1000), MemTotal: 64 * GiB,
		MemUsed: uint64(float64(64*GiB) * clamp(35+25*math.Sin(s.t/15+1), 5, 99) / 100),
		UtilPct: clamp(40+45*math.Sin(s.t/11+3), 0, 100),
		PowerW:  190 + 90*math.Abs(math.Sin(s.t/17)),
	}
	sys.GPUs = []core.GPUDevice{a100, h200, mi210}
	return sys
}

// genEvent synthesizes one agent event; caller holds s.mu.
func (s *Source) genEvent(now time.Time) {
	b := s.backends[s.rng.Intn(len(s.backends))]
	ev := core.AgentEvent{
		At:           now,
		Agent:        agentNames[s.rng.Intn(len(agentNames))],
		Model:        b.model,
		Kind:         evKinds[s.rng.Intn(len(evKinds))],
		PromptTokens: int64(400 + s.rng.Intn(9000)),
		OutputTokens: int64(30 + s.rng.Intn(1200)),
	}
	if ev.Kind == "note" || ev.Kind == "error" {
		ev.Note = notes[s.rng.Intn(len(notes))]
	}
	s.addAgent(ev)
}

func (s *Source) addAgent(ev core.AgentEvent) {
	s.agents = append(s.agents, ev)
	if len(s.agents) > core.AgentHistoryLen {
		s.agents = s.agents[len(s.agents)-core.AgentHistoryLen:]
	}
}

// RecordAgent lets external scripts push events into the demo feed too.
func (s *Source) RecordAgent(ev core.AgentEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addAgent(ev)
}

func (s *Source) addProbe(p core.ProbeSample) {
	s.probes = append(s.probes, p)
	// Consumers assume newest-last ordering; keep the ring sorted by time.
	sort.SliceStable(s.probes, func(i, j int) bool {
		return s.probes[i].At.Before(s.probes[j].At)
	})
	if len(s.probes) > core.ProbeHistoryLen {
		s.probes = s.probes[len(s.probes)-core.ProbeHistoryLen:]
	}
}

// ProbeAll satisfies the UI prober interface by synthesizing samples now.
func (s *Source) ProbeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.backends {
		s.addProbe(s.synthProbe(s.backends[i], time.Now(), 60, 180, 0.3, 1))
	}
}

// synthProbe fabricates one plausible probe result; spans size the random
// ttft/duration draws.
func (s *Source) synthProbe(b backend, at time.Time, ttftLo, ttftSpan, durLo, durSpan float64) core.ProbeSample {
	ttft := ttftLo + s.rng.Float64()*ttftSpan
	dur := durLo + s.rng.Float64()*durSpan
	n := int(dur * (b.outBase / (1 + s.rng.Float64())))
	return core.ProbeSample{
		At: at, Addr: b.addr, Model: b.model, OK: true,
		TTFTms: ttft,
		TokPS:  float64(n) / dur,
		Tokens: n,
	}
}

// genProbe drops in one background probe; caller holds s.mu.
func (s *Source) genProbe(now time.Time) {
	b := s.backends[s.rng.Intn(len(s.backends))]
	s.addProbe(s.synthProbe(b, now, 80, 140, 0.4, 1.2))
}

func (s *Source) snapshot(now time.Time) core.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	sys := s.sysSample()
	snap := core.Snapshot{
		At:     now,
		Uptime: now.Sub(s.start),
		Sys:    &sys,
		Agents: append([]core.AgentEvent(nil), s.agents...),
		Probes: append([]core.ProbeSample(nil), s.probes...),
	}
	for i, b := range s.backends {
		ps := core.ProviderSnapshot{
			Label:    b.label,
			Kind:     b.kind,
			Addr:     b.addr,
			OK:       true,
			Models:   []core.ModelInfo{{Name: b.model}},
			OutTokPS: tail(s.histOut[b.label]),
			InTokPS:  tail(s.histIn[b.label]),
			KVPct:    clamp(s.kv[i], 0, 100),
			TTFTms:   90 + 70*math.Abs(math.Sin(s.t/10+float64(i))),
		}
		ps.Running = 1 + i%3 + int(clamp(math.Sin(s.t/7+float64(i))*1.5+1.5, 0, 4))
		ps.Waiting = int(clamp(math.Sin(s.t/13+float64(i*2))*3+3, 0, 24))
		ps.OutHist = append([]float64(nil), s.histOut[b.label]...)
		ps.InHist = append([]float64(nil), s.histIn[b.label]...)
		ps.OutT0 = s.t0[b.label]
		ps.InT0 = s.t0[b.label]
		snap.Providers = append(snap.Providers, ps)
	}
	return snap
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func tail(h []float64) float64 {
	if len(h) == 0 {
		return 0
	}
	return h[len(h)-1]
}

func ring(h []float64, v float64) []float64 {
	if len(h) >= core.HistoryLen {
		h = h[1:]
	}
	return append(h, v)
}

// advanceT0 tracks the timestamp of hist[0], sliding forward one cadence on
// ring wrap so the UI can place samples on an absolute time axis (mirrors
// the collector).
func advanceT0(t0 time.Time, length int, now time.Time, cadence time.Duration) time.Time {
	if t0.IsZero() {
		return now
	}
	if length >= core.HistoryLen {
		return t0.Add(cadence)
	}
	return t0
}
