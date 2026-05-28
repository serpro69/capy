# Vault — Implementation Plan

> **Design:** [./design.md](./design.md)
> **Created:** 2026-05-28

## Package Structure

```
internal/vault/
  store.go         — VaultStore: SQLite connection lifecycle, schema, CRUD operations
  encryption.go    — RequireVaultKey(), vault-specific key handling (reads CAPY_VAULT_KEY)
  scanner.go       — Single-pass JSONL scanner for FTS extraction
  scanner_types.go — Minimal JSON wire types for JSONL parsing (~20 lines)
  discovery.go     — Session file discovery across Claude Code projects directory
  import.go        — Import orchestration: discovery → metadata → idempotent upsert
  machine.go       — Machine identity resolution (env var → file → generate)
  metadata.go      — Metadata extraction from JSONL files (timestamps, counts)
  tui/
    app.go         — Root bubbletea application, composes sub-models
    list.go        — Session list model (left panel)
    viewer.go      — Session content viewer model (right panel)
    search.go      — Search input + results model
    styles.go      — lipgloss style definitions
cmd/capy/
  vault.go         — cobra subcommand group + individual commands
```

## Encryption & DB Initialization

### Vault Key

Vault uses `CAPY_VAULT_KEY` (not `CAPY_DB_KEY`) for encryption. `internal/vault/encryption.go` provides a `RequireVaultKey()` function that reads from this env var. It reuses `store.EncryptedDSN()`, `store.URIEscapePassphrase()`, and `store.URIEscapePath()` directly — these are already exported and take the key as a parameter.

### DB Path Resolution

1. `CAPY_VAULT_PATH` env var → if set, use it
2. Default: `~/.config/capy/vault.db`

The `~/.config/capy/` directory is created on first use (`os.MkdirAll`).

### Connection Lifecycle

`VaultStore` follows the same lazy-init pattern as `ContentStore` — the DB is opened on first operation via `getDB()`. On open:

1. Read `CAPY_VAULT_KEY`, build encrypted DSN
2. Open connection, set WAL mode + pragmas (`journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`)
3. Run canary query to verify passphrase
4. Execute schema DDL (all CREATE TABLE IF NOT EXISTS / CREATE VIRTUAL TABLE IF NOT EXISTS)
5. Apply migrations (if any)
6. Prepare statements

On close: close prepared statements, close connection pool, run WAL checkpoint (same pattern as `ContentStore.Close()`).

### Schema DDL

All tables from design.md are created in a single `schemaSQL` constant: `vault_sessions`, `vault_files`, `vault_fts`, `vault_meta`. The FTS5 table uses `tokenize='porter unicode61'`. Foreign keys use `ON DELETE CASCADE` (for `vault_files`). FTS5 virtual tables don't support foreign keys — FTS cleanup is explicit (`DELETE FROM vault_fts WHERE session_uuid = ?` in the same transaction). The `vault_meta` table is seeded with a schema version on first creation.

## Machine Identity

`internal/vault/machine.go` resolves the stable machine identifier:

1. Check `CAPY_MACHINE_ID` env var — if set and non-empty, return it
2. Check `~/.config/capy/machine-id` file — if exists and contains valid UUID, return it
3. Generate UUIDv4, write atomically to `~/.config/capy/machine-id` (write to temp file + rename to avoid race conditions), return it

The machine ID is resolved once per process invocation and cached in memory.

## Scanner

### JSON Wire Types

`internal/vault/scanner_types.go` defines minimal structs for JSONL deserialization. These are intentionally decoupled from `internal/session/parse.go` to avoid inheriting operational parsing logic (type normalization, filtering decisions).

Required fields only: `type`, `subtype`, `uuid`, `timestamp`, `sessionId`, `message` (raw JSON), and within messages: `id`, `role`, `content` (raw JSON), and within content blocks: `type`, `text`, `name`, `input` (raw JSON).

### Scan Algorithm

`internal/vault/scanner.go` implements `ScanSession(path string) ([]ScanResult, error)`:

1. Open file, create buffered scanner (16MB line buffer, matching `parse.go`)
2. Initialize assistant block accumulator: `assistantBlocks := map[string][]contentBlock{}` keyed by `message.id`
3. Stream line-by-line:
   - Unmarshal top-level JSON to get `type`, `timestamp`, `message`
   - Skip non-user/assistant/system types
   - For user messages: extract text from content blocks, strip `<system-reminder>` tags
   - For assistant messages: accumulate content blocks per `message.id`. Deduplicate by `(Type, Text, Name, ID)` tuple — matching the proven approach in `internal/session/parse.go:161-174`. This correctly handles Claude Code's non-cumulative progressive snapshots where each JSONL line carries one content block sharing the same `message.id`.
   - For system/away_summary: extract content text
4. Group extracted content into turns (sequential user→assistant pairs)
5. Sanitize: run `sanitize.StripSecrets()` on each `ScanResult.ContentText`
6. Return `[]ScanResult` with one entry per turn

