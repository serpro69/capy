# Tasks: Sessionflow RAG v2 — Tool Content Enrichment

> Design: [./design-v2.md](./design-v2.md)
> Implementation: [./implementation-v2.md](./implementation-v2.md)
> Status: pending
> Created: 2026-05-07

## Task 0: Capture v1 baseline for comparison test
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation-v2.md#phase-5-comparison-test](./implementation-v2.md#phase-5-comparison-test)

### Subtasks
- [x] 0.1 Capture v1 baseline: run `go run -tags fts5 ./cmd/dump-session/ .capy/knowledge.db b73c8a63 > docs/wip/sessionflow-rag/comparison-baseline-session.md` to dump all chunks for session `b73c8a63` (the v2 design session). This file is consumed by Task 5 for the two-lens comparison. This MUST run before any code changes to capture the v1 index state.

## Task 1: Extractor registry and tool extractors
- **Status:** done
- **Depends on:** —
- **Docs:** [implementation-v2.md#phase-1-extractor-registry](./implementation-v2.md#phase-1-extractor-registry)

### Subtasks
- [x] 1.1 Create `internal/session/tools.go` with `Action` enum (`ActionSkip`, `ActionEnrich`, `ActionPromote`), `ToolExtractor` struct (`Extract func(json.RawMessage) string`, `Action`), and `ExtractorRegistry` struct with `Lookup(name) (ToolExtractor, bool)` method
- [x] 1.2 Implement shared PAL extractor: unmarshal `prompt` field, truncate to 768 chars with `... (truncated, N chars total)` suffix, wrap in `--- PAL: <subtool> ---` / `--- End PAL ---` delimiters. Register for exact names: `mcp__pal__chat`, `mcp__pal__thinkdeep`, `mcp__pal__codereview`, `mcp__pal__consensus`, `mcp__pal__analyze`, `mcp__pal__debug`, `mcp__pal__planner`, `mcp__pal__challenge`, `mcp__pal__secaudit`, `mcp__pal__refactor`. All with `ActionPromote`.
- [x] 1.3 Implement Tier 2 extractors: `Read` (`file_path` → `[Read: <path>]`), `Write` (`file_path`), `Edit` (`file_path`), `Grep` (`pattern`), `Agent` (`description` + `subagent_type` → `[Agent: <type> — "<desc>"]`). All with `ActionEnrich`.
- [x] 1.4 Create `NewDefaultRegistry()` constructor that builds the full registry. Expose as package-level `DefaultRegistry`.
- [x] 1.5 Write unit tests in `internal/session/tools_test.go`: each extractor with valid input, malformed input, empty fields. PAL truncation at boundary (≤768 no suffix, >768 with suffix). Registry lookup: exact match found, unknown tool not found. Test that `mcp__capy__capy_search` and `Bash` are not in the registry.

## Task 2: Parser changes — contentBlock, extractAssistantBlocks, TurnPair
- **Status:** pending
- **Depends on:** Task 1
- **Docs:** [implementation-v2.md#phase-2-parser-changes](./implementation-v2.md#phase-2-parser-changes)

### Subtasks
- [ ] 2.1 Add `Input json.RawMessage json:"input"` to `contentBlock` struct in `internal/session/parse.go` (line 69-74). Verify existing tests pass unchanged.
- [ ] 2.2 Add `ToolMeta []string` field to `TurnPair` struct (alongside existing `ToolNames`). Add `toolMeta []string` to `parsedMessage` struct.
- [ ] 2.3 Update `extractAssistantBlocks` to accept/use `DefaultRegistry`. For each `tool_use` block: call `registry.Lookup(b.Name)`, route on action (Promote → append to texts + toolNames, Enrich → append to toolMeta + toolNames, Skip → do nothing). Return updated signature `(text string, toolNames []string, toolMeta []string)`.
- [ ] 2.4 Update `buildTurnPairs` to accumulate and pass through `toolMeta` into `TurnPair.ToolMeta`. Verify: `flushPair` logic unchanged — PAL-bearing turns survive because promoted text is in `currentAssistantText`; pure Tier 2 tool-only turns still discarded.
- [ ] 2.5 Update `ParseSubagents` (parse.go:412-420) to copy `ToolMeta` field when wrapping sub-agent turn pairs. Currently only copies `HumanText`, `AssistantText`, `ToolNames` — `ToolMeta` would be silently dropped without this.
- [ ] 2.6 Update tests in `internal/session/parse_test.go`: tool-only turn with PAL → turn survives with PAL block as AssistantText. Tool-only turn with only Read → discarded. Mixed turn (text + PAL + Read) → correct content. `TotalAssistantChars` includes Tier 1. Session with only PAL conversations meeting threshold → `IsIndexable` returns true. Sub-agent turn pairs include `ToolMeta`.

## Task 3: Transcript and chunk title changes
- **Status:** pending
- **Depends on:** Task 2
- **Docs:** [implementation-v2.md#phase-3-transcript-changes](./implementation-v2.md#phase-3-transcript-changes)

### Subtasks
- [ ] 3.1 In `BuildTranscript` (`internal/session/transcript.go`), replace `[Tools: ...]` comma-separated line with per-line rendering of `tp.ToolMeta` entries. Apply same change in `writeSubagentBlock`.
- [ ] 3.2 Update `buildChunkTitle` in `chunk.go` to format PAL tools as `| PAL: <subtool>` (strip `mcp__pal__` prefix) instead of raw `| Tools: mcp__pal__chat`. Non-PAL tools continue under `| Tools:`. Similar to existing `| Subagent:` label pattern.
- [ ] 3.3 Update tests in `internal/session/transcript_test.go`: turns with ToolMeta → enriched metadata lines. Turns with PAL in AssistantText → PAL delimiter blocks in assistant section. Empty ToolMeta → no metadata lines. Sub-agent turns with ToolMeta → enriched lines inside sub-agent block.
- [ ] 3.4 Update tests in `internal/session/chunk_test.go`: turn pairs with `mcp__pal__chat` and `Read` in `ToolNames` → chunk title contains `| PAL: chat | Tools: Read`.

## Task 4: Sanitization and re-index mechanism
- **Status:** pending
- **Depends on:** Task 2
- **Docs:** [implementation-v2.md#phase-4-sanitization-and-re-index](./implementation-v2.md#phase-4-sanitization-and-re-index)

### Subtasks
- [ ] 4.1 Update `sanitizeParsedSession` in `internal/session/sweep.go` (line 335-339) to also sanitize `ToolMeta` entries via `sanitize.StripSecrets`. Tier 1 content is already covered (it's in `AssistantText`).
- [ ] 4.2 Add tests: TurnPair with `ToolMeta: ["[Grep: password=hunter2]"]` → sanitized. Agent description containing secret → sanitized.
- [ ] 4.3 Expose `capy sweep --reindex` CLI flag that calls `SweepWithOptions` with `Reindex: true`. The existing `SweepOptions.Reindex` field already bypasses the mtime gate — just needs CLI wiring.
- [ ] 4.4 Add test: `capy sweep --reindex` re-parses sessions regardless of mtime. Without flag, unchanged sessions are skipped.

## Task 5: Comparison test (semi-manual)
- **Status:** pending
- **Depends on:** Task 0, Task 1, Task 2, Task 3, Task 4
- **Docs:** [implementation-v2.md#phase-5-comparison-test](./implementation-v2.md#phase-5-comparison-test)

### Subtasks
- [ ] 5.1 Re-index with v2: run `capy sweep --reindex` to force re-parse all sessions with the v2 parser.
- [ ] 5.2 Capture v2 results: run `go run -tags fts5 ./cmd/dump-session/ .capy/knowledge.db b73c8a63 > docs/wip/sessionflow-rag/comparison-v2-session.md` to dump the re-indexed session. Compare against `comparison-baseline-session.md` from Task 0.
- [ ] 5.3 Correctness comparison: PAL delimiter blocks present? Enriched `[Read: ...]` lines? `mcp__capy__*` absent? Bash absent? PAL-only turns preserved? Chunk count reasonable? Compare both dumps against the raw JSONL gists for ground truth: [v2 design session (b73c8a63)](https://gist.github.com/serpro69/4a9cd49433fd447cd234db7ab88ac6ea), [v1 design session (b5ee5362)](https://gist.github.com/serpro69/5f5bef3734ddd548b0881c0f03802980).
- [ ] 5.4 Usefulness comparison: do the specific queries find the PAL discussions? Does the narrative coherence improve? Document findings. If usefulness doesn't improve meaningfully, reconsider before shipping.

## Task 6: Final verification
- **Status:** pending
- **Depends on:** Task 0, Task 1, Task 2, Task 3, Task 4, Task 5

### Subtasks
- [ ] 6.1 Run `test` skill — full test suite (`make test`, `make test-race`), verify no regressions
- [ ] 6.2 Run `document` skill — update CONTRIBUTING.md if the `tools.go` extractor pattern is worth documenting
- [ ] 6.3 Run `review-code` skill with Go input to review the implementation
- [ ] 6.4 Run `review-spec` skill to verify implementation matches design-v2.md and implementation-v2.md
- [ ] 6.5 Delete `cmd/dump-session/` — temporary utility, not part of the shipped CLI
