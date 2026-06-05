# Vault v2 — Implementation Plan

> Design: [./design.md](./design.md) · Tasks: [./tasks.md](./tasks.md)
> Parent v1: [../implementation.md](../implementation.md)

Audience: an experienced Go developer with zero context for this codebase. Build/test always use `-tags fts5`; tests require `CAPY_DB_KEY` and `CAPY_VAULT_KEY`. The `go` profile's `database.md` applies: parameterized queries only, `defer rows.Close()`, `errors.Is(err, sql.ErrNoRows)`, batched writes, human-reviewed schema DDL.

Read first (real touch-points referenced below):
- `internal/vault/store.go` — `VaultStore`, `writeRecord`/`writeChildren`, `scanSessionMeta`, `GetSession`/`GetFiles`, `Checkpoint`, prepared statements.
- `internal/vault/import.go` — `Import`, `buildRecord`, `computeContentHash`, status constants.
- `internal/vault/migrations.go` — `migrateVault`, `ensureVaultMigrationsTable`, `beginImmediate`, `isBusy` (with `TODO` to move).
- `cmd/capy/encrypt.go` — `rekeyEncrypted`, `openEncrypted`, `backupDB`, `swapAndVerify`, `checkpointDB`.
- `internal/server/server.go:vaultSweep` — current-project sweep, `bgWg`, cooperative `ctx`.
- `internal/store/migrate.go` — the migration pattern to mirror for `vault_snapshots`.
- `internal/sqliteutil/sqliteutil.go` — shared open/recovery; new home for tx helpers + `Rekey`.

---

## Tech debt (independent, low-risk — land early)

### `beginImmediate`/`isBusy` consolidation (V2.1)

