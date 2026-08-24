# tokentop

`btop` for AI: a terminal dashboard for LLM inference engines and the agents
hammering them.

<p align="center">
  <img src="docs/images/dashboard.png" alt="tokentop dashboard" width="900">
</p>

```
./tokentop --demo          # simulated fleet, works instantly
./tokentop                 # auto-discovers local engines (ports + processes)
tokentop ssh://maci@box    # watch engines on another host
```

## What it shows

- **Backends** - every engine found locally or via ssh, with model, version,
  KV-cache pressure, queue depth and throughput. Fingerprinted kinds:
  Ollama, llama.cpp/llamafile/ramalama, vLLM, SGLang, TRT-LLM/Triton,
  LM Studio, MLX (mlx-lm / LM Studio), KoboldCpp, LocalAI, TGI, LiteLLM,
  GPUStack, Lemonade, OmniRoute (auto-detected via its routing header;
  per-model context windows shown) - plus a generic OpenAI-compatible
  fallback so nothing is left out (text-generation-webui and TabbyAPI are
  discovered by process/port but identified as generic OpenAI).
- **Throughput charts** - aggregate decode + prompt tokens/sec as heat-colored
  area charts; engine-published tok/s gauges are trusted when present.
- **Probes** (`p`, `--probe N`) - tiny streaming generations measuring real
  TTFT and decode speed per backend.
- **Agent feed** - any harness can POST usage events:
  ```
  curl -X POST localhost:8420/v1/events -d \
    '{"agent":"coder","kind":"tool","prompt_tokens":4200,"output_tokens":310,"note":"shell(git status)"}'
  ```
- **System strip** - RAM/swap/load, CPU model, OS+kernel, GPU driver versions
  (incl. CUDA), NPU enumeration (Intel NPU, AMD XDNA xN, Qualcomm Cloud
  AI100, Apple Neural Engine with chip generation), GPU temp/util/VRAM/
  power - on Apple Silicon including live wired-memory and
  utilization from IOAccelerator - and a second identity row for sensors.
- **Braille charts** - dot-matrix rendering with btop-style fading bloom;
  timescale compresses leftward (`t` toggles) with faint grid marks showing
  where each doubling begins.
- **Hot reload** - rebuild the binary while it runs and tokentop restarts
  into the fresh build automatically (`--no-hot-reload` to disable).

## Zero vendor libraries

