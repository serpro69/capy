# ADR-002: Naive code fence toggle in markdown chunker

**Status:** Accepted (known limitation)
**Date:** 2026-03-20

## Context

The markdown chunker in `chunk.go` toggles `inFence` on any line matching `` ^`{3,} ``. This means if content inside a fenced code block contains a line starting with triple backticks (e.g., a markdown tutorial demonstrating code fences), the parser loses track of fence state.

## Decision

Keep the current behavior. It matches the reference TypeScript implementation in `context-mode/src/store.ts`.

## Rationale

- Real-world LLM tool output (command results, API responses, log files) rarely contains nested code fences.
- The edge case — markdown-about-markdown — is uncommon in the use case this tool targets (sandboxed command output indexing).
- A proper fix would require tracking the exact fence delimiter (number of backticks, optional info string) to match open/close pairs, adding complexity for a marginal case.
- Even when the parser misclassifies, the resulting chunks are still searchable — the content is preserved, just split at the wrong boundary.

## Consequences

Markdown documents that demonstrate code fence syntax may be chunked incorrectly. Acceptable for the current use case.
