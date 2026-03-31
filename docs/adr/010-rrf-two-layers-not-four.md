# ADR-010: RRF uses 2 search layers, not 4

**Status:** Accepted
**Date:** 2026-03-31
**Upstream:** context-mode runs porter OR + trigram OR (2 layers) for RRF. The old cascading fallback ran porter+AND, porter+OR, trigram+AND, trigram+OR (4 layers).

## Context

The original capy port used the old 8-layer cascading fallback (4 layers × 2 passes with fuzzy correction). The upstream sync replaces this with Reciprocal Rank Fusion (RRF).

When porting RRF, the question was whether to run all 4 layer variants (AND + OR for both porter and trigram) or just 2 (OR only for both).

## Decision

Run 2 layers: porter OR and trigram OR. Do not run AND variants.

## Rationale

- With RRF handling ranking via score fusion, the AND/OR distinction is unnecessary
- OR mode returns a superset of AND mode — every AND result also appears in OR results
- RRF's `1/(K+rank)` scoring naturally promotes results that appear at high positions across multiple layers — the precision that AND mode provided is achieved by the fusion itself
- Running 2 queries instead of 4 is faster and simpler
- Both layers run concurrently via goroutines (Go improvement over TS's sequential execution)

## Consequences

- `searchPorter` and `searchTrigramQuery` always use OR mode
- `sanitizeQuery` and `sanitizeTrigramQuery` no longer need to support AND mode (but kept for backward compatibility)
- Half the queries per search call compared to a naive 4-layer RRF
