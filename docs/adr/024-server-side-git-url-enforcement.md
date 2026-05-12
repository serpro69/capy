# ADR-024: Server-side enforcement for git platform URLs in capy_fetch_and_index

**Status:** Accepted
**Date:** 2026-05-12
**Amends:** ADR-023 (routing rewrite — extends enforcement from guidance-only to mechanical block).

## Context

Issue [#44](https://github.com/serpro69/capy/issues/44) (v0.9.0) established the comprehension-vs-extraction routing principle: git diffs, small commands, and sequential content use direct tools (Bash/Read); large-output extraction uses capy tools.

Issue [#47](https://github.com/serpro69/capy/issues/47) (v0.9.1) extended the principle to web content via guidance-only changes: routing docs, tool descriptions, and blocked-WebFetch redirects were updated to say "for GitHub issues/PRs, use `gh issue view` instead of `capy_fetch_and_index`."

Issue [#49](https://github.com/serpro69/capy/issues/49) demonstrated this was insufficient. In a Codex review session, the model had the rule in context but still routed a GitHub issue through `capy_fetch_and_index`. Post-mortem analysis identified two compounding failures:

1. **Competing mechanical signals outweigh soft guidance.** The redirect chain — URL provided → model reaches for URL tool → WebFetch blocked → "use capy_fetch_and_index" — is concrete and highly salient. "Prefer runtime-native tools when available" is soft language that loses at decision time.

2. **Client-side hooks don't fire in all runtimes.** PreToolUse hooks are registered in Claude Code's settings and execute in its client-side hook system. Codex has its own MCP client that calls capy's MCP server directly, bypassing the hooks entirely. The `FormatAllow` guidance added in v0.9.1 never reached the model.

## Decision

### D1: Server-side block in `handleFetchAndIndex`

When `capy_fetch_and_index` receives a URL matching a git platform issue/PR/MR pattern, the MCP server returns a redirect message instead of fetching. No HTTP request is made. The response names the specific alternative command (e.g., `gh issue view 44 --repo serpro69/capy` for GitHub).

This is the only enforcement layer that works for ALL MCP clients — Claude Code, Codex, Cursor, or any future client — because it operates inside the MCP server, not in a client-specific hook system.

### D2: Client-side hook upgraded from FormatAllow to FormatBlock

The PreToolUse hook for `capy_fetch_and_index` with git platform URLs now uses `FormatBlock` (hard deny) instead of `FormatAllow` (advisory context). For clients that do run hooks (Claude Code), this provides belt-and-suspenders: the hook blocks the call before it reaches the MCP server.

Gist URLs remain `FormatAllow` — they can legitimately be large extraction targets where BM25 indexing is appropriate.

### D3: Shared `internal/giturl` package

URL detection logic is needed by both `internal/hook` (client-side enforcement) and `internal/server` (MCP server-side enforcement). These packages have no existing dependency on each other and creating one would be architecturally wrong — `hook` is client middleware; `server` is the MCP server.

The shared `internal/giturl` package exports `ParsePlatformURL` and `IsGistURL`. Both consumers import the shared package independently.

### D4: Platform-generic detection

URL matching is not hardcoded to GitHub. `ParsePlatformURL` recognizes:

- **GitHub:** `owner/repo/issues/N`, `owner/repo/pull/N`
- **GitLab:** `.../-/issues/N`, `.../-/merge_requests/N`
- **Bitbucket:** `.../pull-requests/N`
- **Gitea:** `.../issues/N`, `.../pulls/N`
- **Generic:** any URL containing `/issues/N`, `/pull/N`, `/pulls/N`, `/pull-requests/N`, or `/merge_requests/N`

GitHub URLs get specific `gh` CLI suggestions (since `gh` is widely available). Other platforms get generic "use your platform's CLI or WebSearch" guidance.

### D5: No "use capy_index later" escape hatch

The block message does not suggest fetching via `gh` and then indexing with `capy_index`. Comprehension via the platform CLI is the end goal, not a step toward re-indexing. Suggesting `capy_index` would make ephemeral comprehension content durable unnecessarily, contradicting the ephemeral-by-default principle from ADR-023.

## Consequences

- `capy_fetch_and_index` will refuse to fetch any URL that matches a git platform issue/PR/MR pattern. There is no override flag. If a legitimate use case for indexing an issue emerges (e.g., batch-scanning 50 issues), the user can fetch content via CLI and pipe it to `capy_index`.
- Gists are not blocked — they can be arbitrarily large. The hook provides soft guidance; the server does not intercept gist URLs.
- The `internal/giturl` package is a new dependency for both `hook` and `server`. It is intentionally minimal (~90 lines, no external dependencies) to keep the coupling surface small.
- Adding new git platform patterns requires updating only `giturl.ParsePlatformURL`.

## Alternatives considered

### Guidance-only (rejected)

Issues #47 and #49 demonstrated this is insufficient. Models have the rule in context but competing mechanical signals win at decision time. Two separate attempts at progressively stronger wording failed to change behavior.

### FormatAllow with per-call guidance (rejected)

Tried in the first iteration of #49. `FormatAllow` lets the tool proceed and injects guidance alongside the result. By the time the model sees the hint, it already has the BM25-fragmented results and has no reason to redo the work with `gh`. Additionally, `FormatAllow` only works in clients that run PreToolUse hooks.

### Hard block with override flag (rejected)

Adding a `force_git_platform: true` parameter to `capy_fetch_and_index` would give the model an easy escape hatch. The entire point of server-side enforcement is to make the wrong path mechanically impossible. The workaround for legitimate edge cases (fetch via CLI, then `capy_index`) is intentionally higher-friction.
