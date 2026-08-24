BINARY  := tokentop
CMD     := ./cmd/tokentop
DIST    := dist
VERSION ?= dev

GO      ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)

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
build: ## build the tokentop binary for this host
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

.PHONY: run
run: build ## build, then run against local engines
	./$(BINARY)

.PHONY: demo
demo: build ## build, then run the simulated fleet
	./$(BINARY) --demo

.PHONY: test
test: ## run all tests with the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## test coverage summary per package
	mkdir -p $(DIST)
	$(GO) test -race -coverprofile=$(DIST)/coverage.out ./... && \
		$(GO) tool cover -func=$(DIST)/coverage.out | tail -1

.PHONY: sbom
sbom: ## generate CycloneDX SBOM of all dependencies into dist/
	mkdir -p $(DIST)
	$(GO) run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0 \
		mod -licenses -std -json -output $(DIST)/tokentop-sbom-$(VERSION).cdx.json .

.PHONY: vet
vet: ## run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## rewrite all Go files with gofmt (including simplifications)
	gofmt -s -w .

.PHONY: check
check: ## verify gofmt -s formatting and vet (CI parity)
	@unformatted=$$(gofmt -s -l .); \
		if [ -n "$$unformatted" ]; then \
			echo "needs gofmt:"; echo "$$unformatted"; exit 1; \
		fi
	$(GO) vet ./...

.PHONY: clean
clean: ## remove build artifacts
	rm -rf $(DIST) $(BINARY) tokentop-dev coverage.out

.PHONY: release
release: test-dist ## build every release platform into dist/ with checksums
	@cd $(DIST) && sha256sum $(BINARY)_* > checksums.txt && \
		tar czf tokentop_$(VERSION)_checksums.tar.gz checksums.txt && rm checksums.txt

.PHONY: test-dist
test-dist: ## build every release platform without packaging
	@mkdir -p $(DIST)
	@for target in $(PLATFORMS); do \
		goos=$${target%/*}; goarch=$${target#*/}; ext=""; \
		[ "$$goos" = "windows" ] && ext=".exe"; \
		name="$(BINARY)_$(VERSION)_$${goos}_$${goarch}$${ext}"; \
		echo "building $$name"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
			$(GO) build -trimpath -ldflags "-s -w" -o $(DIST)/$$name $(CMD) || exit 1; \
	done

.PHONY: install
install: build ## install into ~/.local/bin
	install -m 0755 $(BINARY) ~/.local/bin/$(BINARY)
