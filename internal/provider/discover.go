package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"tokentop/internal/core"
	"tokentop/internal/procs"
)

// scanTimeout bounds each identification request during discovery.
const scanTimeout = 700 * time.Millisecond

var defaultCandidates = []string{
	"http://127.0.0.1:11434", // ollama
	"http://127.0.0.1:30000", // sglang
	"http://127.0.0.1:8000",  // vllm / trtllm-serve / triton / lemonade (legacy)
	"http://127.0.0.1:13305", // lemonade server
	"http://127.0.0.1:8001",
	"http://127.0.0.1:8080", // llama.cpp server / mlx-lm / TGI / localai
	"http://127.0.0.1:8081",
	"http://127.0.0.1:1234", // lm studio (metal/mlx models)
	"http://127.0.0.1:5001", // koboldcpp
	"http://127.0.0.1:5000", // tabbyapi
	"http://127.0.0.1:4000", // litellm proxy
	"http://127.0.0.1:1337", // jan
	"http://127.0.0.1:4891", // gpt4all
	"http://127.0.0.1:7860", // text-generation-webui
	"http://127.0.0.1:80",   // gpustack
	"http://127.0.0.1:3000",
}

// CandidatePorts exposes the well-known engine ports (for remote probing).
func CandidatePorts() []int {
	seen := map[int]bool{}
	var out []int
	for _, base := range defaultCandidates {
		u, err := url.Parse(base)
		if err != nil {
			continue
		}
		if p := u.Port(); p != "" {
			if n, err := strconv.Atoi(p); err == nil && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

var scanClient = &http.Client{Timeout: scanTimeout}

// procSampler lazily starts the platform process sampler; engine processes
// found by name contribute candidate URLs ahead of the blind port scan.
var procSampler = struct {
	sync.Once
	*procs.Sampler
}{}

func procCandidateURLs() []string {
	procSampler.Do(func() { procSampler.Sampler = procs.NewSampler() })
	var urls []string
	for _, p := range procSampler.Snapshot() {
		if p.Engine == "" {
			continue
		}
		port := p.PortHint
		if port == 0 {
			port = p.DefPort
		}
		if port > 0 {
			urls = append(urls, fmt.Sprintf("http://127.0.0.1:%d", port))
		}
	}
	return urls
}

// Discover probes well-known ports plus any engine processes running locally,
// returning a provider for every backend that answers.
func Discover(ctx context.Context) []Provider {
	seen := map[string]bool{}
	var bases []string
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			bases = append(bases, u)
		}
	}
	for _, u := range procCandidateURLs() {
		add(u)
	}
	for _, base := range defaultCandidates {
		add(base)
	}

	var found []Provider
	for _, base := range bases {
		if kind := Identify(ctx, base); kind != "" {
			found = append(found, newProvider(kind, base))
		}
	}
	return found
}

// Attach builds a provider for an explicit URL, identifying its kind first.
func Attach(ctx context.Context, base string) Provider {
	if kind := Identify(ctx, base); kind != "" {
		return newProvider(kind, base)
	}
	return nil
}

func newProvider(kind, base string) Provider {
	if kind == core.KindOllama {
		return NewOllama(base)
	}
	return NewOpenAICompat(base, kind, kind)
}

// Identify returns the provider kind serving base, or "" if none matches.
// Order matters: specific metrics prefixes first, then engine-specific
// endpoints, then generic OpenAI probing.
func Identify(ctx context.Context, base string) string {
	if isOllama(ctx, base) {
		return core.KindOllama
	}
	if text, err := getText(scanClient, base+"/metrics"); err == nil {
		switch {
		case strings.Contains(text, "vllm:"):
			return core.KindVLLM
		case strings.Contains(text, "sglang:"):
			return core.KindSGLang
		case strings.Contains(text, "nv_inference"), strings.Contains(text, "trtllm"):
			return core.KindTRTLLM
		}
	}
	if sglangInfoOK(ctx, base) {
		return core.KindSGLang
	}
	if tritonReady(ctx, base) {
		return core.KindTRTLLM // Triton Inference Server (typical TRT-LLM host)
	}
	if probeContains(ctx, base, "/v1/health", `"version"`, `"status"`) ||
		probeContains(ctx, base, "/api/v1/models", `"data"`) {
		return core.KindLemonade
	}
	models := getOpenAIModels(ctx, base)
	switch {
	case models == nil:
		// engines whose OpenAI listing needs auth or lives on another path
		if bodyOK(ctx, base+"/health/liveliness", "alive") ||
			bodyOK(ctx, base+"/health/liveliness", "I'm alive!") {
			return core.KindLiteLLM
		}
		return ""
	case idsLookMLX(models):
		return core.KindMLX // mlx-community models via mlx-lm or LM Studio
	case probeContains(ctx, base, "/api/v0/models", `"state"`):
		return core.KindLMStudio
	case probeContains(ctx, base, "/api/extra/version", "koboldcpp"):
		return core.KindKoboldCPP
	case probeContains(ctx, base, "/info", `"version"`),
		probeContains(ctx, base, "/info", "text-generation"):
		return core.KindTGI
	case probeContains(ctx, base, "/readyz", "OK"),
		probeContains(ctx, base, "/readyz", "ok"):
		return core.KindLocalAI
	case probeContains(ctx, base, "/", "gpustack"):
		return core.KindGPUStack
	case healthOK(ctx, base):
		return core.KindLlamaCPP // llama.cpp /health answers {"status":"ok"}
	default:
		return core.KindOpenAI
	}
}

// probeContains fetches a path and checks that every needle appears.
func probeContains(ctx context.Context, base, path string, needles ...string) bool {
	text, err := getText(scanClient, base+path)
	if err != nil {
		return false
	}
	lower := strings.ToLower(text)
	for _, n := range needles {
		if !strings.Contains(lower, strings.ToLower(n)) {
			return false
		}
	}
	return true
}

// bodyOK reports whether path answers 200 with the substring in its body.
func bodyOK(ctx context.Context, url, substr string) bool {
	text, err := getText(scanClient, url)
	return err == nil && strings.Contains(strings.ToLower(text), strings.ToLower(substr))
}

// sglangInfoOK detects SGLang via its native /get_model_info endpoint.
func sglangInfoOK(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/get_model_info", nil)
	if err != nil {
		return false
	}
	resp, err := scanClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		ModelPath string `json:"model_path"`
	}
	return json.NewDecoder(resp.Body).Decode(&body) == nil && body.ModelPath != ""
}

// tritonReady checks the KServe v2 readiness endpoint served by Triton.
func tritonReady(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/health/ready", nil)
	if err != nil {
		return false
	}
	resp, err := scanClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// idsLookMLX reports whether any served model id looks like an MLX build
// (mlx-community/…, …-mlx-q4 …).
func idsLookMLX(mr *modelsResp) bool {
	for _, d := range mr.Data {
		if strings.Contains(strings.ToLower(d.ID), "mlx") {
			return true
		}
	}
	return false
}

func isOllama(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/ps", nil)
	if err != nil {
		return false
	}
	resp, err := scanClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Models []json.RawMessage `json:"models"`
	}
	return json.NewDecoder(resp.Body).Decode(&body) == nil && body.Models != nil
}

type modelsResp struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func getOpenAIModels(ctx context.Context, base string) *modelsResp {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil
	}
	resp, err := scanClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var mr modelsResp
	if json.NewDecoder(resp.Body).Decode(&mr) != nil || len(mr.Data) == 0 {
		return nil
	}
	return &mr
}

func healthOK(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := scanClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
