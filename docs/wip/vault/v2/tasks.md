# Tasks: Vault v2

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Parent (v1): [../tasks.md](../tasks.md)
> Status: pending
> Created: 2026-06-05
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic retention, redacted sharing/export, TUI lazy-windowing viewer, TUI 3-panel split, encryptPlain (unencrypted→encrypted) extraction, snapshot retention eviction policy, store-side context.Context propagation (Task 3 dropped — deferred until a store-side cancelling caller exists)

Single flat plan (no phase boundary). Task 0 is the only hard gate and gates ONLY the PreCompact tasks (14–16); all other tasks proceed independently. Build/test with `-tags fts5`; `CAPY_DB_KEY` + `CAPY_VAULT_KEY` required.

**Tx-helper note:** tasks that open a write transaction use the vault tx helper — the local `beginImmediate` (`migrations.go`) before Task 1 lands, or `sqliteutil.BeginImmediate` after. Either works; this is **not** a hard ordering dependency on Task 1.

## Task 0: Investigate PreCompact hook payload
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1–13
- **Slicing strategy:** Risk-First (highest uncertainty; gates Tasks 14–16)
- **Docs:** [implementation.md#v20--investigate-precompact-payload-risk-first-gates-v213-15](./implementation.md)

### Subtasks
- [ ] 0.1 In `internal/hook/precompact.go`, add a debug branch gated behind `CAPY_DEBUG_PRECOMPACT=1` that writes the raw `input []byte` to an `os.CreateTemp` file (0600) and logs the path to stderr
- [ ] 0.2 Trigger `/compact` in a real Claude Code session; capture the payload
- [ ] 0.3 Document the JSON shape in `docs/wip/vault/v2/precompact-investigation.md`: is the session file path present? session ID? project dir?
- [ ] 0.4 Verify timing at the **content level, not mtime**: capture a copy of the session-file contents AT hook time, then diff against the post-`/compact` file — confirm the hook-time copy still contains pre-compaction turns the compacted file lost. (mtime alone can't distinguish pre- from post-mutation.)
- [ ] 0.5 Ensure the debug branch is a no-op when the env var is unset (no behavior change shipped)
- [ ] 0.6 **Decision gate:** if the hook-time content is already the compacted transcript (pre-compaction turns absent), STOP — file-based capture is impossible; re-scope Tasks 14–16 per design.md §PreCompact (SessionStart-cached copy, or drop). Record the decision in the investigation doc

## Task 1: Consolidate `beginImmediate`/`isBusy` into `sqliteutil`
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 2, 5–13
- **Docs:** [implementation.md#beginimmediateisbusy-consolidation-v21](./implementation.md)

### Subtasks
- [ ] 1.1 Add exported `BeginImmediate(db *sql.DB) (*sql.Tx, error)` and `IsBusy(error) bool` to `internal/sqliteutil/sqliteutil.go` (parameterize or keep a thin per-store wrapper for the meta-table no-op write)
- [ ] 1.2 Delete the copies and update all call sites. Locations (verified): vault — both in `internal/vault/migrations.go` (incl. the `TODO`); store — `beginImmediate` in `internal/store/migrate.go:109`, `isBusy` in `internal/store/retry.go:14`
- [ ] 1.3 Verify: `make test-race` green; `grep -rn "func beginImmediate\|func isBusy" internal/` returns only `sqliteutil`

## Task 2: Route `session.SessionDir()` through `config.ClaudeProjectsDir()`
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 1, 5–13
- **Docs:** [implementation.md#sessiondir-routing-v23](./implementation.md)

### Subtasks
- [ ] 2.1 Replace the hardcoded `~/.claude/projects/` base in `internal/session/sweep.go:SessionDir()` with `config.ClaudeProjectsDir()` (already `CLAUDE_CONFIG_DIR`-aware)
- [ ] 2.2 Verify: `go test -tags fts5 ./internal/session/... ./internal/config/...` green; a `CLAUDE_CONFIG_DIR`-set test resolves the overridden root

## Task 3: `context.Context` propagation — `internal/store` — DROPPED
- **Status:** dropped
- **Size:** — (removed from v2 scope)
- **Rationale:** Converting the encrypted knowledge store to `*Context` variants has **no functional beneficiary.** The store has no cancelling callers, and the only long-running cancellable operation — the Task 10 all-projects sweep — lives in `internal/vault` and already gets cooperative cancellation from `Import`'s per-session loop check (`import.go:136`). It would spend regression risk on encryption-critical code (CLAUDE.md's foremost invariant, "Encryption is mandatory") purely for sibling symmetry with the vault. "No diff is the safest diff" on encryption paths.
- **Re-trigger:** revisit only when a store-side caller genuinely needs sub-transaction cancellation. Until then `internal/store` keeps its plain `db.Query`/`Exec`/`Begin` calls. Tracked here so the decision is durable, not a chat-only aside.
**Not actionable — dropped, retained as rationale only (do NOT implement).** The original plan was to convert `internal/store` `db.Query`/`Exec`/`QueryRow`/`Begin` → `*Context` variants and add a leading `ctx context.Context` to public methods lacking one. This is **not** done in v2 (see Rationale above). `internal/store` keeps its plain `db.Query`/`Exec`/`Begin` calls. Re-open only when a store-side cancelling caller genuinely needs sub-transaction cancellation.

## Task 4: `context.Context` propagation — `internal/vault`
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10–13
- **Slicing strategy:** isolated behavior-preserving commit (the sole ctx task now that store-side Task 3 is dropped)
- **Docs:** [implementation.md — context.Context propagation, vault only](./implementation.md)
- **Note:** kept (unlike dropped Task 3) because the cancelling caller — the Task 10 all-projects sweep — lives in this package; threading `ctx` makes vault's public API cancellation-ready where it matters.

### Subtasks
- [ ] 4.1 Convert `internal/vault` DB calls → `*Context` variants; replace `VaultStore.ctx()` (returns `context.Background()`) with a real threaded `ctx`; add leading `ctx` to public methods (`GetSession`, `ListSessions`, `Search`, `Stats`, `InsertSession`, `ReplaceSession`, `DeleteSession`, `WriteBatch`, …)
- [ ] 4.2 Update CLI callers (`cmd.Context()`) and `server.vaultSweep` (`sweepCtx`) to pass context
- [ ] 4.3 Verify: `CAPY_VAULT_KEY=… go test -tags fts5 -count=1 -race ./internal/vault/... ./cmd/capy/... ./internal/server/...` green

## Task 5: zstd BLOB codec + compress-on-write / decompress-on-read
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 7, 10, 11, 12, 13
- **Slicing strategy:** Vertical (write→store→read all exercised end-to-end)
- **Docs:** [implementation.md#zstd-codec-v25](./implementation.md)

### Subtasks
- [ ] 5.1 `go get github.com/klauspost/compress` + `go mod tidy`; apply `/kk:dependency-handling` to confirm the resolved `EncodeAll`/`DecodeAll` API
- [ ] 5.2 **Build the migration runner** (`vaultMigrationApplied` + apply-loop in `migrations.go`, mirroring `internal/store/migrate.go`) and migration `0001_blob_encoding`: `ALTER TABLE vault_sessions ADD COLUMN encoding TEXT` + same on `vault_files` (legacy rows → NULL = raw)
- [ ] 5.3 Create `internal/vault/codec.go` — shared package-level `*zstd.Encoder`/`*zstd.Decoder`; `encodeBlob([]byte) (data []byte, encoding string)` (returns `"raw"` when `CAPY_VAULT_NO_COMPRESS` set or not smaller, else `"zstd"`); `decodeBlob(encoding string, b []byte) ([]byte, error)` switching on the column. **No magic-byte detection** (unsafe for arbitrary sidecars — the `encoding` column is authoritative)
- [ ] 5.4 Wire write side: add `encoding` to `vault_sessions`/`vault_files` INSERT+UPDATE statements; `writeRecord` (raw_jsonl) and `writeChildren` (file content) call `encodeBlob` and store the returned encoding. On first `zstd` write, set `vault_meta` `min_reader_version` = `"2"`. **Add the matching open-time check in `openDB()`** (after the canary): read `min_reader_version` and refuse with a clear error if it exceeds a `supportedReaderVersion` constant (`2`) — without the read step the marker protects no one
- [ ] 5.5 Wire read side: add `encoding` to `sessionMetaColumns` + `GetFiles` SELECT; thread it into `decodeBlob` in `scanSessionMeta`/`GetSession` and `GetFiles`
- [ ] 5.6 Confirm `content_hash`/`size_bytes`/FTS still computed on UNCOMPRESSED bytes (no change to `computeContentHash`/`buildRecord`)
- [ ] 5.7 Tests: round-trip Insert→Get byte-identical; compressible row stored `encoding='zstd'` (raw blob carries zstd magic); legacy `encoding IS NULL` row reads correctly (mixed corpus); **regression: a raw sidecar fixture whose first bytes are `0x28B52FFD` round-trips unchanged**; `content_hash`/`size_bytes` identical to v1; `min_reader_version` set after first compressed write; **a vault whose `min_reader_version` is hand-set to `"3"` fails to open with the version error, while `"2"`/absent opens fine**
- [ ] 5.8 Verify: `go test -tags fts5 ./internal/vault/...` green

## Task 6: `capy vault compact` (recompress + VACUUM)
- **Status:** pending
- **Depends on:** Task 5
- **Size:** S
- **Can run in parallel with:** Task 8, 9 (after Task 5)
- **Docs:** [implementation.md#capy-vault-compact-v26](./implementation.md)

### Subtasks
- [ ] 6.1 **Busy pre-check before the rewrite phase** (a `Checkpoint()` reporting busy pages → abort "stop the server first"), so `Compact` fails fast instead of doing all the UPDATE work then hitting contention at VACUUM
- [ ] 6.2 Add `VaultStore.Compact()` — rewrite **legacy** rows `WHERE encoding IS NULL` only (`'raw'` is the terminal "compression declined" state; selecting it too would prevent a clean no-op) via batched `BeginImmediate` UPDATEs that `SET raw_jsonl = ?, encoding = ?` with **the encoding `encodeBlob` returns** (`'zstd'` or `'raw'`) — never hard-coded `'zstd'` (that would mislabel an incompressible blob and corrupt its read). Same for `vault_files.raw_content`. Abort early with a clear error if `CAPY_VAULT_NO_COMPRESS` is set (it would compress nothing)
- [ ] 6.3 Run `VACUUM` on a dedicated single connection opened after the pool closes (mirror `Checkpoint`), with `PRAGMA temp_store = MEMORY` so the transient copy isn't written to an unencrypted on-disk temp (VACUUM itself is lock-protected, unlike rekey's swap)
- [ ] 6.4 Add the `compact` subcommand to `cmd/capy/vault.go`; report before/after file size (`os.Stat`)
- [ ] 6.5 Verify: a legacy (`encoding IS NULL`) fixture **including one incompressible blob** compacts → no row left `NULL`, each is `'zstd'` (carries the zstd magic) or `'raw'` (the incompressible one, round-trips byte-identical), file size dropped, `search`/`show` unchanged; a second `Compact` is a true no-op; `Compact` under `CAPY_VAULT_NO_COMPRESS` errors without modifying the DB

## Task 7: Extract shared `Rekey` helper from `cmd/capy/encrypt.go`
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 10–13
- **Slicing strategy:** isolated behavior-preserving commit — ⚠ encryption-critical, shared with knowledge store
- **Docs:** [implementation.md#shared-rekey-extraction-v27](./implementation.md)

### Subtasks
- [ ] 7.1 Add `sqliteutil.Rekey(dbPath, oldKey, newKey string) (RekeyResult, error)` (backup-API rotation: open old → checkpoint → backup-copy into temp opened with new key → swap+verify); bring `openEncrypted`/`backupDB`/`swapAndVerify`/`checkpointDB` into `sqliteutil`. **Strip the hard-coded `capy encrypt:` stdout/stderr out of `swapAndVerify`** (`encrypt.go:261,269,282,283`): the util returns `RekeyResult` (incl. the `.bak` path) and wrapped errors; the cmd layer prints. A low-level util must not do user-facing I/O, and `vault rekey` must not emit "capy encrypt:"
- [ ] 7.2 Rewire `cmd/capy/encrypt.go:rekeyEncrypted` to call `sqliteutil.Rekey` and print its own messages from `RekeyResult`; leave `encryptPlain` in `cmd/capy`
- [ ] 7.3 Verify: `go test -tags fts5 ./internal/store/... ./cmd/capy/...` green; the existing rekey round-trip in `encryption_lifecycle_test.go` passes unchanged; **`capy encrypt`'s stdout/stderr is unchanged** after moving the prints to the cmd layer

## Task 8: `capy vault rekey` command
- **Status:** pending
- **Depends on:** Task 7
- **Size:** S
- **Can run in parallel with:** Task 6, 9
- **Docs:** [implementation.md#capy-vault-rekey-v28](./implementation.md)

### Subtasks
- [ ] 8.1 Add the `rekey` subcommand to `cmd/capy/vault.go`: prompt current passphrase, read new key from `CAPY_VAULT_KEY` (error if unset — note this differs from `capy encrypt`, which prompts when its env key is unset). **Confirm before rotating** ("rotating to the key currently in `CAPY_VAULT_KEY` — proceed?") and **reject new == old** (a forgotten env update would silently no-op-rotate a compromised key), then call `sqliteutil.Rekey(vault.VaultDBPath(), old, new)`
- [ ] 8.2 **Do NOT add a `VaultStore.Checkpoint()` pre-check** — it opens with `CAPY_VAULT_KEY` = the *new* key and fails on the old-key DB; the WAL flush happens inside `Rekey` on the old-key source connection. Document a hard "stop the MCP server first" requirement in `--help` + README (the file-swap `rename` isn't lock-protected; the check is best-effort only). **Handle the old-key `.bak`:** print a prominent warning that `<vault>.bak` stays decryptable by the old (compromised) key, and accept `--remove-backup` to unlink it after the new file verifies open — documenting that this is deletion, not guaranteed erasure (SSD/CoW)
- [ ] 8.3 Verify: import → `rekey` (old→new) → reopen with new key lists same sessions; reopen with old key fails; `.bak` exists by default and is removed with `--remove-backup`; `rekey` is rejected when `CAPY_VAULT_KEY` equals the old key

## Task 9: `capy vault merge --from <path>`
- **Status:** pending
- **Depends on:** Task 5, Task 11 (and **Task 14 for subtask 9.4 only** — it consumes `InsertSnapshot`/`vault_snapshots`; on the shared branch 9.4 must land after Task 14's commit)
- **Size:** M
- **Can run in parallel with:** Task 6, 8 (after Task 5 + Task 11)
- **Slicing strategy:** Contract-First (source-vault read boundary), then idempotent upsert reuse
- **Docs:** [implementation.md#capy-vault-merge---from-v29](./implementation.md)
- **Why Task 11 dep:** 9.3 reuses Task 11's `StatusExcluded` for the 0-msg guard — a hard code dependency, not just sequencing.

### Subtasks
- [ ] 9.1 Add `sqliteutil.OpenSourceForMerge(dbPath, key)` (renamed from `OpenReadOnly` — it checkpoints the source, a **write**, so it needs a writable source file + dir; temp-copy a read-only source; document on `--help`) — does NOT run `schemaSQL`/`migrateVault` against the source, and **checkpoints any pending WAL first** (a copied-live source vault may carry a `-wal`; do NOT use `immutable=1`, which silently skips WAL-resident rows)
- [ ] 9.2 Extract `buildFTS(uuid, mainBytes, files) []FTSRow` from `import.go:buildRecord` so disk-import and merge share the scanner wiring
- [ ] 9.3 Create `internal/vault/merge.go` — `MergeFrom(ctx, dest, srcPath, srcKey, opts)`: **feature-detect the source schema first** (`PRAGMA table_info` for the `encoding` column — absent ⇒ treat blobs as `raw`; `sqlite_master` for `vault_snapshots` — absent ⇒ skip 9.4) so a v1 source doesn't raise "no such column/table"; iterate source `vault_sessions`+`vault_files`, `decodeBlob(encoding, …)`, apply `dest.SessionDigest` idempotent decision (skip same-hash/smaller, else insert/replace), carry source `machine_id`/`claude_project_dir`/`project_path`/`git_branch` verbatim, apply the 0-msg guard (Task 11's `StatusExcluded`), batch via `WriteBatch`
- [ ] 9.4 **Carry snapshots (depends on Task 14):** when the source has a `vault_snapshots` table (per the 9.3 probe), iterate it per UUID after the parent row exists and `InsertSnapshot` into the destination (dedup via `UNIQUE(session_uuid, content_hash)`); **skip the step entirely when the source lacks the table** — a true runtime no-op, not a "no such table" error. The destination always has the table + `InsertSnapshot` (Task 14 ships in this v2 unit), so only the source can lack it
- [ ] 9.5 Add the `merge` subcommand to `cmd/capy/vault.go`: `--from` (required), `--key`/`CAPY_VAULT_MERGE_KEY`, `--dry-run`, `--project`; `import`-style table output. Concurrency: writes only the destination via batched `beginImmediate`, so a concurrent server sweep is absorbed by `busy_timeout`+retry (like `import`) — no "stop the server" requirement (unlike `rekey`) and no busy pre-check (unlike `compact`); note this in `--help`
- [ ] 9.6 Verify: fixture vaults (overlapping + distinct UUIDs; one v2 source with snapshots; **one v1-shaped source with no `encoding` column and no `vault_snapshots` table**) → `merge` brings in distinct sessions, keeps larger overlaps, `search` finds source-only content, source `machine_id`/`project_path` preserved, **source snapshots appear in dest (deduped)**, and **the v1-shaped source merges cleanly** (blobs read as raw, snapshot step skipped — no "no such column/table"); `--dry-run` writes nothing

## Task 10: All-projects opt-in server sweep
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 11–13
- **Docs:** [implementation.md#all-projects-opt-in-sweep-v210](./implementation.md)

### Subtasks
- [ ] 10.1 In `internal/server/server.go:vaultSweep`, branch on `CAPY_VAULT_SWEEP_ALL`: when set, discover from `config.ClaudeProjectsDir()` (root) instead of `ProjectSessionDir(s.projectDir)`; keep `ctx`/`bgWg`/`Open()` probe/`Close()` intact; log a per-run summary
- [ ] 10.2 Verify: server-startup test with `CAPY_VAULT_SWEEP_ALL=1` + multi-project fixture imports from >1 project; unset → current project only

## Task 11: Exclude 0-message sessions from import
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10, 12, 13
- **Docs:** [implementation.md#exclude-0-msg-sessions-v24](./implementation.md)

### Subtasks
- [ ] 11.1 Add `StatusExcluded = "excluded"` to `internal/vault/import.go` status constants + `ImportResult` accounting + CLI table output
- [ ] 11.2 In the `Import` loop, after `buildRecord`, skip sessions with `rec.Session.MessageCount == 0` (record `StatusExcluded`, don't batch)
- [ ] 11.3 Verify: a fixture dir with a 0-msg + a normal session → 0-msg excluded (absent from `list`), normal imported; re-import after it gains messages archives it

## Task 12: TUI completion — keybindings + in-list filter
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10, 11, 13
- **Risk focus:** `r`/`R` are the destructive/exec surface — must teardown bubbletea (release alt-screen + TTY) before exec, same care as v1 Task 4b
- **Docs:** [implementation.md#keybindings--in-list-filter-v211](./implementation.md)

### Subtasks
- [ ] 12.1 `f` — in-list project filter (predicate over `ListSessions(Project:)`); update `app.go` dispatch + status-bar hints in `styles.go`
- [ ] 12.2 `c` — copy current message to clipboard via OSC-52 escape to stdout (no native dep); always show a **status-bar confirmation** afterward (OSC-52 silently no-ops on terminals/multiplexers without clipboard passthrough, so the user must get explicit feedback)
- [ ] 12.3 `r`/`R` — emit a `tea.Quit`-then-action intent carrying the UUID/dir; perform restore/`claude --resume` from the CLI layer after `Run()` returns
- [ ] 12.4 Verify: `internal/vault/tui` unit tests cover filter narrowing, OSC-52 emission + status-bar confirmation, and the quit-then-action intent for `r`/`R`; race-clean

## Task 13: TUI glamour markdown rendering behind a build tag
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 7, 10, 11, 12
- **Docs:** [implementation.md#glamour-markdown-behind-a-build-tag-v212](./implementation.md)

### Subtasks
- [ ] 13.1 `go get github.com/charmbracelet/glamour@<latest-v0.x>` (the `github.com/charmbracelet/...` path, NOT `charm.land/.../v2`); apply `/kk:dependency-handling`
- [ ] 13.2 **Verify dependency safety:** `go mod graph | grep lipgloss` shows only `github.com/charmbracelet/lipgloss` (v1) and NO `charm.land/lipgloss/v2`. If glamour pulls lipgloss v2, pin a lower glamour version
- [ ] 13.3 Create `tui/render_glamour.go` (`//go:build glamour`) using `glamour.NewTermRenderer(WithAutoStyle(), WithWordWrap(width))`; `tui/render_default.go` (`//go:build !glamour`) keeps lipgloss-only; route `viewer.go` through the active renderer
- [ ] 13.4 Add a `build-glamour` Makefile target (`-tags fts5,glamour`) and a CI matrix entry that builds + runs the `tui` tests with the tag, so the tagged path doesn't bit-rot (the default build never compiles it)
- [ ] 13.5 Verify: `make build` (default) succeeds and the binary does NOT link glamour; `make build-glamour` succeeds and renders markdown; CI runs both; `go mod graph` confirms no `charm.land/lipgloss/v2`

## Task 14: PreCompact — `vault_snapshots` schema (migration 0002)
- **Status:** pending
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
- **Status:** pending
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
- **Status:** pending
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
- **Depends on:** Task 1, 2, 4–16 (Task 3 dropped)
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 17.1 Run `/kk:test` — full suite (`-tags fts5`, both keys, race); no regressions in existing tests
- [ ] 17.2 Run `/kk:document` — update `docs/architecture.md`, `CLAUDE.md`, AND `README.md` (new subcommands `compact`/`merge`/`rekey`/`snapshots`, `-tags glamour` build, `CAPY_VAULT_SWEEP_ALL`/`CAPY_VAULT_MERGE_KEY` env vars)
- [ ] 17.3 Run `/kk:review-code go` — review the full v2 diff
- [ ] 17.4 Run `/kk:review-spec` — verify implementation matches design.md + implementation.md

## Dependency Graph

```
Task 0 (investigate) ──────────────────┬─→ Task 14 (snapshots schema) ─→ Task 15 (hook) ─→ Task 16 (snapshot CLI) ─┐
                                        │                                                                            │
Task 5 (codec + migration runner) ──┬─→ Task 6 (compact) ──────────────────────────────────────────────────────────┤
                                     ├─→ Task 9 (merge) ───────────────────────────────────────────────────────────┤
Task 11 (0-msg) ─────────────────────┘   (Task 9 needs Task 5 + Task 11; subtask 9.4 also needs Task 14)            │
Task 5 ──────────────────────────────────→ Task 14 ──→ Task 9.4 (snapshot carry consumes InsertSnapshot)            │
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
