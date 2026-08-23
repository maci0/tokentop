package provider

import (
	"testing"

	"tokentop/internal/core"
)

const vllmFixture = `# HELP vllm:num_requests_running Number of requests currently running.
# TYPE vllm:num_requests_running gauge
vllm:num_requests_running{model="qwen"} 3.0
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{model="qwen"} 7.0
# TYPE vllm:gpu_cache_usage_perc gauge
vllm:gpu_cache_usage_perc{model="qwen"} 0.62
# TYPE vllm:prompt_tokens_total counter
vllm:prompt_tokens_total{model="qwen"} 1000.0
# TYPE vllm:generation_tokens_total counter
vllm:generation_tokens_total{model="qwen"} 2500.0
vllm:time_to_first_token_seconds_sum{model="qwen"} 4.0
vllm:time_to_first_token_seconds_count{model="qwen"} 20.0
vllm:request_success_total{finished_reason="stop",model_name="qwen"} 11
`

func TestParsePromSumsLabeledSeries(t *testing.T) {
	fam := ParseProm(vllmFixture)
	if got := fam["vllm:prompt_tokens_total"]; got != 1000 {
		t.Fatalf("prompt_tokens_total = %v, want 1000", got)
	}
	if _, ok := fam["vllm:num_requests_running"]; !ok {
		t.Fatal("labels were not stripped from family names")
	}
}

func TestParsePromSkipsCommentsAndBuckets(t *testing.T) {
	fam := ParseProm("# comment\nm_bucket{le=\"1\"} 2\nother 3\n")
	if len(fam) != 1 || fam["other"] != 3 {
		t.Fatalf("unexpected families: %#v", fam)
	}
}

func TestClassifyVLLM(t *testing.T) {
	var m Metrics
	classify(ParseProm(vllmFixture), &m)
	if m.InTotal != 1000 || m.OutTotal != 2500 {
		t.Errorf("totals = in:%v out:%v", m.InTotal, m.OutTotal)
	}
	if m.Running != 3 || m.Waiting != 7 {
		t.Errorf("queues = run:%d wait:%d", m.Running, m.Waiting)
	}
	if !m.HasKV || m.KVPct < 61.9 || m.KVPct > 62.1 {
		t.Errorf("kv pct = %v hasKV=%v", m.KVPct, m.HasKV)
	}
	if want := 4.0 / 20.0 * 1000; m.TTFTms != want {
		t.Errorf("ttft ms = %v, want %v", m.TTFTms, want)
	}
}

func TestClassifyRatioVsPercentCache(t *testing.T) {
	var ratio Metrics
	classify(map[string]float64{"llamacpp:kv_cache_usage_ratio": 0.75}, &ratio)
	if ratio.KVPct != 75 {
		t.Errorf("ratio -> pct = %v", ratio.KVPct)
	}
	var pct Metrics
	classify(map[string]float64{"kv_cache_usage_perc": 40}, &pct)
	if pct.KVPct != 40 {
		t.Errorf("percent passthrough = %v", pct.KVPct)
	}
}

func TestSplitMetric(t *testing.T) {
	name, val, ok := splitMetric(`a:b_c{label="x,y z"} 1.5e2`)
	if !ok || name != "a:b_c" || val != 150 {
		t.Fatalf("got %q %v %v", name, val, ok)
	}
	if _, _, ok := splitMetric("garbage"); ok {
		t.Fatal("valueless line parsed")
	}
}

// Ensure kind constants stay stable; they key UI colors and probes.
func TestKindConstants(t *testing.T) {
	if core.KindOllama != "ollama" || core.KindVLLM != "vllm" ||
		core.KindLlamaCPP != "llama.cpp" || core.KindOpenAI != "openai" {
		t.Fatal("kind strings drifted")
	}
}
