MODULE := github.com/serpro69/capy
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X $(MODULE)/internal/version.Version=$(VERSION)"
BUILD_TAGS := -tags "fts5"

.PHONY: build test test-race vet clean bench bench-perf bench-quality compare

build:
	CGO_ENABLED=1 go build $(BUILD_TAGS) $(LDFLAGS) -o capy ./cmd/capy/

test:
	CGO_ENABLED=1 go test $(BUILD_TAGS) ./...

test-race:
	CGO_ENABLED=1 go test -race $(BUILD_TAGS) ./...

vet:
	go vet $(BUILD_TAGS) ./...

clean:
	rm -f capy

bench: bench-perf bench-quality

BENCH_BRANCH := $(shell git rev-parse --abbrev-ref HEAD | tr '/' '-')

bench-perf:
	mkdir -p bench-results
	CGO_ENABLED=1 go test $(BUILD_TAGS) -bench=. -benchmem -count=6 \
	  ./internal/store/ ./internal/executor/ \
	  | tee bench-results/$(BENCH_BRANCH).txt

bench-quality:
	mkdir -p bench-results
	rm -f $(CURDIR)/bench-results/$(BENCH_BRANCH).json
	CGO_ENABLED=1 CAPY_BENCH_RESULTS=$(CURDIR)/bench-results/$(BENCH_BRANCH).json \
	  go test $(BUILD_TAGS) -run='^TestBench' -v -p 1 ./internal/store/ ./internal/server/

compare:
ifndef BASE
	$(error BASE is required: make compare BASE=<branch> TARGET=<branch>)
endif
ifndef TARGET
	$(error TARGET is required: make compare BASE=<branch> TARGET=<branch>)
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
