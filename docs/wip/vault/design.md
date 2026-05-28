# Vault — Design Document

> **Status:** Draft
> **Created:** 2026-05-28

## Problem

Claude Code sessions are ephemeral, project-scoped, and destructible. Content is lost to compaction (`/compact` rewrites the JSONL file), auto-cleanup (30-day default retention), and accidental deletion. There is no cross-project archive, no way to search past conversations globally, and no restore mechanism once a session file is gone.

### How Might We

How might we give Claude Code power users a single, durable archive of every conversation they've ever had — across all projects — so they can search, fully view, revisit, restore and/or share past sessions, without losing data to compaction, project-scoping, auto-cleanup (30-day default retention), or accidental deletion?

A vault inverts all three properties — permanent, global, and preserving the raw JSONL verbatim. The vault is both a search index and a backup/restore system — verbatim preservation is load-bearing, not just an implementation detail.

## Target User

Any Claude Code user, even if they don't use other capy features (MCP server, context-window management). The vault should have a low-friction entry point that doesn't require the full capy setup.

## Success Criteria

A user runs `capy vault import`, and every session across all projects is archived verbatim. After Claude Code auto-cleans a 31-day-old session, the user can `show`, `restore`, `search`, or browse via TUI (`--tui`). Cross-machine: copy vault.db from machine A to machine B (same `CAPY_VAULT_KEY` required), run `import` again, and sessions from both machines coexist — no duplicates, no overwrites.

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

- Default: `~/.config/capy/vault.db`
- Override: `CAPY_VAULT_PATH` environment variable
- Encryption: `CAPY_VAULT_KEY` environment variable (separate from `CAPY_DB_KEY`)

### Schema

Four tables + 1 FTS5 virtual table + 1 metadata table + 3 indexes.

**`vault_sessions`** — active state, one row per session. Holds metadata and the raw JSONL blob for verbatim restore.

