//go:build linux

package procs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSamplerLinuxTree runs the linux sampler against a synthetic /proc tree.
func TestSamplerLinuxTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// fake ollama: comm-less cmdline NUL-separated; stat with utime+stime
	// (fields 14,15) and rss in pages (field 24: 512 pages = 2 MiB)
	write("123/cmdline", "ollama\x00serve\x00--port\x0011434\x00")
	write("123/stat", "123 (ollama) S 1 1 0 0 -1 0 0 0 0 0 100 50 0 0 0 0 1 0 0 0 512")
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
