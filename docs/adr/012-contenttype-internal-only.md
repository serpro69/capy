# ADR-012: ContentType filter stays outside the MCP tool schema

**Status:** Accepted
**Date:** 2026-03-31
**Amended:** 2026-10-11 — terminal knowledge search
**Upstream:** context-mode added `contentType` parameter to `ctx_search` tool schema (v1.0.26).

## Context

The TS upstream added a `contentType` filter ("code" or "prose") to the search tool. Every chunk already has a `content_type` column set during indexing. The question is whether to expose this in capy's MCP tool schema.

## Decision

Wire `ContentType` into `SearchOptions` and the dynamic SQL WHERE clause, but do NOT expose it in the `capy_search` MCP tool schema. Terminal users can select it with `capy search --type code|prose`.

## Rationale

- At the original decision, no tool, hook, or internal flow passed a contentType filter
- MCP tool schemas are the LLM's UX. Every parameter adds token overhead to every prompt and gives the LLM another knob to hallucinate or misuse
- The `SearchOptions` struct makes adding it to the schema later trivial — literally one `mcp.WithString` line
- Backend support is already there (1-line SQL clause), so this is a UX decision, not a technical limitation
- Terminal knowledge search supplies a concrete human-selected use case: limit results to code blocks or prose. A CLI flag does not add token overhead or another choice to MCP tool calls, so this narrower exposure preserves the original MCP rationale

## Consequences

- `SearchOptions.ContentType` exists and works — the CLI and internal code can use it
- `capy search --type code|prose` filters stored chunk classifications, not file extensions; omission searches both
- The MCP tool definition for `capy_search` does not include a `contentType` parameter
- LLMs calling `capy_search` cannot filter by content type (they get all results)
- Revisit MCP exposure separately if a tool-specific use case warrants it
