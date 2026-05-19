# Tasks: Benchmark Suite

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: done
> Created: 2026-05-17

## Task 1: Fixture types and test helpers
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md#fixture-helpers](./implementation.md#fixture-helpers)

### Subtasks
- [x] 1.1 Create `internal/store/benchutil_test.go` with `BenchEntry` and `BenchCase` struct types matching the JSONL schema
- [x] 1.2 Implement `loadFixtures(t testing.TB, contentType string) []BenchEntry` using `runtime.Caller(0)` for path resolution
- [x] 1.3 Implement `hashFixtureManifest(t testing.TB) string` — SHA-256 over all fixture files concatenated in sorted order
- [x] 1.4 Implement `newBenchStore(t testing.TB) *ContentStore` — delegate directly to existing `newTestStore(t)` which handles encryption key and cleanup
- [x] 1.5 Implement `seedStore(t testing.TB, store *ContentStore, entries []BenchEntry)` — dispatch to correct `Index*` method per `content_type` (markdown→`Index`, json→`IndexJSON`, plaintext→`IndexPlainText`, transcript→`IndexChunked`, curated→`Index`)
- [x] 1.6a Implement `benchSearchOpts() SearchOptions` returning `IncludeKinds: {KindDurable, KindEphemeral, KindSession}` to ensure all fixture kinds are visible to search
- [x] 1.6 Add `TestBenchFixtureLoad` that validates each fixture file deserializes without error

## Task 2: Seed fixture datasets
- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md#seed-fixtures](./implementation.md#seed-fixtures)

### Subtasks
- [x] 2.1 Create `internal/store/testdata/bench/markdown.jsonl` — 12 entries with realistic documentation content, 4 cases each covering exact/typo/negative queries
- [x] 2.2 Create `internal/store/testdata/bench/json.jsonl` — 10 entries: API responses, config files, structured data with parent-key preservation needles
- [x] 2.3 Create `internal/store/testdata/bench/plaintext.jsonl` — 10 entries: test output, logs, command output, build output, profiling
- [x] 2.4 Create `internal/store/testdata/bench/transcript.jsonl` — 10 entries: conversation transcript content in Human:/Assistant: format
- [x] 2.5 Create `internal/store/testdata/bench/curated.jsonl` — 10 entries: ADRs, conventions, review findings
- [x] 2.6 Verify `TestBenchFixtureLoad` passes for all five files

## Task 3: `qualstat` CLI
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md#qualstat](./implementation.md#qualstat)

### Subtasks
- [x] 3.1 Create `cmd/qualstat/main.go` with `Report`, `Metadata`, `Metrics`, `Failure`, `PostProcessingDelta` structs matching the JSON report schema (see design.md input JSON report schema section)
- [x] 3.2 Implement single-file mode — parse one JSON report, print absolute metrics as ASCII table
- [x] 3.3 Implement two-file comparison mode — validate dataset hash match, compute deltas, print comparison table with regression markers
- [x] 3.4 Implement per-metric-category warning thresholds — strict (0.0) for Perfect Recall Rate and Match-Layer Accuracy, configurable (default -0.02) for R@K/MRR/NDCG/Compression/Context Recall
- [x] 3.5 Implement failures diff — NEW and RESOLVED only, capped at 10 entries
- [x] 3.6 Add `--color` flag, exit code logic (0 clean, 1 regressions), epsilon float comparison
- [x] 3.7 Create `cmd/qualstat/testdata/` with hand-crafted JSON reports and write table-driven tests covering all modes

## Task 4: Retrieval quality benchmarks
- **Status:** done
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#retrieval-quality](./implementation.md#retrieval-quality)