The helpers are split across files (verified): in the **store**, `beginImmediate` is in `internal/store/migrate.go:109` and `isBusy` in `internal/store/retry.go:14`; in the **vault**, both are in `internal/vault/migrations.go:52-97`. Move all into `internal/sqliteutil` as exported `BeginImmediate(db *sql.DB) (*sql.Tx, error)` and `IsBusy(error) bool`. The no-op write currently targets each package's own meta table (`vault_meta` / store's `sources`) — parameterize the lock table or have each store keep a thin wrapper that supplies its meta-table no-op write (`sqlite_master` reads don't take the RESERVED lock, so a per-store sentinel write is still needed).

- Step: add `BeginImmediate`/`IsBusy` to `sqliteutil`; delete the vault + store copies; update call sites → verify: `make test-race` green; `grep -rn "func beginImmediate\|func isBusy" internal/` returns only `sqliteutil`.

### `SessionDir()` routing (V2.3)

`internal/session/sweep.go:SessionDir()` hardcodes `~/.claude/projects/`. Route it through `config.ClaudeProjectsDir()` (already `CLAUDE_CONFIG_DIR`-aware since v1 Task 3.1).

- Step: replace the hardcoded base in `SessionDir()` with `config.ClaudeProjectsDir()` → verify: `go test -tags fts5 ./internal/session/... ./internal/config/...` green; a `CLAUDE_CONFIG_DIR`-set test resolves the overridden root.

### `context.Context` propagation, both stores (V2.2a, V2.2b)

Two behavior-preserving commits. **High blast radius — isolate each; do not bundle with feature work.** **Before implementing, confirm scope (see design §7):** V2.2a (store) has **no functional beneficiary** — the only cancellable caller (the V2.10 sweep) is in `internal/vault` and already gets loop-level cancellation via `import.go:136`; `store.go:200` `ctx()` returns `context.Background()`. The reviewed recommendation is to **do V2.2b (vault) and defer V2.2a (store)** until a store-side cancelling caller exists ("no diff is the safest diff" on encryption-critical code). The `Task 4 → Task 3` ordering is sequencing preference, not a code dependency, so decoupling makes deferral free. Retained as planned only because the user chose full sibling consistency.

- **V2.2a `internal/store` (deferral candidate):** convert `db.Query`/`Exec`/`QueryRow`/`Begin` to `*Context` variants threading the store's context; add `ctx context.Context` as the leading param to public methods that don't already take one. Keep behavior identical (pass `context.Background()` from callers that have none yet).
  - Step → verify: `CAPY_DB_KEY=… go test -tags fts5 -count=1 -race ./internal/store/...` passes **unchanged**; the bench gate (`make bench-quality`) shows no regression.
- **V2.2b `internal/vault`:** same conversion; replace `s.ctx()` (returns `context.Background()`) usage with a real threaded `ctx`. The public methods (`GetSession`, `ListSessions`, `Search`, `Stats`, `InsertSession`, etc.) gain a leading `ctx`. Update CLI + server-sweep callers to pass their context (`cmd.Context()`, `sweepCtx`).
  - Step → verify: `CAPY_VAULT_KEY=… go test -tags fts5 -count=1 -race ./internal/vault/... ./cmd/capy/... ./internal/server/...` green.

---

## Storage & cross-machine

### zstd codec (V2.5)

**Migration runner first.** Compression now uses an explicit `encoding` column (NOT magic-byte detection — see design §1, corrected after review), so V2.5 must build the migration runner that `migrations.go` lacks today (it has only `ensureVaultMigrationsTable`):
- Add `vaultMigrationApplied(tx *sql.Tx, name string) (bool, error)` + an apply-loop in `migrateVault`, mirroring `internal/store/migrate.go:applyMigrations`/`migrationApplied`.
- Migration `0001_blob_encoding`: `ALTER TABLE vault_sessions ADD COLUMN encoding TEXT`; `ALTER TABLE vault_files ADD COLUMN encoding TEXT`. Guarded (skip if `vaultMigrationApplied`), idempotent, inside `BeginImmediate`. `ADD COLUMN` doesn't rewrite existing rows; legacy rows get `NULL` (read as raw).

New `internal/vault/codec.go`:
- Package-level `var blobEncoder *zstd.Encoder` (`zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))`) and `var blobDecoder *zstd.Decoder` (`zstd.NewReader(nil)`), `init()` (creation errors are programmer errors → panic in init, or lazy `sync.Once`).
- `encodeBlob(b []byte) (data []byte, encoding string)` → if `CAPY_VAULT_NO_COMPRESS` set or `blobEncoder.EncodeAll(b,nil)` isn't smaller, return `(b, "raw")`; else `(compressed, "zstd")`.
- `decodeBlob(encoding string, b []byte) ([]byte, error)` → `switch encoding { case "", "raw": return b, nil; case "zstd": return blobDecoder.DecodeAll(b, nil); default: error }`. **No magic-byte guessing** — the column is authoritative for arbitrary sidecar bytes.

Schema/prepared-statement changes (`store.go`): add `encoding` to the INSERT/UPDATE column lists for `vault_sessions` and `vault_files`, and to `sessionMetaColumns` reads + `GetFiles` SELECT.

Wire write side:
- `writeRecord`: `data, enc := encodeBlob(sess.RawJSONL)`; pass `data` to the blob param and `enc` to the new `encoding` param (both INSERT + UPDATE).
- `writeChildren`: same per file (`f.RawContent`).
- On the first `zstd` write, set the `vault_meta` `min_reader_version` row (e.g. `"2"`) if absent — a future binary checks it on open and refuses with a clear error (can't protect v1, documents intent; see design §1 Downgrade safety).

Wire read side: thread the row's `encoding` into `decodeBlob` in `scanSessionMeta`/`GetSession` (raw_jsonl) and `GetFiles` (raw_content).

Add `github.com/klauspost/compress` to `go.mod` (apply `/kk:dependency-handling`: `go get`, `go mod tidy`; confirm resolved API matches `EncodeAll`/`DecodeAll`).

- Step → verify: a unit test round-trips Insert→Get and asserts (a) a compressible session is stored with `encoding='zstd'` and the stored blob (queried raw) begins with the zstd magic, (b) `GetSession`/`GetFiles` return bytes byte-identical to the originals, (c) a hand-inserted legacy row with `encoding IS NULL` reads correctly (mixed-corpus), (d) **a raw sidecar fixture whose first bytes are `0x28B52FFD` round-trips byte-identical** (the regression that magic-detection would have corrupted), (e) `content_hash`/`size_bytes` unchanged from v1 for the same input, (f) `min_reader_version` is set after the first compressed write.

### `capy vault compact` (V2.6)

New `VaultStore.Compact()` + `cmd/capy/vault.go` `compact` subcommand.
- **Busy pre-check FIRST (corrected after review):** before any rewrite, run a `Checkpoint()`; if it reports busy pages, abort with "stop the server first" — fail fast rather than doing all the UPDATE work and only hitting contention at VACUUM.
- Rewrite raw blobs: select rows `WHERE encoding IS NULL OR encoding = 'raw'` (the column is authoritative — no byte-peeking); for each, `UPDATE … SET raw_jsonl = ?, encoding = 'zstd'` with `encodeBlob(rawBytes)`. Same for `vault_files`. Batch in `BeginImmediate` transactions (~50/batch).
- After rewrites, run `VACUUM` on a **dedicated single connection** opened after the pool closes (mirror `Checkpoint`'s standalone-connection pattern), with `PRAGMA temp_store = MEMORY` set on that connection so the transient copy never lands in an unencrypted on-disk temp file. SQLite's exclusive lock genuinely protects VACUUM (unlike rekey's file-swap).
- Report `before`/`after` file size via `os.Stat`.

- Step → verify: a test imports raw (`encoding IS NULL`) fixtures, runs `Compact`, asserts every row now has `encoding='zstd'` + the stored blob carries the zstd magic, `os.Stat` size dropped, and `search`/`show` still return identical content; a second `Compact` is a no-op (nothing left to rewrite).

### Shared `Rekey` extraction (V2.7)

Move the already-encrypted rotation out of `cmd/capy/encrypt.go` into `internal/sqliteutil`:
- New `sqliteutil.Rekey(dbPath, oldKey, newKey string) error` = open source with old key (`store.EncryptedDSN`) → `checkpointDB` → open temp with new key → backup-API copy → `swapAndVerify`. Bring `openEncrypted`/`backupDB`/`swapAndVerify`/`checkpointDB` along (unexported helpers in `sqliteutil`).
- Rewire `cmd/capy/encrypt.go:rekeyEncrypted` to call `sqliteutil.Rekey`. Leave `encryptPlain` (unencrypted→encrypted, `PRAGMA rekey`) in `cmd/capy` — vault never needs it.
- **Encryption-critical, shared with the knowledge store — own green commit; do not mix with vault code.**

- Step → verify: `go test -tags fts5 ./internal/store/... ./cmd/capy/...` green; the existing `encryption_lifecycle_test.go` rekey round-trip passes unchanged.

### `capy vault rekey` (V2.8)

`cmd/capy/vault.go` `rekey` subcommand: prompt current passphrase (`terminal.ReadPassphrase`), read new key from `CAPY_VAULT_KEY` (error if unset/empty — the new key must be exported, like `capy encrypt`), call `sqliteutil.Rekey(vault.VaultDBPath(), old, new)`.
- **Do NOT run a `VaultStore.Checkpoint()` pre-check (corrected after review):** `Checkpoint()` (`store.go:389`) opens the DB with `RequireVaultKey()` = `CAPY_VAULT_KEY`, which now holds the **new** key — it cannot open the still-old-key-encrypted DB and fails before rotation. The WAL flush happens inside `Rekey` via `checkpointDB(srcDB)` on the **old-key** source connection.
- **Document a hard "stop the server first" requirement** in `--help` + README: `Rekey` ends with `swapAndVerify` (a filesystem `rename`), not protected by SQLite locking; the old-key checkpoint inside `Rekey` is best-effort, not a guard against an idle-but-attached process. (Contrast `compact`'s VACUUM, genuinely lock-protected.)

- Step → verify: end-to-end test: import → `rekey` (old→new) → reopen with new key lists the same sessions → reopen with old key fails → `.bak` exists. (Server-running case is a documented operator constraint, not an automated guard.)

### `capy vault merge --from` (V2.9)

New `sqliteutil.OpenReadOnly(dbPath, key)` (Open Decision #2, resolved): opens the source **without** running `schemaSQL`/`migrateVault`, and **checkpoints any pending WAL first** so a copied-live source vault's `-wal` rows aren't missed (corrected after review — do NOT use `immutable=1`, which silently skips WAL-resident rows). A cleanly-closed source has no pending WAL (no-op).

New `internal/vault/merge.go`:
- `MergeFrom(ctx, dest *VaultStore, srcPath, srcKey string, opts MergeOptions) (ImportResult, error)`. Open the source via `OpenReadOnly` (`--key`/`CAPY_VAULT_MERGE_KEY`; default `CAPY_VAULT_KEY`).
- Iterate source `vault_sessions` (stream with a cursor; `defer rows.Close()`). For each: load source `vault_files`, `decodeBlob(encoding, …)` the main + sidecars (source may be compressed).
- Decision: `dest.SessionDigest(uuid)` → skip if same `content_hash` or smaller source `size_bytes`; else build a `SessionRecord` and Insert/Replace. Carry source `machine_id`/`claude_project_dir`/`project_path`/`git_branch` verbatim (do not recompute). Apply the 0-msg exclusion (Task 11's `StatusExcluded`) — hence the Task 11 dependency.
- Extract `buildFTS(uuid, mainBytes, files) []FTSRow` from `import.go:buildRecord` (the scanner-wiring loop) so merge and disk-import share it.
- **Carry `vault_snapshots` (added after review):** after the parent session row exists, iterate the source's `vault_snapshots` for that UUID and `InsertSnapshot` into the destination, deduped by `UNIQUE(session_uuid, content_hash)`. No-op when the source table is empty (PreCompact not shipped).
- Batch via `WriteBatch`; reuse `ImportResult`/status accounting.

`cmd/capy/vault.go` `merge` subcommand: `--from <path>` (required), `--key`/`CAPY_VAULT_MERGE_KEY`, `--dry-run`, `--project`. Table output identical to `import`.

- Step → verify: build two fixture vaults with overlapping + distinct UUIDs (one with snapshots); `merge --from B` into A → A gains B's distinct sessions, keeps the larger of overlaps, `search` finds B-only content, B's `machine_id`/`project_path` are preserved, and **B's snapshots appear in A** (deduped). `--dry-run` writes nothing.

---

## Reach

### All-projects opt-in sweep (V2.10)

`internal/server/server.go:vaultSweep`: branch on `os.Getenv("CAPY_VAULT_SWEEP_ALL")`. When set, discover from `config.ClaudeProjectsDir()` (root) instead of `vault.ProjectSessionDir(s.projectDir)`. `DiscoverSessions` already auto-detects a projects-root input, so the only change is the input path + a log line ("vault sweep (all projects): N projects"). Keep the `ctx`, `bgWg`, fail-fast `st.Open()` probe, and `Close()` exactly as-is.

- Step → verify: a server-startup test with `CAPY_VAULT_SWEEP_ALL=1` and a multi-project fixture root imports sessions from >1 project; without the env var, only the current project is swept.

---

## Durability — PreCompact (gated on V2.0)

### V2.0 — Investigate PreCompact payload (Risk-First, gates V2.13–15)

Carried from v1 Task 0. In `internal/hook/precompact.go`, behind `CAPY_DEBUG_PRECOMPACT=1`, write the raw `input []byte` to an `os.CreateTemp` file (0600) AND a copy of the **session file contents as seen at hook time** to a second temp file; log both paths. Trigger `/compact`, then document the JSON shape (session file path? session ID? project dir?) in `docs/wip/vault/v2/precompact-investigation.md`.
- **Timing must be content-level, not mtime (corrected after review):** an mtime delta can't distinguish pre- from post-mutation (the file may be truncated but freshly stamped). Diff the hook-time session-file copy against the post-`/compact` file and confirm the hook-time copy **contains pre-compaction turns the post-compaction file no longer has**. That — not an mtime change — is the gate.
- Ensure the debug handler is a no-op when the env var is unset.

- Step → verify: `precompact-investigation.md` answers (a) can we locate the session file from the payload, and (b) does the hook-time content still include the messages compaction removes? **If the hook-time copy is already the compacted transcript, STOP — re-scope V2.13–16 per design.md §PreCompact.**

### vault_snapshots schema + migration runner (V2.13)

The migration runner (`vaultMigrationApplied` + apply-loop) is already built in V2.5 for migration `0001_blob_encoding`; this adds migration **`0002_vault_snapshots`** using it. Create `vault_snapshots` (see design §5 for columns — incl. its own `encoding` column baked into the DDL) + index `(session_uuid, captured_at DESC)`, guarded, idempotent, inside `BeginImmediate`. Add prepared statements + `InsertSnapshot`/`ListSnapshots`/`GetSnapshot` to `store.go` (blob via `encodeBlob`/`decodeBlob` + the `encoding` column).
- **Snapshot `content_hash` scope (corrected after review):** `sha256(rawJSONL)` over the **main transcript only** — NOT the composite `computeContentHash` (main + sidecars) used for `vault_sessions`. Snapshots store no sidecar rows. This defines `UNIQUE(session_uuid, content_hash)` dedup semantics.

- Step → verify: opening a v1 vault applies `0002_vault_snapshots` once (re-open is a no-op); `vault_migrations` records the name; CASCADE removes snapshots when the parent session is deleted.

### PreCompact handler (V2.14)

Replace the `handlePreCompact` stub: parse payload (per V2.0) → resolve session file + UUID + project dir. Then, **in this order (corrected after review — FK + clobber safety):**
1. **Archive the active session first** via the existing single-session import path (`DiscoverSessions(dir)` → filter to this UUID → `Import`). This reads the pre-compaction **main + sidecars** from disk, applies the idempotent decision, and *creates the parent `vault_sessions` row if absent* — satisfying the snapshot FK and avoiding a main-only `ReplaceSession` that would `DELETE` then fail to re-insert sidecars.
2. **Then** `InsertSnapshot` (main transcript; dedup via `UNIQUE(session_uuid, content_hash)`).

Short-lived process discipline: open vault, import-one + insert-snapshot, `Close()` (checkpoints), return fast; log + swallow errors so `/compact` is never blocked. Confirm hook wiring in `internal/hook/` dispatch.

- Step → verify: with a captured payload fixture, including **a brand-new session that has no pre-existing `vault_sessions` row** → the active row is created, then the snapshot inserts without an FK error; a second identical invocation dedups (no new snapshot); an existing session with sidecars is **not** stripped of them (no clobber).

### Snapshot CLI (V2.15)

`cmd/capy/vault.go`: `snapshots <id>` (list: hash, size, captured_at, `--json`); extend `restore` with `--snapshot <hash>` to write the snapshot blob instead of the active row. It calls `RestoreSession(uuid, snapshotJSONL, nil, …)` — **`files` is nil because snapshots store no sidecars**, so a snapshot restore reconstructs `<uuid>.jsonl` only (main-transcript-only by design — `/compact` mutates only the main transcript; sidecars are restorable from the active row). State this in `--help`, alongside delete-cascades-snapshots.

- Step → verify: `snapshots <id>` lists captured snapshots; `restore <id> --snapshot <hash>` writes the main transcript only (no `<uuid>/` tree); `delete <id>` removes the session **and** its snapshots.

---

## TUI

### Keybindings + in-list filter (V2.11)

`internal/vault/tui/`: add `f` (in-list project filter via `bubbles/list`'s filter or a custom predicate over `ListSessions(Project:)`), `c` (copy current message — prefer OSC-52 escape to stdout; no native dep), `r` (restore), `R` (resume). For `r`/`R`: send `tea.Quit`, and after `Run()` returns, perform the restore/exec from the CLI layer (the program must release the alt-screen + TTY first — same teardown as Task 4b). Update `app.go` key dispatch + `styles.go`/status bar hints. **OSC-52 silently no-ops on terminals/multiplexers without clipboard passthrough (added after review):** always show a status-bar confirmation after `c` ("copied" / "copy requested — terminal may not support OSC-52") so the user isn't left guessing.

- Step → verify: `internal/vault/tui` unit tests cover filter narrowing, `c` emitting an OSC-52 sequence **and a status-bar confirmation**, and `r`/`R` producing a quit-then-action intent (assert the post-quit action carries the right UUID/dir). Race-clean.

### Glamour markdown behind a build tag (V2.12)

Add `github.com/charmbracelet/glamour` pinned to a **v0.x** release on the `github.com/charmbracelet/...` path. **Verify** `go mod graph | grep lipgloss` shows only `github.com/charmbracelet/lipgloss` (v1), **no** `charm.land/lipgloss/v2`. Split the viewer render:
- `tui/render_glamour.go` (`//go:build glamour`): build a `glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width))`, render assistant/user markdown through it.
- `tui/render_default.go` (`//go:build !glamour`): the current lipgloss-only path.
- A small interface or function var lets `viewer.go` call the active renderer without `#ifdef`-style branching.
- **Add a CI/Makefile build for `-tags glamour` (added after review):** a `build-glamour` Makefile target + a CI matrix entry, otherwise the tagged render path bit-rots (the default build never compiles it). Run the `tui` tests under both tag sets.

- Step → verify: `make build` (default) succeeds and the binary does **not** link glamour (`go tool nm`/binary-size check); `make build-glamour` (`-tags fts5,glamour`) succeeds and renders markdown; CI runs both; `go mod graph` confirms no `charm.land/lipgloss/v2`.

---

## Correctness

### Exclude 0-msg sessions (V2.4)

`internal/vault/import.go`: add `StatusExcluded = "excluded"` to the status constants + `ImportResult` accounting. After `buildRecord`, if `rec.Session.MessageCount == 0`, record `StatusExcluded` and `continue` (don't batch). Mirror in `MergeFrom`. Surface excluded count in CLI table output.

- Step → verify: import a fixture dir containing a 0-msg session + a normal session → the 0-msg session is excluded (not in `list`), the normal one imported; re-importing the 0-msg session after it gains messages archives it.

---

## Final verification (V2.16)

Depends on all. Run `/kk:test` (full suite, `-tags fts5`, both keys set, race), `/kk:document` (update `docs/architecture.md`, `CLAUDE.md`, **and `README.md`** — the v1 doc pass initially missed user-facing docs; add the new subcommands + `-tags glamour` build option + `CAPY_VAULT_SWEEP_ALL`/`CAPY_VAULT_MERGE_KEY` env vars), `/kk:review-code go` (full v2 diff), `/kk:review-spec` (implementation vs. these docs).

## Assumptions (carried from design.md)

1. **PreCompact fires before file mutation** (UNVERIFIED — V2.0 gate; dealbreaker for V2.13–15 only).
2. zstd frame magic never collides with raw JSONL leading bytes (true by construction; round-trip + mixed-corpus test).
3. Backup-API rekey works for sqlite3mc vaults (validated by round-trip).
4. Re-scan in merge reproduces equivalent FTS (validated by merge→search).
5. VACUUM reclaims pages after compact (validated by before/after os.Stat).

## Not Doing (carried from design.md)

Cloud sync, multi-user, Codex, diffing, real-time watch, automatic retention, redacted sharing/export, TUI lazy-windowing & 3-panel split, `encryptPlain` extraction, snapshot retention eviction. See design.md §Not Doing for one-line rationales.
