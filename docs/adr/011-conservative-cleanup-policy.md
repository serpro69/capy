# ADR-011: Conservative cleanup policy (vs upstream's aggressive stale deletion)

**Status:** Accepted
**Date:** 2026-03-31
**Upstream:** context-mode added `cleanupStaleSources(maxAgeDays)` which deletes any source where `last_accessed_at` is older than N days, regardless of access count.

## Context

The TS upstream added aggressive cleanup: any source not accessed for N days gets deleted, even if it was accessed hundreds of times before. This made sense for an originally-ephemeral DB where aggressive culling keeps the current session clean.

capy's persistent DB (ADR-006) changes the calculus. Deleting a source that was accessed 50 times but not in the last 30 days would surprise users and force re-indexing.

## Decision

Do not port `cleanupStaleSources`. Keep the existing `Cleanup()` method which only removes sources meeting ALL of:
- `access_count = 0` (never accessed via search)
- Cold tier (not accessed for 30+ days)
- Older than `maxAgeDays`

## Rationale

- A persistent knowledge base's value comes from accumulation over time
- "I fetched React docs last month and searched them 20 times" → should NOT be deleted just because 31 days passed
- Sources with `access_count = 0` are genuinely unused — safe to prune
- If aggressive cleanup is ever needed, add an `aggressive` flag to existing `Cleanup` rather than a separate method
- DB size concerns are mitigated by tiered freshness reporting in `capy_stats` — users can see what's cold and manually clean up

## Consequences

- capy's knowledge base may grow larger than context-mode's over time
- `capy_stats` shows tier distribution so users can monitor
- `capy_cleanup --force` only removes never-accessed cold sources
- No automatic background cleanup — always explicit
