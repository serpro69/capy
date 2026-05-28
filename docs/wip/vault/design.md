# Vault — Design Document

> **Status:** Draft
> **Created:** 2026-05-28

## Problem

Claude Code sessions are ephemeral, project-scoped, and destructible. Content is lost to compaction (`/compact` rewrites the JSONL file), auto-cleanup (30-day default retention), and accidental deletion. There is no cross-project archive, no way to search past conversations globally, and no restore mechanism once a session file is gone.

### How Might We

How might we give Claude Code power users a single, durable archive of every conversation they've ever had — across all projects — so they can search, fully view, revisit, restore and/or share past sessions, without losing data to project-scoping, auto-cleanup (30-day default retention), or accidental deletion?

A vault inverts all three properties — permanent, global, and preserving the raw JSONL verbatim. The vault is both a search index and a backup/restore system — verbatim preservation is load-bearing, not just an implementation detail.

## Target User

Any Claude Code user, even if they don't use other capy features (MCP server, context-window management). The vault should have a low-friction entry point that doesn't require the full capy setup.

## Success Criteria

A user runs `capy vault import`, and every session across all projects is archived verbatim. After Claude Code auto-cleans a 31-day-old session, the user can `show`, `restore`, `search`, or browse via TUI (`--tui`). Cross-machine: run `capy vault checkpoint` on machine A, copy `vault.db` to machine B (same `CAPY_VAULT_KEY` required), run `import` again, and sessions from both machines coexist — no duplicates, no overwrites.

