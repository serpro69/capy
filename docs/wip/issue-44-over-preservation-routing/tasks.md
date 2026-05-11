# Tasks: Fix over-preservation and over-routing (Issue #44)

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> ADR: [023-fetch-ephemeral-default-and-routing-rewrite](../../adr/023-fetch-ephemeral-default-and-routing-rewrite.md)
> Status: pending
> Created: 2026-05-11

## Task 1: Add `kind` parameter to `capy_fetch_and_index`
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md § Task 1](./implementation.md#task-1-add-kind-parameter-to-capy_fetch_and_index)

### Subtasks
- [ ] 1.1 Add optional `kind` string parameter (enum: `["durable", "ephemeral"]`) to `toolFetchAndIndex()` in `internal/server/tools.go`
- [ ] 1.2 In `handleFetchAndIndex()` (`internal/server/tool_fetch.go`): read `kind` from request, default to `store.KindEphemeral`, validate against `"durable"`/`"ephemeral"` only (reject `"session"` and invalid values)
- [ ] 1.3 Replace all four `store.KindDurable` literals at the indexing call sites (lines 132, 138, 141, 145) with the resolved kind variable
- [ ] 1.4 Update the response text to indicate whether content was indexed as durable or ephemeral, and remind about `source:` filter for ephemeral follow-up queries
- [ ] 1.5 Add tests in `tool_fetch_test.go`: no-kind → ephemeral, `kind: "durable"` → durable, `kind: "ephemeral"` → ephemeral, `kind: "invalid"` → error, `kind: "session"` → error
- [ ] 1.6 Verify: `go build ./...` and `go test -tags fts5 ./internal/server/...`

## Task 2: Cap search fallback source listing
- **Status:** pending
- **Depends on:** —
- **Docs:** [implementation.md § Task 2](./implementation.md#task-2-cap-search-fallback-source-listing)

### Subtasks
- [ ] 2.1 In `handleSearch()` (`internal/server/tool_search.go` lines 184-199): replace the `ListSources()` + per-source formatting loop with `CountSourcesByKind()` calls for non-excluded kinds
- [ ] 2.2 Format a single summary line: count of sources and sections for each included kind, plus pointer to `capy_stats` for details
- [ ] 2.3 Write/update test: trigger no-results with multiple indexed sources → assert output contains count summary, does NOT contain individual source labels, still contains ephemeral/session hints when applicable
- [ ] 2.4 Verify: `go test -tags fts5 ./internal/server/...`

## Task 3: Update tool descriptions
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation.md § Task 3](./implementation.md#task-3-update-tool-descriptions-in-toolsgo)

### Subtasks
- [ ] 3.1 `toolExecute()` in `tools.go`: remove "MANDATORY" prefix, remove "git queries (git log, git diff)" from examples, reframe as extraction tool with positive/negative guidance
- [ ] 3.2 `toolBatchExecute()` in `tools.go`: remove "THIS IS THE PRIMARY TOOL", reframe as broad exploration/extraction tool with "NOT for" guidance
- [ ] 3.3 `toolFetchAndIndex()` in `tools.go`: update description to mention ephemeral default, `kind` parameter, and `source:` filter pattern for follow-up search
- [ ] 3.4 Verify: `go build ./...`, start MCP server, call `tools/list`, inspect descriptions

## Task 4: Full AGENTS.md rewrite
- **Status:** pending
- **Depends on:** Task 1, Task 3
- **Docs:** [implementation.md § Task 4](./implementation.md#task-4-full-agentsmd-rewrite)

### Subtasks
- [ ] 4.1 Write new `.capy/AGENTS.md` with task-aware routing structure: decision principle, when to use direct tools, when to use capy, blocked commands, source kinds, Read vs capy_execute_file, output constraints, subagent routing, capy commands
- [ ] 4.2 Verify routing clarity: does the document unambiguously route `git diff` to Bash? Does it route broad `rg` to capy? Does it explain when to pass `kind: "durable"` to fetch?
- [ ] 4.3 Verify no orphaned references: check if other docs reference AGENTS.md sections that were renamed or removed

## Task 5: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4

### Subtasks
- [ ] 5.1 Run `go build ./...` — verify compilation
- [ ] 5.2 Run `go test -tags fts5 ./...` — verify all tests pass
- [ ] 5.3 Run `kk:test` to verify test coverage for new/changed code
- [ ] 5.4 Run `kk:document` to update any relevant documentation
- [ ] 5.5 Run `kk:review-code` with Go profile to review all changes
- [ ] 5.6 Run `kk:review-spec` to verify implementation matches design.md, implementation.md, and ADR-023
