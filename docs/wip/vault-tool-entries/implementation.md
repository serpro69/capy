# Implementation: Tool name + inputs in vault tool-result entries

> Design: [./design.md](./design.md)

File-level changes, grouped by task (see [tasks.md](./tasks.md)).

## Task 1 — Consolidate `beginImmediate`/`isBusy` into `sqliteutil` (v2 Task 1)

`internal/sqliteutil/sqliteutil.go`
- Add `func BeginImmediate(db *sql.DB, lockTable string) (*sql.Tx, error)`: the
  current beginImmediate body, with the no-op write parameterized as
  `DELETE FROM <lockTable> WHERE 0`. `lockTable` is a trusted internal constant
  (`sources` / `vault_meta`), never user input — safe to interpolate.
- Add `func IsBusy(err error) bool`: the current isBusy body (typed
  `sqlite3.Error` code match + string fallback).

`internal/store/migrate.go`
- Delete local `beginImmediate`. Replace its 3 call sites
  (`migrate017`, `migrate018`, line 41/180) with
  `sqliteutil.BeginImmediate(db, "sources")`.
- Replace `isBusy(err)` calls with `sqliteutil.IsBusy(err)`.

`internal/store/retry.go`
- Delete the file (only contains `isBusy`, now orphaned). Check for `retry_test.go`.

`internal/store/cleanup.go`
- Line 343: `beginImmediate(db)` → `sqliteutil.BeginImmediate(db, "sources")`.

`internal/vault/migrations.go`
- Delete local `beginImmediate` and `isBusy`.

`internal/vault/store.go`
- Lines 439/465/539: `beginImmediate(db)` → `sqliteutil.BeginImmediate(db, "vault_meta")`.

Verify: `grep -rn "func beginImmediate\|func isBusy" internal/` returns nothing
(only `sqliteutil` has `BeginImmediate`/`IsBusy`).

## Task 2 — Tool-entry enrichment (the feature)

`internal/vault/scanner.go`
- Add `collectToolUseSummaries(blocks []contentBlock, into map[string]string)`:
  for each `tool_use` block, `into[b.ID] = toolUseSummary(b.Name, b.Input)`.
- In `scan`, after pass 1, build `toolUses` by walking assistant entries' blocks.
- `extractUserBlocks(raw, toolUses)`: for each `tool_result`, prefix
  `toolUses[b.ToolUseID]` (when non-empty) before `truncateHeadTail`.

`internal/vault/render.go`
- `renderUserContent(raw, toolUses)`: same prefix logic (shared by show + TUI).
- `collectDisplay`: build `toolUses` from assistant entries, pass into
  `renderUserContent`.

`internal/vault/transcript.go`
- `ParseTranscript`: build `toolUses`, pass into `renderUserContent`.

Shared helper for the prefix (in scanner.go or render.go):
`func prefixToolResult(label, body string) string` → `label + "\n" + body` when
label != "", else body.

## Task 3 — `index_version` infra + import gate

`internal/vault/store.go`
- `schemaSQL`: add `index_version INTEGER NOT NULL DEFAULT 1` to `vault_sessions`.
- `const currentIndexVersion = 2` (+ doc comment).
- `Session`: add `IndexVersion int`.
- `stmtInsertSession` / `stmtUpdateSession`: add `index_version` column/value.
- `writeRecord`: pass `sess.IndexVersion` for both insert and update.
- `SessionDigest`: extend to return `indexVersion int`
  (`SELECT content_hash, size_bytes, index_version …`).

`internal/vault/migrations.go`
- Add `vaultMigrationApplied(tx, name)` + record helper.
- Add `migrate0003AddIndexVersion(db)`: guarded by name; `ALTER TABLE
  vault_sessions ADD COLUMN index_version INTEGER NOT NULL DEFAULT 1`.
- Wire into `migrateVault`'s apply-loop.

`internal/vault/import.go`
- `buildRecord`: set `sess.IndexVersion = currentIndexVersion`.
- Skip predicate (see design.md): skip when
  `hash == existingHash && existingIndexVersion >= currentIndexVersion`;
  else honor the existing smaller-divergent-variant skip; else replace.

## Task 4 — Reindex

`internal/vault/store.go`
- `stmtUpdateIndexVersion`: `UPDATE vault_sessions SET index_version = ? WHERE uuid = ?`.
- `UpdateSessionFTS(uuid string, newVersion int, fts []FTSRow) error`: one tx —
  delete FTS by session, insert new rows, bump version. No blob rewrite.
- `OutdatedSessionUUIDs(maxVersion int) ([]string, error)`:
  `SELECT uuid FROM vault_sessions WHERE index_version < ? ORDER BY end_time DESC`.

`internal/vault/reindex.go` (new)
- `Reindex(ctx, store) ReindexResult`: for each outdated uuid, `GetSession` +
  `GetFiles`, re-scan (`ScanSession` + `ScanSubagent`), build FTS rows
  (reuse `ftsRow`), `UpdateSessionFTS(uuid, currentIndexVersion, fts)`.
  Cooperative ctx cancellation at the session boundary, per-session error capture
  (mirror `Import`). Returns counts.

`cmd/capy/vault.go`
- `newVaultReindexCmd(env)` + register in `newVaultCmd`. Opens store, runs
  `vault.Reindex`, prints `reindexed N, skipped M, errors K`. `guardTUI`.

## Tests

- `scanner_test.go` / `render_test.go` / `transcript_test.go`: update tool-result
  assertions for the new prefix; add a case for an unknown `tool_use_id`
  (no prefix) and for Bash/Read summaries.
- `store_test.go`: `UpdateSessionFTS` (FTS replaced, version bumped, blob
  untouched), `OutdatedSessionUUIDs`, `SessionDigest` returns version.
- `migrations_test.go` (new): `0003` adds the column, idempotent re-run, fresh-DB
  has the column from `schemaSQL`.
- `import_test.go`: version-stale-but-hash-identical → StatusUpdated; current →
  StatusSkipped; smaller-divergent still skipped.
- `reindex_test.go` (new): outdated session reindexed + version bumped; current
  session untouched; ctx cancellation.
- `sqliteutil_test.go`: `BeginImmediate`/`IsBusy` smoke (lock acquisition, busy
  classification).
