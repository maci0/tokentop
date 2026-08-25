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

## Make targets

`make help` lists everything. The ones expected in day-to-day work:

| target | what it does |
|---|---|
| `make build` | host binary with version stamping |
| `make demo` / `make run` | build, then launch |
| `make test` | all tests, `-race -shuffle=on` (same flags as CI) |
| `make cover` | coverage summary per package into `dist/` |
| `make check` | gofmt -s + vet |
| `make ci` | everything CI gates on: fmt, vet, govulncheck, race tests |
| `make fmt` | rewrite files with gofmt -s |
| `make fix` | apply `go fix` modernization autofixes, then gofmt |

## Before opening a PR

CI (`.github/workflows/ci.yml`) runs gofmt -s and `govulncheck ./...` on
Linux only (both are platform-independent), `go vet ./...` and
`go test -race -shuffle=on ./...` on Linux, macOS and Windows, plus
cross-compiles of linux/amd64, linux/arm64, darwin/amd64,
darwin/arm64 and windows/amd64. Each cross-compile job also runs
`go vet ./...` under its GOOS/GOARCH, so platform-specific files get the
same static analysis as the host build. Everything except the three-OS
matrix is one command locally:

```
make ci
```

Keep platform-specific code behind build tags or runtime checks; the
cross-compile job catches code that only builds, or only vets cleanly, on
the author's OS.

## Releases

Push a tag `v*`: GitHub Actions tests, cross-compiles every platform,
generates checksums and a CycloneDX SBOM, and attaches binaries to the
release. Locally, `make release VERSION=x.y.z` reproduces the same artifacts
in `dist/`.
