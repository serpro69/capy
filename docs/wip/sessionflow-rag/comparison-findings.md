# Sessionflow RAG v2 — Comparison Findings

> Session: `b73c8a63-0710-494e-9748-1bcce18ddfef` (v2 design session)
> v1 baseline: `comparison-baseline-session.md` (captured pre-v2, Task 0)
> v2 output: `comparison-baseline-session-v2.md` (captured post-reindex)
> Date: 2026-05-07

## Metrics

| Metric | v1 | v2 | Delta |
|--------|----|----|-------|
| Chunks (DB header) | 31 | 34 | +3 |
| Enriched `[Read: ...]` lines | 0 | 41 | +41 |
| Enriched `[Agent: ...]` lines | 0 | 2 | +2 |
| PAL delimiter blocks (`--- PAL: ---`) | 0 | 6 | +6 |
| `mcp__capy__*` in metadata lines | present | 0 | removed |
| `Bash` in metadata lines | present | 0 | removed |
| Old `[Tools: ...]` format in metadata | present | 0 (only in quoted text) | replaced |

## Correctness Lens

All checks pass:

- **PAL delimiter blocks present:** Yes — 6 `--- PAL: chat ---` blocks with truncated prompt content (768-char cap with `... (truncated, N chars total)` suffix).
- **Enriched `[Read: ...]` lines:** Yes — 41 enriched lines with full file paths replacing the old `[Tools: Read, Read, ...]` comma-separated format.
- **`mcp__capy__*` absent from metadata:** Confirmed. Zero matches in metadata lines. Occurrences in text content are expected (the session discusses these tools in its design conversation).
- **Bash absent from metadata:** Confirmed. Zero matches in metadata lines. Same caveat as above for text content.
- **PAL-only turns preserved:** Yes — chunks 10-13 have `PAL: chat` in titles without other tools, confirming PAL-bearing tool-only turns survive.
- **Chunk count reasonable:** 34 vs 31 — modest increase from surviving PAL turns, not an explosion.
- **Chunk titles improved:** Titles now show `| PAL: chat | Tools: Read, Agent` instead of `| Tools: mcp__pal__chat, Read, Bash, mcp__capy__capy_search, ...`. Cleaner, more useful for BM25 boosting.

## Usefulness Lens

Four targeted queries tested against the v2-indexed session:

### Query 1: "whitelist vs pattern-based extraction"
**Result:** Found the PAL discussion chunk (Turns 7-10) containing the full debate — PAL arguing for YAGNI on smart fallback, user pushing back on scope. In v1, this was invisible (only "PAL agrees on..." synthesis survived).

### Query 2: "TotalAssistantChars contamination"
**Result:** Found the PAL round that caught the bug (Turns 1-4 PAL block) and the consensus summary that followed. The actual catch — "if Tier 1 content is injected into AssistantText, it contaminates TotalAssistantChars" — is now searchable. In v1, only the assistant's post-PAL synthesis mentioned it.

### Query 3: "PAL design reasoning tool content"
**Result:** Found the selective enrichment discussion (Turns 7-10) where the user corrected the framing from "include missing 60%" to "selectively enrich." The full reasoning chain about noise vs signal is preserved.

### Query 4: "parse.go tool_use contentBlock"
**Result:** Found both the root cause analysis (contentBlock struct missing Input field) and the proposed changes (add Input field, create extractor registry). Also found cross-session results from code review chunks.

### Narrative coherence
v2 transcripts are significantly more coherent than v1. Turns now contain:
- What the assistant said (speech)
- What it asked PAL (with the actual question, truncated to 768 chars)
- What files it read (with full paths)
- What agents it delegated to (with description)

This transforms chunks from "I consulted PAL and explored the code" to "I asked PAL about whitelist vs pattern-based extraction, read parse.go and transcript.go, and delegated codebase exploration to an Explore agent."

## Conclusion

v2 delivers meaningful improvement on both lenses. The PAL discussions — which are the highest-value content in this design session — are now fully searchable. Navigation context (file paths, agent descriptions) adds specificity that v1 lacked. No regressions observed.

## Known Limitations (documented in design-v2.md)

- **Turn boundary collapse:** Non-PAL tool-only turns still collapse, merging text from different conversation phases. Deferred to v3.
- **tool_result not captured:** PAL responses (in user messages) not indexed — only the prompt. Deferred to v3.
- **`--- End PAL ---` count mismatch:** 8 vs 6 `--- PAL:` — the extra 2 are in quoted design text discussing the delimiter format, not actual delimiter blocks.
