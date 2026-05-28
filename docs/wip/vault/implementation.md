# Vault — Implementation Plan

> **Design:** [./design.md](./design.md)
> **Created:** 2026-05-28

## Package Structure

```
internal/sqliteutil/ — shared SQLite open/recovery: canary query + corrupt/wrong-passphrase/
                       unencrypted classification + backup (used by both store and vault)
internal/vault/
  store.go         — VaultStore: SQLite connection lifecycle, schema, CRUD operations
  encryption.go    — RequireVaultKey(), VaultDBPath() (reads CAPY_VAULT_KEY / CAPY_VAULT_PATH)
  migrations.go    — vault_migrations table + migrateVault() (see Migration Strategy)
  scanner.go       — Single-pass JSONL scanner for FTS extraction
  scanner_types.go — Minimal JSON wire types for JSONL parsing (~20 lines)
  discovery.go     — Session file discovery; resolves the projects dir via config.ClaudeProjectsDir() (no local helper)
  import.go        — Import orchestration: discovery → scan → idempotent upsert
  machine.go       — Machine identity resolution (env var → file → generate)
  tui/
    app.go         — Root bubbletea application, composes sub-models
    list.go        — Session list model (left panel)
    viewer.go      — Session content viewer model (right panel)
    search.go      — Search input + results model
    render.go      — Faithful raw_jsonl → display renderer (lipgloss; separate from the scanner)
    styles.go      — lipgloss style definitions
cmd/capy/
  vault.go         — cobra subcommand group + individual commands
```

## Encryption & DB Initialization

### Vault Key

Vault uses `CAPY_VAULT_KEY` (not `CAPY_DB_KEY`) for encryption. `internal/vault/encryption.go` provides a `RequireVaultKey()` function that reads from this env var. It reuses `store.EncryptedDSN()`, `store.URIEscapePassphrase()`, and `store.URIEscapePath()` directly — these are already exported and take the key as a parameter.

**Prerequisite:** the corruption/passphrase recovery path in `internal/store/` must be shared before implementing the vault store. **Extract it into `internal/sqliteutil/` — this is the primary plan, not an alternative.** Exporting the four predicates alone (`IsSQLiteCorruption`, `BackupCorruptDB`, `IsWrongPassphrase`, `IsGarbageFile`) is insufficient: `IsWrongPassphrase` only matches `*errWrongPassphrase`, and that type — plus `*errUnencryptedDB` and the `isUnencryptedDB` canary — is constructed *inside* `store.openDB()` and unexported. A vault `openDB()` building its own errors would never satisfy the exported predicate, and it cannot construct `store`'s unexported types. Move the canary query, the corrupt / wrong-passphrase / unencrypted classification, the error types, and `backupCorruptDB` into `sqliteutil`, and have both `store` and `vault` call it.

### DB Path Resolution

1. `CAPY_VAULT_PATH` env var → if set, use it
2. Default: `$XDG_DATA_HOME/capy/vault.db` (typically `~/.local/share/capy/vault.db`), consistent with capy's knowledge store XDG convention

The directory is created on first use (`os.MkdirAll`).

### Connection Lifecycle

`VaultStore` follows the same lazy-init pattern as `ContentStore` — the DB is opened on first operation via `getDB()`. On open:

