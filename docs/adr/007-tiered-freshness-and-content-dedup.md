# ADR-007: Tiered freshness metadata and content hash deduplication

**Status:** Accepted
**Date:** 2026-03-20
**Upstream:** context-mode had no freshness tracking (ephemeral DB). TTL cache added in v1.0.29+ but only for `fetch_and_index`.

## Context

With a persistent knowledge base (ADR-006), we need mechanisms to handle staleness and avoid redundant re-indexing. context-mode didn't need these because the DB was ephemeral.

## Decision

Every source tracks `last_accessed_at`, `access_count`, and `content_hash` (SHA-256). Sources are classified into tiers:

| Tier | Criteria | Behavior |
|------|----------|----------|
| Hot | Accessed within 7 days | Normal ranking |
| Warm | Accessed within 30 days | Normal ranking |
| Cold | Not accessed for 30+ days | Candidate for pruning |

Content deduplication: when re-indexing a source with the same label, compare SHA-256 hashes. Same hash = skip (update access time only). Different hash = delete old chunks, re-index.

Freshness does NOT affect search ranking — BM25 scores remain pure relevance.

## Rationale

- Avoids re-indexing identical content (common in `batch_execute` loops)
- Provides data for intelligent cleanup without deleting useful content
- Pruning is never automatic — only via explicit `capy_cleanup` call
- Tiers are informational (displayed in `capy_stats`) and used by cleanup policy, not search ranking

## Consequences

- Schema includes `access_count`, `last_accessed_at`, `content_hash` columns
- Every search hit updates access metadata (synchronous, not goroutine — see ADR-004)
- Cleanup policy is conservative: only prunes sources with `access_count = 0` AND cold tier (see ADR-011)