### Subtasks
- [x] 4.1 Create `internal/store/bench_test.go` with `TestBench` parent function and `CAPY_BENCH_RESULTS` skip guard, with `t.Run("RetrievalQuality", ...)` subtest
- [x] 4.2 Implement per-content-type flow: load fixtures, create single store, seed all haystacks, run all cases with `benchSearchOpts()`
- [x] 4.3 Implement metric helpers: `isRelevant` (any needle match), `computeRecallAtK`, `computeNDCG`, `computeMRR`, `computeContextRecall` (fractional), `containsAllNeedles`
- [x] 4.4 Implement match-layer accuracy validation — `MatchLayer` vs `ExpectedLayer` using exact MatchLayer vocabulary from design.md
- [x] 4.5 Implement rank ceiling checks and negative case validation (zero results for empty needles — any non-zero is a failure)
- [x] 4.6 Build `Report` struct with metadata (git SHA, manifest hash, timestamp, Go version), per-content-type aggregates, overall aggregates, post-processing rank deltas, and failures array
- [x] 4.7 `TestBench` parent writes JSON report to `CAPY_BENCH_RESULTS` path once, after both subtests complete

## Task 5: Context reduction benchmarks (NIAH)
- **Status:** done
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#context-reduction](./implementation.md#context-reduction)

### Subtasks
- [x] 5.1 Add `t.Run("ContextReduction", ...)` subtest inside the `TestBench` parent function from Task 4 (shared store setup — no separate file needed)
- [x] 5.2 Implement NIAH metrics: Compression Ratio (measure formatted summary size, not raw SearchResult.Content), fractional Context Recall, Perfect Recall, Effective Compression
- [x] 5.3 Format results using `intentSearch`-style summary (title + first-line preview per result) to measure the actual context-reduction surface
- [x] 5.4 Handle negative cases — zero results = perfect (CR=1, recall=1); non-zero results for negative query = failure (recall=0)
- [x] 5.5 Aggregate per content type and overall, results merged into same `Report` struct via parent `TestBench`

## Task 6: Performance and executor benchmarks
- **Status:** done
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#performance](./implementation.md#performance)

### Subtasks
- [x] 6.1 Create `internal/store/bench_perf_test.go` with `BenchmarkIndex/{content_type}` — use unique labels per iteration (e.g., `bench-{type}-{i}`) to avoid hitting `AlreadyIndexed` dedup fast-path, `b.ResetTimer` after setup
- [x] 6.2 Add `BenchmarkSearch/{content_type}/{corpus_size}` — pre-seed at N=100/1000/10000, search one query per iteration
- [x] 6.3 Add `BenchmarkSearchByTier/{tier}` — queries designed to hit specific tiers, grouped by expected MatchLayer
- [x] 6.4 Create `internal/executor/bench_test.go` with `BenchmarkExecutorOverhead/{language}` and `BenchmarkExecutorOverheadParallel/{language}` with `exec.LookPath` skip guards
- [x] 6.5 Add `BenchmarkExecutorScaling/{output_size}` at 1KB/10KB/100KB/1MB
- [x] 6.6 Add `BenchmarkSafeEnv` and `BenchmarkProcessGroupKill`
- [x] 6.7 Create `internal/server/bench_integration_test.go` with `TestBench5000ByteThreshold` — measure latency cliff at 4999 vs 5001 bytes, append to quality JSON report

## Task 7: Makefile and final verification
- **Status:** done
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6

### Subtasks
- [x] 7.1 Add `bench`, `bench-perf`, `bench-quality`, and `compare` targets to `Makefile` — must include `CGO_ENABLED=1`, `$(BUILD_TAGS)`, and `-p 1` for bench-quality
- [x] 7.2 Add `bench-results/` to `.gitignore`
- [x] 7.3 End-to-end: `make bench-quality` completes, produces `.json` in `bench-results/`
- [x] 7.4 End-to-end: self-comparison via `qualstat` shows zero deltas
- [x] 7.5 Verify `go test ./...` passes with quality benchmarks skipped
- [x] 7.6 Run `test` skill to verify full test suite
- [x] 7.7 Run `document` skill to update relevant docs
- [x] 7.8 Run `review-code` skill with Go input to review the implementation
- [x] 7.9 Run `review-spec` skill to verify implementation matches design and implementation docs
