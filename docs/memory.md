# Session Memory: MCP Port

After Action Review from implementation sessions. Learnings that apply to future work on this repo.

---

## Go / Language Gotchas

### RE2 has no backreferences
Go's `regexp` package uses RE2, which does not support backreferences (`\1`). When porting regex patterns from TypeScript/JS that use backreferences (e.g., matching opening and closing quote characters), expand each pattern into separate per-quote-type variants. See `internal/security/shell_escape.go` for the pattern: `quotedPattern(prefix, suffix)` creates separate single-quote and double-quote regexps.

### Integer division truncates timeout budgets
`int(remaining.Seconds())` truncates — 1.9s becomes 1, and 0.9s becomes 0. When 0 is passed to the executor as `TimeoutSec`, it's interpreted as "use default (30s)" rather than "no time left". Always check `remainingSec <= 0` *before* passing to the executor to avoid accidentally giving a timed-out budget the full default timeout. See `tool_batch.go` timeout budget loop.

### `signal.Stop` doesn't close the channel
Calling `signal.Stop(ch)` stops signal delivery but does NOT close the channel. Goroutines blocked on `<-ch` will leak forever. Use a `done` channel with `select` to allow clean shutdown. See `lifecycle.go`.

### Map iteration order is non-deterministic
Go randomizes map iteration. When building user-visible strings from map keys (e.g., runtime language lists in tool descriptions), sort the keys first with `slices.Sort`. Otherwise tool schemas change between runs, which can confuse clients that hash or diff them.

---

## Build System

### FTS5 requires build tag
Any test that touches the ContentStore (directly or via server handlers) needs `-tags fts5`. Without it, you get `no such module: fts5` at runtime — which looks like a code bug, not a build configuration issue. Always use `make test` or `go test -tags fts5 ./...`, never bare `go test ./...`.

### Gitignore pattern `capy` matches `cmd/capy/`
A bare `capy` in `.gitignore` matches the directory `cmd/capy/` too. Use `/capy` to only match the binary at the repo root.

---

## mcp-go Library

### Actual types differ from initial spec
The implementation.md was written before the mcp-go API was explored. Key differences:
- Type is `*mcpserver.MCPServer` (in `server` subpackage), not `*mcp.Server`
- `ServeStdio()` is a convenience that creates its own `context.Background()` — it ignores the caller's context. Use `NewStdioServer()` + `Listen(ctx, os.Stdin, os.Stdout)` to honor context cancellation.
- `CallToolRequest` has helpers: `GetString(key, default)`, `GetFloat(key, default)`, `GetArguments()` returns `map[string]any`
- Tool results: `mcp.NewToolResultText(text)`, `mcp.NewToolResultError(text)`, content is `[]Content` with `TextContent` concrete type

---

## Architecture Patterns

### Intent search must not leak raw output on failure
The whole point of intent search is to keep large output OUT of the LLM context. If indexing fails, returning the raw output defeats the purpose. Always truncate to a preview (e.g., 3KB) + error message on index failure. See `intent_search.go`.

### Batch handler response doesn't include raw output
The `capy_batch_execute` tool response contains only: summary line, section inventory (titles + sizes), and search result snippets. The raw combined command output lives only in the indexed store. Don't write tests that assert raw output text (like "skipped") appears in the tool response — it won't. Assert on inventory structure or timing instead.

### TS reference is authoritative, not the Go implementation spec
When the Go implementation.md disagrees with `context-mode/src/*.ts`, the TS code is correct. The Go spec was derived from the TS code but has transcription errors (e.g., `* → [^\s]*` should be `* → .*`, `\s+` should be `\s`). Always verify against the TS reference when behavior seems wrong.

---

## Workflow Learnings

### Code review catches real bugs
Every code review in this session caught at least one meaningful issue:
- Task 7: Regex recompilation per call (P2)
- Task 8: Goroutine leak in lifecycle guard (P1), unused context parameter (P1), non-deterministic runtime list (P2), snippet offset bug (P2)
- Task 9: DRY violation in intent helpers (P1), silent error swallowing in batch search (P2), raw output leak on index failure (P2)

Don't skip reviews even when tests pass.

### Implementation review catches doc drift early
Running implementation-review after a batch of tasks catches spec-vs-code divergence before it compounds. The 8 findings from reviewing Tasks 7-9 were all doc fixes, not code fixes — meaning the code was correct but docs were stale. Fixing docs early prevents future implementers from building on wrong assumptions.

### Test helpers need cleanup hooks
Server tests that lazily initialize the ContentStore (SQLite) need `t.Cleanup(func() { srv.shutdown() })` to close the DB before `t.TempDir()` cleanup runs. Otherwise you get "directory not empty" errors from WAL files.
