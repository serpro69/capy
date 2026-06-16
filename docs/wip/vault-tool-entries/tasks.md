# Tasks: Tool name + inputs in vault tool-result entries

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: done
> Created: 2026-06-16

**Not doing:** generic rendering of arbitrary tool inputs (only the common tools
get input detail via the existing `toolUseSummary`; all tools get the name) — see
[design.md §Deferred](./design.md) for *why* (consistency + FTS noise/size) and the
concrete next step (bounded default-case summary, bump `currentIndexVersion`, reindex);
zstd compression / `encoding` column / `vault_snapshots` / PreCompact (all remain
pending v2 work); background/automatic reindex (explicit `capy vault reindex` only).

Build/test with `-tags fts5`; `CAPY_DB_KEY` + `CAPY_VAULT_KEY` required.

## Task 1: Consolidate `beginImmediate`/`isBusy` into `sqliteutil` (== v2 Task 1)
- **Status:** done
- **Depends on:** —
- **Size:** S
- **Docs:** [implementation.md#task-1](./implementation.md)

### Subtasks
- [x] 1.1 Add exported `BeginImmediate(db *sql.DB, lockTable string)` + `IsBusy(err)` to `internal/sqliteutil/sqliteutil.go`
- [x] 1.2 Delete `beginImmediate`/`isBusy` from `internal/store/migrate.go`, delete `internal/store/retry.go`, update store call sites (`migrate.go`, `cleanup.go:343`) to `sqliteutil.BeginImmediate(db, "sources")` / `sqliteutil.IsBusy`
- [x] 1.3 Delete `beginImmediate`/`isBusy` from `internal/vault/migrations.go`, update `vault/store.go` call sites to `sqliteutil.BeginImmediate(db, "vault_meta")`
- [x] 1.4 Verify `grep -rn "func beginImmediate\|func isBusy" internal/` is empty (PASS); race green on sqliteutil+vault, store green non-race
- [x] 1.5 Tests for `sqliteutil.BeginImmediate`/`IsBusy`

## Task 2: Tool-entry enrichment (display + FTS)
- **Status:** done
- **Depends on:** —
- **Size:** M
- **Docs:** [implementation.md#task-2](./implementation.md)

### Subtasks
- [x] 2.1 `scanner.go`: build `tool_use_id → summary` map (`collectToolUseSummaries`/`prefixToolResult`); enrich `extractUserBlocks` tool-result text (FTS)
- [x] 2.2 `render.go`: `renderUserContent` gains the map param; `collectDisplay` builds + passes it (show)
- [x] 2.3 `transcript.go`: `ParseTranscript` builds + passes the map (TUI)
- [x] 2.4 scanner/render/transcript tests for the prefix + unknown-id (no prefix) case

## Task 3: `index_version` infra + import skip gate
- **Status:** done
- **Depends on:** Task 1 (uses `sqliteutil.BeginImmediate` in the migration), Task 2 (the indexer change that justifies v2)
- **Size:** M
- **Docs:** [implementation.md#task-3](./implementation.md)

### Subtasks
- [x] 3.1 `store.go`: add `index_version` to `schemaSQL` + `sessionMetaColumns`/`scanSessionMeta`; `currentIndexVersion = 2`; `Session.IndexVersion`; insert/update stmts + `writeRecord`
- [x] 3.2 `migrations.go`: `vaultMigrationApplied` guard + `columnExists` + apply-loop; `migrate0003AddIndexVersion`
- [x] 3.3 `store.go`: extend `SessionDigest` to return `index_version`
- [x] 3.4 `import.go`: stamp `IndexVersion` in `buildRecord`; gate skip predicate on hash AND version
- [x] 3.5 Tests: migration (fresh/legacy/idempotent + columnExists), import gate (version-stale → updated), digest

## Task 4: Reindex (DB-driven) + CLI command
- **Status:** done
- **Depends on:** Task 3
- **Size:** M
- **Docs:** [implementation.md#task-4](./implementation.md)

### Subtasks
- [x] 4.1 `store.go`: `stmtUpdateIndexVersion`; `UpdateSessionFTS(uuid, newVersion, fts)`; `OutdatedSessionUUIDs(maxVersion)`
- [x] 4.2 `reindex.go`: `Reindex(ctx, store)` (DB-driven re-scan via shared `scanSessionAndSubagents` + `UpdateSessionFTS`, ctx-aware, per-session errors)
- [x] 4.3 `cmd/capy/vault.go`: `newVaultReindexCmd` + register
- [x] 4.4 Tests: `UpdateSessionFTS` (FTS replaced, version bumped, blob untouched), `OutdatedSessionUUIDs`, end-to-end `Reindex` (stale→enriched), cancellation

## Task 5: Fix reindex/import hang on large vaults (post-merge regression)
- **Status:** done
- **Depends on:** Task 3, Task 4
- **Size:** M
- **Root cause:** `vault_fts.session_uuid` is `UNINDEXED`, so the per-session
  `DELETE FROM vault_fts WHERE session_uuid = ?` in `UpdateSessionFTS` /
  `ReplaceSession` is a full scan of the whole FTS index (~272 ms each over 51k
  rows on a 497 MB vault). Migration `0003` flags every pre-existing row stale, so
  `reindex` ran that delete for all sessions and `import` re-wrote every still-on-disk
  version-stale session via full `ReplaceSession` (blob rewrite + per-session FTS
  delete) — degrading both into an effective hang. Confirmed via SIGQUIT goroutine
  dump (blocked in `UpdateSessionFTS` at the `DELETE`) + measurement on the real vault.

### Subtasks
- [x] 5.1 `store.go`: `RebuildFTSBatch` — bump versions, survivor-filter (existence check), ONE `WHERE session_uuid IN (...)` delete, bulk insert; `UpdateSessionFTS` now its single-session wrapper; unexported `checkpointWAL` (`PRAGMA wal_checkpoint(TRUNCATE)`)
- [x] 5.2 `reindex.go`: batch `FTSRebuild` (`reindexBatchSessions`/`reindexBatchBytes`), flush via `RebuildFTSBatch`, checkpoint between batches; `ftsContentBytes` helper
- [x] 5.3 `import.go`: route hash-identical + version-stale sessions to an FTS-only batch (no blob rewrite — ADR-025 D2 amended); `flush` writes both the replace-batch and the FTS-batch with per-session retry
- [x] 5.4 Docs: ADR-025 D2 amendment + D4 rewrite (batched IN-delete, UNINDEXED rationale, structural fix deferred); design.md reindex/skip-gate prose
- [x] 5.5 Tests: `RebuildFTSBatch` (bulk replace, version bump, missing-session skip, blob untouched), reindex batch-boundary, import FTS-only (no blob/metadata rewrite, FTS rebuilt); validated on real 497 MB vault — reindex 489 sessions in ~39 s (was an effective hang), idempotent on re-run

**Deferred (structural):** make the delete-by-session indexable (external-content
FTS5 over a real content table, or a rowid↔session_uuid map) so `ReplaceSession`/
`DeleteSession` also avoid the full scan. Not done here: it is a schema change with
cross-machine vault-copy compatibility implications. The batched bulk-delete fixes
the hang within the existing `UNINDEXED` schema. See ADR-025 D4.

## Task 6: Exclude Read/NotebookRead result bodies + surface index version in stats
- **Status:** done
- **Depends on:** Task 2 (the three-parser correlation), Task 3/4 (index_version + reindex)
- **Size:** M
- **Decision:** redefine the v2 indexer IN PLACE — no `currentIndexVersion` bump. The
  version was introduced on this unreleased branch and no shipped/durable vault holds
  v2 yet, so a single reindex yields the complete v2 result; a bump would force a
  redundant second pass (ADR-025 D1). Condition: a vault must not have been reindexed
  to v2 before this lands.

### Subtasks
- [x] 6.1 `scanner.go`: `toolCall{name,summary}` type + `excludedResultTools` ({Read, NotebookRead}); shared `collectToolUseSummaries` map → `map[string]toolCall` (all 3 parsers); `extractUserBlocks` skips excluded results (FTS)
- [x] 6.2 `render.go`/`transcript.go`: `renderUserContent` collapses excluded results to a one-line marker via `collapsedToolResult` (display); `raw_jsonl`/JSON output unchanged
- [x] 6.3 `store.go`: `VaultStats.IndexVersion` + `OutdatedSessions`; `Stats()` computes them; `currentIndexVersion` doc comment covers both v2 behaviors + the released-boundary bump rule. `cmd/capy/vault.go`: `printStats` + `statsJSON` surface them
- [x] 6.4 Tests: scanner Read-excluded/Bash-indexed/call-searchable; render + transcript Read-collapse + Bash-full; canonical `sampleMainJSONL` switched Read→Bash (indexed path); reindex/import-stale tests reworked to a Bash token. Validated on real 497 MB vault — reindex 487 sessions in ~28 s, idempotent, stats shows the index-version line
- [x] 6.5 Docs: ADR-025 D1 (in-place redefinition + stats visibility); design.md (Excluded results, Version semantics, Deferred reconciled); README/architecture vault tables; scanner.go doc comments

**Invariant:** the tool CALL summary ("Read /path") stays indexed on the assistant
row, so a session is still findable by what it read; only the result BODY is
dropped from FTS / collapsed in display. Bash/Grep results stay indexed.

## Completion
- [x] `/kk:test` full suite green with `-tags fts5` (`make test-race` green across all packages)
- [x] Isolated code review (`/kk:review-code:isolated`) — 4 corroborated findings, all fixed (see Review fixes below); 2 systemic findings indexed as `kk:review-findings`
- [x] `/kk:document` — README + architecture.md command tables updated with `reindex`; `docs/wip/vault/v2/tasks.md` Task 1 → done-on-branch, Task 5.2 → runner-already-built
- [x] Reflection note (below)

## Review fixes (post-implementation, isolated review)
- [x] `migrate0003AddIndexVersion`: read-only fast-path before `BeginImmediate` — avoids a RESERVED write lock on every `getDB()` open once applied (corroborated HIGH)
- [x] `UpdateSessionFTS`: bump `index_version` first + `RowsAffected()==0` guard — bails before inserting orphaned FTS rows if the session was deleted concurrently (`vault_fts` has no FK) (corroborated HIGH)
- [x] `OutdatedSessionUUIDs`: `ctx context.Context` + `QueryContext` — completes `Reindex`'s cancellation story (corroborated MEDIUM); `Reindex` treats a cancelled initial query as a clean cooperative stop (matching `Import`)
- [x] `OutdatedSessionUUIDs`: `make([]string, 0)` — consistency with the file's slice convention (corroborated LOW)
- Kept (with rationale): `BeginImmediate` shared backoff (pal confirmed correct); `columnExists` PRAGMA interpolation (trusted constant, PRAGMA can't bind, mirrors `BeginImmediate`); concrete `*VaultStore` in `Reindex`, error-string cardinality, `fmt.Printf` to stdout — all consistent with the existing codebase.

## Reflection
The display half was free as designed (re-parsed from `raw_jsonl`); the shared `userToolResultLine` fixture already paired a `Read` tool_use with its result, so the enrichment "just worked" in existing tests with only assertion updates. The bigger surprise was scope: this "low-effort" feature required standing up the **first real vault migration runner** (v2-shared infra) and the `beginImmediate`/`isBusy` consolidation — both done to v2's intended shape so v2 builds on them rather than redoing them. The `index_version` insert path must stamp `currentIndexVersion` **explicitly** (not rely on the column DEFAULT) so that migrated DBs (ALTER default 1) still record fresh inserts as current. Threading `ctx` into `OutdatedSessionUUIDs` introduced a subtle behavior change — a pre-cancelled context now fails the query rather than being caught by the loop check — fixed by making `Reindex` treat query-time cancellation as graceful, matching `Import`. Future work this area should know: the migration runner is name-keyed + idempotent, so v2's `0001`/`0002` slot in regardless of order relative to `0003`.
