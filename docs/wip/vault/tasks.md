# Tasks: Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-05-28
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic cleanup, sharing/export with redaction, PreCompact snapshots (deferred), file-history preservation, cross-machine vault merge command, shell completions (trivial via cobra, add during polish)

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
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, Task 2
- **Docs:** [implementation.md#encryption--db-initialization](./implementation.md#encryption--db-initialization)

### Subtasks
- [ ] 1.1 Export corruption helpers from `internal/store/`: rename `isSQLiteCorruption` → `IsSQLiteCorruption`, `backupCorruptDB` → `BackupCorruptDB`, `isWrongPassphrase` → `IsWrongPassphrase`, `isGarbageFile` → `IsGarbageFile` in `internal/store/retry.go` and `internal/store/encryption.go`. Add doc comments. (Alternative: extract to `internal/sqliteutil/`.)
- [ ] 1.2 Create `internal/vault/encryption.go` — `RequireVaultKey()` reads `CAPY_VAULT_KEY`, `VaultDBPath()` resolves `CAPY_VAULT_PATH` with `$XDG_DATA_HOME/capy/vault.db` default (consistent with knowledge store XDG convention)
- [ ] 1.3 Create `internal/vault/store.go` — `VaultStore` struct with lazy-init `getDB()`, `openDB()` using `store.EncryptedDSN()`, schema DDL execution, WAL+pragma setup, `Close()` with WAL checkpoint. Session replacement uses `UPDATE` (not DELETE+INSERT) to preserve locations
- [ ] 1.4 Define schema DDL constant — 5 tables (`vault_sessions` with `title TEXT`, `vault_session_locations` with CASCADE, `vault_files` with CASCADE, `vault_fts` with UNINDEXED columns including `message_index`, `vault_meta`) + 3 indexes + `vault_migrations` tracking table. Note: CASCADE is for session deletion only; replacement preserves locations via UPDATE
- [ ] 1.5 Create `internal/vault/machine.go` — machine identity resolution: `CAPY_MACHINE_ID` env → `~/.config/capy/machine-id` file → generate UUIDv4 with atomic write. Fallback: if file write fails, derive deterministic ID from `hostname + os.Username()` and log a warning
- [ ] 1.6 Implement prepared statements for core operations: insert session, update session (for replacement — preserves locations), upsert session location, insert file, delete files by session_uuid, insert FTS row, delete FTS rows by session_uuid, delete session by UUID, query session by UUID (partial match 8+ chars, error on ambiguous), query locations by session_uuid, query files by session_uuid, list sessions with location GROUP BY and filters (multi-location dedup), search with auto-quoting (plain keyword mode)
- [ ] 1.7 Implement migration framework: `vault_migrations` table, `migrateVault` function called from `openDB()`, following `internal/store/migrate.go` pattern
- [ ] 1.8 Tests: DB creation with encryption, schema validation, machine ID generation and persistence (including write-failure fallback), prepared statement execution against empty DB, ON DELETE CASCADE verification (insert session + locations + files, delete session, verify locations and files gone), UPDATE-based replacement (verify locations preserved when session is updated, files and FTS rebuilt)
- [ ] 1.9 Verify: `go test -tags fts5 ./internal/vault/...` passes with `CAPY_VAULT_KEY=test-key`

## Task 2: FTS scanner — JSONL text extraction for search indexing
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, Task 1
- **Docs:** [implementation.md#scanner](./implementation.md#scanner)

### Subtasks
- [ ] 2.1 Create `internal/vault/scanner_types.go` — minimal JSON wire types: `jsonlLine` (with `cwd`, `gitBranch`, `aiTitle`, `customTitle` fields), `jsonlMessage`, `contentBlock` (decoupled from `internal/session/parse.go`)
- [ ] 2.2 Create `internal/vault/scanner.go` — `ScanSession(r io.Reader) (*ScanOutput, error)`: accepts `io.Reader` (works for both file and in-memory BLOB). Single-pass streaming JSONL reader with 16MB line buffer, progressive snapshot dedup via `(Type, Text, Name, ID)` tuple matching (same approach as `internal/session/parse.go:161-174`), system-reminder stripping. Returns `ScanOutput` with results, title, cwd, gitBranch
- [ ] 2.3 Implement line-type routing per design.md JSONL Line Types table: extract from `user`, `assistant`, `ai-title`, `custom-title`, `attachment`, `system:away_summary`; skip all other types
- [ ] 2.4 Implement text extraction per content block type: text blocks (keep), tool_use (extract name + input summary for Read/Edit/Bash/Agent), thinking (skip), tool_result (extract bounded text content — first 16KB with head+tail truncation, skip image/binary blocks)
- [ ] 2.5 Extract metadata: `aiTitle`/`customTitle` → `ScanOutput.Title` (custom wins); `cwd`/`gitBranch` from first user entry; timestamps from first/last lines; message count
- [ ] 2.6 Produce one `ScanResult` per message (NOT per turn-pair): each user message → one `ScanResult` with `Role="user"`, each assistant message → one `ScanResult` with `Role="assistant"`. Track `TurnIndex` (increments on user→assistant boundary) and `MessageIndex` (sequential within a turn)
- [ ] 2.7 Add sanitization: run `sanitize.StripSecrets()` on each `ScanResult.ContentText` before returning
- [ ] 2.8 Implement subagent scanning: `ScanSubagent(r io.Reader, subagentID string) ([]ScanResult, error)` that scans a subagent JSONL from an `io.Reader` and produces `ScanResult` entries with populated `SubagentID`
- [ ] 2.9 Tests: scan a real session JSONL fixture → verify: extracted text contains expected content, tool names present, ai-title extracted, gitBranch and cwd extracted, base64/system-reminders absent, progressive snapshots deduplicated correctly, tool_result text extracted (bounded), subagent content extracted via io.Reader, secrets stripped from ContentText, all skipped line types produce no ScanResult
- [ ] 2.10 Verify: `go test -tags fts5 ./internal/vault/...` passes; scanner produces non-empty results for test fixtures

## Task 3: Session discovery and import pipeline end-to-end
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#discovery](./implementation.md#discovery), [implementation.md#import-pipeline](./implementation.md#import-pipeline)

### Subtasks
- [ ] 3.1 Create `internal/vault/discovery.go` — `ClaudeProjectsDir()` resolves `CLAUDE_CONFIG_DIR` env → `~/.claude/projects/` default (shared by discovery, restore, resume). `DiscoverSessions(rootDir string) ([]SessionFile, error)`: auto-detect input type (Claude config dir, projects root, single project dir, loose JSONLs), walk and find `*.jsonl` + collect all files in `<uuid>/` directories recursively (skip files > 5 MB)
- [ ] 3.2 Metadata extraction is now handled by the scanner (`ScanOutput`) — no separate `metadata.go` needed. `ScanOutput` provides title, cwd, gitBranch, timestamps, message_count
- [ ] 3.3 Create `internal/vault/import.go` — `Import(store *VaultStore, sessions []SessionFile, opts ImportOptions) ImportResult`: orchestrate discovery → scan → idempotent upsert with batch transactions (~50 sessions or ~100MB, whichever first). On batch error: retry per-session. On session error: log and continue
- [ ] 3.4 Implement composite content_hash with framing: for each file (main JSONL + associated files sorted by relative path), hash `len(path) || path || len(content) || content`
- [ ] 3.5 Implement idempotent import logic: compute composite hash, check existing by UUID, apply size-based merge (larger wins). Replacement uses `UPDATE vault_sessions SET ...` (preserves locations) + explicit delete/rebuild of vault_files and vault_fts. Insertion uses `INSERT INTO vault_sessions ...`. Upsert `vault_session_locations` with `project_path` (from scanner's `cwd` or mangled dir name), `claude_project_dir`, and `gitBranch` from scanner. On skip (same hash), still upsert location. Machine-ID mismatch detection: warn if vault contains sessions from other machines not yet including current machine
- [ ] 3.6 Tests: import from a test directory with multiple session files including subagents and tool-results → verify correct sessions in DB with title/gitBranch populated, all associated files preserved in vault_files (except > 5MB), locations recorded correctly (including cwd extraction), idempotent re-import skips but records new locations, same UUID in different project dirs produces multiple location rows, larger file replaces smaller with locations preserved (UPDATE not DELETE+INSERT), smaller file skipped, FTS rows created with sanitized content (one row per message, including bounded tool_result text), composite hash with framing covers subagent changes, CLAUDE_CONFIG_DIR respected, auto-detect input types work
- [ ] 3.7 Verify: `go test -tags fts5 ./internal/vault/...` passes; import a test fixture directory and query back sessions + locations + files via store methods

## Task 4: CLI commands — import, list, search, show, restore, resume, stats
- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#cli-commands](./implementation.md#cli-commands)

### Subtasks
- [ ] 4.1 Create `cmd/capy/vault.go` — `newVaultCmd()` cobra command group, register on root command in `main.go`. Shared flag: `--tui` (bool, default false). Shared pre-run: resolve vault DB path, verify `CAPY_VAULT_KEY`
- [ ] 4.2 Implement `import` subcommand — mutating by default with table output (UUID, title, project, size, status), `--dry-run` to preview without writing, `--path` for custom dir (auto-detects input type), `--project` filter. Calls `vault.Import()`
- [ ] 4.3 Implement `list` subcommand — `ListSessions()` store method with `--project` filter (EXISTS subquery), `--limit`, `--json`, reverse chronological sort. Table output: short UUID, title, project, date range, messages, size. Multi-location dedup via GROUP BY
- [ ] 4.4 Implement `search` subcommand — plain keyword mode by default (auto-quote tokens), `--raw` for FTS5 MATCH syntax. `snippet()` for context. `--project` (EXISTS subquery), `--after`, `--before`, `--role` filters, `--limit`, `--json`. Output: ranked results with short UUID, project, date, role, snippet
- [ ] 4.5 Implement `show` subcommand — partial UUID resolution (8+ chars, `WHERE uuid LIKE ?%`, show candidates on ambiguous match with date/project/title), fetch `raw_jsonl` + subagent `vault_files`, render with `--format <text|markdown|json>`, pipe through `$PAGER` (text format only). Non-JSONL vault_files not rendered
- [ ] 4.6 Implement `restore` subcommand — partial UUID (8+ chars), choose location (prompt if multiple, use `--output` to override). Write `raw_jsonl` to `ClaudeProjectsDir()/<claude_project_dir>/` (respects CLAUDE_CONFIG_DIR) or `--output` path. Restore all `vault_files` entries. **Path safety:** validate each `relative_path`. Prompt before overwriting
- [ ] 4.7 Implement `resume` subcommand — partial UUID (8+ chars), `--dir` override flag. Pre-check `exec.LookPath("claude")`, close vault store. Directory fallback chain: `--dir` → existing `project_path` → cwd → prompt. Prefer location matching current `machine_id`. Launch via `os/exec.Command` with inherited stdio. Return exit code through cobra
- [ ] 4.8 Implement `delete` subcommand — partial UUID (8+ chars), show session info, `--yes` to skip confirmation. Transactional: delete FTS rows then session (CASCADE handles files + locations)
- [ ] 4.9 Implement `stats` subcommand — query session count, total size, per-project breakdown, oldest/newest dates, `--json`
- [ ] 4.10 Implement `checkpoint` subcommand — flush WAL, report success
- [ ] 4.11 Verify: run each command manually against a vault with imported test sessions. `import` → `list` shows sessions with titles → `search <term>` finds expected result (plain keyword mode, `--role` filtering) → `show <id>` displays content with `--format markdown` export → `restore <id>` recreates files (path safety validated, CLAUDE_CONFIG_DIR respected) → `delete <id>` removes session → `checkpoint` flushes WAL → `stats --json` shows correct counts

## Task 5: MCP server startup sweep
- **Status:** pending
- **Depends on:** Task 3
- **Size:** S
- **Can run in parallel with:** Task 4
- **Docs:** [implementation.md#mcp-server-startup-sweep](./implementation.md#mcp-server-startup-sweep)

### Subtasks
- [ ] 5.1 Extend MCP server startup in `internal/server/server.go` — after existing `session.Sweep()`, add vault sweep goroutine for current project. Skip silently if `CAPY_VAULT_KEY` not set (vault is opt-in). Use cooperative cancellation (server context + timeout)
- [ ] 5.2 Tests: server startup with `CAPY_VAULT_KEY` set imports sessions from test project directory. Server startup without key skips silently
- [ ] 5.3 Verify: start MCP server with vault key set, check `capy vault list` shows sessions from the server's project

## Task 6: TUI interface — interactive browsing, search, and viewing
- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#tui-implementation](./implementation.md#tui-implementation)

### Subtasks
- [ ] 6.1 Add bubbletea ecosystem dependencies to `go.mod`: `bubbletea`, `bubbles`, `lipgloss`, `glamour`
- [ ] 6.2 Create `internal/vault/tui/styles.go` — lipgloss style definitions for panels, roles, highlights, status bar
- [ ] 6.3 Create `internal/vault/tui/list.go` — session list model wrapping `bubbles/list`, custom item rendering (short UUID, project, date, size), data from `VaultStore.ListSessions()`
- [ ] 6.4 Create `internal/vault/tui/viewer.go` — session content viewer wrapping `bubbles/viewport`, fetches `raw_jsonl` + subagent files from `vault_files`. Uses lazy line-indexing: holds raw `[]byte` + `\n`-offset index, only unmarshals lines in the visible viewport on demand. Human/Assistant formatting with subagent content inline (dimmed prefix), markdown rendering via glamour, vim-style keybindings (j/k/g/G)
- [ ] 6.5 Create `internal/vault/tui/search.go` — search model combining `bubbles/textinput` with results list, debounced FTS5 queries (200ms), snippet display with highlights
- [ ] 6.6 Create `internal/vault/tui/app.go` — root model composing list + viewer + search, three-panel layout, mode transitions (browse ↔ search ↔ view), key binding dispatch
- [ ] 6.7 Wire `--tui` flag in CLI commands: `list --tui` starts in browse mode, `search --tui` starts in search mode, `show --tui` starts in view mode
- [ ] 6.8 Verify: `capy vault list --tui` shows navigable session list → select session shows content in viewer → `/` activates search → results are clickable → `q` exits cleanly

## Task 7: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 7.1 Run `/kk:test` — full test suite including vault package, verify no regressions in existing tests
- [ ] 7.2 Run `/kk:document` — update relevant docs (architecture.md, CLAUDE.md if needed)
- [ ] 7.3 Run `/kk:review-code go` — review the full vault implementation
- [ ] 7.4 Run `/kk:review-spec` — verify implementation matches design and implementation docs

## Follow-up: Extract shared `ClaudeProjectsDir()` into existing session package
- **Status:** pending (non-blocking, post-v1)
- **Description:** `internal/session/sweep.go:SessionDir()` hardcodes `~/.claude/projects/` without respecting `CLAUDE_CONFIG_DIR`. Vault's `ClaudeProjectsDir()` helper does respect it. Extract the vault helper to a shared location (e.g., `internal/session/paths.go` or `internal/platform/`) and update `SessionDir()` to use it. This aligns both code paths.
- **Why deferred:** Vault's discovery already works correctly. The asymmetry only affects the existing session sweep (which only runs for the current project anyway). Low risk, clean follow-up.

## Dependency Graph

```
Task 0 (investigate) ─── optional, does not block shipping

Task 1 (store) ──────┐
                      ├──→ Task 3 (import) ──→ Task 4 (CLI) ──→ Task 6 (TUI) ──→ Task 7
Task 2 (scanner) ────┘          │
                                └──→ Task 5 (server sweep) ─────────────────────→ Task 7
```
