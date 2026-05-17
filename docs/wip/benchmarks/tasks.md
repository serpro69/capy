# Tasks: Benchmark Suite

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-05-17

## Task 1: Fixture types and test helpers
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#fixture-helpers](./implementation.md#fixture-helpers)

### Subtasks
- [ ] 1.1 Create `internal/store/benchutil_test.go` with `BenchEntry` and `BenchCase` struct types matching the JSONL schema
- [ ] 1.2 Implement `loadFixtures(t testing.TB, contentType string) []BenchEntry` using `runtime.Caller(0)` for path resolution
- [ ] 1.3 Implement `hashFixtureFile(t testing.TB, contentType string) string` returning hex-encoded SHA-256
- [ ] 1.4 Implement `newBenchStore(t testing.TB) *ContentStore` wrapping the existing `newTestStore` pattern with `t.Cleanup`
- [ ] 1.5 Implement `seedStore(t testing.TB, store *ContentStore, entries []BenchEntry)` to index all haystacks
- [ ] 1.6 Add `TestBenchFixtureLoad` that validates each fixture file deserializes without error

## Task 2: Seed fixture datasets
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md#seed-fixtures](./implementation.md#seed-fixtures)

### Subtasks
- [ ] 2.1 Create `internal/store/testdata/bench/markdown.jsonl` — 10-20 entries with realistic documentation content, 3-5 cases each covering exact/partial/typo/negative/structural queries
- [ ] 2.2 Create `internal/store/testdata/bench/json.jsonl` — API responses, config files, structured data with parent-key preservation needles
- [ ] 2.3 Create `internal/store/testdata/bench/plaintext.jsonl` — logs, command output, build output
- [ ] 2.4 Create `internal/store/testdata/bench/transcript.jsonl` — conversation transcript content
- [ ] 2.5 Create `internal/store/testdata/bench/curated.jsonl` — architecture decisions, conventions, review findings
- [ ] 2.6 Verify `TestBenchFixtureLoad` passes for all five files

## Task 3: `qualstat` CLI
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#qualstat](./implementation.md#qualstat)

### Subtasks
- [ ] 3.1 Create `cmd/qualstat/main.go` with `Report`, `Metadata`, `Metrics`, `Failure` structs matching the JSON report schema
- [ ] 3.2 Implement single-file mode — parse one JSON report, print absolute metrics as ASCII table
- [ ] 3.3 Implement two-file comparison mode — validate dataset hash match, compute deltas, print comparison table with regression markers
- [ ] 3.4 Implement per-metric-category warning thresholds — strict (0.0) for Context Recall and Routing Accuracy, configurable (default -0.02) for R@K/MRR/NDCG/Compression
- [ ] 3.5 Implement failures diff — NEW and RESOLVED only, capped at 10 entries
- [ ] 3.6 Add `--color` flag, exit code logic (0 clean, 1 regressions), epsilon float comparison
- [ ] 3.7 Create `cmd/qualstat/testdata/` with hand-crafted JSON reports and write table-driven tests covering all modes

## Task 4: Retrieval quality benchmarks
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#retrieval-quality](./implementation.md#retrieval-quality)

### Subtasks
- [ ] 4.1 Create `internal/store/bench_test.go` with `TestBenchRetrievalQuality` function and `CAPY_BENCH_RESULTS` skip guard
- [ ] 4.2 Implement per-content-type flow: load fixtures, create single store, seed all haystacks, run all cases
- [ ] 4.3 Implement metric helpers: `computeRecallAtK`, `computeNDCG`, `computeMRR`, `containsAllNeedles`
- [ ] 4.4 Implement routing accuracy validation — `MatchLayer` vs `ExpectedLayer` comparison
- [ ] 4.5 Implement rank ceiling checks and negative case validation (zero results for empty needles)
- [ ] 4.6 Build `Report` struct with metadata (git SHA, dataset hash, timestamp, Go version), per-content-type aggregates, overall aggregates, and failures array
- [ ] 4.7 Write JSON report to `CAPY_BENCH_RESULTS` path

## Task 5: Context reduction benchmarks (NIAH)
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#context-reduction](./implementation.md#context-reduction)

### Subtasks
- [ ] 5.1 Create `internal/store/bench_context_test.go` with `TestBenchContextReduction` and skip guard
- [ ] 5.2 Implement NIAH metric computation: Compression Ratio, Context Recall, Effective Compression
- [ ] 5.3 Handle negative cases — correctly returning nothing = perfect compression with perfect recall
- [ ] 5.4 Aggregate per content type and overall, merge results into the quality JSON report
- [ ] 5.5 Evaluate whether to share store setup with Task 4 (subtests of single `TestBench`) or keep independent — decide based on indexing cost during implementation

## Task 6: Performance and executor benchmarks
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Docs:** [implementation.md#performance](./implementation.md#performance)

### Subtasks
- [ ] 6.1 Create `internal/store/bench_perf_test.go` with `BenchmarkIndex/{content_type}` — index one haystack per iteration, `b.ResetTimer` after setup
- [ ] 6.2 Add `BenchmarkSearch/{content_type}/{corpus_size}` — pre-seed at N=100/1000/10000, search one query per iteration
- [ ] 6.3 Add `BenchmarkSearchByTier/{tier}` — queries designed to hit specific tiers, grouped by expected MatchLayer
- [ ] 6.4 Create `internal/executor/bench_test.go` with `BenchmarkExecutorOverhead/{language}` and `BenchmarkExecutorOverheadParallel/{language}` with `exec.LookPath` skip guards
- [ ] 6.5 Add `BenchmarkExecutorScaling/{output_size}` at 1KB/10KB/100KB/1MB
- [ ] 6.6 Add `BenchmarkSafeEnv` and `BenchmarkProcessGroupKill`
- [ ] 6.7 Create `internal/server/bench_integration_test.go` with `TestBench5000ByteThreshold` — measure latency cliff at 4999 vs 5001 bytes, append to quality JSON report

## Task 7: Makefile and final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6

### Subtasks
- [ ] 7.1 Add `bench`, `bench-perf`, `bench-quality`, and `compare` targets to `Makefile`
- [ ] 7.2 Add `bench-results/` to `.gitignore`
- [ ] 7.3 End-to-end: `make bench` completes, produces `.txt` and `.json` in `bench-results/`
- [ ] 7.4 End-to-end: `make compare BASE=main TARGET=main` shows zero deltas
- [ ] 7.5 Verify `go test ./...` passes with quality benchmarks skipped
- [ ] 7.6 Run `test` skill to verify full test suite
- [ ] 7.7 Run `document` skill to update relevant docs
- [ ] 7.8 Run `review-code` skill with Go input to review the implementation
- [ ] 7.9 Run `review-spec` skill to verify implementation matches design and implementation docs
