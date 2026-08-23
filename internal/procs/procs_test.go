package procs

import (
	"os"
	"path/filepath"
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
		if got := extractPort(c.args); got != c.want {
			t.Errorf("extractPort(%v) = %d, want %d", c.args, got, c.want)
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
		i.PortHint = extractPort(c.args)
		eng, def, ok := matchEngine(i)
		if ok != c.ok || eng != c.engine || (ok && def != c.port) {
			t.Errorf("match(%s %v) = %q/%d/%v, want %q/%d/%v",
				c.name, c.args, eng, def, ok, c.engine, c.port, c.ok)
		}
	}
}

// TestSamplerLinuxTree runs the linux sampler against a synthetic /proc tree.
func TestSamplerLinuxTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	// fake ollama: comm-less cmdline NUL-separated; stat with utime+stime
	write("123/cmdline", "ollama\x00serve\x00--port\x0011434\x00")
	write("123/stat", "123 (ollama) S 1 1 0 0 -1 0 0 0 0 0 100 50 0 0 0 0")
	write("123/status", "Name: ollama\nVmRSS:\t 2048 kB\n")
	// kernel thread without cmdline must be skipped
	write("456/stat", "456 (kworker/0:1) S 2 0 0 0")
	// firefox: not an engine
	write("789/cmdline", "/usr/bin/firefox\x00")
	write("789/stat", "789 (firefox) S 1 1 0 0 0 0 500 700 0 0")

	restoreRoot := swapProcRoot(root)
	defer restoreRoot()

	s := NewSampler()
	first := s.Snapshot()
	var found *Info
	for i := range first {
		if first[i].PID == 123 {
			found = &first[i]
		}
	}
	if found == nil {
		t.Fatal("engine process missing from sample")
	}
	if found.Engine != "ollama" || found.PortHint != 11434 || found.RSS != 2048<<10 {
		t.Fatalf("sample = %+v", found)
	}
	if found.CPUPct != 0 {
		t.Errorf("first sample must have no cpu delta, got %v", found.CPUPct)
	}
	for _, p := range first {
		if p.PID == 456 {
			t.Error("kernel thread leaked into listing")
		}
	}
}

func TestSelfIsSkipped(t *testing.T) {
	s := NewSampler()
	list := s.Snapshot()
	for _, p := range list {
		if p.PID == osGetpid() && p.Name == baseName(os.Args[0]) {
			t.Errorf("tokentop's own test process leaked in: %+v", p)
		}
	}
}
