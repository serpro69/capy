# Implementation Plan: Benchmark Suite

> Design: [./design.md](./design.md)
> Status: pending
> Created: 2026-05-17

## Overview

Implementation is split into 7 tasks, ordered by dependency. Each task produces a self-contained, testable artifact. The first 3 tasks build infrastructure (fixture format, test helpers, qualstat). Tasks 4-6 implement the three benchmark tracks. Task 7 is final verification.

## Prerequisites

- Familiarity with Go's `testing` package (`testing.B`, `testing.T`, sub-benchmarks)
- Understanding of capy's `internal/store` package — specifically `ContentStore`, `SearchWithFallback`, `Index`, `SearchResult`, `MatchLayer`
- Understanding of capy's `internal/executor` package — `PolyglotExecutor`, `ExecRequest`, `ExecResult`
- Understanding of capy's `internal/server` package — intent-search logic at the 5000-byte boundary

## Task 1: Fixture Types and Test Helpers {#fixture-helpers}

Define the Go types that deserialize the JSONL fixture format and shared helpers for all benchmark files.

### Location

`internal/store/benchutil_test.go`

### What to Build

**Fixture types** matching the JSONL schema from [design.md#dataset-format](./design.md#dataset-format):

- `BenchEntry` — top-level: `ID`, `ContentType`, `Haystack`, `SourceLabel`, `SourceKind`, `Cases []BenchCase`
- `BenchCase` — per-query: `CaseID`, `Query`, `Needles []string`, `ExpectedLayer`, `ExpectedRankCeiling int`

**Helpers:**

- `loadFixtures(t testing.TB, contentType string) []BenchEntry` — reads `testdata/bench/{contentType}.jsonl`, unmarshals each line, returns slice. Uses `runtime.Caller(0)` to resolve path relative to the package directory.
- `hashFixtureFile(t testing.TB, contentType string) string` — returns hex-encoded SHA-256 of the fixture file. Embedded in quality reports for dataset version validation.
- `newBenchStore(t testing.TB) *ContentStore` — creates an in-memory SQLite store for benchmarks, using the same pattern as the existing `newTestStore` in `store_test.go`. Registers cleanup via `t.Cleanup`.
- `seedStore(t testing.TB, store *ContentStore, entries []BenchEntry)` — indexes all haystacks from the entries into the store.

### Verify

`go test -run='^TestBenchFixtureLoad$' -v ./internal/store/` — a small test that loads each fixture file and validates it deserializes without error. Will initially fail until Task 2 provides fixture data.

## Task 2: Seed Fixture Datasets {#seed-fixtures}

Create initial JSONL fixture files under `internal/store/testdata/bench/`.

### Location

`internal/store/testdata/bench/`

### What to Build

Five JSONL files, one per content type: `markdown.jsonl`, `json.jsonl`, `plaintext.jsonl`, `transcript.jsonl`, `curated.jsonl`.

Each file needs a minimum of 10-20 entries (haystacks) with 3-5 cases per entry to provide enough corpus density for meaningful BM25 IDF calculations. Cases should cover:

- **Exact match** queries — terms present verbatim in the haystack (expected tier: `porter` or `rrf(porter+trigram)`)
- **Partial/substring** queries — partial terms that only trigram can match (expected tier: `trigram`)
- **Typo/misspelling** queries — deliberate misspellings triggering fuzzy correction (expected tier: `fuzzy+porter` or `fuzzy+trigram`)
- **Negative** queries — completely unrelated queries that should return nothing (expected tier: `none`)
- **Structural needle** queries — needles array includes parent context (e.g., JSON parent key, markdown heading) to test chunker context preservation

Content should be realistic but synthetic — representative of actual tool output, documentation, API responses, logs, and curated knowledge entries.

### Verify

`go test -run='^TestBenchFixtureLoad$' -v ./internal/store/` passes. Each file deserializes cleanly. Manually inspect that haystacks are realistic and needle strings actually appear in their respective haystacks.

## Task 3: `qualstat` CLI {#qualstat}

Build the quality report comparison tool.

### Location

`cmd/qualstat/main.go`

### What to Build

A single-file Go CLI (stdlib only: `encoding/json`, `fmt`, `os`, `math`, `flag`).

**Report types** — define structs matching the JSON report schema from [design.md#qualstat-cli-design](./design.md#qualstat-cli-design):
- `Report` — top-level with `Metadata`, `ByContentType map[string]Metrics`, `Overall Metrics`, `Failures []Failure`
- `Metadata` — `Timestamp`, `GitSHA`, `GitBranch`, `DatasetHash`, `GoVersion`
- `Metrics` — all quality and context reduction metrics
- `Failure` — `CaseID`, `Type`, `Expected`, `Actual`, `Detail`

**Core logic:**
- Parse 1 or 2 JSON file arguments
- Single-file mode: print absolute metrics table
- Two-file mode: validate dataset hash match (abort on mismatch), compute deltas, print comparison table with regression markers
- Warning thresholds per metric category: strict (0.0) for Context Recall and Routing Accuracy; configurable (default -0.02) for R@K, MRR, NDCG, Compression
- Float comparison with epsilon (0.0001)
- Failures diff: show NEW and RESOLVED only, cap at 10 entries
- Exit code: 0 clean, 1 regressions
- No color default, `--color` flag for terminal use

### Verify

`go build ./cmd/qualstat/` compiles. Create two small hand-crafted JSON report files in `cmd/qualstat/testdata/`, run `go test ./cmd/qualstat/` with table-driven tests covering: single-file mode, comparison with no regressions, comparison with regressions, dataset hash mismatch, failure diff output.

## Task 4: Retrieval Quality Benchmarks {#retrieval-quality}

Implement the Track B quality benchmarks.

### Location

`internal/store/bench_test.go`

### What to Build

`TestBenchRetrievalQuality(t *testing.T)` — the main quality benchmark function.

**Skip guard:** skip when `CAPY_BENCH_RESULTS` env var is unset.

**Flow:**
1. For each content type (`markdown`, `json`, `plaintext`, `transcript`, `curated`):
   a. Load fixtures via `loadFixtures`
   b. Create a single store via `newBenchStore`
   c. Seed all haystacks via `seedStore`
   d. For each case in each entry, call `SearchWithFallback` with `query`, collect `[]SearchResult`
   e. Compute per-case metrics:
      - **R@K** (K=1,3,5,10): does any result in top-K contain ALL needles?
      - **NDCG@10**: any result containing a needle substring is "relevant"
      - **MRR**: 1/rank of first result containing all needles
      - **Routing Accuracy**: `result.MatchLayer == case.ExpectedLayer`
      - **Rank Ceiling**: actual rank of needle-containing result <= `ExpectedRankCeiling`
   f. For negative cases (empty needles): verify zero results returned
2. Aggregate metrics per content type and overall
3. Collect individual case failures with actionable detail
4. Build the `Report` struct, populate `Metadata` (git SHA via `exec.Command("git", ...)`, dataset hash, timestamp, Go version)
5. Write JSON to `CAPY_BENCH_RESULTS` path

**Metric computation helpers** — implement as unexported functions in the same file:
- `computeRecallAtK(results []SearchResult, needles []string, k int) float64`
- `computeNDCG(results []SearchResult, needles []string, k int) float64`
- `computeMRR(results []SearchResult, needles []string) float64`
- `containsAllNeedles(text string, needles []string) bool`

### Verify

```
mkdir -p bench-results
CAPY_BENCH_RESULTS=bench-results/test.json go test -run='^TestBenchRetrievalQuality$' -v ./internal/store/
```
Produces a valid JSON report. `go run ./cmd/qualstat bench-results/test.json` prints a readable table.

## Task 5: Context Reduction Benchmarks (NIAH) {#context-reduction}

Implement the Track A context reduction benchmarks.

### Location

`internal/store/bench_context_test.go`

### What to Build

`TestBenchContextReduction(t *testing.T)` — NIAH benchmark function.

**Skip guard:** same `CAPY_BENCH_RESULTS` pattern.

**Flow:**
1. For each content type, load fixtures, create and seed a single store (same as Task 4)
2. For each case, call `SearchWithFallback` and compute:
   - **Compression Ratio** = `1 - (total_bytes_of_returned_snippets / len(haystack))`
   - **Context Recall** = `1` if all needles found across returned snippet texts, `0` otherwise
   - **Effective Compression** = `Compression Ratio × Context Recall`
3. For negative cases: Compression Ratio = 1.0, Context Recall = 1.0 (correctly returning nothing is perfect compression with perfect recall)
4. Aggregate per content type and overall
5. Merge into the same `Report` struct as Task 4 (or append to existing report if `CAPY_BENCH_RESULTS` file already exists)

**Design note:** This shares the store setup with Task 4. Consider whether both `TestBenchRetrievalQuality` and `TestBenchContextReduction` should run as subtests of a single `TestBench` function to avoid double-indexing. The trade-off is simplicity (separate functions, independent runs) vs efficiency (shared store). If dataset is small, separate functions are fine. If indexing becomes expensive, refactor into a shared setup with `t.Run` subtests.

### Verify

Same as Task 4 — JSON report now includes context reduction metrics alongside retrieval quality. `qualstat` displays both sections.

## Task 6: Performance and Executor Benchmarks {#performance}

Implement the `testing.B` benchmarks for store performance, executor overhead, and the 5000-byte threshold.

### Location

- `internal/store/bench_perf_test.go` — store performance
- `internal/executor/bench_test.go` — executor overhead
- `internal/server/bench_integration_test.go` — 5000-byte threshold

### What to Build

**Store performance** (`bench_perf_test.go`):

- `BenchmarkIndex/{content_type}` — load fixtures, `b.ResetTimer()`, index one haystack per iteration
- `BenchmarkSearch/{content_type}/{corpus_size}` — pre-seed store with N entries from fixtures (use `b.StopTimer`/`b.StartTimer` around setup), search one query per iteration
- `BenchmarkSearchByTier/{tier}` — pre-seed store, use queries known to hit specific tiers, group sub-benchmarks by expected MatchLayer

**Executor overhead** (`bench_test.go`):

- `BenchmarkExecutorOverhead/{language}` — for shell/JS/python, run simple command through `PolyglotExecutor` vs direct `exec.Command`. Skip if runtime unavailable (`exec.LookPath`)
- `BenchmarkExecutorOverheadParallel/{language}` — same using `b.RunParallel()` to expose lock contention
- `BenchmarkExecutorScaling/{output_size}` — commands producing 1KB/10KB/100KB/1MB output through executor
- `BenchmarkSafeEnv` — `BuildSafeEnv()` in a loop
- `BenchmarkProcessGroupKill` — measure cleanup latency of Setpgid + SIGKILL

**5000-byte threshold** (`bench_integration_test.go`):

- `TestBench5000ByteThreshold(t *testing.T)` — a `TestBench*` function (quality, not `testing.B`) with the same `CAPY_BENCH_RESULTS` skip guard
- Execute commands producing output at 4999 and 5001 bytes
- Measure wall-clock time for the intent-search indexing path
- Record the latency delta and effective compression at the boundary
- Append results to the quality JSON report

### Verify

Performance: `go test -bench=. -benchmem -count=3 ./internal/store/ ./internal/executor/` produces `benchstat`-compatible output.

5000-byte threshold: appears in the quality JSON report when run via `make bench-quality`.

## Task 7: Makefile and Final Verification {#verification}

Wire up orchestration and verify the full pipeline end-to-end.

### Location

`Makefile` (add targets), `.gitignore` (add `bench-results/`)

### What to Build

**Makefile targets** as specified in [design.md#orchestration](./design.md#orchestration):
- `bench` — runs both `bench-perf` and `bench-quality`
- `bench-perf` — `mkdir -p bench-results`, `go test -bench=. -benchmem -count=6 ... | tee bench-results/{branch}.txt`
- `bench-quality` — `mkdir -p bench-results`, `CAPY_BENCH_RESULTS=... go test -run='^TestBench' -v ...`
- `compare` — `benchstat` + `qualstat` side by side, takes `BASE` and `TARGET` args

**`.gitignore`** — add `bench-results/`

### Verify

Full end-to-end:
1. `make bench` — completes without error, produces `.txt` and `.json` in `bench-results/`
2. `make compare BASE=main TARGET=main` — produces comparison table with zero deltas
3. `go test ./...` — regular test suite still passes, quality benchmarks are skipped (no `CAPY_BENCH_RESULTS`)
4. Run `review-code` skill with Go input to review the implementation
5. Run `review-spec` skill to verify implementation matches this design doc
6. Run `test` skill to verify the full test suite
7. Run `document` skill to update relevant docs