```sql
CREATE TABLE vault_sessions (
    uuid              TEXT PRIMARY KEY,
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
- **No `vault_messages` table** — initially considered for message-level search and TUI display, but rejected. The TUI needs to parse `raw_jsonl` on the fly anyway for faithful rendering (thinking blocks, tool results, formatting). FTS5 `UNINDEXED` columns on the `vault_fts` table provide message-level search resolution (turn_index, role) without a separate table. This eliminates double-stored text, external content triggers, and cascade complexity.
- **FTS5 one-row-per-message** — each user message and each assistant message is a separate FTS row, with the `role` UNINDEXED column distinguishing them. This enables `--role` filtering on search and produces focused `snippet()` results. Better granularity than one-row-per-session or one-row-per-turn-pair. FTS5 handles 100K+ small rows trivially.
- **`subagent_id` uses empty string sentinel** — SQLite treats NULL as never equal to NULL in composite primary keys, which would break uniqueness constraints. Empty string `''` avoids this.
- **`ON DELETE CASCADE`** on `vault_files` and `vault_session_locations` — when a session is replaced (larger version wins), all associated files and locations are automatically cleaned up within the same transaction. Note: CASCADE applies only to regular tables, not to `vault_fts` (FTS5 virtual tables don't support foreign keys — FTS rows require explicit `DELETE FROM vault_fts WHERE session_uuid = ?` in the same transaction).
- **`vault_files` instead of `vault_subagents`** — a generic files table preserves the entire session directory (subagent JSONLs, meta.json, tool-results, and any future sidecar types), not just known file types. This is consistent with vault's promise of full preservation. Subagent metadata (agent_type, description) is read from the stored `meta.json` files at display time.
- **FTS content is sanitized** — `sanitize.StripSecrets()` runs on scanner output before FTS insertion. This prevents secrets from appearing in search snippet results. The `raw_jsonl` blob and `vault_files` content remain unsanitized — verbatim preservation is the point.
- **Snapshots deferred** — `vault_snapshots` (append-only cold storage for pre-compaction content) is deferred to a future version. The PreCompact hook payload format is unverified (see [Assumption #2](#assumptions)), and the vault works without snapshots — `import` and server-startup sweep are the primary archival paths. See [Future Improvements](#future-improvements).

## Session Discovery & Import

### Discovery

`capy vault import` discovers sessions by walking the Claude Code projects directory. The base path is resolved as:
1. `CLAUDE_CONFIG_DIR` env var → use `$CLAUDE_CONFIG_DIR/projects/`
2. Default: `~/.claude/projects/`

For each mangled project directory, it discovers:
- `*.jsonl` files (main sessions)
- `<uuid>/` directories containing all session files (subagents, tool-results, any other sidecars)

Supports `--path <dir>` for importing from custom locations (USB backup, copied `.claude/` directory from another machine).

### Project Path Handling

Claude Code mangles project paths by replacing `/` and `.` with `-`. This is lossy (can't distinguish `/` from `.` in the original). Two columns on `vault_session_locations` track this:

- **`claude_project_dir`** — always the mangled directory name (e.g., `-home-sergio-Projects-personal-capy`). Always available from any import source. Used for restore (writing back to `~/.claude/projects/<claude_project_dir>/<uuid>.jsonl`).
- **`project_path`** — best-known real project path. From hooks: accurate `os.Getwd()`. From bulk import: the `cwd` field in the JSONL (first user message that has it), falling back to the mangled dir name. Used for display and resume.

### Metadata Extraction

| Field | Source |
|-------|--------|
| `uuid` | Filename (minus `.jsonl` extension) |
| `project_path` | `os.Getwd()` from hooks; `cwd` field from first user JSONL line during bulk import; mangled dir name as last resort |
| `claude_project_dir` | Mangled directory name (always available) |
| `start_time` / `end_time` | First and last JSONL line timestamps |
| `message_count` | Count of user + assistant entries |
| `size_bytes` | File size on disk |
| `content_hash` | SHA-256 composite: main session file + all files in the session directory (sorted by relative path) |
| `git_branch` | `git rev-parse --abbrev-ref HEAD` from hooks (NULL if detached HEAD or bulk import) |
| `machine_id` | From machine identity resolution (see Cross-Machine section) |

### Idempotent Import Logic

1. Compute composite `content_hash` of the session (main file + all session directory files)
2. If UUID exists with same hash → skip (already archived)
3. If UUID exists with different hash and incoming `size_bytes >= existing` → replace (session grew)
4. If UUID exists with different hash and incoming `size_bytes < existing` → skip (compacted, don't overwrite fuller archive)
5. If UUID doesn't exist → insert

**Known limitation:** Cross-machine merge after independent compaction may lose content. If machine A has a 1MB pre-compaction version and machine B has a 0.6MB independently-compacted version, importing B's vault into A would skip B's version (smaller). If B's version had unique post-compaction content, it's lost. This is an accepted trade-off of the single-version-per-session model.

Session replacement is transactional: delete old FTS rows (`DELETE FROM vault_fts WHERE session_uuid = ?`) → insert new session + files + FTS rows, all within one `sql.Tx`. `ON DELETE CASCADE` handles `vault_files` cleanup automatically.

### Transaction Batching

- **Bulk imports**: batch ~50 sessions per transaction for write performance. On batch failure, fall back to per-row insertion to isolate the problem session.
- **Hook imports**: per-session transactions (single session at a time).

## FTS Scanner

The scanner is a single-pass JSONL reader in `internal/vault/scanner.go` that extracts searchable text for the FTS5 index. It defines its own minimal JSON wire types decoupled from `internal/session/parse.go`.

### Extraction Scope

**Extracted per turn:**
1. All `"text"` values from content blocks (user and assistant messages)
2. Tool use names (Bash, Read, Edit, etc.)
3. Tool input summaries — file paths from Read/Edit, commands from Bash
4. Subagent descriptions and types

**Skipped:**
- Base64 and binary content
- System-reminder tags and their contents
- Progressive snapshot duplicates — assistant messages sharing the same `message.id` are accumulated and deduplicated by `(Type, Text, Name, ID)` tuple, matching the proven approach in `internal/session/parse.go:161-174`
- JSON structural noise

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

3. **PreCompact hook** (pending investigation) — the `precompact` hook event exists in capy's hook router (`internal/hook/hook.go:26`) but `handlePreCompact` is a stub that discards input. The hook payload format is unknown — `HookAdapter` has no `ParsePreCompact` method. Before implementing this path, the payload must be captured and documented by triggering `/compact` with a debug handler. Additionally, the hook wrapper (`capy.sh`) forces `exit 0` on all hook invocations, which means PreCompact failures are silently swallowed. If PreCompact archival is implemented, failures should be logged to `~/.config/capy/vault-error.log` since post-compaction recovery via `import` is impossible.

### Why Not SessionEnd Hooks

Session-end hooks were rejected for the existing session sweep (see `docs/done/sessionflow-rag/design.md`): hooks are short-lived processes where goroutines die on exit, and there's a risk of DB lock contention with the dying MCP server. The same reasoning applies to vault — the MCP server startup pattern is safer and proven.

### Hook Wiring (for confirmed paths)

The MCP server startup sweep is wired in `internal/server/server.go`, after the existing `session.Sweep()` call. Vault sweep is opt-in: silently skipped if `CAPY_VAULT_KEY` is not set.

### Execution Context

Hooks and server startup execute inside the project directory, providing accurate metadata:
- `os.Getwd()` → accurate project path (no unmangling needed)
- `git rev-parse --abbrev-ref HEAD` → current branch (store NULL if result is literal `"HEAD"`, indicating detached HEAD state)

### Scope Limitation

The MCP server startup sweep is scoped to the current project's session directory only. Sessions from other projects are NOT archived by this path. If a user works exclusively on project A, sessions from project B will age past Claude Code's 30-day cleanup without being archived. The gap is closed by periodic `capy vault import` (e.g., via cron or manual habit). Users should be advised during `capy vault setup` or first import to run `capy vault import` periodically or set up a cron job.

### Failure Mode

If the server startup sweep fails, it logs a warning. It must not block the MCP server or Claude Code. Sessions can always be recovered via `capy vault import`.

## CLI Commands

All commands are under `capy vault` and require `CAPY_VAULT_KEY` to be set.

| Command | Description |
|---------|-------------|
| `import [--path <dir>] [--project <filter>]` | Scan and archive sessions. Mutating by default, `--dry-run` to preview. |
| `search <query> [--project] [--after] [--before] [--role]` | FTS search with turn-level results and snippet context. |
| `list [--project <filter>] [--limit N]` | List sessions, reverse chronological. |
| `show <session_id>` | Display full session. Parses raw_jsonl on the fly. Partial UUID match. |
| `restore <session_id> [--output <path>]` | Write JSONL + full session directory back to disk. |
| `resume <session_id>` | Restore + launch `claude --resume <session_id>`. |
| `stats` | DB size, session count, per-project breakdown. |

All commands support `--tui` flag for interactive bubbletea interface. Partial UUID matching (6-8 chars, git-style) on `show`, `restore`, `resume`.

### Restore Details

Restore writes back the complete session directory structure:
- `<uuid>.jsonl` from `vault_sessions.raw_jsonl`
- `<uuid>/<relative_path>` for each entry in `vault_files` (subagents, tool-results, etc.)

Default target: `~/.claude/projects/<claude_project_dir>/` (from the session's location, always known). Override with `--output <path>`. Prompts before overwriting existing files. If a session has multiple locations, restore prompts the user to choose which location to restore to (or uses `--output`).

**Path safety:** Before writing any `vault_files` entry, the restore path is validated:
- Reject `relative_path` values that are absolute paths
- Reject `relative_path` values containing `..` components
- Verify `filepath.Join(restoreRoot, uuid, relativePath)` resolves to a path under the restore root (containment check via `filepath.Rel`)
- Skip entries that fail validation with a warning to stderr

### Resume Details

Resume restores the session files, then launches Claude Code using `os/exec.Command("claude", "--resume", sessionID)` with stdin/stdout/stderr inherited. If `project_path` is an accurate real path (from hook import), `resume` changes to that directory first. If `project_path` is a mangled dir name (from bulk import), `resume` warns the user and prompts for the project directory.

## TUI Interface

Activated by `--tui` on any vault command. Built with bubbletea (Charm ecosystem).

### Layout

- **Left panel** (`bubbles/list`): session list grouped by project. Shows short UUID, date, message count. Fuzzy-filterable.
- **Right panel** (`bubbles/viewport`): session content viewer. Parses `raw_jsonl` on the fly with Human/Assistant formatting, markdown rendering (glamour), syntax-highlighted code.
- **Bottom bar**: mode indicator, keybindings, filter state.

### Modes

- **Browse** (default on `list --tui`): navigate sessions, preview on selection, Enter for full viewer.
- **Search** (default on `search --tui`): live search input, debounced FTS5 queries, snippet highlights. Enter scrolls to `turn_index` in viewer.
- **View** (default on `show --tui`): vim-style navigation (j/k, g/G, / for in-viewer search). `--show-tools` and `--show-thinking` flags. The viewer uses lazy line-indexing: holds the raw `[]byte` plus a `\n`-offset index, and only unmarshals lines in the visible viewport. When subagent files exist in `vault_files`, the viewer fetches and indexes them alongside the main session (matching `claude-history`'s approach of rendering subagent content with visual distinction).

### Key Bindings

`q`/`Esc` back/quit, `/` search, `f` filter project, `r` restore, `c` copy message, `R` resume.

### Dependencies

bubbletea, bubbles (list, viewport, textinput), lipgloss (styling), glamour (markdown rendering).

### Package Structure

`internal/vault/tui/` with separate files per model (list, viewer, search) and a root `app.go` compositor. Imports `internal/vault/` for data access.

## Cross-Machine Merge

### Workflow

1. Copy `vault.db` from machine A → machine B
2. Ensure the same `CAPY_VAULT_KEY` is available on machine B (the DB is encrypted; a different key will fail to open it)
3. Run `capy vault import` on machine B
4. B's local sessions merge alongside A's sessions — no duplicates, no data loss

**Important:** Copying vault.db replaces B's existing vault entirely. If B already has a vault with local sessions, those are lost unless B first copies its own vault elsewhere. A merge-from-vault workflow (`capy vault merge --from <path>`) is planned for a future version — see [Future Improvements](#future-improvements).

### Merge Behavior Per Table

| Table | Strategy |
|-------|----------|
| `vault_sessions` | UUID exists + same hash → skip. Different hash + incoming larger → replace. Different hash + incoming smaller → skip. New UUID → insert. |
| `vault_session_locations` | Upsert by composite PK `(uuid, machine_id, claude_project_dir)`. Updates `last_seen_at` on match. |
| `vault_files` | Cascades with parent session via `ON DELETE CASCADE`. |
| `vault_fts` | Explicitly deleted and rebuilt as part of session insert/replace transactions. |
| `vault_meta` | Local-only, not merged. |

### Machine Identity

Machine identity is resolved outside the database to survive DB copies:

1. `CAPY_MACHINE_ID` env var → if set, use it (Docker/CI)
2. `~/.config/capy/machine-id` file → if exists, use it
3. Neither → generate UUIDv4, write atomically to file

Each machine tags its imports with its own stable ID. Copying vault.db doesn't carry the machine identity.

## Assumptions

1. **Claude Code JSONL format is stable** — the `type`/`message`/`content` structure won't break in incompatible ways. If it does, raw BLOBs are still restorable; only the scanner needs updating.
2. **PreCompact hook fires before file mutation** — UNVERIFIED. The `handlePreCompact` stub exists but the payload format is undocumented. This assumption must be validated before implementing PreCompact archival. The vault's core functionality (import + server-startup sweep) does not depend on this assumption.
3. **Session UUIDs are globally unique** — cross-machine merge relies on UUID collision being impossible at this scale. Cross-project forking creates the same UUID in multiple project directories — vault handles this via `vault_session_locations` (content stored once, locations tracked separately).
4. **Claude Code session directory structure is stable** — the mangled-path convention and `~/.claude/projects/` location persist. Discovery also respects `CLAUDE_CONFIG_DIR` for non-default installations.
5. **`CLAUDE_CONFIG_DIR` inconsistency with existing capy** — vault's discovery respects `CLAUDE_CONFIG_DIR`, but capy's existing `internal/session/sweep.go:SessionDir()` hardcodes `~/.claude/projects/`. This means the vault will archive sessions from non-default Claude installations that the MCP server's session sweep never indexed. This is an acceptable asymmetry for v1; a shared `ClaudeProjectsDir()` helper should be extracted as a follow-up to align both code paths.
6. **`cwd` field availability in JSONL** — Claude Code user entries commonly include a `cwd` field with the working directory. This is used by `claude-history` for accurate project path resolution and is preferred over mangled directory names during bulk import. If `cwd` is absent (older sessions, non-standard setups), the mangled dir name is used as fallback.

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
