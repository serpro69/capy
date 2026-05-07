# Sessionflow RAG v2 — Design Document

> **Issue:** [#41](https://github.com/serpro69/capy/issues/41)
> **Predecessor:** [design.md](./design.md) (v1, implemented)
> **Status:** Draft
> **Created:** 2026-05-07

## Problem

Session indexing v1 captures human/assistant conversational text and extracts tool names as metadata (`[Tools: Read, Bash]`), but drops all tool_use input content. This was an intentional design choice — raw tool outputs flood context and most tool calls are navigation noise.

However, v1 cannot distinguish between noise tools and reasoning tools. A `mcp__pal__chat` call carrying three paragraphs of design trade-off analysis is filtered the same way as 15 consecutive Read calls exploring a codebase. The result: sessions rich in tool-mediated reasoning index as sparse transcripts that miss the most valuable content.

### Findings from Issue #41

Tested against session `b5ee5362` (sessionflow-RAG v1 design session): 336 JSONL lines, 135 user messages, 201 assistant messages, ~100 exchanges.

1. **Tool-only turns invisible (~60% of turns):** Assistant turns that are pure tool_use with no text show as empty. The exploration phase is almost entirely lost.
2. **MCP tool content stripped:** `mcp__pal__chat` carries full design reasoning in `input.prompt`, but only the tool name survives. Multiple PAL review exchanges with significant design analysis reduce to "I consulted PAL."
3. **Coarse chunking merges intermediate exchanges:** ~100 exchanges compressed to ~10 chunks. Some intermediate exchanges fall between chunk boundaries.
4. **User tool_result messages empty:** Expected behavior, but contributes to sparseness.

### Root Causes in Code

1. **`contentBlock` struct** (`parse.go:69-74`): Declares `Type`, `Text`, `Name`, `ID` — no `Input` field. When `json.Unmarshal` encounters the `input` key in a tool_use JSON block, it silently discards it.
2. **`extractAssistantBlocks()`** (`parse.go:305-322`): For `tool_use` blocks, only extracts `b.Name`. Even with an `Input` field, this function would ignore it.
3. **`buildTurnPairs()`** (`parse.go:326-378`): The `flushPair()` closure at line 335 checks `len(currentAssistantText) == 0`. Tool-only turns have empty assistant text, so the entire turn — including accumulated tool names — is discarded.

## Solution

**Selective enrichment** — not capturing all tool content, but promoting high-signal tool inputs to first-class transcript content and enriching existing turns with better tool metadata.

Three categories of change:
1. **Promote** reasoning tools whose inputs contain human-readable design discussion — these become transcript content with delimiter blocks.
2. **Enrich** navigation tools on text-bearing turns — metadata lines gain specificity (`[Read: internal/store/types.go]` instead of `[Tools: Read]`).
3. **Skip** everything else — self-referential tools, infrastructure, unevaluated tools.

Tool-only turns (pure Read/Bash sequences with no assistant text) remain discarded — exploration noise is not enrichment. The exception: tool-only turns containing Promote-tier tools survive, because the promoted content becomes the assistant text.

## Design Decisions

### Decision 1: Extractor Registry (Table-Driven, Not Pattern-Based)

A new file `internal/session/tools.go` houses a table-driven registry mapping tool names to extraction behavior.

**Why table-driven, not pattern-based:** MCP namespaces don't reliably predict input format. `mcp__pal__chat` has human-readable text; `mcp__serena__find_symbol` has structured JSON; `mcp__pal__listmodels` is a utility call with no reasoning content. A prefix match on `mcp__pal__*` would incorrectly promote `listmodels` and `version`. Making tool classification explicit in a table is honest about what we know.

**Why not a "smart" fallback that walks JSON:** We considered a fallback that extracts short string values from unknown tool inputs. Rejected on YAGNI grounds — most unknown tools have structured JSON where extracting random field values adds noise to BM25 term frequencies (`"mode": "fast"`, `"uuid": "abc-123"`). The marginal benefit doesn't justify the complexity and test surface.

**Why default to Skip, not name-only:** The registry is the exhaustive policy of what enters the index. Unevaluated tools should not contribute content — if a tool turns out to be valuable, adding an extractor is a one-line table entry. An unknown tool name in a chunk title wasn't helping retrieval anyway, and the assistant's text already describes what it did.

### Decision 2: Three-Tier Action Model

Each extractor returns a `(text, Action)` tuple where Action is one of:

- **`Promote`** — Content becomes part of `AssistantText`. Creates searchable transcript content. Tier 1 content counts toward `IsIndexable` thresholds because it IS first-class content.
- **`Enrich`** — Content becomes a metadata line (`[Read: path]`) appended after assistant text. Does NOT create turns — only decorates text-bearing turns. Does NOT count toward `IsIndexable`. Extractors return empty string on malformed/missing input; Enrich falls back to `[<Name>]` (name-only).
- **`Skip`** — Tool is dropped entirely. No metadata, no content, no ToolNames entry.

Extractors are pure functions that return empty string on failure. For Promote, empty string silently drops the block (no partial content). For Enrich, empty string degrades to name-only metadata.

**Why Tier 1 content counts toward `IsIndexable`:** A session that is pure PAL design consultation with no "natural" assistant text is still a design session worth indexing. The gate should treat promoted content as first-class. (Identified during PAL review — without this, PAL-only sessions would be incorrectly filtered out.)

### Decision 3: Tier 1 — Promote Conversational Tools (Exact Matches)

Only PAL tools with conversational/reasoning content are promoted. Each is registered by **exact name**, not by prefix:

| Tool | Input field | Promoted |
|------|-------------|----------|
| `mcp__pal__chat` | `prompt` | Yes |
| `mcp__pal__thinkdeep` | `prompt` | Yes |
| `mcp__pal__codereview` | `prompt` | Yes |
| `mcp__pal__consensus` | `prompt` | Yes |
| `mcp__pal__analyze` | `prompt` | Yes |
| `mcp__pal__debug` | `prompt` | Yes |
| `mcp__pal__planner` | `prompt` | Yes |
| `mcp__pal__challenge` | `prompt` | Yes |
| `mcp__pal__secaudit` | `prompt` | Yes |
| `mcp__pal__refactor` | `prompt` | Yes |
| `mcp__pal__listmodels` | — | No (utility, no reasoning) |
| `mcp__pal__version` | — | No (utility) |

**Format:** Sub-agent-style delimiter blocks, reusing the existing transcript pattern:
```
--- PAL: chat ---
Review this design for session indexing. Consider whether the
chunking strategy preserves enough context for BM25 retrieval...
(truncated, 2341 chars total)
--- End PAL ---
```

**Why sub-agent-style delimiters:** Reuses a pattern already in the transcript (sub-agent blocks). Makes PAL exchanges visually distinct, structurally parseable, and searchable via BM25 title boost (`| PAL: chat` in chunk titles).

**Truncation: 768 characters.** The prompt's opening states the question/goal — the part someone would search for. The tail is often pasted code or repeated context. 768 chars captures the opening plus a full paragraph of reasoning while leaving ~80% of a 4096-byte chunk for surrounding conversation.

A `... (truncated, N chars total)` suffix is appended when truncation occurs, preserving the prompt's weight footprint.

**Why only the prompt, not the tool_result (PAL's response):** Deferred to v3. Three reasons:
1. The assistant's next text turn synthesizes the PAL response — indexing both creates heavy duplication.
2. `tool_result` blocks are in user messages. Correlating them with originating `tool_use` via `tool_use_id` across message boundaries requires parser changes beyond v2 scope.
3. Users search for intent ("how did we decide on whitelist?") more than for specific answer content. The prompt captures intent.

### Decision 4: Tier 2 — Enrich Navigation Tools

| Tool | Input field | Format |
|------|-------------|--------|
| `Read` | `file_path` | `[Read: <path>]` |
| `Write` | `file_path` | `[Write: <path>]` |
| `Edit` | `file_path` | `[Edit: <path>]` |
| `Grep` | `pattern` | `[Grep: <pattern>]` |
| `Agent` | `description`, `subagent_type` | `[Agent: <type> — "<desc>"]` |

These replace the v1 `[Tools: Read, Bash]` comma-separated line with per-tool enriched metadata lines. They don't create turns — tool-only sequences of Tier 2 calls remain discarded.

**Why Bash is Skip, not Enrich:** Most Bash commands are routine navigation (`git log`, `git status`, `ls`, `mkdir`) where the command string doesn't add search value. The assistant's text already describes what it did ("let me check the git history"). Unlike Read/Write/Edit where the file path has high specificity ("which session touched parse.go?"), Bash commands are too varied and often too noisy. Filtering git commands from non-git commands adds complexity for marginal gain.

**Why Agent uses description-only:** The `Agent` tool's `input.prompt` contains the delegation instruction, but sub-agent sessions are already parsed from `<uuid>/subagents/agent-*.jsonl` and appended to the transcript. Including the full Agent prompt would create redundancy (intent vs execution). The `description` field (~50 chars) captures delegation intent concisely.

### Decision 5: Skip Everything Else

| Pattern | Reason |
|---------|--------|
| `mcp__capy__*` | Self-referential (capy indexing its own tool calls) |
| `mcp__claude_ai_*` | Authentication infrastructure noise |
| `mcp__serena__*` | Structured JSON inputs (name paths, file paths) — lower search value than Read/Edit paths and high noise from nested symbol identifiers |
| `Bash` | Routine navigation, noisy commands (see Decision 4) |
| All unknown tools | Unevaluated — not in policy table |

The registry is the single source of truth. If a tool isn't in the table, it doesn't enter the index.

### Decision 6: No Chunking Changes

Window stays at 4 turn pairs / 1 overlap / step 3. With enriched content, turns are larger and more turns survive (PAL-bearing tool-only turns), so more chunks are produced — which is desirable (finer granularity). The existing oversized-split mechanism handles edge cases where enriched turns exceed 4096 bytes.

Tuning chunk parameters before evaluating real-world BM25 retrieval impact of enriched content would be premature optimization.

### Decision 7: Transcript Format

Tier 2 enriched lines **replace** the v1 `[Tools: Name, Name]` format — the enriched lines carry strictly more information. A turn with both Tier 1 and Tier 2 content renders as:

```
Human: Check the design
Assistant: Let me review the design with PAL and check the implementation.
--- PAL: chat ---
Review this design for session indexing...
(truncated, 1847 chars total)
--- End PAL ---
[Read: internal/session/parse.go]
[Read: internal/session/chunk.go]
```

Order: assistant speech → promoted blocks → enriched metadata lines. Bottom-loading of metadata (all Tier 2 after Tier 1) is acceptable — Claude typically places tool calls after the text block in a single response.

**Ordering assumption:** The parser processes `mergedBlocks` in JSONL line order. This produces speech-before-tools ordering because Claude currently emits text blocks before tool_use blocks in progressive snapshots. If Claude's emission order changes, the parser would need an explicit sort-by-type step. This is low risk — the ordering has been stable across all observed session files.

### Decision 8: tool_result Capture Deferred to v3

The PAL response (returned as `tool_result` in the subsequent user message) is not captured in v2. The assistant's synthesis is the primary searchable artifact. If real-world search quality suffers from missing PAL responses, v3 adds `tool_use_id` correlation across message boundaries.

## Scope & Integration Points

All changes are confined to `internal/session/`:

| File | Change |
|------|--------|
| `internal/session/tools.go` | **New.** `Action` enum, `ToolExtractor` type, `ExtractorRegistry`, all extractor functions |
| `internal/session/parse.go` | Add `Input json.RawMessage` to `contentBlock`. Update `extractAssistantBlocks` to call registry and return `toolMeta`. Update `TurnPair` to include `ToolMeta`. |
| `internal/session/transcript.go` | Replace `[Tools: ...]` line with per-tool enriched metadata lines. Render PAL delimiter blocks from `AssistantText`. |
| `internal/session/tools_test.go` | **New.** Unit tests for each extractor and registry lookup. |
| `internal/session/parse_test.go` | Update for new behavior: PAL turns survive, enriched metadata, `TotalAssistantChars` includes Tier 1. |
| `internal/session/transcript_test.go` | Update for enriched metadata format and PAL blocks. |

**Not changed:** store, schema, search, cleanup, MCP tools, chunking parameters, CLI.

## Re-indexing

The sweep's mtime gate (`shouldSkip` in `sweep.go:248`) compares file modification time against the stored `indexed_at` timestamp. If the JSONL file hasn't been modified, the session is skipped **before** parsing — the content_hash in `index.go:93` is never reached. A parser-only change (like v2) produces different transcripts for the same unchanged files, but the mtime gate prevents re-parsing.

The sweep already supports a `SweepOptions{Reindex: true}` flag (`sweep.go:60-62`) that bypasses the mtime gate. The `SweepWithOptions` function at line 151 respects it. However, this is not currently exposed through the MCP server startup path (which calls `Sweep()` with default options).

**Required for v2:** Expose a one-time re-index mechanism. Options (choose one during implementation):
1. **CLI flag:** `capy sweep --reindex` — explicit, user-controlled.
2. **Parser-version salt:** Append a version string to the content hash so parser changes produce different hashes. Automatic but adds permanent overhead.
3. **Documented manual step:** Instruct users to run `capy cleanup --kind session` to purge all session sources, then restart the server. Simplest, no code change.

Option 1 is preferred — it uses the existing `SweepOptions.Reindex` mechanism and makes the re-index explicit.

## Secret Sanitization

The existing `sanitizeParsedSession` (`sweep.go:335-339`) strips secrets from `HumanText` and `AssistantText`. The new `ToolMeta` field must also be sanitized — a Grep pattern like `[Grep: password=hunter2]` or an Agent description containing secrets would otherwise pass through unsanitized.

**Required:** Update `sanitizeParsedSession` to iterate `ToolMeta` entries and apply `sanitize.StripSecrets` to each. Add test coverage for secrets in Grep patterns and Agent descriptions.

Note: Tier 1 promoted content (PAL blocks) is already covered because it's injected into `AssistantText` during parsing — `sanitizeParsedSession` sanitizes it along with the rest of the assistant text.

## Chunk Title Formatting

The v1 chunk title format is `Session <datetime> | Turns <start>-<end> | Tools: mcp__pal__chat, Read`. The `buildChunkTitle` function (`chunk.go:94`) renders raw `ToolNames` directly.

For v2, promoted PAL tools should appear as `| PAL: chat` (not `| Tools: mcp__pal__chat`) in chunk titles for cleaner BM25 boosting. This requires a small update to `buildChunkTitle`: when rendering tool names, strip `mcp__pal__` prefix for promoted tools and group them under a `PAL:` label, similar to the existing `Subagent:` label.

## Open Questions

- **tool_result capture (v3):** If search quality evaluation shows missing PAL responses hurt retrieval, add `tool_use_id` correlation in the parser. Tracked as a known limitation.
- **Re-index mechanism:** Option 1 (CLI flag) preferred, but decision deferred to implementation.

## References

- [GitHub issue #41](https://github.com/serpro69/capy/issues/41) — Session indexing loses tool call content and PAL conversations
- [design.md](./design.md) — v1 design (predecessor)
- [implementation.md](./implementation.md) — v1 implementation plan
- PAL consultation log: 2 sessions, 6 rounds total. Key decisions: whitelist over pattern-based (round 1), YAGNI fallback (round 2), 768-char truncation compromise (round 3), Tier 1 counts toward IsIndexable (round 4, caught by PAL), Bash demoted to Skip (round 6, caught by user)
