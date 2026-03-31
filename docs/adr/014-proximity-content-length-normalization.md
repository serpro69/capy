# ADR-014: Proximity reranking normalized by content length

**Status:** Accepted
**Date:** 2026-03-31
**Upstream:** context-mode uses `proximityBoost = 1 + (1 / (1 + minDistance/100))` with a magic constant of 100.

## Context

Proximity reranking boosts search results where query terms appear close together. The upstream TS formula uses `minDistance/100` — the constant 100 was presumably tuned for their typical chunk sizes but has no principled basis.

## Decision

Normalize by content length instead: `boost = 1.0 / (1.0 + float64(minSpan) / float64(max(len(content), 1)))`.

Additionally, use FTS5 highlight markers (char(2)/char(3)) to find match positions instead of re-scanning content with `strings.Index`.

## Rationale

- A 50-character span in a 100-character chunk should be treated differently than a 50-character span in a 10,000-character chunk. Normalizing by content length captures this.
- The magic constant 100 is arbitrary — it happens to work for medium-sized chunks but over-boosts in small chunks and under-boosts in large ones.
- FTS5 highlight markers are already computed by the search query — they pinpoint exact match locations including stemmed forms that wouldn't match via naive string search.
- Both improvements are free in terms of API surface — the reranking formula is an internal implementation detail.

## Consequences

- Proximity boost range is (0, 1] multiplied by `(1 + boost)`, giving final multiplier range of [1, 2]
- Small chunks with close terms get proportionally similar boost to large chunks with close terms
- Stemmed term matching (e.g., query "authentication" matching "authenticating") works correctly via highlight markers
- Falls back to `strings.Index` if `Highlighted` field is empty (defensive)
