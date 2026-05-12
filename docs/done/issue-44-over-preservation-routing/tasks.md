# Tasks: Fix over-preservation and over-routing (Issue #44)

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> ADR: [023-fetch-ephemeral-default-and-routing-rewrite](../../adr/023-fetch-ephemeral-default-and-routing-rewrite.md)
> Status: done
> Created: 2026-05-11

## Task 1: Add `kind` parameter to `capy_fetch_and_index`
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md § Task 1](./implementation.md#task-1-add-kind-parameter-to-capy_fetch_and_index)

### Subtasks
- [x] 1.1 Add optional `kind` string parameter (enum: `["durable", "ephemeral"]`) to `toolFetchAndIndex()` in `internal/server/tools.go`
- [x] 1.2 In `handleFetchAndIndex()` (`internal/server/tool_fetch.go`): read and validate `kind` **before** the TTL cache check (before line 52). Default to `store.KindEphemeral`, validate against `"durable"`/`"ephemeral"` only (reject `"session"` and invalid values)
- [x] 1.3 Modify the TTL cache check (lines 52-68): after cache hit, compare `meta.Kind` against requested kind. If they differ, bypass cache and proceed with re-fetch+re-index
- [x] 1.4 Replace all four `store.KindDurable` literals at the indexing call sites (lines 132, 138, 141, 145) with the resolved kind variable
- [x] 1.5 Update the response text to indicate whether content was indexed as durable or ephemeral, and remind about `source:` filter for ephemeral follow-up queries
- [x] 1.6 Add tests in `tool_fetch_test.go`: no-kind → ephemeral, `kind: "durable"` → durable, `kind: "ephemeral"` → ephemeral, `kind: "invalid"` → error, `kind: "session"` → error, cache-hit with kind mismatch → cache bypassed and kind updated
- [x] 1.7 Verify: `go build ./...` and `go test -tags fts5 ./internal/server/...`

## Task 2: Cap search fallback source listing
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation.md § Task 2](./implementation.md#task-2-cap-search-fallback-source-listing)

### Subtasks
- [x] 2.1 In `handleSearch()` (`internal/server/tool_search.go` lines 184-199): replace the `ListSources()` + per-source formatting loop with `CountSourcesByKind()` calls for non-excluded kinds
- [x] 2.2 Format a single summary line: source count per included kind, plus pointer to `capy_stats` for details (section counts intentionally omitted — source count is sufficient)
- [x] 2.3 Update the ephemeral-excluded hint (`tool_search.go:126-131`): add fetched content alongside command output. Change text to mention that ephemeral sources now include both command output and fetched web pages, and add `source: "<label>"` as a recovery path alongside `include_kinds`
- [x] 2.4 Write/update test: trigger no-results with multiple indexed sources → assert output contains count summary, does NOT contain individual source labels, still contains updated ephemeral/session hints when applicable
- [x] 2.5 Verify: `go test -tags fts5 ./internal/server/...`

## Task 3: Update tool descriptions
- **Status:** done
- **Depends on:** Task 1
- **Docs:** [implementation.md § Task 3](./implementation.md#task-3-update-tool-descriptions-in-toolsgo)

### Subtasks
- [x] 3.1 `toolExecute()` in `tools.go`: remove "MANDATORY" prefix, remove "git queries (git log, git diff)" from examples, reframe as extraction tool with positive/negative guidance
- [x] 3.2 `toolBatchExecute()` in `tools.go`: remove "THIS IS THE PRIMARY TOOL", reframe as broad exploration/extraction tool with "NOT for" guidance
- [x] 3.3 `toolFetchAndIndex()` in `tools.go`: update description to mention ephemeral default, `kind` parameter, and `source:` filter pattern for follow-up search
- [x] 3.4 `toolSearch()` in `tools.go`: fix stale description — change default from "durable only" to "durable and session", remove "fetched/indexed reference content" parenthetical, update `include_kinds` help text to match actual `effectiveKindFilter` behavior
- [x] 3.5 `toolExecuteFile()` in `tools.go`: soften "PREFER THIS OVER Read/cat" to align with comprehension-vs-extraction principle. Reframe: "for large files (10k+ lines) where you only need derived answers. Read is correct when you need to understand or edit the file."
- [x] 3.6 Verify: `go build ./...`, start MCP server, call `tools/list`, inspect descriptions

## Task 4: Full routing rewrite (AGENTS.md + generated routing blocks)
- **Status:** done
- **Depends on:** Task 1, Task 3
- **Docs:** [implementation.md § Task 4](./implementation.md#task-4-full-routing-rewrite-agentsmd--generated-routing-blocks)

### Subtasks
- [x] 4.1 Write new `.capy/AGENTS.md` with task-aware routing structure: decision principle, when to use direct tools, when to use capy, blocked commands, source kinds, Read vs capy_execute_file, output constraints, subagent routing, capy commands
- [x] 4.2 Rewrite `GenerateRoutingInstructions()` in `internal/platform/routing.go` to produce the same task-aware routing content as AGENTS.md
- [x] 4.3 Rewrite `RoutingBlock()` in `internal/hook/routing.go` — replace "MUST use capy", "Primary tool", "DO NOT use Bash >20 lines", "Bash is ONLY for" with task-aware comprehension-vs-extraction guidance
- [x] 4.4 Update `BASH_GUIDANCE` constant in `internal/hook/routing.go` — replace "Bash is best for: git, mkdir, rm, mv, navigation, and short-output commands only" with task-aware guidance
- [x] 4.5 Review `GREP_GUIDANCE` and `READ_GUIDANCE` constants — update if they contradict the new routing principle (READ_GUIDANCE is likely fine as-is)
- [x] 4.6 Review build-tool redirect in `internal/hook/pretooluse.go:111-116` — removed hard redirect; build tools now receive BASH_GUIDANCE instead, consistent with comprehension-vs-extraction principle (build output is sequential comprehension content)
- [x] 4.7 Verify routing clarity: does AGENTS.md unambiguously route `git diff` to Bash? Does `RoutingBlock()` do the same for subagents? Does it explain when to pass `kind: "durable"` to fetch?
- [x] 4.8 Verify no orphaned references: check if other docs reference AGENTS.md sections that were renamed or removed
- [x] 4.9 Run tests: `go test -tags fts5 ./internal/hook/... ./internal/platform/...`

## Task 5: Final verification
- **Status:** done
- **Depends on:** Task 1, Task 2, Task 3, Task 4

### Subtasks
- [x] 5.1 Run `go build ./...` — verify compilation
- [x] 5.2 Run `go test -tags fts5 ./...` — verify all tests pass
- [x] 5.3 Run `kk:test` to verify test coverage for new/changed code
- [x] 5.4 Run `kk:document` to update any relevant documentation
- [x] 5.5 Run `kk:review-code` with Go profile to review all changes
- [x] 5.6 Run `kk:review-spec` to verify implementation matches design.md, implementation.md, and ADR-023
