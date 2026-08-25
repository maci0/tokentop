// Package procs discovers local inference engines by inspecting running
// processes rather than guessing ports. Everything comes from procfs,
// ps(1) or Win32 CIM - no vendor libraries.
package procs

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Info is one sampled process relevant to engine discovery or accounting.
type Info struct {
	PID      int      `json:"pid"`
	Name     string   `json:"name"` // executable / comm
	Args     []string `json:"args,omitempty"`
	RSS      uint64   `json:"rss_bytes,omitempty"`
	CPUPct   float64  `json:"cpu_pct,omitempty"` // percent of one core
	PortHint int      `json:"port,omitempty"`    // --port found on the command line
	Engine   string   `json:"engine,omitempty"`  // matched well-known engine id
	DefPort  int      `json:"def_port,omitempty"`
}

// raw is the platform-sampled record before delta math.
type raw struct {
	pid        int
	name       string
	args       []string
	rss        uint64
	cpuPercent float64 // provided directly by the OS tooling when available
	ticks      uint64  // cumulative CPU jiffies (linux path)
}

// platformList is implemented per GOOS.
var platformList func() ([]raw, error)

// clkTck is the jiffies-per-second constant on the linux path. USER_HZ is
// fixed at 100 by the Linux ABI; there is no runtime probe.
const clkTck = 100

// Sampler turns raw process listings into Infos, deriving CPU percentage on
// linux from tick deltas between samples.
type Sampler struct {
	mu   sync.Mutex
	prev map[int]uint64
	last time.Time

	// minRefresh throttles expensive OS tooling (PowerShell CIM on Windows
	// takes seconds); within the window the previous snapshot is returned.
	minRefresh time.Duration
	cached     []Info
}

func NewSampler() *Sampler {
	return &Sampler{prev: map[int]uint64{}, minRefresh: defaultSamplerRefresh}
}

// Snapshot lists processes, best effort. Returns nil on unsupported/erroring
// platforms so callers can degrade silently.
func (s *Sampler) Snapshot() []Info {
	if platformList == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.minRefresh > 0 && !s.last.IsZero() && now.Sub(s.last) < s.minRefresh {
		return s.cached
	}
	list, err := platformList()
	if err != nil {
		return nil
	}
	dt := now.Sub(s.last).Seconds() // elapsed time (monotonic) since previous successful poll
	s.last = now

	self := os.Getpid()
	out := make([]Info, 0, len(list))
	for _, r := range list {
		if r.pid == self {
			continue
		}
		info := Info{
			PID:      r.pid,
			Name:     r.name,
			Args:     r.args,
			RSS:      r.rss,
			PortHint: ExtractPort(r.args),
		}
		switch {
		case r.cpuPercent > 0:
			info.CPUPct = r.cpuPercent
		case dt > 0:
			pticks := s.prev[r.pid]
			if pticks > 0 && r.ticks >= pticks {
				info.CPUPct = clampPct(float64(r.ticks-pticks) / clkTck / dt * 100)
			}
		}
		if _, tracked := s.prev[r.pid]; !tracked || r.ticks != 0 {
			s.prev[r.pid] = r.ticks
		}
		if eng, defPort, ok := MatchEngine(info); ok {
			info.Engine, info.DefPort = eng, defPort
		}
		out = append(out, info)
	}
	// prune dead pids so the map cannot grow forever
	live := make(map[int]struct{}, len(list))
	for _, r := range list {
		live[r.pid] = struct{}{}
	}
	for pid := range s.prev {
		if _, ok := live[pid]; !ok {
			delete(s.prev, pid)
		}
	}
	s.cached = out
	return out
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100*1024 { // many-core boxes can exceed 100 by design
		v = 100 * 1024
	}
	return v
}

// ExtractPort scans argv for explicit listen-port flags. Exported so the
// remote ssh path can reuse the same convention for command lines gathered
// from another host.
func ExtractPort(args []string) int {
	for i, a := range args {
		for _, flag := range []string{"--port", "--http-port", "--listen-port"} {
			if a == flag && i+1 < len(args) {
				if p, err := strconv.Atoi(strings.TrimSpace(args[i+1])); err == nil {
					return p
				}
			}
			if after, ok := strings.CutPrefix(a, flag+"="); ok {
				if p, err := strconv.Atoi(after); err == nil {
					return p
				}
			}
		}
	}
	return 0
}

