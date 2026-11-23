# A brief for an archived session

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

A developer returning to a long conversation often needs the last request and reply before deciding whether to reopen the archive. A ranked hit can also refer to a point in the middle of a session. Today `vault show` renders the transcript and `vault resume` can restore and launch a host. A brief gives the developer a small, attributable excerpt of what was known at the selected point. [Vault workflows](../../../reference/vault.md)

## Interface

Use `capy vault brief <session-id> [--through-line N]` or `capy_vault_brief({"session":"<id>","through_line":N})`. The session is required. Use the vault's literal, minimum-eight-character UUID prefix semantics. The optional line is a nonnegative integer in the same coordinate system used by vault search hits. Omission means the latest archived line. MCP defaults to the current imported-project scope and uses the existing `project` and `all_projects` selectors. The terminal uses the vault's normal cross-project explicit-ID lookup. Resolve ambiguity **inside** the requested scope. Wrong key, absent vault, missing match, ambiguous prefix, deadline and storage failure each need an accurate result.

Show the archive's identity, platform, effective project and current title as metadata. Then show the opening indexed user message and the most recent indexed user message at or before the line, followed by the latest indexed assistant reply in that request's turn through the same line. If opening and latest coincide, print it once. Cite the stored role and line for each excerpt. Label an absent reply explicitly. User and assistant content is quoted historical data. Assistant rows can include summaries of tool calls. If no user row is available, return the identified archive with that fact. A line past the end yields the latest available exchange.

The projection covers the selected session's main transcript. Selecting an independently archived child UUID gives that child's own main transcript. A Claude sidecar has its own line coordinates and is outside this interface. Retain the stored line convention across both platforms. Cap the complete rendered brief at 4,096 UTF-8 bytes, including headers, citations and omission markers. Prefer the latest request and reply when allocating the budget. Metadata reflects the current archive, including later renames. Every quoted line must satisfy the historical cutoff.

## Assumptions

The per-line vault index already contains sanitized role, turn, message and line coordinates. An indexed exchange may lag a live session until import. A brief describes available indexed material. Reading selected rows is practical within a short query deadline on representative multi-project vaults.

## Not Doing

Automatic hook injection would run on a different trust and latency path. Inferring decisions, commitments or completion from assistant prose would introduce claims absent from the archive. Verbatim export belongs to `vault show`.

## Rejected alternatives

A parallel event journal would require another import, deletion and replica-merge lifecycle. Selecting the latest session implicitly can surface another conversation when multiple agents are active.
