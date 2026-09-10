# Design: Benchmark Suite

> Issue: [#61](https://github.com/serpro69/capy/issues/61)
> Status: draft
> Created: 2026-05-17

## Problem Statement

Capy claims ~98% context reduction and provides persistent agent memory via FTS5/BM25 search with three-tier fallback. There is no way to validate these claims, catch regressions, or compare with alternative tooling. The project needs a benchmark suite that measures both pillars quantitatively.

## Goals

1. Validate context reduction effectiveness — not just bytes saved, but whether reduced context preserves the information the LLM needs
2. Measure retrieval quality across all content types capy indexes (markdown, JSON, plaintext, transcripts, curated knowledge)
3. Track performance (indexing throughput, search latency, sandbox overhead)
4. Enable regression detection after feature changes or bugfixes
5. Provide comparison baselines for evaluating alternative approaches

## Non-Goals

- CI integration (benchmarks are manually triggered)
- LLM-in-the-loop evaluation (all metrics are deterministic)
- Benchmarking the MCP transport layer or Claude Code integration

## Two-Track Design

### Track A: Context Reduction (NIAH)

The core insight: "bytes saved" is a vanity metric in isolation. A 98% reduction is harmful if the 2% kept dropped the information the LLM needed.

Context reduction is measured via **Needle-in-a-Haystack (NIAH)**: each test case provides a large raw output (haystack), a search query (intent), and specific information that must survive reduction (needles). Three metrics per case:

- **Compression Ratio** = `1 - (size_of_returned_snippets / size_of_haystack)`
- **Context Recall** = `needles_found / total_needles` (fractional, 0.0–1.0). A case with 3 needles where 2 survive scores 0.67. Perfect recall = 1.0; total loss = 0.0. This gives gradient signal for regression tracking — a change that drops 1 needle out of 5 is distinguishable from one that drops all 5.
- **Perfect Recall Pass Rate** = percentage of cases achieving Context Recall = 1.0. This is the strict binary metric for pass/fail thresholds.
- **Effective Compression** = `Compression Ratio × Context Recall` — partial credit proportional to needle survival

This captures the information-loss vs compression tradeoff without requiring an LLM judge.

### Track B: Retrieval Quality

Measures search accuracy across capy's FTS5 knowledge base. Capy indexes five content types, each with different chunking strategies:

| Content Type | Chunker | Source Kind |
|---|---|---|
| Markdown | Heading-aware (H1-H4), code-fence aware | durable |
| JSON | Recursive key-path walk, identity field titles | durable/ephemeral |
| Plaintext | Blank-line then fixed-line groups (20 lines, 2-line overlap) | ephemeral |
| Transcripts | Pre-chunked by session sweep | session |
| Curated Knowledge | User-provided via `capy_index` | durable |

Metrics per content type:
- **R@K** (K=1,3,5,10) — is at least one relevant result in top-K? A result is relevant if it contains ANY needle substring. This is standard IR recall-at-K — it does NOT require all needles in a single result (that's Context Recall in Track A).
- **NDCG@10** — normalized discounted cumulative gain (any chunk containing any needle substring is "relevant")
- **MRR** — reciprocal rank of first relevant result (first result containing any needle)
- **Match-Layer Accuracy** — does `SearchResult.MatchLayer` match `expected_layer` from the fixture? Note: `MatchLayer` tracks which FTS layer produced the result, not the full pipeline route (e.g., synonym-AND vs flat-OR fallback both produce the same layer tags). See [MatchLayer vocabulary](#matchlayer-vocabulary) for the complete set of values.
- **Rank Ceiling Pass Rate** — did the needle appear within `expected_rank_ceiling`?

**Important:** Benchmarks must pass `SearchOptions{IncludeKinds: []SourceKind{KindDurable, KindEphemeral, KindSession}}` to `SearchWithFallback`. The default empty `IncludeKinds` excludes ephemeral sources (see `effectiveKindFilter` in `search.go:558`), which would make plaintext and some JSON fixtures invisible to search.

### Search Tier Testing

The search pipeline has four passes, not three (`search.go:30-67`):

1. **Synonym-AND RRF** — synonym-expanded query with implicit AND between groups, fused across porter + trigram
2. **Flat-OR RRF** — if pass 1 returned zero results, retry with flat OR (no synonym expansion)
3. **Fuzzy-corrected synonym-AND RRF** — if results < limit, correct typos via Levenshtein, re-enter synonym AND
4. **Fuzzy-corrected flat-OR RRF** — if fuzzy synonym AND also returned zero, fall back to flat OR on corrected query

The pipeline is tested as a full black box with **expected layer assertions**, not in isolation.

Rationale:
- Isolated tier testing ignores **pipeline shadowing** — noisy pass 1 returning low-quality matches prevents later passes from firing
- Black-box testing hides **layer regressions** — correct result via wrong layer wastes CPU

Each fixture case carries `expected_layer`. The benchmark validates that results come from the expected FTS layer. Layer regressions are caught even when overall R@K stays stable.

#### MatchLayer Vocabulary {#matchlayer-vocabulary}

The complete set of `MatchLayer` string values emitted by `rrfSearch` and `SearchWithFallback`:

| Value | Meaning |
|---|---|
| `porter` | Result found only in porter FTS5 table |
| `trigram` | Result found only in trigram FTS5 table |
| `rrf(porter+trigram)` | Result found in both tables, fused score > single-layer max |
| `fuzzy+porter` | Fuzzy-corrected query, result from porter only |
| `fuzzy+trigram` | Fuzzy-corrected query, result from trigram only |
| `fuzzy+rrf(porter+trigram)` | Fuzzy-corrected query, result from both tables |
| `none` | No results expected (negative test case — not a real MatchLayer value) |

Fixture `expected_layer` values must use these exact strings. Note that `MatchLayer` does not distinguish synonym-AND from flat-OR fallback — both call `rrfSearch` and produce the same layer tags. The metric is therefore "match-layer accuracy," not full pipeline routing accuracy.

Post-processing (source diversification, entity boost, proximity reranking) is evaluated via pre- vs post-reranking position tracking. If a needle drops rank after post-processing, the report flags a negative rank delta. Post-processing rank deltas are included in the `qualstat` report as a diagnostic section (resolving Open Question 3).

### Shared: Performance

Standard Go `testing.B` benchmarks measured via `benchstat`:

**Store performance** (`internal/store/bench_perf_test.go`):
- Indexing throughput per content type
- Search latency at corpus sizes N=100, 1000, 10000
- Search latency grouped by MatchLayer (fallback penalty quantification)

**Executor performance** (`internal/executor/bench_test.go`):
- Sandbox overhead per language vs direct `exec.Command`
- Parallel executor overhead via `b.RunParallel()` (lock contention in `BuildSafeEnv`, tmpdir allocation)
- Output capture scaling at 1KB/10KB/100KB/1MB
- `BuildSafeEnv` cost
- Process group kill latency

**5000-byte threshold** (`internal/server/bench_integration_test.go`):
- Latency cliff when ephemeral indexing kicks in (4999 vs 5001 bytes)
- Cost/benefit: indexing overhead vs token savings at various output sizes
- Measures the actual `intentSearch` output (formatted summary with titles, first-line previews, searchable terms) — not raw `SearchResult.Content`. This matches the real context-reduction surface as implemented in `intent_search.go`.

## Architecture

### Single-Repo Layout

Everything lives in the capy repo:

```
internal/store/
  bench_test.go              # retrieval quality (TestBench* functions → JSON)
  bench_perf_test.go         # performance (testing.B → benchstat)
  bench_context_test.go      # context reduction NIAH (TestBench* → JSON)
  testdata/bench/
    markdown.jsonl
    json.jsonl
    plaintext.jsonl
    transcript.jsonl
    curated.jsonl
internal/executor/
  bench_test.go              # sandbox overhead (testing.B → benchstat)
internal/server/
  bench_integration_test.go  # 5000-byte threshold (TestBench* → JSON)
cmd/qualstat/
  main.go                    # quality report diff CLI
bench-results/               # .gitignored, local, named by branch/SHA
Makefile                     # bench targets
```

Rationale for single repo:
- Go benchmarks need `internal/` package access
- Datasets co-located with benchmarks avoids cross-repo env var indirection
- Atomic commits: code change + fixture update in one PR
- `testdata/` is ignored by `go build` — no binary bloat

### Dataset Format

Per-content-type JSONL files. Each line is a self-contained test entry with one haystack and multiple query cases:

```json
{
  "id": "md_001",
  "content_type": "markdown",
  "haystack": "...raw content to index...",
  "source_label": "bench-markdown-001",
  "source_kind": "durable",
  "cases": [
    {
      "case_id": "md_001_q1",
      "query": "database connection pooling configuration",
      "needles": ["maxIdleConns defaults to 10", "## Database Config"],
      "expected_layer": "porter",
      "expected_rank_ceiling": 3
    },
    {
      "case_id": "md_001_q2",
      "query": "cnofiguration poolign",
      "needles": ["maxIdleConns defaults to 10"],
      "expected_layer": "fuzzy+porter",
      "expected_rank_ceiling": 5
    },
    {
      "case_id": "md_001_q_neg",
      "query": "kubernetes pod affinity",
      "needles": [],
      "expected_layer": "none",
      "expected_rank_ceiling": 0
    }
  ]
}
```

Key schema decisions:
- **`needles` as array** — all must be present for Context Recall = 1. Catches orphaned snippet problem (structural context stripped by chunker, e.g., parent JSON keys lost)
- **Negative cases** — empty `needles` + `expected_layer: "none"`. A negative case asserts "no results expected." If `SearchWithFallback` returns any results for a negative query, this is a failure in both tracks: Track B flags it as a match-layer accuracy failure (`expected: none, got: <layer>`), and Track A treats it as imperfect compression (bytes entered context that shouldn't have). Negative queries must be genuinely unrelated to the entire corpus for that content type, not just the individual haystack — since all haystacks are indexed into one store, a "negative" query that happens to match another entry is a fixture bug, not a system failure.
- **`expected_rank_ceiling`** — hard pass/fail for catastrophic ranking regressions. Soft ranking drift tracked via aggregate MRR
- **Multiple cases per haystack** — avoids re-indexing the same content for each query variant

### Store Granularity

Quality benchmarks create one store **per content type**, not per fixture entry. All haystacks from `markdown.jsonl` are indexed into a single store before queries run.

Rationale: BM25 relies on Inverse Document Frequency (IDF). A single-document store makes every term 100% document frequency, trivially inflating R@K and NDCG. Realistic distractor noise is required for meaningful ranking evaluation.

### Result Storage and Comparison

No CI — developers run benchmarks locally and compare manually.

**Performance results**: `benchstat` (standard Go tooling)
- Output to `bench-results/{branch}.txt`
- Compare: `benchstat bench-results/main.txt bench-results/feature.txt`

**Quality results**: `qualstat` (custom Go CLI mirroring `benchstat` UX)
- Output to `bench-results/{branch}.json`
- Compare: `qualstat bench-results/main.json bench-results/feature.json`

**Dataset manifest hash**: benchmark tests compute a SHA-256 hash of ALL fixture files concatenated in sorted order (a manifest hash), not per-file hashes. This single hash is embedded in the JSON report metadata. `qualstat` aborts if comparing reports with different manifest hashes, which catches any fixture change across any content type.

## `qualstat` CLI Design

`cmd/qualstat/main.go` — zero-dependency (stdlib only) Go CLI.

### Modes

- **Single report**: `qualstat results/main.json` — prints absolute metrics table, no deltas
- **Comparison**: `qualstat results/main.json results/feature.json` — prints deltas with regression markers

### Output Format

Markdown-compatible ASCII table:

```
Dataset: sha256:abc123... (verified)

=== Retrieval Quality ===
Metric                    main      feature    delta
------------------------------------------------------
R@1  (markdown)           0.850     0.880     +0.030
R@5  (markdown)           0.950     0.960     +0.010
Routing Accuracy (md)     0.980     0.920     -0.060 !

=== Context Reduction ===
Metric                    main      feature    delta
------------------------------------------------------
Eff. Compression (md)     94.2%     94.2%      0.0%
Context Recall (json)     1.000     0.950     -0.050 !

=== Overall ===
...

Failures diff: 2 new, 1 resolved
  NEW: json_003_q2 -- routing: expected trigram, got fuzzy+porter
  RESOLVED: md_012_q3 -- was: ranking breach, now passes
```

### Input JSON Report Schema

The JSON report produced by benchmark tests and consumed by `qualstat`:

```json
{
  "metadata": {
    "timestamp": "2026-05-17T14:30:00Z",
    "git_sha": "abc123def",
    "git_branch": "main",
    "dataset_hash": "sha256:e3b0c44298...",
    "go_version": "go1.23.0"
  },
  "by_content_type": {
    "markdown": {
      "recall_at_1": 0.85,
      "recall_at_3": 0.92,
      "recall_at_5": 0.95,
      "recall_at_10": 0.98,
      "ndcg_at_10": 0.87,
      "mrr": 0.89,
      "match_layer_accuracy": 0.96,
      "rank_ceiling_pass_rate": 0.98,
      "avg_compression_ratio": 0.97,
      "avg_context_recall": 0.97,
      "perfect_recall_rate": 0.94,
      "avg_effective_compression": 0.94,
      "case_count": 50,
      "negative_case_count": 5,
      "negative_false_positive_count": 0
    }
  },
  "overall": { },
  "post_processing_deltas": [
    {
      "case_id": "md_001_q1",
      "pre_rank": 1,
      "post_rank": 3,
      "delta": -2
    }
  ],
  "failures": [
    {
      "case_id": "json_003_q2",
      "type": "match_layer",
      "expected": "trigram",
      "actual": "fuzzy+porter",
      "detail": "query matched via fuzzy correction instead of trigram"
    }
  ]
}
```

### Warning Thresholds

Per-metric category (not global):
- **Strict (zero tolerance)**: Context Recall, Routing Accuracy — in deterministic SQLite, any regression is a bug
- **Configurable (default -0.02)**: R@K, MRR, NDCG, Compression Ratio — natural trade-offs from tuning are acceptable within bounds

### Behavior

- Failures diff shows only NEW and RESOLVED (persistent failures omitted — not actionable)
- Failure output capped at 10 entries, then `... and N more omitted`
- No color by default (pipe-friendly, AI-agent-friendly). Optional `--color` flag
- Exit code: 0 clean, 1 regressions detected
- Float comparison uses epsilon (0.0001) to prevent false warnings from rounding jitter

## Orchestration

Makefile targets at repo root:

```makefile
.PHONY: bench bench-perf bench-quality compare

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
	$(error BASE is required: make bench-compare BASE=<branch> TARGET=<branch>)
endif
ifndef TARGET
	$(error TARGET is required: make bench-compare BASE=<branch> TARGET=<branch>)
endif
	@echo "=== Performance (benchstat) ==="
	benchstat bench-results/$(BASE).txt bench-results/$(TARGET).txt
	@echo ""
	@echo "=== Retrieval Quality (qualstat) ==="
	go run $(BUILD_TAGS) ./cmd/qualstat bench-results/$(BASE).json bench-results/$(TARGET).json
```

Implementation notes vs original design:

- **Branch name sanitization**: `BENCH_BRANCH` uses `tr '/' '-'` to convert slashes in branch names (e.g., `feat/bench` → `feat-bench`), preventing accidental subdirectory creation.
- **Absolute path**: `$(CURDIR)` prefix on `CAPY_BENCH_RESULTS` because `go test` changes the working directory to each package under test — relative paths resolve per-package, not from the repo root.
- **Clean state**: `rm -f` before `bench-quality` prevents stale keys from accumulating when the store test merges into an existing report.
- **Input validation**: `ifndef` guards on `compare` give clear error messages instead of confusing file-not-found errors from `benchstat`.
- **`-p 1`** in `bench-quality` forces serial package execution. Without it, `go test` runs packages in parallel, and both `internal/store/` and `internal/server/` would write to the same `CAPY_BENCH_RESULTS` JSON file concurrently, corrupting the report. Both packages use merge-append logic so ordering is resilient, but `-p 1` avoids concurrent file access entirely.

Usage: `make bench` on main, switch to feature branch, `make bench` again, then `make bench-compare BASE=main TARGET=feature`.

Quality tests use a skip guard — they skip when `CAPY_BENCH_RESULTS` is unset, so `go test ./...` during normal development is not affected.

## Technical Notes

### Go Test Working Directory

`go test` sets cwd to the package directory. Cross-package fixture access (e.g., `internal/server/` reading `internal/store/testdata/bench/`) requires a path helper using `runtime.Caller(0)` to resolve the module root.

### Dataset Size

If fixtures stay small (< 10-20 MB total), commit them directly. If haystacks grow large (e.g., massive API responses for context reduction testing), generate them dynamically via a script or fetch via a `make download-datasets` target.

### Executor Benchmark I/O Variance

Executor benchmarks create temp directories per run, making them disk-I/O bound. Document that running with `TMPDIR=/dev/shm` (tmpfs) isolates executor CPU overhead from host disk latency for reproducible results.

## Resolved Questions

- **Q2 (minimum dataset size):** 10-20 haystacks per content type with 3-5 cases each. This provides enough corpus density for meaningful BM25 IDF calculations while keeping total fixture size under 10 MB. If retrieval metrics show high variance with this dataset size, increase to 50 entries during Task 2 implementation.
- **Q3 (post-processing rank deltas):** Included in the `qualstat` report as a diagnostic section. See [Search Tier Testing](#search-tier-testing).

## Open Questions

1. Should `qualstat` support exporting results to a format consumable by visualization tools (e.g., CSV for spreadsheet charting)?
