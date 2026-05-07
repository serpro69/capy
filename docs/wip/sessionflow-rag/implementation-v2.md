# Sessionflow RAG v2 — Implementation Plan

> **Design:** [./design-v2.md](./design-v2.md)
> **Issue:** [#41](https://github.com/serpro69/capy/issues/41)
> **Created:** 2026-05-07

This plan is ordered for incremental development. Each task builds on the previous and can be verified independently.

## Prerequisites

Read these files before starting:
- `docs/wip/sessionflow-rag/design-v2.md` — this feature's design document
- `docs/wip/sessionflow-rag/design.md` — v1 design (context for what already exists)
- `internal/session/parse.go` — JSONL parser, `contentBlock`, `extractAssistantBlocks`, `buildTurnPairs`
- `internal/session/transcript.go` — `BuildTranscript`, `TurnOffset`, `[Tools: ...]` rendering
- `internal/session/chunk.go` — `ChunkSession`, `buildChunkTitle` (uses `ToolNames`)
- `internal/session/tools_test.go`, `parse_test.go`, `transcript_test.go` — existing test patterns

All tests require `-tags fts5` and `CAPY_DB_KEY` env var. Use `make test`.

---

## Phase 1: Extractor Registry

### 1.1 Action type and ToolExtractor interface

**New file:** `internal/session/tools.go`

Define the core types:

```go
type Action int

const (
    ActionSkip    Action = iota
    ActionEnrich
    ActionPromote
)
```

`ToolExtractor` is a struct with an `Extract` function and a default `Action`:

```go
type ToolExtractor struct {
    Extract func(input json.RawMessage) string
    Action  Action
}
```

The `Extract` function takes `json.RawMessage` and returns a formatted string. If extraction fails (malformed input), the function returns `""` — the caller treats empty string from a non-Skip extractor as a graceful degradation to name-only for Enrich, or silent drop for Promote.

**Verify:** compiles, no test needed yet.

### 1.2 ExtractorRegistry

**File:** `internal/session/tools.go`

The registry holds:
- `exact map[string]ToolExtractor` — exact name matches (e.g., `"Read"`, `"mcp__pal__chat"`)
- `fallbackAction Action` — what to do for unknown tools (default: `ActionSkip`)

Lookup method:

```go
func (r *ExtractorRegistry) Lookup(name string) (ToolExtractor, bool)
```

Returns the extractor and whether an explicit match was found. Lookup order: exact match only. No prefix matching needed — all PAL tools are registered individually by exact name.

A package-level `DefaultRegistry` variable is initialized in an `init`-like function (or a `NewDefaultRegistry()` constructor called at package level).

**Verify:** unit test in `tools_test.go` — exact match found, unknown tool returns not-found.

### 1.3 Individual extractors

**File:** `internal/session/tools.go`

Each extractor is a function that unmarshals only the fields it needs:

**Tier 1 — Promote (PAL conversational tools):**

All PAL conversational tools share the same extractor. The `prompt` field is extracted, truncated to 768 chars, and wrapped in delimiter blocks:

```
--- PAL: <subtool> ---
<prompt text, truncated to 768 chars>
... (truncated, N chars total)
--- End PAL ---
```

The `<subtool>` is derived from the tool name: strip `mcp__pal__` prefix. E.g., `mcp__pal__chat` → `chat`, `mcp__pal__thinkdeep` → `thinkdeep`.

Register exact names: `mcp__pal__chat`, `mcp__pal__thinkdeep`, `mcp__pal__codereview`, `mcp__pal__consensus`, `mcp__pal__analyze`, `mcp__pal__debug`, `mcp__pal__planner`, `mcp__pal__challenge`, `mcp__pal__secaudit`, `mcp__pal__refactor`.

Input struct: `struct{ Prompt string `json:"prompt"` }`

If `prompt` is empty or unmarshal fails, return `""`.

**Tier 2 — Enrich (navigation tools):**

- **Read:** Extract `file_path`. Format: `[Read: <file_path>]`. Input struct: `struct{ FilePath string `json:"file_path"` }`.
- **Write:** Same pattern, field `file_path`. Format: `[Write: <file_path>]`.
- **Edit:** Same pattern, field `file_path`. Format: `[Edit: <file_path>]`.
- **Grep:** Extract `pattern`. Format: `[Grep: <pattern>]`. Input struct: `struct{ Pattern string `json:"pattern"` }`.
- **Agent:** Extract `description` and `subagent_type`. Format: `[Agent: <type> — "<desc>"]`. Input struct: `struct{ Description string; SubagentType string }` with json tags. If `subagent_type` is empty, use `"Agent"`. If `description` is empty, use `"subagent task"`.

All Tier 2 extractors: if the relevant field is empty or unmarshal fails, return `""`.

**Verify:** unit tests in `tools_test.go` for each extractor:
- Valid input → expected format string
- Malformed JSON input → empty string (graceful)
- Empty/missing field → empty string
- PAL truncation: input >768 chars → truncated with suffix
- PAL truncation: input ≤768 chars → no suffix

### 1.4 Registry initialization

**File:** `internal/session/tools.go`

Create `NewDefaultRegistry()` that builds the full registry with all extractors from 1.3. Expose as package-level `DefaultRegistry`.

**Verify:** unit test — `DefaultRegistry.Lookup("Read")` returns Enrich extractor, `DefaultRegistry.Lookup("mcp__pal__chat")` returns Promote extractor, `DefaultRegistry.Lookup("mcp__capy__capy_search")` returns not-found (Skip).

---

## Phase 2: Parser Changes

### 2.1 Add Input field to contentBlock

**File:** `internal/session/parse.go`

Add `Input json.RawMessage json:"input"` to the `contentBlock` struct (currently at line 69-74). This captures tool_use arguments without parsing them.

**Verify:** existing tests pass unchanged — the new field is populated but not yet used.

### 2.2 Add ToolMeta to TurnPair and parsedMessage

**File:** `internal/session/parse.go`

Add `ToolMeta []string` to the `TurnPair` struct (alongside existing `ToolNames`). `ToolNames` continues to feed BM25 chunk title generation. `ToolMeta` feeds transcript rendering.

Add `toolMeta []string` to the `parsedMessage` struct.

**Verify:** existing tests pass unchanged — new fields are zero-valued.

### 2.3 Update extractAssistantBlocks to use registry

**File:** `internal/session/parse.go`

Change `extractAssistantBlocks` to accept the registry (or use `DefaultRegistry`) and process tool_use blocks through it:

For each `tool_use` block:
1. Call `registry.Lookup(b.Name)`.
2. If not found → Skip (do nothing, don't add to toolNames or toolMeta).
3. If found, call `extractor.Extract(b.Input)` to get formatted text.
4. Based on `extractor.Action`:
   - `ActionPromote` → append formatted text to `texts` slice (becomes part of AssistantText). Add name to `toolNames`.
   - `ActionEnrich` → if formatted text is non-empty, append to `toolMeta`. If empty, append name-only fallback `[<name>]` to `toolMeta`. Add name to `toolNames`.
   - `ActionSkip` → do nothing.

Updated return signature: `(text string, toolNames []string, toolMeta []string)`.

**Verify:** unit tests with synthetic content blocks:
- Block with `mcp__pal__chat` + input → returns promoted text in `text`, name in `toolNames`
- Block with `Read` + input → returns enriched line in `toolMeta`, name in `toolNames`
- Block with `mcp__capy__capy_search` → nothing in any return value
- Block with unknown tool → nothing in any return value
- Mixed blocks (text + PAL + Read) → text includes speech + PAL block, toolMeta has Read line

### 2.4 Wire toolMeta through buildTurnPairs

**File:** `internal/session/parse.go`

Update `buildTurnPairs` to accumulate `toolMeta` alongside `toolNames` and include it in the constructed `TurnPair`.

Declare `currentToolMeta []string` alongside `currentToolNames` (line 332). In the assistant message handler, append `msg.toolMeta` entries to `currentToolMeta` (mirroring line 364 for toolNames). In `flushPair`, include `currentToolMeta` in the constructed `TurnPair.ToolMeta`, then reset `currentToolMeta = nil` alongside the existing `currentToolNames = nil` reset (line 350). The reset is safety-critical — without it, metadata leaks between turn pairs.

The existing `flushPair` logic at line 335 checks `len(currentAssistantText) == 0`. This remains correct: Promote-tier tool content is already in `currentAssistantText` (injected by `extractAssistantBlocks`), so PAL-bearing turns survive. Pure Tier 2/Skip tool-only turns still have empty `currentAssistantText` and are correctly discarded.

`TotalAssistantChars` — the existing `totalChars += len(aText)` in `buildTurnPairs` automatically includes Tier 1 promoted text because it's part of `aText`. This is correct: Tier 1 content IS first-class assistant text and should count toward `IsIndexable`.

**Verify:** unit tests:
- Tool-only turn with PAL call → turn pair created, `AssistantText` contains PAL block
- Tool-only turn with only Read calls → no turn pair (discarded)
- `TotalAssistantChars` includes PAL block text
- Session with only PAL conversations → `IsIndexable` returns true if ≥200 chars

### 2.5 Update ParseSubagents to propagate ToolMeta

**File:** `internal/session/parse.go`

`ParseSubagents` (line 412-420) manually constructs new `TurnPair` structs, copying `HumanText`, `AssistantText`, `ToolNames`. It must also copy the new `ToolMeta` field. Without this, enriched metadata from sub-agent sessions is silently dropped.

**Verify:** unit test: parse a sub-agent JSONL containing Read calls → resulting TurnPair has populated `ToolMeta`.

---

## Phase 3: Transcript Changes

### 3.1 Replace [Tools: ...] with enriched metadata lines

**File:** `internal/session/transcript.go`

In `BuildTranscript`, replace the `[Tools: ...]` rendering (currently at line 56-58):

Old:
```go
if len(tp.ToolNames) > 0 {
    fmt.Fprintf(&b, "[Tools: %s]\n", strings.Join(tp.ToolNames, ", "))
}
```

New:
```go
for _, meta := range tp.ToolMeta {
    fmt.Fprintf(&b, "%s\n", meta)
}
```

This renders each enriched metadata line on its own line. PAL delimiter blocks are already part of `AssistantText` (injected during parsing), so they render naturally in the `Assistant: <text>` section.

Apply the same change in `writeSubagentBlock` (line 98-99) — sub-agent turns also get enriched metadata.

**Verify:** unit tests in `transcript_test.go`:
- Turn with `ToolMeta: ["[Read: parse.go]", "[Edit: types.go]"]` → two metadata lines
- Turn with PAL in AssistantText + Read in ToolMeta → PAL block appears in assistant section, Read line after
- Turn with empty ToolMeta → no metadata lines (clean)
- Sub-agent turn with ToolMeta → enriched lines inside sub-agent block

### 3.2 Update chunk title generation for PAL tools

**File:** `internal/session/chunk.go`

`buildChunkTitle` (line 94) uses `tp.ToolNames` to build the `| Tools: name1, name2` part of chunk titles. Currently it renders raw tool names, which produces `| Tools: mcp__pal__chat, Read`.

Update the title builder: when a tool name starts with `mcp__pal__`, strip the prefix and collect under a `PAL:` label (similar to the existing `Subagent:` label). Non-PAL tools render under `Tools:` as before. Example: `Session <datetime> | Turns 3-6 | PAL: chat | Tools: Read, Edit`.

**Verify:** unit test: turn pairs with `mcp__pal__chat` and `Read` in `ToolNames` → title contains `| PAL: chat | Tools: Read`.

---

## Phase 4: Sanitization and Re-index

### 4.1 Update sanitizeParsedSession for ToolMeta

**File:** `internal/session/sweep.go`

`sanitizeParsedSession` (line 335-339) currently sanitizes only `HumanText` and `AssistantText`. Add a loop over `ToolMeta` entries to apply `sanitize.StripSecrets` to each.

Note: Tier 1 promoted content (PAL blocks) is already covered because it's part of `AssistantText`.

**Verify:** unit test: TurnPair with `ToolMeta: ["[Grep: password=hunter2]"]` → sanitized. TurnPair with PAL block in AssistantText containing a secret → sanitized via existing path.

### 4.2 Expose re-index mechanism

**File:** `cmd/capy/sweep.go` (or extend existing CLI)

The sweep already supports `SweepOptions{Reindex: true}` which bypasses the mtime gate. Expose this via `capy sweep --reindex` CLI flag. The MCP server startup path continues to use `Sweep()` (no re-index) — the flag is for explicit one-time use after parser upgrades.

**Verify:** `capy sweep --reindex` re-parses all sessions regardless of mtime. Without the flag, unchanged sessions are skipped.

---

## Phase 5: Comparison Test

### 5.1 Baseline capture

Before merging v2 changes, capture the v1 indexed content for the current session (the session that produced these v2 design docs). Query via:

```
capy_search(
    queries: ["whitelist vs pattern-based extraction",
              "TotalAssistantChars contamination",
              "PAL design reasoning tool content",
              "parse.go tool_use contentBlock"],
    include_kinds: ["session"],
    source: "<this-session-uuid>"
)
```

Record: chunk count, chunk titles, content snippets, which queries return results.

### 5.2 Re-index with v2

After implementing all changes:
1. Delete the session's source entry (or modify the session file's mtime to force re-indexing).
2. Restart the MCP server to trigger a sweep.
3. The v2 parser produces enriched transcripts; content_hash mismatch triggers re-indexing.

### 5.3 Post-v2 capture

Run the same queries from 4.1. Record the same metrics.

### 5.4 Two-lens comparison (semi-manual)

**Correctness lens:**
- PAL delimiter blocks present in chunks?
- Enriched `[Read: ...]` lines instead of `[Tools: Read]`?
- `mcp__capy__*` calls absent from transcript?
- Bash calls absent from transcript?
- PAL-only turns preserved (not discarded)?
- Chunk count reasonable (higher than v1 due to more surviving turns)?

**Usefulness lens:**
- "whitelist vs pattern-based extraction" → finds the PAL discussion where this was debated?
- "TotalAssistantChars contamination" → surfaces the PAL round that caught this bug?
- "parse.go tool_use" → returns chunks showing which files were read during analysis?
- Overall: is the narrative more coherent than v1's sparse transcript?

Document findings. If usefulness doesn't improve meaningfully, reconsider the design before shipping.

---

## Phase 6: Final Verification

### 6.1 Full test suite

Run `make test` and `make test-race`. All existing tests must pass. No regressions.

### 6.2 Review and documentation

- Run `kk:review-code` on the final diff.
- Run `kk:review-spec` to verify implementation matches design-v2.md.
- Update CONTRIBUTING.md if the `internal/session/tools.go` pattern is worth documenting.
- Consider ADR if any v2 decisions override v1 design choices.
