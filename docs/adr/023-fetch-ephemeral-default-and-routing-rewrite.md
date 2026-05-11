# ADR-023: Fetch ephemeral-by-default and task-aware routing rewrite

**Status:** Accepted
**Date:** 2026-05-11
**Supersedes:** Amends ADR-017 (fetch_and_index kind assignment).

## Context

Issue [#44](https://github.com/serpro69/capy/issues/44) identified two related problems from production session transcript analysis (Codex and Claude Code):

### Problem A: Over-preservation of task artifacts

`capy_fetch_and_index` hardcodes `KindDurable` (ADR-017 §Write sites table). Every fetched URL — GitHub issues, PR pages, investigation logs, gists — becomes a permanent DB resident subject to retention-score cleanup rather than TTL eviction. This causes:

- Knowledge DB bloat from transient task context (the same class of problem ADR-022 addressed for oversized sources).
- BM25 pollution: a fetched GitHub issue discussing "authentication" competes head-to-head with a curated API reference doc mentioning "authentication." The issue is noise; the reference doc is signal.

Evidence: session transcripts show `capy_fetch_and_index` used on GitHub issues during investigation, producing durable rows that outlive the task by weeks.

### Problem B: Over-routing through capy

AGENTS.md (the agent-facing routing rules) and MCP tool descriptions push agents toward capy for *all* commands, including operations where BM25 fragmentation actively degrades output quality:

- **Diffs routed through `capy_batch_execute`:** A code review session ran `git diff master...HEAD` (128.8KB) through batch execute, chunked it into 16 BM25-indexed sections, then queried fragments. Code review requires full sequential comprehension of every changed line — BM25 keyword search returns ranked fragments, missing cross-chunk interactions, boundary-spanning bugs, and structural context.
- **Small commands routed through sandbox:** `git status --short`, `wc -l`, `git branch --show-current` (1-25 lines of output) routed through `capy_batch_execute`. The sandbox + indexing + BM25 query overhead exceeds any context savings on sub-50-line output.
- **Unbounded source listings:** The search handler's no-results fallback dumps every indexed source label — itself context pollution from capy's own control plane.

Root cause in AGENTS.md: "Bash is ONLY for: git, mkdir, rm, mv..." (too restrictive) and `capy_batch_execute` tool description: "THIS IS THE PRIMARY TOOL" (unconditional primacy). These create a gravity well where agents default to capy for everything because the instructions say to.

## Decision

### D1: `capy_fetch_and_index` defaults to `KindEphemeral`

Add an optional `kind` parameter to `capy_fetch_and_index` accepting `"durable"` or `"ephemeral"`, defaulting to `"ephemeral"`.

- Ephemeral fetched content is subject to TTL eviction (24h default, per ADR-017) and excluded from default search.
- Agents opt into `kind: "durable"` for content intended as persistent reference (API docs, library guides, specifications).
- The fetch response already guides agents to use `source: "<label>"` for follow-up queries, which bypasses kind filtering — the fetch-then-search workflow works without change.
- `capy_index` remains hardcoded `KindDurable`. Explicit indexing represents deliberate intent to persist; adding a kind parameter would muddy that signal.

### D2: No `capy_promote` tool

The implicit promote/demote path already exists in `indexPreparedChunks`: re-indexing content with the same label and hash but a different kind updates the kind in-place. A dedicated tool adds API surface without clear value — agents that need to promote ephemeral content can re-index via `capy_index(content, source)`.

### D3: Search fallback listing capped to summary-only

Replace the unbounded per-source listing in the search handler's no-results path with a single summary line: source count and total section count. Per-query "ephemeral excluded" and "session excluded" hints are preserved — they provide targeted, actionable guidance. Detailed source browsing belongs in `capy_stats`, not in search fallback output.

### D4: Full AGENTS.md rewrite — task-aware routing

Replace the current "capy is the primary tool, use Bash only for exceptions" framing with a task-aware decision tree:

**Core principle:** Use capy when you need to *extract* specific facts from large output. Use direct tools (Bash, Read) when you need to *comprehend* content fully.

Key routing rules:
- **Git commands always use Bash directly.** Diffs, logs, status — these are comprehension content or small output. Removing `git log, git diff` from `capy_execute` examples.
- **Small-output commands (<~50 lines) use Bash directly.** Sandbox + index overhead exceeds savings.
- **Content requiring full comprehension uses Read/Bash.** Code review diffs, files to edit, instruction documents, checklists.
- **Sequential/ordered content uses Read/Bash.** BM25 ranking reorders by relevance, destroying sequence.
- **High-volume exploration and extraction uses capy.** Broad `rg` searches, API responses, large log analysis — content is large and you only need targeted facts.

### D5: Tool description updates

- `capy_execute`: Remove "MANDATORY" prefix and "git queries (git log, git diff)" from examples.
- `capy_batch_execute`: Remove "THIS IS THE PRIMARY TOOL." Reframe as exploration/extraction tool.
- `capy_fetch_and_index`: Document ephemeral default and `kind` parameter. Note that follow-up search needs `source:` filter or `include_kinds`.

## Variants Considered

### URL-heuristic default kind for fetch

Infer kind from URL pattern: `github.com` issues/PRs/gists → ephemeral; docs/reference URLs → durable; unknown → ephemeral.

**Dropped.** URL patterns are fragile (is `github.com/org/repo/wiki` a reference or an investigation artifact?), the heuristic would need ongoing maintenance, and it obscures intent. An explicit `kind` parameter is clearer — the agent decides, not a regex.

### Targeted patches to AGENTS.md instead of full rewrite

Keep the current structure but add anti-patterns and soften language.

**Dropped.** The current structure's "ONLY for" / "PRIMARY TOOL" framing creates a bias that patches cannot overcome. Agents weight strong imperative language heavily. Adding "except for diffs" caveats to a document that says "MANDATORY: Use for any command" sends contradictory signals. A clean rewrite with a coherent principle (comprehension vs. extraction) is more reliable than patching exceptions onto a flawed frame.

### Include ephemeral in default search

Change `effectiveKindFilter` default from `{durable, session}` to `{durable, ephemeral, session}` so freshly fetched content appears in default search.

**Dropped.** ADR-017 specifically excluded ephemeral from default search to prevent BM25 pollution from command output. Fetch content becoming ephemeral doesn't change the rationale — transient content should not dilute curated knowledge in search results. The `source:` filter bypass is the correct access pattern for fetch-then-search.

### Separate AGENTS.md per AI runtime

Create runtime-specific routing guidance for Claude Code vs. Codex.

**Dropped.** Both runtimes use the same MCP tool names and face the same routing tradeoffs. Separate docs would diverge and require double maintenance. A single document using plain language and MCP tool names is universally applicable.

## Rationale

- Ephemeral-default for fetch aligns with observed usage: the majority of fetched URLs are task-scoped investigation artifacts, not curated reference docs. Opting into durable is a low-friction escape hatch.
- Task-aware routing preserves capy's value for its core use case (context reduction on genuinely large output) while preventing misuse that degrades output quality (BM25 fragmentation of comprehension content).
- Summary-only search fallback eliminates a direct context pollution vector from capy's own control plane.
- Pre-release status makes the behavioral changes (fetch default, tool descriptions) acceptable without deprecation cycles.

## Consequences

**Positive**

- Fetched task artifacts no longer become permanent DB residents. Knowledge DB contains higher-signal content.
- Code reviews receive full diffs in context instead of BM25 fragments — review quality improves.
- Small commands execute directly without sandbox overhead — lower latency, no false context savings.
- Search no-results responses are concise instead of flooding context with source inventories.

**Negative / trade-offs**

- **Breaking change:** existing workflows that fetch reference docs and expect them to persist across sessions must add `kind: "durable"`. Acceptable in pre-release.
- **Fetch-then-search requires `source:` filter:** agents must pass `source: "<label>"` or `include_kinds: ["durable", "ephemeral"]` to query freshly fetched ephemeral content. The fetch response text already guides this pattern.
- **AGENTS.md rewrite risk:** agents may over-correct and stop using capy for legitimate high-volume extraction. Mitigated by positive guidance ("when to use capy") alongside negative guidance ("when not to").
- **Tool description softening risk:** without "MANDATORY" and "PRIMARY TOOL," agents on some runtimes may underweight capy. Mitigated by clear positive framing of capy's value in extraction scenarios.

**Cross-reference**

- ADR-017 (source-kind separation) — this ADR amends the fetch write site's kind assignment from `durable` to `ephemeral` in ADR-017's write-site table. All other ADR-017 decisions (schema, cleanup split, search defaults) remain unchanged.
- ADR-022 (source-size guard) — orthogonal. Size guards prevent oversized sources regardless of kind.
- ADR-011 (conservative cleanup) — preserved for durable; ephemeral uses strict TTL per ADR-017.

## Release notes

- **Breaking change — `capy_fetch_and_index` default kind:** fetched web content is now indexed as `ephemeral` (24h TTL, excluded from default search). To persist reference docs across sessions, pass `kind: "durable"`. Follow-up searches on fetched content should use `source: "<label>"` to bypass kind filtering.
- **Search improvement:** no-results fallback no longer dumps the full source inventory. Use `capy_stats` for source browsing.
- **Tool routing guidance updated:** AGENTS.md and tool descriptions now use task-aware routing. Git commands, small-output commands, and comprehension content (diffs, files for review) use direct tools. Capy is recommended for high-volume exploration and extraction tasks.
