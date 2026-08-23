# tokentop

`btop` for AI: a terminal dashboard for LLM inference engines and the agents
hammering them.

```
./tokentop --demo          # simulated fleet, works instantly
./tokentop                 # auto-discovers local engines (ports + processes)
tokentop ssh://maci@box    # watch engines on another host
```

## What it shows

- **Backends** - every engine found locally or via ssh, with model, version,
  KV-cache pressure, queue depth and throughput. Fingerprinted kinds:
  Ollama, llama.cpp/llamafile/ramalama, vLLM, SGLang, TRT-LLM/Triton,
  LM Studio, MLX (mlx-lm / LM Studio), KoboldCpp, LocalAI, TGI,
  text-generation-webui, TabbyAPI, LiteLLM, GPUStack, Lemonade - plus a
  generic OpenAI-compatible fallback so nothing is left out.
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
  (incl. CUDA), NPU enumeration, GPU temp/util/VRAM/power/fans/clocks, and a
  second identity row for sensors.

## Zero vendor libraries

Everything comes from procfs/sysfs/sysctl, vendor CLIs it shells out to
(`nvidia-smi`, `rocm-smi`, `xpu-smi`, `system_profiler`) or plain HTTP from
the engines themselves. No NVML, no Level Zero, no cgo: single static binary,
trivially cross-compiled.

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

SSH mode needs key-based auth (`BatchMode=yes`); the remote only needs a
POSIX shell - no agent is installed.

## Keys

| key | action |
|---|---|
| `q` | quit |
| `space` | pause streaming |
| `p` | fire probes at every backend |
| `?` | help |

## Flags

```
--demo            simulated fleet, zero setup
--add URL         attach an openai-compatible endpoint (repeatable)
ssh://user@host   positional; monitor remote hosts (repeatable)
--probe N         auto-probe every N seconds
--interval D      poll interval (default 1s)
--ingest ADDR     agent event listen address (default 127.0.0.1:8420)
--no-ingest       disable the event endpoint
--once            render one frame and exit
--seed N          demo RNG seed
```

## Build & test

```
go test ./...
go build -o tokentop .
GOOS=darwin GOARCH=arm64 go build -o tokentop-macos .
GOOS=windows GOARCH=amd64 go build -o tokentop.exe .
```

Releases: push a tag `v*` and GitHub Actions attaches binaries for
linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64.
