// Package core defines the shared data model flowing from collectors to the UI.
package core

import "time"

const HistoryLen = 180 // rolling samples per provider (~3 min at 1s poll)

// Rolling history caps for snapshot payloads.
const (
	AgentHistoryLen = 64  // agent events kept per snapshot
	ProbeHistoryLen = 128 // probe samples kept per snapshot
)

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
	KindLiteLLM   = "litellm"
	KindGPUStack  = "gpustack"
	KindLemonade  = "lemonade"   // AMD Ryzen AI server
	KindOmniRoute = "omnirouter" // OmniRoute local AI gateway (port 20128)
)

type ModelInfo struct {
	Name     string
	SizeVRAM uint64
	CtxMax   uint64
}

// ProviderSnapshot is one observation of an inference backend.
type ProviderSnapshot struct {
	Label   string
	Kind    string
	Addr    string
	OK      bool
	Err     string
	Version string // engine version, best effort

	PID     int     // serving process, when found locally
	ProcRSS uint64  // resident memory of that process
	ProcCPU float64 // percent of one core

	// OutT0/InT0 timestamp Hist[0]; combined with the collector cadence this
	// lets the UI place samples on an absolute time axis (outages and late
	// joiners no longer skew the chart window).
	OutT0 time.Time
	InT0  time.Time

	Models []ModelInfo

	OutTokPS float64
	InTokPS  float64
	Running  int
	Waiting  int
	KVPct    float64 // kv cache usage, 0..100
	TTFTms   float64 // engine-reported avg ttft if available

	OutHist []float64
	InHist  []float64
}

func (p *ProviderSnapshot) PrimaryModel() string {
	if len(p.Models) > 0 {
		return p.Models[0].Name
	}
	return "-"
}

// AgentEvent is a token-usage event pushed by an agent or harness. The HTTP
// wire shape is defined separately by ingest's agentEventWire.
type AgentEvent struct {
	At           time.Time
	Agent        string
	Model        string
	Kind         string // turn | tool | error | note
	PromptTokens int64
	OutputTokens int64
	Note         string
}

// ProbeSample is one synthetic generation probe result.
type ProbeSample struct {
	At     time.Time
	Addr   string
	Model  string
	OK     bool
	Err    string
	TTFTms float64
	TokPS  float64
	Tokens int
}

// TempReading is one thermal sensor value in millidegrees Celsius.
type TempReading struct {
	Label  string
	MilliC int
	IsGPU  bool
}

// GPUDevice is one accelerator as reported by sysfs/vendor CLIs (no vendor
// libraries linked).
type GPUDevice struct {
	Vendor   string // nvidia | amd | intel | apple
	Index    int
	Name     string
	MilliC   int
	MemUsed  uint64
	MemTotal uint64
	UtilPct  float64
	PowerW   float64
	Driver   string
}

// SysSample carries host-level vitals: RAM, swap, load and temperatures.
type SysSample struct {
	CPUModel string
	OsName   string // PRETTY_NAME / product version
	Kernel   string // uname -r

	MemTotal uint64
	MemUsed  uint64

	SwapTotal uint64
	SwapUsed  uint64

	Load1  float64
	Load5  float64
	Load15 float64

	HostUptime time.Duration
	Drivers    map[string]string // vendor -> version
	NPUs       []string          // detected accelerator drivers
	RemoteHost string            // set when stats come via ssh

	Temps []TempReading
	GPUs  []GPUDevice
}

// Snapshot is everything the UI needs for one frame.
type Snapshot struct {
	At        time.Time
	Uptime    time.Duration
	Providers []ProviderSnapshot
	Agents    []AgentEvent // newest last
	Probes    []ProbeSample
	Sys       *SysSample
}
