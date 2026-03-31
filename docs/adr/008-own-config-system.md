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
