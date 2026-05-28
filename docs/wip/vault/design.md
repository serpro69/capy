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

2. **Scanner layer** (`internal/vault/scanner.go`) — a single-pass JSONL reader that extracts searchable text from session files. Defines its own minimal JSON wire types (~20 lines), decoupled from `internal/session/parse.go`. Extracts: message text, tool names, tool input summaries (file paths, commands), subagent descriptions. Skips: base64, binary, system-reminder noise, progressive snapshot duplicates.

3. **Discovery layer** (`internal/vault/discovery.go`) — walks `~/.claude/projects/*/` to find all session JSONL files and subagent directories across all projects. Handles metadata extraction: project path, timestamps, message count, file size, content hash.

4. **CLI/TUI layer** (`cmd/capy/vault.go` + `internal/vault/tui/`) — cobra subcommand group (`capy vault <cmd>`). Each command supports a `--tui` flag that switches to an interactive bubbletea interface.

## Storage Model

### Database Location

- Default: `~/.config/capy/vault.db`
- Override: `CAPY_VAULT_PATH` environment variable
- Encryption: `CAPY_VAULT_KEY` environment variable (separate from `CAPY_DB_KEY`)

### Schema

Four tables: 3 real + 1 FTS5 virtual.

**`vault_sessions`** — active state, one row per session. Holds metadata and the raw JSONL blob for verbatim restore.

```sql
CREATE TABLE vault_sessions (
    uuid          TEXT PRIMARY KEY,
    project_path  TEXT NOT NULL,
    git_branch    TEXT,
    start_time    DATETIME,
    end_time      DATETIME,
    message_count INTEGER NOT NULL DEFAULT 0,
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    machine_id    TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    archived_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    raw_jsonl     BLOB NOT NULL
);
```

**`vault_subagents`** — one row per subagent file, linked to parent session via `ON DELETE CASCADE`. Stores raw subagent JSONL + metadata from `.meta.json`.

```sql
CREATE TABLE vault_subagents (
    session_uuid TEXT NOT NULL REFERENCES vault_sessions(uuid) ON DELETE CASCADE,
    agent_id     TEXT NOT NULL,
    agent_type   TEXT,
    description  TEXT,
    raw_jsonl    BLOB NOT NULL,
    PRIMARY KEY (session_uuid, agent_id)
);
```

**`vault_snapshots`** — append-only cold storage for pre-compaction content. The PreCompact hook writes here before compaction destroys content. No FTS index, no cascade.

```sql
CREATE TABLE vault_snapshots (
    uuid           TEXT NOT NULL,
    content_hash   TEXT NOT NULL,
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    trigger_reason TEXT NOT NULL,
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    raw_jsonl      BLOB NOT NULL,
    PRIMARY KEY (uuid, content_hash)
);
```

**`vault_fts`** — FTS5 virtual table with one row per turn. Uses `UNINDEXED` columns for metadata that is stored and returned but not tokenized.

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
- **Raw JSONL as uncompressed BLOB** — sqlcipher handles page-level compression. Application-level compression (zstd) adds complexity to every read/write path for marginal savings at typical session sizes (100KB-2MB).
- **No `vault_messages` table** — initially considered for message-level search and TUI display, but rejected. The TUI needs to parse `raw_jsonl` on the fly anyway for faithful rendering (thinking blocks, tool results, formatting). FTS5 `UNINDEXED` columns on the `vault_fts` table provide message-level search resolution (turn_index, role) without a separate table. This eliminates double-stored text, external content triggers, and cascade complexity.
- **`vault_snapshots` as cold storage** — separates "current state" from "historical preservation". PreCompact hook writes here, preventing search index pollution from multiple versions of the same session. The active `vault_sessions` table feeds FTS — clean, deduplicated, always reflects the latest state.
- **FTS5 one-row-per-turn** — better for search granularity and `snippet()` performance than one-row-per-session. FTS5 handles 100K+ small rows trivially. Loading a 500-byte turn for snippet extraction is much faster than loading a 500KB session blob.
- **`subagent_id` uses empty string sentinel** — SQLite treats NULL as never equal to NULL in composite primary keys, which would break uniqueness constraints. Empty string `''` avoids this.
- **`ON DELETE CASCADE`** on `vault_subagents` — when a session is replaced (larger version wins), its subagents are automatically cleaned up within the same transaction.

## Session Discovery & Import

### Discovery

`capy vault import` walks `~/.claude/projects/*/` to find all session JSONL files across all projects. For each mangled project directory, it discovers:
- `*.jsonl` files (main sessions)
- `<uuid>/subagents/agent-*.jsonl` directories (subagent files)
- `<uuid>/subagents/agent-*.meta.json` files (subagent metadata)

Supports `--path <dir>` for importing from custom locations (USB backup, copied `.claude/` directory from another machine).

### Project Path Handling

