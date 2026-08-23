package remote

import (
	"context"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"tokentop/internal/core"
	"tokentop/internal/sysmon"
)

func parseURL(raw string) (*url.URL, error) { return url.Parse(raw) }

var ioDiscard = nopWriter{}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// Stats samples load and memory from the remote /proc every few seconds and
// merges it into snapshots, tagged with the host so the UI can show origin.
type Stats struct {
	Target Target

	mu   sync.Mutex
	last core.SysSample
	at   time.Time
}

// Run polls until ctx is done.
func (s *Stats) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	s.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.poll(ctx)
		}
	}
}

func (s *Stats) poll(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "ssh", s.Target.sshArgs("--",
		"cat /proc/loadavg 2>/dev/null; echo ---; cat /proc/meminfo 2>/dev/null")...).Output()
	if err != nil {
		return // keep last good sample; UI shows staleness via age
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parseRemoteStats(string(out), &s.last)
	s.last.RemoteHost = s.Target.Host
	s.at = time.Now()
}

// Merge overlays fresh remote stats onto a local sample. Stale data (>20s)
// is ignored entirely.
func (s *Stats) Merge(into *core.SysSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at.IsZero() || time.Since(s.at) > 20*time.Second {
		return
	}
	into.Load1, into.Load5, into.Load15 = s.last.Load1, s.last.Load5, s.last.Load15
	if s.last.MemTotal > 0 {
		into.MemTotal, into.MemUsed = s.last.MemTotal, s.last.MemUsed
		into.SwapTotal, into.SwapUsed = s.last.SwapTotal, s.last.SwapUsed
	}
	into.RemoteHost = s.last.RemoteHost
}

// parseRemoteStats reads the combined loadavg+meminfo dump: everything
// before the --- marker is loadavg, after it is /proc/meminfo.
func parseRemoteStats(out string, s *core.SysSample) {
	lines := strings.Split(out, "\n")
	var meminfo strings.Builder
	inMem := false
	for _, line := range lines {
		switch {
		case strings.TrimSpace(line) == "---":
			inMem = true
		case !inMem:
			if l1, l5, l15 := sysmon.ParseLoadavg(line); l1 > 0 || l5 > 0 || l15 > 0 {
				s.Load1, s.Load5, s.Load15 = l1, l5, l15
			}
		default:
			meminfo.WriteString(line + "\n")
		}
	}
	if meminfo.Len() > 0 {
		sysmon.ParseMeminfo([]byte(meminfo.String()), s)
	}
}
