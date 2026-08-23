package remote

import (
	"strings"
	"testing"

	"tokentop/internal/core"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		raw     string
		user    string
		host    string
		port    int
		wantErr bool
	}{
		{"ssh://maci@192.168.0.211", "maci", "192.168.0.211", 22, false},
		{"ssh://root@gpu-box:2222", "root", "gpu-box", 2222, false},
		{"ssh://192.168.1.5", "", "192.168.1.5", 22, false},
		{"http://x", "", "", 0, true},
		{"ssh://", "", "", 0, true},
		{"ssh://h:notaport", "", "h", 0, true},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseTarget(%q) expected error", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTarget(%q): %v", c.raw, err)
			continue
		}
		if got.User != c.user || got.Host != c.host || got.Port != c.port {
			t.Errorf("ParseTarget(%q) = %+v, want user=%q host=%q port=%d",
				c.raw, got, c.user, c.host, c.port)
		}
	}
}

func TestProbeScript(t *testing.T) {
	s := probeScript([]int{11434, 8000})
	if !strings.Contains(s, "for p in 11434 8000") {
		t.Errorf("script missing port list:\n%s", s)
	}
	if !strings.Contains(s, "/dev/tcp") || !strings.Contains(s, "nc -z") {
		t.Error("script must try /dev/tcp then nc")
	}
}

func TestParseRemoteStats(t *testing.T) {
	var s core.SysSample
	parseRemoteStats("3.10 2.20 1.05 4/900 12345\n---\nMemTotal:       16096680 kB\nMemAvailable:   8000000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1000000 kB\n", &s)
	if s.Load1 != 3.10 || s.Load15 != 1.05 {
		t.Errorf("load = %v %v %v", s.Load1, s.Load5, s.Load15)
	}
	if want := uint64(16096680) << 10; s.MemTotal != want {
		t.Errorf("memtotal = %d want %d", s.MemTotal, want)
	}
	if s.MemUsed != (uint64(16096680)-8000000)<<10 {
		t.Errorf("memused = %d", s.MemUsed)
	}
	if s.SwapUsed != (uint64(2000000)-1000000)<<10 {
		t.Errorf("swapused = %d", s.SwapUsed)
	}
}

func TestFreeLocalPort(t *testing.T) {
	p := freeLocalPort()
	if p <= 0 || p > 65535 {
		t.Fatalf("port = %d", p)
	}
}
