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
- **Context Recall** = `1` if ALL needles found in returned snippets, `0` otherwise
- **Effective Compression** = `Compression Ratio × Context Recall` — zero credit if any needle was dropped

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
- **R@K** (K=1,3,5,10) — is the needle in top-K results?
- **NDCG@10** — normalized discounted cumulative gain (any chunk containing a needle substring is "relevant")
- **MRR** — reciprocal rank of first result containing a needle
- **Routing Accuracy** — does `SearchResult.MatchLayer` match `expected_layer` from the fixture?
- **Rank Ceiling Pass Rate** — did the needle appear within `expected_rank_ceiling`?

### Search Tier Testing

The three-tier fallback pipeline (Porter+trigram RRF → flat OR → fuzzy Levenshtein) is tested as a full pipeline with **expected tier assertions**, not in isolation.

Rationale:
- Isolated tier testing ignores **pipeline shadowing** — noisy tier 1 returning low-quality matches prevents tier 2 from firing
- Black-box testing hides **routing regressions** — correct result via wrong tier wastes CPU

Each fixture case carries `expected_layer`. The benchmark validates that the pipeline routes queries through the intended tier. Routing regressions are caught even when overall R@K stays stable.

Post-processing (source diversification, entity boost, proximity reranking) is evaluated via pre- vs post-reranking position tracking. If a needle drops rank after post-processing, the report flags a negative rank delta.

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
- **Negative cases** — empty `needles` + `expected_layer: "none"`. Tests that search correctly returns nothing for irrelevant queries
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

**Dataset hash guard**: benchmark tests compute SHA-256 of the fixture file and embed it in the JSON report. `qualstat` aborts if comparing reports from different dataset versions.

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

bench-perf:
	mkdir -p bench-results
	go test -bench=. -benchmem -count=6 \
	  ./internal/store/ ./internal/executor/ \
	  | tee bench-results/$$(git rev-parse --abbrev-ref HEAD).txt

bench-quality:
	mkdir -p bench-results
	CAPY_BENCH_RESULTS=bench-results/$$(git rev-parse --abbrev-ref HEAD).json \
	  go test -run='^TestBench' -v ./internal/store/ ./internal/server/

compare:
	@echo "=== Performance (benchstat) ==="
	benchstat bench-results/$(BASE).txt bench-results/$(TARGET).txt
	@echo ""
	@echo "=== Retrieval Quality (qualstat) ==="
	go run ./cmd/qualstat bench-results/$(BASE).json bench-results/$(TARGET).json
```

Usage: `make bench` on main, switch to feature branch, `make bench` again, then `make compare BASE=main TARGET=feature`.

Quality tests use a skip guard — they skip when `CAPY_BENCH_RESULTS` is unset, so `go test ./...` during normal development is not affected.

## Technical Notes

### Go Test Working Directory

`go test` sets cwd to the package directory. Cross-package fixture access (e.g., `internal/server/` reading `internal/store/testdata/bench/`) requires a path helper using `runtime.Caller(0)` to resolve the module root.

### Dataset Size

If fixtures stay small (< 10-20 MB total), commit them directly. If haystacks grow large (e.g., massive API responses for context reduction testing), generate them dynamically via a script or fetch via a `make download-datasets` target.

### Executor Benchmark I/O Variance

Executor benchmarks create temp directories per run, making them disk-I/O bound. Document that running with `TMPDIR=/dev/shm` (tmpfs) isolates executor CPU overhead from host disk latency for reproducible results.

## Open Questions

1. Should `qualstat` support exporting results to a format consumable by visualization tools (e.g., CSV for spreadsheet charting)?
2. What is the minimum dataset size per content type needed for statistically meaningful retrieval metrics?
3. Should post-processing rank deltas (pre vs post reranking) be part of the `qualstat` report or a separate diagnostic?
