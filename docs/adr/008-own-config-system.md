# ADR-008: Own configuration system (vs reading Claude Code settings only)

**Status:** Accepted
**Date:** 2026-03-20
**Upstream:** context-mode reads `.claude/settings.json` for security rules and has no separate configuration.

## Context

context-mode is tightly coupled to Claude Code's settings. It reads deny/allow patterns from `.claude/settings.json` and has no independent configuration. This works because it's a Claude Code plugin with no standalone identity.

capy is a standalone binary that happens to integrate with Claude Code. It needs configuration for things Claude Code doesn't control: DB paths, cleanup thresholds, executor timeouts, title weights, cache TTLs.

## Decision

Three-level TOML configuration with precedence (lowest to highest):

1. `~/.config/capy/config.toml` (global)
2. `.capy/config.toml` (project)
3. `.capy.toml` (project root)

Additionally, capy reads `.claude/settings.json` for security policy rules (deny/allow patterns) to maintain compatibility.

## Rationale

- TOML is simpler than JSON for human-edited config, and Go has mature TOML libraries
- XDG-compliant paths for global config
- Security policies should be shared with Claude Code (single source of truth for deny/allow)
- Everything else (DB path, timeouts, weights, TTLs) is capy-specific and belongs in capy's config

## Consequences

- Users can tune capy per-project without touching Claude Code settings
- Config values like `title_weight`, `fetch_ttl_hours` enable divergences from upstream defaults without code changes
- `capy setup` creates `.mcp.json` and hook config but does not create `.capy.toml` — defaults work out of the box

## Amendment: shared search budget (2026-10-12)

Task 19 of upstream-sync-v1.0.169 adds `[search] window_ms`, `taper_after` and
`block_after` to the same three-level TOML flow. Defaults are 60000/3/8. These
fields merge by presence, reject invalid supplied scalars (including zero), and
validate the merged relationship `1 <= taper_after < block_after <= 10000`.
The window must be an integer between 1000 and 3600000 milliseconds.

The server captures the merged values at construction, so changes require a
restart. Calls taper/block strictly after their thresholds and reset at or after
window expiry. Every valid `capy_search` response reports a coherent snapshot of
call count, effective result limit, calls left before taper/block and reset time,
including empty and degraded responses. Invalid input consumes no budget.

This gives operators control over fan-out pressure while retaining the existing
default limits. It adds no environment aliases or per-request policy overrides.
The counter belongs to the shared MCP server; per-agent isolation still requires
a verified request identity ([D1](../feat/wip/upstream-sync-v1.0.169/upstream-audit.md#d1-per-agent-search-budgets)).
CLI knowledge search and vault-only MCP search do not use this counter. Removing
the new settings restores defaults without any database or generated-artifact
migration.
