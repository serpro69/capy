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
- `hashFixtureManifest(t testing.TB) string` — computes a single SHA-256 over all fixture files concatenated in sorted filename order. Returns hex-encoded hash. This manifest hash is embedded in quality reports; `qualstat` aborts if comparing reports with different manifest hashes.
- `newBenchStore(t testing.TB) *ContentStore` — delegates directly to `newTestStore(t)` (defined in `store_test.go:17`), which already handles the encryption key setup via `t.Setenv(encryptionKeyEnv, testEncryptionKey)` and cleanup via `t.Cleanup`. Do not reimplement — just wrap or alias.
- `seedStore(t testing.TB, store *ContentStore, entries []BenchEntry)` — indexes all haystacks from the entries into the store. Dispatches to the correct `Index*` method based on `BenchEntry.ContentType`:

  | `content_type` | Method | Rationale |
  |---|---|---|
  | `markdown` | `Index(content, label, "", kind)` | Auto-detects markdown via `DetectContentType` |
  | `json` | `IndexJSON(content, label, kind)` | Uses JSON-specific recursive key-path chunker |
  | `plaintext` | `IndexPlainText(content, label, kind)` | Uses plaintext paragraph-split chunker |
  | `transcript` | `IndexChunked(content, label, "", kind, chunks)` | Pre-chunk by session boundary markers in the fixture; or use `Index` with auto-detect if fixtures don't need pre-chunking |
  | `curated` | `Index(content, label, "", kind)` | Auto-detect (curated content is typically markdown) |
- `benchSearchOpts() SearchOptions` — returns `SearchOptions{IncludeKinds: []SourceKind{KindDurable, KindEphemeral, KindSession}}`. All quality benchmarks must use this to ensure ephemeral and session fixtures are visible to search (default empty `IncludeKinds` excludes ephemeral).

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
   d. For each case in each entry, call `SearchWithFallback` with `query` and `benchSearchOpts()`, collect `[]SearchResult`
   e. Compute per-case metrics:
      - **R@K** (K=1,3,5,10): is at least one relevant result in top-K? A result is relevant if it contains ANY needle substring. This is standard IR recall-at-K — do not require all needles in a single result.
      - **NDCG@10**: any result containing any needle substring is "relevant"
      - **MRR**: 1/rank of first relevant result (first result containing any needle)
      - **Match-Layer Accuracy**: `result.MatchLayer == case.ExpectedLayer` (see MatchLayer vocabulary in design.md)
      - **Rank Ceiling**: actual rank of first relevant result <= `ExpectedRankCeiling`
   f. For negative cases (empty needles): verify zero results returned. Any non-zero result count is a failure.
2. Aggregate metrics per content type and overall
3. Collect individual case failures with actionable detail
4. Build the `Report` struct, populate `Metadata` (git SHA via `exec.Command("git", ...)`, manifest hash via `hashFixtureManifest`, timestamp, Go version)
5. Write JSON to `CAPY_BENCH_RESULTS` path

**Metric computation helpers** — implement as unexported functions in the same file:
- `isRelevant(text string, needles []string) bool` — returns true if text contains ANY needle substring (for R@K, NDCG, MRR)
- `computeRecallAtK(results []SearchResult, needles []string, k int) float64` — 1.0 if any result in top-K is relevant, 0.0 otherwise
- `computeNDCG(results []SearchResult, needles []string, k int) float64`
- `computeMRR(results []SearchResult, needles []string) float64` — 1/rank of first relevant result
- `computeContextRecall(results []SearchResult, needles []string) float64` — fractional: count distinct needles found across ALL result texts / total needles. Used by Track A only.
- `containsAllNeedles(text string, needles []string) bool` — retained for structural-needle assertions where a single chunk must preserve parent context

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

**Important — measurement surface:** Context reduction must measure what actually enters the LLM context, not raw `SearchResult.Content`. In production, `intentSearch` (`internal/server/intent_search.go:14`) formats results as a summary with section count, matched section titles, first-line previews (120 chars), and searchable terms. The benchmark should measure **the byte size of this formatted summary output**, not the raw search results. For the store-level benchmark, approximate by computing `len(formatted_summary)` where the summary follows the same format as `intentSearch` output.

**Flow:**
1. For each content type, load fixtures, create and seed a single store (same as Task 4)
2. For each case, call `SearchWithFallback` with `benchSearchOpts()`, then format results using the same summary logic as `intentSearch` (title + first-line preview per result, plus metadata header)
3. Compute:
   - **Compression Ratio** = `1 - (len(formatted_summary) / len(haystack))`
   - **Context Recall** = `needles_found / total_needles` (fractional, 0.0–1.0). Count distinct needles found across all result content texts.
   - **Perfect Recall** = `1` if Context Recall == 1.0, `0` otherwise
   - **Effective Compression** = `Compression Ratio × Context Recall`
4. For negative cases (empty needles): if zero results returned, Compression Ratio = 1.0, Context Recall = 1.0 (correctly returning nothing is perfect). If results ARE returned, Compression Ratio is computed normally but Context Recall = 0.0 (false positive — nothing should have matched).
5. Aggregate per content type and overall

**Report merge strategy:** Both `TestBenchRetrievalQuality` (Task 4) and `TestBenchContextReduction` are subtests of a single `TestBench(t *testing.T)` function using `t.Run`. This avoids double-indexing (shared store setup) and eliminates the file corruption risk from concurrent package-level writes to `CAPY_BENCH_RESULTS`. The single `TestBench` function writes the report once at the end, containing both retrieval quality and context reduction metrics.

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

- `BenchmarkIndex/{content_type}` — load fixtures, create a fresh store per iteration (or use unique labels per iteration, e.g., `fmt.Sprintf("bench-%s-%d", contentType, i)`), `b.ResetTimer()`, index one haystack per iteration. **Dedup guard:** the same label+content hash hits `AlreadyIndexed` fast-path (`index.go:108`), measuring the no-op instead of actual indexing. Either use unique labels per `b.N` iteration or create a fresh store each iteration.
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
- `bench-perf` — `mkdir -p bench-results`, `CGO_ENABLED=1 go test $(BUILD_TAGS) -bench=. -benchmem -count=6 ... | tee bench-results/{branch}.txt`
- `bench-quality` — `mkdir -p bench-results`, `CGO_ENABLED=1 ... go test $(BUILD_TAGS) -run='^TestBench' -v -p 1 ...` (note: `-p 1` forces serial package execution to prevent concurrent writes to `CAPY_BENCH_RESULTS`)
- `bench-compare` — `benchstat` + `qualstat` side by side, takes `BASE` and `TARGET` args

**`.gitignore`** — add `bench-results/`

### Verify

Full end-to-end:
1. `make bench` — completes without error, produces `.txt` and `.json` in `bench-results/`
2. `make bench-compare BASE=main TARGET=main` — produces comparison table with zero deltas
3. `go test ./...` — regular test suite still passes, quality benchmarks are skipped (no `CAPY_BENCH_RESULTS`)
4. Run `review-code` skill with Go input to review the implementation
5. Run `review-spec` skill to verify implementation matches this design doc
6. Run `test` skill to verify the full test suite
7. Run `document` skill to update relevant docs
