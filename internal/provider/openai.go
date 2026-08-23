package provider

import (
	"context"
	"strings"
	"sync"

	"tokentop/internal/core"
)

// OpenAICompat scrapes any server exposing Prometheus /metrics plus
// /v1/models: vLLM, SGLang, llama.cpp-server, TGI, LocalAI, LiteLLM,
// GPUStack, Lemonade and anything else OpenAI-shaped.
type OpenAICompat struct {
	base    string
	label   string
	kind    string
	version sync.Once
}

func NewOpenAICompat(base, label, kind string) *OpenAICompat {
	return &OpenAICompat{base: strings.TrimRight(base, "/"), label: label, kind: kind}
}

func (o *OpenAICompat) Label() string { return o.label }
func (o *OpenAICompat) Addr() string  { return o.base }
func (o *OpenAICompat) Kind() string  { return o.kind }

// tolerantKinds keep working even without both metrics and /v1/models.
func tolerantKind(kind string) bool {
	switch kind {
	case core.KindLemonade, core.KindGPUStack, core.KindLiteLLM,
		core.KindTRTLLM, core.KindLMStudio:
		return true
	}
	return false
}

func (o *OpenAICompat) Poll(ctx context.Context) (*Metrics, error) {
	m := &Metrics{}
	text, merr := getText(httpClient, o.base+"/metrics")
	if merr != nil && o.kind == core.KindVLLM {
		return nil, merr // vLLM without metrics is not worth showing
	}
	if merr == nil {
		classify(ParseProm(text), m)
	}
	var lm struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	haveModels := getJSON(ctx, o.base+"/v1/models", &lm) == nil
	if haveModels {
		for _, d := range lm.Data {
			if d.ID != "" {
				m.Models = append(m.Models, core.ModelInfo{Name: d.ID})
			}
		}
	}
	if !haveModels && merr != nil && !tolerantKind(o.kind) {
		return nil, endpointErr{o.base}
	}

	switch o.kind {
	case core.KindLMStudio:
		enrichLMStudio(ctx, o.base, m)
	case core.KindLemonade:
		enrichLemonade(ctx, o.base, m)
	}
	if m.Version == "" {
		m.Version = fetchVersion(&o.version, o.base)
	}
	return m, nil
}

// enrichLMStudio pulls the native /api/v0/models feed: load state and context
// length that the thin OpenAI listing lacks.
func enrichLMStudio(ctx context.Context, base string, m *Metrics) {
	var v0 struct {
		Data []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
			State      string `json:"state"`
			MaxContext int64  `json:"max_context_length"`
		} `json:"data"`
	}
	if getJSON(ctx, base+"/api/v0/models", &v0) != nil {
		return
	}
	m.Models = m.Models[:0]
	for _, d := range v0.Data {
		if d.Type != "" && !strings.EqualFold(d.Type, "llm") {
			continue // embeddings and friends are noise here
		}
		mi := core.ModelInfo{Name: d.ID, State: d.State}
		if d.MaxContext > 0 {
			mi.CtxMax = uint64(d.MaxContext)
		}
		m.Models = append(m.Models, mi)
	}
}

// enrichLemonade reads /v1/health: version plus loaded-model inventory.
func enrichLemonade(ctx context.Context, base string, m *Metrics) {
	var h struct {
		Version     string `json:"version"`
		ModelLoaded string `json:"model_loaded"`
		AllLoaded   []struct {
			ModelName string  `json:"model_name"`
			CtxSize   float64 `json:"ctx_size"`
		} `json:"all_models_loaded"`
	}
	if getJSON(ctx, base+"/v1/health", &h) != nil {
		return
	}
	if h.Version != "" {
		m.Version = h.Version
	}
	switch {
	case len(h.AllLoaded) > 0:
		m.Models = m.Models[:0]
		for _, mm := range h.AllLoaded {
			mi := core.ModelInfo{Name: mm.ModelName, State: "loaded"}
			if mm.CtxSize > 0 {
				mi.CtxMax = uint64(mm.CtxSize)
			}
			m.Models = append(m.Models, mi)
		}
	case h.ModelLoaded != "":
		m.Models = []core.ModelInfo{{Name: h.ModelLoaded, State: "loaded"}}
	}
}

type endpointErr struct{ base string }

func (e endpointErr) Error() string { return "no known endpoints on " + e.base }

var errNoEndpoints = endpointErr{}
