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
  the retained feed is ignored, so retries are safe) and `thinking_tokens`.

### Fixed

- `go install` binaries report the installed module version from `--version`
  and `toktop update`, instead of impersonating 0.1.0.
- Crush session databases are summed once per review; a continued session
  contributes only growth after attach.
- Transcripts that grew under one mtime stamp are still read.

## [0.5.0] - 2026-08-26

Latest tagged release. Binaries, checksums, and a CycloneDX SBOM are on
[GitHub Releases](https://github.com/maci0/toktop/releases/tag/v0.5.0).
