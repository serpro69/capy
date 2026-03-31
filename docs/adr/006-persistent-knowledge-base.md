# ADR-006: Persistent per-project knowledge base (vs ephemeral per-session)

**Status:** Accepted
**Date:** 2026-03-20
**Upstream:** context-mode uses `/tmp/context-mode-<PID>.db` (ephemeral, dies with process). Persistence was retrofitted in v1.0.29.

## Context

context-mode originally created a fresh SQLite database per session in `/tmp`. Each session started with an empty knowledge base. This was a deliberate simplicity choice — no stale data, no cleanup, no cross-session concerns.

capy had the opportunity to design persistence from day one since it was a new implementation.

## Decision

The knowledge base is persistent per-project, stored at `~/.local/share/capy/<project-hash>/knowledge.db` (XDG-compliant). It survives across sessions.

## Rationale

- Fetched documentation (React docs, API references) shouldn't require re-fetching every session
- Content hash deduplication (ADR-007) means re-indexing the same content is a no-op
- Tiered freshness (ADR-007) handles staleness without requiring ephemeral DBs
- The DB location is configurable via `.capy.toml` — can be changed per-project or shared via VCS
- context-mode eventually added persistence (v1.0.29) validating this direction

## Consequences

- Requires cleanup mechanisms (the `capy_cleanup` tool and `capy cleanup` CLI)
- DB can grow over time — mitigated by tiered freshness and conservative cleanup policy
- Cross-session content is a feature, not a bug — "I fetched React docs yesterday, they're still here today"
