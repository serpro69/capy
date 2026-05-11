# Implementation Plan: Fix over-preservation and over-routing (Issue #44)

**Design:** [design.md](design.md)
**ADR:** [023](../../adr/023-fetch-ephemeral-default-and-routing-rewrite.md)

## Prerequisites

- Familiarity with capy's MCP tool registration in `internal/server/tools.go`
- Understanding of the source-kind system from ADR-017: `kind` column on `sources` table, `KindDurable`/`KindEphemeral`/`KindSession` constants in `internal/store/schema.go`
- Understanding of `effectiveKindFilter` in `internal/store/search.go:558-571`

## Task 1: Add `kind` parameter to `capy_fetch_and_index`

### What to change

**`internal/server/tools.go` — `toolFetchAndIndex()` function (line 211-226):**
Add a new optional `kind` string parameter with enum `["durable", "ephemeral"]`. Update the tool description to mention ephemeral default and the `source:` filter pattern for follow-up search.

**`internal/server/tool_fetch.go` — `handleFetchAndIndex()` function (line 33-165):**
1. Read the `kind` argument from request. Default to `store.KindEphemeral` if absent or empty.
2. Validate: must be `"durable"` or `"ephemeral"` (use `store.SourceKind.Valid()` — but note it also accepts `"session"`, so validate explicitly against the two allowed values).
3. Replace all three `store.KindDurable` literals at lines 132, 138, 141, 145 with the resolved kind variable.
4. Update the response text (line 159-163) to indicate whether content was indexed as durable or ephemeral, and remind about `source:` filter for follow-up queries on ephemeral content.

### What NOT to change

- `internal/server/tool_index.go` — `handleIndex()` stays hardcoded `KindDurable`.
- `internal/server/tool_batch.go` — `handleBatchExecute()` stays hardcoded `KindEphemeral`.
- `internal/store/` — no store changes. `Index`, `IndexJSON`, `IndexPlainText` already accept `kind` parameter.

### Verify

- `go build ./...` compiles
- `go test -tags fts5 ./internal/server/...` passes
- Write a test in `tool_fetch_test.go` that:
  - Fetches a URL with no `kind` param → assert stored as ephemeral
  - Fetches with `kind: "durable"` → assert stored as durable
  - Fetches with `kind: "ephemeral"` → assert stored as ephemeral
  - Fetches with `kind: "invalid"` → assert error response

## Task 2: Cap search fallback source listing

### What to change

**`internal/server/tool_search.go` — no-results source listing (lines 184-199):**

Replace the current block:
```
sources, _ := st.ListSources()
var parts []string
for _, src := range sources {
    // ... format each source
}
if len(parts) > 0 {
    output += "\n\nIndexed sources: " + strings.Join(parts, ", ")
}
```

With a summary-only approach using `CountSourcesByKind`:
```
durableCount, _ := st.CountSourcesByKind(store.KindDurable)
// Also count session if not excluded, for a combined total
// Format: "12 durable sources indexed. Refine your query terms, or use capy_stats for source details."
```

Count only non-excluded kinds (respect `ephemeralExcluded` and `sessionExcluded` flags already computed at lines 96-97). Format a single line with the count and a pointer to `capy_stats`.

