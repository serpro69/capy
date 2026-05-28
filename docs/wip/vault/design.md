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

A user runs `capy vault import`, and every session across all projects is archived verbatim. After Claude Code auto-cleans a 31-day-old session, the user can `show`, `restore`, `search`, or browse via TUI (`--tui`). Zero data loss. Cross-machine: copy vault.db from machine A to machine B, run `import` again, and sessions from both machines coexist — no duplicates, no overwrites.

## Architecture Overview

Vault is a new `internal/vault/` package providing verbatim, cross-project session archival with full-text search. It operates a separate encrypted SQLite database (`vault.db`) independent of capy's per-project FTS knowledge store.

### Four Layers

1. **Storage layer** (`internal/vault/store.go`) — owns the SQLite connection lifecycle, schema, encryption (via `CAPY_VAULT_KEY`), and all CRUD operations. Imports `EncryptedDSN` and corruption helpers from `internal/store/` but manages its own connection pool, WAL checkpointing, and prepared statements.

2. **Scanner layer** (`internal/vault/scanner.go`) — a single-pass JSONL reader that extracts searchable text from session files. Defines its own minimal JSON wire types (~20 lines), decoupled from `internal/session/parse.go`. Extracts: message text, tool names, tool input summaries (file paths, commands), subagent descriptions. Skips: base64, binary, system-reminder noise, progressive snapshot duplicates. Scanner output is sanitized via `sanitize.StripSecrets()` before FTS insertion to prevent secrets from appearing in search snippets.

3. **Discovery layer** (`internal/vault/discovery.go`) — walks the Claude Code session directory to find all session JSONL files and their associated session directories (subagents, tool-results, and any other sidecars) across all projects. Respects `CLAUDE_CONFIG_DIR` environment variable, falling back to `~/.claude/`. Handles metadata extraction: project path, timestamps, message count, file size, content hash.

4. **CLI/TUI layer** (`cmd/capy/vault.go` + `internal/vault/tui/`) — cobra subcommand group (`capy vault <cmd>`). Each command supports a `--tui` flag that switches to an interactive bubbletea interface.

## Storage Model

### Database Location

- Default: `~/.config/capy/vault.db`
- Override: `CAPY_VAULT_PATH` environment variable
- Encryption: `CAPY_VAULT_KEY` environment variable (separate from `CAPY_DB_KEY`)

### Schema

Three tables + 1 FTS5 virtual table + 1 metadata table.

**`vault_sessions`** — active state, one row per session. Holds metadata and the raw JSONL blob for verbatim restore.

```sql
CREATE TABLE vault_sessions (
    uuid              TEXT PRIMARY KEY,
    project_path      TEXT NOT NULL,
    claude_project_dir TEXT NOT NULL,
    git_branch        TEXT,
    start_time        DATETIME,
    end_time          DATETIME,
    message_count     INTEGER NOT NULL DEFAULT 0,
    size_bytes        INTEGER NOT NULL DEFAULT 0,
    machine_id        TEXT NOT NULL,
    content_hash      TEXT NOT NULL,
    archived_at       DATETIME DEFAULT CURRENT_TIMESTAMP,
    raw_jsonl         BLOB NOT NULL
);
```

- `project_path` — best-known real project path: accurate `os.Getwd()` from hooks, mangled dir name from bulk import. Used for display and resume (cd target).
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

**`vault_fts`** — FTS5 virtual table with one row per turn. Uses `UNINDEXED` columns for metadata that is stored and returned but not tokenized. FTS5 virtual tables do not support foreign keys or `ON DELETE CASCADE` — FTS rows must be explicitly deleted in the same transaction when a session is replaced.

```sql
CREATE VIRTUAL TABLE vault_fts USING fts5(
    content_text,
    session_uuid UNINDEXED,
    subagent_id  UNINDEXED,
    turn_index   UNINDEXED,
    role         UNINDEXED,
    tokenize='porter unicode61'
);
```

**`vault_meta`** — key-value store for vault-level configuration (schema version, etc.).

