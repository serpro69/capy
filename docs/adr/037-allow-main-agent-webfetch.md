# ADR-037: Allow Claude main-agent WebFetch with advisory guidance

**Status:** Accepted
**Date:** 2026-11-03
**Amends:** The unconditional Claude main-agent WebFetch denial inherited from the context-mode port ([port design](../feat/done/port-mcp-core-merged/design.md)).
**Preserves:** [ADR-024](024-server-side-git-url-enforcement.md) (git platform URL enforcement; its matcher is narrowed to numeric IDs by decision 5), Claude main-agent Bash HTTP redirects, child observation routing.

## Context

capy's routing principle splits web content by need: comprehension of small
authoritative pages uses runtime-native tools, extraction from large pages uses
`capy_fetch_and_index`, whose BM25 fragments destroy sequential context. The
generated instructions, the WebFetch denial message and the gist guidance all
direct comprehension to "runtime-native", "runtime web" or "direct web" tools.

In Claude Code the only native tool that reads a given URL is WebFetch, and the
PreToolUse hook denied every main-agent WebFetch call. Bash `curl` is also
redirected for the main agent, and WebSearch searches rather than reading a URL.
Outside git platform URLs, which `gh` or a platform CLI covers, the guidance
therefore led back to `capy_fetch_and_index` — the tool it says not to use for
comprehension. Codex is unaffected: its hosted web tool is not intercepted.

The denial's original justification, context flooding, applies weakly to
WebFetch. WebFetch answers a prompt about the page rather than returning raw
HTML into the transcript as `curl` does. Using it for a large page costs
extraction precision, not context-window integrity. Using
`capy_fetch_and_index` for comprehension content fragments it.

## Decision

1. **Allow main-agent WebFetch** with `FormatAllow` guidance shown once per
   session through the existing `guidanceOnce` claim (new `webfetch` marker
   type). `FormatAllow` carries no `permissionDecision`, so Claude Code's
   native permission rules and prompts still apply.
2. **Keep the git platform denial.** Issue/PR/MR URLs remain denied with the
   specific platform CLI command, as before. A strictly better native tool
   exists there, and ADR-024 shows soft guidance loses when the model already
   holds a URL.
3. **Leave child routing unchanged.** A child with recent suitable capy
   evidence still receives one redirect whose message offers native retry if
   the capy call fails; otherwise it receives advisory guidance.
4. **No configuration switch.** No `[routing.webfetch]` setting is added until
   a concrete need appears.
5. **Numeric item IDs only.** `giturl.ParsePlatformURL` now requires the
   segment after `issues`, `pull`, `pulls`, `pull-requests` or
   `merge_requests` to be numeric, optionally with a `.diff`/`.patch` suffix
   (kept blocked; the suffix is dropped from the number), matching ADR-024
   D4's `/issues/N` form. Previously any segment matched, so documentation
   pages such as `docs.github.com/en/pull-requests/...` were denied as
   platform items. The narrowing applies everywhere the classifier is used:
   the main-agent WebFetch denial, child WebFetch redirect eligibility, and
   the `capy_fetch_and_index` hook and server blocks. The set of matched URLs
   only shrinks; no URL becomes newly blocked.

### Relation to ADR-024

ADR-024 rejected guidance-only enforcement because "competing mechanical signals
outweigh soft guidance". That evidence came from the chain *URL → WebFetch
denied → use `capy_fetch_and_index`*, which pushed a GitHub issue into BM25
fragments. Removing the generic denial removes that misleading mechanical
signal. Where a mechanical rule remains valuable — git platform URLs — it stays.
For other pages the remaining risk is that a model uses WebFetch on a large
page where `capy_fetch_and_index` would extract better. That outcome is bounded
and recoverable, unlike fragmenting comprehension content.

## Consequences

- Main-agent WebFetch on non-git URLs proceeds; the first call in a session
  carries a short advisory pointing large-page extraction to capy.
- Children with recent capy evidence are redirected once while the main agent is
  not. This asymmetry is accepted for now: removing child WebFetch redirects
  would leave `capy_fetch_and_index` observations without a consumer and
  requires revisiting the observation contract (PostToolUse recording, the Codex
  PostToolUse matcher and the routing text). Follow-up (unassigned): decide
  whether child WebFetch should match main-agent behavior; verify with
  `TestObservedChildRoutingLifecycle`.
- Allowing WebFetch does not bypass site bot protection. Claude Code's WebFetch
  is not a hosted crawler like Codex's web tool, so sites that refuse scripted
  local requests can still refuse it.
- Rollback restores the denial in the PreToolUse WebFetch branch and the
  routing text. Stray `-webfetch.once` markers are ignored by older binaries.

## Alternatives considered

### Deny once, allow after capy fails (rejected for now)

Matches the child model but needs new failure evidence: PostToolUse records only
successes. It still routes comprehension content to BM25 on the first attempt.

### Advisory guidance on every call (rejected)

Repeats the same text on every fetch. A once-per-session advisory follows the
established Read/Grep pattern.

### Keep the denial and fix only the wording (rejected)

Rewording cannot provide a native page-reading tool where the hook denies it.
