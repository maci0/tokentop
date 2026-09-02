BINARY  := toktop
CMD     := ./cmd/toktop
DIST    := dist
VERSION ?= dev
# VERSION is interpolated into -ldflags and dist filenames. Refuse values
# that would break the shell, the linker flag, or the artifact name.
CHECK_VERSION = printf '%s' '$(VERSION)' | grep -qE '^[A-Za-z0-9._+-]+$$' || { echo "make: VERSION must match [A-Za-z0-9._+-]+ (got '$(VERSION)')" >&2; exit 1; }

GO          ?= go
# go.mod's go line is the compiler pin. GOTOOLCHAIN=auto would keep a newer
# host toolchain (and its GOEXPERIMENT defaults), so two machines would emit
# different binaries from the same source.
GO_VERSION  := $(shell awk '/^go / { print $$2; exit }' go.mod)
export GOTOOLCHAIN := go$(GO_VERSION)
# Instruction-set baselines: an ambient GOAMD64=v3 would change amd64 artifacts.
export GOAMD64 := v1
export GOARM64 := v8.0
# Race tests turn cgo on in their recipes. Everything else matches the
# released artifacts, including vet/staticcheck so they analyze the same
# net resolver the binaries ship.
export CGO_ENABLED := 0
# Strip paths, omit git stamps (checkout vs tarball would disagree), honor
# go.sum, produce a PIE.
GO_BUILDFLAGS := -trimpath -buildvcs=false -mod=readonly -buildmode=pie
STATICCHECK := $(GO) tool staticcheck
LDFLAGS     := -s -w -X main.version=$(VERSION)
# gofmt from the selected toolchain, not a different major on PATH.
GOFMT = $$($(GO) env GOROOT)/bin/gofmt

# crush and opencode keep sessions in SQLite rather than JSONL, so reading
# them means linking a database driver. Released binaries carry it (pure Go,
# so cross-compilation is unaffected). opencode still does nothing until
# --opencode-db asks for it; crush is read whenever the tag is on, because
# its database lives inside the watched project. Build with TAGS= to leave
# the driver out entirely.
TAGS    ?= sqlite
GOTAGS  := $(if $(TAGS),-tags $(TAGS),)

# Pin locale and timezone for every recipe: glob expansion order and formatted
# dates must not follow the invoking shell's environment into artifacts
# (reproducible-builds.org practice).
export LC_ALL := C
export TZ := UTC

# One timestamp for archive metadata: SOURCE_DATE_EPOCH when the caller sets
# it, otherwise this commit's time, so two builds of the same source agree.
# Exported so gzip/tar/any honoring tool sees the same value, not only Make.
export SOURCE_DATE_EPOCH ?= $(shell git log -1 --pretty=%ct 2>/dev/null || echo 0)

# Normalizing tar metadata (--sort/--mtime/--owner) needs GNU tar; bsdtar
# cannot express it. Where the system tar is bsdtar (macOS) GNU tar is often
# installed as gtar, so prefer that name when it exists.
TAR       := $(shell command -v gtar >/dev/null 2>&1 && echo gtar || echo tar)
TAR_REPRO := --sort=name --mtime="@$(SOURCE_DATE_EPOCH)" --owner=0 --group=0 --numeric-owner

PLATFORMS := \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64 \
	windows/amd64 windows/arm64

.DEFAULT_GOAL := help

.PHONY: help
help: ## show available targets
	@if [ -t 1 ] && [ -z "$${NO_COLOR}" ] && [ "$${TERM:-}" != "dumb" ]; then \
		color=1; \
	else \
		color=; \
	fi; \
	grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk -v color="$$color" 'BEGIN {FS = ":.*## "} { if (color) printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2; else printf "  %-14s %s\n", $$1, $$2 }'

