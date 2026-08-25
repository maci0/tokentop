package procs

import (
	"os"
	"testing"
)

func TestExtractPort(t *testing.T) {
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"ollama", "serve"}, 0},
		{[]string{"llama-server", "--port", "8081"}, 8081},
		{[]string{"vllm", "--port=9000"}, 9000},
		{[]string{"x", "--http-port", "5002"}, 5002},
		{[]string{"x", "-p", "1234"}, 0}, // -p is ambiguous, must not match
	}
	for _, c := range cases {
		if got := ExtractPort(c.args); got != c.want {
			t.Errorf("ExtractPort(%v) = %d, want %d", c.args, got, c.want)
		}
	}
}

func TestMatchEngine(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		engine string
		port   int
		ok     bool
	}{
		{"ollama", []string{"ollama", "serve"}, "ollama", 11434, true},
		{"llama-server", []string{"/usr/bin/llama-server", "--port", "8081"}, "llama.cpp", 8080, true}, // def; hint overrides separately
		{"llamafile", []string{"llamafile", "--port", "8080"}, "llama.cpp", 8080, true},
		{"python3", []string{"python3", "-m", "vllm.entrypoints.openai.api_server"}, "vllm", 8000, true},
		{"python3", []string{"python3", "-m", "sglang.launch_server"}, "sglang", 30000, true},
		{"tritonserver", []string{"/opt/tritonserver/bin/tritonserver"}, "triton", 8000, true},
		{"koboldcpp.py", []string{"python3", "koboldcpp.py", "--port", "5001"}, "koboldcpp", 5001, true},
		{"jan", []string{"/opt/Jan/jan"}, "jan", 1337, true},
		{"LM Studio Helper", []string{"/opt/LM Studio Helper"}, "lmstudio", 1234, true},
		{"bash", []string{"bash", "-c", "janitor --clean"}, "", 0, false}, // 'jan' false-positive guard
		{"firefox", []string{"firefox"}, "", 0, false},
	}
	for _, c := range cases {
		i := Info{PID: 1, Name: c.name, Args: c.args}
		i.PortHint = ExtractPort(c.args)
		eng, def, ok := MatchEngine(i)
		if ok != c.ok || eng != c.engine || (ok && def != c.port) {
			t.Errorf("match(%s %v) = %q/%d/%v, want %q/%d/%v",
				c.name, c.args, eng, def, ok, c.engine, c.port, c.ok)
		}
	}
}

func TestSelfIsSkipped(t *testing.T) {
	s := NewSampler()
	list := s.Snapshot()
	for _, p := range list {
		if p.PID == os.Getpid() && p.Name == baseName(os.Args[0]) {
			t.Errorf("toktop's own test process leaked in: %+v", p)
		}
	}
}

// Derived CPU percentages saturate at zero (counter resets must not read as
// negative load) and cap at 100 cores: many-core boxes legitimately exceed
// 100% of one core, but a runaway multiplier must stay bounded.
func TestClampPctBounds(t *testing.T) {
	cases := map[float64]float64{
		-1:            0,
		0:             0,
		55.5:          55.5,
		100 * 1024:    100 * 1024,
		100*1024 + .5: 100 * 1024,
	}
	for in, want := range cases {
		if got := clampPct(in); got != want {
			t.Errorf("clampPct(%v) = %v, want %v", in, got, want)
		}
	}
}
