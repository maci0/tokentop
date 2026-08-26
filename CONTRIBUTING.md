# Contributing to toktop

## Prerequisites

- Go, at the version pinned in `go.mod` (the toolchain downloads the right
  one automatically if your `go` is newer). CI installs the same version via
  `go-version-file: go.mod`.
- A C compiler (`gcc` or `clang`) for `go test -race`; plain builds and
  cross-compiles are pure Go and need nothing else.
- No services or databases: everything is stdlib plus the modules in
  `go.mod`.

## Quickstart

```
git clone https://github.com/maci0/toktop && cd toktop
make test        # all tests, race detector, shuffled order
make demo        # build and run against a simulated fleet
```

## The edit-test loop

Run one package or one test while iterating (drop `-run` for a whole package):

```
go test -race ./internal/ui
go test -race ./internal/core -run TestSanitizeTextPreservesUTF8
```

To see the dashboard render without an interactive terminal:

```
./toktop --demo --once --frames 2
TOKTOP_COLUMNS=120 TOKTOP_LINES=38 ./toktop --demo --once   # fixed size, for screenshots
```

While a TUI instance runs, rebuilding the binary restarts it into the fresh
build automatically (hot reload); pass `--no-hot-reload` to disable.

## Regenerating the README screenshot

`docs/images/dashboard.png` is captured from a live demo frame under tmux
and rendered by `scripts/screenshot.py`:

```
tmux new-session -d -x 120 -y 38 -s shot './toktop --demo'
tmux capture-pane -e -p -t shot > capture.txt
tmux kill-session -t shot
python3 scripts/screenshot.py capture.txt docs/images/dashboard.png
```

Its dependencies (pyte, pillow) are declared in `scripts/requirements.txt`;
install them into a scratch venv (`uv pip install -r scripts/requirements.txt`),
not the system.

## Make targets

`make help` lists everything. The ones expected in day-to-day work:

| target | what it does |
|---|---|
| `make build` | host binary with version stamping |
| `make demo` / `make run` | build, then launch |
| `make test` | all tests, `-race -shuffle=on` (same flags as CI) |
| `make cover` | coverage summary per package into `dist/` |
| `make check` | gofmt -s + staticcheck + vet |
| `make ci` | everything CI gates on: fmt, lint, vet, govulncheck, race tests |
| `make fmt` | rewrite files with gofmt -s |
| `make fix` | apply `go fix` modernization autofixes, then gofmt |
| `make lint` | staticcheck over both halves of the sqlite tag gate |
| `make vet-cross` | vet + staticcheck on every release platform (the pre-ship gate release.yml runs) |

## Before opening a PR

CI (`.github/workflows/ci.yml`) runs gofmt -s and `govulncheck ./...` on
Linux only (both are platform-independent), `staticcheck` and
`go vet ./...` and `go test -race -shuffle=on ./...` on Linux, macOS and
Windows, plus cross-compiles of linux/amd64, linux/arm64, darwin/amd64,
darwin/arm64 and windows/amd64. Each cross-compile job also runs
`go vet ./...` and staticcheck under its GOOS/GOARCH, so platform-specific
files get the same static analysis as the host build. Both halves of the
sqlite tag gate (`agentusage` with and without `-tags sqlite`) are vetted
and staticchecked everywhere. Everything except the three-OS matrix is one
command locally:

```
make ci
```

Keep platform-specific code behind build tags or runtime checks; the
cross-compile job catches code that only builds, or only vets and lints
cleanly, on the author's OS.

## Releases

Push a tag `v*`: GitHub Actions tests, cross-compiles every platform,
generates checksums and a CycloneDX SBOM, and attaches binaries to the
release. Locally, `make release VERSION=x.y.z` reproduces the same artifacts
in `dist/`. The checksums tarball is built deterministically: members are
sorted, timestamps come from `SOURCE_DATE_EPOCH` (defaulting to the commit
time), ownership is normalized, and gzip's name/mtime header is stripped, so
two builds of one source produce byte-identical archives. This needs GNU tar;
where the system tar is bsdtar (macOS), install GNU tar as `gtar`.
