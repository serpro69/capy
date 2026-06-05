# Tasks: Vault v2

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Parent (v1): [../tasks.md](../tasks.md)
> Status: pending
> Created: 2026-06-05
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic retention, redacted sharing/export, TUI lazy-windowing viewer, TUI 3-panel split, encryptPlain (unencrypted→encrypted) extraction, snapshot retention eviction policy

Single flat plan (no phase boundary). Task 0 is the only hard gate and gates ONLY the PreCompact tasks (14–16); all other tasks proceed independently. Build/test with `-tags fts5`; `CAPY_DB_KEY` + `CAPY_VAULT_KEY` required.

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
- [ ] 0.4 Verify timing: compare the session file `mtime` before vs. after the hook fires — does PreCompact run BEFORE or AFTER file mutation?
- [ ] 0.5 Ensure the debug branch is a no-op when the env var is unset (no behavior change shipped)
- [ ] 0.6 **Decision gate:** if the hook fires AFTER mutation, STOP — file-based capture is impossible; re-scope Tasks 14–16 per design.md §PreCompact (SessionStart-cached copy, or drop). Record the decision in the investigation doc

## Task 1: Consolidate `beginImmediate`/`isBusy` into `sqliteutil`
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 0, 2, 5–13
- **Docs:** [implementation.md#beginimmediateisbusy-consolidation-v21](./implementation.md)

### Subtasks
- [ ] 1.1 Add exported `BeginImmediate(db *sql.DB) (*sql.Tx, error)` and `IsBusy(error) bool` to `internal/sqliteutil/sqliteutil.go` (parameterize or keep a thin per-store wrapper for the meta-table no-op write)
- [ ] 1.2 Delete the copies in `internal/vault/migrations.go` (incl. the `TODO`) and `internal/store/retry.go`; update all call sites
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

## Task 3: `context.Context` propagation — `internal/store`
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2 (NOT Task 4 — land Task 3 first)
- **Slicing strategy:** isolated behavior-preserving commit — ⚠ encryption-critical knowledge store; do not bundle with feature work
- **Docs:** [implementation.md#contextcontext-propagation-both-stores-v22a-v22b](./implementation.md)

### Subtasks
- [ ] 3.1 Convert `internal/store` `db.Query`/`Exec`/`QueryRow`/`Begin` → `*Context` variants threading the store's context; add leading `ctx context.Context` to public methods lacking one (callers without a context pass `context.Background()`)
- [ ] 3.2 Verify: `CAPY_DB_KEY=… go test -tags fts5 -count=1 -race ./internal/store/...` passes UNCHANGED; `make bench-quality` shows no regression vs. master

## Task 4: `context.Context` propagation — `internal/vault`
- **Status:** pending
- **Depends on:** Task 1, Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Slicing strategy:** isolated behavior-preserving commit, adjacent to Task 3
- **Docs:** [implementation.md#contextcontext-propagation-both-stores-v22a-v22b](./implementation.md)

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
- [ ] 5.2 Create `internal/vault/codec.go` — shared package-level `*zstd.Encoder`/`*zstd.Decoder`; `encodeBlob([]byte) []byte`; `decodeBlob([]byte) ([]byte, error)` with zstd-magic (`0x28B52FFD`) detection, passthrough for raw JSONL
- [ ] 5.3 Wire write side: `store.go:writeRecord` (raw_jsonl, both INSERT + UPDATE) and `writeChildren` (file content) call `encodeBlob`
- [ ] 5.4 Wire read side: `scanSessionMeta`/`GetSession` and `GetFiles` call `decodeBlob` after `Scan`
- [ ] 5.5 Confirm `content_hash`/`size_bytes`/FTS still computed on UNCOMPRESSED bytes (no change to `computeContentHash`/`buildRecord`)
- [ ] 5.6 Tests: round-trip Insert→Get returns byte-identical content; stored blob (queried raw) carries the zstd magic; a hand-inserted RAW row still reads (mixed corpus); `content_hash`/`size_bytes` identical to v1 for the same input
- [ ] 5.7 Verify: `go test -tags fts5 ./internal/vault/...` green

## Task 6: `capy vault compact` (recompress + VACUUM)
- **Status:** pending
- **Depends on:** Task 5
- **Size:** S
- **Can run in parallel with:** Task 8, 9 (after Task 5)
- **Docs:** [implementation.md#capy-vault-compact-v26](./implementation.md)

### Subtasks
- [ ] 6.1 Add `VaultStore.Compact()` — rewrite uncompressed blobs (detect in Go after scan) via batched `BeginImmediate` UPDATEs for `vault_sessions.raw_jsonl` and `vault_files.raw_content`
- [ ] 6.2 Run `VACUUM` on a dedicated single connection opened after the pool closes (mirror `Checkpoint`); abort with guidance if the DB is busy
- [ ] 6.3 Add the `compact` subcommand to `cmd/capy/vault.go`; report before/after file size (`os.Stat`)
- [ ] 6.4 Verify: a raw-blob fixture compacts → every stored blob carries the zstd magic, file size dropped, `search`/`show` unchanged

## Task 7: Extract shared `Rekey` helper from `cmd/capy/encrypt.go`
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, 1, 2, 5, 10–13
- **Slicing strategy:** isolated behavior-preserving commit — ⚠ encryption-critical, shared with knowledge store
- **Docs:** [implementation.md#shared-rekey-extraction-v27](./implementation.md)

### Subtasks
- [ ] 7.1 Add `sqliteutil.Rekey(dbPath, oldKey, newKey string) error` (backup-API rotation: open old → checkpoint → backup-copy into temp opened with new key → swap+verify); bring `openEncrypted`/`backupDB`/`swapAndVerify`/`checkpointDB` into `sqliteutil`
- [ ] 7.2 Rewire `cmd/capy/encrypt.go:rekeyEncrypted` to call `sqliteutil.Rekey`; leave `encryptPlain` in `cmd/capy`
- [ ] 7.3 Verify: `go test -tags fts5 ./internal/store/... ./cmd/capy/...` green; the existing rekey round-trip in `encryption_lifecycle_test.go` passes unchanged

## Task 8: `capy vault rekey` command
- **Status:** pending
- **Depends on:** Task 7
- **Size:** S
- **Can run in parallel with:** Task 6, 9
- **Docs:** [implementation.md#capy-vault-rekey-v28](./implementation.md)

### Subtasks
- [ ] 8.1 Add the `rekey` subcommand to `cmd/capy/vault.go`: prompt current passphrase, read new key from `CAPY_VAULT_KEY` (error if unset), call `sqliteutil.Rekey(vault.VaultDBPath(), old, new)`
- [ ] 8.2 Pre-check: run `VaultStore.Checkpoint()`; abort with guidance ("stop the MCP server first") if busy pages remain
- [ ] 8.3 Verify: import → `rekey` (old→new) → reopen with new key lists same sessions; reopen with old key fails; `.bak` exists

## Task 9: `capy vault merge --from <path>`
- **Status:** pending
- **Depends on:** Task 5
- **Size:** M
- **Can run in parallel with:** Task 6, 8 (after Task 5)
- **Slicing strategy:** Contract-First (source-vault read boundary), then idempotent upsert reuse
- **Docs:** [implementation.md#capy-vault-merge---from-v29](./implementation.md)

### Subtasks
- [ ] 9.1 Add a read-only source opener (`sqliteutil.OpenReadOnly` that skips DDL/migrations — do NOT run `schemaSQL`/`migrateVault` against the source)
- [ ] 9.2 Extract `buildFTS(uuid, mainBytes, files) []FTSRow` from `import.go:buildRecord` so disk-import and merge share the scanner wiring
- [ ] 9.3 Create `internal/vault/merge.go` — `MergeFrom(ctx, dest, srcPath, srcKey, opts)`: iterate source `vault_sessions`+`vault_files`, `decodeBlob`, apply `dest.SessionDigest` idempotent decision (skip same-hash/smaller, else insert/replace), carry source `machine_id`/`claude_project_dir`/`project_path`/`git_branch` verbatim, batch via `WriteBatch`. Include the 0-msg guard (Task 11)
- [ ] 9.4 Add the `merge` subcommand to `cmd/capy/vault.go`: `--from` (required), `--key`/`CAPY_VAULT_MERGE_KEY`, `--dry-run`, `--project`; `import`-style table output
- [ ] 9.5 Verify: two fixture vaults (overlapping + distinct UUIDs) → `merge` brings in distinct sessions, keeps larger overlaps, `search` finds source-only content, source `machine_id`/`project_path` preserved; `--dry-run` writes nothing

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
- [ ] 12.2 `c` — copy current message to clipboard via OSC-52 escape to stdout (no native dep)
- [ ] 12.3 `r`/`R` — emit a `tea.Quit`-then-action intent carrying the UUID/dir; perform restore/`claude --resume` from the CLI layer after `Run()` returns
- [ ] 12.4 Verify: `internal/vault/tui` unit tests cover filter narrowing, OSC-52 emission, and the quit-then-action intent for `r`/`R`; race-clean

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
- [ ] 13.4 Verify: `make build` (default) succeeds and the binary does NOT link glamour; `go build -tags fts5,glamour ./...` succeeds and renders markdown; `go mod graph` confirms no lipgloss v2

## Task 14: PreCompact — `vault_snapshots` schema + migration runner
- **Status:** pending
- **Depends on:** Task 0 (favorable timing confirmed), Task 5
- **Size:** M
- **Can run in parallel with:** Task 6, 8, 9 (after Task 5)
- **Docs:** [implementation.md#vault_snapshots-schema--migration-runner-v213](./implementation.md)

### Subtasks
- [ ] 14.1 In `internal/vault/migrations.go`, add `vaultMigrationApplied(db, name) (bool, error)` + an apply-loop in `migrateVault` (mirror `internal/store/migrate.go`)
- [ ] 14.2 Migration `0001_vault_snapshots`: create `vault_snapshots` (snapshot_id PK, session_uuid FK CASCADE, content_hash, size_bytes, captured_at, trigger, raw_jsonl compressed BLOB; `UNIQUE(session_uuid, content_hash)`) + index `(session_uuid, captured_at DESC)`; guarded, idempotent, inside `BeginImmediate`. NOT in FTS
- [ ] 14.3 Add `InsertSnapshot`/`ListSnapshots`/`GetSnapshot` + prepared statements to `store.go` (compressed blob via `encodeBlob`/`decodeBlob`)
- [ ] 14.4 Verify: opening a v1 vault applies the migration once (re-open no-op); `vault_migrations` records the name; CASCADE removes snapshots when the parent session is deleted

## Task 15: PreCompact hook handler
- **Status:** pending
- **Depends on:** Task 0, Task 14
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#precompact-handler-v214](./implementation.md)

### Subtasks
- [ ] 15.1 Replace the `handlePreCompact` stub: parse the payload (per Task 0) → resolve session file + UUID + project dir → read pre-compaction bytes → scan
- [ ] 15.2 `InsertSnapshot` (dedup via UNIQUE) + idempotent upsert of the active `vault_sessions` row; open→write→`Close()` fast (short-lived process); log + swallow errors so `/compact` is never blocked
- [ ] 15.3 Confirm hook wiring in `internal/hook/` dispatch routes PreCompact to the handler
- [ ] 15.4 Verify: a captured-payload fixture → snapshot row with pre-compaction content; a second identical invocation dedups; active row reflects the larger blob

## Task 16: Snapshot CLI — `snapshots` + `restore --snapshot`
- **Status:** pending
- **Depends on:** Task 14, Task 15
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [implementation.md#snapshot-cli-v215](./implementation.md)

### Subtasks
- [ ] 16.1 Add `snapshots <id>` subcommand (list hash/size/captured_at, `--json`)
- [ ] 16.2 Extend `restore` with `--snapshot <hash>` to write the snapshot blob (reuse v1 restore path-safety); document delete-cascades-snapshots in `--help`
- [ ] 16.3 Verify: `snapshots <id>` lists; `restore <id> --snapshot <hash>` writes that content; `delete <id>` removes the session AND its snapshots

## Task 17: Final verification
- **Status:** pending
- **Depends on:** Task 1–16
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 17.1 Run `/kk:test` — full suite (`-tags fts5`, both keys, race); no regressions in existing tests
- [ ] 17.2 Run `/kk:document` — update `docs/architecture.md`, `CLAUDE.md`, AND `README.md` (new subcommands `compact`/`merge`/`rekey`/`snapshots`, `-tags glamour` build, `CAPY_VAULT_SWEEP_ALL`/`CAPY_VAULT_MERGE_KEY` env vars)
- [ ] 17.3 Run `/kk:review-code go` — review the full v2 diff
- [ ] 17.4 Run `/kk:review-spec` — verify implementation matches design.md + implementation.md

## Dependency Graph

```
Task 0 (investigate) ───────────────┬─→ Task 14 (snapshots schema) ─→ Task 15 (hook) ─→ Task 16 (snapshot CLI) ─┐
                                     │                                                                            │
Task 5 (zstd codec) ──┬─→ Task 6 (compact) ───────────────────────────────────────────────────────────────────┤
                      ├─→ Task 9 (merge) ──────────────────────────────────────────────────────────────────────┤
                      └─→ Task 14 ─────────────────────────────────────────────────────────────────────────────┤
                                                                                                                 │
Task 7 (rekey extract) ─→ Task 8 (vault rekey) ─────────────────────────────────────────────────────────────────┤
                                                                                                                 │
Task 1 (sqliteutil) ──→ Task 4 (vault ctx)                                                                       │
Task 3 (store ctx) ───→ Task 4                                                                                   │
                                                                                                                 │
Task 2 (SessionDir) ─────────────────────────────────────────────────────────────────────────────────────────→ │
Task 10 (all-proj sweep) ──────────────────────────────────────────────────────────────────────────────────────┤
Task 11 (0-msg) ───────────────────────────────────────────────────────────────────────────────────────────────┤
Task 12 (TUI keys) ────────────────────────────────────────────────────────────────────────────────────────────┤
Task 13 (TUI glamour) ─────────────────────────────────────────────────────────────────────────────────────────┴─→ Task 17 (final)
```
