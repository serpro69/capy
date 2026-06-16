# Tasks: Tool name + inputs in vault tool-result entries

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: in-progress
> Created: 2026-06-16

**Not doing:** generic rendering of arbitrary tool inputs (only the common tools
get input detail via the existing `toolUseSummary`; all tools get the name);
zstd compression / `encoding` column / `vault_snapshots` / PreCompact (all remain
pending v2 work); background/automatic reindex (explicit `capy vault reindex` only).

Build/test with `-tags fts5`; `CAPY_DB_KEY` + `CAPY_VAULT_KEY` required.

## Task 1: Consolidate `beginImmediate`/`isBusy` into `sqliteutil` (== v2 Task 1)
- **Status:** in-progress
- **Depends on:** —
- **Size:** S
- **Docs:** [implementation.md#task-1](./implementation.md)

### Subtasks
- [ ] 1.1 Add exported `BeginImmediate(db *sql.DB, lockTable string)` + `IsBusy(err)` to `internal/sqliteutil/sqliteutil.go`
- [ ] 1.2 Delete `beginImmediate`/`isBusy` from `internal/store/migrate.go`, delete `internal/store/retry.go`, update store call sites (`migrate.go`, `cleanup.go:343`) to `sqliteutil.BeginImmediate(db, "sources")` / `sqliteutil.IsBusy`
- [ ] 1.3 Delete `beginImmediate`/`isBusy` from `internal/vault/migrations.go`, update `vault/store.go` call sites to `sqliteutil.BeginImmediate(db, "vault_meta")`
- [ ] 1.4 Verify `grep -rn "func beginImmediate\|func isBusy" internal/` is empty; `make test-race` green
- [ ] 1.5 Tests for `sqliteutil.BeginImmediate`/`IsBusy`

## Task 2: Tool-entry enrichment (display + FTS)
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Docs:** [implementation.md#task-2](./implementation.md)

### Subtasks
- [ ] 2.1 `scanner.go`: build `tool_use_id → summary` map; enrich `extractUserBlocks` tool-result text (FTS)
- [ ] 2.2 `render.go`: `renderUserContent` gains the map param; `collectDisplay` builds + passes it (show)
- [ ] 2.3 `transcript.go`: `ParseTranscript` builds + passes the map (TUI)
- [ ] 2.4 Update scanner/render/transcript tests for the prefix + unknown-id (no prefix) case

## Task 3: `index_version` infra + import skip gate
- **Status:** pending
- **Depends on:** Task 1 (uses `sqliteutil.BeginImmediate` in the migration), Task 2 (the indexer change that justifies v2)
- **Size:** M
- **Docs:** [implementation.md#task-3](./implementation.md)

### Subtasks
- [ ] 3.1 `store.go`: add `index_version` to `schemaSQL`; `currentIndexVersion = 2`; `Session.IndexVersion`; insert/update stmts + `writeRecord`
- [ ] 3.2 `migrations.go`: `vaultMigrationApplied` guard + apply-loop; `migrate0003AddIndexVersion`
- [ ] 3.3 `store.go`: extend `SessionDigest` to return `index_version`
- [ ] 3.4 `import.go`: stamp `IndexVersion` in `buildRecord`; gate skip predicate on hash AND version
- [ ] 3.5 Tests: migration (add/idempotent/fresh-DB), import gate, digest

## Task 4: Reindex (DB-driven) + CLI command
- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Docs:** [implementation.md#task-4](./implementation.md)

### Subtasks
- [ ] 4.1 `store.go`: `stmtUpdateIndexVersion`; `UpdateSessionFTS(uuid, newVersion, fts)`; `OutdatedSessionUUIDs(maxVersion)`
- [ ] 4.2 `reindex.go`: `Reindex(ctx, store)` (DB-driven re-scan + `UpdateSessionFTS`, ctx-aware, per-session errors)
- [ ] 4.3 `cmd/capy/vault.go`: `newVaultReindexCmd` + register
- [ ] 4.4 Tests: `UpdateSessionFTS` (FTS replaced, version bumped, blob untouched), `OutdatedSessionUUIDs`, `Reindex`

## Completion
- [ ] `/kk:test` full suite green with `-tags fts5`
- [ ] `/kk:document` — update vault docs (commands, index_version); update `docs/wip/vault/v2/tasks.md` Task 1 → done-on-branch
- [ ] Reflection note + index non-obvious learnings
