# Tasks: Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-05-28
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic cleanup, sharing/export, PreCompact snapshots (deferred), file-history preservation, cross-machine vault merge

## Task 0: Investigate PreCompact hook payload
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1, Task 2
- **Slicing strategy:** Risk-First (most uncertain piece first)
- **Blocking:** No — this task is informational and does NOT block shipping. Findings feed into a future PreCompact archival feature (see design.md Future Improvements).
- **Docs:** [implementation.md#precompact-hook-deferred](./implementation.md#precompact-hook-deferred)

### Subtasks
- [ ] 0.1 Add a debug handler in `internal/hook/precompact.go` that writes the raw stdin payload to `~/.config/capy/precompact-debug.json`
- [ ] 0.2 Trigger `/compact` in a Claude Code session and capture the payload
- [ ] 0.3 Document the JSON structure: which fields are present, whether it includes session file path, session ID, project directory
- [ ] 0.4 Verify timing: does the hook fire before or after file mutation? (Check file mtime before and after hook execution)
- [ ] 0.5 Write findings to `docs/wip/vault/precompact-investigation.md` — this unblocks future PreCompact archival implementation
- [ ] 0.6 Verify: documented payload structure exists and is sufficient to locate the session file

## Task 1: Vault store foundation — schema, encryption, connection lifecycle
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, Task 2
- **Docs:** [implementation.md#encryption--db-initialization](./implementation.md#encryption--db-initialization)

### Subtasks
- [ ] 1.1 Export corruption helpers from `internal/store/`: rename `isSQLiteCorruption` → `IsSQLiteCorruption`, `backupCorruptDB` → `BackupCorruptDB`, `isWrongPassphrase` → `IsWrongPassphrase`, `isGarbageFile` → `IsGarbageFile` in `internal/store/retry.go` and `internal/store/encryption.go`. Add doc comments. (Alternative: extract to `internal/sqliteutil/`.)
- [ ] 1.2 Create `internal/vault/encryption.go` — `RequireVaultKey()` reads `CAPY_VAULT_KEY`, `VaultDBPath()` resolves `CAPY_VAULT_PATH` with `~/.config/capy/vault.db` default
- [ ] 1.3 Create `internal/vault/store.go` — `VaultStore` struct with lazy-init `getDB()`, `openDB()` using `store.EncryptedDSN()`, schema DDL execution, WAL+pragma setup, `Close()` with WAL checkpoint
- [ ] 1.4 Define schema DDL constant — 5 tables (`vault_sessions`, `vault_session_locations` with CASCADE, `vault_files` with CASCADE, `vault_fts` with UNINDEXED columns including `message_index`, `vault_meta`) + 3 indexes (`idx_sessions_end_time`, `idx_locations_project`, `idx_locations_session`) with correct constraints and FKs
- [ ] 1.5 Create `internal/vault/machine.go` — machine identity resolution: `CAPY_MACHINE_ID` env → `~/.config/capy/machine-id` file → generate UUIDv4 with atomic write. Fallback: if file write fails, derive deterministic ID from `hostname + os.Username()` and log a warning
- [ ] 1.6 Implement prepared statements for core operations: insert/replace session, upsert session location, insert file, insert FTS row, delete FTS rows by session_uuid, query session by UUID, query locations by session_uuid, query files by session_uuid, list sessions with location JOIN and filters
- [ ] 1.7 Tests: DB creation with encryption, schema validation, machine ID generation and persistence (including write-failure fallback), prepared statement execution against empty DB, ON DELETE CASCADE verification (insert session + locations + files, delete session, verify locations and files gone)
- [ ] 1.8 Verify: `go test -tags fts5 ./internal/vault/...` passes with `CAPY_VAULT_KEY=test-key`

## Task 2: FTS scanner — JSONL text extraction for search indexing
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 0, Task 1
- **Docs:** [implementation.md#scanner](./implementation.md#scanner)

### Subtasks
- [ ] 2.1 Create `internal/vault/scanner_types.go` — minimal JSON wire types: `jsonlLine`, `jsonlMessage`, `contentBlock` (decoupled from `internal/session/parse.go`)
- [ ] 2.2 Create `internal/vault/scanner.go` — `ScanSession(path string) ([]ScanResult, error)`: single-pass streaming JSONL reader with 16MB line buffer, progressive snapshot dedup via `(Type, Text, Name, ID)` tuple matching (same approach as `internal/session/parse.go:161-174`), system-reminder stripping. Also extracts `cwd` from the first user message that has it (returned in metadata, not in ScanResult)
- [ ] 2.3 Implement text extraction per content block type: text blocks (keep), tool_use (extract name + input summary for Read/Edit/Bash/Agent), thinking (skip), tool_result (skip content but note tool name)
- [ ] 2.4 Produce one `ScanResult` per message (NOT per turn-pair): each user message → one `ScanResult` with `Role="user"`, each assistant message → one `ScanResult` with `Role="assistant"`. Track `TurnIndex` (increments on user→assistant boundary) and `MessageIndex` (sequential within a turn)
- [ ] 2.5 Add sanitization: run `sanitize.StripSecrets()` on each `ScanResult.ContentText` before returning
- [ ] 2.6 Implement subagent scanning: `ScanSubagents(dir string) ([]ScanResult, error)` that identifies `subagents/agent-*.jsonl` files by path pattern, reads `.meta.json` for context, and produces `ScanResult` entries with populated `SubagentID`
- [ ] 2.7 Tests: scan a real session JSONL fixture → verify extracted text contains expected content, tool names present, base64/system-reminders absent, progressive snapshots deduplicated correctly (test with multiple blocks sharing same message.id), subagent content extracted, secrets stripped from ContentText
- [ ] 2.8 Verify: `go test -tags fts5 ./internal/vault/...` passes; scanner produces non-empty results for test fixtures

## Task 3: Session discovery and import pipeline end-to-end
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#discovery](./implementation.md#discovery), [implementation.md#import-pipeline](./implementation.md#import-pipeline)

### Subtasks
- [ ] 3.1 Create `internal/vault/discovery.go` — `ClaudeConfigDir()` resolves `CLAUDE_CONFIG_DIR` env → `~/.claude/` default. `DiscoverSessions(rootDir string) ([]SessionFile, error)`: walk projects directory, find `*.jsonl` + collect all files in `<uuid>/` directories recursively (subagents, tool-results, any sidecars)
- [ ] 3.2 Create `internal/vault/metadata.go` — `ExtractMetadata(path string) (SessionMeta, error)`: fast first-pass reading timestamps, type fields, message count, and `cwd` from the first user message that has it. Git branch extraction with detached HEAD handling (literal `"HEAD"` → NULL)
- [ ] 3.3 Create `internal/vault/import.go` — `Import(store *VaultStore, sessions []SessionFile, opts ImportOptions) ImportResult`: orchestrate discovery → metadata → idempotent upsert with batch transactions (~50 per tx for bulk, per-session fallback on failure)
- [ ] 3.4 Implement composite content_hash: SHA-256 over main JSONL bytes concatenated with all associated file bytes (sorted by relative path)
- [ ] 3.5 Implement idempotent import logic: compute composite hash, check existing by UUID, apply size-based merge (larger wins), transactional insert/replace (delete old FTS → insert session + locations + files + FTS rows). Upsert `vault_session_locations` with `project_path` (from `cwd` field or mangled dir name) and `claude_project_dir`. On skip (same hash), still upsert location to record this (machine, project dir) combination
- [ ] 3.6 Tests: import from a test directory with multiple session files including subagents and tool-results → verify correct sessions in DB, all associated files preserved in vault_files, locations recorded correctly (including cwd extraction), idempotent re-import skips but records new locations, same UUID in different project dirs produces multiple location rows, larger file replaces smaller, smaller file skipped, FTS rows created with sanitized content (one row per message, not per turn-pair), composite hash covers subagent changes, CLAUDE_CONFIG_DIR respected
- [ ] 3.7 Verify: `go test -tags fts5 ./internal/vault/...` passes; import a test fixture directory and query back sessions + locations + files via store methods

## Task 4: CLI commands — import, list, search, show, restore, resume, stats
- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#cli-commands](./implementation.md#cli-commands)

### Subtasks
- [ ] 4.1 Create `cmd/capy/vault.go` — `newVaultCmd()` cobra command group, register on root command in `main.go`. Shared flag: `--tui` (bool, default false). Shared pre-run: resolve vault DB path, verify `CAPY_VAULT_KEY`
- [ ] 4.2 Implement `import` subcommand — mutating by default with table output (UUID, project, size, status), `--dry-run` to preview without writing, `--path` for custom dir, `--project` filter. Calls `vault.Import()`
- [ ] 4.3 Implement `list` subcommand — `ListSessions()` store method with `--project` filter, `--limit`, reverse chronological sort. Table output: short UUID, project, date range, messages, size
- [ ] 4.4 Implement `search` subcommand — FTS5 MATCH query with `snippet()`, joined to `vault_sessions` for metadata. `--project`, `--after`, `--before`, `--role` filters, `--limit`. Output: ranked results with short UUID, project, date, role, snippet context
- [ ] 4.5 Implement `show` subcommand — partial UUID resolution (query `WHERE uuid LIKE ?%`, error if ambiguous), fetch `raw_jsonl` + `vault_files` entries matching `subagents/*.jsonl`, parse and render Human/Assistant format with subagent content inline (dimmed, prefixed), pipe through `$PAGER`
- [ ] 4.6 Implement `restore` subcommand — partial UUID, choose location (prompt if multiple, use `--output` to override). Write `raw_jsonl` to `~/.claude/projects/<claude_project_dir>/` (or `--output` path), restore all `vault_files` entries recreating directory structure. **Path safety:** validate each `relative_path` (reject absolute, reject `..`, containment check). Prompt before overwriting existing files
- [ ] 4.7 Implement `resume` subcommand — pre-check `exec.LookPath("claude")`, close vault store, restore + launch via `os/exec.Command("claude", "--resume", sessionID)` with inherited stdio. Return exit code through cobra (no `os.Exit`). If `project_path` starts with `/` (real path from hook import), cd there first. If mangled (starts with `-`), warn and prompt for directory
- [ ] 4.8 Implement `stats` subcommand — query session count, total size, per-project breakdown, oldest/newest dates
- [ ] 4.9 Verify: run each command manually against a vault with imported test sessions. `import` → `list` shows sessions → `search <term>` finds expected result with `--role` filtering working → `show <id>` displays content including subagent conversations → `restore <id>` recreates files with subagents and tool-results (path safety validated) → `stats` shows correct counts

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

## Dependency Graph

```
Task 0 (investigate) ─── optional, does not block shipping

Task 1 (store) ──────┐
                      ├──→ Task 3 (import) ──→ Task 4 (CLI) ──→ Task 6 (TUI) ──→ Task 7
Task 2 (scanner) ────┘          │
                                └──→ Task 5 (server sweep) ─────────────────────→ Task 7
```
