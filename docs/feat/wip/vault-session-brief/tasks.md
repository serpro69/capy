# Vault session brief tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: automatic hook injection, inferred decisions, verbatim export

## Task 1. Read a session brief in the terminal

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#terminal-brief)

- [ ] 1.1 Add a scoped metadata/index snapshot reader in `internal/vault`, with literal prefix resolution, numeric as-of cutoff and deterministic turn/message ordering.
- [ ] 1.2 Register `vault brief` in `cmd/capy/vault.go`. Render sanitized, cited messages and omission markers inside the total byte budget and reject unsupported interactive mode.
- [ ] 1.3 Verify a pending reply, duplicate opening/latest request, empty or stale index and two-platform line anchors. Measure lookup of an old session in a large vault.

## Task 2. Read a project-scoped brief from MCP

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#mcp-brief)

- [ ] 2.1 Register `capy_vault_brief` in `tools.go` and call the shared reader after argument/credential checks and existing vault project selection.
- [ ] 2.2 Expose the follow-up on main-transcript hits. Keep sidecar coordinates distinct and verify explicit selection of an independently archived child.
- [ ] 2.3 Exercise the public tool with disabled vault, current/cross-project lookups, scoped ambiguity and a cutoff before a later assistant response.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Verify the brief across two imports of the same session and after a remembered deletion. Check the 4,096-byte limit and record the vault size, lookup latency and cancellation evidence.
- [ ] 3.2 Run `$kk:document`. Update the vault/agent-facing tool references and enumeration for the additional MCP tool. Include the distinction between a brief and `vault resume`.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
