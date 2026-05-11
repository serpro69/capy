# ADR-022: Source-size guard and DB bloat prevention

**Status:** Accepted
**Date:** 2026-05-11
**Amends:** ADR-011 (conservative cleanup policy), ADR-017 (source-kind separation)
**Issue:** [#43](https://github.com/serpro69/capy/issues/43)

## Context

`knowledge.db` grew to 88 MB with only 125 sources / 1572 chunks. Investigation revealed the bloat came from three compounding factors:

1. **No content-size guard on `store.Index`/`store.IndexChunked`.** The `content` parameter on `capy_index` and auto-index paths from `capy_execute`/`capy_batch_execute` accepted unbounded input. A single 10 MB source made it through.
2. **Trigram FTS index explosion.** FTS5 trigram tokenization has superlinear index growth. A 12 MB source produced ~42 MB of trigram index data.
3. **Freelist accumulation.** Deleted rows leave dead pages that SQLite never returns without an explicit `VACUUM`.

The per-chunk size (`MaxChunkBytes = 4096`) was fine — the problem was no cap on **total source size**, so a single source could produce 2500+ chunks, each individually reasonable but collectively devastating to the trigram index.

### Entry points and their guards (before this change)

| Entry point | Size guard | Applies to |
|---|---|---|
| `capy_index` (path param) | 10 MB file check | file reads only |
| `capy_index` (content param) | **none** | inline content from LLM |
| `capy_fetch_and_index` | 10 MB body limit | HTTP responses |
| `capy_execute` / `capy_batch_execute` | **none** | auto-indexed command output |
| `IndexChunked` (session sweep) | **none** | conversation transcripts |

## Decision

### 1. Single size gate at the store layer

Add a `MaxSourceBytes` check in `store.Index()` and `store.IndexChunked()` — the two entry points through which all content reaches the FTS5 tables. This catches every caller (MCP tools, CLI commands, session sweep) without per-handler whack-a-mole.

**Default:** 2 MB. Configurable via `store.max_source_bytes` in `.capy.toml`.

**Behavior:** Hard reject with a typed `SourceTooLargeError` that includes the content size, the limit, and a pointer to the config key. Not truncation — silent truncation would make search lie about having indexed the full source.

### 2. No trigram skip (deferred)

The issue proposed a second threshold (`TrigramSkipBytes = 512 KB`) where sources above it get porter-only indexing (no trigram). We dropped this from the initial fix because:

- The 2 MB cap already bounds the worst-case trigram growth per source to ~8 MB — manageable.
- Two thresholds add complexity: partial indexing states, cleanup handling mixed porter/trigram sources, size-dependent search behavior.
- The aggregate scenario (many 1.5 MB sources) is theoretical at current usage patterns.

If aggregate trigram growth becomes a real problem, the skip can be added later with usage data to justify the complexity.

### 3. Retroactive cleanup of oversized sources

`Cleanup()` now runs an oversized-eviction pass **before** the existing retention-score and TTL passes. Sources whose total content size (summed across chunks) exceeds `MaxSourceBytes` are flagged as evictable with reason `"oversized"`, regardless of retention score, access count, or kind.

This is implemented in cleanup rather than as a migration because:
- Migrations should be schema-only; silently deleting user-indexed content on startup is too aggressive.
- Cleanup is the "reduce DB size" path — users expect it to remove things.
- Cleanup supports `--dry-run` (default), so users see what would happen before committing.

### 4. Manual source eviction (`cleanup --source`)

`EvictByLabel(label, dryRun)` force-evicts a specific source by exact label match, regardless of retention score, TTL, or access count. Exposed via both the MCP `capy_cleanup` tool (`source` parameter) and the CLI (`capy cleanup --source <label>`).

### 5. Auto-VACUUM after cleanup

After a non-dry-run cleanup that evicts at least one source, `Cleanup()` checks the freelist ratio. If freelist pages exceed 20% of total pages, it runs `VACUUM` automatically. With SQLCipher, VACUUM re-encrypts the entire DB, so it's heavier than plain SQLite — the 20% threshold avoids triggering it on small evictions.

An explicit `capy cleanup --vacuum` flag is also available for manual use.

### 6. `capy dbsize` diagnostic subcommand

New CLI command showing table-level page counts, content-by-kind breakdown, top 15 sources by content size, freelist stats, and vocabulary size. Essential for diagnosing bloat and verifying cleanup results.

## Variants Considered

### Trigram skip at 512 KB

Skip `chunks_trigram` insertion for sources between 512 KB and 2 MB. This would turn index growth from superlinear to linear for large sources.

**Deferred.** Adds implementation complexity (two thresholds, partial index states, cleanup must handle mixed porter/trigram) for a scenario the 2 MB cap already makes unlikely. Can be revisited with data.

### Truncation instead of rejection

Silently truncate oversized content to the limit instead of rejecting.

**Rejected.** Truncation makes search lie — a user who indexes a 5 MB document and searches for content in the last 3 MB would get no results with no explanation why. Hard rejection with a clear error message is more honest and actionable.

### Per-handler size guards

Add size checks in each tool handler (`handleIndex`, `handleBatchExecute`, `handleFetchAndIndex`, etc.) instead of at the store layer.

**Rejected.** Five write sites today, more possible in the future. A store-level gate is a single chokepoint that catches every caller including future ones. Per-handler guards are defense-in-depth only — `tool_index.go` retains its file-size early-out to avoid reading a large file into memory just to have `store.Index` reject it.

### Migration-based cleanup of existing oversized sources

Run a one-shot migration on DB open that deletes sources exceeding the new limit.

**Rejected.** Migrations should be schema-only. Silently deleting user-indexed content on startup without visibility or consent is too aggressive. Cleanup with `--dry-run` default gives users control.

## Consequences

**Positive**

- DB bloat is bounded: no single source can produce more than ~8 MB of trigram index (down from unbounded).
- Existing bloated databases are fixed by the next `capy cleanup --force` or periodic cleanup run.
- Users can diagnose DB size issues with `capy dbsize` and surgically remove specific sources with `cleanup --source`.
- Dead pages are reclaimed automatically when significant.

**Negative / trade-offs**

- Content > 2 MB is now rejected. Users must pre-filter or split large documents. The error message explains the limit and points to the config key.
- `tool_index.go` file-read guard drops from 10 MB to 2 MB, matching the store-level cap. Users who previously indexed large files via path will now get a rejection.
- Auto-VACUUM after cleanup adds latency (SQLCipher re-encryption) — bounded by the 20% threshold.

**Cross-reference**

- ADR-006 (persistence) — unchanged.
- ADR-011 (conservative cleanup) — **amended**: cleanup now also evicts oversized sources regardless of access count, and auto-vacuums when freelist > 20%.
- ADR-016 (WAL + checkpoint) — VACUUM interacts with WAL; the dedicated single-connection pattern from Checkpoint is reused.
- ADR-017 (source-kind separation) — oversized eviction runs across all kinds, before kind-specific passes.

## Configuration

```toml
[store]
max_source_bytes = 2097152  # 2 MB, default
```

## CLI changes

```
capy cleanup --source <label>   # evict a specific source by exact label
capy cleanup --vacuum           # run VACUUM to reclaim dead pages
capy dbsize                     # show disk usage breakdown
```

## MCP changes

`capy_cleanup` tool gains a `source` string parameter for source-specific eviction.
