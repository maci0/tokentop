BINARY  := toktop
CMD     := ./cmd/toktop
DIST    := dist
VERSION ?= dev

GO          ?= go
STATICCHECK := $(GO) run honnef.co/go/tools/cmd/staticcheck@2026.2.1
LDFLAGS     := -s -w -X main.version=$(VERSION)

# opencode keeps its sessions in SQLite rather than JSONL, so reading it means
# linking a database driver in for one agent. Released binaries carry it (it is
# pure Go, so cross-compilation is unaffected) and it still does nothing until
# --opencode-db asks for it. Build with TAGS= to leave the driver out entirely.
TAGS    ?= sqlite
GOTAGS  := $(if $(TAGS),-tags $(TAGS),)

# Pin locale and timezone for every recipe: glob expansion order and formatted
# dates must not follow the invoking shell's environment into artifacts
# (reproducible-builds.org practice).
export LC_ALL := C
export TZ := UTC

# One timestamp for archive metadata: SOURCE_DATE_EPOCH when the caller sets
# it, otherwise this commit's time, so two builds of the same source agree.
SOURCE_DATE_EPOCH ?= $(shell git log -1 --pretty=%ct 2>/dev/null || echo 0)

# Normalizing tar metadata (--sort/--mtime/--owner) needs GNU tar; bsdtar
# cannot express it. Where the system tar is bsdtar (macOS) GNU tar is often
# installed as gtar, so prefer that name when it exists.
TAR       := $(shell command -v gtar >/dev/null 2>&1 && echo gtar || echo tar)
TAR_REPRO := --sort=name --mtime="@$(SOURCE_DATE_EPOCH)" --owner=0 --group=0 --numeric-owner

PLATFORMS := \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64 \
	windows/amd64

.DEFAULT_GOAL := help

.PHONY: help
help: ## show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

# CGO stays off so the host build matches the released artifacts exactly;
# with cgo available the net package links host-specific resolver code and
# two developer machines produce different binaries from identical source.
.PHONY: build
build: ## build the toktop binary for this host
	CGO_ENABLED=0 $(GO) build $(GOTAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

.PHONY: run
run: build ## build, then run against local engines
	./$(BINARY)

.PHONY: demo
demo: build ## build, then run the simulated fleet
	./$(BINARY) --demo

.PHONY: test
test: ## run all tests with the race detector, shuffled order (both halves of the sqlite tag gate)
	$(GO) test -race -shuffle=on ./...
	$(GO) test -tags sqlite -race -shuffle=on ./agentusage/...

.PHONY: cover
cover: ## test coverage summary per package into dist/
	mkdir -p $(DIST)
	$(GO) test $(GOTAGS) -race -shuffle=on -coverprofile=$(DIST)/coverage.out ./...
	$(GO) tool cover -func=$(DIST)/coverage.out | tail -1

.PHONY: sbom
sbom: ## generate CycloneDX SBOM of all dependencies into dist/
	mkdir -p $(DIST)
	$(GO) run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0 \
		mod -licenses -std -json -output $(DIST)/toktop-sbom-$(VERSION).cdx.json .

.PHONY: vet
vet: ## run go vet (both halves of the sqlite tag gate)
	$(GO) vet ./...
	$(GO) vet -tags sqlite ./agentusage/...

# Same per-platform gate release.yml runs before shipping; PLATFORMS is the
# single source of truth so CI and local checks cannot list different targets.
# staticcheck is built for the host once: `go run` would inherit GOOS and
# produce a tool binary for the analyzed platform instead of the analyzer.
.PHONY: vet-cross
vet-cross: ## vet + staticcheck every release platform from PLATFORMS
	@mkdir -p $(DIST)/bin
	@env GOBIN=$(CURDIR)/$(DIST)/bin $(GO) install honnef.co/go/tools/cmd/staticcheck@2026.2.1 || exit 1
	@for target in $(PLATFORMS); do \
		goos=$${target%/*}; goarch=$${target#*/}; \
		echo "checking $$goos/$$goarch"; \
		env GOOS=$$goos GOARCH=$$goarch $(GO) vet ./... || exit 1; \
		env GOOS=$$goos GOARCH=$$goarch $(GO) vet -tags sqlite ./agentusage/... || exit 1; \
		env GOOS=$$goos GOARCH=$$goarch $(DIST)/bin/staticcheck ./... || exit 1; \
		env GOOS=$$goos GOARCH=$$goarch $(DIST)/bin/staticcheck -tags sqlite ./agentusage/... || exit 1; \
	done
	@rm -rf $(DIST)/bin

.PHONY: lint
lint: ## run staticcheck (both halves of the sqlite tag gate)
	$(STATICCHECK) ./...
	$(STATICCHECK) -tags sqlite ./agentusage/...

.PHONY: fmt
fmt: ## rewrite all Go files with gofmt (including simplifications)
	gofmt -s -w .

.PHONY: fix
fix: ## apply go fix modernization autofixes, then gofmt
	$(GO) fix ./...
	gofmt -s -w .

.PHONY: check
check: ## verify gofmt -s formatting, vet and staticcheck (CI parity)
	@unformatted=$$(gofmt -s -l .); \
		if [ -n "$$unformatted" ]; then \
			echo "needs gofmt:"; echo "$$unformatted"; exit 1; \
		fi
	@$(MAKE) lint
	@$(MAKE) vet

.PHONY: ci
ci: ## everything CI runs before merging: fmt, lint, vet, govulncheck, race tests
	@$(MAKE) check
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
	@$(MAKE) test

.PHONY: clean
clean: ## remove build artifacts
	rm -rf $(DIST) $(BINARY) coverage.out

.PHONY: release
release: checksums ## build every release platform into dist/ with reproducible checksums

.PHONY: checksums
checksums: test-dist ## checksum the dist/ binaries into a byte-reproducible tarball
	@$(TAR) --sort=name --version >/dev/null 2>&1 || \
		{ echo "$(TAR) rejects --sort: deterministic packaging needs GNU tar (install it as gtar)"; exit 1; }
	@cd $(DIST) && { command -v sha256sum >/dev/null 2>&1 && sha256sum $(BINARY)_* || shasum -a 256 $(BINARY)_*; } > checksums.txt
	@cd $(DIST) && { command -v sha256sum >/dev/null 2>&1 && sha256sum -c checksums.txt || shasum -a 256 -c checksums.txt; }
	@cd $(DIST) && $(TAR) $(TAR_REPRO) -c checksums.txt | gzip -n > toktop_$(VERSION)_checksums.tar.gz && rm checksums.txt

# Binaries of any earlier version are dropped first: leftovers would
# otherwise ride the toktop_* glob into checksums.txt and the release.
.PHONY: test-dist
test-dist: ## build every release platform without packaging
	@mkdir -p $(DIST)
	@rm -f $(DIST)/$(BINARY)_*
	@for target in $(PLATFORMS); do \
		goos=$${target%/*}; goarch=$${target#*/}; ext=""; \
		[ "$$goos" = "windows" ] && ext=".exe"; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}$${ext}"; \
		echo "building $$name"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
			$(GO) build $(GOTAGS) -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(DIST)/$$name $(CMD) || exit 1; \
	done

.PHONY: install
install: build ## install into ~/.local/bin
	mkdir -p ~/.local/bin
	install -m 0755 $(BINARY) ~/.local/bin/$(BINARY)
