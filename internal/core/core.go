// Package core defines the shared data model flowing from collectors to the UI.
package core

import "time"

const HistoryLen = 180 // rolling samples per provider (~3 min at 1s poll)

// Provider kinds.
const (
	KindOllama    = "ollama"
	KindVLLM      = "vllm"
	KindLlamaCPP  = "llama.cpp" // llama-server, llamafile, ramalama
	KindOpenAI    = "openai"    // generic openai-compatible
	KindSGLang    = "sglang"
	KindTRTLLM    = "trt-llm" // TensorRT-LLM via trtllm-serve or Triton
	KindMLX       = "mlx"     // mlx-lm / LM Studio serving Metal models
	KindLMStudio  = "lmstudio"
	KindKoboldCPP = "koboldcpp"
	KindLocalAI   = "localai"
	KindTGI       = "tgi"
	KindOoba      = "oobabooga" // text-generation-webui
	KindTabbyAPI  = "tabbyapi"
	KindLiteLLM   = "litellm"
	KindGPUStack  = "gpustack"
	KindLemonade  = "lemonade" // AMD Ryzen AI server
)

type ModelInfo struct {
	Name     string `json:"name"`
	SizeVRAM uint64 `json:"vram_bytes,omitempty"`
	CtxUsed  uint64 `json:"ctx_used,omitempty"`
	CtxMax   uint64 `json:"ctx_max,omitempty"`
	State    string `json:"state,omitempty"` // loaded | loading | not-loaded
}

// ProviderSnapshot is one observation of an inference backend.
type ProviderSnapshot struct {
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Addr    string `json:"addr"`
	OK      bool   `json:"ok"`
	Err     string `json:"error,omitempty"`
	Version string `json:"version,omitempty"` // engine version, best effort

	PID     int     `json:"pid,omitempty"`      // serving process, when found locally
	ProcRSS uint64  `json:"proc_rss,omitempty"` // resident memory of that process
	ProcCPU float64 `json:"proc_cpu,omitempty"` // percent of one core

	Models []ModelInfo `json:"models,omitempty"`

	OutTokPS float64 `json:"out_tok_s"`
	InTokPS  float64 `json:"in_tok_s"`
	Running  int     `json:"running"`
	Waiting  int     `json:"waiting"`
	KVPct    float64 `json:"kv_pct"`  // kv cache usage, 0..100
	TTFTms   float64 `json:"ttft_ms"` // engine-reported avg ttft if available

	OutHist []float64 `json:"-"`
	InHist  []float64 `json:"-"`
}

func (p *ProviderSnapshot) PrimaryModel() string {
	if len(p.Models) > 0 {
		return p.Models[0].Name
	}
	return "-"
}

// AgentEvent is a token-usage event pushed by an agent or harness.
type AgentEvent struct {
	At           time.Time `json:"ts"`
	Agent        string    `json:"agent"`
	Model        string    `json:"model,omitempty"`
	Kind         string    `json:"kind"` // turn | tool | error | note
	PromptTokens int64     `json:"prompt_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	Note         string    `json:"note,omitempty"`
}

// ProbeSample is one synthetic generation probe result.
type ProbeSample struct {
	At     time.Time `json:"ts"`
	Addr   string    `json:"addr"`
	Model  string    `json:"model"`
	OK     bool      `json:"ok"`
	Err    string    `json:"error,omitempty"`
	TTFTms float64   `json:"ttft_ms"`
	TokPS  float64   `json:"tok_s"`
	Tokens int       `json:"tokens"`
}

// TempReading is one thermal sensor value in millidegrees Celsius.
type TempReading struct {
	Label  string `json:"label"`
	MilliC int    `json:"milli_c"`
	IsGPU  bool   `json:"gpu,omitempty"`
}

// GPUDevice is one accelerator as reported by sysfs/vendor CLIs (no vendor
// libraries linked).
type GPUDevice struct {
	Vendor     string  `json:"vendor"` // nvidia | amd | intel | apple
	Index      int     `json:"index"`
	Name       string  `json:"name,omitempty"`
	MilliC     int     `json:"milli_c"`
	MemUsed    uint64  `json:"mem_used"`
	MemTotal   uint64  `json:"mem_total"`
	UtilPct    float64 `json:"util_pct"`
	PowerW     float64 `json:"power_w"`
	FanRPM     int     `json:"fan_rpm,omitempty"`
	ClocksSM   int     `json:"clock_sm_mhz,omitempty"`
	Driver     string  `json:"driver,omitempty"`
	VBios      string  `json:"vbios,omitempty"`
	ComputeCap string  `json:"compute_cap,omitempty"`
}

// SysSample carries host-level vitals: RAM, swap, load and temperatures.
type SysSample struct {
	CPUModel string `json:"cpu_model,omitempty"`
	OsName   string `json:"os,omitempty"`     // PRETTY_NAME / product version
	Kernel   string `json:"kernel,omitempty"` // uname -r

	MemTotal uint64 `json:"mem_total"`
	MemUsed  uint64 `json:"mem_used"`

	SwapTotal uint64 `json:"swap_total"`
	SwapUsed  uint64 `json:"swap_used"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	HostUptime time.Duration     `json:"host_uptime,omitempty"`
	Drivers    map[string]string `json:"drivers,omitempty"`     // vendor -> version
	NPUs       []string          `json:"npus,omitempty"`        // detected accelerator drivers
	RemoteHost string            `json:"remote_host,omitempty"` // set when stats come via ssh

	Temps []TempReading `json:"temps,omitempty"`
	GPUs  []GPUDevice   `json:"gpus,omitempty"`
}

// Snapshot is everything the UI needs for one frame.
type Snapshot struct {
	At        time.Time
	Uptime    time.Duration
	Providers []ProviderSnapshot
	Agents    []AgentEvent // newest last
	Probes    []ProbeSample
	Sys       *SysSample
	Paused    bool
}
