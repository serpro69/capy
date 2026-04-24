# Tasks: Sessionflow RAG

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Status: pending
> Created: 2026-04-23

## Task 1: Storage foundation — KindSession, migration, config
- **Status:** in-progress
- **Depends on:** —
- **Docs:** [implementation.md#phase-1-storage-foundation](./implementation.md#phase-1-storage-foundation)

### Subtasks
- [x] 1.1 Add `KindSession SourceKind = "session"` to `internal/store/types.go`, update `Valid()`, add session fields to `StoreStats`
- [x] 1.2 Add migration-tracking table to `internal/store/migrate.go`, retroactively record migration 017, then add new migration for `session` kind CHECK constraint. Handle both DB populations (migrated DBs without CHECK, fresh DBs with CHECK). Update `schemaSQL` in `schema.go`.
- [x] 1.3 Add `SessionTTLDays int` to `CleanupConfig` in `internal/config/config.go` (default 60), add validation in `loader.go`, add `sessionTTL()` to server
- [x] 1.4 Generalize `cleanupEphemeral` → `cleanupByTTL(kind, ttl)` in `internal/store/cleanup.go`. Update signatures: `Cleanup(dryRun, ephemeralTTL, sessionTTL)`, `ClassifySources(ephemeralTTL, sessionTTL)`, `Stats(ephemeralTTL, sessionTTL)`. Add `PurgeSession()`. Update all callers (`tool_cleanup.go`, `tool_stats.go`, `server.go`).
- [x] 1.5 Update default kind filter in `effectiveKindFilter()` at `internal/store/search.go` from `[KindDurable]` to `[KindDurable, KindSession]`. Add `KindScopeIncludesSession` (or generalize to `KindScopeIncludes`). Update `parseIncludeKinds` error message in `tool_search.go` to include `"session"`. Add session no-results hint alongside ephemeral hint.
- [x] 1.6 Write tests for all above: migration applies, session kind accepted, TTL cleanup works, search includes sessions

## Task 2: Session JSONL parser
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md#phase-2-session-parsing](./implementation.md#phase-2-session-parsing)

### Subtasks
- [ ] 2.1 Create `internal/session/parse.go` with JSONL parser: line-by-line reading, type routing, text extraction, tool name extraction, system-reminder stripping
- [ ] 2.2 Define `ParsedSession` and `TurnPair` types with session metadata (SessionID, StartTime, TotalAssistantChars)
- [ ] 2.3 Add sub-agent parsing: discover `<uuid>/subagents/` directory, parse `agent-*.jsonl` and `agent-*.meta.json`, return sub-agent turn pairs with metadata
- [ ] 2.4 Add `IsIndexable()` session-level gate: min 2 turn pairs, min 200 chars assistant text
- [ ] 2.5 Write unit tests in `internal/session/parse_test.go` with synthetic JSONL fixtures covering: valid session, empty session, tool-result-only session, away_summary, system-reminder tags, sub-agent conversations, malformed JSON lines

## Task 3: Transcript builder and chunking
- **Status:** pending
- **Depends on:** Task 2
- **Docs:** [implementation.md#phase-2-session-parsing](./implementation.md#phase-2-session-parsing), [implementation.md#phase-3-chunking](./implementation.md#phase-3-chunking)

### Subtasks
- [ ] 3.1 Create `internal/session/transcript.go` to convert `ParsedSession` → plaintext transcript string with `Human:`/`Assistant:` format, `[Tools: ...]` lines, `[Session summary: ...]` entries, and `--- Subagent ---` delimiters
- [ ] 3.2 Create `internal/session/chunk.go` with `ChunkSession(session *ParsedSession, transcript string, maxBytes int) []store.Chunk`: sliding window of ~4 turn pairs, 1-pair overlap, title generation from structured `ParsedSession` data (NOT parsed from transcript text). Export `SplitOversized` and `ChunkHasCode` from `internal/store/chunk.go` for reuse.
- [ ] 3.3 Wire session chunking into the sweep orchestrator (Task 4.3) — chunks are produced by the session package and passed to the store's `Index` (or a new `IndexChunked`). No `"session"` case needed in `chunkContent`.
- [ ] 3.4 Write unit tests in `internal/session/chunk_test.go`: transcript builder output, chunk window boundaries from structured TurnPairs, overlap, title format, oversized chunk splitting

## Task 4: Sweep mechanism
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3
- **Docs:** [implementation.md#phase-4-sweep-integration](./implementation.md#phase-4-sweep-integration)

### Subtasks
- [ ] 4.1 Create `internal/session/sweep.go` with `SessionDir(projectDir string)` — path mangling (replace `/` and `.` with `-`), directory existence check
- [ ] 4.2 Add mtime gate logic: query `session:` sources from store, build `uuid → indexed_at` map, compare `max(file.mtime, subagents_dir.mtime)` against indexed_at
- [ ] 4.3 Add `Sweep(ctx context.Context, store, projectDir)` orchestrator: accepts context for cooperative cancellation, checks `ctx.Err()` between files. Discovery → list → mtime gate → parse → gate → transcript → chunk → index. Log results.
- [ ] 4.4 Integrate into `Server.Serve()` as background goroutine. Derive context from server's `ctx` parameter (NOT `context.Background()`), apply 30s timeout on top: `context.WithTimeout(ctx, 30*time.Second)`
- [ ] 4.5 Write unit tests in `internal/session/sweep_test.go`: directory derivation, mtime gate logic. Write integration test with temp directory of synthetic session files.

## Task 5: CLI and tool updates
- **Status:** pending
- **Depends on:** Task 1, Task 4
- **Docs:** [implementation.md#phase-5-cli-and-tool-updates](./implementation.md#phase-5-cli-and-tool-updates)

### Subtasks
- [ ] 5.1 Add `purge_session` boolean parameter to `capy_cleanup` MCP tool in `internal/server/tool_cleanup.go`
- [ ] 5.2 Add `--kind` string flag to `cmd/capy/cleanup.go` CLI command
- [ ] 5.3 Update stats rendering in `internal/server/tool_stats.go` to include session source count and fresh/stale breakdown
- [ ] 5.4 Write tests for cleanup with purge_session, stats with session sources

## Task 6: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5

### Subtasks
- [ ] 6.1 Run `test` skill to verify all tasks — full test suite (`make test`, `make test-race`), integration tests, edge cases
- [ ] 6.2 Add end-to-end integration test in `internal/server/integration_test.go`: synthetic sessions → sweep → search → verify results → cleanup → verify eviction
- [ ] 6.3 Run `document` skill to write ADR-019 for KindSession decision, update CONTRIBUTING.md if needed
- [ ] 6.4 Run `review-code` skill to review the implementation
- [ ] 6.5 Run `review-spec` skill to verify implementation matches design and implementation docs
