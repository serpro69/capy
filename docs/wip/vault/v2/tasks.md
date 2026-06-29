# Tasks: Vault v2

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Parent (v1): [../tasks.md](../tasks.md)
> Status: pending
> Created: 2026-06-05
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic retention, redacted sharing/export, TUI lazy-windowing viewer, TUI 3-panel split, encryptPlain (unencrypted→encrypted) extraction, snapshot retention eviction policy, store-side context.Context propagation (Task 3 dropped — deferred until a store-side cancelling caller exists), PreCompact archival / `vault_snapshots` (Tasks 14–16 dropped — `/compact` is append-only, no file-level data loss; see precompact-investigation.md)

Single flat plan (no phase boundary). Task 0 is the only hard gate and gates ONLY the PreCompact tasks (14–16); all other tasks proceed independently. Build/test with `-tags fts5`; `CAPY_DB_KEY` + `CAPY_VAULT_KEY` required.

**Tx-helper note:** tasks that open a write transaction use the vault tx helper — the local `beginImmediate` (`migrations.go`) before Task 1 lands, or `sqliteutil.BeginImmediate` after. Either works; this is **not** a hard ordering dependency on Task 1.

## Task 0: Investigate PreCompact hook payload
- **Status:** done — investigation complete; PreCompact archival (Tasks 14–16) **dropped**. Findings + re-trigger conditions in [precompact-investigation.md](./precompact-investigation.md).
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1–13
- **Slicing strategy:** Risk-First (highest uncertainty; gates Tasks 14–16)
- **Docs:** [implementation.md#v20--investigate-precompact-payload-risk-first-gates-v213-15](./implementation.md)

### Subtasks
- [x] 0.1 In `internal/hook/precompact.go`, add a debug branch gated behind `CAPY_DEBUG_PRECOMPACT=1` that writes the raw `input []byte` to an `os.CreateTemp` file (0600) and logs the path to stderr — also dumps a hook-time copy of the `transcript_path` session file (0600) for the 0.4 content-level timing diff (per implementation.md V2.0). Unit tests in `precompact_test.go`.
- [x] 0.2 Trigger `/compact` in a real Claude Code session; capture the payload — captured for a manual `/compact` (session `7abfb552`); payload + hook-time session copy dumped to `0600` temp files
- [x] 0.3 Document the JSON shape in `docs/wip/vault/v2/precompact-investigation.md`: is the session file path present? session ID? project dir? — yes to all (`transcript_path`, `session_id`, `cwd`, `hook_event_name`, `trigger`)
- [x] 0.4 Verify timing at the **content level, not mtime**: capture a copy of the session-file contents AT hook time, then diff against the post-`/compact` file — confirm the hook-time copy still contains pre-compaction turns the compacted file lost. (mtime alone can't distinguish pre- from post-mutation.) — **Finding:** `/compact` is **append-only**; the hook-time copy is a byte-identical *prefix* of the post-compact file (nothing removed — a compaction-summary entry is appended). The hook reads the full pre-compaction transcript.
- [x] 0.5 Ensure the debug branch is a no-op when the env var is unset (no behavior change shipped) — gated on `CAPY_DEBUG_PRECOMPACT == "1"` exactly; covered by `TestPreCompact_NoOpWhenEnvUnset` / `TestPreCompact_NoOpWhenEnvNotExactlyOne`
- [x] 0.6 **Decision gate:** if the hook-time content is already the compacted transcript (pre-compaction turns absent), STOP — file-based capture is impossible; re-scope Tasks 14–16 per design.md §PreCompact (SessionStart-cached copy, or drop). Record the decision in the investigation doc — gate criterion NOT triggered (hook-time content is the full pre-compaction transcript), but the append-only finding makes archival low-value → **Decision: drop Tasks 14–16** for now (re-trigger conditions in the investigation doc)
- [x] 0.7 **Remove the debug scaffolding** once the investigation concludes: the `CAPY_DEBUG_PRECOMPACT` branch + `dumpPreCompactDebug`/`writeDebugTemp` in `precompact.go` (and `precompact_test.go`) are temporary instrumentation. — **Done:** reverted `handlePreCompact` to a documented pure no-op; `precompact_test.go` reduced to a no-op contract test. Instrumentation preserved in this branch's git history for revival. Debug hook command in `.claude/settings.local.json` reverted.

## Task 1: Consolidate `beginImmediate`/`isBusy` into `sqliteutil`
- **Status:** done — completed early on branch `vault_tool_cmd` (the [vault-tool-entries](../../vault-tool-entries/) feature needed the vault migration runner, which sits on this consolidation). Do NOT redo.
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 2, 5–13
- **Docs:** [implementation.md#beginimmediateisbusy-consolidation-v21](./implementation.md)

### Subtasks
- [x] 1.1 Added exported `BeginImmediate(db *sql.DB, lockTable string) (*sql.Tx, error)` and `IsBusy(error) bool` to `internal/sqliteutil/sqliteutil.go`. **Signature note:** the no-op lock table is **parameterized** (`lockTable`) rather than a per-store wrapper — store passes `"sources"`, vault passes `"vault_meta"`.
- [x] 1.2 Deleted the copies and updated all call sites. `internal/store/retry.go` removed; `beginImmediate`/`isBusy` gone from `internal/store/migrate.go` and `internal/vault/migrations.go`; call sites in `store/{migrate,cleanup}.go` and `vault/store.go` route through `sqliteutil`.
- [x] 1.3 Verified: `make test-race` green; `grep -rn "func beginImmediate\|func isBusy" internal/` returns nothing (only `sqliteutil.BeginImmediate`/`IsBusy` exist).

## Task 2: Route `session.SessionDir()` through `config.ClaudeProjectsDir()`
- **Status:** done
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 1, 5–13
- **Docs:** [implementation.md#sessiondir-routing-v23](./implementation.md)

### Subtasks
- [x] 2.1 Replace the hardcoded `~/.claude/projects/` base in `internal/session/sweep.go:SessionDir()` with `config.ClaudeProjectsDir()` (already `CLAUDE_CONFIG_DIR`-aware)
- [x] 2.2 Verify: `go test -tags fts5 ./internal/session/... ./internal/config/...` green; a `CLAUDE_CONFIG_DIR`-set test resolves the overridden root — added `TestSessionDir_HonorsClaudeConfigDir`

## Task 3: `context.Context` propagation — `internal/store` — DROPPED
- **Status:** dropped
- **Size:** — (removed from v2 scope)
- **Rationale:** Converting the encrypted knowledge store to `*Context` variants has **no functional beneficiary.** The store has no cancelling callers, and the only long-running cancellable operation — the Task 10 all-projects sweep — lives in `internal/vault` and already gets cooperative cancellation from `Import`'s per-session loop check (`import.go:136`). It would spend regression risk on encryption-critical code (CLAUDE.md's foremost invariant, "Encryption is mandatory") purely for sibling symmetry with the vault. "No diff is the safest diff" on encryption paths.
- **Re-trigger:** revisit only when a store-side caller genuinely needs sub-transaction cancellation. Until then `internal/store` keeps its plain `db.Query`/`Exec`/`Begin` calls. Tracked here so the decision is durable, not a chat-only aside.
**Not actionable — dropped, retained as rationale only (do NOT implement).** The original plan was to convert `internal/store` `db.Query`/`Exec`/`QueryRow`/`Begin` → `*Context` variants and add a leading `ctx context.Context` to public methods lacking one. This is **not** done in v2 (see Rationale above). `internal/store` keeps its plain `db.Query`/`Exec`/`Begin` calls. Re-open only when a store-side cancelling caller genuinely needs sub-transaction cancellation.

## Task 4: `context.Context` propagation — `internal/vault`
- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10–13
- **Slicing strategy:** isolated behavior-preserving commit (the sole ctx task now that store-side Task 3 is dropped)
- **Docs:** [implementation.md — context.Context propagation, vault only](./implementation.md)
- **Note:** kept (unlike dropped Task 3) because the cancelling caller — the Task 10 all-projects sweep — lives in this package; threading `ctx` makes vault's public API cancellation-ready where it matters.

### Subtasks
- [x] 4.1 Convert `internal/vault` DB calls → `*Context` variants; replace `VaultStore.ctx()` (returns `context.Background()`) with a real threaded `ctx`; add leading `ctx` to public methods (`GetSession`, `ListSessions`, `Search`, `Stats`, `InsertSession`, `ReplaceSession`, `DeleteSession`, `WriteBatch`, …). **Also:** threaded `ctx` through `migrations.go` (no contextless seam from `openDB`) and added `sqliteutil.BeginImmediateContext` (the contextless `BeginImmediate` now delegates to it, so the store's call sites stay untouched per dropped Task 3). `Close()`/`Checkpoint()` deliberately stay contextless (lifecycle: Close runs at shutdown when the request ctx is already cancelled, and its WAL flush must complete).
- [x] 4.2 Update CLI callers (`cmd.Context()`) and `server.vaultSweep` (`sweepCtx`) to pass context. **Also:** TUI — `dataStore`/`searcher` interfaces gained `ctx` (to match `VaultStore`), and the program `ctx` is carried on the bubbletea `Model`/`searchModel` (documented framework-boundary exception to "don't store ctx in a struct", since bubbletea owns `Update`/`View` and the model's lifetime == the program-ctx lifetime).
- [x] 4.3 Verify: `CAPY_VAULT_KEY=… go test -tags fts5 -count=1 -race ./internal/vault/... ./cmd/capy/... ./internal/server/...` green — PASS (+ `./internal/sqliteutil/...`; `go vet -tags fts5 ./...` clean)

## Task 5: zstd BLOB codec + compress-on-write / decompress-on-read
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0/P1/P2-bugs; applied the one corroborated fix (`markMinReaderVersion` now stamped once per write tx via `writeChildren` returning `(bool, error)`). Remaining findings (per-write `os.Getenv`, test-style) intentionally kept.
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 7, 10, 11, 12, 13
- **Slicing strategy:** Vertical (write→store→read all exercised end-to-end)
- **Docs:** [implementation.md#zstd-codec-v25](./implementation.md)

### Subtasks
- [x] 5.1 `go get github.com/klauspost/compress` + `go mod tidy`; apply `/kk:dependency-handling` to confirm the resolved `EncodeAll`/`DecodeAll` API — resolved `v1.18.6`; context7 confirmed `EncodeAll(src, dst) []byte` / `DecodeAll(src, dst) ([]byte, error)` thread-safe + reentrant on a shared `*Encoder`/`*Decoder` (`NewWriter(nil)`/`NewReader(nil)`)
- [x] 5.2 ~~Build the migration runner~~ **(runner already built on branch `vault_tool_cmd`** — `vaultMigrationApplied` + apply-loop + `columnExists` are in `migrations.go`, with `0003_add_index_version` as the first migration). Just **add migration `0001_blob_encoding`** to the apply-loop: `ALTER TABLE vault_sessions ADD COLUMN encoding TEXT` + same on `vault_files` (legacy rows → NULL = raw), guarded by `vaultMigrationApplied`, idempotent, inside `sqliteutil.BeginImmediate`. NOTE: `0003` already exists (index_version), so apply order is `0001`→`0002`→`0003` by listing them in that order in `migrateVault`; they are independent + name-keyed, so order is cosmetic. **Update:** `0002_vault_snapshots` is dropped (Tasks 14–16 dropped), so v2 adds only `0001` here alongside the existing `0003`. — added `migrate0001AddBlobEncoding` (fast-path read + locked re-check, `columnExists`-guarded ALTER on both tables); `encoding TEXT` also added to `schemaSQL` for fresh vaults (mirrors the `0003`/`index_version` pattern)
- [x] 5.3 Create `internal/vault/codec.go` — shared package-level `*zstd.Encoder`/`*zstd.Decoder`; `encodeBlob([]byte) (data []byte, encoding string)` (returns `"raw"` when `CAPY_VAULT_NO_COMPRESS` set or not smaller, else `"zstd"`); `decodeBlob(encoding string, b []byte) ([]byte, error)` switching on the column. **No magic-byte detection** (unsafe for arbitrary sidecars — the `encoding` column is authoritative)
- [x] 5.4 Wire write side: add `encoding` to `vault_sessions`/`vault_files` INSERT+UPDATE statements; `writeRecord` (raw_jsonl) and `writeChildren` (file content) call `encodeBlob` and store the returned encoding. On first `zstd` write, set `vault_meta` `min_reader_version` = `"2"`. **Add the matching open-time check in `openDB()`** (after the canary): read `min_reader_version` and refuse with a clear error if it exceeds a `supportedReaderVersion` constant (`2`) — without the read step the marker protects no one — `markMinReaderVersion` (INSERT OR IGNORE, idempotent) called within the write tx on any zstd blob; `checkReaderVersion` wired into `openDB()` after `migrateVault` (vault_meta guaranteed present)
- [x] 5.5 Wire read side: add `encoding` to `sessionMetaColumns` + `GetFiles` SELECT; thread it into `decodeBlob` in `scanSessionMeta`/`GetSession` and `GetFiles` — **deviation:** kept `sessionMetaColumns` metadata-only; `encoding` is selected adjacent to `raw_jsonl` only in the blob-fetching `stmtSessionsByPrefix`, so `ListSessions`/ambiguous-candidate reads don't fetch an unused column. Functionally equivalent for `GetSession`.
- [x] 5.6 Confirm `content_hash`/`size_bytes`/FTS still computed on UNCOMPRESSED bytes (no change to `computeContentHash`/`buildRecord`) — confirmed: `import.go` hashes/sizes/scans the raw `contents` map before any store call; compression is applied only at the `writeRecord`/`writeChildren` blob seam
- [x] 5.7 Tests: round-trip Insert→Get byte-identical; compressible row stored `encoding='zstd'` (raw blob carries zstd magic); legacy `encoding IS NULL` row reads correctly (mixed corpus); **regression: a raw sidecar fixture whose first bytes are `0x28B52FFD` round-trips unchanged**; `content_hash`/`size_bytes` identical to v1; `min_reader_version` set after first compressed write; **a vault whose `min_reader_version` is hand-set to `"3"` fails to open with the version error, while `"2"`/absent opens fine** — all in `codec_test.go`
- [x] 5.8 Verify: `go test -tags fts5 ./internal/vault/...` green — green (race + vet clean; `cmd/capy`/`server` regression-clean)

## Task 6: `capy vault compact` (recompress + VACUUM)
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0; applied both actionable fixes: threaded `ctx` into `vacuum()`→`ExecContext` (pal HIGH — long VACUUM now cancellable; indexed as `kk:review-findings`) and wrapped the `markCompressed` error return (P1 consistency). Remaining nits (vacuum DSN `_synchronous`, before-size sampling) intentionally kept to mirror the canonical `store.Vacuum()`.
- **Depends on:** Task 5
- **Size:** S
- **Can run in parallel with:** Task 8, 9 (after Task 5)
- **Docs:** [implementation.md#capy-vault-compact-v26](./implementation.md)

### Subtasks
- [x] 6.1 **Busy pre-check before the rewrite phase** (a `Checkpoint()` reporting busy pages → abort "stop the server first"), so `Compact` fails fast instead of doing all the UPDATE work then hitting contention at VACUUM — `Compact` calls `s.Checkpoint()` first; busy → wrapped "stop the MCP server before compacting" error
- [x] 6.2 Add `VaultStore.Compact()` — rewrite **legacy** rows `WHERE encoding IS NULL` only (`'raw'` is the terminal "compression declined" state; selecting it too would prevent a clean no-op) via batched `BeginImmediate` UPDATEs that `SET raw_jsonl = ?, encoding = ?` with **the encoding `encodeBlob` returns** (`'zstd'` or `'raw'`) — never hard-coded `'zstd'` (that would mislabel an incompressible blob and corrupt its read). Same for `vault_files.raw_content`. Abort early with a clear error if `CAPY_VAULT_NO_COMPRESS` is set (it would compress nothing) — `internal/vault/compact.go`: `compactTable` collects keys then rewrites ≤50/tx; `rewriteBatch` persists the returned `enc`; `CAPY_VAULT_NO_COMPRESS` aborts first; stamps `min_reader_version` once if any zstd produced
- [x] 6.3 Run `VACUUM` on a dedicated single connection opened after the pool closes (mirror `Checkpoint`), with `PRAGMA temp_store = MEMORY` so the transient copy isn't written to an unencrypted on-disk temp (VACUUM itself is lock-protected, unlike rekey's swap) — `vacuum(ctx)` mirrors `store.Vacuum`; `ctx` threaded via `ExecContext` so a long VACUUM is cancellable (review fix); **skips VACUUM when zero rows rewritten** so a second compact is a genuine no-op
- [x] 6.4 Add the `compact` subcommand to `cmd/capy/vault.go`; report before/after file size (`os.Stat`) — `newVaultCompactCmd` + `printCompactResult` (before/after via `dbFileSize`, reclaimed delta); `guardTUI`; no-vault short-circuit
- [x] 6.5 Verify: a legacy (`encoding IS NULL`) fixture **including one incompressible blob** compacts → no row left `NULL`, each is `'zstd'` (carries the zstd magic) or `'raw'` (the incompressible one, round-trips byte-identical), file size dropped, `search`/`show` unchanged; a second `Compact` is a true no-op; `Compact` under `CAPY_VAULT_NO_COMPRESS` errors without modifying the DB — `compact_test.go`: `_RewritesLegacyBlobs` (zstd+raw mix, no NULL, round-trip, size drop, search hit, marker), `_MultipleBatches` (>50 rows → batch boundary), `_SecondRunIsNoOp`, `_NoCompressEnvErrors` (DB unmodified). All race-clean

## Task 7: Extract shared `Rekey` helper from `cmd/capy/encrypt.go`
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0. Applied all four agreed fixes: ① `defer os.Remove(tmpPath)` temp-leak cleanup in `Rekey`+`SwapAndVerify` (dropped redundant manual removes); ② rollback dual-error messages use two `%w` (was `%w`+`%v`, which hid the rollback error from `errors.Is/As`) — indexed `kk:review-findings`; ③ corrected `Checkpoint`'s inherited "%d pages busy" message (`busy` is a 0/1 flag, not a count) → "database is busy"; ④ added `TestRekey_WALSource` covering the WAL-source Checkpoint path. Kept as-is (deliberate/inherited): `openEncrypted` not delegating to `OpenWithCanary` (it caps `MaxOpenConns(1)` for serial rekey; flat error mirrors the original — noted for Task 8), `SwapAndVerify` name/SRP, store's delegation-guard `TestEncryptedDSN`, sidecar `os.Remove` discards, cross-volume rename + `.bak`-overwrite (pre-existing constraints).
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 10–13
- **Slicing strategy:** isolated behavior-preserving commit — ⚠ encryption-critical, shared with knowledge store
- **Docs:** [implementation.md#shared-rekey-extraction-v27](./implementation.md)

### Subtasks
- [x] 7.1 Add `sqliteutil.Rekey(dbPath, oldKey, newKey string) (RekeyResult, error)` (backup-API rotation: open old → checkpoint → backup-copy into temp opened with new key → swap+verify); bring `openEncrypted`/`backupDB`/`swapAndVerify`/`checkpointDB` into `sqliteutil`. **Strip the hard-coded `capy encrypt:` stdout/stderr out of `swapAndVerify`** (`encrypt.go:261,269,282,283`): the util returns `RekeyResult` (incl. the `.bak` path) and wrapped errors; the cmd layer prints. A low-level util must not do user-facing I/O, and `vault rekey` must not emit "capy encrypt:" — **done.** `Rekey`/`RekeyResult{BackupPath}` added to `sqliteutil`; the CRITICAL rollback-failure stderr prints became wrapped errors that name the recovery path.
- [x] 7.2 Rewire `cmd/capy/encrypt.go:rekeyEncrypted` to call `sqliteutil.Rekey` and print its own messages from `RekeyResult`; leave `encryptPlain` in `cmd/capy` — **done.** `rekeyEncrypted` is now a thin print wrapper; success output centralised in cmd-layer `printRekeyDone`.
- [x] 7.3 Verify: `go test -tags fts5 ./internal/store/... ./cmd/capy/...` green; the existing rekey round-trip in `encryption_lifecycle_test.go` passes unchanged; **`capy encrypt`'s stdout/stderr is unchanged** after moving the prints to the cmd layer — **done.** `store`/`cmd/capy`/`sqliteutil` race-clean; `vault`/`server` regression-clean; `go vet` clean. New `sqliteutil.TestRekey_RoundTrip` directly covers the extracted path.

**Deviations from plan (both forced, behavior-preserving — confirmed via pal/gemini-3.1-pro):**
1. **Import cycle:** `store` imports `sqliteutil`, so `sqliteutil.Rekey` (needing `EncryptedDSN`) could not reach `store.EncryptedDSN`. Resolution: moved `EncryptedDSN` + the two URI escapers into `sqliteutil` (the canonical SQLite layer — `vault` already reached into `store` only for this DSN); `store.EncryptedDSN` is now a thin delegating wrapper so its 4 callers, vault's 3, cmd's 2, and all tests stay unchanged (byte-identical output). Escapers unexported (`uriEscapePath`/`uriEscapePassphrase`); `store.URIEscapePath`/`URIEscapePassphrase` removed (no external callers; the one store test moved to `sqliteutil`). Optional follow-up (migrate all callers, drop wrapper) noted as a `TODO` in `encryption.go`.
2. **`encryptPlain` shares two helpers:** the plan said `checkpointDB`/`swapAndVerify` move to `sqliteutil` as *unexported*, but `encryptPlain` (which stays in `cmd/capy`) also uses both. Exported them as `sqliteutil.Checkpoint`/`SwapAndVerify` (single source of truth — duplicating the rollback logic was the alternative); `openEncrypted`/`backupDB` (only `Rekey` uses them) stay unexported.

## Task 8: `capy vault rekey` command
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0/P1/P2; applied both agreed LOW fixes: ① corrected the misleading no-`--remove-backup` `.bak` warning (the old "rerun with --remove-backup" suggestion was wrong — rekey isn't idempotent, so a rerun hits the new==old reject or does a full second rotation; now says "delete it manually (or pass --remove-backup on the next rotation)"); ② added a fail-fast `new==old` check in `RunE` *before* the confirm prompt (the authoritative check stays in `runVaultRekey` so the unit test is unaffected). Kept `os.IsNotExist` (matches every sibling vault subcommand — changing one line would break file consistency).
- **Depends on:** Task 7
- **Size:** S
- **Can run in parallel with:** Task 6, 9
- **Docs:** [implementation.md#capy-vault-rekey-v28](./implementation.md)

### Subtasks
- [x] 8.1 Add the `rekey` subcommand to `cmd/capy/vault.go`: prompt current passphrase, read new key from `CAPY_VAULT_KEY` (error if unset — note this differs from `capy encrypt`, which prompts when its env key is unset). **Confirm before rotating** ("rotating to the key currently in `CAPY_VAULT_KEY` — proceed?") and **reject new == old** (a forgotten env update would silently no-op-rotate a compromised key), then call `sqliteutil.Rekey(vault.VaultDBPath(), old, new)` — `newVaultRekeyCmd` + prompt-free core `runVaultRekey`; new key read via `vault.RequireVaultKey()` (group `PersistentPreRunE` already errors if unset); `new==old` rejected both fail-fast in `RunE` (pre-confirm) and authoritatively in `runVaultRekey`; `promptYesNo` confirm (safe-default "no" when non-interactive)
- [x] 8.2 **Do NOT add a `VaultStore.Checkpoint()` pre-check** — it opens with `CAPY_VAULT_KEY` = the *new* key and fails on the old-key DB; the WAL flush happens inside `Rekey` on the old-key source connection. Document a hard "stop the MCP server first" requirement in `--help` + README (the file-swap `rename` isn't lock-protected; the check is best-effort only). **Handle the old-key `.bak`:** print a prominent warning that `<vault>.bak` stays decryptable by the old (compromised) key, and accept `--remove-backup` to unlink it after the new file verifies open — documenting that this is deletion, not guaranteed erasure (SSD/CoW) — no Checkpoint pre-check; `--help` + README "Key rotation" section document stop-the-server + the residual-secret `.bak`; `--remove-backup` unlinks after `Rekey` verifies open (its `SwapAndVerify` re-opens the new file before returning); deletion≠erasure caveat in both. Corrected the now-stale README "key rotation … deferred to a future version" line.
- [x] 8.3 Verify: import → `rekey` (old→new) → reopen with new key lists same sessions; reopen with old key fails; `.bak` exists by default and is removed with `--remove-backup`; `rekey` is rejected when `CAPY_VAULT_KEY` equals the old key — `vault_unit_test.go`: `TestRunVaultRekey_RoundTrip` (new key opens + lists session, old key errors, `.bak` preserved), `_RemoveBackup` (`.bak` unlinked, rotation still valid), `_RejectsNewEqualsOld` (errors, vault byte-identical, no `.bak`); `vault_test.go:TestVaultRekeyNoVault` subprocess smoke test (no-vault short-circuit before the prompt). The prompt path itself isn't subprocess-testable because `terminal.ReadPassphrase` reads `/dev/tty`, hence the in-process core (indexed `kk:test-patterns`). Race + vet clean.

## Task 9: `capy vault merge --from <path>`
- **Status:** done — isolated review (code-reviewer sub-agent; pal/gemini-3.1-pro step-2 expert pass unavailable due to a harness input-validation failure on the continuation call) found no P0/P1. Applied the two agreed doc-clarity fixes: clarified the `--project` help text to name the mangled dir form (it filters `claude_project_dir`, consistent with `import` but differing from `list`'s `project_path`), and added a comment at the scan-failure site noting the dest row is left unchanged (mirrors import's buildRecord-failure posture). Kept (deliberate): the 6-param `MergeFrom` signature (`srcKey`/`srcKeyEnv` are credentials, not user-facing `MergeOptions` tuning) and the test-helper raw-string JSONL (matches the `cmd/capy` package convention; `jsonlBytes` is a `vault`-package-only helper).
- **Depends on:** Task 5, Task 11 (subtask 9.4 dropped along with Tasks 14–16 — see [precompact-investigation.md](./precompact-investigation.md))
- **Size:** M
- **Can run in parallel with:** Task 6, 8 (after Task 5 + Task 11)
- **Slicing strategy:** Contract-First (source-vault read boundary), then idempotent upsert reuse
- **Docs:** [implementation.md#capy-vault-merge---from-v29](./implementation.md)
- **Why Task 11 dep:** 9.3 reuses Task 11's `StatusExcluded` for the 0-msg guard — a hard code dependency, not just sequencing.
- **v2 update:** snapshot carry (9.4) and all `vault_snapshots` handling referenced in 9.3/9.6 are **dropped** with Tasks 14–16 (see [precompact-investigation.md](./precompact-investigation.md)). Merge handles `vault_sessions` + `vault_files` only — ignore the snapshot references in the subtasks below (source feature-detect of `vault_snapshots`, snapshot dedup, snapshot fixtures/assertions).

### Subtasks
- [x] 9.1 Add `sqliteutil.OpenSourceForMerge(dbPath, key)` (renamed from `OpenReadOnly` — it checkpoints the source, a **write**, so it needs a writable source file + dir; temp-copy a read-only source; document on `--help`) — does NOT run `schemaSQL`/`migrateVault` against the source, and **checkpoints any pending WAL first** (a copied-live source vault may carry a `-wal`; do NOT use `immutable=1`, which silently skips WAL-resident rows). **Signature deviation:** shipped as `OpenSourceForMerge(ctx, dbPath, key, keyEnv)` — added `ctx` (Task 4 ctx-threading convention; cancels the canary + checkpoint) and `keyEnv` (names the passphrase source so a wrong source key yields a `WrongPassphraseError` via `OpenWithCanary`, not a cryptic failure). Best-effort WAL checkpoint (a hard failure — e.g. a read-only source — surfaces with a "source must be writable" hint).
- [x] 9.2 Extract `buildFTS(uuid, mainBytes, files) []FTSRow` from `import.go:buildRecord` so disk-import and merge share the scanner wiring. **Deviation (no new function):** this extraction was **already done** under a different name — `scanSessionAndSubagents(uuid, mainBytes, files) (*ScanOutput, []FTSRow, error)` is the shared scanner wiring (extracted during the reindex work; `buildRecord` and `Reindex` both use it). `MergeFrom` reuses it directly (`_, fts, err := scanSessionAndSubagents(...)`, the same shape `rebuildSessionFTS` uses) rather than adding a redundant thin wrapper.
- [x] 9.3 Create `internal/vault/merge.go` — `MergeFrom(ctx, dest, srcPath, srcKey, opts)`: **feature-detect the source schema first** (`PRAGMA table_info` for the `encoding` column — absent ⇒ treat blobs as `raw`) so a v1 source doesn't raise "no such column"; iterate source `vault_sessions`+`vault_files`, `decodeBlob(encoding, …)`, apply `dest.SessionDigest` idempotent decision (skip same-hash/smaller, else insert/replace), carry source `machine_id`/`claude_project_dir`/`project_path`/`git_branch` verbatim, apply the 0-msg guard (Task 11's `StatusExcluded`), batch via `WriteBatch`. **Implementation notes:** (a) **Approach: carry ALL source metadata verbatim, re-scan only FTS** with the current indexer (sets `index_version=currentIndexVersion`) — identical posture to `capy vault reindex`, which also rebuilds FTS while leaving stored metadata untouched; (b) **no FTS-only upgrade branch** (unlike import) — `reindex` owns version upgrades of already-present sessions; merge only brings in NEW or larger content; (c) refactored `columnExists` to take a `ctxQueryer` interface so one impl serves both the migration runner (`*sql.Tx`) and merge's source probe (`*sql.DB`); (d) **robustness beyond spec:** `checkReaderVersion(srcDB)` refuses a source newer than this binary supports; UUIDs collected up front (compact's pattern) so no source cursor is held across dest writes.
- [ ] ~~9.4 Carry snapshots~~ **DROPPED** with Tasks 14–16 — v2 ships no `vault_snapshots` table, so there is nothing to carry. Merge copies sessions + files only. See [precompact-investigation.md](./precompact-investigation.md).
- [x] 9.5 Add the `merge` subcommand to `cmd/capy/vault.go`: `--from` (required), `--key`/`CAPY_VAULT_MERGE_KEY`, `--dry-run`, `--project`; `import`-style table output (reuses `printImportResult`). Concurrency: writes only the destination via batched `beginImmediate`, so a concurrent server sweep is absorbed by `busy_timeout`+retry (like `import`) — no "stop the server" requirement (unlike `rekey`) and no busy pre-check (unlike `compact`); noted in `--help`. **Added guards (fail-loud):** missing source file errors instead of `sql.Open` silently CREATING an empty source DB and merging as a no-op; a source path equal to the destination is rejected (`samePath`, abs + `os.SameFile`); key precedence `--key` > `CAPY_VAULT_MERGE_KEY` > `CAPY_VAULT_KEY` via `resolveMergeKey`.
- [x] 9.6 Verify: fixture vaults (overlapping + distinct UUIDs; **one v1-shaped source with no `encoding` column**) → `merge` brings in distinct sessions, keeps larger overlaps, `search` finds source-only content, source `machine_id`/`project_path` preserved, and **the v1-shaped source merges cleanly** (blobs read as raw — no "no such column"); `--dry-run` writes nothing. Snapshot fixtures/assertions dropped with 9.4. — `internal/vault/merge_test.go`: `_DistinctAndLargerWins` (different src/dest keys, replace+new+skip, carried metadata, FTS re-scan via search), `_Idempotent`, `_DryRunWritesNothing`, `_ExcludesEmptySource`, `_ProjectFilter`, `_V1ShapedSourceMergesCleanly` (raw blobs + sidecar round-trip), `_WrongSourceKeyFails`. `cmd/capy`: `TestResolveMergeKey`, `TestSamePath`, `TestVaultMerge_EndToEnd` (CLI dry-run + real merge + `--from`-required + same-vault guards). Race + vet clean.

## Task 10: All-projects opt-in server sweep
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0/P1/P2. Applied the corroborated finding: exact-length assertions (`require.Len`) in both new tests, matching the sibling `TestVaultSweep_ImportsCurrentProjectSessions`. Also moved the all-projects summary log to fire after the `st.Open()` probe (so it never precedes an open failure) yet before `Import` (so it confirms the opt-in even on an all-skipped run). Kept `t.Setenv("CAPY_VAULT_SWEEP_ALL", "")` in the default test — flagged as a no-op but actually defensive (overrides an ambient `CAPY_VAULT_SWEEP_ALL=1` in the dev's shell for the test's duration).
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 11–13
- **Docs:** [implementation.md#all-projects-opt-in-sweep-v210](./implementation.md)

### Subtasks
- [x] 10.1 In `internal/server/server.go:vaultSweep`, branch on `CAPY_VAULT_SWEEP_ALL`: when set, discover from `config.ClaudeProjectsDir()` (root) instead of `ProjectSessionDir(s.projectDir)`; keep `ctx`/`bgWg`/`Open()` probe/`Close()` intact; log a per-run summary — `allProjects` branch resolves the root via `config.ClaudeProjectsDir()` (warns on failure since the all-projects sweep is explicitly opted into); `countProjects` helper feeds the `slog.Info("vault sweep (all projects)", "projects", N, "sessions", M)` summary; goroutine/`bgWg`/`ctx`/`Open()`/`Close()` machinery untouched
- [x] 10.2 Verify: server-startup test with `CAPY_VAULT_SWEEP_ALL=1` + multi-project fixture imports from >1 project; unset → current project only — `vault_sweep_test.go`: `setupVaultSweepMultiProject`/`writeProjectSession` lay two projects under the Claude root; `TestVaultSweep_AllProjects_ImportsAcrossProjects` (both projects' sessions archived) + `TestVaultSweep_DefaultScopesToCurrentProject` (sibling project's session absent). Race + vet clean; full server suite green

## Task 11: Exclude 0-message sessions from import
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0/P1/P2. Applied the one agreed P3 (added `excluded` to the cooperative-cancellation `slog.Debug` summary, which had omitted the new counter). Deferred the other P3 (untyped `Status*` string constants → a `type ImportStatus string`) as an inline `TODO` at the constants block — a cross-cutting refactor out of this task's scope.
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10, 12, 13
- **Docs:** [implementation.md#exclude-0-msg-sessions-v24](./implementation.md)

### Subtasks
- [x] 11.1 Add `StatusExcluded = "excluded"` to `internal/vault/import.go` status constants + `ImportResult` accounting + CLI table output
- [x] 11.2 In the `Import` loop, after `buildRecord`, skip sessions with `rec.Session.MessageCount == 0` (record `StatusExcluded`, don't batch)
- [x] 11.3 Verify: a fixture dir with a 0-msg + a normal session → 0-msg excluded (absent from `list`), normal imported; re-import after it gains messages archives it — `import_test.go`: `TestImport_ExcludesEmptySessions` (0-msg excluded + dry-run reports it + absent from `list`/`GetSession`), `TestImport_ArchivesPreviouslyEmptySessionOnceItGainsMessages` (re-import after gaining turns archives as `StatusNew`). Race + vet clean; `make build` green.

## Task 12: TUI completion — keybindings + in-list filter
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0/P1; reviewers flagged disjoint sets (no corroboration). Applied the "recommended set": ① pal HIGH — `Model.View()` appended the status as an extra row on top of full-height sub-models → alt-screen overflow/flicker (pre-existing for the rare error path; Task 12's `c`/filter statuses made it routine). Fixed with a `bodyHeight`/`layoutSubmodels` seam: a status now reserves the bottom row (sub-models sized to height-1), routed through both WindowSizeMsg and `withStatus`/`withError`/`clearStatus` (indexed `kk:review-findings`). ② pal MEDIUM — copy confirmation never cleared → `clearStatus` on the next non-`c` keystroke. ③ pal HIGH (pre-existing, opportunistic) — `viewer.contentWidth()` returned 1 when width==0 (view-mode startup before the first WindowSizeMsg wrapped the whole transcript to 1 col) → 80-col fallback. ④ nits — renamed `actionFor(key)`→`(k)`, documented the OSC-52 write-error discard. Status now also renders neutral (StatusBar) for info vs error (red) instead of always-red. Kept (convention-consistent): `currentMessage` `break` (mirrors `rowForLine`/`lineForRow`), `performTUIAction` default, `*cobra.Command` helper params. New tests: `_StatusReservesRowNoOverflow`, `_CopyStatusClearsOnNextKey`.
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10, 11, 13
- **Risk focus:** `r`/`R` are the destructive/exec surface — must teardown bubbletea (release alt-screen + TTY) before exec, same care as v1 Task 4b
- **Docs:** [implementation.md#keybindings--in-list-filter-v211](./implementation.md)

### Subtasks
- [x] 12.1 `f` — in-list project filter (predicate over `ListSessions(Project:)`); update `app.go` dispatch + status-bar hints in `styles.go` — **deviation:** the filter input + flag live on `listModel` (`list.go`), the app owns the re-query (it holds store+ctx); hints surfaced via `bubbles/list.AdditionalShortHelpKeys` (`/`,`f`,`enter`,`r`,`R`,`q`) + a `filter project:` prompt with `enter apply · esc clear`, rather than free-text in `styles.go`
- [x] 12.2 `c` — copy current message to clipboard via OSC-52 escape (no native dep); always show a **status-bar confirmation** afterward — **deviation:** the escape is written to **os.Stderr** (not stdout): same TTY as bubbletea's stdout renderer but out-of-band, so the alt-screen isn't disturbed. Writer is an injectable `Model.clipOut io.Writer` for tests. "current message" = the message at the top of the viewport (`viewer.currentMessage`)
- [x] 12.3 `r`/`R` — emit a `tea.Quit`-then-action intent carrying the UUID; perform restore/`claude --resume` from the CLI layer after `Run()` returns — `tui.Run` now returns `(Action, error)`; `launchTUI`→`performTUIAction` dispatches to extracted `restoreVaultSession`/`resumeVaultSession` (shared with the CLI subcommands). Intent carries UUID only (CLI re-fetches files/dir — no staleness). Wired in both list (selected) and viewer (open session)
- [x] 12.4 Verify: `internal/vault/tui` unit tests cover filter narrowing, OSC-52 emission + status-bar confirmation, and the quit-then-action intent for `r`/`R`; race-clean — `keybindings_test.go`: `_FilterNarrowsByProject`/`_FilterEscClearsAndRestoresAll`/`_FilterViewShowsInput`, `_CopyEmitsOSC52AndConfirms`/`_CopyNothingToCopyWhenEmpty`/`TestOSC52Sequence`/`TestViewer_CurrentMessage`, `_ListRestoreIntent`/`_ListResumeIntent`/`_ViewRestoreResumeIntentUsesOpenSession`/`_RestoreNoopWhenListEmpty`. Test helper `key`→`keyMsg` (collided with new `bubbles/key` import). Full suite + race + vet clean

## Task 13: TUI glamour markdown rendering behind a build tag
- **Status:** done — isolated review (code-reviewer + pal/gemini-3.1-pro) found no P0/P1; the one P1 (CI missing `CAPY_VAULT_KEY`) was a verified false positive (tui tests use the in-memory `stubStore`; vault tests self-provide the key via `t.Setenv`, so the existing `check` job runs the full `./...` suite green with only `CAPY_DB_KEY`). Applied the 3 agreed nits: ① lock-wrapped the guarded-var reads in the cache test via `currentGlamourRenderer()`/`resetGlamourCache()` (corroborated — removes a latent race if anyone adds `t.Parallel`); ② clarified the `strings.Trim` comment (corroborated — trimming both ends is correct since markdown normalization already drops author leading/trailing blanks); ③ documented the `go.mod` glamour require as opt-in. Kept (rationale): the CI env (false positive), `actions/checkout@v6` (pre-existing, copied for consistency), terminal-escape note (local single-user data, not a trust boundary; the raw-wrap path has the same property; redacted sharing is explicitly Not Doing).
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10, 11, 12
- **Docs:** [implementation.md#glamour-markdown-behind-a-build-tag-v212](./implementation.md)

### Subtasks
- [x] 13.1 `go get github.com/charmbracelet/glamour@v0.9.1`; applied `/kk:dependency-handling`. **Deviation from "latest v0.x":** pinned **v0.9.1**, not the literal latest v0.x (v0.10.0). v0.9.1 requires lipgloss **exactly v1.1.0** (the project's current pin → zero drift); v0.10.0+ require a lipgloss pre-release pseudo-version (`v1.1.1-0.20250404203927-…`) that MVS would bump **globally** (go.mod is build-tag-agnostic), perturbing the default build. API confirmed: `NewTermRenderer`/`WithAutoStyle`/`WithWordWrap`/`(*TermRenderer).Render`.
- [x] 13.2 **Dependency safety verified:** `go mod graph | grep lipgloss` resolves only `github.com/charmbracelet/lipgloss@v1.1.0`; **no `charm.land/*` anywhere**. (The design-time "current glamour is v2/charm.land" note is stale — `github.com/charmbracelet/glamour` stays on lipgloss v1 through v1.0.0.)
- [x] 13.3 `tui/render_glamour.go` (`//go:build glamour`) + `tui/render_default.go` (`//go:build !glamour`) provide a build-tag-selected `renderBody(role, body, st, width) []string` seam. **Deviation:** routed at `render.go`'s `renderTranscript` body seam (which `viewer.go` calls) rather than editing `viewer.go` directly — a plain build-tagged function, not an interface/func-var. Glamour renders **user/assistant** markdown only (tool/system, zero/negative width, and any glamour error fall back to `wrapBody` — content never lost); a width-keyed cached `*glamour.TermRenderer` (mutex-guarded, lazy-init, rebuilt on width change) avoids per-message renderer construction during a re-wrap pass.
- [x] 13.4 Added `build-glamour` Makefile target (`-tags fts5,glamour`) + a dedicated `glamour` CI job that vets + race-tests the tagged `internal/vault/tui` subset **and** asserts via `go tool nm` that the default binary excludes glamour while the tagged binary includes it (linkage guard prevents bit-rot + accidental unconditional import).
- [x] 13.5 Verified: default `make build` links **0** glamour symbols, `make build-glamour` links them (152; ~10 MB larger); both tag paths build/vet/race-test green; `go mod graph` confirms no `charm.land/lipgloss/v2`. New tests: `render_default_test.go` (default ≡ wrapBody all roles), `render_glamour_test.go` (markdown styled, role/zero-width fallback, observable width-honoring, width-keyed cache).

## Task 14: PreCompact — `vault_snapshots` schema (migration 0002)
- **Status:** dropped — see [precompact-investigation.md](./precompact-investigation.md) §0.6. `/compact` is append-only (no file-level data loss; the sweep already archives the full transcript), so `vault_snapshots` cold storage is unwarranted. Re-trigger conditions in the investigation doc. **Not actionable — retained as rationale only.**
- **Depends on:** Task 0 (favorable timing confirmed), Task 5 (migration runner + codec)
- **Size:** M
- **Can run in parallel with:** Task 6, 8, 9 (after Task 5)
- **Docs:** [implementation.md#vault_snapshots-schema--migration-runner-v213](./implementation.md)

### Subtasks
- [ ] 14.1 Migration `0002_vault_snapshots` (reuses the runner built in Task 5.2): create `vault_snapshots` (snapshot_id PK, session_uuid FK CASCADE, content_hash, size_bytes, captured_at, trigger, **encoding**, raw_jsonl BLOB; `UNIQUE(session_uuid, content_hash)`) + index `(session_uuid, captured_at DESC)`; guarded, idempotent, inside `BeginImmediate`. NOT in FTS
- [ ] 14.2 Add `InsertSnapshot`/`ListSnapshots`/`GetSnapshot` + prepared statements to `store.go` (blob via `encodeBlob`/`decodeBlob` + the `encoding` column). Snapshot `content_hash` = `sha256(rawJSONL)` over the **main transcript only** (not the composite `computeContentHash`) — defines the dedup key
- [ ] 14.3 (Conditional) If the V2.0 corpus measurement shows snapshot growth would exceed the active-session total, add the keep-N-recent-per-session cap here (`DELETE … WHERE snapshot_id NOT IN (… ORDER BY captured_at DESC LIMIT N)` after insert); otherwise keep-all-distinct
- [ ] 14.4 Verify: opening a v1 vault applies `0002` once (re-open no-op); `vault_migrations` records the name; CASCADE removes snapshots when the parent session is deleted

## Task 15: PreCompact hook handler
- **Status:** dropped — with Task 14 (see [precompact-investigation.md](./precompact-investigation.md) §0.6). `handlePreCompact` stays a no-op. **Not actionable — retained as rationale only.**
- **Depends on:** Task 0, Task 14
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#precompact-handler-v214](./implementation.md)

### Subtasks
- [ ] 15.1 Replace the `handlePreCompact` stub: parse the payload (per Task 0) → resolve session file + UUID + project dir
- [ ] 15.2 **Archive the active session FIRST** via the existing single-session import path. Add a `DiscoverSession(dir, uuid)` helper (main `<dir>/<uuid>.jsonl` + `collectAssociatedFiles(<dir>/<uuid>)`) and `Import` that one `SessionFile` — **not** `DiscoverSessions(dir)`-then-filter, which walks every session's sidecar tree on the `/compact` critical path. Reads pre-compaction main + sidecars from disk, creates the parent `vault_sessions` row if absent (satisfies the snapshot FK; avoids a main-only `ReplaceSession` clobbering existing sidecars)
- [ ] 15.3 **Guard the FK, THEN `InsertSnapshot`:** `Import` does not always create a row (Task-11 0-msg exclusion, read/scan error). Confirm the parent exists (Import status or `SessionDigest`) before `InsertSnapshot`; if absent, log and skip the snapshot (no FK crash). Then `InsertSnapshot` (main transcript; dedup via UNIQUE); open→import-one+insert→`Close()` fast (short-lived process); log + swallow errors so `/compact` is never blocked
- [ ] 15.4 Confirm hook wiring in `internal/hook/` dispatch routes PreCompact to the handler
- [ ] 15.5 Verify: a captured-payload fixture **for a brand-new session with no pre-existing `vault_sessions` row** → parent row created, then snapshot inserts without FK error; **a session `Import` produces no row for (0-msg/read error)** → snapshot skipped with a log, no FK crash; a second identical invocation dedups; an existing session with sidecars keeps them (no clobber)

## Task 16: Snapshot CLI — `snapshots` + `restore --snapshot`
- **Status:** dropped — with Tasks 14–15 (see [precompact-investigation.md](./precompact-investigation.md) §0.6). No `vault_snapshots` table ships, so there is nothing to list/restore. **Not actionable — retained as rationale only.**
- **Depends on:** Task 14, Task 15
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [implementation.md#snapshot-cli-v215](./implementation.md)

### Subtasks
- [ ] 16.1 Add `snapshots <id>` subcommand (list hash/size/captured_at, `--json`)
- [ ] 16.2 Extend `restore` with `--snapshot <hash>` → `RestoreSession(uuid, snapshotJSONL, nil, …)` (nil files — snapshots store no sidecars, so this is **main-transcript-only by design**: `/compact` only mutates main; sidecars come from the active row). Document the main-only behavior AND delete-cascades-snapshots in `--help`
- [ ] 16.3 Verify: `snapshots <id>` lists; `restore <id> --snapshot <hash>` writes the main transcript only (no `<uuid>/` tree); `delete <id>` removes the session AND its snapshots

## Task 17: Final verification
- **Status:** pending
- **Depends on:** Task 1, 2, 4–13 (Tasks 3, 14–16 dropped)
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 17.1 Run `/kk:test` — full suite (`-tags fts5`, both keys, race); no regressions in existing tests
- [ ] 17.2 Run `/kk:document` — update `docs/architecture.md`, `CLAUDE.md`, AND `README.md` (new subcommands `compact`/`merge`/`rekey`, `-tags glamour` build, `CAPY_VAULT_SWEEP_ALL`/`CAPY_VAULT_MERGE_KEY` env vars)
- [ ] 17.3 Run `/kk:review-code go` — review the full v2 diff
- [ ] 17.4 Run `/kk:review-spec` — verify implementation matches design.md + implementation.md

## Task 18: Design addenda (post-plan follow-ups)
- **Status:** in-progress (A1, A2, A3 done; A4 pending)
- **Depends on:** —
- **Size:** per-addendum (currently M)
- **Docs:** [design.md § Addenda](./design.md#addenda)

Tracks the items appended to design.md § Addenda after the initial v2 plan — each a
self-contained follow-up outside the Task 0–17 graph (so Task 17 final verification
does not gate them; each addendum carries its own verification). Numbered 18 to
avoid renumbering the existing plan.

### Addenda
- [x] A1 — TUI collapse-then-open for large `tool_result` bodies in the `--tui`
  viewer (any tool past a size threshold; the FTS-excluded Read/NotebookRead set is
  a subset). Plain `vault show` unchanged. Reuses the openable-marker mechanism but
  needs a new "open inline content" target since the body is inline in `raw_jsonl`,
  not a sidecar file. Display-only; FTS unaffected. See design.md § Addenda A1.
  **Done.** `transcript.go`: viewer-specific `splitUserContentForViewer` keeps the
  full body (vs the shared `renderUserContent`, untouched, that `vault show` keeps
  using) + `overCollapseThreshold` (>20 lines OR >2000 bytes) + `TranscriptMessage.
  Collapsed/ToolSummary`. `tui/render.go`: collapsed RoleTool → openable marker
  (`toolMarkerRow`/`markerRowFor`). `tui/viewer.go`: `inInline`/`inlineLabel` detail
  state + `inDetail()` generalization of the `inSub` paths + `openInlineContent`
  (distinct from `openSubagent` — renders the carried body, esc/q returns). Tests in
  `transcript_test.go` + new `tui/collapse_test.go`. Isolated review
  (code-reviewer + pal/gemini-3.5-flash): no P0/P1 correctness bugs; applied the
  agreed P1 (stale `renderUserContent`/`collapsedToolResult` "shared with
  transcript.go" doc comments corrected) + clarity/test-coverage nits (threshold
  rationale, byte-threshold test, `markerRowFor`/guard comments). Declined pal's
  slice-init nit (mirrors the sibling `renderUserContent` nil-slice idiom; nil-safe).
- [x] A2 — Recover in-flight (queued) user messages. A message the user submits
  while the assistant is mid-turn is written as a top-level `attachment` object
  (`{"type":"attachment","attachment":{"type":"queued_command","prompt":"…",
  "commandMode":"prompt"}}`) with **no `message` field** — so all three vault
  JSONL parsers dropped it: the scanner's `attachment` case only read
  `message.content` (FTS), and render/transcript had no `attachment` case at all
  (display). The message was lost from `vault show`, the TUI viewer, **and** FTS
  search. See design.md § Addenda A2.
  **Done.** `scanner_types.go`: added `jsonlLine.Attachment`. `scanner.go`: shared
  `queuedCommandPrompt()` (type-discriminates `queued_command`, ignores
  `task_reminder`/others) + `userTextContent()` (normalizes the prompt into the
  equivalent user `message.content` so all three readers reuse their existing user
  path). All three parsers gained an `attachment` case that recovers the prompt as
  a **user turn** — indexed `role=user` (plain, no annotation, counts toward
  MessageCount) and displayed as a user message. **Display decision (user-chosen):
  annotate the turn "· queued"** — a display-only `queued`/`Queued` flag on
  `displayMsg`/`renderEntry` (render.go) and `TranscriptMessage`/`transcriptEntry`
  (transcript.go); the role stays `user` (so style, `--role user` filter, and
  MessageCount are untouched), only the header label gets the suffix
  (`[You · queued]` / `## 👤 You · queued` / TUI `▌ You · queued` via
  `messageHeader(role, queued)`). The bracketing `queue-operation` enqueue/remove
  lines carry the same text but are operational metadata (no parser handles their
  type) → not indexed, so no duplication. **Deferred (documented):** a message
  enqueued but never dequeued (session ended while queued) has no `queued_command`
  line and is intentionally not surfaced — it was never sent to the model (rationale
  in `queuedCommandPrompt`'s doc comment). The knowledge-base session sweep
  (`internal/session`) is a separate parser out of A2's vault scope. Tests:
  `scanner_test.go` (indexed once as user, anchored, MessageCount, task_reminder
  ignored, enqueue not duplicated), `render_test.go` (text + markdown annotation,
  task_reminder ignored), `transcript_test.go` (RoleUser + Queued + anchor),
  `tui/render_test.go` (header annotation). Default + glamour builds, race + vet
  clean. Isolated review (code-reviewer + pal/gemini-3.1-pro): APPROVE, no P0/P1;
  applied both corroborated nits (clarifying comment on the scanner's early
  `return`; strengthened `userTextContent` discard comment — kept the `_` to match
  the `json.Marshal` convention in `internal/store/chunk.go`, since `return nil`
  would silently drop the message). Dismissed the `"operation":"remove"` test nit
  (verified `"remove"` is the real value in `.files/9f153112.json`).

- [x] A3 — Display Edit/Write tool results as a diff-view in the TUI viewer. See
  design.md § Addenda A3. **Finding:** unlike Read (a file-dump body), an Edit/Write
  `tool_result.content` is a one-line "updated successfully" string; the real change
  lives in the sibling `toolUseResult.structuredPatch` (a pre-computed unified diff),
  which the scanner never parsed. **Done.** New `diffResultTools = {Edit, Write}`
  category (distinct from `excludedResultTools`): **FTS** excludes their success
  bodies (`scanner.go ftsExcludedResult`; in-place, no `index_version` bump on the
  unreleased branch); **plain `show` unchanged** (keeps the verbatim success string —
  user decision); **TUI** collapses to a marker showing a `(+a −b)` stat that expands
  to the reconstructed diff, colored by line prefix (`tui/render.go renderDiffBody`:
  `+` green / `-` red / `@@` cyan, bypassing the glamour `renderBody` seam).
  `internal/vault/diff.go` (`diffBodyFromToolResult`) keeps diff TEXT in package
  `vault`, color in `tui`. `jsonlLine.ToolUseResult` threaded through
  `transcriptEntry` → `splitUserContentForViewer`; `TranscriptMessage.Diff` carries
  the flag. Fallback: an Edit with no patch renders its plain success body. MultiEdit
  out of scope (absent from corpus; trivial to add). Tests: `scanner_test.go`
  (Edit result FTS-excluded, call still indexed), `diff_test.go` (Edit/Write/multi-
  hunk/no-patch parsing), `transcript_test.go` (diff marker + no-patch fallback),
  `tui/collapse_test.go` (marker stat + expand shows diff). Default + glamour builds,
  race + vet clean. Isolated review (code-reviewer + pal/gemini-3.1-pro): no P0/P1.
  Applied: ① `diffConsumed` guard enforcing one-diff-per-line (reviewer's P1 was
  empirically impossible — 0/15506 corpus lines have >1 tool_result — but the guard
  converts the documented assumption into an invariant); ② skip empty-`lines` hunks in
  `diffBodyFromToolResult` (→ `ok=false` fallback to the success body, no header-only
  artifact); ③ dropped the unused `toolResultPatch.FilePath`; ④ pre-allocated
  `renderDiffBody` rows; ⑤ gofmt-realigned the `transcriptEntry` struct my edit
  dirtied. Declined: `diffResultTools` as a mutable `var` (mirrors the existing
  `excludedResultTools` pattern). The other four files gofmt flags were pre-existing
  (HEAD already dirty) — left untouched.

## Dependency Graph

```
Task 0 (investigate) ── DONE → Tasks 14→15→16 (PreCompact archival) DROPPED (append-only /compact; see precompact-investigation.md)
                                                                                                                    │
Task 5 (codec + migration runner) ──┬─→ Task 6 (compact) ──────────────────────────────────────────────────────────┤
                                     ├─→ Task 9 (merge) ───────────────────────────────────────────────────────────┤
Task 11 (0-msg) ─────────────────────┘   (Task 9 needs Task 5 + Task 11; subtask 9.4 dropped with Tasks 14–16)      │
                                                                                                                    │
Task 7 (rekey extract) ─→ Task 8 (vault rekey) ─────────────────────────────────────────────────────────────────────┤
                                                                                                                    │
Task 4 (vault ctx) ─────────────────────────────────────────────────────────────────────────────────────────────────┤
                                  [Task 3 store-side ctx DROPPED — no functional beneficiary; see Task 3]            │
                                                                                                                    │
Task 2 (SessionDir) ────────────────────────────────────────────────────────────────────────────────────────────→ │
Task 10 (all-proj sweep) ───────────────────────────────────────────────────────────────────────────────────────────┤
Task 11 ────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
Task 12 (TUI keys) ───────────────────────────────────────────────────────────────────────────────────────────────────┤
Task 13 (TUI glamour) ──────────────────────────────────────────────────────────────────────────────────────────────┴─→ Task 17 (final)
```

## Numbering note

Design/implementation docs use `V2.N` feature labels; this file uses `Task N`. They are **not** 1:1 (e.g. design `V2.4` 0-msg = `Task 11`; `V2.11` TUI keys = `Task 12`). The `Docs:` link on each task is the authoritative bridge to its implementation.md section.
