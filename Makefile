.ONESHELL:
.SHELL      := $(shell which bash)
.SHELLFLAGS := -ec

MODULE          := github.com/serpro69/capy
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS         := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"
BUILD_TAGS      := -tags "fts5"
BENCH_BRANCH    := $(shell git rev-parse --abbrev-ref HEAD | tr '/' '-')
TOOLBOX_VERSION := latest # also accepts other refs like branch names ('master', 'feat/...'), or tags ('v1.2.3')

.PHONY: help build test test-race fmt vet clean bench bench-perf bench-quality bench-compare sync

help: ## Print this help message
	@grep -E "^[a-zA-Z_-]+:.*?## .*$$" $(MAKEFILE_LIST) |\
		sort |\
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

build: ## Build capy binary
	CGO_ENABLED=1 go build $(BUILD_TAGS) $(LDFLAGS) -o capy ./cmd/capy/

test: ## Run all tests
	CGO_ENABLED=1 go test $(BUILD_TAGS) ./...

test-race: ## Run tests with race detector
	CGO_ENABLED=1 go test -race $(BUILD_TAGS) ./...

fmt: ## Format .go files
	go fmt ./...

vet: ## Lint .go files
	go vet $(BUILD_TAGS) ./...

clean: ## Remove capy binary
	rm -f capy

bench: ## Run all benchmarks
bench: bench-perf bench-quality

bench-perf: ## Run performance benchmarks
	mkdir -p bench-results
	CGO_ENABLED=1 go test $(BUILD_TAGS) -bench=. -benchmem -count=6 \
	  ./internal/store/ ./internal/executor/ \
	  | tee bench-results/$(BENCH_BRANCH).txt

bench-quality: ## Run quality benchmarks
	mkdir -p bench-results
	rm -f $(CURDIR)/bench-results/$(BENCH_BRANCH).json
	CGO_ENABLED=1 CAPY_BENCH_RESULTS=$(CURDIR)/bench-results/$(BENCH_BRANCH).json \
	  go test $(BUILD_TAGS) -run='^TestBench' -v -p 1 ./internal/store/ ./internal/server/

bench-compare: ## Compare benchmarks in BASE and TARGET branches
ifndef BASE
	$(error BASE is required: make bench-compare BASE=<branch> TARGET=<branch>)
endif
ifndef TARGET
	$(error TARGET is required: make bench-compare BASE=<branch> TARGET=<branch>)
endif
	@echo "=== Performance (benchstat) ==="
	@if command -v benchstat >/dev/null 2>&1; then \
	  benchstat bench-results/$(BASE).txt bench-results/$(TARGET).txt; \
	else \
	  echo "benchstat not found — install with: go install golang.org/x/perf/cmd/benchstat@latest"; \
	  echo "Skipping performance comparison."; \
	fi
	@echo ""
	@echo "=== Retrieval Quality (qualstat) ==="
	go run $(BUILD_TAGS) ./cmd/qualstat bench-results/$(BASE).json bench-results/$(TARGET).json

sync: ## Sync serpro69/claude-toolbox template files against TOOLBOX_VERSION
	@.claude/toolbox/scripts/template-sync.sh --local --version $(TOOLBOX_VERSION)
