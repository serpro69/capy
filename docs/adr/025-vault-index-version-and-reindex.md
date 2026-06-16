# ADR-025: `index_version` and DB-driven reindex for the vault FTS index

**Status:** Accepted
**Date:** 2026-06-16

## Context

The vault keeps two derived views of an archived session: the **display** path
(`capy vault show`, the TUI viewer) re-parses the verbatim `raw_jsonl` blob on
every read, while the **search** path (FTS5 index rows) is *persisted* at import
time. When the indexer logic changes — the triggering case was the
[vault-tool-entries](../wip/vault-tool-entries/) feature, which began tagging each
`tool_result` row with the tool call that produced it — the display path updates
for free, but every already-archived session's FTS rows go stale.

Re-importing does not fix this: `vault import` is idempotent and skips a session
whose content hash is unchanged (ADR-era "larger-total-size wins" merge logic).
And the obvious automatic fixes are each unacceptable for capy:

- A **migration backfill on DB-open** would re-scan the entire vault synchronously
  on the first open after upgrade. For a multi-thousand-session vault that stalls
  the MCP server's startup handshake.
- A **background goroutine sweep** on server start adds lifecycle and concurrency
  complexity that fights capy's strict WAL-checkpoint-on-close invariant (ADR-016)
  and its fail-loud ethos (a silent background index mutation is hard to reason
  about).

We need a way to upgrade existing vaults' search index that is off the server hot
path, observable, and reaches sessions that were archived and then deleted from
disk (which `import` can never see again).

## Decision

### D1: `index_version` is the per-session, DB-driven source of truth

`vault_sessions` carries an `index_version INTEGER`. A code constant
`currentIndexVersion` (now `2`) stamps the indexer logic; bump it whenever
`scanner.go` extraction changes in a way that should re-index existing sessions.
Fresh inserts stamp `currentIndexVersion` **explicitly** (not via column DEFAULT),
so a vault migrated with `DEFAULT 1` still records new inserts as current.

### D2: `import` opportunistically upgrades on-disk sessions

The skip predicate skips a found session only when `hash == existing_hash AND
existing_index_version >= currentIndexVersion`. A hash-identical but
version-stale session is re-scanned and replaced (its `mainBytes` are already in
memory), upgrading it for free during a normal import. The pre-existing
"smaller divergent variant" skip is preserved unchanged.

### D3: `capy vault reindex` is an explicit, DB-driven command

`reindex` re-scans `raw_jsonl` + sidecars **from the stored blobs** (no disk
dependency) for every session below `currentIndexVersion`, rebuilds its FTS rows,
and bumps the version. It is a CLI command, **not** a background worker — off the
server hot path, observable progress, no goroutine/lifecycle complexity. This is
the only path that reaches sessions archived then deleted from disk.

### D4: `UpdateSessionFTS` rewrites only the index, never blobs

Reindex writes via `UpdateSessionFTS`, which deletes + reinserts the session's FTS
rows and bumps `index_version` in one transaction — it does **not** rewrite
`raw_jsonl` or any sidecar blob (unlike `ReplaceSession`). On a full-vault reindex
this avoids massive WAL bloat / write amplification for zero benefit (the stored
content is unchanged). It bumps the version first and uses `RowsAffected() == 0` as
an existence check, bailing before inserting FTS rows that a concurrent
`DeleteSession` would otherwise orphan (`vault_fts` has no foreign key).

### D5: A name-keyed migration runner; `0001`/`0002` reserved for v2

The change stands up the vault migration runner that `migrations.go` previously
lacked (`vaultMigrationApplied` + apply-loop + `columnExists`, mirroring
`internal/store/migrate.go`), with `0003_add_index_version` as its first
migration. Migrations are name-keyed and idempotent, so application order does not
matter; `0001`/`0002` are deliberately left unused for pending vault v2
(`0001_blob_encoding`, `0002_vault_snapshots`). The runner's applied-check uses a
read-only fast-path so it does not acquire the write lock on every `getDB()` open.

## Consequences

- Any future indexer change is a one-line `currentIndexVersion` bump plus a
  `capy vault reindex` run; the mechanism does not need to be re-litigated.
- Reindex is manual. A user who upgrades and never runs `reindex` (and never
  re-imports the affected sessions) keeps a stale-but-functional search index —
  the result text is still indexed, only the new enrichment is missing. This is an
  accepted graceful degradation, not a correctness bug.
- The migration runner is now shared infrastructure: vault v2 adds its migrations
  to the same apply-loop rather than building its own.

## Deferred (anchored here)

**Generic input rendering for arbitrary/MCP tools.** Tool-result enrichment
currently shows the tool *name* for every tool but *inputs* only for the common
tools (Bash/Read/Edit/Write/Agent/Task), reusing `toolUseSummary`. Rendering inputs
for arbitrary/MCP tools (e.g. `mcp__capy__capy_search` queries) was deferred for
**consistency** (the result label stays identical to the assistant-side call label)
and to avoid **FTS noise/size** (free-form input JSON is low-signal and would
degrade BM25 ranking). It is anchored to this ADR precisely because adding it is a
`currentIndexVersion` bump + reindex — the machinery decided here. Full rationale
and the concrete next step: [vault-tool-entries/design.md §Deferred](../wip/vault-tool-entries/design.md).

## Alternatives considered

### Migration backfill on DB-open (rejected)
Re-scans the whole vault synchronously on first open after upgrade. Stalls MCP
server startup on large vaults. Reindex from stored blobs is the same work, moved
to an explicit, cancellable command.

### Background goroutine sweep on server start (rejected)
Avoids manual invocation but adds goroutine lifecycle (cancellation on shutdown
without blocking the WAL checkpoint), concurrency contention with the import sweep,
and a silent index mutation that fights the fail-loud ethos. Not worth it for a
one-time-per-version upgrade.

### Pure import-gate, no reindex command (rejected)
Gating only inside `import` is simpler but `import` only processes sessions still
present on disk. A session archived and then deleted locally would be permanently
stuck on a stale index. The DB-driven `reindex` command closes that gap.
(Second opinion via pal/gemini converged on this same conclusion.)
