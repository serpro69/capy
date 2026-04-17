# ADR-017: Separate ephemeral and durable sources in the knowledge store

**Status:** Proposed
**Date:** 2026-04-15
**Supersedes:** Amends (but does not replace) ADR-006, ADR-007, ADR-011.

## Context

capy is pitched as solving two problems: context-flood protection (§ADR-006) and persistent queryable memory (§ADR-007). These two jobs have opposite lifecycle requirements:

| Role             | Wants                                                 | Examples                                   |
| ---------------- | ----------------------------------------------------- | ------------------------------------------ |
| Flood protection | Short-lived, aggressive cleanup, "snapshot" semantics | `git diff`, `find`, `ls`, API debug output |
| Long-term memory | Durable, accumulative, "knowledge" semantics          | fetched docs, curated indexed files        |

Both land in the same SQLite FTS5 store, searchable by the same `capy_search`, ranked by the same BM25, with only a free-form `label` string to separate them. The current code does not encode provenance or intended lifecycle anywhere in the schema.

### Evidence the conflation bites in practice

- `internal/store/schema.go:9-19` — the `sources` table has no source-kind column; `content_type` is just `plaintext|markdown|json` (see ADR-012), not a lifecycle hint.
- `internal/server/intent_search.go:17` — every `capy_execute`/`capy_execute_file` call with an `intent` parameter and >5 KB output auto-indexes the raw output into the persistent store. This is the dominant write path during normal sessions; ephemeral pollution is steady-state, not theoretical.
- `internal/store/cleanup.go:24-61` — `retentionScore` rewards code-density (pure code ⇒ `salience = 0.7`, same as a curated source file) and `cleanup.go:112` refuses to evict anything with `access_count > 0`. One intent-search hit makes an ephemeral row effectively immortal.
- `internal/server/tool_search.go:85` — search only filters by `Source` string. A `batch:git_diff,find,ls` chunk competes head-to-head against `https://react.dev/...` chunks in the same BM25 space.
- Labeling is inconsistent across write sites: `tool_execute.go:82` (`execute:<lang>`), `tool_execute_file.go:54` (`file:<path>`), `tool_batch.go:91` (`batch:<labels>`), `tool_fetch.go:48` (no prefix — raw URL or user string), `tool_index.go:48` (no prefix — user string or `indexed-content`). Ephemeral sources get prefixes; durable sources do not. Worse than no convention.

### Why BM25 pollution is worse than it looks

SQLite FTS5's BM25 relies on term frequency and inverse document frequency. A 10k-line `git diff` chunked into the index has very varied vocabulary and can mention a term like `authentication` dozens of times across modified files — inflating TF relative to a concise prose doc that mentions it twice. Even when the prose doc ranks first, having an ephemeral diff at position 2 or 3 still burns LLM context on noise. The cost compounds.

## Decision

Introduce a **binary source-kind column** on the `sources` table:

- `kind = 'ephemeral'` — transient command output captured for flood protection. Default-excluded from search. Cleaned up on a strict TTL (24h, configurable) regardless of `access_count`.
- `kind = 'durable'` — curated/fetched content intended as long-term memory. Default-included in search. Cleaned up by the existing retention-score policy (ADR-011).

Write sites are responsible for tagging at creation:

| Write site                                                                     | `kind`      |
| ------------------------------------------------------------------------------ | ----------- |
| `tool_execute.go` (via `handleIntentOrReturn` → `intent_search.go`)            | `ephemeral` |
| `tool_execute_file.go` (via `handleIntentOrReturnWithSource`)                  | `ephemeral` |
| `tool_batch.go` (combined batch output)                                        | `ephemeral` |
| `tool_fetch.go` (HTTP-fetched content)                                         | `durable`   |
| `tool_index.go` (explicit user-authored or file-sourced index)                 | `durable`   |