Everything comes from procfs/sysfs/sysctl, vendor CLIs it shells out to
(`nvidia-smi`, `rocm-smi`, `xpu-smi`, `system_profiler`, `ioreg` - each the
vendor's documented interface with no in-process alternative) or plain HTTP
from the engines themselves. SSH transport is an embedded pure-Go client, so
remote monitoring needs no ssh binary either. No NVML, no Level Zero, no
cgo: single static binary, trivially cross-compiled.

Linux reads `/proc` + `/sys`; macOS uses sysctls and `system_profiler`;
Windows uses `GlobalMemoryStatusEx`, `RtlGetVersion` and one CIM query for
process command lines.

## Engines: how discovery works

1. Running **processes** matching well-known names (`ollama`, `llama-server`,
   `vllm`, `sglang`, `koboldcpp`, `lm studio`, `lemonade-server`, ...) give
   candidate URLs, honouring `--port` flags.
2. A scan of well-known ports follows (11434, 30000, 8000, 13305, 8080,
   1234, 5001, 5000, 4000, 1337, 4891, 7860, ...).
3. Each candidate is fingerprinted by its HTTP surface; anything unrecognized
   that still speaks OpenAI is shown as such.

Attach anything explicitly:

```
tokentop --add http://10.0.0.5:8000        # repeatable
tokentop ssh://user@host                   # remote engines + host vitals
```

SSH mode is built in (pure Go, no ssh binary needed) and the remote only
needs a POSIX shell - no agent is installed. Discovery reads the remote
`/proc` directly: listening sockets from `/proc/net/tcp(+6)` (with an active
port probe as fallback) plus engine processes with their `--port` flags, so
engines on custom ports are found just like locally. Engine traffic flows
through direct TCP channels on one persistent connection - no local port
forwards, nothing to race or leak. Host vitals stream the same way: load,
memory, uptime, CPU model, OS, kernel and GPU rows (`nvidia-smi`, or
`rocm-smi` on AMD boxes).

Auth tries, in order: `--ssh-key PATH`, keys from `~/.ssh/config`
(`HostName`, `User`, `Port`, `IdentityFile` are honored), your default
keys, ssh-agent, and finally a password prompt when stdin is a terminal
(or set `TOKENTOP_SSH_PASSWORD` for headless runs). Host keys use
trust-on-first-use, stored under your config dir; a changed key is refused
loudly.

## Keys

| key | action |
|---|---|
| `q` | quit |
| `esc` | close help / quit |
| `space` | pause streaming |
| `p` | fire probes at every backend |
| `t` | toggle compressed timescale + grid |
| `?` | help |

## Flags

```
--demo            simulated fleet, zero setup
--add URL         attach an openai-compatible endpoint (repeatable)
ssh://user@host   positional; monitor remote hosts (repeatable)
--ssh-key PATH    private key for ssh targets (overrides ~/.ssh/config)
--bearer TOKEN    bearer token sent to engines; OmniRoute API keys etc.
                  (env: OMNIROUTE_API_KEY, then TOKENTOP_BEARER)
--probe N         auto-probe every N seconds
--interval D      poll interval (default 1s)
--ingest ADDR     agent event listen address (default 127.0.0.1:8420)
--no-ingest       disable the event endpoint
--once            render one frame and exit
--frames N        with --once: snapshots to accumulate before rendering
--seed N          demo RNG seed
--no-hot-reload   disable restart-on-rebuild while running
--version         print version and exit
```

Password auth for ssh targets: interactive prompt, or `TOKENTOP_SSH_PASSWORD`.

## Environment variables

| variable | what it does |
|---|---|
| `OMNIROUTE_API_KEY` | bearer token fallback for `--bearer` (checked first) |
| `TOKENTOP_BEARER` | bearer token fallback for `--bearer` (checked after `OMNIROUTE_API_KEY`) |
| `TOKENTOP_SSH_PASSWORD` | ssh password for headless runs; otherwise an interactive prompt |
| `TOKENTOP_COLUMNS` / `TOKENTOP_LINES` | fixed frame size for `--once` output (screenshots, capture); must be > 40 / > 20 |

The flag always wins over its env fallback. Prefer an env var over
`--bearer` for tokens: command-line arguments are visible in process
listings to every user on the host. Unknown `TOKENTOP_*` variables are
reported at startup, so a typo fails loudly instead of doing nothing.
Out-of-range flag values (`--interval 0`, negative `--probe`, `--frames < 1`
with `--once`) abort with exit code 2 instead of being silently adjusted.

## Build & test

```
make help                          # every task, one line each
make build                         # host binary, version-stamped
make demo                          # build, then run the simulated fleet
make test                          # all tests, -race -shuffle=on
make ci                            # exactly what CI gates on before merging
go test ./internal/ui              # one package while iterating
go test ./internal/core -run TestSanitizeTextPreservesUTF8   # one test
```

Cross-compiles (no cgo anywhere):

```
GOOS=darwin GOARCH=arm64 go build -o tokentop-macos ./cmd/tokentop
GOOS=windows GOARCH=amd64 go build -o tokentop.exe ./cmd/tokentop
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for prerequisites, the edit-test loop,
and what CI runs.

Releases: push a tag `v*` and GitHub Actions attaches binaries for
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, plus a
CycloneDX SBOM of every dependency (`make sbom`). CI runs `govulncheck` on
every push; Dependabot keeps go modules and workflow actions current.
