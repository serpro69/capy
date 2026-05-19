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
| Rank Ceiling Pass | 1.000 | |

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
| Compression Ratio | 50.5% | 156 |
| Context Recall | 0.983 | |
| Perfect Recall Rate | 97.1% | |
| Effective Compression | 50.4% | |

### By Content Type

| Content Type | Compression | Context Recall | Perfect Recall | Eff. Compression | Cases |
|---|---|---|---|---|---|
| Transcript | 62.7% | 1.000 | 100.0% | 62.7% | 30 |
| Plaintext | 60.4% | 1.000 | 100.0% | 60.4% | 30 |
| Curated | 48.4% | 1.000 | 100.0% | 48.4% | 30 |
| JSON | 44.3% | 0.925 | 87.5% | 43.9% | 30 |
| Markdown | 39.1% | 0.990 | 97.9% | 39.1% | 36 |

Transcript and plaintext compress best because they have the largest haystacks relative to information density. Markdown compresses least because heading-aware chunking preserves structural context (headings, code fences), producing larger summary sections. JSON has the lowest context recall due to deeply nested structures splitting across chunk boundaries.

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

## Writing Fixtures

Fixtures live in `internal/store/testdata/bench/`. One JSONL file per content type, one JSON object per line.

### Schema

Each line is a JSON object with this structure:

```json
{
  "id": "md_001",
  "content_type": "markdown",
  "haystack": "# Heading\n\nContent to index...",
  "source_label": "bench-markdown-001",
  "source_kind": "durable",
  "cases": [
    {
      "case_id": "md_001_q1",
      "query": "search query terms",
      "needles": ["substring that must appear in results"],
      "expected_layer": "rrf(porter+trigram)",
      "expected_rank_ceiling": 3
    }
  ]
}
```

**Fields:**

| Field | Required | Description |
|---|---|---|
| `id` | Yes | Unique entry ID. Convention: `{type_prefix}_{NNN}` (e.g., `md_001`, `json_005`). |
| `content_type` | Yes | One of: `markdown`, `json`, `plaintext`, `transcript`, `curated`. Determines which indexing method is used. |
| `haystack` | Yes | The content to index. Should be realistic — representative of actual tool output, docs, API responses, logs, or knowledge entries. |
| `source_label` | Yes | Label for the indexed source. Convention: `bench-{type}-{NNN}`. |
| `source_kind` | Yes | One of: `durable`, `ephemeral`, `session`. Affects search visibility — benchmarks use `benchSearchOpts()` which includes all three kinds. |
| `cases` | Yes | Array of test cases (queries to run against this haystack). Aim for 3-5 cases per entry. |

**Case fields:**

| Field | Required | Description |
|---|---|---|
| `case_id` | Yes | Unique case ID. Convention: `{entry_id}_q{N}` for positive, `{entry_id}_q_neg` for negative. |
| `query` | Yes | Search query to run. |
| `needles` | Yes | Substrings that must appear in search results. Empty `[]` = negative case (expects zero results). |
| `expected_layer` | Yes | Which search layer should resolve this query (see below). |
| `expected_rank_ceiling` | Yes | Maximum acceptable rank for the first relevant result. Set to `0` to skip rank check. |

### Content types and indexing methods

| `content_type` | Index method | When to use |
|---|---|---|
| `markdown` | `Index()` with auto-detect | Documentation, READMEs, design docs. Heading-aware chunking preserves H1-H4 context. |
| `json` | `IndexJSON()` | API responses, config files, structured data. Recursive key-path chunking. |
| `plaintext` | `IndexPlainText()` | Logs, test output, build output. Paragraph-split chunking. |
| `transcript` | `IndexChunked()` | Conversation transcripts in Human:/Assistant: format. |
| `curated` | `Index()` with auto-detect | ADRs, conventions, review findings. User-curated knowledge. |

### Expected match layers

capy's search uses two-layer RRF (Reciprocal Rank Fusion) — Porter stemming and trigram substring run in parallel and results are fused. Most queries resolve via both layers simultaneously.

| Query type | `expected_layer` | Example |
|---|---|---|
| Exact terms present in content | `rrf(porter+trigram)` | "database connection pooling" |
| Deliberate typos/misspellings | `fuzzy+rrf(porter+trigram)` | "cnofiguration poolign" |
| Only trigram can match (substrings) | `trigram` | Rare — only when Porter stemming cannot match |
| Negative (should return nothing) | `none` | Fabricated terms |

Single-layer values like `porter` are only correct when the other layer genuinely cannot match, which is rare.

### Needles

Needles are substrings checked against search result content. A result is "relevant" if it contains ANY needle (for R@K, MRR, NDCG). Context Recall is fractional: `distinct needles found / total needles`.

Good needles:
- Specific strings that appear verbatim in the haystack: `"`maxIdleConns` defaults to 10"`
- Structural context (headings, JSON keys): `"## Connection Pool Settings"`
- Unique identifiers unlikely to appear in other entries

Bad needles:
- Generic terms that appear in many entries (causes false relevance)
- Substrings of common words (triggers unintended matches)

### Negative cases

Negative cases have `needles: []` and must return zero results. Any non-zero result is a failure.

The fuzzy corrector and trigram index are aggressive. Real English words — even domain-unrelated ones like "pipeline", "simulation", or 3-letter words like "tun" — will fuzzy-match or trigram-match into a software corpus. **Use fabricated or ultra-domain-specific terms** (e.g., paleontology Latin nomenclature: `"anomalocaris opabinia hallucigenia"`) that share no substrings with the corpus vocabulary.

### JSON rank ceilings

The JSON chunker splits content along key-path boundaries. Deeply nested structures (e.g., `metrics.http.p99_latency_ms`) may spread across multiple chunks, making rank predictions unreliable. Use generous rank ceilings (5-10) for JSON fixtures, or set `expected_rank_ceiling` to `0` to skip the check entirely.

### Corpus density

Aim for at least 10 entries per content type with 3-5 cases each. BM25 IDF calculations need enough documents for term frequencies to be meaningful — too few entries and every term gets high IDF regardless of actual distinctiveness.

### Dataset hash

All fixture files are hashed together into a single manifest hash embedded in the JSON report. `qualstat` refuses to compare reports with different hashes. This means **any fixture change — even a single character — invalidates all existing reports**. After modifying fixtures, re-run `make bench` on both branches before comparing.

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