**Data-loss guarantees:** Vault protects against cleanup (30-day auto-delete), accidental deletion, and project-scoping (sessions archived globally). Compaction is a known gap in v1 — if `/compact` runs before the next server startup sweep or manual `import`, the pre-compaction content is lost. PreCompact hook archival (see [Future Improvements](#future-improvements)) will close this gap in v2.

## Architecture Overview

Vault is a new `internal/vault/` package providing verbatim, cross-project session archival with full-text search. It operates a separate encrypted SQLite database (`vault.db`) independent of capy's per-project FTS knowledge store.

### Four Layers

1. **Storage layer** (`internal/vault/store.go`) — owns the SQLite connection lifecycle, schema, encryption (via `CAPY_VAULT_KEY`), and all CRUD operations. Imports `EncryptedDSN`, `URIEscapePassphrase`, and `URIEscapePath` from `internal/store/` (already exported). Corruption recovery helpers (`isSQLiteCorruption`, `backupCorruptDB`, `isWrongPassphrase`, `isGarbageFile`) are currently unexported in `internal/store/` — these must be exported (or extracted to a shared `internal/sqliteutil/` package) before vault can use them. Manages its own connection pool, WAL checkpointing, and prepared statements.

2. **Scanner layer** (`internal/vault/scanner.go`) — a single-pass JSONL reader that extracts searchable text from session files. Defines its own minimal JSON wire types (~20 lines), decoupled from `internal/session/parse.go`. Extracts: message text, tool names, tool input summaries (file paths, commands), subagent descriptions. Skips: base64, binary, system-reminder noise, progressive snapshot duplicates. Scanner output is sanitized via `sanitize.StripSecrets()` before FTS insertion to prevent secrets from appearing in search snippets.

3. **Discovery layer** (`internal/vault/discovery.go`) — walks the Claude Code session directory to find all session JSONL files and their associated session directories (subagents, tool-results, and any other sidecars) across all projects. Respects `CLAUDE_CONFIG_DIR` environment variable, falling back to `~/.claude/`. Handles metadata extraction: project path, timestamps, message count, file size, content hash.

4. **CLI/TUI layer** (`cmd/capy/vault.go` + `internal/vault/tui/`) — cobra subcommand group (`capy vault <cmd>`). Each command supports a `--tui` flag that switches to an interactive bubbletea interface.

## Storage Model

### Database Location

- Default: `$XDG_DATA_HOME/capy/vault.db` (typically `~/.local/share/capy/vault.db`), consistent with capy's knowledge store which uses `XDG_DATA_HOME` for databases. Config files belong under `~/.config/`; databases are data.
- Override: `CAPY_VAULT_PATH` environment variable
- Encryption: `CAPY_VAULT_KEY` environment variable (separate from `CAPY_DB_KEY`)

### Schema

Four tables + 1 FTS5 virtual table + 1 metadata table + 3 indexes.

**`vault_sessions`** — active state, one row per session. Holds metadata and the raw JSONL blob for verbatim restore.

```sql
CREATE TABLE vault_sessions (
    uuid              TEXT PRIMARY KEY,
    title             TEXT,
    start_time        DATETIME,
    end_time          DATETIME,
    message_count     INTEGER NOT NULL DEFAULT 0,
    size_bytes        INTEGER NOT NULL DEFAULT 0,
    content_hash      TEXT NOT NULL,
    archived_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    raw_jsonl         BLOB NOT NULL
);
```

Location-specific metadata (`project_path`, `claude_project_dir`, `git_branch`, `machine_id`) lives in `vault_session_locations` — see below.

**`vault_session_locations`** — tracks where a session has been seen. A session may exist in multiple project directories due to cross-project forking (Claude Code's `--fork-session` copies the JSONL to a different project's Claude directory). One row per unique (uuid, machine, project dir) combination.

```sql
CREATE TABLE vault_session_locations (
    session_uuid      TEXT NOT NULL REFERENCES vault_sessions(uuid) ON DELETE CASCADE,
    machine_id        TEXT NOT NULL,
    claude_project_dir TEXT NOT NULL,
    project_path      TEXT NOT NULL,
    git_branch        TEXT,
    first_seen_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_seen_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (session_uuid, machine_id, claude_project_dir)
);
```

- `project_path` — best-known real project path: accurate `os.Getwd()` from hooks, `cwd` field from JSONL during bulk import, mangled dir name as last resort. Used for display and resume (cd target).
- `claude_project_dir` — always the mangled directory name under `~/.claude/projects/`. Used for restore (writing files back to the correct Claude Code location). Always available regardless of import source.

**`vault_files`** — stores all files from the session directory (subagent JSONLs, subagent meta.json, tool-results, and any other sidecars). Linked to parent session via `ON DELETE CASCADE`. This ensures full preservation of the entire session directory — not just the main JSONL.

```sql
CREATE TABLE vault_files (
    session_uuid  TEXT NOT NULL REFERENCES vault_sessions(uuid) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    raw_content   BLOB NOT NULL,
    PRIMARY KEY (session_uuid, relative_path)
);
```

The `relative_path` preserves the directory structure relative to the session UUID directory. Examples:
- `subagents/agent-abc123.jsonl`
- `subagents/agent-abc123.meta.json`
- `tool-results/toolu_01xyz.json`

**`vault_fts`** — FTS5 virtual table with one row per message. Uses `UNINDEXED` columns for metadata that is stored and returned but not tokenized. FTS5 virtual tables do not support foreign keys or `ON DELETE CASCADE` — FTS rows must be explicitly deleted in the same transaction when a session is replaced.

```sql
CREATE VIRTUAL TABLE vault_fts USING fts5(
    content_text,
    session_uuid  UNINDEXED,
    subagent_id   UNINDEXED,
    turn_index    UNINDEXED,
    message_index UNINDEXED,
    role          UNINDEXED,
    tokenize='porter unicode61'
);
```

**Indexes:**

```sql
CREATE INDEX idx_sessions_end_time ON vault_sessions(end_time DESC);
CREATE INDEX idx_locations_project ON vault_session_locations(project_path);
CREATE INDEX idx_locations_session ON vault_session_locations(session_uuid);
```

`idx_sessions_end_time` supports the `list` command's default `ORDER BY end_time DESC`. `idx_locations_project` supports `--project` substring filtering on `list` and `search`. `idx_locations_session` supports location lookups by UUID for restore/resume.

**Multi-location deduplication:** Since `vault_session_locations` is one-to-many, queries that JOIN sessions to locations must avoid duplicate rows. `list` uses `GROUP BY vault_sessions.uuid` and picks the most recent location's `project_path` (by `last_seen_at`) for display. `search` returns FTS results per message (already session-scoped via `session_uuid`), joining to one representative location per session. `--project` filters with `EXISTS (SELECT 1 FROM vault_session_locations WHERE ... AND project_path LIKE ?)` rather than a direct JOIN to avoid row multiplication.

**`vault_meta`** — key-value store for vault-level configuration (schema version, etc.).

```sql
CREATE TABLE vault_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

### Design Rationale

- **Separate DB from FTS knowledge store** — different retention policies (vault archives forever, FTS has tiered cleanup) and different scope (vault is global, knowledge store is per-project).
- **Session content separated from locations** — Claude Code's `--fork-session` copies a session JSONL into a different project's directory, creating the same UUID in multiple project directories. `vault_sessions` stores content once (deduplicated by hash), while `vault_session_locations` tracks every (machine, project dir) where the session was seen. This avoids duplicate BLOBs and enables correct restore to any known location. `ON DELETE CASCADE` ensures locations are cleaned up with the parent session.
- **Raw JSONL as uncompressed BLOB** — application-level compression (zstd) is deferred to a future version. The current design accepts the storage cost for implementation simplicity — every read path (restore, show, TUI) would need decompression. See [Future Improvements](#future-improvements) for the compression roadmap. **Read-path performance:** `show` and TUI viewer must load the full BLOB into Go memory. For typical sessions (100KB–1MB) this is fast. For large sessions (5–10MB), the concern is not the raw `[]byte` load (single allocation, fast) but AST inflation from fully parsing JSONL into Go structs — a 10MB JSONL routinely becomes 50–100MB of heap objects with hundreds of thousands of small allocations, causing GC pressure. The mitigation is **lazy line-indexing**: load the raw `[]byte`, scan for `\n` boundaries to build an offset index (`[]struct{Start, End int}` — microseconds, near-zero allocation), then `json.Unmarshal` only the lines in the visible viewport on demand. This keeps one large `[]byte` in memory with minimal heap objects regardless of session size. No full-parse-and-cache needed — lazy line-indexing is simpler and is the v1 approach, not a deferred optimization.
- **`title` column on `vault_sessions`** — populated from `ai-title` JSONL entries (Claude's auto-generated session summaries), overridden by `custom-title` entries (user-set names), with first user message (truncated) as fallback. Essential for usable `list` output and TUI browsing — without it, sessions are indistinguishable walls of UUIDs. Both `claude-vault` and `claude-history` display session previews.
- **No `vault_messages` table** — initially considered for message-level search and TUI display, but rejected. The TUI needs to parse `raw_jsonl` on the fly anyway for faithful rendering (thinking blocks, tool results, formatting). FTS5 `UNINDEXED` columns on the `vault_fts` table provide message-level search resolution (turn_index, role) without a separate table. This eliminates double-stored text, external content triggers, and cascade complexity.
- **FTS5 one-row-per-message** — each user message and each assistant message is a separate FTS row, with the `role` UNINDEXED column distinguishing them. This enables `--role` filtering on search and produces focused `snippet()` results. Better granularity than one-row-per-session or one-row-per-turn-pair. FTS5 handles 100K+ small rows trivially.
- **`subagent_id` uses empty string sentinel** — SQLite treats NULL as never equal to NULL in composite primary keys, which would break uniqueness constraints. Empty string `''` avoids this.
- **`ON DELETE CASCADE`** on `vault_files` and `vault_session_locations` — when a session is deleted (e.g., `capy vault delete`), all associated files, locations, and FTS rows are cleaned up. Note: CASCADE applies only to regular tables; FTS5 virtual tables don't support foreign keys, so FTS rows require explicit `DELETE FROM vault_fts WHERE session_uuid = ?` in the same transaction. **Session replacement does not use CASCADE** — it uses `UPDATE` on the session row to preserve existing locations, and explicitly rebuilds `vault_files` and `vault_fts` only.
- **`vault_files` instead of `vault_subagents`** — a generic files table preserves the entire session directory (subagent JSONLs, meta.json, tool-results, and any future sidecar types), not just known file types. This is consistent with vault's promise of full preservation. Subagent metadata (agent_type, description) is read from the stored `meta.json` files at display time.
- **FTS content is sanitized** — `sanitize.StripSecrets()` runs on scanner output before FTS insertion. This prevents secrets from appearing in search snippet results. The `raw_jsonl` blob and `vault_files` content remain unsanitized — verbatim preservation is the point.
- **Snapshots deferred** — `vault_snapshots` (append-only cold storage for pre-compaction content) is deferred to a future version. The PreCompact hook payload format is unverified (see [Assumption #2](#assumptions)), and the vault works without snapshots — `import` and server-startup sweep are the primary archival paths. See [Future Improvements](#future-improvements).

## Session Discovery & Import

### Discovery

`capy vault import` discovers sessions by walking the Claude Code projects directory. The base path is resolved via a shared `ClaudeProjectsDir()` helper (used by discovery, restore, and resume):
1. `CLAUDE_CONFIG_DIR` env var → use `$CLAUDE_CONFIG_DIR/projects/`
2. Default: `~/.claude/projects/`

For each mangled project directory, it discovers:
- `*.jsonl` files (main sessions)
- `<uuid>/` directories containing all session files (subagents, tool-results, any other sidecars)

`--path <dir>` supports multiple input shapes: a full Claude config dir (containing `projects/`), a `projects/` root directly, a single mangled project directory, or a directory containing loose JSONL files. Discovery auto-detects the input type by checking for `projects/` subdirectory or `*.jsonl` files at the given path.

### Project Path Handling

Claude Code mangles project paths by replacing `/` and `.` with `-`. This is lossy (can't distinguish `/` from `.` in the original). Two columns on `vault_session_locations` track this:

- **`claude_project_dir`** — always the mangled directory name (e.g., `-home-sergio-Projects-personal-capy`). Always available from any import source. Used for restore (writing back to `~/.claude/projects/<claude_project_dir>/<uuid>.jsonl`).
- **`project_path`** — best-known real project path. From hooks: accurate `os.Getwd()`. From bulk import: the `cwd` field in the JSONL (first user message that has it), falling back to the mangled dir name. Used for display and resume.

### Metadata Extraction

| Field | Source |
|-------|--------|
| `uuid` | Filename (minus `.jsonl` extension) |
| `title` | `aiTitle` from `ai-title` JSONL entry; `customTitle` from `custom-title` entry (takes precedence); first user message text (truncated to 120 chars) as fallback |
| `project_path` | `os.Getwd()` from hooks; `cwd` field from first user JSONL line during bulk import; mangled dir name as last resort |
| `claude_project_dir` | Mangled directory name (always available) |
| `start_time` / `end_time` | First and last JSONL line timestamps |
| `message_count` | Count of user + assistant entries |
| `size_bytes` | File size on disk |
| `content_hash` | SHA-256 composite with framing: for each file (main JSONL + associated files sorted by relative path), hash `len(relativePath) || relativePath || len(content) || content`. This prevents boundary-equivalent file sets from colliding |
| `git_branch` | `gitBranch` field from the first user JSONL entry (always present on user-type lines). NULL if absent. Sessions may span branch switches — only the initial branch is recorded |
| `machine_id` | From machine identity resolution (see Cross-Machine section) |

### Idempotent Import Logic

1. Compute composite `content_hash` of the session using framed hashing (see Metadata Extraction)
2. If UUID exists with same hash → skip (already archived)
3. If UUID exists with different hash and incoming `size_bytes >= existing` → replace (session grew)
4. If UUID exists with different hash and incoming `size_bytes < existing` → skip (compacted, don't overwrite fuller archive)
5. If UUID doesn't exist → insert

**Known limitation:** Cross-machine merge after independent compaction may lose content. If machine A has a 1MB pre-compaction version and machine B has a 0.6MB independently-compacted version, importing B's vault into A would skip B's version (smaller). If B's version had unique post-compaction content, it's lost. This is an accepted trade-off of the single-version-per-session model.

**Session replacement preserves locations.** Replacement is transactional but avoids `ON DELETE CASCADE` for `vault_session_locations`: (1) `UPDATE vault_sessions SET ... WHERE uuid = ?` (updates metadata, raw_jsonl, content_hash — no DELETE trigger), (2) `DELETE FROM vault_files WHERE session_uuid = ?` + re-insert files, (3) `DELETE FROM vault_fts WHERE session_uuid = ?` + re-insert FTS rows. Locations are preserved across replacements because the session row is updated, not deleted and re-inserted. New locations are upserted separately.

### Transaction Batching

- **Bulk imports**: batch ~50 sessions OR ~100MB total `raw_jsonl` per transaction (whichever limit hits first). On any batch tx error, log the error, then retry each session in the batch individually. On individual session failure, log and continue — don't abort the entire import.
- **Hook imports**: per-session transactions (single session at a time).

## FTS Scanner

The scanner is a single-pass JSONL reader in `internal/vault/scanner.go` that extracts searchable text for the FTS5 index. It defines its own minimal JSON wire types decoupled from `internal/session/parse.go`. The scanner accepts `io.Reader` (not just file paths) so it works for both import-from-disk and render-from-BLOB scenarios (TUI viewer parses from `vault_files` BLOBs).

### JSONL Line Types

Every line type in the session file gets an explicit handling decision:

| Line type | Action | Notes |
|-----------|--------|-------|
| `user` | **Extract** | Text content, `cwd`, `gitBranch` |
| `assistant` | **Extract** | Text + tool_use names/summaries; deduplicate progressive snapshots |
| `ai-title` | **Extract** | `aiTitle` field → session title (high-value search signal) |
| `custom-title` | **Extract** | `customTitle` field → session title (overrides ai-title) |
| `system` (subtype `away_summary`) | **Extract** | Summary text |
| `system` (subtype `turn_duration`) | **Skip** | Timing metadata, not searchable |
| `attachment` | **Extract** | Attachment filename for search (from content blocks) |
| `permission-mode` | **Skip** | Session config, not searchable |
| `file-history-snapshot` | **Skip** | Undo snapshots, not searchable |
| `last-prompt` | **Skip** | Duplicate of user message |
| `queue-operation` | **Skip** | Internal scheduling |
| `progress` | **Skip** | Streaming progress, not final content |

### Extraction Scope

**Extracted per message:**
1. All `"text"` values from content blocks (user and assistant messages)
2. Tool use names (Bash, Read, Edit, etc.)
3. Tool input summaries — file paths from Read/Edit, commands from Bash
4. Bounded tool_result text — first 16KB of text content from tool_result blocks (head+tail truncation matching `claude-history`'s approach). Tool results contain searchable content (error messages, grep output, build logs) that users commonly search for
5. Subagent descriptions and types
6. `aiTitle` / `customTitle` fields for session title
7. Attachment filenames

**Skipped:**
- Base64 and binary content (including image blocks in tool_result)
- System-reminder tags and their contents
- Progressive snapshot duplicates — assistant messages sharing the same `message.id` are accumulated and deduplicated by `(Type, Text, Name, ID)` tuple, matching the proven approach in `internal/session/parse.go:161-174`
- JSON structural noise
- `tool_result` blocks with no text content (e.g., image-only results)

### Sanitization

Scanner output is run through `sanitize.StripSecrets()` before FTS insertion. This prevents secrets (API keys, tokens, credentials) from appearing in `snippet()` search results. The `raw_jsonl` blob and `vault_files` content remain unsanitized — verbatim preservation is the point.

### Output

For each message, the scanner produces a `ScanResult`:

```
ScanResult {
    TurnIndex    int
    MessageIndex int       // sequential within the turn (0 = first message)
    Role         string    // "user", "assistant", "system"
    SubagentID   string    // "" for main session
    ContentText  string    // extracted, sanitized searchable text
    Timestamp    time.Time
}
```

One FTS row is inserted per `ScanResult`. Each user message and each assistant message produces a separate `ScanResult` with the appropriate `Role`. This enables `--role` filtering on search and produces focused `snippet()` results per message rather than per turn-pair.

### Performance

Single-pass, streaming, O(n) in file size. The dedup map for progressive snapshots is bounded by unique message IDs (typically hundreds). No full parse into turn pairs — line-by-line extraction only.

## Hook Integration

### Archival Triggers

Two confirmed archival paths, plus a third pending investigation:

1. **MCP server startup** (background, current project) — a background goroutine runs vault import for the current project's session directory on server boot. Same pattern as existing session sweep in `internal/session/sweep.go`. Captures sessions that ended since last boot. One-session delay is acceptable.

2. **Explicit `capy vault import`** (manual, all projects) — for onboarding, cross-machine scenarios, and catching anything the background sweep missed. This is the primary archival path.

3. **PreCompact hook** (pending investigation) — the `precompact` hook event exists in capy's hook router (`internal/hook/hook.go:26`) and `handlePreCompact` already receives the full payload via `input []byte` — it just returns `(nil, nil)` without acting. The hook payload format is unknown — `HookAdapter` has no `ParsePreCompact` method. Before implementing this path, the payload must be captured and documented by triggering `/compact` with a debug handler. Implementing archival is adding logic to the existing handler, not adding a new hook path. Additionally, the hook wrapper (`capy.sh`) forces `exit 0` on all hook invocations, which means PreCompact failures are silently swallowed. If PreCompact archival is implemented, failures should be logged to `~/.config/capy/vault-error.log` since post-compaction recovery via `import` is impossible.

### Why Not SessionEnd Hooks

Session-end hooks were rejected for the existing session sweep (see `docs/done/sessionflow-rag/design.md`): hooks are short-lived processes where goroutines die on exit, and there's a risk of DB lock contention with the dying MCP server. The same reasoning applies to vault — the MCP server startup pattern is safer and proven.

### Hook Wiring (for confirmed paths)

The MCP server startup sweep is wired in `internal/server/server.go`, after the existing `session.Sweep()` call. Vault sweep is opt-in: silently skipped if `CAPY_VAULT_KEY` is not set.

### Execution Context

Hooks and server startup execute inside the project directory, providing accurate metadata:
- `os.Getwd()` → accurate project path (no unmangling needed)
- `gitBranch` field from JSONL user entries → current branch (no need to shell out to `git`; the field is present on every user-type line)

### Scope Limitation

The MCP server startup sweep is scoped to the current project's session directory only. Sessions from other projects are NOT archived by this path. If a user works exclusively on project A, sessions from project B will age past Claude Code's 30-day cleanup without being archived. The gap is closed by periodic `capy vault import` (e.g., via cron or manual habit). Users should be advised during `capy vault setup` or first import to run `capy vault import` periodically or set up a cron job.

### Failure Mode

If the server startup sweep fails, it logs a warning. It must not block the MCP server or Claude Code. Sessions can always be recovered via `capy vault import`.

## CLI Commands

All commands are under `capy vault` and require `CAPY_VAULT_KEY` to be set.

| Command | Description |
|---------|-------------|
| `import [--path <dir>] [--project <filter>]` | Scan and archive sessions. Mutating by default, `--dry-run` to preview. |
| `search <query> [--project] [--after] [--before] [--role] [--json]` | Full-text search with snippet context. Plain keyword mode by default (each token auto-quoted); `--raw` for FTS5 MATCH syntax. |
| `list [--project <filter>] [--limit N] [--json]` | List sessions with title, reverse chronological. |
| `show <session_id> [--format <text\|markdown\|json>]` | Display full session. Parses raw_jsonl on the fly. Default: pager. `--format` for export. |
| `restore <session_id> [--output <path>]` | Write JSONL + full session directory back to disk. |
| `resume <session_id> [--dir <path>]` | Restore + launch `claude --resume <session_id>`. |
| `delete <session_id>` | Remove a session from the vault. Confirmation prompt unless `--yes`. |
| `stats [--json]` | DB size, session count, per-project breakdown. |
| `checkpoint` | Flush WAL into main DB file. Run before copying vault.db to another machine. |

All commands support `--tui` flag for interactive bubbletea interface. Partial UUID matching (8+ chars, git-style) on `show`, `restore`, `resume`, `delete`. On ambiguous match, show candidates with date, project, and title to disambiguate.

### Restore Details

Restore writes back the complete session directory structure:
- `<uuid>.jsonl` from `vault_sessions.raw_jsonl`
- `<uuid>/<relative_path>` for each entry in `vault_files` (subagents, tool-results, etc.)

Default target: `ClaudeProjectsDir()/<claude_project_dir>/` where `ClaudeProjectsDir()` respects `CLAUDE_CONFIG_DIR` (same helper used by discovery and import — see [Session Discovery](#session-discovery--import)). Override with `--output <path>`. Prompts before overwriting existing files. If a session has multiple locations, restore prompts the user to choose which location to restore to (or uses `--output`).

**Path safety:** Before writing any `vault_files` entry, the restore path is validated:
- Reject `relative_path` values that are absolute paths
- Reject `relative_path` values containing `..` components
- Verify `filepath.Join(restoreRoot, uuid, relativePath)` resolves to a path under the restore root (containment check via `filepath.Rel`)
- Skip entries that fail validation with a warning to stderr

### Resume Details

Resume restores the session files, then launches Claude Code using `os/exec.Command("claude", "--resume", sessionID)` with stdin/stdout/stderr inherited. Directory resolution follows a fallback chain:

1. `--dir <path>` flag → use it (explicit override, validated to exist)
2. `project_path` starts with `/` and exists as a directory on this machine → use it
3. Current working directory → use it (user is likely already in the right project)
4. Prompt the user with `project_path` as a hint (may be from a different machine or a mangled dir name)

For sessions with multiple locations, prefer the location matching the current machine's `machine_id`. All chosen paths are validated to exist before launching `claude`.

## TUI Interface

Activated by `--tui` on any vault command. Built with bubbletea (Charm ecosystem).

### Layout

- **Left panel** (`bubbles/list`): session list grouped by project. Shows short UUID, title, date, message count. Fuzzy-filterable.
- **Right panel** (`bubbles/viewport`): session content viewer. Parses `raw_jsonl` on the fly with Human/Assistant formatting, markdown rendering (glamour), syntax-highlighted code.
- **Bottom bar**: mode indicator, keybindings, filter state.

### Modes

- **Browse** (default on `list --tui`): navigate sessions, preview on selection, Enter for full viewer.
- **Search** (default on `search --tui`): live search input, debounced FTS5 queries, snippet highlights. Enter scrolls to `turn_index` in viewer.
- **View** (default on `show --tui`): vim-style navigation (j/k, g/G, / for in-viewer search). `--show-tools` and `--show-thinking` flags. The viewer uses lazy line-indexing: holds the raw `[]byte` plus a `\n`-offset index, and only unmarshals lines in the visible viewport. When subagent files exist in `vault_files`, the viewer fetches and parses them inline with visual distinction (dimmed, prefixed — matching `claude-history`'s approach). Non-JSONL vault_files (tool-results, meta.json, other sidecars) are archive-only — preserved for `restore` but not rendered in `show` or the TUI viewer.

### Key Bindings

`q`/`Esc` back/quit, `/` search, `f` filter project, `r` restore, `c` copy message, `R` resume.

### Dependencies

bubbletea, bubbles (list, viewport, textinput), lipgloss (styling), glamour (markdown rendering).

### Package Structure

`internal/vault/tui/` with separate files per model (list, viewer, search) and a root `app.go` compositor. Imports `internal/vault/` for data access.

## Cross-Machine Merge

### Workflow

1. Run `capy vault checkpoint` on machine A (flushes WAL into main DB file — without this, recent writes may live in `vault.db-wal` and be lost during copy)
2. Copy `vault.db` from machine A → machine B
3. Ensure the same `CAPY_VAULT_KEY` is available on machine B (the DB is encrypted; a different key will fail to open it)
4. Run `capy vault import` on machine B
5. B's local sessions merge alongside A's sessions — no duplicates, no data loss

**Important:** Copying vault.db replaces B's existing vault entirely. If B already has a vault with local sessions, those are lost unless B first copies its own vault elsewhere. A merge-from-vault workflow (`capy vault merge --from <path>`) is planned for a future version — see [Future Improvements](#future-improvements).

**Machine-ID mismatch detection:** When `capy vault import` opens a vault.db whose stored `machine_id` entries don't include the current machine, it prints a prominent warning: "This vault.db contains sessions from machine(s) X. Your local sessions are not yet archived — consider running `import` before replacing this file." This prevents accidental data loss from blind overwrite.

### Merge Behavior Per Table

| Table | Strategy |
|-------|----------|
| `vault_sessions` | UUID exists + same hash → skip. Different hash + incoming larger → UPDATE in place (preserves locations). Different hash + incoming smaller → skip. New UUID → insert. |
| `vault_session_locations` | Upsert by composite PK `(uuid, machine_id, claude_project_dir)`. Updates `last_seen_at` on match. Preserved across session replacement (UPDATE, not DELETE+INSERT). |
| `vault_files` | On replacement: explicitly delete old files + insert new ones (within same tx). On session delete: CASCADE. |
| `vault_fts` | Explicitly deleted and rebuilt as part of session insert/replace transactions. |
| `vault_meta` | Local-only, not merged. |

### Machine Identity

Machine identity is resolved outside the database to survive DB copies:

1. `CAPY_MACHINE_ID` env var → if set, use it (Docker/CI)
2. `~/.config/capy/machine-id` file → if exists, use it
3. Neither → generate UUIDv4, write atomically to file

Each machine tags its imports with its own stable ID. Copying vault.db doesn't carry the machine identity.

## DB Size Projection

Based on measured data from a real Claude Code installation (219 sessions over ~3 months):

| Component | Total size | Per-session avg | Notes |
|-----------|-----------|-----------------|-------|
| Main JSONL files | 114.6 MB | 536 KB | Stored as `raw_jsonl` BLOB |
| Subagent files | 14.1 MB | 64 KB (across 58 sessions with subagents) | Stored in `vault_files` |
| Tool-result files | 3.1 MB | 70 KB (across 31 sessions with tool-results) | Stored in `vault_files` |
| FTS index overhead | ~10-15 MB (est.) | — | Porter-tokenized text + UNINDEXED metadata |
| **Total estimated** | **~145 MB** | — | **Uncompressed, single machine** |

**Growth rate:** ~50 MB/month for an active user (1-2 sessions/day). **12-month projection:** ~600 MB–1 GB uncompressed. SQLite handles this fine. Cross-machine copy becomes slower past 500 MB (the stated use case); `zstd` compression (v2) would reduce this by 5-8x.

**Per-file size cap:** `vault_files` entries larger than 5 MB are skipped with a warning to stderr. Large tool-results (multi-MB build logs, screenshots) are reproducible — the conversation JSONL is the critical artifact. This cap prevents degenerate DB growth from outlier files.

**No automatic cleanup:** Vault archives forever by design. Users who need to shed space can use `capy vault delete` to remove individual sessions.

## Assumptions

1. **Claude Code JSONL format is stable** — the `type`/`message`/`content` structure won't break in incompatible ways. If it does, raw BLOBs are still restorable; only the scanner needs updating.
2. **PreCompact hook fires before file mutation** — UNVERIFIED. The `handlePreCompact` handler already receives the full payload via `input []byte` but the payload format is undocumented. This assumption must be validated before implementing PreCompact archival. The vault's core functionality (import + server-startup sweep) does not depend on this assumption.
3. **Session UUIDs are globally unique** — cross-machine merge relies on UUID collision being impossible at this scale. Cross-project forking creates the same UUID in multiple project directories — vault handles this via `vault_session_locations` (content stored once, locations tracked separately).
4. **Claude Code session directory structure is stable** — the mangled-path convention and `~/.claude/projects/` location persist. Discovery also respects `CLAUDE_CONFIG_DIR` for non-default installations.
5. **`cwd` and `gitBranch` field availability in JSONL** — Claude Code user entries include `cwd` (working directory) and `gitBranch` (current branch) fields. Verified present on every user-type line in sampled sessions. Used for project path resolution and branch metadata during import.

### Known Asymmetries

- **`CLAUDE_CONFIG_DIR` handling:** Vault's discovery respects `CLAUDE_CONFIG_DIR`, but capy's existing `internal/session/sweep.go:SessionDir()` hardcodes `~/.claude/projects/`. Vault will archive sessions from non-default Claude installations that the MCP server's session sweep never indexed. A shared `ClaudeProjectsDir()` helper should be extracted as a follow-up to align both code paths. Tracked as a non-blocking follow-up in tasks.md.

## Not Doing

- **Cloud sync** — vault is local-only; cross-machine transfer is manual file copy
- **Multi-user access** — single-user tool, no auth or sharing server
- **Codex session support** — different format and discovery paths; future work
- **Session diffing** — comparing versions of the same session across compaction events
- **Real-time watch mode** — no filesystem watcher; hook-driven + explicit import only
- **Automatic cleanup/retention** — vault archives forever; no TTL, no tiered retention
- **File-history preservation** — `~/.claude/file-history/<uuid>/` is a global sidecar used for undo support. Claude Code looks it up by session UUID automatically, so it doesn't need to be copied for resume to work. Not included in vault's preservation scope
- **Sharing/export with redaction** — the HMW statement mentions sharing aspirationally; the redaction pipeline (inspired by pi-share-hf) is a separate feature requiring its own design for secret scanning, deny patterns, and review workflow. Deferred to a future milestone.

## Future Improvements

### BLOB Compression (zstd)

Raw JSONL blobs are stored uncompressed in v1. JSONL is highly compressible text (typical 5-8x reduction with zstd). On a machine with 214 sessions totaling ~110MB of JSONL, compression could reduce vault.db from ~110MB to ~15-25MB. This matters for cross-machine copy (the stated use case) and for long-term storage growth.

Compression is deferred because:
- It adds complexity to every read path (restore, show, TUI viewer all need decompression)
- The performance impact on interactive operations (TUI scrolling, `show` rendering) needs benchmarking
- v1 needs to ship and validate the core archival/search/restore workflow first

**Implementation plan for v2:** Add `compress/zstd` encoding (Go's `github.com/klauspost/compress/zstd` — ~20 lines of encode/decode). Benchmark: (a) import throughput with vs. without compression, (b) `show` latency for 1MB, 5MB, 10MB sessions, (c) TUI viewer scroll performance with compressed blobs. If decompression latency exceeds 50ms for a typical session, consider caching decompressed content or streaming decompression. Add `capy vault compact` command to recompress existing uncompressed blobs in-place.

### PreCompact Hook Archival

Once the PreCompact hook payload is captured and documented:
- Add a `vault_snapshots` table for append-only cold storage of pre-compaction content
- Wire the hook handler to archive both to `vault_snapshots` (historical) and `vault_sessions` (active)
- Add `capy vault snapshots <session_id>` to list snapshots and `restore --snapshot <hash>` to restore from a specific snapshot
- Design snapshot retention policy (e.g., keep N most recent per session, or age-based eviction)

### Cross-Machine Vault Merge

The v1 cross-machine workflow requires copying vault.db and replacing the destination vault entirely. A proper merge command would allow importing sessions from a source vault into an existing destination vault:

- `capy vault merge --from <path> [--key <key>]` — opens the source vault (optionally with a different encryption key), iterates sessions, and applies the same idempotent import logic (hash-based, larger wins) against the destination vault.
- Handles the case where source and destination have different `CAPY_VAULT_KEY` values.
- Merges `vault_session_locations` from both vaults, preserving all known locations.

### All-Projects Server Startup Sweep

The v1 server startup sweep is scoped to the current project only, leaving other projects' sessions unarchived. A future version could expand the sweep to walk `~/.claude/projects/` (same as `capy vault import`) in the background. This is feasible since discovery is a fast directory walk, but adds latency to server startup and increases write contention on vault.db. Needs benchmarking against real session directories (hundreds of projects, thousands of files).

### Sharing/Export Pipeline

A redacted export command for safely sharing sessions. Inspired by pi-share-hf's multi-stage pipeline: secret scanning (regex + TruffleHog), deny patterns, LLM review, manual verification before export. Requires its own design document.

## Inspiration Projects

Design was informed by analysis of four existing tools:

| Project | Key Takeaway |
|---------|-------------|
| claude-vault (Rust) | Hook-driven auto-archive (PreCompact/SessionEnd), FTS5 schema design |
| claude-history (Rust) | TUI with fuzzy search, fork/resume from viewer |
| cc-sessions (Python) | Incremental indexing, resume by partial ID |
| pi-share-hf (TypeScript) | Multi-stage redaction pipeline for sharing |

## References

- [Sessionflow RAG design](../../done/sessionflow-rag/design.md) — existing session parsing and sweep architecture
- [ADR-019/020](../../adr/) — encrypted knowledge DB, WAL/rekey incompatibility
- [ADR-017](../../adr/017-source-kind-separation.md) — source kind separation