# CGO stays off so the host build matches the released artifacts exactly;
# with cgo available the net package links host-specific resolver code and
# two developer machines produce different binaries from identical source.
.PHONY: build
build: ## build the toktop binary for this host
	@$(CHECK_VERSION)
	CGO_ENABLED=0 $(GO) build $(GOTAGS) $(GO_BUILDFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

.PHONY: run
run: build ## build, then run against local engines
	./$(BINARY)

.PHONY: demo
demo: build ## build, then run the simulated fleet
	./$(BINARY) --demo

# -race links runtime/cgo. Honor CC when set; otherwise gcc, then clang
# (Go's search order). make build keeps CGO off; only race tests need this.
NEED_CC = cc="$${CC:-}"; \
	if [ -z "$$cc" ]; then \
		if command -v gcc >/dev/null 2>&1; then cc=gcc; \
		elif command -v clang >/dev/null 2>&1; then cc=clang; \
		fi; \
	fi; \
	if [ -z "$$cc" ] || ! command -v $$cc >/dev/null 2>&1; then \
		echo "make: go test -race needs a C compiler (gcc or clang) on PATH" >&2; \
		echo "  CGO stays off for make build; only the race tests need it." >&2; \
		exit 1; \
	fi

.PHONY: test
test: ## run all tests with the race detector, shuffled order (both halves of the sqlite tag gate)
	@$(NEED_CC)
	CGO_ENABLED=1 $(GO) test -mod=readonly -race -shuffle=on ./...
	CGO_ENABLED=1 $(GO) test -mod=readonly -tags sqlite -race -shuffle=on ./agentusage/...

# Same flags and toolchain as `make test`. PKG is required; RUN and TESTTAGS
# are optional (TESTTAGS=sqlite is the other half of the agentusage gate).
.PHONY: test-pkg
test-pkg: ## one package/test: PKG=./internal/ui [RUN=TestName] [TESTTAGS=sqlite]
	@if [ -z "$(PKG)" ]; then \
		echo "make test-pkg: set PKG (e.g. PKG=./internal/ui)" >&2; \
		echo "  optional: RUN=TestName  TESTTAGS=sqlite" >&2; \
		exit 1; \
	fi
	@$(NEED_CC)
	CGO_ENABLED=1 $(GO) test -mod=readonly $(if $(TESTTAGS),-tags $(TESTTAGS) )-race -shuffle=on $(if $(RUN),-run "$(RUN)" )"$(PKG)"

.PHONY: cover
cover: ## test coverage summary per package into dist/
	@$(NEED_CC)
	mkdir -p $(DIST)
	CGO_ENABLED=1 $(GO) test -mod=readonly $(GOTAGS) -race -shuffle=on -coverprofile=$(DIST)/coverage.out ./...
	$(GO) tool cover -func=$(DIST)/coverage.out | tail -1

.PHONY: sbom
sbom: ## generate CycloneDX SBOM of all dependencies into dist/
	@$(CHECK_VERSION)
	mkdir -p $(DIST)
	$(GO) run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0 \
		mod -licenses -std -json -output $(DIST)/toktop-sbom-$(VERSION).cdx.json .

.PHONY: vet
vet: ## run go vet (both halves of the sqlite tag gate)
	$(GO) vet -mod=readonly ./...
	$(GO) vet -mod=readonly -tags sqlite ./agentusage/...

# Same per-platform gate release.yml runs before shipping; PLATFORMS is the
# single source of truth so CI and local checks cannot list different targets.
# staticcheck is built for the host once: installing with GOOS set would
# produce a tool binary for the analyzed platform instead of the analyzer.
.PHONY: vet-cross
vet-cross: ## vet + staticcheck every release platform from PLATFORMS
	@mkdir -p $(DIST)/bin
	@env GOBIN=$(CURDIR)/$(DIST)/bin $(GO) install tool || exit 1
	@for target in $(PLATFORMS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		echo "checking $$goos/$$goarch"; \
		env GOOS=$$goos GOARCH=$$goarch $(GO) vet -mod=readonly ./... || exit 1; \
		env GOOS=$$goos GOARCH=$$goarch $(GO) vet -mod=readonly -tags sqlite ./agentusage/... || exit 1; \
		env GOOS=$$goos GOARCH=$$goarch $(DIST)/bin/staticcheck ./... || exit 1; \
		env GOOS=$$goos GOARCH=$$goarch $(DIST)/bin/staticcheck -tags sqlite ./agentusage/... || exit 1; \
	done
	@rm -rf $(DIST)/bin

.PHONY: lint
lint: ## run staticcheck (both halves of the sqlite tag gate)
	$(STATICCHECK) ./...
	$(STATICCHECK) -tags sqlite ./agentusage/...

.PHONY: site-check
site-check: ## bun test the Cloudflare Worker in site/ (CI parity)
	@command -v bun >/dev/null 2>&1 || { \
		echo "make site-check: bun is not on PATH (see .bun-version)" >&2; \
		exit 1; \
	}
	bun test site/

.PHONY: fmt
fmt: ## rewrite all Go files with gofmt (including simplifications)
	$(GOFMT) -s -w .

.PHONY: fix
fix: ## apply go fix modernization autofixes, then gofmt
	$(GO) fix ./...
	GOOS=darwin $(GO) fix ./...
	GOOS=windows $(GO) fix ./...
	$(GOFMT) -s -w .

.PHONY: tidy-check
tidy-check: ## fail if go.mod or go.sum would change
	$(GO) mod tidy -diff

.PHONY: scripts-check
scripts-check: ## black, ruff and mypy over scripts/ (same pins as CI)
	@command -v uv >/dev/null 2>&1 || { \
		echo "make scripts-check: uv is not on PATH (pins are scripts/requirements-dev.txt)" >&2; \
		exit 1; \
	}
	uv run --isolated --no-project --with-requirements scripts/requirements-dev.txt black --check scripts/
	uv run --isolated --no-project --with-requirements scripts/requirements-dev.txt ruff check scripts/
	uv run --isolated --no-project --with-requirements scripts/requirements-dev.txt mypy scripts/

.PHONY: check
check: ## verify go.mod, gofmt -s formatting, vet and staticcheck (CI parity)
	@unformatted=$$($(GOFMT) -s -l .); \
		if [ -n "$$unformatted" ]; then \
			echo "needs gofmt:" >&2; echo "$$unformatted" >&2; exit 1; \
		fi
	@$(MAKE) tidy-check
	@$(MAKE) lint
	@$(MAKE) vet

.PHONY: ci
ci: ## Go merge gates: tidy-diff, fmt, lint, vet, govulncheck, race tests
	@$(MAKE) check
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
	@$(MAKE) test

.PHONY: pr
pr: ## every PR merge gate except the OS matrix: ci + site-check + scripts-check
	@$(MAKE) ci
	@$(MAKE) site-check
	@$(MAKE) scripts-check

.PHONY: clean
clean: ## remove build artifacts
	rm -rf $(DIST) $(BINARY) coverage.out

.PHONY: release
release: checksums ## build every release platform into dist/ with reproducible checksums

.PHONY: checksums
checksums: test-dist ## checksum the dist/ binaries into a byte-reproducible tarball
	@$(TAR) --sort=name --version >/dev/null 2>&1 || \
		{ echo "$(TAR) rejects --sort: deterministic packaging needs GNU tar (install it as gtar)" >&2; exit 1; }
	@cd $(DIST) && \
		files=$$(printf '%s\n' $(BINARY)_* | sort) && \
		if command -v sha256sum >/dev/null 2>&1; then \
			sha256sum $$files > checksums.txt && sha256sum -c checksums.txt; \
		else \
			shasum -a 256 $$files > checksums.txt && shasum -a 256 -c checksums.txt; \
		fi
	@cd $(DIST) && $(TAR) $(TAR_REPRO) -c checksums.txt | gzip -n > toktop_$(VERSION)_checksums.tar.gz && rm checksums.txt

# Binaries of any earlier version are dropped first: leftovers would
# otherwise ride the toktop_* glob into checksums.txt and the release.
.PHONY: test-dist
test-dist: ## build every release platform without packaging
	@$(CHECK_VERSION)
	@mkdir -p $(DIST)
	@rm -f $(DIST)/$(BINARY)_*
	@for target in $(PLATFORMS); do \
		goos=$${target%/*}; goarch=$${target#*/}; ext=""; \
		if [ "$$goos" = "windows" ]; then ext=".exe"; fi; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}$${ext}"; \
		echo "building $$name"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
			$(GO) build $(GOTAGS) $(GO_BUILDFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$$name $(CMD) || exit 1; \
	done

# XDG user bin on Linux; override on macOS so the binary lands on PATH
# (PREFIX=/usr/local or PREFIX=$(brew --prefix)).
PREFIX ?= $(HOME)/.local

.PHONY: install
install: build ## install into PREFIX/bin (default ~/.local/bin)
	mkdir -p "$(PREFIX)/bin"
	install -m 0755 $(BINARY) "$(PREFIX)/bin/$(BINARY)"
