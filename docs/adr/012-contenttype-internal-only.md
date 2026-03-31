# ADR-012: ContentType filter is internal only, not exposed in MCP tool schema

**Status:** Accepted
**Date:** 2026-03-31
**Upstream:** context-mode added `contentType` parameter to `ctx_search` tool schema (v1.0.26).

## Context

The TS upstream added a `contentType` filter ("code" or "prose") to the search tool. Every chunk already has a `content_type` column set during indexing. The question is whether to expose this in capy's MCP tool schema.

## Decision

Wire `ContentType` into `SearchOptions` and the dynamic SQL WHERE clause, but do NOT expose it in the `capy_search` MCP tool schema.

## Rationale

- No current caller uses it — no tool, hook, or internal flow passes a contentType filter
- MCP tool schemas are the LLM's UX. Every parameter adds token overhead to every prompt and gives the LLM another knob to hallucinate or misuse
- The `SearchOptions` struct makes adding it to the schema later trivial — literally one `mcp.WithString` line
- Backend support is already there (1-line SQL clause), so this is a UX decision, not a technical limitation
- When a concrete use case emerges (e.g., "search only code blocks"), adding the schema parameter is instant

## Consequences

- `SearchOptions.ContentType` exists and works — internal code can use it
- The MCP tool definition for `capy_search` does not include a `contentType` parameter
- LLMs calling `capy_search` cannot filter by content type (they get all results)
- Revisit if a real use case appears