Claude Code mangles project paths by replacing `/` and `.` with `-`. This is lossy (can't distinguish `/` from `.` in the original). Two strategies:

- **Hook imports** (PreCompact, server startup): use `os.Getwd()` for the accurate, unmangled project path.
- **Bulk imports**: store the raw mangled directory name as-is. Unmangling happens at display time only (best-effort, never persisted as the unmangled form).

### Metadata Extraction

| Field | Source |
|-------|--------|
| `uuid` | Filename (minus `.jsonl` extension) |
| `project_path` | `os.Getwd()` from hooks; mangled dir name from bulk import |
| `start_time` / `end_time` | First and last JSONL line timestamps |
| `message_count` | Count of user + assistant entries |
| `size_bytes` | File size on disk |
| `content_hash` | SHA-256 of main session file only (subagent changes always touch the main file via tool_result) |
| `git_branch` | `git rev-parse --abbrev-ref HEAD` from hooks; NULL from bulk import |
| `machine_id` | From machine identity resolution (see Cross-Machine section) |

### Idempotent Import Logic

1. Compute `content_hash` of the file
2. If UUID exists with same hash → skip (already archived)
3. If UUID exists with different hash and incoming `size_bytes >= existing` → replace (session grew)
4. If UUID exists with different hash and incoming `size_bytes < existing` → skip (compacted, don't overwrite fuller archive)
5. If UUID doesn't exist → insert

Session replacement is transactional: delete old FTS rows → insert new session + subagents + FTS rows, all within one `sql.Tx`. `ON DELETE CASCADE` handles subagent cleanup automatically.

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
- Progressive snapshot duplicates — tracked via `seen` map keyed by `message.id` + block index
- JSON structural noise

### Output

For each turn, the scanner produces a `ScanResult`:

```
ScanResult {
    TurnIndex   int
    Role        string    // "user", "assistant", "system"
    SubagentID  string    // "" for main session
    ContentText string    // extracted searchable text
    Timestamp   time.Time
}
```

One FTS row is inserted per `ScanResult`.

### Performance

Single-pass, streaming, O(n) in file size. The `seen` map for dedup is bounded by unique message IDs (typically hundreds). No full parse into turn pairs — line-by-line extraction only.

## Hook Integration

### Archival Triggers

Three points at which sessions enter the vault:

1. **PreCompact hook** (immediate, critical path) — fires before `/compact` rewrites the session file. Short-lived `capy hook pre_compact` process. Writes to both `vault_snapshots` (cold storage) and `vault_sessions` (active state). This is the last chance to capture pre-compaction content.

2. **MCP server startup** (background, current project) — a background goroutine runs vault import for the current project's session directory on server boot. Same pattern as existing session sweep in `internal/session/sweep.go`. Captures sessions that ended since last boot. One-session delay is acceptable.

3. **Explicit `capy vault import`** (manual, all projects) — for onboarding, cross-machine scenarios, and catching anything the background sweep missed.

### Why Not SessionEnd Hooks

Session-end hooks were rejected for the existing session sweep (see `docs/done/sessionflow-rag/design.md`): hooks are short-lived processes where goroutines die on exit, and there's a risk of DB lock contention with the dying MCP server. The same reasoning applies to vault — the MCP server startup pattern is safer and proven.

### Hook Wiring

The hook router in `internal/hook/` switches on event type with a direct call:
- `PreCompact` → `vault.ArchiveSession(payload)`
- `PreToolUse` → existing logic

### Execution Context

Hooks execute inside the project directory, providing accurate metadata:
- `os.Getwd()` → accurate project path (no unmangling needed)
- `git rev-parse --abbrev-ref HEAD` → current branch

### Failure Mode

If the hook fails (vault DB locked, missing key, disk full), it logs a warning and exits non-zero. It must not block Claude Code's operation. The session can be recovered later via `capy vault import`.

## CLI Commands

All commands are under `capy vault` and require `CAPY_VAULT_KEY` to be set.

| Command | Description |
|---------|-------------|
| `import [--path <dir>] [--project <filter>]` | Scan and archive sessions. Dry-run by default, `--force` to import. |
| `search <query> [--project] [--after] [--before] [--role]` | FTS search with turn-level results and snippet context. |
| `list [--project <filter>] [--limit N]` | List sessions, reverse chronological. |
| `show <session_id>` | Display full session. Parses raw_jsonl on the fly. Partial UUID match. |
| `restore <session_id> [--output <path>]` | Write JSONL back to disk. Restores subagents too. |
| `resume <session_id>` | Restore + exec `claude --resume <session_id>`. |
| `stats` | DB size, session count, per-project breakdown. |

All commands support `--tui` flag for interactive bubbletea interface. Partial UUID matching (6-8 chars, git-style) on `show`, `restore`, `resume`.

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
| `vault_subagents` | Cascades with parent session via `ON DELETE CASCADE`. |
| `vault_snapshots` | `INSERT OR IGNORE` on `(uuid, content_hash)`. Snapshots from both machines accumulate. |
| `vault_fts` | Rebuilt as part of session insert/replace transactions. |
| `vault_meta` | Local-only, not merged. |

### Machine Identity

Machine identity is resolved outside the database to survive DB copies:

1. `CAPY_MACHINE_ID` env var → if set, use it (Docker/CI)
2. `~/.config/capy/machine-id` file → if exists, use it
3. Neither → generate UUIDv4, write atomically to file

Each machine tags its imports with its own stable ID. Copying vault.db doesn't carry the machine identity.

## Assumptions

1. **Claude Code JSONL format is stable** — the `type`/`message`/`content` structure won't break in incompatible ways. If it does, raw BLOBs are still restorable; only the scanner needs updating.
2. **PreCompact hook fires before file mutation** — vault reads the file after receiving the hook event but before Claude Code rewrites it.
3. **Session UUIDs are globally unique** — cross-machine merge relies on UUID collision being impossible at this scale.
4. **`~/.claude/projects/` directory structure is stable** — the mangled-path convention won't change across Claude Code versions.
5. **sqlcipher page-level compression is sufficient** — no application-level compression needed at typical session sizes (100KB-2MB).

## Not Doing

- **Cloud sync** — vault is local-only; cross-machine transfer is manual file copy
- **Multi-user access** — single-user tool, no auth or sharing server
- **Codex session support** — different format and discovery paths; future work
- **Session diffing** — comparing versions of the same session across compaction events
- **Real-time watch mode** — no filesystem watcher; hook-driven + explicit import only
- **Automatic cleanup/retention** — vault archives forever; no TTL, no tiered retention
- **Vault DB migration tooling** — schema is simple enough for inline migrations initially

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
