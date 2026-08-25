BINARY  := toktop
CMD     := ./cmd/toktop
DIST    := dist
VERSION ?= dev

GO      ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)

# opencode keeps its sessions in SQLite rather than JSONL, so reading it means
# linking a database driver in for one agent. Released binaries carry it (it is
# pure Go, so cross-compilation is unaffected) and it still does nothing until
# --opencode-db asks for it. Build with TAGS= to leave the driver out entirely.
TAGS    ?= sqlite
GOTAGS  := $(if $(TAGS),-tags $(TAGS),)

PLATFORMS := \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64 \
	windows/amd64

.DEFAULT_GOAL := help

.PHONY: help
help: ## show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## build the toktop binary for this host
	$(GO) build $(GOTAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

.PHONY: run
run: build ## build, then run against local engines
	./$(BINARY)

.PHONY: demo
demo: build ## build, then run the simulated fleet
	./$(BINARY) --demo

.PHONY: test
test: ## run all tests with the race detector, shuffled order
	$(GO) test $(GOTAGS) -race -shuffle=on ./...

.PHONY: cover
cover: ## test coverage summary per package
	mkdir -p $(DIST)
	$(GO) test $(GOTAGS) -race -shuffle=on -coverprofile=$(DIST)/coverage.out ./... && \
		$(GO) tool cover -func=$(DIST)/coverage.out | tail -1

.PHONY: sbom
sbom: ## generate CycloneDX SBOM of all dependencies into dist/
	mkdir -p $(DIST)
	$(GO) run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0 \
		mod -licenses -std -json -output $(DIST)/toktop-sbom-$(VERSION).cdx.json .

.PHONY: vet
vet: ## run go vet
	$(GO) vet $(GOTAGS) ./...

.PHONY: fmt
fmt: ## rewrite all Go files with gofmt (including simplifications)
	gofmt -s -w .

.PHONY: fix
fix: ## apply go fix modernization autofixes, then gofmt
	$(GO) fix ./...
	gofmt -s -w .

.PHONY: check
check: ## verify gofmt -s formatting and vet (CI parity)
	@unformatted=$$(gofmt -s -l .); \
		if [ -n "$$unformatted" ]; then \
			echo "needs gofmt:"; echo "$$unformatted"; exit 1; \
		fi
	$(GO) vet $(GOTAGS) ./...

.PHONY: ci
ci: ## everything CI runs before merging: fmt, vet, govulncheck, race tests
	@$(MAKE) check
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
	$(GO) test $(GOTAGS) -race -shuffle=on ./...

.PHONY: clean
clean: ## remove build artifacts
	rm -rf $(DIST) $(BINARY) toktop-dev coverage.out

.PHONY: release
release: test-dist ## build every release platform into dist/ with checksums
	@cd $(DIST) && { command -v sha256sum >/dev/null 2>&1 && sha256sum $(BINARY)_* || shasum -a 256 $(BINARY)_*; } > checksums.txt && \
		tar czf toktop_$(VERSION)_checksums.tar.gz checksums.txt && rm checksums.txt

.PHONY: test-dist
test-dist: ## build every release platform without packaging
	@mkdir -p $(DIST)
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
