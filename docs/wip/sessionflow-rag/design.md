# Sessionflow RAG — Design Document

> **Issue:** [#24](https://github.com/serpro69/capy/issues/24)
> **Status:** Draft
> **Created:** 2026-04-23

## Problem

Claude Code stores conversation sessions as JSONL files in `~/.claude/projects/<project-dir-name>/`. These sessions contain valuable decisions, rationale, and context that is lost after conversations end. capy already solves context flooding and provides persistent queryable memory, but only for content explicitly indexed during a session — the conversations themselves are invisible to future searches.

## Solution

Sessionflow RAG makes past sessions automatically searchable by indexing their human/assistant text content into capy's FTS5 knowledge base. A background sweep runs at MCP server startup, parsing session JSONL files, filtering out noise, chunking the conversational text into BM25-friendly windows, and indexing them under a new `KindSession` source kind.

The feature is entirely automatic — no MCP tool is exposed, the assistant never needs to think about it, and the indexed content stays portable across machines by using machine-agnostic labels (session UUID + datetime, not file paths).

## Design Decisions

### Source Kind: `KindSession`

A new source kind alongside `KindDurable` and `KindEphemeral`. Sessions have a distinct lifecycle: they are not transient command output (ephemeral) nor explicitly curated knowledge (durable). They represent conversational history with a natural decay curve measured in weeks/months.

- **Cleanup:** Strict TTL (default 60 days, configurable via `session_ttl_days`), same mechanism as ephemeral. Access count is ignored — sessions decay by age, not usage frequency.
- **Search:** Included in default search results alongside durable content. Users can exclude via `include_kinds: ["durable"]`.
- **Stats:** Reported separately with fresh/stale TTL bucketing.

### Indexing Trigger: MCP Server Startup

Indexing runs as a background goroutine fired during `Server.Serve()`. This was chosen over alternatives:

- **Session-end hook** — rejected. The hook is a short-lived process; goroutines die when it exits. Also risks DB lock contention with the dying MCP server (see `internal/hook/sessionend.go` WAL warning).
- **Session-start hook** — rejected for the same lifecycle reason (short-lived process).
- **MCP server startup** — chosen. The server is a long-lived process with safe DB access. A background goroutine doesn't block Claude Code's UI. The 1-2 second delay before the previous session becomes searchable is invisible.

Trade-off: one-session delay before content is searchable. Acceptable because you never search the current session.

### Session Directory Discovery

Claude Code stores sessions in `~/.claude/projects/<dir-name>/` where `<dir-name>` is the absolute project path with `/` and `.` replaced by `-`. Example: `/home/sergio/Projects/personal/capy` → `-home-sergio-Projects-personal-capy`.

The mangling function: `strings.NewReplacer("/", "-", ".", "-").Replace(absProjectDir)`

This is based on observed behavior, not official documentation. The implementation should fail gracefully if the derived directory does not exist (log warning, skip session indexing).

### Labels: Machine-Agnostic

Labels use the format `session:<ISO-datetime>:<session-UUID>`. Example:
```
session:2026-04-05T12:06:26Z:102ad512-759a-43ad-8805-353ce341f65c
```

- **Datetime** comes from the first user message timestamp in the JSONL.
- **UUID** is the session filename (minus `.jsonl` extension).
- Datetime-first enables chronological sorting via `source: "session:"` filter.
- UUID ensures uniqueness across machines.
- No file paths in labels — the knowledge.db remains portable across machines and OS.

### Sweep Algorithm

Runs once per server boot in a background goroutine with 30s timeout:

1. Derive session directory from `projectDir` via path mangling.
2. List all `.jsonl` files.
3. Query existing `session:` sources from the store, build `uuid → indexed_at` map (extract UUID by splitting label on `:`).
4. For each file: compare `max(file.mtime, subagents_dir.mtime)` against `indexed_at`. Skip if mtime <= indexed_at.
5. Parse JSONL, apply filtering, build transcript, apply session-level gate.
6. For sessions with `<uuid>/subagents/` directory: parse sub-agent JSONL files, read `meta.json`, append to parent transcript.
7. Compute content_hash of filtered transcript.
8. Call `Index()` with label, content type `"session"`, kind `KindSession`.
9. Existing content_hash dedup handles unchanged-but-touched files.

Error handling: individual parse failures are logged and skipped. One corrupt file does not block the sweep.

## Session File Format

Claude Code JSONL files contain one JSON object per line. Each has a `type` field. The complete taxonomy (from analysis of 96 sessions across 2 active projects):

### Message Types

| Type | Keep/Skip | Reason |
|------|-----------|--------|
| `user` (string content) | **KEEP** | Direct human input |
| `user` (text blocks in array) | **KEEP** | Human text; strip `<system-reminder>` tags |
| `user` (tool_result blocks only) | **SKIP** | Automatic SDK returns, no human content |
| `assistant` text blocks | **KEEP** | Core conversational content |
| `assistant` thinking blocks | **SKIP** | Content stripped by Claude Code, only base64 signature remains |
| `assistant` tool_use blocks | **EXTRACT name** | Tool names go in chunk title for BM25 boost |
| `system/away_summary` | **KEEP** | Dense session recap, high signal |
| `system/turn_duration` | **SKIP** | Timing metadata |
| `system/local_command` | **SKIP** | Slash command stdout |
| `attachment` (all 13 subtypes) | **SKIP** | Infrastructure metadata |
| `file-history-snapshot` | **SKIP** | File backup metadata |
| `permission-mode` | **SKIP** | Startup metadata |
| `progress` | **SKIP** | Progress indicators |
| `queue-operation` | **SKIP** | Queue metadata |
| `custom-title` | **SKIP** | Session label |
| `agent-name` | **SKIP** | Session label |
| `last-prompt` | **SKIP** | UI state |

### Content Block Types

**User messages:**
- `message.content` is either a string (19% of messages) or an array (80%).
- Array blocks: `tool_result` (skip), `text` (keep — but check for `<system-reminder>` tags).
- `toolUseResult` top-level field: structured tool output annotation. Always skip.

**Assistant messages:**
- `message.content` is always an array.
- Block types: `text` (keep), `tool_use` (extract `name` field only), `thinking` (skip — content is empty string, only `signature` field has data).
- Tool name path: `message.content[i].name` where `message.content[i].type == "tool_use"`.
- Exactly 1 `tool_use` per assistant message (observed invariant).

### Sub-Agent Conversations

Sessions that spawned sub-agents have a bare directory `<uuid>/` alongside the `.jsonl` file, containing:
- `subagents/agent-<hex-id>.jsonl` — full conversation transcript (same JSONL schema, `isSidechain: true`).
- `subagents/agent-<hex-id>.meta.json` — `{"agentType": "Explore", "description": "..."}`.
- `tool-results/toolu_<id>.json` — offloaded large tool results (rare). Skip these.

Sub-agent conversations are parsed and appended to the parent transcript with delimiters:
```
--- Subagent: Explore — "Explore setup command implementation" ---
Human: ...
Assistant: ...
--- End subagent ---
```

## Filtering

### Per-Message Filtering

1. **Skip** user messages where all content blocks are `tool_result` (no `text` blocks).
2. **Skip** user messages starting with `/` (slash commands — defensive, not observed in stored JSONL).
3. **Strip** `<system-reminder>...</system-reminder>` tags from text content.
4. **Skip** `thinking` blocks entirely (content is stripped/empty).
5. **Extract** tool `name` from `tool_use` blocks for metadata; do not include block content.
6. **Keep** `system/away_summary` content as inline session summaries.
7. **Skip** all other non-user/non-assistant types.

### Session-Level Gate

After filtering, a session must pass:
- **Minimum 2 meaningful turn pairs** (human text + assistant text response).
- **Minimum 200 characters of total assistant text**.

Sessions that fail are skipped entirely. Based on analysis: 46% of sessions (44/96) would be filtered out — config-only sessions, aborted conversations, tool-only interactions.

## Chunking

### Transcript Format

Filtered messages are converted to plaintext transcript:

```
Human: <user text>
Assistant: <assistant text>
[Tools: Read, Edit, mcp__capy__capy_search]

[Session summary: <away_summary text>]

--- Subagent: Explore — "Explore setup command implementation" ---
Human: <subagent user text>
Assistant: <subagent assistant text>
--- End subagent ---
```

Tool-result-only user messages are omitted from the transcript entirely. The `[Tools: ...]` line appears only when the assistant message contained `tool_use` blocks. Tool names are kept as-is (no normalization of MCP tool names).

### Sliding Window

Chunking uses a sliding window of ~4 turn pairs with 1-pair overlap, adapted from `chunkPlainText` in `internal/store/chunk.go`. The split happens on turn boundaries (the `Human:` prefix), not arbitrary line counts.

- **Window size:** ~4 turn pairs. Preserves enough conversational context for BM25 to match multi-turn decisions.
- **Overlap:** 1 turn pair. If a decision spans two chunks, the overlapping turn appears in both.
- **Oversized chunks:** Split at paragraph boundaries using existing `splitOversized` logic when exceeding `maxChunkBytes` (4096).

### Chunk Titles

Format: `Session 2026-04-05T12:06:26Z | Turns 3-6 | Tools: Read, Edit`

When the window contains sub-agent turns: `Session 2026-04-05T12:06:26Z | Turns 3-6 | Subagent: Explore | Tools: Read, Edit`

The title exploits the BM25 title-weight boost in `internal/store/search.go` for targeted retrieval.

### Content Type

Chunks use content type `"session"` — a new value added to the taxonomy alongside `"code"`, `"prose"`, `"plaintext"`, `"markdown"`, `"json"`.

## Storage

### Schema Migration

The `sources` table CHECK constraint changes:
```sql
-- Before
CHECK (kind IN ('ephemeral', 'durable'))
-- After
CHECK (kind IN ('ephemeral', 'durable', 'session'))
```

Added via `applyMigrations` in `internal/store/migrate.go`, following the existing migration pattern.

### Types

- `KindSession SourceKind = "session"` added to `internal/store/types.go`.
- `Valid()` updated to accept `KindSession`.
- `"session"` added as a content type value.

### Cleanup

Sessions use strict TTL, same mechanism as ephemeral. `cleanupEphemeral` is generalized to `cleanupByTTL(kind SourceKind, ttl time.Duration)` and called twice from `Cleanup()`: once for ephemeral (24h), once for session (60 days).

### Config

`CleanupConfig` gains:
```go
SessionTTLDays int `toml:"session_ttl_days"` // default: 60, minimum: 1
```

### Search

Default `IncludeKinds` changes from `[KindDurable]` to `[KindDurable, KindSession]`. The existing `WHERE kind IN (...)` SQL clause supports this.

### Stats

`StoreStats` gains:
```go
SessionSourceCount int
SessionFreshCount  int
SessionStaleCount  int
```

`ClassifySources` handles `KindSession` with fresh/stale bucketing using the session TTL.

### CLI Cleanup

- **MCP tool (`capy_cleanup`):** gains `purge_session` boolean flag (mirrors `purge_ephemeral`).
- **CLI (`capy cleanup`):** gains `--kind` flag for kind-specific cleanup (`capy cleanup --kind session`).

## Integration Points

| Component | Change |
|-----------|--------|
| `internal/store/types.go` | `KindSession`, `Valid()`, content type |
| `internal/store/schema.go` | CHECK constraint (via migration) |
| `internal/store/migrate.go` | New migration for `session` kind |
| `internal/store/cleanup.go` | Generalize `cleanupEphemeral` → `cleanupByTTL`, add session cleanup path |
| `internal/store/chunk.go` | New `chunkTranscript` function |
| `internal/store/index.go` | Handle `"session"` content type routing to `chunkTranscript` |
| `internal/store/types.go` | `StoreStats` session counters |
| `internal/config/config.go` | `SessionTTLDays` in `CleanupConfig` |
| `internal/config/loader.go` | Validation for `SessionTTLDays` |
| `internal/server/server.go` | Background goroutine for session sweep |
| `internal/server/tool_cleanup.go` | `purge_session` parameter |
| `internal/server/tool_stats.go` | Session stats rendering |
| `internal/server/tool_search.go` | Default `IncludeKinds` update |
| `cmd/capy/cleanup.go` | `--kind` CLI flag |

New files:
| File | Purpose |
|------|---------|
| `internal/session/parse.go` | JSONL parsing, message filtering, transcript building |
| `internal/session/sweep.go` | Session directory discovery, mtime gate, sweep orchestration |
| `internal/session/parse_test.go` | Unit tests for parsing and filtering |
| `internal/session/sweep_test.go` | Unit tests for sweep mechanism |

## Open Questions

None — all design decisions have been made. See the PAL consultation thread for the full decision log including alternatives considered.

## References

- [ADR-017: Source kind separation](../../adr/017-source-kind-separation.md) — establishes the `kind` column and `IncludeKinds` array design
- [ADR-011: Conservative cleanup policy](../../adr/011-conservative-cleanup-policy.md) — retention scoring (not used for sessions)
- [GitHub issue #24](https://github.com/serpro69/capy/issues/24) — original feature request
- Claude Code session format: JSONL in `~/.claude/projects/<dir-name>/`, analyzed from 96 sessions across 2 projects