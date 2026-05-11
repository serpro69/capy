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
1. Read and validate the `kind` argument **before** the TTL cache check (before line 52). Default to `store.KindEphemeral` if absent or empty. Validate explicitly against `"durable"`/`"ephemeral"` only (reject `"session"` and invalid values). This must happen first because the cache check needs to compare the requested kind against the cached kind.
2. Modify the TTL cache check (lines 52-68): after finding a cache hit, compare `meta.Kind` (available on `SourceMeta` at `internal/store/types.go:100`) against the requested kind. If they differ, bypass the cache and proceed with re-fetch+re-index so the kind change takes effect. This prevents the cache from silently swallowing kind changes (e.g., a previously-durable source that should now be ephemeral).
3. Replace all four `store.KindDurable` literals at lines 132, 138, 141, 145 with the resolved kind variable.
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
  - Fetches with `kind: "session"` → assert error response
  - Fetches a URL as durable, then re-fetches same URL with `kind: "ephemeral"` (within cache TTL) → assert cache is bypassed and kind is updated to ephemeral
  - Fetches a URL (default ephemeral), then re-fetches same URL with `kind: "durable"` → assert cache is bypassed and kind is updated to durable

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

With a summary-only approach using `CountSourcesByKind` (which returns source count only — section count is not needed for a directional summary):
```
durableCount, _ := st.CountSourcesByKind(store.KindDurable)
// Also count session if not excluded
// Format: "12 durable sources indexed. Refine your query terms, or use capy_stats for source details."
```

Count only non-excluded kinds (respect `ephemeralExcluded` and `sessionExcluded` flags already computed at lines 96-97). Format a single line with source counts per kind and a pointer to `capy_stats`. Section counts are intentionally omitted — the source count is sufficient to tell the agent "content exists, your query didn't match" and `capy_stats` provides the detailed breakdown.

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

**`internal/server/tools.go` — `toolSearch()` (lines 190-209):**
- Fix the main description (line 193): change `"By default returns only durable sources (fetched/indexed reference content)"` to `"By default returns durable and session sources"`. Remove the parenthetical that calls fetched content "reference content" — after this change, fetched content is ephemeral by default.
- Fix the `include_kinds` description (line 205): change `"Default: [\"durable\"] only"` to `"Default: [\"durable\", \"session\"]"` to match the actual behavior in `effectiveKindFilter` at `search.go:569`. Update the `"durable"` parenthetical from `"fetched/indexed reference content, retained by retention score"` to `"explicitly indexed reference content, retained by retention score"` since fetched content is no longer durable by default.

### What NOT to change

- `toolIndex()`, `toolExecuteFile()`, `toolStats()`, `toolDoctor()`, `toolCleanup()` — no changes needed.
- Tool annotations (read-only/destructive hints) — no changes.

### Verify

- `go build ./...` compiles
- Manual inspection: start capy MCP server, call `tools/list`, verify updated descriptions render correctly in the tool listing.

## Task 4: Full routing rewrite (AGENTS.md + generated routing blocks)

### Important: Three routing surfaces

There are three places where routing rules live — all three must be updated together:

1. **`.capy/AGENTS.md`** — the static file read by agents directly from the repo.
2. **`internal/platform/routing.go:18` — `GenerateRoutingInstructions()`** — generates the same routing block that `capy setup` writes into CLAUDE.md/AGENTS.md. Contains the identical "MANDATORY", "ONLY for", "Primary tool" language. This is the source-of-truth for the file-based routing block.
3. **`internal/hook/routing.go:6` — `RoutingBlock()`** — the XML routing block injected at runtime into subagent prompts (via `pretooluse.go:139`) and session start (via `sessionstart.go:6`). Contains "You MUST use capy", "Primary tool for research", "DO NOT use Bash for commands producing >20 lines", "Bash is ONLY for git/mkdir/rm/mv/navigation". Plus the guidance constants `BASH_GUIDANCE`, `GREP_GUIDANCE`, `READ_GUIDANCE` at lines 57-84.

Rewriting only AGENTS.md leaves the runtime-injected routing (`RoutingBlock()`) and the setup-generated routing (`GenerateRoutingInstructions()`) still pushing the old "capy-first" behavior. Subagents in particular get their routing from `RoutingBlock()`, not from AGENTS.md.

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

**`internal/platform/routing.go` — `GenerateRoutingInstructions()` (line 18-111):**
Rewrite the generated routing block to match the new AGENTS.md content. This function should produce the same task-aware routing text as AGENTS.md. The content is written to disk during `capy setup`, so it must be self-contained (no @-imports).

**`internal/hook/routing.go` — `RoutingBlock()` (line 6-54):**
Rewrite the XML routing block to match the new task-aware routing principle. Key changes:
- Replace `"You MUST use capy MCP tools"` with the comprehension-vs-extraction decision principle.
- Replace `"Primary tool for research"` in the hierarchy with extraction-focused guidance.
- Replace `"DO NOT use Bash for commands producing >20 lines"` with the nuanced rule (Bash for git, small commands, comprehension content).
- Replace `"Bash is ONLY for git/mkdir/rm/mv/navigation"` with the positive framing.

**`internal/hook/routing.go` — guidance constants (lines 57-84):**
Update `BASH_GUIDANCE` (line 76-84): replace `"Bash is best for: git, mkdir, rm, mv, navigation, and short-output commands only"` with task-aware guidance that doesn't imply Bash is a last resort.
Review `GREP_GUIDANCE` and `READ_GUIDANCE` — these may be fine as-is since they already provide nuanced advice.

### What NOT to change

- `CLAUDE.md` reference to AGENTS.md (`@.capy/AGENTS.md`) — keep the include.
- `.claude/CLAUDE.extra.md` — no changes needed.
- `internal/hook/pretooluse.go` and `internal/hook/sessionstart.go` — these are injection points, not content sources. They call `RoutingBlock()` which we're updating.
- `internal/platform/setup.go` — the setup logic that writes routing to disk and replaces stale blocks stays the same; only the content from `GenerateRoutingInstructions()` changes.

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
