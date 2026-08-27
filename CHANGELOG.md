# Changelog

Consumer-facing changes to the `toktop` CLI, the ingest `/v1/events` body, and
the `agentusage` Go package. GitHub Releases also attach auto-generated notes
from git; this file is the summary aimed at people upgrading.

The project is 0.x: those surfaces may change without a major bump. `toktop
update` always fetches the latest GitHub release; older tags are not a
support channel (see SECURITY.md).

## [Unreleased]

### Added

- Agent rows whose traffic is already counted by a watched engine are labelled
  `via <engine>` and are not added again to header or chart totals.
- Ingest `POST /v1/events` accepts optional `id` (a repeat of a key still in
  the retained feed is ignored, so retries are safe), `thinking_tokens`, and
  `via_engine` (the same attribution, so a harness POST is not added on top
  of a watched engine's totals).
- Ingest token counts accept whole JSON numbers (`100.0`, `1e2`), matching
  what Python `json.dumps` of a float emits.

### Changed

- `agentusage.Sample` now has `Input` (billed prompt tokens since attach).
  Prompt rates use that field, not max-context minus summed output.
- Header and chart totals skip `via_engine` events individually, so an agent
  that switches onto a watched engine still contributes tokens spent before.

### Fixed

- Ingest type errors for `POST /v1/events` name the JSON field (for example
  `prompt_tokens must be an integer`) instead of the internal Go type.
- `go install` binaries report the installed module version from `--version`
  and `toktop update`, instead of impersonating 0.1.0.
- Crush session databases are summed once per review; a continued session
  contributes only growth after attach.
- Transcripts that grew under one mtime stamp are still read.

## [0.5.0] - 2026-08-26

Latest tagged release. Binaries, checksums, and a CycloneDX SBOM are on
[GitHub Releases](https://github.com/maci0/toktop/releases/tag/v0.5.0).
