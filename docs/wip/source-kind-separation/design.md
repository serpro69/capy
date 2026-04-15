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

- A third source kind for authored notes. Out of scope until an authoring workflow exists (see ADR-017 §Alternatives #1).
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

Add an index on `kind` to keep the `WHERE kind = 'durable'` filter fast as row counts grow. Composite index with `indexed_at` optional — benchmark first; the default SQLite B-tree on a 2-value column may not need one.

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
    IncludeEphemeral bool // false: search durable only (default)
}
```

Semantics at the store layer:

- If `opts.Source == ""` and `opts.IncludeEphemeral == false` → `WHERE sources.kind = 'durable'`.
- If `opts.Source != ""` (caller explicitly named a source) → no `kind` filter; trust the caller's intent (this keeps `batch_execute`'s intra-batch `SourceMatchMode: "exact"` path working).
- If `opts.IncludeEphemeral == true` → no `kind` filter.

### `capy_search` tool schema

Add an optional `include_ephemeral` boolean (default `false`). Existing callers that don't pass the parameter get the new default behavior. Document the parameter's purpose clearly so agents know when to set it.

### `capy_cleanup` tool schema

Add an optional `purge_ephemeral` boolean (default `false`). When `true`, invokes the ephemeral TTL purge regardless of `dry_run` semantics (still respected for previewing). This is mostly a debugging/manual convenience; the automatic TTL path runs on every `Cleanup()` call regardless.

### Configuration

Extend `.capy.toml` `[store]` section:

```toml
[store]
ephemeral_ttl_hours = 24  # default
```

Expose via `config.Config.Store.EphemeralTTLHours` in `internal/config/config.go`.

## Cleanup policy

`store.Cleanup(dryRun bool)` becomes a two-path function:

### Path 1: Durable sources — unchanged

Existing `retentionScore`-based eviction (ADR-011):
- `access_count = 0` AND `retentionScore < evictableThreshold (0.15)`.

### Path 2: Ephemeral sources — new

Strict TTL:
- `kind = 'ephemeral'` AND `indexed_at < datetime('now', '-N hours')` where N comes from `config.Store.EphemeralTTLHours`.
- `access_count` is **ignored** — the whole point is that intent-search hits don't extend ephemeral lifetime.
- Same transactional deletion (chunks + trigram + source row).

The return value groups pruned sources by kind so `capy_cleanup` output can show `3 ephemeral (TTL), 1 durable (retention)`.

### Scheduling

No background goroutine. Cleanup still runs only when `capy_cleanup` is invoked or when the user runs the CLI. The ADR-011 "no automatic cleanup" principle is preserved. A future enhancement could trigger ephemeral TTL eviction lazily during each `Index` call; out of scope here.

## Search-path details

### `store.SearchWithFallback`

The current implementation in `internal/store/search.go:24` delegates to `rrfSearch` and `execDynamicSearch`. Both paths accept `SearchOptions`. The filter is applied at the SQL layer:

- Porter and trigram FTS5 SELECTs need a `JOIN sources s ON chunks.source_id = s.id` (already present) with an added `AND s.kind = 'durable'` clause conditional on the options.
- The fuzzy Levenshtein fallback path also needs the filter — it operates on the `sources` table directly via vocabulary expansion.

**Performance note:** FTS5 MATCH is the primary filter; adding a `s.kind = 'durable'` predicate runs after the match. On a corpus dominated by ephemeral rows, this could waste work — FTS5 ranks N hits, then we discard most of them. If this becomes measurable, we can push the filter into a `content_rowid` pre-filter via a companion table. Not doing this upfront; benchmark first.

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

`store.StoreStats` gains per-kind tier breakdowns:

```go
type StoreStats struct {
    // existing fields (SourceCount, ChunkCount, etc.)

    DurableSourceCount   int
    EphemeralSourceCount int

    // Tier counts remain overall, plus:
    DurableHotCount       int
    DurableWarmCount      int
    DurableColdCount      int
    DurableEvictableCount int

    EphemeralCount        int // = EphemeralSourceCount, expressed for clarity
    EphemeralStaleCount   int // ephemeral rows past TTL but not yet cleaned
}
```

`classifyTier` only applies to `durable` sources — running temporal decay math on rows that live 24 h is noise.

`capy_stats` output gains a new table section showing durable vs ephemeral counts and how much of the DB size comes from each. The existing tier-distribution section becomes durable-only.

## Security and edge cases

- **Migration idempotency:** detect existing `kind` column via `PRAGMA table_info`; skip the `ALTER TABLE` if present. Run the retroactive `UPDATE` only on the migration turn (write a migration marker row into a new `_capy_migrations` table — minimal, one row per migration).
- **Content-hash dedup:** unchanged. Re-indexing the same content with the same label updates `last_accessed_at`. If the re-indexed `kind` differs from the stored `kind`, prefer the new `kind` (callers are expected to be consistent; changing kind is a deliberate promotion/demotion).
- **Concurrent writes:** `Index` already uses `BEGIN IMMEDIATE` via a dummy `DELETE`. No new locking concerns.
- **TTL clock skew:** uses `datetime('now')` in SQLite so no local time-zone confusion. Tests must not mock `time.Now()` at the store layer for this reason.

## Open questions

1. **Should intent-search bypass the persistent store entirely and use an in-memory SQLite attached DB?** Current answer: no — the session-scoped re-query use case requires FTS5 and rebuilding the index on every call is wasteful. Ephemeral writes to disk with TTL eviction is the simpler path. Revisit if TTL churn becomes a WAL-size problem.
2. **Should `capy_cleanup` output explain *why* each row was evicted (TTL vs retention)?** Proposed: yes, add a `reason` column to the dry-run preview table.
3. **Default TTL of 24h — is this right?** Seems right for the dominant use case (a session lasts hours, not days). Users who want stricter can set `ephemeral_ttl_hours = 1`; users who want lenient can set `72`. Revisit based on telemetry from the `capy_stats` `EphemeralStaleCount` field.
4. **Should re-indexing the same content with `kind = 'durable'` promote an existing ephemeral row?** Proposed: yes. Promotion is the honest case (the user explicitly ran `capy_index` on content they consider reference-grade).

## Testing strategy

- **Unit tests** in `internal/store/`:
  - `Index` with each kind stores the correct value.
  - `SearchWithFallback` with `IncludeEphemeral = false` (default) excludes ephemeral rows.
  - `SearchWithFallback` with explicit `Source` returns ephemeral rows even when `IncludeEphemeral = false`.
  - `Cleanup` on a mixed DB prunes ephemeral past TTL and leaves durable retention logic untouched.
  - Kind-promotion: re-indexing an ephemeral label with `kind = 'durable'` promotes the row.
- **Migration tests** in `internal/store/store_test.go`:
  - Open an in-memory DB with a pre-ALTER schema; verify migration adds the column and tags by prefix.
  - Open a post-ALTER DB; verify migration is a no-op.
- **Integration tests** in `internal/server/`:
  - Run `capy_execute` with an intent → verify a `kind = 'ephemeral'` row exists.
  - Run `capy_fetch_and_index` → verify `kind = 'durable'`.
  - Run `capy_search` on a mixed corpus → verify ephemeral rows are not in results.
  - Pass `include_ephemeral: true` → verify ephemeral rows appear.
  - Pass `source: "execute:shell"` → verify ephemeral rows appear (explicit source override).

## Rollout

Single PR. Feature flag not required — the default-durable migration is safe, and the only behavioral change visible to users is that `capy_search` no longer returns stale command-output hits, which is the intended improvement.

Release note should explicitly call out:

- `capy_search` now excludes ephemeral sources by default. Pass `include_ephemeral: true` or an explicit `source:` filter to query them.
- Ephemeral sources are purged automatically after 24 hours (configurable via `ephemeral_ttl_hours`).
- Existing databases are migrated in place.
