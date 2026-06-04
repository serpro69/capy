# Tasks: Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-05-28
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic cleanup, sharing/export with redaction, PreCompact snapshots (deferred), file-history preservation, cross-machine vault merge command, shell completions (trivial via cobra, add during polish), custom-title/customTitle title override (deferred — absent from JSONL), `capy vault rekey` (deferred to Future Improvements), glamour markdown rendering (deferred — lean binary)

## Task 0: Investigate PreCompact hook payload
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1, Task 2
- **Slicing strategy:** Risk-First (most uncertain piece first)
- **Blocking:** No — this task is informational and does NOT block shipping. Findings feed into a future PreCompact archival feature (see design.md Future Improvements).
- **Docs:** [implementation.md#precompact-hook-deferred](./implementation.md#precompact-hook-deferred)

### Subtasks
- [ ] 0.1 Add a debug handler in `internal/hook/precompact.go` that writes the raw stdin payload to a temp file (`os.CreateTemp`), gated behind `CAPY_DEBUG_PRECOMPACT=1` env var. Write with 0600 permissions. Log the temp file path to stderr
- [ ] 0.2 Trigger `/compact` in a Claude Code session and capture the payload
- [ ] 0.3 Document the JSON structure: which fields are present, whether it includes session file path, session ID, project directory
- [ ] 0.4 Verify timing: does the hook fire before or after file mutation? (Check file mtime before and after hook execution)
- [ ] 0.5 Write findings to `docs/wip/vault/precompact-investigation.md` — this unblocks future PreCompact archival implementation
- [ ] 0.6 Remove the debug handler before shipping (or ensure it's no-op when env var is unset)
- [ ] 0.7 Verify: documented payload structure exists and is sufficient to locate the session file

## Task 1: Vault store foundation — schema, encryption, connection lifecycle
- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, Task 2
- **Docs:** [implementation.md#encryption--db-initialization](./implementation.md#encryption--db-initialization)

### Subtasks
- [x] 1.1 **Extract** the shared SQLite open/recovery path into `internal/sqliteutil/` (primary plan — exporting predicates alone is insufficient). Move out of `internal/store/{retry,encryption,store}.go`: the canary query, the corrupt / wrong-passphrase / unencrypted-DB classification, the `errWrongPassphrase`/`errUnencryptedDB` types + `isUnencryptedDB`, and `backupCorruptDB`. Have both `store` and `vault` call `sqliteutil`. Rationale: `IsWrongPassphrase` only matches store's unexported `*errWrongPassphrase`, constructed inside `store.openDB()` — vault can't construct it, so a vault `openDB()` would never satisfy an exported predicate.
  - **⚠ High blast radius — isolate this step.** It refactors working, **encryption-critical** code shared with the existing knowledge store; a regression can make installed users' `knowledge.db` fail to open or mis-handle recovery (data loss). Land it as its **own behavior-preserving commit that passes green BEFORE any vault code (1.2+) depends on it.** Do not bundle it with vault store work.
  - **Behavior-preserving contract** — these `store.getDB()`/`openDB()` semantics must be identical before and after the move: (1) wrong passphrase on a *real* encrypted DB → error with **no** backup-and-recreate (never destroy data on a key typo); (2) a too-small/garbage file (`0 < size < 512 B`) is **not** classified as wrong-passphrase (the `!IsGarbageFile` guard) so recovery proceeds; (3) genuine corruption → back up `.db`/`-wal`/`-shm` sidecars, then recreate; (4) an existing *unencrypted* DB → the distinct "run `capy encrypt`" error.
  - **Gate before proceeding:** `CAPY_DB_KEY=test-key-for-development go test -tags fts5 -count=1 -race ./internal/store/...` passes **unchanged**; move/keep the classification unit tests into `internal/sqliteutil/`. The full-suite run (Task 7.1) is the backstop, not the gate.
- [x] 1.2 Create `internal/vault/encryption.go` — `RequireVaultKey()` reads `CAPY_VAULT_KEY`, `VaultDBPath()` resolves `CAPY_VAULT_PATH` with `$XDG_DATA_HOME/capy/vault.db` default (consistent with knowledge store XDG convention)
- [x] 1.3 Create `internal/vault/store.go` — `VaultStore` struct with lazy-init `getDB()`, `openDB()` using `store.EncryptedDSN()`, schema DDL execution, WAL+pragma setup, `Close()` with WAL checkpoint. Session replacement uses `UPDATE` (not DELETE+INSERT) so it overwrites location/metadata/blob in place and preserves `archived_at`
- [x] 1.4 Define schema DDL constant — 4 tables (`vault_sessions` with `title TEXT` + **1:1 location columns** `machine_id`/`claude_project_dir`/`project_path`/`git_branch`, `vault_files` with CASCADE, `vault_fts` with UNINDEXED columns including `message_index` **and `line_index`** (the source JSONL line number — the view-jump anchor), `vault_meta`) + **1 index** (`idx_sessions_end_time`; **no** `project_path` index — a substring `LIKE` can't use one). **No `vault_session_locations` table** — location is 1:1 on `vault_sessions`. CASCADE applies to `vault_files` only. `vault_migrations` is **not** in `schemaSQL` — the migration framework creates it (Task 1.7)
- [x] 1.5 Create `internal/vault/machine.go` — machine identity resolution: `CAPY_MACHINE_ID` env → `~/.config/capy/machine-id` file → generate UUIDv4 with atomic write. Fallback: if file write fails, derive deterministic ID from `hostname + os.Username()` and log a warning
- [x] 1.6 Implement prepared statements for core operations: insert session (incl. location columns), update session for replacement (overwrites metadata + location + blob), insert file, delete files by session_uuid, insert FTS row, delete FTS rows by session_uuid, delete session by UUID, query session by UUID (partial match 8+ chars, error on ambiguous), query files by session_uuid, list sessions with `--project`/`--limit` filters (plain `SELECT ... ORDER BY end_time DESC`, no GROUP BY), search with auto-quoting (plain keyword mode) returning `session_uuid`, `subagent_id`, `line_index`, and `role` for result navigation
- [x] 1.7 Implement migration framework: create the `vault_migrations` tracking table (by-name, like `store/migrate.go:ensureMigrationsTable` — the sole migration-state store; **no** `schema_version` in `vault_meta`) and a `migrateVault` function called from `openDB()`, following the `internal/store/migrate.go` pattern. This (not `schemaSQL`) creates `vault_migrations`
- [x] 1.8 Tests: DB creation with encryption, schema validation, machine ID generation and persistence (including write-failure fallback), prepared statement execution against empty DB, ON DELETE CASCADE verification (insert session + files, delete session, verify files gone), UPDATE-based replacement (verify location columns + raw_jsonl overwritten, files and FTS rebuilt, `archived_at` preserved)
- [x] 1.9 Verify: `go test -tags fts5 ./internal/vault/...` passes with `CAPY_VAULT_KEY=test-key`

## Task 2: FTS scanner — JSONL text extraction for search indexing
- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, Task 1
- **Docs:** [implementation.md#scanner](./implementation.md#scanner)

### Subtasks
- [x] 2.1 Create `internal/vault/scanner_types.go` — minimal JSON wire types: `jsonlLine` (with `cwd`, `gitBranch`, `aiTitle`, and `prUrl`/`prRepository`/`prNumber` fields; **no `customTitle`** — deferred tier), `jsonlMessage`, `contentBlock` (decoupled from `internal/session/parse.go`)
- [x] 2.2 Create `internal/vault/scanner.go` — `ScanSession(r io.Reader) (*ScanOutput, error)`: accepts `io.Reader` (works for both file and in-memory BLOB). Single-pass streaming JSONL reader with 16MB line buffer, progressive snapshot dedup via `(Type, Text, Name, ID)` tuple matching (same approach as `internal/session/parse.go:161-174`), system-reminder stripping. Returns `ScanOutput` with results, title, cwd, gitBranch
- [x] 2.3 Implement line-type routing per design.md JSONL Line Types table: extract from `user`, `assistant`, `ai-title`, `pr-link`, `attachment`, `system:away_summary`; **skip all other types by default** (incl. `custom-title`, `progress`, `agent-name`, `system:turn_duration`/`local_command`/`informational`, and any unknown type)
- [x] 2.4 Implement content-block extraction. **Assistant** blocks: text (keep), tool_use (name + input summary for Read/Edit/Bash/Agent), thinking (skip) — assistant entries never carry tool_result. **User** blocks: human text/string → `Role="user"`, and `tool_result` → a separate `Role="tool"` result (bounded text: 16KB cap, 75% head / 25% tail on char boundary, skip image/binary). tool_result lives in `user` entries, not assistant
- [x] 2.5 Extract metadata: **last** `aiTitle` → `ScanOutput.Title`, with guarded first-user-message fallback (string content, not `tool_result`, not `<…>`-prefixed); `customTitle` tier deferred (absent from JSONL); `cwd`/`gitBranch` from first user entry; timestamps from first/last lines that have one; `message_count` = human-text user turns + assistant turns (exclude tool_result-only user entries)
- [x] 2.6 Produce one `ScanResult` per message (NOT per turn-pair): human user text → `Role="user"`, each tool_result → `Role="tool"`, each assistant message → `Role="assistant"`, away-summary/pr-link → `Role="system"`. Set `LineIndex` (0-based source JSONL line; first/canonical line for deduped snapshots). Track `TurnIndex` and `MessageIndex` for ordering only
- [x] 2.7 Add sanitization: run `sanitize.StripSecrets()` on each `ScanResult.ContentText` before returning
- [x] 2.8 Implement subagent scanning: `ScanSubagent(r io.Reader, subagentID string) ([]ScanResult, error)` that scans a subagent JSONL from an `io.Reader` and produces `ScanResult` entries with populated `SubagentID`
- [x] 2.9 Tests against **small committed sanitized fixtures** (CI has no `~/.claude`). Verify: extracted text + tool names; **`tool_result` text extracted from `user` entries as `Role="tool"`** (bounded 16KB 75/25) via an explicit fixture so the user-branch is actually exercised; `--role user` returns no tool output; ai-title (last wins) extracted; guarded title fallback skips `tool_result`/`<…>` first lines; `gitBranch`/`cwd` extracted; base64/system-reminders absent; progressive snapshots deduplicated; `LineIndex` points at the source line; `pr-link` extracted; `agent-name`/`progress`/`custom-title`/unknown types produce no `ScanResult`; secrets stripped; subagent content via io.Reader. Fixtures must exercise: tool_result(user), ai-title, no-title fallback, progressive snapshots, pr-link/agent-name, subagents
- [x] 2.10 Verify: `go test -tags fts5 ./internal/vault/...` passes; scanner produces non-empty results for test fixtures

## Task 3: Session discovery and import pipeline end-to-end
- **Status:** done
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#discovery](./implementation.md#discovery), [implementation.md#import-pipeline](./implementation.md#import-pipeline)

### Subtasks
- [x] 3.1 Create `internal/vault/discovery.go`. Resolve the projects dir via the **existing** `config.ClaudeProjectsDir()` (`internal/config/paths.go`), extended to honor `CLAUDE_CONFIG_DIR` — **do not add a second helper** (this also fixes `session/sweep.go:SessionDir()`). Note: extending `config.ClaudeProjectsDir()` changes behavior for its existing caller `ResolveSourceProject`, so re-run `internal/config` + `internal/session` tests to confirm no regression. `DiscoverSessions(rootDir string) ([]SessionFile, error)`: auto-detect input type (Claude config dir, projects root, single project dir, loose JSONLs), walk and find `*.jsonl` + collect all files in `<uuid>/` directories recursively (skip **non-subagent** files > 5 MB; never skip `subagents/*.jsonl` or the main JSONL)
- [x] 3.2 Metadata extraction is now handled by the scanner (`ScanOutput`) — no separate `metadata.go` needed. `ScanOutput` provides title, cwd, gitBranch, timestamps, message_count
- [x] 3.3 Create `internal/vault/import.go` — `Import(store *VaultStore, sessions []SessionFile, opts ImportOptions) ImportResult`: orchestrate discovery → scan → idempotent upsert with batch transactions (~50 sessions or ~100MB, whichever first). On batch error: retry per-session. On session error: log and continue
- [x] 3.4 Implement composite content_hash with framing: for each file (main JSONL keyed `<uuid>.jsonl`, associated files keyed by relative path, all sorted by key), hash `len(key) || key || len(content) || content`. Also compute `size_bytes` = sum of all hashed content lengths (main + sidecars) — the replace tiebreaker, not main-JSONL size
- [x] 3.5 Implement idempotent import logic: compute composite hash, check existing by UUID, apply **total-size** merge (larger total content wins — main + sidecars, not main alone). Replacement uses `UPDATE vault_sessions SET ...` (metadata + location + raw_jsonl) + explicit delete/rebuild of vault_files and vault_fts. Insertion uses `INSERT INTO vault_sessions ...` **with location columns** (`project_path` = scanner's `cwd` → `config.unmanglePath` recovery → raw mangled name; `claude_project_dir`; `git_branch` from scanner; `machine_id`). On skip (same hash), nothing to update (single location already stored). Batches acquire the write lock via `beginImmediate` (BUSY-retry) for concurrency with the server sweep. Machine-ID mismatch detection: warn if no `vault_sessions.machine_id` matches the current machine
- [x] 3.6 Tests using **committed sanitized fixture directories** → verify: correct sessions in DB with title/gitBranch/project_path/machine_id populated (incl. cwd extraction for project_path); all associated files preserved in vault_files **except non-subagent files > 5 MB** (a > 5 MB subagent JSONL is still kept); idempotent re-import of an unchanged session skips; **larger total content replaces** (incl. shrinking main JSONL + grown sidecar — must replace, not skip) with location columns + raw_jsonl overwritten and files/FTS rebuilt (`archived_at` preserved); smaller total skipped; FTS rows created with sanitized content (one per message, with `tool_result` text as `Role="tool"` from user entries); composite hash + total `size_bytes` cover subagent changes; CLAUDE_CONFIG_DIR respected; auto-detect input types work
- [x] 3.7 Verify: `go test -tags fts5 ./internal/vault/...` passes; import a test fixture directory and query back sessions + files via store methods

## Task 4: CLI commands — group + read/query (import, list, search, show, stats, checkpoint)
- **Status:** done
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#cli-commands](./implementation.md#cli-commands)

### Subtasks
- [x] 4.1 Create `cmd/capy/vault.go` — `newVaultCmd()` cobra command group, register on root command in `main.go`. Shared flag: `--tui` (bool, default false). Shared pre-run: resolve vault DB path, verify `CAPY_VAULT_KEY`, **and probe `store.getDB()` once so a wrong key / corrupt DB fails fast with one clear error** before `import` (otherwise `vault.Import` reports it as N identical per-session `StatusError`s — see Task 3 follow-up below). **Note:** the getDB probe lives in the `import` command (via the new `VaultStore.Open()`), not a shared pre-run — exactly matching the Task 3 follow-up wording ("Task 4.1 now probes store.getDB() before invoking Import"). A shared pre-run probe would create an empty `vault.db` for `checkpoint` on a fresh install and risk pool-vs-checkpoint contention; the shared pre-run instead verifies `CAPY_VAULT_KEY` + resolves the path. `--tui` is defined but fails loud ("not yet implemented") in list/search/show until Task 6 wires it.
- [x] 4.2 Implement `import` subcommand — mutating by default with table output (UUID, title, project, size, status), `--dry-run` to preview without writing, `--path` for custom dir (auto-detects input type), `--project` filter. Calls `vault.Import()`
- [x] 4.3 Implement `list` subcommand — `ListSessions()` store method with `--project` filter (`WHERE project_path LIKE ?`), `--limit`, `--json`, reverse chronological sort. Table output: short UUID, title, project, date range, messages, size. One row per session (location is 1:1 — no GROUP BY)
- [x] 4.4 Implement `search` subcommand — plain keyword mode by default (auto-quote tokens), `--raw` for FTS5 MATCH syntax. `snippet()` for context. `--project` (`WHERE project_path LIKE ?`), `--after`, `--before`, `--role <user|assistant|tool|system>` (`tool` = tool_result output), `--limit`, `--json`. Output: ranked results with short UUID, project, date, role, snippet
- [x] 4.5 Implement `show` subcommand — partial UUID resolution (8+ chars, `WHERE uuid LIKE ?%`, show candidates on ambiguous match with date/project/title), fetch `raw_jsonl` + subagent `vault_files`, render with `--format <text|markdown|json>`, pipe through `$PAGER` (text format only). Non-JSONL vault_files not rendered. **Note:** rendering lives in a new `internal/vault/render.go` (faithful, unsanitized, independent of the scanner). Subagents render as appended standalone sections (design blesses standalone as spec-conformant); thinking excluded by default (no toggle until TUI Task 6); `--format json` emits verbatim `raw_jsonl`.
- [x] 4.6 Implement `stats` subcommand — query session count, total size, per-project breakdown, oldest/newest dates, `--json` (new `VaultStore.Stats()`)
- [x] 4.7 Implement `checkpoint` subcommand — flush WAL, report success (new public `VaultStore.Checkpoint()`, mirrors `ContentStore.Checkpoint`)
- [x] 4.8 Verify: `import` → `list` shows sessions with titles → `search <term>` finds expected result (plain keyword mode, `--role` filtering) → `show <id>` displays content with `--format markdown` export → `checkpoint` flushes WAL → `stats --json` shows correct counts (covered by `cmd/capy/vault_test.go:TestVaultCommands_EndToEnd`)

## Task 4b: CLI commands — destructive / filesystem / exec (restore, resume, delete)
- **Status:** done
- **Depends on:** Task 4
- **Size:** S
- **Can run in parallel with:** Task 5
- **Docs:** [implementation.md#cli-commands](./implementation.md#cli-commands)
- **Why a separate task:** these three are the highest-risk surface — `restore` writes files to disk (path traversal), `resume` execs an external process, `delete` is irreversible data loss. Splitting them out gives that surface a **focused `/kk:review-code` checkpoint** distinct from the read/query commands. (Reuses the Task 4 command group from 4.1.)

### Subtasks
- [x] 4b.1 Implement `restore` subcommand — partial UUID (8+ chars). Write `raw_jsonl` to `ClaudeProjectsDir()/<claude_project_dir>/` (respects CLAUDE_CONFIG_DIR) or the `--output` path (location is single per session — no location prompt). Restore all `vault_files` entries. **Path safety:** `filepath.EvalSymlinks` the restore root, then per entry reject absolute paths / `..` components and containment-check via `filepath.Rel` (see impl §Restore Command). Prompt before overwriting
- [x] 4b.2 Implement `resume` subcommand — partial UUID (8+ chars), `--dir` override flag. Pre-check `exec.LookPath("claude")`, close vault store. Directory fallback chain: `--dir` → existing `project_path` → cwd → prompt. Launch via `os/exec.Command` with inherited stdio. Return exit code through cobra
- [x] 4b.3 Implement `delete` subcommand — partial UUID (8+ chars), show session info, `--yes` to skip confirmation. Transactional: delete FTS rows then session (CASCADE handles `vault_files`)
- [x] 4b.4 Verify: `restore <id>` recreates files (path safety validated, CLAUDE_CONFIG_DIR respected, `diff` clean vs originals) → `resume <id>` launches `claude --resume` in the resolved dir → `delete <id>` removes the session from `list`/`search`

## Task 5: MCP server startup sweep
- **Status:** done
- **Depends on:** Task 3
- **Size:** S
- **Can run in parallel with:** Task 4, Task 4b
- **Docs:** [implementation.md#mcp-server-startup-sweep](./implementation.md#mcp-server-startup-sweep)

### Subtasks
- [x] 5.1 Extend MCP server startup in `internal/server/server.go` — after existing `session.Sweep()`, add vault sweep goroutine for current project. The goroutine **opens its own `VaultStore` and `Close()`s it when done** (Close runs the WAL checkpoint; `shutdown()` closes only the ContentStore; existing `bgWg.Wait()` ensures the sweep + Close finish before exit). Skip silently if `CAPY_VAULT_KEY` not set (vault is opt-in). Use cooperative cancellation (server context + timeout)
- [x] 5.2 Tests: server startup with `CAPY_VAULT_KEY` set imports sessions from test project directory. Server startup without key skips silently
- [x] 5.3 Verify: start MCP server with vault key set, check `capy vault list` shows sessions from the server's project

## Task 6: TUI interface — interactive browsing, search, and viewing
- **Status:** done
- **Depends on:** Task 4, Task 4b
- **Size:** L
- **Can run in parallel with:** —
- **Docs:** [implementation.md#tui-implementation](./implementation.md#tui-implementation)
- **Risk focus:** 6.4 (viewer) is ~half this task's risk — lazy line-indexing + two render targets + subagent search-jump + inline rendering. It's the place to slow down (not a separate task, but where bugs will concentrate). The models are interdependent (`app.go` composes list+viewer+search), so this stays one task rather than splitting.

### Subtasks
- [x] 6.1 Add bubbletea ecosystem dependencies to `go.mod`: `bubbletea` v1.3.10, `bubbles` v1.0.0, `lipgloss` v1.1.0 (v1 line — `@latest` resolves to v1; v2 is pre-release under `/v2` paths). **Excluded `glamour` from v1** (confirmed with user); lipgloss-only rendering. NOTE: context7 serves bubbles **v2** docs — v1 API verified via `go doc` against pinned versions (indexed as `kk:lang-idioms`)
- [x] 6.2 Create `internal/vault/tui/styles.go` — lipgloss style definitions for roles, markers, status bar, results, errors
- [x] 6.3 Create `internal/vault/tui/list.go` — session list model wrapping `bubbles/list`, custom `sessionItem` rendering (title + short UUID/date/msgs/size/project), data from `VaultStore.ListSessions()`. Built-in `/` filter disabled so `/` opens global FTS search (see Follow-up: in-list fuzzy filter)
- [x] 6.4 Create `internal/vault/tui/viewer.go` (+ `internal/vault/transcript.go` for the shared `ParseTranscript` parser, and `tui/render.go` for lipgloss styling + the source-line→display-row map). **Markers-only subagents** (confirmed with user): launch points render as markers; a subagent transcript is viewed standalone via search-jump (exact, by `subagent_id`+`line_index`) or by selecting an openable marker (`]`/`[` focus, `Enter` open), `Esc`/`q` returns. Jump resolution uses `line_index`/`subagent_id`, never `turn_index`. vim keys (j/k/g/G, ctrl+d/u). **Deviation:** eager render-once instead of lazy windowing — see Follow-up below
- [x] 6.5 Create `internal/vault/tui/search.go` — `bubbles/textinput` + results list, debounced FTS5 queries (200ms via `tea.Tick`, latest-wins by seq), snippet rows carrying `subagent_id`/`line_index` anchors
- [x] 6.6 Create `internal/vault/tui/app.go` — root model composing list + viewer + search, **mode-based single-pane** transitions (browse ↔ search ↔ view) + key dispatch + `Run()` entry. (Deviation from the 3-panel split — see Follow-up below)
- [x] 6.7 Wire `--tui` flag in CLI commands: `list --tui` browse, `search <query> --tui` search (prefilled), `show <id> --tui` view. `launchTUI` probes `store.Open()` before the alt-screen. `restore`/`resume`/`delete` still reject `--tui` (no interactive mode)
- [x] 6.8 Verify: logic covered by `internal/vault/tui` unit tests (list/search/view transitions, subagent search-jump + return, marker focus/open + highlight, debounce gating, resize-stable scroll) + `transcript_test.go`; race-clean, ~86% tui coverage. **Interactive terminal walkthrough still recommended (needs a TTY — cannot run in CI).** Isolated `/kk:review-code` applied: fixed search-results scroll window, focused-marker highlight (color-independent glyph), value-receiver consistency (`setActive`/`openSession`), and resize-stable scroll (`savedMainLine` source-line anchor).

## Task 7: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 4b, Task 5, Task 6
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 7.1 Run `/kk:test` — full test suite including vault package, verify no regressions in existing tests
- [ ] 7.2 Run `/kk:document` — update relevant docs (architecture.md, CLAUDE.md if needed)
- [ ] 7.3 Run `/kk:review-code go` — review the full vault implementation
- [ ] 7.4 Run `/kk:review-spec` — verify implementation matches design and implementation docs

## Follow-up: Route `session.SessionDir()` through the shared `config.ClaudeProjectsDir()`
- **Status:** pending (non-blocking, post-v1)
- **Description:** Task 3.1 extends `config.ClaudeProjectsDir()` (`internal/config/paths.go:62`) to honor `CLAUDE_CONFIG_DIR`, and vault uses it directly. `internal/session/sweep.go:SessionDir()` still mangles against a hardcoded `~/.claude/projects/`; update it to resolve the base via `config.ClaudeProjectsDir()` so both paths agree.
- **Why deferred:** Vault uses the shared helper from day one; this only realigns the pre-existing session sweep (current-project only). Low risk, clean follow-up.

## Follow-up: `context.Context` propagation through VaultStore query/exec paths
- **Status:** pending (non-blocking, post-v1) — declined during the Task 1 isolated review
- **Description:** The Task 1 isolated review (code-reviewer + pal, corroborated) flagged that `VaultStore`'s read/write methods use plain `db.Query`/`db.Exec`/`db.Begin` rather than the `*Context` variants that `profiles/go/review-code/database.md` prefers.
- **Why deferred (deliberate):** The sibling knowledge store (`internal/store`) uses plain calls almost everywhere (109 `Exec`, 13 `Query`, 69 `QueryRow`, plain `Begin`; only 3 `BeginTx`). Vault mirrors it. None of the public methods accept a `context.Context`, so threading `QueryContext(s.ctx())` today is inert ceremony (`s.ctx()` returns `context.Background()`) that would diverge vault from its sibling for zero functional benefit. Real cancellation needs the public methods to take a `context.Context`, which is speculative until a cancelling caller exists (Task 5's server-startup sweep). **Partially addressed in Task 5:** `vault.Import` now takes a leading `ctx` and checks `ctx.Err()` at each session boundary (loop-level cooperative cancellation), so the server sweep's 30s timeout / shutdown stops it promptly without blocking `bgWg.Wait()`. **Still deferred:** the `VaultStore` DB methods themselves remain plain (`db.Query`/`db.Exec`/`db.Begin`) — an in-flight transaction is not interruptible. **Concrete next step:** convert both `internal/store` and `internal/vault` to `*Context` variants together (and add `ctx` params to the remaining `VaultStore` methods) so the two stores stay consistent. (`sqliteutil.OpenWithCanary` already takes a `ctx` — that part is done.)

## Follow-up: `vault.Import` has no fail-fast on a DB-open failure
- **Status:** resolved (Task 4.1 CLI + Task 5 server sweep both probe `Open()`)
- **Description:** `Import` never probes the connection up front. On a wrong `CAPY_VAULT_KEY` or corrupt vault, `warnOnMachineMismatch` swallows the open error and the loop then calls `SessionDigest`/`WriteBatch` per session — each hitting the same open error — so `Import` returns with `Errors == N` identical failures instead of one clean abort. This is mild friction (a table full of duplicate errors), not data loss.
- **How resolved:** `Import` itself still returns `ImportResult` (no `error`) and does not probe — the fix lives in its callers. **Task 4.1** probes `store.Open()` before invoking `Import` for the CLI path; **Task 5** does the same in `server.vaultSweep` (the non-CLI caller this follow-up anticipated), logging `slog.Warn("vault sweep: cannot open vault store")` and returning early on failure. Both user-facing callers of `Import` now fail fast. (The corroborated Task 5 isolated review re-flagged the missing sweep probe; this fix closes it.) If a *third* `Import` caller is ever added, give it the same `st.Open()` pre-probe.

## Follow-up: TUI viewer uses eager render, not lazy windowing
- **Status:** pending (non-blocking, post-v1) — deliberate Task 6 deviation
- **Description:** design.md §Read-path performance specifies a lazy `\n`-offset index that unmarshals only the visible viewport lines. Task 6 instead parses + styles the whole transcript once (`tui/render.go:renderTranscript`) and uses `bubbles/viewport`'s native scrolling, resolving search jumps via a source-line→display-row map (`msgRowStart`/`rowForLine`).
- **Why deviated:** (1) the design's lazy+no-dedup combination would re-render every progressive assistant snapshot (3× growing copies of one message) — poor UX; deduping requires cross-line state that breaks per-line laziness; (2) eager cost (transient parse, retained display string ≈ blob size) equals what `capy vault show` already pays, and typical sessions are ≤1MB (design projection: 536KB avg); (3) the search→scroll **contract** (`line_index`/`subagent_id` anchors, never `turn_index`) is fully preserved. **Concrete next step:** if profiling shows lag on multi-MB sessions, implement a windowed renderer that re-renders only the visible source-line span around `viewport` offset and lazy-loads on scroll-to-edge.

## Follow-up: TUI is mode-based single-pane, not the 3-panel split
- **Status:** pending (non-blocking, post-v1) — deliberate Task 6 deviation
- **Description:** design.md §Layout sketches a side-by-side list+viewer split with a bottom bar. Task 6 ships mode-based full-screen panes (browse ↔ view ↔ search) instead, which satisfies the Task 6.8 flow with far less layout/focus machinery. **Next step:** if a persistent split is wanted, compose list (left) + viewer (right) with lipgloss `JoinHorizontal` and route focus between panes.

## Follow-up: TUI subagent markers use order-based launch mapping
- **Status:** pending (non-blocking, post-v1)
- **Description:** `ParseTranscript` maps Task/Agent launch points to archived subagent files **in order**, and only makes markers openable when the counts align (otherwise markers are visible-only). The JSONL carries no verified `tool_use_id`↔`agent-<id>` link, so a precise mapping isn't possible from the data alone; search-jump (which uses the stored `subagent_id`) is always exact. **Next step:** if Claude Code's JSONL gains a reliable launch→subagent correlation (or the `agent-<id>` is confirmed to equal the launching `tool_use` id), map markers precisely and drop the count-alignment guard.

## Follow-up: TUI deferred keybindings (in-list filter, f/r/c/R)
- **Status:** pending (non-blocking, post-v1)
- **Description:** design.md §Key Bindings lists `f` (filter project), `r` (restore), `c` (copy message), `R` (resume), plus in-list fuzzy filtering. v1 ships the core browse/search/view flow (Task 6.8) only; the list's built-in `/` filter is disabled so `/` opens global FTS search. **Next step:** add project filtering to the list model, and wire `r`/`R` to `vault.RestoreSession` + `claude --resume` (note: these are the destructive/exec surface — they warrant the same care as Task 4b and should suspend/teardown the bubbletea program before exec).

## Dependency Graph

```
Task 0 (investigate) ─── optional, does not block shipping

Task 1 (store) ──┐
                 ├─→ Task 3 (import) ─┬─→ Task 4 (CLI read) ─→ Task 4b (CLI destructive) ─→ Task 6 (TUI) ─→ Task 7
Task 2 (scanner)─┘                    └─→ Task 5 (server sweep) ───────────────────────────────────────────→ Task 7
```