The `ListSources()` call is no longer needed in this path — remove it. If `ListSources` is unused elsewhere after this change, leave it (it's a store-layer method that may have other consumers or future uses).

### What NOT to change

- Per-query "ephemeral excluded" / "session excluded" hints at lines 117-151 — these are targeted and actionable. Keep them.
- Throttle warnings at lines 172-177 — keep them.
- The `ListSources` store method itself — don't delete it; other code paths or `capy_stats` may use it.

### Verify

- `go test -tags fts5 ./internal/server/...` passes
- Write/update a test that triggers the no-results path with multiple indexed sources and asserts:
  - Output contains a count summary (e.g., "12 durable sources")
  - Output does NOT contain individual source labels
  - Output still contains the ephemeral/session hints when applicable

## Task 3: Update tool descriptions in `tools.go`

### What to change

**`internal/server/tools.go` — `toolExecute()` (lines 117-143):**
- Remove "MANDATORY: Use for any command where output exceeds 20 lines." prefix.
- Remove "git queries (git log, git diff)" from the example list.
- Reframe description: "Execute code in a sandboxed subprocess for large-output extraction. Only stdout enters context — raw data stays in the subprocess. Best for: API calls, broad searches (rg, grep), log analysis, data processing, and commands producing hundreds+ lines where you only need extracted facts. NOT for: git commands (use Bash), small-output commands (<50 lines, use Bash), content you need to comprehend fully (use Read/Bash)."

**`internal/server/tools.go` — `toolBatchExecute()` (lines 228-253):**
- Remove "THIS IS THE PRIMARY TOOL." from description.
- Reframe: "Execute multiple commands in ONE call, auto-index all output, and search with multiple queries. Returns search results directly — no follow-up calls needed. Best for broad exploration passes where you need to run multiple commands and extract specific answers from combined output. NOT for: git diffs, small commands, or content you need to read fully."

**`internal/server/tools.go` — `toolFetchAndIndex()` (lines 211-226):**
- Update description to mention ephemeral default: "Fetches URL content, converts HTML to markdown, indexes as ephemeral (24h TTL, excluded from default search), and returns a ~3KB preview. Use source: filter or include_kinds for follow-up search. Pass kind: 'durable' for reference docs you want to persist across sessions."

### What NOT to change

- `toolSearch()`, `toolIndex()`, `toolExecuteFile()`, `toolStats()`, `toolDoctor()`, `toolCleanup()` — no changes needed.
- Tool annotations (read-only/destructive hints) — no changes.

### Verify

- `go build ./...` compiles
- Manual inspection: start capy MCP server, call `tools/list`, verify updated descriptions render correctly in the tool listing.

## Task 4: Full AGENTS.md rewrite

### What to change

**`.capy/AGENTS.md` — complete rewrite.**

The new document structure:

1. **Header and purpose.** Capy MCP tools are available for context-window protection. One sentence on what capy does.

2. **Decision principle.** "Choose the tool based on what you need from the output: *comprehension* (you need to understand the full content) → direct tools (Bash, Read). *Extraction* (you need specific facts from large output) → capy tools."

3. **When to use direct tools (Bash / Read).** Positive list:
   - Git commands: diffs, logs, status, branch — always Bash. Diffs are comprehension content; BM25 fragments destroy review quality.
   - Small-output commands (<~50 lines): ls, wc, file, git status — Bash directly.
   - Files you need to comprehend or edit: Read tool. Required before Edit.
   - Sequential/ordered content: test output, build logs where order matters — Bash or Read.
   - Instruction files, checklists, configs: Read and internalize whole (carry forward existing anti-pattern from current AGENTS.md).

4. **When to use capy tools.** Positive list:
   - `capy_batch_execute`: Broad exploration — multiple commands + queries in one call. Example: initial repo scan with `rg --files` + symbol searches.
   - `capy_execute` / `capy_execute_file`: Single command or file producing hundreds+ lines where you only need extracted facts. API calls, log analysis, data processing.
   - `capy_fetch_and_index`: Fetch web content for reference lookup. Default ephemeral (24h). Pass `kind: "durable"` for reference docs (API docs, library guides, specs). Use `source: "<label>"` for follow-up search.
   - `capy_index`: Persist curated knowledge durably. For content you explicitly want searchable across sessions.
   - `capy_search`: Query indexed content. Batch all questions as array. Use `source:` to scope. Default excludes ephemeral — use `include_kinds` or `source:` to include.

5. **Blocked commands.** Keep: curl/wget, inline HTTP, WebFetch blocks. These are genuine SSRF/context-flood protections.

6. **Source kinds table.** Updated to reflect fetch → ephemeral default. Include guidance on fetch-then-search pattern (use `source:` filter).

7. **Read vs capy_execute_file.** Carry forward existing guidance — it's already well-written. "Default to Read. Reach for capy_execute_file only when file is genuinely large AND you want a derived answer AND you can write the exact script upfront."

8. **Output constraints.** Carry forward: 500-word limit, artifacts to files, descriptive source labels.

9. **Subagent routing.** Carry forward: routing block auto-injected, Bash agents upgraded to general-purpose.

10. **capy commands table.** Carry forward: `capy stats`, `capy doctor`.

### What NOT to change

- `CLAUDE.md` reference to AGENTS.md (`@.capy/AGENTS.md`) — keep the include.
- `.claude/CLAUDE.extra.md` — no changes needed.
- Hook configurations or settings — no changes.

### Verify

- Read the final AGENTS.md as an AI agent would: does the routing guidance unambiguously tell you to use Bash for `git diff` during code review? Does it tell you to use capy for a broad `rg` search across a large repo? If both answers are yes, the rewrite works.
- Verify no orphaned references: if the current AGENTS.md is referenced by other docs, update those references.

## Task 5: Final verification

### What to do

1. Run `go build ./...` — verify compilation.
2. Run `go test -tags fts5 ./...` — verify all tests pass.
3. Start capy as MCP server, call `tools/list`, verify tool descriptions.
4. Run `kk:review-code` to review all changes.
5. Run `kk:test` to verify test coverage.
6. Run `kk:document` to update any relevant documentation.
7. Run `kk:review-spec` to verify implementation matches this design doc and ADR-023.