### Tool Input Extraction

For tool_use blocks, the scanner extracts searchable summaries from the `input` field:
- **Read/Edit/Write**: extract `file_path` value
- **Bash**: extract `command` value
- **Agent**: extract `prompt` value (first 200 chars)
- **Other tools**: extract tool name only (no input parsing)

This keeps the scanner simple while covering the most useful search scenarios.

## Discovery

### Claude Config Directory Resolution

`internal/vault/discovery.go` resolves the Claude Code base directory:

1. Check `CLAUDE_CONFIG_DIR` env var — if set, use `$CLAUDE_CONFIG_DIR/projects/`
2. Default: `~/.claude/projects/`

### Walking Session Directories

`DiscoverSessions(rootDir string) ([]SessionFile, error)`:

1. If `rootDir` is empty, resolve via Claude config directory logic above
2. List all entries in root directory (each is a mangled project path)
3. For each project directory, list `*.jsonl` files
4. For each JSONL file, check for `<uuid>/` directory and collect all files within it recursively (subagents, tool-results, any other sidecars)
5. Return `[]SessionFile` with: path, UUID, project directory name (mangled), list of associated files

### SessionFile Type

```
SessionFile {
    Path           string            // full path to main .jsonl
    UUID           string            // extracted from filename
    ProjectDir     string            // mangled directory name
    AssociatedFiles []AssociatedFile  // all files in <uuid>/ directory
}

AssociatedFile {
    AbsPath      string  // full path on disk
    RelativePath string  // relative to <uuid>/ (e.g., "subagents/agent-abc.jsonl")
}
```

## Import Pipeline

### Orchestration

`internal/vault/import.go` implements the import flow:

1. Call `DiscoverSessions()` to find all session files
2. Open `VaultStore`
3. For each session file (or batch of ~50 for bulk):
   a. Read raw bytes from main JSONL
   b. Read all associated files from the session directory
   c. Compute composite SHA-256 content hash (main file + all associated files, sorted by relative path)
   d. Check idempotent import logic (skip/replace/insert decision)
   e. If inserting/replacing:
      - Extract metadata (timestamps, message count) via lightweight JSONL scan
      - Run scanner to produce `[]ScanResult` for FTS (including subagent files identified by `subagents/agent-*.jsonl` path pattern)
      - Begin transaction
      - If replacing: delete old FTS rows (`DELETE FROM vault_fts WHERE session_uuid = ?`)
      - Insert/replace `vault_sessions` row (CASCADE auto-deletes old `vault_files`)
      - Insert `vault_files` rows for all associated files
      - Insert `vault_fts` rows (one per ScanResult, with sanitized content)
      - Commit transaction
4. Report: imported N, skipped N, errors N

### Metadata Extraction

`internal/vault/metadata.go` extracts session metadata without full parsing:
- `start_time`: timestamp of the first JSONL line
- `end_time`: timestamp of the last JSONL line
- `message_count`: count of lines where type is "user" or "assistant" (or message.role is)
- `size_bytes`: `os.Stat(path).Size()`

This is a fast first-pass that only reads timestamps and type fields — no content extraction.

### Git Branch Extraction

When available (hooks, server startup):
- Run `git rev-parse --abbrev-ref HEAD`
- If result is literal `"HEAD"` (detached HEAD state), store NULL instead
- If git is not available or not a git repo, store NULL

## Hook Integration

### MCP Server Startup Sweep

In `internal/server/server.go`, the existing session sweep goroutine pattern is extended. After the existing `session.Sweep()` call, a vault sweep runs for the current project:
1. Discover sessions for the current project directory only
2. Import any that aren't in the vault yet
3. Uses the same cooperative cancellation (server context + timeout)

This requires `CAPY_VAULT_KEY` to be set. If not set, the vault sweep is silently skipped (vault is opt-in).

### PreCompact Hook (Deferred)

Before implementing PreCompact archival:
1. Capture the raw PreCompact hook payload by adding a debug log to `handlePreCompact`
2. Trigger `/compact` in Claude Code and inspect the logged payload
3. Document the JSON structure in this implementation plan
4. Add `ParsePreCompact` to the adapter interface (or document how to extract session path from existing fields)
5. Verify the hook fires synchronously before file mutation

If the payload format is suitable, implement the archival path with prominent error logging to `~/.config/capy/vault-error.log` (since `capy.sh` swallows exit codes).

## CLI Commands

### Subcommand Group

`cmd/capy/vault.go` registers a `vault` subcommand on the root cobra command. Each sub-command is a separate function (`newVaultImportCmd()`, `newVaultSearchCmd()`, etc.) following the existing pattern in `cmd/capy/`.

All vault commands share: `--tui` flag (bool, default false), vault DB path resolution, vault key requirement.

### Import Command

