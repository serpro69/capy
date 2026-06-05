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

Both `internal/vault/migrations.go:52-97` and `internal/store/retry.go` define `beginImmediate` + `isBusy`. Move both into `internal/sqliteutil` as exported `BeginImmediate(db *sql.DB) (*sql.Tx, error)` and `IsBusy(error) bool`. The no-op write currently targets each package's own meta table (`vault_meta` / store's table) — parameterize the lock table or use a table both share (`sqlite_master` read won't take the RESERVED lock; keep a tiny per-store sentinel write). Simplest: keep `BeginImmediate` generic and pass the lock statement, or have each store keep a thin wrapper that supplies its meta-table no-op write.

- Step: add `BeginImmediate`/`IsBusy` to `sqliteutil`; delete the vault + store copies; update call sites → verify: `make test-race` green; `grep -rn "func beginImmediate\|func isBusy" internal/` returns only `sqliteutil`.

### `SessionDir()` routing (V2.3)

`internal/session/sweep.go:SessionDir()` hardcodes `~/.claude/projects/`. Route it through `config.ClaudeProjectsDir()` (already `CLAUDE_CONFIG_DIR`-aware since v1 Task 3.1).

- Step: replace the hardcoded base in `SessionDir()` with `config.ClaudeProjectsDir()` → verify: `go test -tags fts5 ./internal/session/... ./internal/config/...` green; a `CLAUDE_CONFIG_DIR`-set test resolves the overridden root.

### `context.Context` propagation, both stores (V2.2a, V2.2b)

Two adjacent behavior-preserving commits. **High blast radius — isolate each; do not bundle with feature work.**

- **V2.2a `internal/store`:** convert `db.Query`/`Exec`/`QueryRow`/`Begin` to `*Context` variants threading the store's context; add `ctx context.Context` as the leading param to public methods that don't already take one. Keep behavior identical (pass `context.Background()` from callers that have none yet).
  - Step → verify: `CAPY_DB_KEY=… go test -tags fts5 -count=1 -race ./internal/store/...` passes **unchanged**; the bench gate (`make bench-quality`) shows no regression.
- **V2.2b `internal/vault`:** same conversion; replace `s.ctx()` (returns `context.Background()`) usage with a real threaded `ctx`. The public methods (`GetSession`, `ListSessions`, `Search`, `Stats`, `InsertSession`, etc.) gain a leading `ctx`. Update CLI + server-sweep callers to pass their context (`cmd.Context()`, `sweepCtx`).
  - Step → verify: `CAPY_VAULT_KEY=… go test -tags fts5 -count=1 -race ./internal/vault/... ./cmd/capy/... ./internal/server/...` green.

---

## Storage & cross-machine

### zstd codec (V2.5)

New `internal/vault/codec.go`:
- Package-level `var blobEncoder *zstd.Encoder` (`zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))`) and `var blobDecoder *zstd.Decoder` (`zstd.NewReader(nil)`), initialized in `init()` (creation errors are programmer errors → panic in init is acceptable, or lazy `sync.Once`).
- `encodeBlob(b []byte) []byte` → `blobEncoder.EncodeAll(b, nil)`.
- `decodeBlob(b []byte) ([]byte, error)` → if `len(b) >= 4 && b[0]==0x28 && b[1]==0xB5 && b[2]==0x2F && b[3]==0xFD` → `blobDecoder.DecodeAll(b, nil)`; else return `b` unchanged.

Wire write side (`store.go`):
- `writeRecord`: wrap `sess.RawJSONL` → `encodeBlob(...)` in both the UPDATE and INSERT `Exec` calls.
- `writeChildren`: wrap `f.RawContent` → `encodeBlob(...)`.
- Snapshot writer (V2.13) uses `encodeBlob` too.

Wire read side:
- `GetSession`/`scanSessionMeta`: after scanning `raw` (the blob), `raw, err = decodeBlob(raw)`.
- `GetFiles`: after `rows.Scan(&f.RelativePath, &f.RawContent)`, `f.RawContent, err = decodeBlob(f.RawContent)`.

Add `github.com/klauspost/compress` to `go.mod` (apply `/kk:dependency-handling`: `go get`, then `go mod tidy`; confirm the resolved API matches `EncodeAll`/`DecodeAll`).

- Step → verify: a unit test round-trips a session through Insert→Get and asserts (a) the *stored* blob (queried raw, bypassing decode) begins with the zstd magic, (b) `GetSession` returns bytes byte-identical to the original, (c) a hand-inserted *raw* JSONL row still reads correctly (mixed-corpus), (d) `content_hash`/`size_bytes` are unchanged from v1 for the same input.

### `capy vault compact` (V2.6)

New `VaultStore.Compact()` + `cmd/capy/vault.go` `compact` subcommand. Rewrite uncompressed blobs:
- Select `uuid, raw_jsonl` where the blob is not already zstd (detect in Go after scan — SQLite can't peek bytes portably); for each, `UPDATE … SET raw_jsonl = encodeBlob(decoded)`. Same for `vault_files`. Batch in `beginImmediate` transactions (~50/batch).
- After rewrites, run `VACUUM` on a **dedicated single connection** opened after the pool is closed (mirror `Checkpoint`'s standalone-connection pattern); abort with guidance if the DB is busy.
- Report `before`/`after` file size via `os.Stat`.

- Step → verify: a test imports raw-blob fixtures, runs `Compact`, asserts every stored blob now carries the zstd magic, `os.Stat` size dropped, and `search`/`show` still return identical content.

### Shared `Rekey` extraction (V2.7)

Move the already-encrypted rotation out of `cmd/capy/encrypt.go` into `internal/sqliteutil`:
- New `sqliteutil.Rekey(dbPath, oldKey, newKey string) error` = open source with old key (`store.EncryptedDSN`) → `checkpointDB` → open temp with new key → backup-API copy → `swapAndVerify`. Bring `openEncrypted`/`backupDB`/`swapAndVerify`/`checkpointDB` along (unexported helpers in `sqliteutil`).
- Rewire `cmd/capy/encrypt.go:rekeyEncrypted` to call `sqliteutil.Rekey`. Leave `encryptPlain` (unencrypted→encrypted, `PRAGMA rekey`) in `cmd/capy` — vault never needs it.
- **Encryption-critical, shared with the knowledge store — own green commit; do not mix with vault code.**

- Step → verify: `go test -tags fts5 ./internal/store/... ./cmd/capy/...` green; the existing `encryption_lifecycle_test.go` rekey round-trip passes unchanged.

### `capy vault rekey` (V2.8)

`cmd/capy/vault.go` `rekey` subcommand: prompt current passphrase (`terminal.ReadPassphrase`), read new key from `CAPY_VAULT_KEY` (error if unset/empty — the new key must be exported, like `capy encrypt`), call `sqliteutil.Rekey(vault.VaultDBPath(), old, new)`. Pre-check: run `VaultStore.Checkpoint()` first; if it reports busy pages, abort ("stop the MCP server first").

- Step → verify: end-to-end test: import → `rekey` (old→new) → reopen with new key lists the same sessions → reopen with old key fails → `.bak` exists.

### `capy vault merge --from` (V2.9)

New `internal/vault/merge.go`:
- `MergeFrom(ctx, dest *VaultStore, srcPath, srcKey string, opts MergeOptions) (ImportResult, error)`. Open source read-only (see Open Decision #2 — likely a `sqliteutil.OpenReadOnly` that skips DDL/migrations; do **not** run `schemaSQL`/`migrateVault` against the source).
- Iterate source `vault_sessions` (stream with a cursor; `defer rows.Close()`). For each: load source `vault_files`, `decodeBlob` the main + sidecars.
- Decision: `dest.SessionDigest(uuid)` → skip if same `content_hash` or smaller source `size_bytes`; else build a `SessionRecord` and Insert/Replace. Carry source `machine_id`/`claude_project_dir`/`project_path`/`git_branch` verbatim (do not recompute).
- Extract `buildFTS(uuid, mainBytes, files) []FTSRow` from `import.go:buildRecord` (the scanner-wiring loop) so merge and disk-import share it.
- Batch via `WriteBatch`; reuse `ImportResult`/status accounting.

`cmd/capy/vault.go` `merge` subcommand: `--from <path>` (required), `--key`/`CAPY_VAULT_MERGE_KEY`, `--dry-run`, `--project`. Table output identical to `import`.

- Step → verify: build two fixture vaults with overlapping + distinct UUIDs; `merge --from B` into A → A gains B's distinct sessions, keeps the larger of overlaps, `search` finds B-only content, and B's `machine_id`/`project_path` are preserved on merged rows. `--dry-run` writes nothing.

---

## Reach

### All-projects opt-in sweep (V2.10)

`internal/server/server.go:vaultSweep`: branch on `os.Getenv("CAPY_VAULT_SWEEP_ALL")`. When set, discover from `config.ClaudeProjectsDir()` (root) instead of `vault.ProjectSessionDir(s.projectDir)`. `DiscoverSessions` already auto-detects a projects-root input, so the only change is the input path + a log line ("vault sweep (all projects): N projects"). Keep the `ctx`, `bgWg`, fail-fast `st.Open()` probe, and `Close()` exactly as-is.

- Step → verify: a server-startup test with `CAPY_VAULT_SWEEP_ALL=1` and a multi-project fixture root imports sessions from >1 project; without the env var, only the current project is swept.

---

## Durability — PreCompact (gated on V2.0)

### V2.0 — Investigate PreCompact payload (Risk-First, gates V2.13–15)

Carried from v1 Task 0. In `internal/hook/precompact.go`, behind `CAPY_DEBUG_PRECOMPACT=1`, write the raw `input []byte` to an `os.CreateTemp` file (0600), log the path to stderr. Trigger `/compact`, capture the payload, document JSON shape (session file path? session ID? project dir?) and **timing** (compare session-file `mtime` before vs. after the hook) in `docs/wip/vault/v2/precompact-investigation.md`. Ensure the debug handler is a no-op when the env var is unset.

- Step → verify: `precompact-investigation.md` exists and answers: can we locate the session file from the payload, and does the hook fire **before** mutation? **If "after", STOP — re-scope V2.13–15 per design.md §PreCompact.**

### vault_snapshots schema + migration runner (V2.13)

`internal/vault/migrations.go`: add `vaultMigrationApplied(db, name) (bool, error)` and an apply-loop in `migrateVault` (mirror `internal/store/migrate.go`). First migration `"0001_vault_snapshots"`: create `vault_snapshots` (see design §5 for columns) + index, guarded, idempotent, inside `BeginImmediate`. Add prepared statements + `InsertSnapshot`/`ListSnapshots`/`GetSnapshot` to `store.go` (compressed blob via `encodeBlob`/`decodeBlob`).

- Step → verify: opening a v1 vault applies `0001_vault_snapshots` once (re-open is a no-op); `vault_migrations` records the name; CASCADE removes snapshots when the parent session is deleted.

### PreCompact handler (V2.14)

Replace the `handlePreCompact` stub: parse payload (per V2.0) → resolve session file + UUID + project dir → read pre-compaction bytes → scan → `InsertSnapshot` (dedup via `UNIQUE(session_uuid, content_hash)`) + idempotent upsert of the active `vault_sessions` row. Short-lived process discipline: open vault, write, `Close()` (checkpoints), return fast; log + swallow errors so `/compact` is never blocked. Confirm hook wiring in `internal/hook/` dispatch.

- Step → verify: simulate a PreCompact invocation with a captured payload fixture → a snapshot row exists with the pre-compaction content; a second identical invocation dedups (no new row); the active session row reflects the larger pre-compaction blob.

### Snapshot CLI (V2.15)

`cmd/capy/vault.go`: `snapshots <id>` (list: hash, size, captured_at, `--json`); extend `restore` with `--snapshot <hash>` to write the snapshot blob instead of the active row (reuse v1 `restore`'s path-safety). Document delete-cascades-snapshots in `--help`.

- Step → verify: `snapshots <id>` lists captured snapshots; `restore <id> --snapshot <hash>` writes the snapshot's content; `delete <id>` removes the session **and** its snapshots.

---

## TUI

### Keybindings + in-list filter (V2.11)

`internal/vault/tui/`: add `f` (in-list project filter via `bubbles/list`'s filter or a custom predicate over `ListSessions(Project:)`), `c` (copy current message — prefer OSC-52 escape to stdout; no native dep), `r` (restore), `R` (resume). For `r`/`R`: send `tea.Quit`, and after `Run()` returns, perform the restore/exec from the CLI layer (the program must release the alt-screen + TTY first — same teardown as Task 4b). Update `app.go` key dispatch + `styles.go`/status bar hints.

- Step → verify: `internal/vault/tui` unit tests cover filter narrowing, `c` emitting an OSC-52 sequence, and `r`/`R` producing a quit-then-action intent (assert the post-quit action carries the right UUID/dir). Race-clean.

### Glamour markdown behind a build tag (V2.12)

Add `github.com/charmbracelet/glamour` pinned to a **v0.x** release on the `github.com/charmbracelet/...` path. **Verify** `go mod graph | grep lipgloss` shows only `github.com/charmbracelet/lipgloss` (v1), **no** `charm.land/lipgloss/v2`. Split the viewer render:
- `tui/render_glamour.go` (`//go:build glamour`): build a `glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(width))`, render assistant/user markdown through it.
- `tui/render_default.go` (`//go:build !glamour`): the current lipgloss-only path.
- A small interface or function var lets `viewer.go` call the active renderer without `#ifdef`-style branching.

- Step → verify: `make build` (default) succeeds and the binary does **not** link glamour (`go tool nm`/binary-size check); `go build -tags fts5,glamour ./...` succeeds and renders markdown; `go mod graph` confirms no lipgloss v2.

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
