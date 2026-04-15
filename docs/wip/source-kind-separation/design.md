# Design: Source-Kind Separation for Persistent Knowledge Store

**Status:** Draft
**Date:** 2026-04-15
**ADR:** [../../adr/017-source-kind-separation.md](../../adr/017-source-kind-separation.md)
**Implementation plan:** [./implementation.md](./implementation.md)
**Tasks:** [./tasks.md](./tasks.md)

## Overview

`capy`'s persistent knowledge store currently mixes two lifecycles in a single SQLite FTS5 corpus:

1. **Ephemeral command output** — captured by `capy_execute`, `capy_execute_file`, `capy_batch_execute` for flood protection. Useful for seconds to minutes.
2. **Durable reference content** — fetched docs, user-indexed material. Useful for days to months.

The only mechanism separating them today is a free-form `label` string, and the labeling convention is inconsistent (`execute:`/`file:`/`batch:` prefixes exist; `fetch` and `index` use raw user-supplied labels). This design introduces a machine-readable `kind` column and wires it through indexing, search, cleanup, and stats. See the accompanying ADR for the full history of considered alternatives.

## Goals

- Make `capy_search` return signal over noise by default: prose queries should not compete against code-dense ephemeral output.
- Make cleanup match the lifecycle of each piece of content: retention scoring for durable, TTL for ephemeral.
- Preserve intra-session re-queryability of ephemeral output (the whole point of auto-indexing command output).
- Keep the MCP tool API surface backward-compatible: existing callers of `capy_search` and `capy_index` keep working without changes; the only new arguments are opt-in.
- Migrate existing knowledge bases without user intervention.

## Non-goals

