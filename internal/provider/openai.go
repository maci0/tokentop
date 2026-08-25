package provider

import (
	"context"
	"strings"

	"github.com/maci0/toktop/internal/core"
)

// OpenAICompat scrapes any server exposing Prometheus /metrics plus
// /v1/models: vLLM, SGLang, llama.cpp-server, TGI, LocalAI, LiteLLM,
// GPUStack, Lemonade and anything else OpenAI-shaped.
type OpenAICompat struct {
	base    string
	label   string
	kind    string
	version versionCache
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
		core.KindTRTLLM, core.KindLMStudio, core.KindOmniRoute:
		return true
	}
	return false
}

func (o *OpenAICompat) Poll(ctx context.Context) (*Metrics, error) {
	m := &Metrics{}
	text, merr := getText(ctx, httpClient, o.base+"/metrics")
	if merr != nil && o.kind == core.KindVLLM {
		return nil, merr // vLLM without metrics is not worth showing
	}
	if merr == nil {
		classify(parseProm(text), m)
	}
	var lm struct {
		Data []struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
			MaxContextLen int64  `json:"max_context_length"` // LM Studio shape
		} `json:"data"`
	}
	haveModels := getJSON(ctx, o.base+"/v1/models", &lm) == nil
	if haveModels {
		for _, d := range lm.Data {
			if d.ID != "" {
				mi := core.ModelInfo{Name: d.ID}
				if n := max(d.ContextLength, d.MaxContextLen); n > 0 {
					mi.CtxMax = uint64(n)
				}
				m.Models = append(m.Models, mi)
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
		m.Version = o.version.fetch(ctx, o.base)
	}
	return m, nil
}

// enrichLMStudio pulls the native /api/v0/models feed: the full model list
// and context lengths that the thin OpenAI listing lacks.
func enrichLMStudio(ctx context.Context, base string, m *Metrics) {
	var v0 struct {
		Data []struct {
			ID         string `json:"id"`
			Type       string `json:"type"`
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
		mi := core.ModelInfo{Name: d.ID}
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
			mi := core.ModelInfo{Name: mm.ModelName}
			if mm.CtxSize > 0 {
				mi.CtxMax = satUint(mm.CtxSize)
			}
			m.Models = append(m.Models, mi)
		}
	case h.ModelLoaded != "":
		m.Models = []core.ModelInfo{{Name: h.ModelLoaded}}
	}
}

type endpointErr struct{ base string }

func (e endpointErr) Error() string { return "no known endpoints on " + e.base }
