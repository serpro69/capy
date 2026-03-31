# ADR-009: Configurable BM25 title weight (vs hardcoded 5x)

**Status:** Accepted
**Date:** 2026-03-31
**Upstream:** context-mode changed from `bm25(chunks, 2.0, 1.0)` to `bm25(chunks, 5.0, 1.0)` in v1.0.26.

## Context

The upstream TS hardcoded a 5x title weight in BM25 scoring to improve heading precision for structured documentation queries. This helps when headings are descriptive ("Authentication Guide", "API Reference").

capy indexes a broader range of content including auto-generated headings from `batch_execute` output (`"Lines 1-20"`, `"Section 3"`, `"batch:git-log,npm-list"`). Boosting these meaningless titles 5x could degrade search quality.

## Decision

Make the BM25 title weight configurable via `[store] title_weight` in `.capy.toml`. Default: `2.0` (current behavior). Users wanting the upstream 5x behavior set `title_weight = 5.0`.

## Rationale

- A tuning knob is better than a hardcoded constant when content structure varies
- Default 2.0 is conservative — doesn't hurt auto-generated content
- Users indexing well-structured docs can opt into 5.0
- The value is threaded through `ContentStore` → dynamic SQL at construction time, not per-query (no runtime cost)
- No SQL injection risk: value comes from config (float64), not request input

## Consequences

- `NewContentStore` takes a `titleWeight float64` parameter
- Dynamic SQL uses `fmt.Sprintf("bm25(%s, %.1f, 1.0)", table, s.titleWeight)` instead of prepared statements
- Test helpers pass `0` (which defaults to 2.0) unless testing custom weights