// ListenPort returns the process's effective listen port: an explicit --port
// flag on the command line when present, else the matched engine's default.
// Zero when neither applies (DefPort is only set for engine matches).
func (i Info) ListenPort() int {
	if i.PortHint != 0 {
		return i.PortHint
	}
	return i.DefPort
}

// engineMatcher identifies well-known serving processes. Matching is
// deliberately conservative to avoid grabbing unrelated processes.
type engineMatcher struct {
	engine  string
	defPort int
	match   func(name string, lowerCmd string, args []string) bool
}

func baseName(n string) string {
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	if i := strings.LastIndexByte(n, '\\'); i >= 0 {
		n = n[i+1:]
	}
	return strings.ToLower(strings.TrimSuffix(n, ".exe"))
}

func anyArgContains(args []string, subs ...string) bool {
	for _, a := range args {
		la := strings.ToLower(a)
		for _, sub := range subs {
			if strings.Contains(la, sub) {
				return true
			}
		}
	}
	return false
}

var engineMatchers = []engineMatcher{
	{"ollama", 11434, func(n, c string, _ []string) bool {
		return n == "ollama" || strings.Contains(c, "ollama serve")
	}},
	{"llama.cpp", 8080, func(n, c string, _ []string) bool {
		return strings.Contains(n, "llama-server") || strings.Contains(c, "llama-server") ||
			strings.Contains(n, "llamafile")
	}},
	{"koboldcpp", 5001, func(_, c string, _ []string) bool {
		return strings.Contains(c, "koboldcpp")
	}},
	{"vllm", 8000, func(_, c string, args []string) bool {
		return anyArgContains(args, "vllm.entrypoints", "/vllm") ||
			baseNameEq(args, "vllm")
	}},
	{"sglang", 30000, func(_, c string, args []string) bool {
		return anyArgContains(args, "sglang.launch_server", "sglang.srt")
	}},
	{"triton", 8000, func(n, _ string, _ []string) bool { return n == "tritonserver" }},
	{"tgi", 8080, func(_, c string, _ []string) bool {
		return strings.Contains(c, "text-generation-launcher")
	}},
	{"tabbyapi", 5000, func(n, _ string, _ []string) bool { return n == "tabbyapi" }},
	{"oobabooga", 7860, func(_, c string, _ []string) bool {
		return strings.Contains(c, "text-generation-webui") || strings.Contains(c, "oobabooga")
	}},
	{"localai", 8080, func(n, _ string, _ []string) bool {
		return n == "localai" || n == "local-ai"
	}},
	{"litellm", 4000, func(_, c string, args []string) bool {
		return baseNameEq(args, "litellm") || anyArgContains(args, "litellm.proxy")
	}},
	{"mlx", 8080, func(_, c string, _ []string) bool {
		return strings.Contains(c, "mlx_lm.server") || strings.Contains(c, "mlx-lm")
	}},
	{"lmstudio", 1234, func(n, _ string, _ []string) bool {
		return strings.Contains(n, "lm-studio") || strings.Contains(n, "lmstudio") ||
			strings.Contains(n, "lm studio")
	}},
	{"gpustack", 80, func(_, c string, args []string) bool {
		return anyArgContains(args, "gpustack.start")
	}},
	{"lemonade", 8000, func(n, _ string, _ []string) bool { return n == "lemonade-server" || n == "lemond" }},
	{"gpt4all", 4891, func(n, _ string, _ []string) bool { return n == "gpt4all" }},
	{"jan", 1337, func(n, _ string, _ []string) bool { return n == "jan" }},
	{"ramalama", 8080, func(_, c string, _ []string) bool {
		return strings.Contains(c, "ramalama")
	}},
}

func baseNameEq(args []string, want string) bool {
	for _, a := range args {
		if baseName(a) == want {
			return true
		}
	}
	return false
}

// MatchEngine finds the well-known engine behind a process, if any. The
// name is basename'd internally, so raw argv[0] works. Exported for the
// remote ssh path, which matches command lines gathered from another host.
func MatchEngine(i Info) (engine string, defPort int, ok bool) {
	name := baseName(i.Name)
	lowerCmd := strings.ToLower(strings.Join(i.Args, " "))
	for _, m := range engineMatchers {
		if m.match(name, lowerCmd, i.Args) {
			return m.engine, m.defPort, true
		}
	}
	return "", 0, false
}

// defaultSamplerRefresh is set by platform files when OS tooling needs
// throttling (windows). Zero means every Snapshot call re-lists.
var defaultSamplerRefresh time.Duration
