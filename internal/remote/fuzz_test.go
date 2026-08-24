package remote

import (
	"testing"

	"tokentop/internal/core"
)

// FuzzParseVitals throws arbitrary bytes at parseVitals, the entry point for
// everything an SSH remote sends back from the vitals script: loadavg,
// meminfo, uptime, CPU/OS/kernel strings and GPU telemetry (nvidia-smi CSV
// or rocm-smi JSON). A hostile remote must not be able to panic the
// dashboard or plant impossible state in a sample: saturating memory math,
// non-negative sensor readings and load-validity reporting are asserted.
func FuzzParseVitals(f *testing.F) {
	for _, seed := range []string{
		vitalsDump,
		vitalsDumpFrom("1.0 1.0 1.0", "", "", "", "", "", rocmJSON),
		vitalsDumpFrom("", "", "", "", "", "", ""),
		"\n%tokentop%\n%tokentop%\n%tokentop%\n%tokentop%\n%tokentop%\n%tokentop%\n",
		"",
		"%tokentop%",
		"nan nan nan\n%tokentop%MemTotal: x\n%tokentop%-1e999\n%tokentop%%tokentop%%tokentop%%tokentop%\n-1, , [N/A], [N/A], [N/A], [N/A], [N/A], [N/A]",
		"1e300 0 0",
		"MemTotal: 99999999999999999999 kB\nMemAvailable: 1 kB\nSwapTotal: 5 kB\nSwapFree: 9 kB",
		`{"card0":{"Temperature (Sensor edge) (C)":"1e308","GPU use (%)":"nan","Used Memory (VRAM)":[1e308],"Total Memory (VRAM)":{"values":[-42]}}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var s core.SysSample
		loadsOK := parseVitals(string(data), &s)

		if loadsOK != (s.Load1 > 0 || s.Load5 > 0 || s.Load15 > 0) {
			t.Fatalf("loadsOK=%v but loads = %v %v %v", loadsOK, s.Load1, s.Load5, s.Load15)
		}
		if s.MemUsed > s.MemTotal {
			t.Fatalf("mem used %d exceeds total %d", s.MemUsed, s.MemTotal)
		}
		if s.SwapUsed > s.SwapTotal {
			t.Fatalf("swap used %d exceeds total %d", s.SwapUsed, s.SwapTotal)
		}
		for i, g := range s.GPUs {
			if g.UtilPct < 0 || g.PowerW < 0 {
				t.Fatalf("gpu[%d] negative sensor reading: %+v", i, g)
			}
		}
	})
}