1. Read `CAPY_VAULT_KEY`, build encrypted DSN
2. Open connection, set WAL mode + pragmas (`journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, `foreign_keys=ON`)
3. Run canary query to verify passphrase
4. Execute schema DDL (all CREATE TABLE IF NOT EXISTS / CREATE VIRTUAL TABLE IF NOT EXISTS)
5. Apply migrations (if any)
6. Prepare statements

On close: close prepared statements, close connection pool, run WAL checkpoint (same pattern as `ContentStore.Close()`).

### Migration Strategy

Schema evolution follows the existing pattern from `internal/store/migrate.go`:

1. `internal/vault/migrations.go` — numbered migration functions (e.g., `migrateVault001AddTitle`)
2. `vault_meta` stores `schema_version` — checked on `openDB()`
3. Each migration is idempotent (safe to re-run on already-migrated DB)
4. Migrations use `beginImmediate` for write-lock acquisition under concurrency (same as `store/migrate.go`)
5. A `vault_migrations` table tracks applied migrations by name (same pattern as `store/migrate.go:ensureMigrationsTable`)

### Schema DDL

All tables from design.md are created in a single `schemaSQL` constant: `vault_sessions` (including `title TEXT` column), `vault_session_locations`, `vault_files`, `vault_fts`, `vault_meta`, plus indexes (`idx_sessions_end_time`, `idx_locations_project`, `idx_locations_session`). The FTS5 table uses `tokenize='porter unicode61'`. Foreign keys use `ON DELETE CASCADE` (for `vault_files` and `vault_session_locations`). FTS5 virtual tables don't support foreign keys — FTS cleanup is explicit (`DELETE FROM vault_fts WHERE session_uuid = ?` in the same transaction). Session replacement uses `UPDATE` (not DELETE+INSERT) to preserve existing `vault_session_locations` — see design.md for details. The `vault_meta` table is seeded with a schema version on first creation.

## Machine Identity

`internal/vault/machine.go` resolves the stable machine identifier:

1. Check `CAPY_MACHINE_ID` env var — if set and non-empty, return it
2. Check `~/.config/capy/machine-id` file — if exists and contains valid UUID, return it
3. Generate UUIDv4, write atomically to `~/.config/capy/machine-id` (write to temp file + rename to avoid race conditions), return it

The machine ID is resolved once per process invocation and cached in memory.

## Scanner

### JSON Wire Types

`internal/vault/scanner_types.go` defines minimal structs for JSONL deserialization. These are intentionally decoupled from `internal/session/parse.go` to avoid inheriting operational parsing logic (type normalization, filtering decisions).

Required fields only: `type`, `subtype`, `uuid`, `timestamp`, `sessionId`, `cwd`, `gitBranch`, `aiTitle`, `message` (raw JSON), and within messages: `id`, `role`, `content` (raw JSON), and within content blocks: `type`, `text`, `name`, `id`, `input` (raw JSON), `tool_use_id`, `content` (for tool_result). No `customTitle` field — that title tier is deferred (absent from JSONL); add it only if the rename store is later found to live in the session file. Also parse `prUrl`/`prRepository`/`prNumber` for `pr-link` lines.

### Scan Algorithm

`internal/vault/scanner.go` implements `ScanSession(r io.Reader) (*ScanOutput, error)`:

The scanner accepts `io.Reader` (not a file path) so it works for both import-from-disk (`os.Open` → reader) and render-from-BLOB (`bytes.NewReader` → reader) scenarios.

```
ScanOutput {
    Results      []ScanResult
    Title        string    // last ai-title; else guarded first-user-message fallback (custom-title tier deferred)
    CWD          string    // from first user entry with cwd
    Branch       string    // from first user entry with gitBranch
    StartTime    time.Time // timestamp of first JSONL line that has one (ai-title lines have none)
    EndTime      time.Time // timestamp of last JSONL line that has one
    MessageCount int       // human-text user turns + assistant turns; excludes tool_result-only user entries
}
```

1. Create buffered scanner (16MB line buffer, matching `parse.go`). A single line exceeding 16MB (e.g. an inline base64 image) overflows `bufio.Scanner` — log and skip it for FTS extraction; `raw_jsonl` still preserves the line verbatim, so `restore`/`show` are unaffected.
2. Initialize assistant block accumulator: `assistantBlocks := map[string][]contentBlock{}` keyed by `message.id`
3. Stream line-by-line, tracking the 0-based `LineIndex` of each line:
   - Unmarshal top-level JSON to get `type`, `timestamp`, `message`
   - Handle by type (see design.md JSONL Line Types table; **unknown/unlisted types → skip**):
     - `user`: a user entry holds **either** human text **or** `tool_result` blocks (≈86% are `tool_result`-only). Extract human `text`/string content (strip `<system-reminder>`) → `Role="user"`; extract bounded `tool_result` text (see Tool Result Extraction) → a **separate** `ScanResult` with `Role="tool"`, so `--role user` stays human-only. Capture `cwd`/`gitBranch` from the first user entry that has them
     - `assistant`: accumulate content blocks per `message.id`. Deduplicate by `(Type, Text, Name, ID)` tuple — matching the proven approach in `internal/session/parse.go:161-174`. Extract `text` and `tool_use` name/input summaries → `Role="assistant"`. Skip `thinking` blocks. Assistant entries never carry `tool_result`
     - `ai-title`: capture `aiTitle` as session title — **keep the last one seen** (titles are emitted progressively)
     - `pr-link`: extract `prUrl`/`prRepository`/`prNumber` → one `ScanResult` (`Role="system"`)
     - `system` (subtype `away_summary`): extract content text → `Role="system"`
     - `attachment`: extract attachment filename from content blocks
     - All other types (`custom-title`, `permission-mode`, `file-history-snapshot`, `last-prompt`, `queue-operation`, `system:turn_duration`, `system:local_command`, `system:informational`, `agent-name`, `progress`, and anything unknown): skip. `custom-title`/`progress` are absent from sampled data, so the skip-by-default rule covers them
4. Produce one `ScanResult` per message (NOT per turn-pair): human user text → `Role="user"`, each `tool_result` → `Role="tool"`, each assistant message → `Role="assistant"`, away-summary/pr-link → `Role="system"`. Set `LineIndex` to the originating JSONL line (for deduped assistant snapshots, the first/canonical line). Track `TurnIndex` (increments on user→assistant boundary) and `MessageIndex` (sequential within a turn) for ordering only — `LineIndex` is the anchor the TUI viewer uses for jump-to-match.
5. Sanitize: run `sanitize.StripSecrets()` on each `ScanResult.ContentText`
6. Return `*ScanOutput` with results, title, cwd, and branch

### Tool Input Extraction

For tool_use blocks, the scanner extracts searchable summaries from the `input` field:
- **Read/Edit/Write**: extract `file_path` value
- **Bash**: extract `command` value
- **Agent**: extract `prompt` value (first 200 chars)
- **Other tools**: extract tool name only (no input parsing)

For `tool_result` blocks (which appear in **`user`** entries, not assistant): extract text content, capped at 16KB with **75% head + 25% tail** char-boundary truncation (matching `claude-history`'s `truncate_for_search`, `claude.rs:147,222`), emitted as a `Role="tool"` `ScanResult`. Skip image/binary content blocks. Tool results contain searchable content (error messages, grep output, build logs) users commonly search for. Note: `tool_result` references `tool_use` by `tool_use_id`, not name — tool names are already captured from the assistant-side `tool_use` blocks, so no correlation is needed.

## Discovery

### Shared Claude Config Helper

Discovery, restore, and resume resolve the Claude projects directory via the **existing** `config.ClaudeProjectsDir()` (`internal/config/paths.go`), **extended to honor `CLAUDE_CONFIG_DIR`** (it currently hardcodes `~/.claude/projects/`). Do **not** add a second helper in `internal/vault/`:

1. Check `CLAUDE_CONFIG_DIR` env var → if set, use `$CLAUDE_CONFIG_DIR/projects/`
2. Default: `~/.claude/projects/`

The same change makes `internal/session/sweep.go:SessionDir()` honor `CLAUDE_CONFIG_DIR` (it shares this path), closing the asymmetry noted in design.md. For `project_path` recovery from a mangled directory name, reuse `config.unmanglePath`/`unmangledProbe` rather than storing the raw mangled name.

### Walking Session Directories

`DiscoverSessions(rootDir string) ([]SessionFile, error)`:

1. If `rootDir` is empty, resolve via `ClaudeProjectsDir()`
2. Auto-detect input type:
   - If `rootDir` contains a `projects/` subdirectory → treat as Claude config dir, walk `projects/`
   - If `rootDir` contains mangled-path subdirectories with `*.jsonl` files → treat as `projects/` root
   - If `rootDir` directly contains `*.jsonl` files → treat as a single project directory
   - Otherwise → error with a descriptive message
3. For each project directory, list `*.jsonl` files
4. For each JSONL file, check for `<uuid>/` directory and collect all files within it recursively (subagents, tool-results, any other sidecars). Skip **non-subagent** files > 5 MB; `subagents/*.jsonl` and the main JSONL are never skipped (see DB Size Projection in design.md)
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
3. For each session file (or batch of ~50 sessions / ~100MB, whichever first):
   a. Read raw bytes from main JSONL
   b. Read all associated files from the session directory (skip **non-subagent** files > 5 MB; never skip `subagents/*.jsonl` or the main JSONL)
   c. Compute composite SHA-256 content hash with framing: for each file (main JSONL keyed `<uuid>.jsonl`, associated files keyed by relative path, all sorted by key), hash `len(key) || key || len(content) || content`. Also compute `size_bytes` = sum of all hashed content lengths (the replace tiebreaker covers main + sidecars, not main alone)
   d. Check idempotent import logic (skip/replace/insert decision), comparing the total `size_bytes` from step (c)
   e. If inserting/replacing:
      - Run scanner (`ScanSession(bytes.NewReader(rawJSONL))`) to produce `*ScanOutput` — extracts FTS results, title, cwd, gitBranch. Also scan subagent files identified by `subagents/agent-*.jsonl` path pattern
      - Begin transaction
      - If replacing: `UPDATE vault_sessions SET ...` (preserves locations), `DELETE FROM vault_files WHERE session_uuid = ?`, `DELETE FROM vault_fts WHERE session_uuid = ?`
      - If inserting: `INSERT INTO vault_sessions ...`
      - Upsert `vault_session_locations` row for this (uuid, machine_id, claude_project_dir) combination, using `cwd` as `project_path` when available, then `config.unmanglePath` recovery, then the raw mangled dir name as last resort
      - Insert `vault_files` rows for all associated files
      - Insert `vault_fts` rows (one per ScanResult, with sanitized content)
      - Commit transaction
   f. If skipping (same hash): still upsert `vault_session_locations` to record this location (outside the main transaction — location-only upsert is cheap and idempotent)
4. Each batch acquires the write lock via the store's `beginImmediate` idiom (RESERVED lock + `SQLITE_BUSY` exponential backoff, per `internal/store/migrate.go`) so a concurrent server-startup sweep doesn't fail the batch outright. On batch tx error: log, retry each session individually. On individual error: log and continue
5. Report: imported N, updated N, skipped N, errors N (per-session errors printed to stderr)

### Metadata Extraction

Metadata is now extracted by the scanner (`ScanOutput`) rather than a separate first-pass, since the scanner already reads every line. The `ScanOutput` struct provides:
- `start_time` / `end_time`: first and last JSONL line timestamps that exist (ai-title lines carry no timestamp — skip them)
- `message_count`: human-text `user` turns + `assistant` turns — **excludes `tool_result`-only user entries** (≈86% of user lines)
- `size_bytes`: **total** bytes across the main JSONL + all associated files (the set `content_hash` covers), summed by the caller — not `os.Stat` of the main file alone. Doubles as the replace tiebreaker (see [Idempotent Import Logic](./design.md#idempotent-import-logic))
- `cwd`: from first user entry with `cwd` field (used as `project_path`; `config.unmanglePath` recovery when absent, raw mangled name last)
- `gitBranch`: from first user entry with `gitBranch` field (present on user-type lines — no `git rev-parse` needed). NULL if absent
- `title`: **last** `ai-title` value; fallback to the first *significant* user message (string content, not a `tool_result` array, not `<…>`-prefixed) truncated to 120 chars. The `custom-title`/`customTitle` override is **deferred** (absent from JSONL — see design.md title rationale)

## Hook Integration

### MCP Server Startup Sweep

In `internal/server/server.go`, the existing session sweep goroutine pattern is extended. After the existing `session.Sweep()` call, a vault sweep runs for the current project:
1. Discover sessions for the current project directory only
2. Open a `VaultStore`, import any sessions not yet in the vault, then **`Close()` it** — all within the same `bgWg`-tracked goroutine
3. Uses the same cooperative cancellation (server context + timeout)

**Lifecycle:** the goroutine owns the `VaultStore` for the duration of the sweep and closes it when done. `server.shutdown()` does **not** close it (it closes only the knowledge `ContentStore`), so the sweep must close its own handle — `VaultStore.Close()` runs the WAL checkpoint (close pool → `wal_checkpoint(TRUNCATE)`), leaving `vault.db-wal` flushed. The existing `bgWg.Wait()` in `shutdown()` already ensures the goroutine (and its `Close`) finishes before the server exits.

This requires `CAPY_VAULT_KEY` to be set. If not set, the vault sweep is silently skipped (vault is opt-in). A concurrent CLI `capy vault import` and this sweep both write `vault.db` — they contend on the WAL and rely on `busy_timeout` + `beginImmediate` retry (see [Import Pipeline](#orchestration)).

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

- Default: mutating (actually imports)
- `--dry-run`: preview what would be imported without writing
- `--path <dir>`: custom source directory
- `--project <filter>`: only import sessions matching project path substring
- Output: table with UUID, project, size, status (new/updated/skipped/error)
- Verify: `capy vault stats` shows updated counts → `capy vault list` shows imported sessions

### Search Command

- Positional arg: search query — plain keyword mode by default (each whitespace-separated token is auto-quoted to prevent FTS5 operator interpretation, matching `claude-vault`'s approach). `--raw` flag passes the query as raw FTS5 MATCH syntax for advanced users
- `--project`: substring filter on `vault_session_locations.project_path` (via `EXISTS` subquery to avoid row multiplication from multi-location sessions)
- `--after`, `--before`: timestamp filters on `vault_sessions`
- `--role <user|assistant|tool|system>`: filter FTS results by the `role` UNINDEXED column (`tool` = `tool_result` output, kept separate from human `user` text so `--role user` returns prompts, not tool output)
- `--limit N`: max results (default 20)
- `--json`: machine-readable output for scripting
- Output: ranked results with short UUID, project, date, role, snippet (with `snippet()` highlights)
- Verify: search for known content → correct session appears with relevant snippet

### List Command

- `--project <filter>`: substring match on `vault_session_locations.project_path` (via `EXISTS` subquery)
- `--limit N`: max results (default 50)
- `--json`: machine-readable output
- Output: table with short UUID, title, project, date range, messages, size
- Default sort: `ORDER BY end_time DESC` (uses `idx_sessions_end_time` index)
- Multi-location dedup: `GROUP BY vault_sessions.uuid`, display most recent location's `project_path`
- Verify: `capy vault list` shows sessions in reverse chronological order with titles

### Show Command

- Positional arg: session UUID (partial match supported, 8+ chars). On ambiguous match, show candidates with date, project, and title
- `--format <text|markdown|json>`: output format (default: text with pager). `markdown` and `json` produce clean export without pager
- Fetches `raw_jsonl` from `vault_sessions`, parses on the fly
- Also fetches `vault_files` entries matching `subagents/*.jsonl` and parses them inline with visual distinction (dimmed, prefixed — matching `claude-history`'s subagent rendering approach). Non-JSONL vault_files (tool-results, sidecars) are archive-only and not rendered
- Renders Human/Assistant format with tool usage indicators
- Pipes through `$PAGER` (or `less` fallback) for long sessions (text format only)
- Verify: `capy vault show <id>` displays full conversation including subagent content

### Restore Command

- Positional arg: session UUID (partial match, 8+ chars)
- `--output <path>`: custom output directory. If omitted and session has multiple locations, prompts the user to choose. Default: `ClaudeProjectsDir()/<claude_project_dir>/` from the chosen location (respects `CLAUDE_CONFIG_DIR`)
- Writes `<uuid>.jsonl` from `vault_sessions.raw_jsonl`
- Writes `<uuid>/<relative_path>` for each entry in `vault_files`
- **Path safety:** resolve the restore root with `filepath.EvalSymlinks` first (so a symlinked component can't redirect writes outside it); then for each `vault_files` entry validate `relative_path`: reject absolute paths, reject `..` components, verify the resolved path is under the resolved restore root via `filepath.Rel()` containment check. Skip invalid entries with a warning to stderr
- Prompts before overwriting existing files
- Verify: restored files match originals → `diff` shows no difference (if originals still exist)

### Resume Command

- Positional arg: session UUID (partial match, 8+ chars)
- `--dir <path>`: explicit directory override (skips fallback chain)
- Calls restore logic to put files back in original Claude Code location
- Pre-check: `exec.LookPath("claude")` — clear error message if not found
- Close the vault store before launching to ensure WAL checkpoint and proper cleanup
- Directory resolution fallback chain: (1) `--dir` flag if provided and valid, (2) `project_path` if it starts with `/` and exists as a directory on this machine, (3) current working directory, (4) prompt with `project_path` as hint. For multi-location sessions, prefer location matching current `machine_id`
- Launches `claude --resume <session_id>` using `os/exec.Command` with inherited stdin/stdout/stderr. Returns the exit code through cobra's error handling (no `os.Exit` — allows deferred cleanup to run)
- Verify: `capy vault resume <id>` opens Claude Code with the session loaded

### Delete Command

- Positional arg: session UUID (partial match, 8+ chars)
- `--yes`: skip confirmation prompt
- Shows session info (title, project, date, message count) and prompts for confirmation
- Transactional: `DELETE FROM vault_fts WHERE session_uuid = ?` then `DELETE FROM vault_sessions WHERE uuid = ?` (CASCADE handles files + locations)
- Verify: deleted session no longer appears in `list` or `search`

### Stats Command

- No args
- `--json`: machine-readable output
- Output: total sessions, total size (DB file), per-project session counts, oldest/newest session dates
- Verify: numbers match `capy vault list | wc -l` and actual DB file size

### Checkpoint Command

- No args
- Runs WAL checkpoint (same as server shutdown path)
- Required before copying vault.db to another machine (WAL mode means recent writes may be in `vault.db-wal`)
- Verify: `vault.db-wal` is empty or absent after checkpoint

## TUI Implementation

### Dependencies

Add to `go.mod`:
- `github.com/charmbracelet/bubbletea` — TUI framework
- `github.com/charmbracelet/bubbles` — list, viewport, textinput components
- `github.com/charmbracelet/lipgloss` — terminal styling

**Glamour is excluded from v1** (see [design.md TUI Dependencies](./design.md#dependencies)) — it transitively pulls in `chroma`/`goldmark` + lexers, conflicting with capy's lean 5-module dependency tree. v1 renders with lipgloss only; rich markdown/syntax highlighting via glamour is a deferred Future Improvement, ideally behind a build tag. **(Reversible decision — flagged for confirmation.)**

### Application Structure

`internal/vault/tui/app.go` defines the root `Model` that composes sub-models:
- `listModel` — session list with filtering
- `viewerModel` — session content viewer
- `searchModel` — search input and results

The root model manages mode transitions (browse ↔ search ↔ view) and delegates `Update`/`View` to the active sub-model.

### List Model

Wraps `bubbles/list` with custom item rendering (short UUID, project, date, size). Delegates to `VaultStore.ListSessions()` for data. Supports fuzzy filtering via the built-in list filter.

### Viewer Model

Wraps `bubbles/viewport` for scrollable content. On activation, fetches `raw_jsonl` from store plus `vault_files` entries matching `subagents/*.jsonl`. Uses lazy line-indexing: holds the raw `[]byte` plus a `\n`-offset index, and only `json.Unmarshal`s lines in the visible viewport on demand (not the FTS scanner — this needs faithful rendering including tool results, thinking blocks, and subagent conversations based on flags). When entered from a search result, it scrolls to the match by resolving the FTS `line_index` directly against the `\n`-offset table (**not** `turn_index`, which is scanner-internal and unreliable across the two parsers). Subagent content is rendered inline with visual distinction (dimmed prefix). Supports `--show-tools` and `--show-thinking` flags.

### Search Model

Combines `bubbles/textinput` for query entry with a results list. Debounces input (200ms) before firing FTS5 queries. Results include `snippet()` context and turn metadata. Selecting a result transitions to the viewer scrolled to the match's `line_index` (resolved against the `\n`-offset table).

### Session Content Renderer

`internal/vault/tui/render.go` parses `raw_jsonl` into displayable content. This is a separate renderer from the scanner — it aims for faithful visual representation, not search extraction. Uses lipgloss for role-based coloring; rich markdown/syntax rendering via glamour is deferred from v1 (see [Dependencies](#dependencies)).

## Assumptions

1. **Claude Code JSONL format is stable** — wire types won't break incompatibly. Raw BLOBs remain restorable regardless.
2. **PreCompact hook fires before file mutation** — UNVERIFIED. Must be validated before implementing PreCompact archival. Core vault functionality does not depend on this.
3. **Session UUIDs are globally unique** — cross-machine merge depends on this.
4. **Claude Code session directory structure is stable** — mangled-path convention persists. Discovery also respects `CLAUDE_CONFIG_DIR`.
5. **JSONL field/line-type shape verified against a 223-session sample** — see [design.md Assumptions #5](./design.md#assumptions). Facts the scanner relies on: `tool_result` lives in `user` entries (0 in assistant); `ai-title` present in 136/223 (progressive, last wins); `custom-title`/`customTitle` and `progress` absent (0); `cwd`/`gitBranch` on user lines. Unknown types are skipped by default, so format drift is non-fatal.

## Not Doing

- **Cloud sync** — local-only; cross-machine is manual file copy
- **Multi-user access** — single-user, no auth
- **Codex session support** — different format; future work
- **Session diffing** — no cross-compaction comparison
- **Real-time watch mode** — no filesystem watcher
- **Automatic cleanup/retention** — vault archives forever
- **Sharing/export with redaction** — separate feature requiring own design
- **PreCompact snapshot archival** — deferred until hook payload is verified (see Future Improvements in design.md)
- **TUI markdown rendering (glamour)** — v1 is lipgloss-only; glamour deferred to keep the binary lean (see Future Improvements in design.md). Reversible decision
- **Vault key rotation (`capy vault rekey`)** — no rekey path in v1; deferred (see Future Improvements in design.md)
