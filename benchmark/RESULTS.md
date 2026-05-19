# Benchmark Results

capy's benchmark suite validates two core claims: that FTS5/BM25 search retrieves the right information, and that context reduction preserves the information the LLM needs. All metrics are deterministic — no LLM-in-the-loop evaluation.

## Setup

- **Corpus**: 156 test cases across 5 content types (markdown, JSON, plaintext, transcripts, curated knowledge)
- **Fixtures**: Synthetic but realistic — documentation, API responses, logs, conversation transcripts, ADRs
- **Search engine**: SQLite FTS5 with two-layer RRF (Porter stemming + trigram), fuzzy Levenshtein correction, post-processing (diversification, proximity reranking, entity boosting)
- **No embeddings**: Pure lexical search. No vector DB, no embedding model, no API key
- **Go version**: go1.25.2
- **Dataset hash**: `sha256:6216880beef5...` (fixture manifest hash — `qualstat` aborts if comparing reports with different hashes)

## Retrieval Quality

Standard IR metrics. A result is "relevant" if it contains any needle substring from the test case.

### Overall

| Metric | Score | Cases |
|--------|-------|-------|
| R@1 | 0.904 | 156 |
| R@3 | 0.974 | |
| R@5 | 0.987 | |
| R@10 | 0.994 | |
| NDCG@10 | 0.952 | |
| MRR | 0.941 | |
| Rank Ceiling Pass | 0.981 | |

### By Content Type

| Content Type | R@1 | R@5 | R@10 | NDCG@10 | MRR | Cases |
|---|---|---|---|---|---|---|
| Transcript | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 30 |
| Curated | 0.933 | 1.000 | 1.000 | 0.975 | 0.967 | 30 |
| Plaintext | 0.900 | 1.000 | 1.000 | 0.957 | 0.944 | 30 |
| Markdown | 0.889 | 1.000 | 1.000 | 0.948 | 0.938 | 36 |
| JSON | 0.800 | 0.933 | 0.967 | 0.881 | 0.857 | 30 |

JSON is the weakest. Recursive key-path chunking sometimes splits deeply nested structures across chunk boundaries, making exact-match queries harder. This is a known trade-off of the JSON chunker — preserving parent context helps, but deep nesting still causes boundary splits.

### What the Metrics Mean

- **R@K**: Is at least one relevant result in the top K? Binary per case, averaged across all cases.
- **NDCG@10**: Normalized Discounted Cumulative Gain — rewards relevant results ranked higher.
- **MRR**: Mean Reciprocal Rank — 1/position of the first relevant result.
- **Rank Ceiling Pass**: Did the first relevant result appear at or above the expected rank?

## Context Reduction (Needle-in-a-Haystack)

"Bytes saved" is a vanity metric if the reduced context drops information the LLM needed. Each test case defines needles — specific facts that must survive reduction. Compression is measured against `intentSearch`-style summaries (title + first-line preview per result), which is the actual surface that enters the LLM context.

### Overall

| Metric | Score | Cases |
|--------|-------|-------|
| Compression Ratio | 44.3% | 156 |
| Context Recall | 0.786 | |
| Perfect Recall Rate | 77.4% | |
| Effective Compression | 35.9% | |

### By Content Type

| Content Type | Compression | Context Recall | Perfect Recall | Eff. Compression | Cases |
|---|---|---|---|---|---|
| Plaintext | 61.2% | 0.850 | 85.0% | 52.3% | 30 |
| Transcript | 57.7% | 0.800 | 80.0% | 47.1% | 30 |
| JSON | 40.7% | 0.750 | 70.0% | 32.9% | 30 |
| Curated | 38.2% | 0.775 | 77.5% | 31.0% | 30 |
| Markdown | 27.1% | 0.760 | 75.0% | 19.4% | 36 |

Plaintext compresses best because it has the largest haystacks relative to the information density. Markdown compresses least because heading-aware chunking preserves structural context (headings, code fences), producing larger summary sections.

### What the Metrics Mean

- **Compression Ratio**: `1 - (summary_bytes / haystack_bytes)`. Higher = more bytes saved.
- **Context Recall**: Fraction of needles found across all search results. 3 needles, 2 found = 0.67.
- **Perfect Recall Rate**: Percentage of cases where every needle survived (Context Recall = 1.0).
- **Effective Compression**: `Compression × Recall`. Partial credit — high compression with low recall scores poorly.

## 5000-Byte Threshold

capy auto-indexes output above 5000 bytes when an `intent` is provided. Below the threshold, raw output passes through. This measures the latency cost of the indexing path.

| Output Size | Latency | Indexed | Output to Context | Compression |
|---|---|---|---|---|
| 4,999 bytes | 1.7 ms | No | 4,999 bytes | — |
| 5,001 bytes | 322 ms | Yes | 316 bytes | 93.7% |
| 10,000 bytes | 7.0 ms | Yes | 318 bytes | 96.8% |
| 50,000 bytes | 24.0 ms | Yes | 319 bytes | 99.4% |

The 5,001-byte case shows the cold-start cost: first indexing into a fresh FTS5 database. Subsequent indexing (10K, 50K) is much faster because the database schema and indexes already exist.

## Match-Layer Accuracy

Match-layer accuracy (1.3% overall) is low because fixture expectations were written assuming single-layer resolution (e.g., "this query should match via `porter`"), but capy's RRF fusion means most queries resolve via `rrf(porter+trigram)` — both layers fire and get fused. The search results are correct; the expected-layer annotations in fixtures need updating to reflect RRF behavior. This is a fixture calibration issue, not a search quality issue.

## Known Limitations

1. **Synthetic fixtures only.** The corpus is hand-crafted, not sampled from real production data. Results may not generalize to all real-world content patterns.
2. **No embedding comparison.** capy uses pure lexical search (FTS5/BM25). We don't benchmark against vector/embedding approaches because capy deliberately avoids them (no API key, no model dependency). A fair comparison would need a shared dataset — see [COMPARISON.md](COMPARISON.md).
3. **156 cases is small.** Enough for regression detection; not enough for statistical significance claims. We'd need 500+ cases to make strong generalization claims.
4. **Context reduction measures summaries, not LLM comprehension.** We measure whether needles appear in the summary text. We don't measure whether an LLM can actually answer questions from the summary. That would require LLM-in-the-loop evaluation, which we deliberately avoid for determinism.

## Reproducing

```bash
git clone https://github.com/serpro69/capy.git
cd capy
export CAPY_DB_KEY=test-key-for-development
make bench-quality    # → bench-results/{branch}.json

# View results
go run -tags fts5 ./cmd/qualstat bench-results/*.json
```

All fixtures, test code, and the `qualstat` CLI are committed. Results land in `bench-results/` (gitignored). The dataset manifest hash in the JSON report ensures you're comparing results from identical fixtures.

## Updating These Tables

The tables in this document are generated from the JSON report:

```bash
go run -tags fts5 ./cmd/qualstat --markdown bench-results/{branch}.json
```

Copy the output, replacing the tables above. The prose sections (methodology, caveats, known limitations) need manual review when results change significantly.
