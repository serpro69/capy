# Tasks: Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-05-28
> Not Doing: Cloud sync, multi-user access, Codex sessions, session diffing, real-time watch, automatic cleanup

## Task 1: Vault store foundation — schema, encryption, connection lifecycle
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [implementation.md#encryption--db-initialization](./implementation.md#encryption--db-initialization)

### Subtasks
- [ ] 1.1 Create `internal/vault/encryption.go` — `RequireVaultKey()` reads `CAPY_VAULT_KEY`, `VaultDBPath()` resolves `CAPY_VAULT_PATH` with `~/.config/capy/vault.db` default
- [ ] 1.2 Create `internal/vault/store.go` — `VaultStore` struct with lazy-init `getDB()`, `openDB()` using `store.EncryptedDSN()`, schema DDL execution, WAL+pragma setup, `Close()` with WAL checkpoint
- [ ] 1.3 Define schema DDL constant — all 5 tables (`vault_sessions`, `vault_subagents`, `vault_snapshots`, `vault_fts`, `vault_meta`) with correct constraints, FKs, and UNINDEXED FTS5 columns
- [ ] 1.4 Create `internal/vault/machine.go` — machine identity resolution: `CAPY_MACHINE_ID` env → `~/.config/capy/machine-id` file → generate UUIDv4 with atomic write
- [ ] 1.5 Implement prepared statements for core operations: insert/replace session, insert subagent, insert FTS row, delete FTS rows by session_uuid, insert snapshot, query session by UUID
- [ ] 1.6 Tests: DB creation with encryption, schema validation, machine ID generation and persistence, prepared statement execution against empty DB
- [ ] 1.7 Verify: `go test -tags fts5 ./internal/vault/...` passes with `CAPY_VAULT_KEY=test-key`

## Task 2: FTS scanner — JSONL text extraction for search indexing
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Docs:** [implementation.md#scanner](./implementation.md#scanner)

### Subtasks
- [ ] 2.1 Create `internal/vault/scanner_types.go` — minimal JSON wire types: `jsonlLine`, `jsonlMessage`, `contentBlock` (decoupled from `internal/session/parse.go`)
- [ ] 2.2 Create `internal/vault/scanner.go` — `ScanSession(path string) ([]ScanResult, error)`: single-pass streaming JSONL reader with 16MB line buffer, progressive snapshot dedup via `seen` map, system-reminder stripping
- [ ] 2.3 Implement text extraction per content block type: text blocks (keep), tool_use (extract name + input summary for Read/Edit/Bash/Agent), thinking (skip), tool_result (skip content but note tool name)
- [ ] 2.4 Implement turn grouping: sequential user→assistant messages grouped into turns, each producing one `ScanResult` with `TurnIndex`, `Role`, `ContentText`
- [ ] 2.5 Implement subagent scanning: `ScanSubagents(dir string) ([]ScanResult, error)` that discovers `agent-*.jsonl` files, reads `.meta.json`, and produces `ScanResult` entries with populated `SubagentID`
- [ ] 2.6 Tests: scan a real session JSONL fixture → verify extracted text contains expected content, tool names present, base64/system-reminders absent, progressive snapshots deduplicated, subagent content extracted
- [ ] 2.7 Verify: `go test -tags fts5 ./internal/vault/...` passes; scanner produces non-empty results for test fixtures

## Task 3: Session discovery and import pipeline end-to-end
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#discovery](./implementation.md#discovery), [implementation.md#import-pipeline](./implementation.md#import-pipeline)

### Subtasks
- [ ] 3.1 Create `internal/vault/discovery.go` — `DiscoverSessions(rootDir string) ([]SessionFile, error)`: walk `~/.claude/projects/*/`, find `*.jsonl` + subagent directories, return `[]SessionFile` with paths, UUID, mangled project dir
- [ ] 3.2 Create `internal/vault/metadata.go` — `ExtractMetadata(path string) (SessionMeta, error)`: fast first-pass reading only timestamps, type fields, and message count from JSONL without full content extraction
- [ ] 3.3 Create `internal/vault/import.go` — `Import(store *VaultStore, sessions []SessionFile, opts ImportOptions) ImportResult`: orchestrate discovery → metadata → idempotent upsert with batch transactions (~50 per tx for bulk, per-session fallback on failure)
- [ ] 3.4 Implement idempotent import logic: compute SHA-256 hash, check existing by UUID, apply size-based merge (larger wins), transactional insert/replace (delete old FTS → insert session + subagents + FTS rows)
- [ ] 3.5 Implement `ArchiveFromHook(payload HookPayload) error` entry point: single-session import with `os.Getwd()` for project path, `git rev-parse` for branch, writes to both `vault_snapshots` and `vault_sessions`
- [ ] 3.6 Tests: import from a test directory with multiple session files → verify correct sessions in DB, idempotent re-import skips, larger file replaces smaller, smaller file skipped, subagents stored, FTS rows created, snapshots created for hook path
- [ ] 3.7 Verify: `go test -tags fts5 ./internal/vault/...` passes; import a test fixture directory and query back sessions via store methods

## Task 4: CLI commands — import, list, search, show, restore, resume, stats
- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#cli-commands](./implementation.md#cli-commands)

### Subtasks
- [ ] 4.1 Create `cmd/capy/vault.go` — `newVaultCmd()` cobra command group, register on root command in `main.go`. Shared flag: `--tui` (bool, default false). Shared pre-run: resolve vault DB path, verify `CAPY_VAULT_KEY`
- [ ] 4.2 Implement `import` subcommand — dry-run default with table output (UUID, project, size, status), `--force` to execute, `--path` for custom dir, `--project` filter. Calls `vault.Import()`
- [ ] 4.3 Implement `list` subcommand — `ListSessions()` store method with `--project` filter, `--limit`, reverse chronological sort. Table output: short UUID, project, date range, messages, size
- [ ] 4.4 Implement `search` subcommand — FTS5 MATCH query with `snippet()`, joined to `vault_sessions` for metadata. `--project`, `--after`, `--before`, `--role` filters, `--limit`. Output: ranked results with short UUID, project, date, role, snippet context
- [ ] 4.5 Implement `show` subcommand — partial UUID resolution (query `WHERE uuid LIKE ?%`, error if ambiguous), fetch `raw_jsonl`, parse and render Human/Assistant format, pipe through `$PAGER`
- [ ] 4.6 Implement `restore` subcommand — partial UUID, write `raw_jsonl` to original location (unmangle project dir for path) or `--output` path, restore subagent files from `vault_subagents`, prompt before overwriting existing files
- [ ] 4.7 Implement `resume` subcommand — restore + `syscall.Exec("claude", ["claude", "--resume", sessionID], env)`. Error if project directory doesn't exist
- [ ] 4.8 Implement `stats` subcommand — query session count, total size, per-project breakdown, oldest/newest dates, snapshot count
- [ ] 4.9 Verify: run each command manually against a vault with imported test sessions. `import --force` → `list` shows sessions → `search <term>` finds expected result → `show <id>` displays content → `stats` shows correct counts

## Task 5: Hook integration — PreCompact archival and MCP server startup sweep
- **Status:** pending
- **Depends on:** Task 3
- **Size:** S
- **Can run in parallel with:** Task 4
- **Docs:** [implementation.md#hook-integration](./implementation.md#hook-integration)

### Subtasks
- [ ] 5.1 Extend hook router in `internal/hook/` — add `PreCompact` event case that calls `vault.ArchiveFromHook(payload)`. Graceful failure: log warning and exit non-zero, never block Claude Code
- [ ] 5.2 Extend MCP server startup in `internal/server/server.go` — after existing `session.Sweep()`, add vault sweep goroutine for current project. Skip silently if `CAPY_VAULT_KEY` not set (vault is opt-in)
- [ ] 5.3 Tests: hook handler receives mock PreCompact payload → session archived in vault_sessions and vault_snapshots. Server startup sweep indexes sessions from test project directory
- [ ] 5.4 Verify: set up hooks in a test Claude Code config, trigger PreCompact → `capy vault list` shows the archived session

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
- [ ] 6.4 Create `internal/vault/tui/viewer.go` — session content viewer wrapping `bubbles/viewport`, parses `raw_jsonl` on the fly with Human/Assistant formatting, markdown rendering via glamour, vim-style keybindings (j/k/g/G)
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
Task 1 (store) ──────┐
                      ├──→ Task 3 (import) ──→ Task 4 (CLI) ──→ Task 6 (TUI) ──→ Task 7
Task 2 (scanner) ────┘          │
                                └──→ Task 5 (hooks) ────────────────────────────→ Task 7
```
