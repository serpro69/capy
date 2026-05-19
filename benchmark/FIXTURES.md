# Writing Benchmark Fixtures

Fixtures live in `internal/store/testdata/bench/`. One JSONL file per content type, one JSON object per line.

## Schema

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

## Content Types and Indexing Methods

| `content_type` | Index method | When to use |
|---|---|---|
| `markdown` | `Index()` with auto-detect | Documentation, READMEs, design docs. Heading-aware chunking preserves H1-H4 context. |
| `json` | `IndexJSON()` | API responses, config files, structured data. Recursive key-path chunking. |
| `plaintext` | `IndexPlainText()` | Logs, test output, build output. Paragraph-split chunking. |
| `transcript` | `IndexChunked()` | Conversation transcripts in Human:/Assistant: format. |
| `curated` | `Index()` with auto-detect | ADRs, conventions, review findings. User-curated knowledge. |

## Expected Match Layers

capy's search uses two-layer RRF (Reciprocal Rank Fusion) — Porter stemming and trigram substring run in parallel and results are fused. Most queries resolve via both layers simultaneously.

| Query type | `expected_layer` | Example |
|---|---|---|
| Exact terms present in content | `rrf(porter+trigram)` | "database connection pooling" |
| Deliberate typos/misspellings | `fuzzy+rrf(porter+trigram)` | "cnofiguration poolign" |
| Only trigram can match (substrings) | `trigram` | Rare — only when Porter stemming cannot match |
| Negative (should return nothing) | `none` | Fabricated terms |

Single-layer values like `porter` are only correct when the other layer genuinely cannot match, which is rare.

## Needles

Needles are substrings checked against search result content. A result is "relevant" if it contains ANY needle (for R@K, MRR, NDCG). Context Recall is fractional: `distinct needles found / total needles`.

Good needles:
- Specific strings that appear verbatim in the haystack: `` `maxIdleConns` defaults to 10 ``
- Structural context (headings, JSON keys): `"## Connection Pool Settings"`
- Unique identifiers unlikely to appear in other entries

Bad needles:
- Generic terms that appear in many entries (causes false relevance)
- Substrings of common words (triggers unintended matches)

## Negative Cases

Negative cases have `needles: []` and must return zero results. Any non-zero result is a failure.

The fuzzy corrector and trigram index are aggressive. Real English words — even domain-unrelated ones like "pipeline", "simulation", or 3-letter words like "tun" — will fuzzy-match or trigram-match into a software corpus. **Use fabricated or ultra-domain-specific terms** (e.g., paleontology Latin nomenclature: `"anomalocaris opabinia hallucigenia"`) that share no substrings with the corpus vocabulary.

## JSON Rank Ceilings

The JSON chunker splits content along key-path boundaries. Deeply nested structures (e.g., `metrics.http.p99_latency_ms`) may spread across multiple chunks, making rank predictions unreliable. Use generous rank ceilings (5-10) for JSON fixtures, or set `expected_rank_ceiling` to `0` to skip the check entirely.

## Corpus Density

Aim for at least 10 entries per content type with 3-5 cases each. BM25 IDF calculations need enough documents for term frequencies to be meaningful — too few entries and every term gets high IDF regardless of actual distinctiveness.

## Dataset Hash

All fixture files are hashed together into a single manifest hash embedded in the JSON report. `qualstat` refuses to compare reports with different hashes. This means **any fixture change — even a single character — invalidates all existing reports**. After modifying fixtures, re-run `make bench` on both branches before comparing.