- Default: dry-run (list what would be imported with status)
- `--force`: actually import
- `--path <dir>`: custom source directory
- `--project <filter>`: only import sessions matching project path substring
- Output: table with UUID, project, size, status (new/updated/skipped/error)
- Verify: `capy vault stats` shows updated counts → `capy vault list` shows imported sessions

### Search Command

- Positional arg: search query (FTS5 MATCH syntax)
- `--project`, `--after`, `--before`: metadata filters (WHERE clauses on JOIN)
- `--role <user|assistant>`: filter FTS results by role UNINDEXED column
- `--limit N`: max results (default 20)
- Output: ranked results with short UUID, project, date, role, snippet
- Verify: search for known content → correct session appears with relevant snippet

### List Command

- `--project <filter>`: substring match on project_path
- `--limit N`: max results (default 50)
- Output: table with short UUID, project, date range, messages, size
- Default sort: `ORDER BY end_time DESC`
- Verify: `capy vault list` shows sessions in reverse chronological order

### Show Command

- Positional arg: session UUID (partial match supported, 6+ chars)
- Fetches `raw_jsonl` from `vault_sessions`, parses on the fly
- Renders Human/Assistant format with tool usage indicators
- Pipes through `$PAGER` (or `less` fallback) for long sessions
- Verify: `capy vault show <id>` displays full conversation matching original session content

### Restore Command

- Positional arg: session UUID (partial match)
- `--output <path>`: custom output directory (default: `~/.claude/projects/<claude_project_dir>/`)
- Writes `<uuid>.jsonl` from `vault_sessions.raw_jsonl`
- Writes `<uuid>/<relative_path>` for each entry in `vault_files`
- Prompts before overwriting existing files
- Verify: restored files match originals → `diff` shows no difference (if originals still exist)

### Resume Command

- Positional arg: session UUID (partial match)
- Calls restore logic to put files back in original Claude Code location
- Launches `claude --resume <session_id>` using `os/exec.Command` with inherited stdin/stdout/stderr, then `os.Exit(cmd.ProcessState.ExitCode())`
- If `project_path` is an accurate real path (starts with `/`, not `-`), changes to that directory first
- If `project_path` is a mangled dir name, warns user and prompts for project directory
- Errors if Claude Code binary not found
- Verify: `capy vault resume <id>` opens Claude Code with the session loaded

### Stats Command

- No args
- Output: total sessions, total size (DB file), per-project session counts, oldest/newest session dates
- Verify: numbers match `capy vault list | wc -l` and actual DB file size

## TUI Implementation

### Dependencies

Add to `go.mod`:
- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/bubbles` — list, viewport, textinput components
- `github.com/charmbracelet/lipgloss` — terminal styling
- `github.com/charmbracelet/glamour` — markdown rendering

### Application Structure

`internal/vault/tui/app.go` defines the root `Model` that composes sub-models:
- `listModel` — session list with filtering
- `viewerModel` — session content viewer
- `searchModel` — search input and results

The root model manages mode transitions (browse ↔ search ↔ view) and delegates `Update`/`View` to the active sub-model.

### List Model

Wraps `bubbles/list` with custom item rendering (short UUID, project, date, size). Delegates to `VaultStore.ListSessions()` for data. Supports fuzzy filtering via the built-in list filter.

### Viewer Model

Wraps `bubbles/viewport` for scrollable content. On activation, fetches `raw_jsonl` from store, parses with a display-oriented parser (not the FTS scanner — this needs faithful rendering including tool results and thinking blocks based on flags). Supports `--show-tools` and `--show-thinking` flags.

### Search Model

Combines `bubbles/textinput` for query entry with a results list. Debounces input (200ms) before firing FTS5 queries. Results include `snippet()` context and turn metadata. Selecting a result transitions to the viewer scrolled to `turn_index`.

### Session Content Renderer

`internal/vault/tui/render.go` parses `raw_jsonl` into displayable content. This is a separate renderer from the scanner — it aims for faithful visual representation, not search extraction. Uses glamour for markdown in assistant text and lipgloss for role-based coloring.

## Assumptions

1. **Claude Code JSONL format is stable** — wire types won't break incompatibly. Raw BLOBs remain restorable regardless.
2. **PreCompact hook fires before file mutation** — UNVERIFIED. Must be validated before implementing PreCompact archival. Core vault functionality does not depend on this.
3. **Session UUIDs are globally unique** — cross-machine merge depends on this.
4. **Claude Code session directory structure is stable** — mangled-path convention persists. Discovery also respects `CLAUDE_CONFIG_DIR`.

## Not Doing

- **Cloud sync** — local-only; cross-machine is manual file copy
- **Multi-user access** — single-user, no auth
- **Codex session support** — different format; future work
- **Session diffing** — no cross-compaction comparison
- **Real-time watch mode** — no filesystem watcher
- **Automatic cleanup/retention** — vault archives forever
- **Sharing/export with redaction** — separate feature requiring own design
- **PreCompact snapshot archival** — deferred until hook payload is verified (see Future Improvements in design.md)