```sql
CREATE TABLE vault_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

### Design Rationale

- **Separate DB from FTS knowledge store** — different retention policies (vault archives forever, FTS has tiered cleanup) and different scope (vault is global, knowledge store is per-project).
- **Raw JSONL as uncompressed BLOB** — application-level compression (zstd) is deferred to a future version. The current design accepts the storage cost for implementation simplicity — every read path (restore, show, TUI) would need decompression. See [Future Improvements](#future-improvements) for the compression roadmap.
- **No `vault_messages` table** — initially considered for message-level search and TUI display, but rejected. The TUI needs to parse `raw_jsonl` on the fly anyway for faithful rendering (thinking blocks, tool results, formatting). FTS5 `UNINDEXED` columns on the `vault_fts` table provide message-level search resolution (turn_index, role) without a separate table. This eliminates double-stored text, external content triggers, and cascade complexity.
- **FTS5 one-row-per-turn** — better for search granularity and `snippet()` performance than one-row-per-session. FTS5 handles 100K+ small rows trivially. Loading a 500-byte turn for snippet extraction is much faster than loading a 500KB session blob.
- **`subagent_id` uses empty string sentinel** — SQLite treats NULL as never equal to NULL in composite primary keys, which would break uniqueness constraints. Empty string `''` avoids this.
- **`ON DELETE CASCADE`** on `vault_files` — when a session is replaced (larger version wins), all associated files are automatically cleaned up within the same transaction. Note: CASCADE applies only to `vault_files`, not to `vault_fts` (FTS5 virtual tables don't support foreign keys — FTS rows require explicit `DELETE FROM vault_fts WHERE session_uuid = ?` in the same transaction).
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

Claude Code mangles project paths by replacing `/` and `.` with `-`. This is lossy (can't distinguish `/` from `.` in the original). Two columns track this:

- **`claude_project_dir`** — always the mangled directory name (e.g., `-home-sergio-Projects-personal-capy`). Always available from any import source. Used for restore (writing back to `~/.claude/projects/<claude_project_dir>/<uuid>.jsonl`).
- **`project_path`** — best-known real project path. From hooks: accurate `os.Getwd()`. From bulk import: the mangled dir name (same as `claude_project_dir`). Used for display and resume.

### Metadata Extraction

| Field | Source |
|-------|--------|
| `uuid` | Filename (minus `.jsonl` extension) |
| `project_path` | `os.Getwd()` from hooks; mangled dir name from bulk import |
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

For each turn, the scanner produces a `ScanResult`:

```
ScanResult {
    TurnIndex   int
    Role        string    // "user", "assistant", "system"
    SubagentID  string    // "" for main session
    ContentText string    // extracted, sanitized searchable text
    Timestamp   time.Time
}
```

One FTS row is inserted per `ScanResult`.

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

### Failure Mode

If the server startup sweep fails, it logs a warning. It must not block the MCP server or Claude Code. Sessions can always be recovered via `capy vault import`.

## CLI Commands

All commands are under `capy vault` and require `CAPY_VAULT_KEY` to be set.

| Command | Description |
|---------|-------------|
| `import [--path <dir>] [--project <filter>]` | Scan and archive sessions. Dry-run by default, `--force` to import. |
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

Default target: `~/.claude/projects/<claude_project_dir>/` (the original Claude Code location, always known). Override with `--output <path>`. Prompts before overwriting existing files.

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
- **View** (default on `show --tui`): vim-style navigation (j/k, g/G, / for in-viewer search). `--show-tools` and `--show-thinking` flags.

### Key Bindings

`q`/`Esc` back/quit, `/` search, `f` filter project, `r` restore, `c` copy message, `R` resume.

### Dependencies

bubbletea, bubbles (list, viewport, textinput), lipgloss (styling), glamour (markdown rendering).

### Package Structure

`internal/vault/tui/` with separate files per model (list, viewer, search) and a root `app.go` compositor. Imports `internal/vault/` for data access.

## Cross-Machine Merge

### Workflow

1. Copy `vault.db` from machine A → machine B
2. Run `capy vault import` on machine B
3. B's local sessions merge alongside A's sessions — no duplicates, no data loss

### Merge Behavior Per Table

| Table | Strategy |
|-------|----------|
| `vault_sessions` | UUID exists + same hash → skip. Different hash + incoming larger → replace. Different hash + incoming smaller → skip. New UUID → insert. |
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
3. **Session UUIDs are globally unique** — cross-machine merge relies on UUID collision being impossible at this scale.
4. **Claude Code session directory structure is stable** — the mangled-path convention and `~/.claude/projects/` location persist. Discovery also respects `CLAUDE_CONFIG_DIR` for non-default installations.

## Not Doing

- **Cloud sync** — vault is local-only; cross-machine transfer is manual file copy
- **Multi-user access** — single-user tool, no auth or sharing server
- **Codex session support** — different format and discovery paths; future work
- **Session diffing** — comparing versions of the same session across compaction events
- **Real-time watch mode** — no filesystem watcher; hook-driven + explicit import only
- **Automatic cleanup/retention** — vault archives forever; no TTL, no tiered retention
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