- A third source kind for authored notes. Out of scope until an authoring workflow exists (see ADR-017 §Variants Considered #1). The `IncludeKinds` array leaves room to add one later without a breaking change.
- Cross-machine / team-shared KBs. Still deferred to the session continuity feature (ADR-015).
- Changing BM25 weights, synonym expansion, or trigram fallback. Unrelated to lifecycle.
- Automating when durable vs ephemeral should be used. Write sites decide at compile time; callers do not choose.

## Data model

### Schema change

Add one column to the `sources` table:

```
kind TEXT NOT NULL DEFAULT 'durable'
  CHECK (kind IN ('ephemeral', 'durable'))
```

- Default is `'durable'` so the `ALTER TABLE` is safe on existing databases and any new write site that forgets to pass a kind fails closed to "keep forever," not "lose data."
- `CHECK` constraint guards against typos.

### Migration (one-shot, on store open)

```sql
ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable';

UPDATE sources
SET kind = 'ephemeral'
WHERE label LIKE 'execute:%'
   OR label LIKE 'file:%'
   OR label LIKE 'batch:%';
```

The retroactive `UPDATE` downgrades existing rows that followed capy's own ephemeral-label convention. Rows from `capy_fetch_and_index` and `capy_index` have no such prefix and stay `durable`. User-authored labels that happen to start with `execute:` / `file:` / `batch:` are a theoretical conflict; in practice these labels are not written by users because the prefix is reserved by the tool handlers.

The migration is idempotent: running it on a DB where `kind` already exists is a no-op (detected via `PRAGMA table_info(sources)`).

### Index strategy

Do not add a `kind` index on first cut. The column has 2 distinct values; SQLite's query planner will usually prefer the existing FTS5 `source_id` path followed by a filter over a low-cardinality B-tree lookup. Revisit only if `EXPLAIN QUERY PLAN` shows a full-table scan after the column exists, or if the durable-source row count grows past ~10k.

## API changes

### `store.ContentStore` (internal)

The indexing entrypoints gain a `kind` parameter. Concretely:

- Introduce `SourceKind` type (Go `string` alias with `KindEphemeral` and `KindDurable` constants) in `internal/store/types.go`.
- Extend `Index(content, label, contentType string)` → `Index(content, label, contentType string, kind SourceKind)` OR keep the current signature as a durable-default and add `IndexWithKind` — **decision: change the signature**, because leaving a defaulted entry point is exactly the error-prone path this change is trying to eliminate. Every call site must decide explicitly.
- `IndexPlainText`, `IndexJSON` — add the same parameter.

### `SearchOptions`

Add one field:

```go
type SearchOptions struct {
    // existing fields...
    IncludeKinds []SourceKind // nil / empty = default (durable only)
}
```

`IncludeKinds` is chosen over a `bool` flag deliberately: ADR-017 §Variants #1 leaves the door open for a third kind, and an array future-proofs the schema at zero cost today. Future values can be added without a breaking change to callers.

Semantics at the store layer:

- If `opts.Source == ""` and `opts.IncludeKinds` is empty → `WHERE sources.kind = 'durable'` (default).
- If `opts.Source != ""` (caller explicitly named a source) → no `kind` filter; trust the caller's intent. This keeps `batch_execute`'s intra-batch `SourceMatchMode: "exact"` path working. Note that the existing `Source` field uses partial `LIKE '%source%'` match (see `types.go`), so any non-empty source string is treated as a deliberate opt-in to everything it matches — including ephemeral rows. This is intentional: callers who want stricter scoping use `SourceMatchMode: "exact"`.
- If `opts.IncludeKinds` is non-empty → filter with `AND sources.kind IN (?, ?, …)` bound to those values.

### `capy_search` tool schema

Add an optional `include_kinds: string[]` argument (default empty → durable only). Accepts `"durable"` and/or `"ephemeral"`. Existing callers that don't pass the parameter get the new default behavior. Document the parameter's purpose clearly so agents know when to set it. Invalid values are rejected with an explicit error listing the accepted set.

### `capy_cleanup` tool schema

Add an optional `purge_ephemeral` boolean (default `false`). When `true`, invokes the ephemeral TTL purge regardless of `dry_run` semantics (still respected for previewing). This is mostly a debugging/manual convenience; the automatic TTL path runs on every `Cleanup()` call regardless.

### Configuration

`EphemeralTTLHours` is an eviction knob, so it goes under the existing `[store.cleanup]` section (alongside `ColdThresholdDays`) rather than at the bare `[store]` level. The `[store.cache]` section is reserved for fetch-cache semantics — see `CacheConfig` in `internal/config/config.go`.

```toml
[store.cleanup]
cold_threshold_days = 30    # existing
ephemeral_ttl_hours = 24    # new; default 24
```

Expose via `config.Config.Store.Cleanup.EphemeralTTLHours`. Reject values `< 1` at config load with an explicit error: `0` does not mean "purge everything" (that's what `capy_cleanup --purge-ephemeral` is for) and negative values are nonsensical. Users who want aggressive purging use the tool; users who want no TTL-based eviction can't have that — it would reintroduce the pollution problem this feature exists to fix.

## Cleanup policy

`store.Cleanup(dryRun bool)` becomes a two-path function:

### Path 1: Durable sources — unchanged

Existing `retentionScore`-based eviction (ADR-011):
- `access_count = 0` AND `retentionScore < evictableThreshold (0.15)`.

### Path 2: Ephemeral sources — new

Strict TTL:
- `kind = 'ephemeral'` AND `indexed_at < datetime('now', '-N hours')` where N comes from `config.Store.Cleanup.EphemeralTTLHours`.
- `access_count` is **ignored** — the whole point is that intent-search hits don't extend ephemeral lifetime.
- Same transactional deletion (chunks + trigram + source row).

The return value groups pruned sources by kind so `capy_cleanup` output can show `3 ephemeral (TTL), 1 durable (retention)`.

### Scheduling

No background goroutine. Cleanup still runs only when `capy_cleanup` is invoked or when the user runs the CLI. The ADR-011 "no automatic cleanup" principle is preserved. A future enhancement could trigger ephemeral TTL eviction lazily during each `Index` call; out of scope here.

## Search-path details

### `store.SearchWithFallback`

The current implementation in `internal/store/search.go:24` delegates to `rrfSearch` and `execDynamicSearch` (at `search.go:505`). Both already `JOIN sources s ON s.id = c.source_id`, so the kind filter is a single additional clause:

- Porter and trigram FTS5 SELECTs in `execDynamicSearch` gain `AND s.kind IN (…)` (or `AND s.kind = 'durable'` when `IncludeKinds` is empty) conditional on the options. This is one SQL-building branch; both layers pick it up automatically.
- Fuzzy correction (`fuzzyCorrectQuery`) operates on the `vocabulary` table to suggest spelling fixes, then re-enters `rrfSearch` with the corrected query. It inherits the kind filter via `execDynamicSearch` — no separate path needed.

Performance is bounded naturally by the 24 h TTL: steady-state ephemeral corpus size is small (hundreds to low thousands of chunks at typical usage). FTS5 MATCH + post-filter is fine at that scale; no pre-filter companion table required.

### Intra-batch search (`tool_batch.go`)

Uses `SearchOptions{Source: sourceLabel, SourceMatchMode: "exact"}`. With the new rule ("explicit source → no kind filter"), this path is unaffected and keeps working on the just-written ephemeral batch output.

### Intent search (`intent_search.go`)

After the ephemeral write, the follow-up `SearchWithFallback` uses `SearchOptions{Source: source}` — explicit source label, so again unaffected.

## Write-site audit

Every current write path, with the kind it maps to:

| File                               | Call                              | Kind        | Notes                                                                                                |
| ---------------------------------- | --------------------------------- | ----------- | ---------------------------------------------------------------------------------------------------- |
| `tool_execute.go:82`               | via `handleIntentOrReturn`        | `ephemeral` | `execute:<lang>` label preserved; only writes via `intent_search.IndexPlainText`.                    |
| `tool_execute_file.go:54,66`       | via `handleIntentOrReturnWithSource` | `ephemeral` | `file:<path>` label preserved.                                                                     |
| `tool_batch.go:92`                 | `st.Index(combinedOutput, ..., "markdown")` | `ephemeral` | `batch:<labels>` label preserved.                                                           |
| `intent_search.go:17`              | `st.IndexPlainText(output, source)` | `ephemeral` | This is the critical pollution source today. Must be tagged ephemeral.                             |
| `tool_fetch.go:132,138,141,145`    | `st.IndexJSON` / `IndexPlainText` / `Index` | `durable`   | HTML-converted, JSON, and fallback paths all durable.                                       |
| `tool_index.go:61`                 | `st.Index(content, source, "")`   | `durable`   | User-invoked indexing. Always durable.                                                              |

No other write sites. Future additions must pass `kind` explicitly; there is no default at the store API.

## Stats changes

`store.StoreStats` gains per-kind breakdowns. The existing `HotCount`/`WarmCount`/`ColdCount`/`EvictableCount` fields are **renamed** with a `Durable` prefix, not duplicated — retention scoring only runs on durable rows, so the original names would become silently misleading. Callers of these fields (e.g., `capy_stats` rendering in `internal/server/tool_stats.go`) update in lockstep.

```go
type StoreStats struct {
    // existing fields (SourceCount, ChunkCount, VocabCount, DBSizeBytes) unchanged.

    DurableSourceCount    int
    EphemeralSourceCount  int

    // Renamed from HotCount/WarmCount/ColdCount/EvictableCount —
    // retention tiers only apply to durable rows.
    DurableHotCount       int
    DurableWarmCount      int
    DurableColdCount      int
    DurableEvictableCount int

    // Ephemeral rows don't get retention scoring; they're bucketed by TTL.
    EphemeralFreshCount   int // indexed within TTL
    EphemeralStaleCount   int // past TTL, awaiting next Cleanup()
}
```

`classifyTier` only applies to `durable` sources — running temporal decay math on rows that live 24 h is noise.

`capy_stats` output gains a section showing durable-vs-ephemeral counts and how much of the DB size comes from each. The existing tier-distribution section becomes durable-only and uses the renamed fields.

## Security and edge cases

- **Migration idempotency and concurrency:** the migration runs inside `BEGIN IMMEDIATE` (SQLite acquires the reserved-write lock eagerly). Inside the transaction, re-check `PRAGMA table_info(sources)` and exit early if the `kind` column already exists. Because capy can run as multiple processes against the same DB (ADR-006), two processes opening a pre-migration DB simultaneously both try to migrate; SQLite serializes the write lock, and the second process's `PRAGMA` check sees the column and returns. The retroactive `UPDATE` is itself idempotent — re-running it on `ephemeral`-tagged rows is a no-op. No separate migrations-tracking table is required for this single migration; add one when the second migration lands.
- **Content-hash dedup with kind mismatch:** re-indexing identical content under the same label with a different `kind` must NOT force a re-chunk. The hash-match short-circuit in `internal/store/index.go:66-78` is extended to update `kind` in place: `UPDATE sources SET kind = ?, last_accessed_at = datetime('now') WHERE id = ?`. Promotion (ephemeral → durable via `capy_index`) and demotion (unlikely but possible) both route through the short-circuit without re-chunking. See also §Open questions 4.
- **Concurrent writes:** `Index` already uses `BEGIN IMMEDIATE` via a dummy `DELETE`. No new locking concerns.
- **TTL clock skew:** uses `datetime('now')` in SQLite so no local time-zone confusion. Tests must not mock `time.Now()` at the store layer for this reason.
- **Store-open ordering:** the store-initialization path must be `exec(schemaSQL) → applyMigrations(db) → prepareStatements(db)`. Prepared statements reference the `kind` column (per the Index-methods change), so preparing before migration fails on existing pre-migration DBs.

## Open questions

1. **Should intent-search bypass the persistent store entirely and use an in-memory SQLite attached DB?** Current answer: no — the session-scoped re-query use case requires FTS5 and rebuilding the index on every call is wasteful. Ephemeral writes to disk with TTL eviction is the simpler path. Revisit if TTL churn becomes a WAL-size problem.
2. **Should `capy_cleanup` output explain *why* each row was evicted (TTL vs retention)?** Proposed: yes, add a `reason` column to the dry-run preview table.
3. **Default TTL of 24h — is this right?** Seems right for the dominant use case (a session lasts hours, not days). Users who want stricter can set `ephemeral_ttl_hours = 1`; users who want lenient can set `72`. Revisit based on telemetry from the `capy_stats` `EphemeralStaleCount` field.
4. **Should re-indexing the same content with `kind = 'durable'` promote an existing ephemeral row?** Resolved: **yes**, via in-place `UPDATE sources SET kind = ?, last_accessed_at = …`. No re-chunking. Promotion is the honest case (the user explicitly ran `capy_index` on content they consider reference-grade). Wired through the existing hash-match short-circuit in `Index` — see §Security and edge cases.

## Testing strategy

- **Unit tests** in `internal/store/`:
  - `Index` with each kind stores the correct value.
  - `SearchWithFallback` with `IncludeKinds = nil` (default) excludes ephemeral rows.
  - `SearchWithFallback` with `IncludeKinds = []SourceKind{KindDurable, KindEphemeral}` returns both.
  - `SearchWithFallback` with explicit `Source` returns ephemeral rows regardless of `IncludeKinds`.
  - `Cleanup` on a mixed DB prunes ephemeral past TTL and leaves durable retention logic untouched.
  - `Cleanup` evicts an ephemeral row with `access_count = 5` once past TTL (the immortality-gate fix).
  - Kind-promotion: re-indexing an ephemeral label with identical content but `kind = KindDurable` updates the `kind` column in place and does NOT re-chunk (check `chunk_count` unchanged, no new rows in `chunks`).
- **Migration tests** in `internal/store/store_test.go`:
  - Open an in-memory DB using the pre-migration DDL (raw `CREATE TABLE sources` without the `kind` column); verify migration adds the column and tags by prefix.
  - Open a post-migration DB; verify migration is a no-op.
  - Run migration concurrently from two goroutines against one DB; assert no errors, column present, retroactive `UPDATE` applied exactly once in effect (idempotent).
- **Config tests**:
  - `ephemeral_ttl_hours = 0` is rejected at config load with a clear error referencing `purge_ephemeral`.
  - `ephemeral_ttl_hours = -1` is rejected.
- **Integration tests** in `internal/server/tool_knowledge_test.go`:
  - Run `capy_execute` with an intent → verify a `kind = 'ephemeral'` row exists in the store.
  - Run `capy_fetch_and_index` → verify `kind = 'durable'`.
  - Run `capy_search` on a mixed corpus → verify ephemeral rows are not in results by default.
  - Pass `include_kinds: ["ephemeral"]` → only ephemeral; `["durable","ephemeral"]` → both.
  - Pass `source: "execute:shell"` (no `include_kinds`) → ephemeral rows appear (explicit source override).
  - **Session-recovery journey:** call `capy_execute(code=…, intent=…)` so ephemeral content lands; then call `capy_search(query=<matching phrase>)` with no source filter → assert zero results AND the zero-results message names both recovery paths (`include_kinds: ["ephemeral"]` and explicit `source:`). Then call the same query with `include_kinds: ["ephemeral"]` → assert hit.

## Rollout

Single PR. Feature flag not required — the default-durable migration is safe, and the only behavioral change visible to users is that `capy_search` no longer returns stale command-output hits, which is the intended improvement.

Release note should explicitly call out:

- **Behavior change:** `capy_search` now excludes ephemeral sources (command output from `capy_execute` / `capy_execute_file` / `capy_batch_execute`) by default.
- **Recovery path for intra-session re-query:** pass `include_kinds: ["ephemeral"]` to include ephemeral-only, `include_kinds: ["durable","ephemeral"]` for both, or use an explicit `source: "execute:<lang>"` / `source: "file:<path>"` / `source: "batch:…"` filter. The zero-results message names these paths when an ephemeral match exists.
- Ephemeral sources are purged automatically after 24 hours (configurable via `[store.cleanup] ephemeral_ttl_hours`; minimum 1).
- Existing databases are migrated in place on first open — no user action required.
