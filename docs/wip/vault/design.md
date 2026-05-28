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

A user runs `capy vault import`, and every session across all projects is archived verbatim. After Claude Code auto-cleans a 31-day-old session, the user can `show`, `restore`, `search`, or browse via TUI (`--tui`). Cross-machine: run `capy vault checkpoint` on machine A, copy `vault.db` to machine B (same `CAPY_VAULT_KEY` required), run `import` again. Copying `vault.db` **replaces** B's vault entirely (a `merge --from` command is deferred — see [Future Improvements](#future-improvements)), so "sessions from both machines coexist" holds only when either (a) B had no prior vault, or (b) B's own sessions still exist on disk and are re-archived by the post-copy `import`. A machine-ID mismatch warning (see [Cross-Machine Merge](#cross-machine-merge)) guards against silent overwrite of unarchived local sessions.

**Single-version archival:** The vault stores exactly one (latest-largest) version per session UUID. Re-importing a larger-but-divergent variant (a forked or independently-compacted copy that is not a strict superset) UPDATEs the row in place and discards the previous verbatim blob — this applies on the same machine too, not only cross-machine. Append-only history (`vault_snapshots`) is deferred — see [Future Improvements](#future-improvements).

**Data-loss guarantees:** Vault protects against cleanup (30-day auto-delete), accidental deletion, and project-scoping (sessions archived globally). Compaction is a known gap in v1 — if `/compact` runs before the next server startup sweep or manual `import`, the pre-compaction content is lost. PreCompact hook archival (see [Future Improvements](#future-improvements)) will close this gap in v2.

## Architecture Overview

Vault is a new `internal/vault/` package providing verbatim, cross-project session archival with full-text search. It operates a separate encrypted SQLite database (`vault.db`) independent of capy's per-project FTS knowledge store.

### Four Layers

1. **Storage layer** (`internal/vault/store.go`) — owns the SQLite connection lifecycle, schema, encryption (via `CAPY_VAULT_KEY`), and all CRUD operations. Imports `EncryptedDSN`, `URIEscapePassphrase`, and `URIEscapePath` from `internal/store/` (already exported). For corruption/passphrase recovery, the open + canary-classification logic is **extracted into a shared `internal/sqliteutil/` package** used by both `store` and `vault`. Exporting the four predicates alone (`IsSQLiteCorruption`, `BackupCorruptDB`, `IsWrongPassphrase`, `IsGarbageFile`) is **insufficient**: `IsWrongPassphrase` and the unencrypted-DB check only recognize the `*errWrongPassphrase`/`*errUnencryptedDB` types that are *constructed inside* `store.openDB()` (via the unexported `isUnencryptedDB` canary). A vault `openDB()` building its own errors would never be matched by an exported predicate, and it cannot construct `store`'s unexported error types. So the canary query, the corrupt / wrong-passphrase / unencrypted classification, **and** `backupCorruptDB` move to `sqliteutil` together (see [implementation prerequisite](./implementation.md#vault-key)). Manages its own connection pool, WAL checkpointing, and prepared statements.

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

- `project_path` — best-known real project path: accurate `os.Getwd()` from hooks, `cwd` field from JSONL during bulk import, `config.unmanglePath` filesystem-probe recovery from the mangled name, and the raw mangled name only as a last resort. Used for display and resume (cd target).
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
    line_index    UNINDEXED,
    role          UNINDEXED,
    tokenize='porter unicode61'
);
```

`line_index` is the **0-based line number of the originating message in `raw_jsonl`** (or in the subagent file when `subagent_id` is set). It is the stable anchor that ties an FTS hit to a viewer scroll position: the viewer's lazy line-indexer already builds a `\n`-offset table over the raw bytes, so it can jump straight to `line_index` without reproducing the scanner's dedup/turn-pairing logic. `turn_index`/`message_index` remain for ordering and display but are **scanner-internal** — they must not be used to drive viewer scrolling, because the viewer is a separate parser that does not dedup progressive snapshots (see [Search→view anchoring](#design-rationale)). For deduplicated assistant snapshots that span multiple JSONL lines, `line_index` is the line of the first (canonical) snapshot.

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
- **`title` column on `vault_sessions`** — populated from `ai-title` JSONL entries (Claude's auto-generated session summaries; emitted repeatedly as the title is refined, so the **last** `ai-title` wins). Measured: 136/223 sampled sessions carry at least one `ai-title`; the other 87 (39%) fall back to the first *significant* user message, truncated to 120 chars. "Significant" follows cc-sessions' guard — a `user` entry whose content is a plain string (not a `tool_result` array) and does not start with `<` (excludes `<system-reminder>`/`<command-name>` noise); without this guard a large fraction of fallback titles would be tool output or internal tags. A user-set custom title override is **not sourced from the JSONL**: no `custom-title` line type or `customTitle` field appears in sampled data (0/223), and Claude Code appears to persist renames outside the session file (e.g. under `~/.claude/sessions/`). The custom-title tier is therefore **deferred** until that store is verified (see [Assumptions](#assumptions)). Essential for usable `list` output and TUI browsing — without a title, sessions are indistinguishable walls of UUIDs.
- **No `vault_messages` table** — initially considered for message-level search and TUI display, but rejected. The TUI needs to parse `raw_jsonl` on the fly anyway for faithful rendering (thinking blocks, tool results, formatting). FTS5 `UNINDEXED` columns on the `vault_fts` table provide message-level search resolution (`role`, `turn_index`, `message_index`) without a separate table. This eliminates double-stored text, external content triggers, and cascade complexity. **Search→view anchoring:** the FTS scanner and the TUI viewer are two independent parsers — the scanner dedups progressive assistant snapshots and assigns turn indices; the viewer does lazy line-indexing without dedup — so a scanner-only `turn_index` has no reliable mapping to a viewer scroll position. The `line_index` UNINDEXED column (the message's line number in `raw_jsonl`) is the shared anchor both sides agree on: "Enter scrolls to the match" resolves `line_index` directly against the viewer's `\n`-offset table, not `turn_index`.
- **FTS5 one-row-per-message** — each user message and each assistant message is a separate FTS row, with the `role` UNINDEXED column distinguishing them. This enables `--role` filtering on search and produces focused `snippet()` results. Better granularity than one-row-per-session or one-row-per-turn-pair. FTS5 handles 100K+ small rows trivially.
- **`subagent_id` uses empty string sentinel** — SQLite treats NULL as never equal to NULL in composite primary keys, which would break uniqueness constraints. Empty string `''` avoids this.
- **`ON DELETE CASCADE`** on `vault_files` and `vault_session_locations` — when a session is deleted (e.g., `capy vault delete`), all associated files, locations, and FTS rows are cleaned up. Note: CASCADE applies only to regular tables; FTS5 virtual tables don't support foreign keys, so FTS rows require explicit `DELETE FROM vault_fts WHERE session_uuid = ?` in the same transaction. **Session replacement does not use CASCADE** — it uses `UPDATE` on the session row to preserve existing locations, and explicitly rebuilds `vault_files` and `vault_fts` only.
- **`vault_files` instead of `vault_subagents`** — a generic files table preserves the session directory (subagent JSONLs, meta.json, tool-results, and any future sidecar types), not just known file types. Preservation is **near-complete, not absolute**: the main `raw_jsonl` is always stored uncapped, subagent `*.jsonl` files (irreproducible conversation content) are always stored, and only large *reproducible* sidecars (non-subagent files > 5 MB — build logs, screenshots) are skipped with a warning (see [DB Size Projection](#db-size-projection)). Subagent metadata (agent_type, description) is read from the stored `meta.json` files at display time.
- **FTS content is sanitized** — `sanitize.StripSecrets()` runs on scanner output before FTS insertion. This prevents secrets from appearing in search snippet results. The `raw_jsonl` blob and `vault_files` content remain unsanitized — verbatim preservation is the point.
- **Snapshots deferred** — `vault_snapshots` (append-only cold storage for pre-compaction content) is deferred to a future version. The PreCompact hook payload format is unverified (see [Assumption #2](#assumptions)), and the vault works without snapshots — `import` and server-startup sweep are the primary archival paths. See [Future Improvements](#future-improvements).

## Session Discovery & Import

### Discovery

`capy vault import` discovers sessions by walking the Claude Code projects directory. The base path is resolved via the **existing** `config.ClaudeProjectsDir()` helper (`internal/config/paths.go`), **extended to honor `CLAUDE_CONFIG_DIR`** (it currently hardcodes `~/.claude/projects/`):
1. `CLAUDE_CONFIG_DIR` env var → use `$CLAUDE_CONFIG_DIR/projects/`
2. Default: `~/.claude/projects/`

Discovery, restore, and resume all call this one helper — do **not** add a second copy in `internal/vault/`. Extending the existing helper also corrects `internal/session/sweep.go:SessionDir()` (same code path) for free (see [Known Asymmetries](#known-asymmetries)).

For each mangled project directory, it discovers:
- `*.jsonl` files (main sessions)
- `<uuid>/` directories containing all session files (subagents, tool-results, any other sidecars)

`--path <dir>` supports multiple input shapes: a full Claude config dir (containing `projects/`), a `projects/` root directly, a single mangled project directory, or a directory containing loose JSONL files. Discovery auto-detects the input type by checking for `projects/` subdirectory or `*.jsonl` files at the given path.

### Project Path Handling

Claude Code mangles project paths by replacing `/` and `.` with `-`. This is lossy (can't distinguish `/` from `.` in the original). Two columns on `vault_session_locations` track this:

- **`claude_project_dir`** — always the mangled directory name (e.g., `-home-sergio-Projects-personal-capy`). Always available from any import source. Used for restore (writing back to `~/.claude/projects/<claude_project_dir>/<uuid>.jsonl`).
- **`project_path`** — best-known real project path. From hooks: accurate `os.Getwd()`. From bulk import: the `cwd` field in the JSONL (first user entry that has it); if absent, recover from the mangled dir name via the existing `config.unmanglePath`/`unmangledProbe` filesystem probe (`internal/config/paths.go`), storing the raw mangled name only as a last resort. Used for display and resume.

### Metadata Extraction

| Field | Source |
|-------|--------|
| `uuid` | Filename (minus `.jsonl` extension) |
| `title` | Last `aiTitle` from `ai-title` entries (emitted progressively — last wins). Fallback: first *significant* user message — string content, not a `tool_result` array, not `<…>`-prefixed — truncated to 120 chars. `customTitle`/`custom-title` override is deferred (absent from JSONL; see [title rationale](#design-rationale)) |
| `project_path` | `os.Getwd()` from hooks; `cwd` field from first user JSONL line during bulk import; `config.unmanglePath` filesystem-probe recovery from the mangled name; raw mangled name as last resort |
| `claude_project_dir` | Mangled directory name (always available) |
| `start_time` / `end_time` | First and last JSONL line timestamps |
| `message_count` | Count of human-text `user` turns + `assistant` turns. **Excludes `tool_result`-only `user` entries** — ≈86% of `user` lines (10,356 / 12,005 sampled) are tool output, not messages — so this reflects conversational length, not raw line count |
| `size_bytes` | **Total** byte size of all hashed content (main JSONL + associated files), matching `content_hash`'s scope. Used for display/stats **and** as the replace tiebreaker (not main-JSONL-only — a shrinking main with an added sidecar must not read as "smaller") |
| `content_hash` | SHA-256 composite with framing: for each file (main JSONL keyed as `<uuid>.jsonl`, associated files keyed by their relative path, all sorted by key), hash `len(key) || key || len(content) || content`. The length-prefix framing prevents boundary-equivalent file sets from colliding |
| `git_branch` | `gitBranch` field from the first user JSONL entry (always present on user-type lines). NULL if absent. Sessions may span branch switches — only the initial branch is recorded |
| `machine_id` | From machine identity resolution (see Cross-Machine section) |

### Idempotent Import Logic

`size_bytes` below is the **total** hashed content size (main JSONL + all associated files), not the main file alone — the tiebreaker must cover the same byte set as `content_hash`, otherwise a shrinking main JSONL with an added/grown sidecar (different hash, smaller main) is wrongly skipped and the new sidecar content is lost.

1. Compute composite `content_hash` of the session using framed hashing (see Metadata Extraction), and `size_bytes` = sum of byte lengths of the same file set
2. If UUID exists with same hash → skip (already archived)
3. If UUID exists with different hash and incoming `size_bytes >= existing` → replace (total content grew)
4. If UUID exists with different hash and incoming `size_bytes < existing` → skip (likely compacted; don't overwrite the fuller archive)
5. If UUID doesn't exist → insert

**Single-version semantics:** replacement UPDATEs the row in place (preserving locations) and discards the previous `raw_jsonl`. A larger-but-divergent variant (forked/edited, not a strict superset) overwrites the prior blob — same machine as well as cross-machine. Append-only history (`vault_snapshots`) is deferred; see [Future Improvements](#future-improvements).

**Known limitation:** Cross-machine merge after independent compaction may lose content. If machine A has a 1MB pre-compaction version and machine B has a 0.6MB independently-compacted version, importing B's vault into A would skip B's version (smaller). If B's version had unique post-compaction content, it's lost. This is an accepted trade-off of the single-version-per-session model.

**Session replacement preserves locations.** Replacement is transactional but avoids `ON DELETE CASCADE` for `vault_session_locations`: (1) `UPDATE vault_sessions SET ... WHERE uuid = ?` (updates metadata, raw_jsonl, content_hash — no DELETE trigger), (2) `DELETE FROM vault_files WHERE session_uuid = ?` + re-insert files, (3) `DELETE FROM vault_fts WHERE session_uuid = ?` + re-insert FTS rows. Locations are preserved across replacements because the session row is updated, not deleted and re-inserted. New locations are upserted separately.

### Transaction Batching

- **Bulk imports**: batch ~50 sessions OR ~100MB total `raw_jsonl` per transaction (whichever limit hits first). Acquire the write lock eagerly per batch via the store's `beginImmediate` idiom (RESERVED lock + `SQLITE_BUSY` exponential-backoff retry, mirroring `internal/store/migrate.go`) so a concurrent writer (e.g. the server startup sweep) doesn't fail the batch outright. On any batch tx error, log it, then retry each session in the batch individually. On individual session failure, log and continue — don't abort the entire import.
- **Hook imports / server sweep**: per-session transactions (single session at a time), using the same `beginImmediate` acquisition.

## FTS Scanner

The scanner is a single-pass JSONL reader in `internal/vault/scanner.go` that extracts searchable text for the FTS5 index. It defines its own minimal JSON wire types decoupled from `internal/session/parse.go`. The scanner accepts `io.Reader` (not just file paths) so it works for both import-from-disk and render-from-BLOB scenarios (TUI viewer parses from `vault_files` BLOBs).

### JSONL Line Types

Each line type gets an explicit handling decision. **Default for any type not listed below (including new or unknown types): skip** — unknown types never block import, and `raw_jsonl` preserves them verbatim regardless. Counts are from a 223-session sample (~43k lines); indicative, not contractual.

| Line type | Action | Notes |
|-----------|--------|-------|
| `user` | **Extract** | Human text from string/`text` blocks (also capture `cwd`, `gitBranch`) **and** bounded `tool_result` text. `tool_result` blocks live in `user` entries (not `assistant`); ≈86% of `user` lines are `tool_result`-only. Tag `tool_result`-derived rows `role="tool"`, human text `role="user"` — see [Output](#output) |
| `assistant` | **Extract** | Text + `tool_use` names/input summaries; deduplicate progressive snapshots. Blocks are `text`/`tool_use`/`thinking` — **never `tool_result`** (0 in sample) |
| `ai-title` | **Extract** | `aiTitle` field → session title. Emitted repeatedly (progressive); keep the **last**. Present in 136/223 sessions |
| `pr-link` | **Extract** | `prUrl` / `prRepository` / `prNumber` → searchable ("which session opened PR #N"). 11 in sample |
| `system` (subtype `away_summary`) | **Extract** | Summary text |
| `attachment` | **Extract** | Attachment filename for search (from content blocks) |
| `custom-title` | **Skip (deferred)** | `customTitle` override — **0 in sample**; not sourced from the JSONL (see [title rationale](#design-rationale)). Revisit if the rename store is verified |
| `system` (subtype `turn_duration`) | **Skip** | Timing metadata |
| `system` (subtype `local_command`) | **Skip** | Local-command meta (81 in sample) |
| `system` (subtype `informational`) | **Skip** | Informational notice (1 in sample) |
| `agent-name` | **Skip** | Subagent display name (47); subagent content is indexed from `subagents/*.jsonl` instead |
| `permission-mode` | **Skip** | Session config |
| `file-history-snapshot` | **Skip** | Undo snapshots |
| `last-prompt` | **Skip** | Duplicate of user message |
| `queue-operation` | **Skip** | Internal scheduling |
| `progress` | **Skip (unverified)** | Streaming progress — **0 in sample**; version-dependent |

### Extraction Scope

**Extracted:**
1. All `"text"` values from content blocks (human `user` text and `assistant` text)
2. Tool use names (Bash, Read, Edit, etc.) from `assistant` entries
3. Tool input summaries — file paths from Read/Edit, commands from Bash
4. Bounded `tool_result` text — these blocks appear in **`user`** entries (the turn after a `tool_use`), **not** `assistant`. Cap at 16KB per result, keeping **75% head + 25% tail** on a char boundary (matching `claude-history`'s `truncate_for_search`, `claude.rs:147,222`). Emitted as a separate `role="tool"` FTS row so `--role user` stays human-only. Tool results carry searchable content (error messages, grep output, build logs) users commonly search for
5. Subagent descriptions and types
6. `aiTitle` (last value) for the session title; significant-first-user-message fallback (see [title rationale](#design-rationale))
7. Attachment filenames
8. `pr-link` fields (`prUrl`, `prRepository`, `prNumber`)

**Skipped:**
- Base64 and binary content (including image blocks in tool_result)
- System-reminder tags and their contents
- `thinking` blocks (assistant reasoning — not a search signal)
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
    LineIndex    int       // 0-based line number in the source JSONL (raw_jsonl, or the subagent file) — stable anchor for view jumping
    Role         string    // "user" (human text) | "assistant" | "tool" (tool_result output) | "system" (away_summary)
    SubagentID   string    // "" for main session
    ContentText  string    // extracted, sanitized searchable text
    Timestamp    time.Time
}
```

One FTS row is inserted per `ScanResult` (its `LineIndex` populates `vault_fts.line_index`). Each human `user` message, each `assistant` message, and each `tool_result` produces a separate `ScanResult` with the appropriate `Role` — `role="tool"` keeps tool output out of `--role user` results. This enables meaningful `--role` filtering and produces focused `snippet()` results per message rather than per turn-pair. For a deduplicated assistant snapshot spanning multiple JSONL lines, `LineIndex` is the first (canonical) line.

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

The MCP server startup sweep is wired in `internal/server/server.go`, after the existing `session.Sweep()` call, in the same `bgWg`-tracked goroutine pattern. Vault sweep is opt-in: silently skipped if `CAPY_VAULT_KEY` is not set.

**VaultStore lifecycle:** the sweep goroutine **opens its own `VaultStore` and `Close()`s it when the sweep finishes** (inside the `bgWg`-tracked goroutine, so it completes before `shutdown()` proceeds). This is deliberate — `server.shutdown()` closes only the knowledge `ContentStore`, and `VaultStore.Close()` is what runs the WAL checkpoint (close pool → `wal_checkpoint(TRUNCATE)`). A bounded open/close keeps `vault.db-wal` flushed without making `Server` own a long-lived vault handle (the sweep is a one-shot at boot, not a per-request dependency).

**Concurrency:** a manual `capy vault import` may run while the server holds a `VaultStore` open — both write the same `vault.db` and contend on the WAL. This is tolerable: `busy_timeout=5000` plus per-batch `beginImmediate` retry (see [Transaction Batching](#transaction-batching)) absorbs transient `SQLITE_BUSY`. There is no cross-process lock; the design assumes a single primary writer at a time and treats overlap as a rare, recoverable case.

### Execution Context

Hooks and server startup execute inside the project directory, providing accurate metadata:
- `os.Getwd()` → accurate project path (no unmangling needed)
- `gitBranch` field from JSONL user entries → current branch (no need to shell out to `git`; the field is present on every user-type line)

### Scope Limitation

The MCP server startup sweep is scoped to the current project's session directory only. Sessions from other projects are NOT archived by this path. If a user works exclusively on project A, sessions from project B will age past Claude Code's 30-day cleanup without being archived. The gap is closed by periodic `capy vault import` (e.g., via cron or manual habit). The `import` command's help text and its first-run output should advise running `capy vault import` periodically or via a cron job. (There is no `capy vault setup` subcommand — vault has no separate setup step; the only prerequisite is `CAPY_VAULT_KEY`.)

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
- Resolve the restore root with `filepath.EvalSymlinks` first, so a symlinked component of the root cannot redirect writes outside it
- Verify `filepath.Join(restoreRoot, uuid, relativePath)` resolves to a path under the (resolved) restore root (containment check via `filepath.Rel`)
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
- **Right panel** (`bubbles/viewport`): session content viewer. Parses `raw_jsonl` on the fly with Human/Assistant formatting and lipgloss role-based styling. Rich markdown/syntax rendering (glamour) is deferred — see [Dependencies](#dependencies).
- **Bottom bar**: mode indicator, keybindings, filter state.

### Modes

- **Browse** (default on `list --tui`): navigate sessions, preview on selection, Enter for full viewer.
- **Search** (default on `search --tui`): live search input, debounced FTS5 queries, snippet highlights. Enter scrolls the viewer to the match's `line_index` (resolved directly against the viewer's `\n`-offset table — not `turn_index`; see [No `vault_messages` table](#design-rationale)).
- **View** (default on `show --tui`): vim-style navigation (j/k, g/G, / for in-viewer search). `--show-tools` and `--show-thinking` flags. The viewer uses lazy line-indexing: holds the raw `[]byte` plus a `\n`-offset index, and only unmarshals lines in the visible viewport. When subagent files exist in `vault_files`, the viewer fetches and parses them inline with visual distinction (dimmed, prefixed — matching `claude-history`'s approach). Non-JSONL vault_files (tool-results, meta.json, other sidecars) are archive-only — preserved for `restore` but not rendered in `show` or the TUI viewer.

### Key Bindings

`q`/`Esc` back/quit, `/` search, `f` filter project, `r` restore, `c` copy message, `R` resume.

### Dependencies

bubbletea, bubbles (list, viewport, textinput), lipgloss (styling).

**Glamour (markdown rendering) is excluded from v1.** capy's current dependency tree is intentionally small (5 direct modules: mcp-go, go-sqlite3, go-toml, cobra, testify). `glamour` transitively pulls in `chroma` + `goldmark` plus many syntax lexers, materially increasing binary size and supply-chain surface — at odds with the lean-binary identity the inspiration tools advertise. v1 renders with lipgloss styling only (role coloring, dimmed subagent prefixes); rich markdown/syntax highlighting via glamour is a [Future Improvement](#future-improvements). **(Reversible decision — flagged for confirmation.)**

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

**Per-file size cap:** non-subagent `vault_files` entries larger than 5 MB are skipped with a warning to stderr. Large tool-results (multi-MB build logs, screenshots) are reproducible — the conversation JSONL is the critical artifact. **The cap never applies to the main `raw_jsonl` (always stored uncapped) nor to `subagents/*.jsonl` files (irreproducible conversation content) — only to reproducible sidecars.** This prevents degenerate DB growth from outlier files without ever dropping conversation content.

**No automatic cleanup:** Vault archives forever by design. Users who need to shed space can use `capy vault delete` to remove individual sessions.

## Assumptions

1. **Claude Code JSONL format is stable** — the `type`/`message`/`content` structure won't break in incompatible ways. If it does, raw BLOBs are still restorable; only the scanner needs updating.
2. **PreCompact hook fires before file mutation** — UNVERIFIED. The `handlePreCompact` handler already receives the full payload via `input []byte` but the payload format is undocumented. This assumption must be validated before implementing PreCompact archival. The vault's core functionality (import + server-startup sweep) does not depend on this assumption.
3. **Session UUIDs are globally unique** — cross-machine merge relies on UUID collision being impossible at this scale. Cross-project forking creates the same UUID in multiple project directories — vault handles this via `vault_session_locations` (content stored once, locations tracked separately).
4. **Claude Code session directory structure is stable** — the mangled-path convention and `~/.claude/projects/` location persist. Discovery also respects `CLAUDE_CONFIG_DIR` for non-default installations.
5. **JSONL field / line-type shape — verified against a 223-session sample (~43k lines), May 2026.**
   - `cwd` and `gitBranch` are present on `user`-type lines (used for project path + branch metadata).
   - `tool_result` blocks appear **only in `user` entries** (10,361 user lines; **0** assistant lines). ≈86% of `user` lines are `tool_result`-only.
   - `ai-title` is present in **136/223** sessions (the other 87 use the guarded first-user-message fallback); emitted progressively (2,348 lines, last wins) with shape `{aiTitle, sessionId, type}` (no timestamp).
   - Also present: `attachment`, `agent-name`, `pr-link`, and system subtypes `away_summary` / `turn_duration` / `local_command` / `informational`.
   - **Absent from the sample (treat as unverified / version-dependent):** `custom-title` and any structured `customTitle` field (**0** — the custom-title title tier is deferred; renamed titles appear to live outside the JSONL, likely under `~/.claude/sessions/`), and `progress` (**0**).
   - Counts are indicative, not contractual. The scanner skips unknown types by default, so divergence from this sample is non-fatal; only the scanner needs updating if the format shifts (raw BLOBs remain restorable regardless).

### Known Asymmetries

- **`CLAUDE_CONFIG_DIR` handling:** Two existing helpers hardcode `~/.claude/projects/` and ignore `CLAUDE_CONFIG_DIR`: `config.ClaudeProjectsDir()` (`internal/config/paths.go:62`, used by `ResolveSourceProject`) and `internal/session/sweep.go:SessionDir()`. Vault must honor `CLAUDE_CONFIG_DIR`, so the fix is to **extend `config.ClaudeProjectsDir()` in place** (not add a third helper in `internal/vault/`) and route both vault and `SessionDir()` through it. Until that lands, vault would archive sessions from non-default Claude installations that the session sweep never indexed. Tracked as a follow-up in tasks.md.

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

### Security & Key Lifecycle

v1's security posture is **encryption-at-rest only** (`CAPY_VAULT_KEY` via sqlite3mc/SQLCipher, same stack as the knowledge store). Two items are explicitly deferred, not solved, in v1:

- **Verbatim-secret concentration (accepted for v1).** `raw_jsonl` and `vault_files` are stored unsanitized — only FTS text passes through `sanitize.StripSecrets` — so `vault.db` concentrates every secret/credential/PII that appeared in any archived session, and `restore` writes them back as plaintext files. This is accepted because the same data already exists in plaintext on the same host under `~/.claude/projects/`: the vault mirrors that local data (encrypted), it does not widen the local attack surface. The erasure tool is `capy vault delete`; a redacted-export path is deferred (see [Sharing/Export Pipeline](#sharingexport-pipeline)).
- **Key rotation (`capy vault rekey`).** No rekey path exists in v1. A forever-archive copied across machines should be able to rotate a compromised `CAPY_VAULT_KEY`. The implementation would reuse the knowledge store's rekey machinery (ADR-020: switch to DELETE journal mode before `PRAGMA rekey`, since WAL is incompatible with rekey), exposed as `capy vault rekey`. Until then, the only migration path for a compromised key is decrypt-and-reimport into a freshly-keyed vault.

### TUI Markdown Rendering (glamour)

v1 renders session content with lipgloss styling only (role coloring, dimmed subagent prefixes) and deliberately excludes `glamour` to keep the binary lean (see [TUI Dependencies](#dependencies)). A future version can add `glamour` for rich markdown + syntax-highlighted code in the viewer — ideally behind a build tag so the lean default is preserved — after benchmarking binary-size and startup-cost impact.

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