Search (`tool_search.go`) default-excludes `ephemeral` unless the caller sets `include_kinds: ["ephemeral"]` (or `["durable","ephemeral"]`) OR passes an explicit `source:` filter that resolves to an ephemeral label. The array-shaped `include_kinds` is chosen over a `bool` so a future third kind can be added without a breaking change (see §Variants Considered #1). Intra-batch search (`tool_batch.go:124`) already uses `SourceMatchMode: "exact"` — this path is unaffected.

Cleanup (`cleanup.go`) splits into two code paths:

1. `durable` sources — unchanged. `retentionScore`, `access_count = 0` gate, `evictableThreshold`.
2. `ephemeral` sources — pure TTL. `IndexedAt < now − TTL`. Access count is ignored. Default TTL 24 h, overridable via `.capy.toml`.

Migration is one-shot on store open: `ALTER TABLE sources ADD COLUMN kind TEXT NOT NULL DEFAULT 'durable'`, then a retroactive `UPDATE` tagging rows whose label matches `execute:%`, `file:%`, `batch:%` as `ephemeral`. Existing durable rows stay durable by the default, matching the safety bias of ADR-011.

## Variants Considered

### 1. Three-kind taxonomy (`ephemeral | reference | note`)

Originally proposed during the analysis phase to distinguish fetched content (`reference`) from agent-authored summaries (`note`), anticipating a future authoring workflow (e.g., `capy_note` tool).

**Dropped.** YAGNI — no authoring-specific tool exists, no cleanup policy differs between `reference` and `note`, and no search default differs between them. A third kind introduced today would be indistinguishable from `durable` in every code path. If an authoring workflow emerges with a distinct lifecycle, a third value can be added then. Binary is cheaper to reason about and to migrate.

### 2. Second FTS5 virtual table for ephemeral content

Create `chunks_ephemeral` as a separate FTS5 table alongside `chunks` and `chunks_trigram`, then `UNION` at search time with a score penalty on ephemeral hits.

**Dropped.** FTS5 BM25 scores are computed relative to the statistics of a single virtual table — IDF depends on the per-table corpus. A score of `2.5` in one table is not comparable to `2.5` in another. Merging and ranking across tables would require reimplementing global BM25 in application code, giving up the main reason we use FTS5 in the first place. The schema-column approach achieves the same result with a trivial `JOIN … WHERE kind = 'durable'` and preserves single-corpus BM25 semantics.

### 3. Steeper temporal decay (`λ ≈ 0.3`) for ephemeral sources

Keep the unified retention formula, but apply a much steeper `λ` (≈2-day half-life) to `ephemeral`.

**Dropped.** This still fights the `access_count > 0` immortality gate in `cleanup.go:112`. An ephemeral row hit once by intent-search would still survive even with a steep decay, because `Cleanup()` short-circuits on `AccessCount > 0`. Strict TTL bypasses that gate cleanly and is simpler to reason about: "ephemeral output older than N hours is gone." No probabilistic scoring needed for content whose useful life is measured in minutes to hours.

### 4. Pure label-namespace convention (no schema change)

Enforce prefix conventions (`execute:`, `batch:`, `file:`, `docs:`) everywhere and make `capy_search` apply a prefix-based filter.

**Dropped.** The label is already overloaded (filter key + staleness hint + provenance + cleanup target — see the user-authored critique in the source of this ADR). Encoding lifecycle in a free-form string keeps all four concerns coupled and leaves the convention re-enforceable only by reviewing every write site. A schema column makes the contract machine-checkable and lets labels go back to being human-readable identifiers.

### 5. Skip the FTS5 index entirely for ephemeral content

Store ephemeral output in a separate plain table (no FTS) and scan linearly when needed.

**Dropped.** `tool_batch.go:124` intra-batch search *does* need FTS5 (`SearchWithFallback` with `SourceMatchMode: "exact"`). Removing ephemeral from the index would require either reimplementing search for that table or giving up the batch-execute UX. The cost of keeping ephemeral in FTS5 (index churn on 24h-TTL rows) is bounded and acceptable for a local CLI — SQLite's WAL + periodic `VACUUM` (ADR-016) handle it.

### 6. Aggressive cleanup flag on the existing unified policy

Add `capy_cleanup --aggressive` that ignores `access_count` and applies a shorter age threshold, keeping the unified policy otherwise.

**Dropped as primary fix; may still ship as a manual escape hatch.** Aggressive cleanup requires the user to notice that pollution has accumulated and run the command. The design goal is that ephemeral data never becomes visible to default search in the first place. A manual flag is a reactive workaround, not a structural fix. (A `capy_cleanup --purge-ephemeral` convenience flag may still be useful operationally — see the design doc.)

## Rationale

- The schema column is the minimum change that encodes lifecycle as a machine-readable fact rather than a labeling convention. Everything else — search filter, cleanup split, stats split — follows trivially from having the column.
- Binary is enough for today and leaves room to add values later.
- Default-exclude-ephemeral-from-search matches the mental model users already have: they index docs to search them; they run commands to extract a specific summary now.
- TTL-based cleanup for ephemeral matches the mental model of a scratch cache.
- The migration is safe because existing rows default to `durable`, and the prefix-based retroactive `UPDATE` only downgrades rows that follow capy's own labeling convention.

## Consequences

**Positive**

- `capy_search` returns signal over noise by default. Prose queries like "authentication flow" no longer compete with `git diff` hunks.
- Cleanup actually does its job. An intent-search on a 50 KB debug dump no longer produces a permanent KB resident.
- The `kind` column becomes a natural extension point for future lifecycle variants (manual notes, imported reference collections, etc.).
- `capy_stats` can report tier distribution split by kind, giving users an honest picture of what's in their KB.

**Negative / trade-offs**

- Schema change requires a one-shot migration. Mitigated by `ALTER TABLE … DEFAULT 'durable'` + retroactive `UPDATE` based on existing prefix convention — no user action required.
- Write-site tagging must be consistent going forward. Mitigated by making `kind` an argument to the `Index` method (not inferred from label), so any new write site is forced to decide.
- The intent-search path writes ephemeral data into the persistent DB, which feels wasteful but preserves the intra-session re-query use case (see design.md §Open question 1).
- Users who explicitly relied on ephemeral output surviving long-term (unlikely, but possible) lose that behavior. Mitigated by exposing `include_kinds` on `capy_search` and a configurable TTL.

**Cross-reference**

- ADR-006 (persistence) — unchanged.
- ADR-007 (tiered freshness + content-hash dedup) — `retentionScore` now applies only to `durable`; dedup still applies to both kinds.
- ADR-011 (conservative cleanup) — preserved for `durable`; explicitly *not* applied to `ephemeral`.
- ADR-012 (content-type internal only) — `kind` is *also* internal, following the same convention.
- ADR-016 (WAL + checkpoint) — index churn from 24 h TTL-eviction is expected to be handled by the existing WAL checkpoint cadence; no new knob required.

## Release notes

User-facing bullets for the release that ships this ADR:

- **Behavior change — `capy_search` default**: ephemeral sources (command output auto-indexed by `capy_execute`, `capy_execute_file`, and `capy_batch_execute`) are now excluded from search results by default. Prose queries no longer compete against `git diff` / `find` / `ls` output in BM25.
- **Recovery paths for intra-session re-query**: to include ephemeral rows, pass either
  - `include_kinds: ["durable","ephemeral"]` (both) or `include_kinds: ["ephemeral"]` (ephemeral only), **or**
  - an explicit `source:` filter naming the ephemeral label prefix, e.g. `source: "execute:shell"`, `source: "file:/abs/path"`, or `source: "batch:git_diff,find"`. An explicit `source:` bypasses kind filtering.

  The zero-results message from `capy_search` names both recovery paths when an ephemeral match exists but is excluded.
- **Automatic ephemeral TTL**: ephemeral sources are now purged 24 hours after indexing (configurable via `[store.cleanup] ephemeral_ttl_hours`; minimum 1). `access_count` is ignored — intent-search hits no longer extend ephemeral lifetime.
- **New `capy_cleanup purge_ephemeral` flag**: one-shot scratch clear that runs only the ephemeral TTL path and leaves durable rows untouched regardless of retention score.
- **Migration**: existing databases are migrated in place on first open. Rows with `execute:` / `file:` / `batch:` label prefixes are retroactively tagged `ephemeral`; all other rows stay `durable`. No user action required.
- **Stats**: `capy_stats` now reports a durable vs. ephemeral source breakdown, durable retention tiers (hot/warm/cold/evictable), and ephemeral TTL buckets (fresh/stale).
