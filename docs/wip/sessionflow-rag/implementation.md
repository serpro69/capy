# Sessionflow RAG — Implementation Plan

> **Design:** [./design.md](./design.md)
> **Issue:** [#24](https://github.com/serpro69/capy/issues/24)
> **Created:** 2026-04-23

This plan is ordered for incremental development. Each task builds on the previous and can be verified independently. The developer should be familiar with Go and SQLite but may have no prior context on the capy codebase.

## Prerequisites

Read these files before starting:
- `CONTRIBUTING.md` — build instructions, test patterns, project structure
- `internal/store/types.go` — source kinds, chunk types, search/index result types
- `internal/store/schema.go` — FTS5 schema
- `internal/store/index.go` — `Index()` function, content_hash dedup, `chunkContent` routing
- `internal/store/cleanup.go` — retention scoring, `cleanupEphemeral`, `Cleanup()`
- `internal/store/chunk.go` — `chunkPlainText`, `chunkMarkdown`, splitting strategies
- `internal/server/server.go` — `Server` struct, `Serve()`, `getStore()`, `ephemeralTTL()`
- `internal/config/config.go` — `Config`, `CleanupConfig`, `DefaultConfig()`
- `docs/adr/017-source-kind-separation.md` — why source kinds exist, the `IncludeKinds` design

All tests require `-tags fts5`. Use `make test` or `go test -tags fts5 -count=1 ./...`.

---

## Phase 1: Storage Foundation

### 1.1 Add KindSession to types

**File:** `internal/store/types.go`

- Add `KindSession SourceKind = "session"` constant.
- Update `Valid()` to return true for `KindSession`.
- Add session-specific fields to `StoreStats`: `SessionSourceCount`, `SessionFreshCount`, `SessionStaleCount`.

**Verify:** existing tests still pass (`go test -tags fts5 ./internal/store/...`). No behavioral change yet.

### 1.2 Schema migration and migration tracker

**Files:** `internal/store/migrate.go`, `internal/store/schema.go`

This is the second migration, which triggers the TODO at `migrate.go:15-16`. Three things must happen:

**A. Add a migration-tracking table.** Before running the new migration, create:
```sql
CREATE TABLE IF NOT EXISTS migrations (
  name TEXT PRIMARY KEY,
  applied_at TEXT DEFAULT CURRENT_TIMESTAMP
)
```
Then retroactively record migration 017 as already applied (`INSERT OR IGNORE INTO migrations (name) VALUES ('017_add_source_kind')`). This replaces the PRAGMA-based idempotency check for future migrations.

**B. Handle two DB populations.** There are two shapes of `sources` table in the wild:
- **Migrated DBs** (had `ALTER TABLE ADD COLUMN kind`): these lack a CHECK constraint entirely (SQLite's ALTER TABLE ADD COLUMN does not support CHECK — see `migrate.go:67-69`). Inserting `kind='session'` already works. The migration just needs to record itself in the tracker.
- **Fresh DBs** (created with `schemaSQL`): these have `CHECK (kind IN ('ephemeral', 'durable'))`. These require a table rebuild: create new table with updated CHECK → copy data → drop old → rename.

Detection: attempt `INSERT INTO sources (label, content_type, kind) VALUES ('__migration_probe', 'plaintext', 'session')` inside the transaction. If it succeeds, rollback the probe row — no table rebuild needed. If it fails with a constraint error, do the table rebuild.

**C. Update `schemaSQL`** in `schema.go` to include `'session'` in the CHECK constraint so future fresh DBs accept the new kind.

**Verify:** `go test -tags fts5 ./internal/store/...` — migration applies on both fresh and migrated DBs. Insert `kind='session'` succeeds. Migration tracker table exists with both migrations recorded.

### 1.3 Config: SessionTTLDays

**File:** `internal/config/config.go`

- Add `SessionTTLDays int` to `CleanupConfig` with toml tag `session_ttl_days`.
- Set default to 60 in `DefaultConfig()`.

**File:** `internal/config/loader.go`

- Add validation: `SessionTTLDays` must be >= 1. Reject with error if < 1.

**File:** `internal/server/server.go`

- Add `sessionTTL()` method mirroring `ephemeralTTL()`: resolves TTL from config with 60-day fallback.

**Verify:** `go test -tags fts5 ./internal/config/...` — config loads with default, rejects invalid values.

### 1.4 Cleanup: Generalize TTL-based eviction

**File:** `internal/store/cleanup.go`

- Rename `cleanupEphemeral` to `cleanupByTTL(kind SourceKind, ttl time.Duration)`. The only change: the `WHERE kind = ?` parameter becomes the `kind` argument instead of hardcoded `KindEphemeral`.
- Update `Cleanup()` signature to `Cleanup(dryRun bool, ephemeralTTL, sessionTTL time.Duration)`. Call `cleanupByTTL` twice: once for `KindEphemeral` with ephemeral TTL, once for `KindSession` with session TTL. Pass both TTLs through to `cleanupDurable` → `ClassifySources`.
- Update `PurgeEphemeral` to call `cleanupByTTL(KindEphemeral, ttl)`.
- Add `PurgeSession(dryRun bool, ttl time.Duration)` that calls `cleanupByTTL(KindSession, ttl)`.
- Update `ClassifySources(ephemeralTTL, sessionTTL time.Duration)` to handle `KindSession` with fresh/stale bucketing using the session TTL.
- Update `Stats(ephemeralTTL, sessionTTL time.Duration)` to pass both TTLs to `ClassifySources` and populate session counters.
- Update all callers of `Cleanup`, `ClassifySources`, and `Stats` to pass both TTLs. Key callers: `tool_cleanup.go`, `tool_stats.go`, `server.go`.

**Verify:** `go test -tags fts5 ./internal/store/...` — existing ephemeral cleanup tests pass. Add tests for session TTL cleanup: index a session-kind source, advance clock past TTL, verify eviction.

### 1.5 Search: Update default IncludeKinds

**File:** `internal/store/search.go`

- Update `effectiveKindFilter()` at line ~569: change default return from `[]SourceKind{KindDurable}` to `[]SourceKind{KindDurable, KindSession}`.
- Add `KindScopeIncludesSession(opts SearchOptions) bool` mirroring `KindScopeIncludesEphemeral` (or generalize both to `KindScopeIncludes(opts SearchOptions, kind SourceKind) bool`).

**File:** `internal/server/tool_search.go`

- Update `parseIncludeKinds` error message at line ~197: change `"accepted: \"durable\", \"ephemeral\""` to include `"session"`.
- Add session-aware no-results hint alongside the existing ephemeral hint at line ~124: when sessions are excluded but session sources exist, surface a recovery hint.

**File:** `internal/server/intent_search.go` (if applicable)

- Verify intent search does not need session kind changes (it writes ephemeral, searches durable).

**Verify:** index a session-kind source manually, search for its content, verify it appears in results. Verify ephemeral sources are still excluded by default. Verify `parseIncludeKinds` accepts `"session"` as a valid value.

---

## Phase 2: Session Parsing

### 2.1 JSONL parser

**New file:** `internal/session/parse.go`

Create a session JSONL parser that reads a file and produces a filtered transcript. The parser:

1. Reads the file line by line.
2. Parses each line as JSON.
3. Routes on `type` field:
   - `"user"`: extract text content (string or text blocks from array). Skip tool_result-only messages. Skip messages starting with `/`. Strip `<system-reminder>` tags.
   - `"assistant"`: extract text from `text` blocks. Extract tool names from `tool_use` blocks. Skip `thinking` blocks.
   - `"system"` with `subtype == "away_summary"`: extract `content` field.
   - All other types: skip.
4. Builds turn pairs: a turn pair is a human text message followed by assistant text response(s). Tool names from assistant tool_use blocks are collected per turn.
5. Extracts session metadata: first user message timestamp (for the label datetime), session UUID (from `sessionId` field or filename).

**Output type:**

```go
type ParsedSession struct {
    SessionID  string
    StartTime  time.Time
    TurnPairs  []TurnPair
    TotalAssistantChars int
}

type TurnPair struct {
    HumanText     string
    AssistantText string
    ToolNames     []string
    IsSubagent    bool
    SubagentType  string
    SubagentDesc  string
}
```

Handle malformed JSON lines gracefully: log warning, skip line, continue.

**Verify:** unit tests with synthetic JSONL covering all message types. Test: valid session, empty session, tool-result-only session, session with away_summary, session with system-reminder tags in user text.

### 2.2 Sub-agent parsing

**File:** `internal/session/parse.go` (extend)

Add function to parse sub-agent conversations:

1. Check if `<uuid>/subagents/` directory exists.
2. List `agent-*.jsonl` files.
3. For each, read the corresponding `agent-*.meta.json` for `agentType` and `description`.
4. Parse the sub-agent JSONL using the same parser logic.
5. Return sub-agent turn pairs with `IsSubagent: true` and the metadata fields populated.

**Verify:** unit tests with synthetic sub-agent JSONL + meta.json files.

### 2.3 Transcript builder

**File:** `internal/session/transcript.go`

Convert `ParsedSession` to a plaintext transcript string:

```
Human: <text>
Assistant: <text>
[Tools: Read, Edit]

[Session summary: <away_summary text>]

--- Subagent: Explore — "description" ---
Human: <text>
Assistant: <text>
--- End subagent ---
```

Rules:
- Tool-result-only user messages are already filtered out by the parser.
- `[Tools: ...]` line appears only when the turn had tool_use blocks.
- `[Session summary: ...]` for away_summary entries (inserted inline at the position they appeared).
- Sub-agent turns are delimited with `---` markers.

**Verify:** unit tests with known ParsedSession → expected transcript string.

### 2.4 Session-level gate

**File:** `internal/session/parse.go` (extend)

Add validation function:

```go
func (s *ParsedSession) IsIndexable() bool
```

Returns true if:
- `len(TurnPairs) >= 2` (counting only non-subagent pairs for the minimum, or all pairs — TBD based on what feels right)
- `TotalAssistantChars >= 200`

**Verify:** unit tests with edge cases: 1 turn pair (reject), 2 pairs with 150 chars (reject), 2 pairs with 300 chars (accept).

---

## Phase 3: Chunking

### 3.1 Transcript chunking function

**New file:** `internal/session/chunk.go`

The chunker lives in `internal/session/` (NOT `internal/store/chunk.go`) because it operates directly on the structured `ParsedSession` data. This avoids the anti-pattern of serializing structured metadata (tool names, subagent info) into text and then re-parsing it with regexes.

Add `ChunkSession(session *ParsedSession, transcript string, maxBytes int) []store.Chunk`:

- Takes both the structured `ParsedSession` (for metadata: tool names, subagent info, timestamps) and the plaintext transcript (for chunk content).
- Sliding window of ~4 turn pairs with 1 turn pair overlap. Window boundaries are computed from `ParsedSession.TurnPairs` indices.
- Each chunk's content is the corresponding slice of the transcript text.
- Each chunk title is built from structured data: `Session <datetime> | Turns <start>-<end> | Tools: <names>`. When sub-agent turns are in the window: append `| Subagent: <type>`.
- Chunks exceeding `maxBytes` (default `store.MaxChunkBytes` = 4096) are split using `store.SplitOversized` (export the existing helper from `chunk.go`).
- `HasCode` is set by checking for code fences in the chunk content (reuse `store.ChunkHasCode`).

**File:** `internal/store/chunk.go`

- Export `SplitOversized` and `ChunkHasCode` (rename from `splitOversized` and `chunkHasCode`) so the session package can reuse them. Alternatively, expose a `MaxChunkBytes` constant.

**File:** `internal/store/index.go`

- The `chunkContent` router does NOT need a `"session"` case. Session chunking is called directly by the sweep orchestrator, which passes pre-chunked `[]store.Chunk` to a new `IndexChunked` method (or calls `Index` with the transcript and lets the existing plaintext fallback handle it — but the former is cleaner since we already have structured chunks).

**Verify:** unit tests in `internal/session/chunk_test.go` with a multi-turn ParsedSession. Verify: correct window boundaries, overlap, title generation from structured data, oversized splitting.

---

## Phase 4: Sweep Integration

### 4.1 Session directory discovery

**New file:** `internal/session/sweep.go`

Add function to derive the Claude Code session directory:

```go
func SessionDir(projectDir string) (string, error)
```

- Compute absolute path of `projectDir`.
- Mangle: replace `/` and `.` with `-`.
- Construct: `~/.claude/projects/<mangled>/`.
- Verify directory exists. Return error if not.

**Verify:** unit test with known project paths → expected directory names. Test the `.` replacement (e.g., paths containing `.config`).

### 4.2 Mtime gate

**File:** `internal/session/sweep.go`

Add the mtime comparison logic:

1. Query all `session:` sources from the store → build `uuid → indexed_at` map.
2. For each `.jsonl` file: extract UUID from filename, compute effective mtime as `max(file.mtime, subagents_dir.mtime)`.
3. Compare against `indexed_at`. Skip if effective mtime <= indexed_at.

**Verify:** unit tests with mocked file stats and store data. Test: new file (no existing source), unchanged file (skip), modified file (process), file with updated sub-agent dir (process).

### 4.3 Sweep orchestrator

**File:** `internal/session/sweep.go`

Add the main sweep function:

```go
func Sweep(ctx context.Context, store *store.ContentStore, projectDir string) (indexed int, skipped int, errors int)
```

Accepts `context.Context` as first parameter (standard Go convention for any function doing I/O). Checks `ctx.Err()` between files for cooperative cancellation. Orchestrates: discovery → list files → mtime gate → parse → gate → transcript → chunk → index.

Logs at info level: `"session sweep complete"` with indexed/skipped/error counts.
Logs at warn level: individual parse failures.

**Verify:** integration test with a temp directory containing synthetic session files. Verify: correct files indexed, mtime gate works, gate filters trivial sessions, errors are logged and skipped.

### 4.4 Server startup integration

**File:** `internal/server/server.go`

In `Serve()`, after `s.registerTools()` and before `stdio.Listen()`. The goroutine must derive its context from the server's `ctx` parameter (NOT `context.Background()`) so it participates in cooperative cancellation when the server shuts down. This prevents the goroutine from outliving `Serve()` and calling `getStore()` after `shutdown()` has closed the store.

```go
go func() {
    sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    indexed, skipped, errs := session.Sweep(sweepCtx, s.getStore(), s.projectDir)
    if indexed > 0 || errs > 0 {
        slog.Info("session sweep", "indexed", indexed, "skipped", skipped, "errors", errs)
    }
}()
```

**Verify:** start the server with synthetic session files present, verify they get indexed. Start again, verify no re-indexing (mtime gate). Shut down the server mid-sweep (if possible), verify clean cancellation.

---

## Phase 5: CLI and Tool Updates

### 5.1 MCP cleanup tool

**File:** `internal/server/tool_cleanup.go`

- Add `purge_session` boolean parameter to the tool schema.
- When `purge_session` is true, call `store.PurgeSession(dryRun, sessionTTL)`.

**Verify:** unit test: call cleanup with `purge_session=true`, verify only session sources are affected.

### 5.2 CLI cleanup command

**File:** `cmd/capy/cleanup.go`

- Add `--kind` string flag (valid values: `"ephemeral"`, `"session"`, `""` for all).
- When `--kind` is set, run only the corresponding cleanup path.

**Verify:** manual test: `./capy cleanup --kind session` with indexed session sources.

### 5.3 Stats rendering

**File:** `internal/server/tool_stats.go`

- Update stats rendering to include session source count and fresh/stale breakdown.

**Verify:** index some session sources, call `capy_stats`, verify session section appears.

---

## Phase 6: Final Verification

### 6.1 Full test suite

Run `make test` and `make test-race`. All existing tests must pass. No regressions.

### 6.2 Integration test

Add an end-to-end test in `internal/server/integration_test.go`:
1. Create synthetic session JSONL files in a temp directory.
2. Boot a test server pointing at that directory.
3. Trigger the sweep.
4. Search for session content via the MCP tool handler.
5. Verify results surface with correct labels and titles.
6. Run cleanup, verify TTL eviction.

### 6.3 Documentation

- Update `CONTRIBUTING.md` if the new `internal/session/` package introduces patterns worth documenting.
- Write ADR for the `KindSession` decision (ADR-019 or next available number).

### 6.4 Code review

Run `kk:review-code` on the final diff. Run `kk:review-spec` to verify implementation matches this design doc.
