package remote

import (
	"slices"
	"testing"

	"github.com/maci0/toktop/internal/core"
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
		"\n%toktop%\n%toktop%\n%toktop%\n%toktop%\n%toktop%\n%toktop%\n",
		"",
		"%toktop%",
		"nan nan nan\n%toktop%MemTotal: x\n%toktop%-1e999\n%toktop%%toktop%%toktop%%toktop%\n-1, , [N/A], [N/A], [N/A], [N/A], [N/A], [N/A]",
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

// FuzzParseDiscoveryOutput throws arbitrary bytes at the remote-discovery
// parsers: parseNetTCP and parseProcScan read whatever a hostile SSH host
// prints for the /proc sweeps, and enginePorts plus ForwardSet turn that into
// the tunnel set. Nothing a remote prints may plant an impossible port (a
// tunnel target outside the TCP range is a broken connection at best), lists
// must stay sorted and duplicate-free, and parsing is deterministic.
func FuzzParseDiscoveryOutput(f *testing.F) {
	for _, seed := range []string{
		netTCPSample,
		netTCPSample + "   9: 0100007F:FFFFF 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 ffff8ba0c4a48000 100 0 0 10 1\n",
		"   0: 0100007F:10000 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 f 100 0 0 10 1\n",
		"1 llama-server --port=99999999\n2 ollama serve\n3 python -m vllm.entrypoints.openai.api_server --port -7\n",
		"102 python -m vllm.entrypoints.openai.api_server --port 9911\n103 \nnotapid junk\n",
		"2147483647 x --port 65535\n-5 ollama serve\n0 proc\n",
		"garbage\n\n0:\n x:y z A\n::::\n",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		out := string(data)

		listening := parseNetTCP(out)
		assertPorts(t, "listening", listening)
		if again := parseNetTCP(out); !slices.Equal(again, listening) {
			t.Fatal("parseNetTCP is not deterministic")
		}

		var d Discovery
		d.Listening = listening
		d.EnginePorts = assertPorts(t, "engine", enginePorts(parseProcScan(out)))
		assertPorts(t, "forward set", d.ForwardSet([]int{11434, 8080, 3000}))

		for _, info := range parseProcScan(out) {
			if info.PID <= 0 {
				t.Fatalf("pid %d survived parseProcScan: %+v", info.PID, info)
			}
			if len(info.Args) == 0 {
				t.Fatalf("info %d has no argv: %+v", info.PID, info)
			}
		}
	})
}

// assertPorts fails unless every port is a number something can listen on and
// the list is strictly increasing, which is what sorted+deduped looks like.
func assertPorts(t *testing.T, which string, ports []int) []int {
	t.Helper()
	for i, p := range ports {
		if p < 1 || p > 65535 {
			t.Fatalf("%s[%d] = %d is not a port", which, i, p)
		}
		if i > 0 && p <= ports[i-1] {
			t.Fatalf("%s not sorted/deduped at %d: %v", which, i, ports)
		}
	}
	return ports
}
