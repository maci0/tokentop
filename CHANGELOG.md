# Changelog

Consumer-facing changes to the `toktop` CLI, the ingest `/v1/events` body, and
the `agentusage` Go package. GitHub Releases also attach auto-generated notes
from git; this file is the summary aimed at people upgrading.

The project is 0.x: those surfaces may change without a major bump. `toktop
update` always fetches the latest GitHub release; older tags are not a
support channel (see SECURITY.md).

## [Unreleased]

### Added

- Ingest `POST /v1/events` honors `Idempotency-Key`: when an event omits
  `id`, the key plus the line's index is used, so a retried POST of the same
  stream is ignored instead of double-counted.
- `agentusage.InputRate` reports billed prompt tokens per second between two
  samples, matching `Rate` for output.
- `agentusage.Process.Watch` starts a watcher from a discovered process so the
  agent name and working directory cannot be swapped.
- Ingest logs 404/405 and handler panics on the same structured stderr line
  as POST `/v1/events` (including `method` and `path`). A 202 whose body
  cannot be written is a warning, not a success.

### Changed

- The agent event ring keeps 512 events so several agents over the 30s rate
  window are not evicted by a shared 64-slot cap.

### Fixed

- `agentusage` counts a thinking-only reading (opencode SQLite, and Claude /
  Qwen / Codex transcript lines that carry reasoning with no billed output)
  instead of reporting nothing until the first completion token.
- `toktop help` and `toktop version --help` list the same flags as
  `toktop --help`.
- `toktop --help update` matches `toktop help update`; extra arguments to
  `--version` are a usage error, like `toktop version`.
- `--agents` readings carry a stable event id, so a retried report of the
  same sample is not added twice.
- `LoadDefinitions` skips specs with only blank roots, matching `RegisterSpec`,
  so `Supported` is not true for an agent `Watch` would reject.
- `LoadDefinitions` names the file in malformed-JSON and unreadable-file errors.
- Agent names passed to `RegisterSpec`, `LoadDefinitions`, `Watch`, and
  `Supported` are trimmed, so a definition of `"claude "` is the same agent
  Discover reports.
- `agentusage` compiles on GOOS values other than linux, darwin, and windows:
  `Discover` reports nothing there, matching `Peers`.

## [0.7.0] - 2026-08-30

Latest tagged release. Binaries, checksums, and a CycloneDX SBOM are on
[GitHub Releases](https://github.com/maci0/toktop/releases/tag/v0.7.0).

### Added

- `agentusage` reads dsh's default session log (`session.jsonl.zstd`):
  concatenated independent Zstandard frames, with the provider's
  `inputTokens` / `outputTokens` / `reasoningTokens` on each completed
  `assistant/message`. Uncompressed `.jsonl` still works. The streaming
  usage chunk is not counted; it repeats the message.

## [0.6.1] - 2026-08-28

Binaries, checksums, and a CycloneDX SBOM are on
[GitHub Releases](https://github.com/maci0/toktop/releases/tag/v0.6.1).

### Changed

- Source builds and `go install` require Go 1.27, the version in `go.mod`.
- `--demo` uses `math/rand/v2`. The same `--seed` no longer matches a 1.26 frame.

### Fixed

- `--agents` refuses a symlink that points outside a transcript root, so a
  writable store cannot pull in another project's session as usage.

## [0.6.0] - 2026-08-27

Binaries, checksums, and a CycloneDX SBOM are on
[GitHub Releases](https://github.com/maci0/toktop/releases/tag/v0.6.0).

### Added

- Agent rows whose traffic is already counted by a watched engine are labelled
  `via <engine>` and are not added again to header or chart totals.
- Ingest `POST /v1/events` accepts optional `id` (a repeat of a key still in
  the retained feed is ignored, so retries are safe), `thinking_tokens`, and
  `via_engine` (the same attribution, so a harness POST is not added on top
  of a watched engine's totals).
- Ingest token counts accept whole JSON numbers (`100.0`, `1e2`), matching
  what Python `json.dumps` of a float emits.
- `agentusage.MatchingEndpoints` attributes many agent processes to engines
  in one connection-table pass.
- windows/arm64 release binaries, matching the other ARM64 targets.
- `agentusage.ErrEmptyTool`, `ErrNoRoots`, and `ErrInvalidDefinitions` so
  `RegisterSpec` and `LoadDefinitions` failures are matchable with `errors.Is`.

### Changed

- `agentusage.Sample` now has `Input` (billed prompt tokens since attach).
  Prompt rates use that field, not max-context minus summed output.
- Header and chart totals skip `via_engine` events individually, so an agent
  that switches onto a watched engine still contributes tokens spent before.
- Dashboard colors match toktop.ai (cool dark, green accent, amber pressure).
  The wordmark is a single accent color.
- The engine list panel is titled ENGINES, matching the header, site, and
  `--once --plain` report.
- Per-agent token counts use ▲ for output and ▼ for prompt, the same
  directions as the header rates.

### Fixed

- Empty dashboard names how to quit and re-run, shows when it is paused, and
  does not suggest `--agents` when that watch is already on.
- Failed probes show the error instead of a green zero rate.
- Engine rows label the KV-cache bar and run/wait queues instead of a bare
  bar and `r/w`.
- Help describes `p` as a real generation, not a synthetic one; the empty
  PROBES panel says `--probe N` needs a quit and re-run.
- Agents-only AGENT FEED shows ingest-down and the POST target, matching
  the full dashboard.
- `--add` rejects non-http(s) URLs, values with no host, and URLs that embed
  userinfo (use `--bearer` / `$TOKTOP_BEARER`). `--ingest` rejects an empty
  or non-`host:port` listen address instead of binding every interface on an
  ephemeral port. `--bearer` that is explicitly empty no longer falls through
  to `$OMNIROUTE_API_KEY` / `$TOKTOP_BEARER`. `--ssh-key` expands `~` and
  fails at startup if the file is missing.
- Agent session directories on Windows (and default APFS) match the
  filesystem's case and separator rules, so transcripts are not dropped
  when an agent recorded `C:/Users/Foo` and toktop resolved `c:\users\foo`.
- `toktop ssh://host` on Windows uses `%USERNAME%` when `$USER` is unset, and
  strips a `DOMAIN\` prefix from the account name.
- Crush session stores fill `Sample.Input` from `prompt_tokens`, matching the
  other adapters.
- `Sample.Empty` is false when only thinking tokens were observed.
- Ingest type errors for `POST /v1/events` name the JSON field (for example
  `prompt_tokens must be an integer`) instead of the internal Go type.
- `go install` binaries report the installed module version from `--version`
  and `toktop update`, instead of impersonating 0.1.0.
- `--agents` no longer re-walks every transcript store on each tick, and
  engine attribution reads the kernel connection tables once per pass.
- Crush session databases are summed once per attach; a continued session
  contributes only growth after attach. A store that cannot be read at
  attach is retried rather than treated as empty, so pre-attach tokens are
  not dumped into the review once it becomes readable.
- Transcripts that grew under one mtime stamp are still read.
- Probes hang up after the requested token budget if an engine keeps
  streaming, and ignore engine-reported usage figures far past that budget.
- `--agents` retargets a watcher when the kernel reuses a PID, so tokens
  are not attributed to the previous process.

## [0.5.0] - 2026-08-26

Binaries, checksums, and a CycloneDX SBOM are on
[GitHub Releases](https://github.com/maci0/toktop/releases/tag/v0.5.0).
