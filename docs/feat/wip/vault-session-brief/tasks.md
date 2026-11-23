# Vault session brief tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: physical-line reconstruction, prompt injection, inferred decisions, verbatim export

## Task 1. Read the latest exchange in the terminal

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Terminal brief](implementation.md#terminal-brief) · [Renderer](implementation.md#render-the-brief) · [Workload](implementation.md#verification)

- [ ] 1.1 Measure the encrypted read path on the 100,000-row fixture before settling the query. Record total indexed bytes and DB/WAL sizes, plan, new connection and repeated timings. Verify cancellation under a shorter caller deadline. Revisit the access path if it misses the two-second target reproducibly.
- [ ] 1.2 Add the existing-file reader under `internal/vault`. Check key, reader version and layout. Resolve the literal prefix inside scope and select main-transcript positions and bodies in one snapshot. Cover absent and empty vaults, accepted old index, incompatible schema/marker, wrong key or damage, and a committed row still in WAL. Check that failures preserve archive contents and a missing path stays missing.
- [ ] 1.3 Wire `vault brief <session-id>` under `newVaultCmd` and use the shared renderer. Verify actual stdout and errors for Claude and Codex, including opening/latest deduplication, a pending and a tool-only assistant, no indexed user and a sidecar with overlapping lines. Reject `--tui` and `--through-line`.
- [ ] 1.4 Exercise a synthetic credential crossing a display cut in a raw imported project path, multiline text resembling a citation, escaped controls, multibyte text and a field above the 64 KiB read limit. Verify the 4,096-byte ceiling, including the final newline, and repeat the workload with the finished reader and renderer.

## Task 2. Request a scoped brief from MCP and from search

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [MCP brief](implementation.md#mcp-brief)

- [ ] 2.1 Register `capy_vault_brief` with its schema, read-only annotation and handler. Check argument types, required session, literal prefix handling, unknown `through_line`, disabled vault, deadline, output accounting and success/error flags. Compare the decoded text with CLI stdout for the same archive.
- [ ] 2.2 Route lookup through the existing vault project selectors. Test a unique in-scope prefix shared with out-of-scope sessions, a local three-way ambiguity, an out-of-scope miss, literal wildcard characters, rejected NUL selectors, a renamed project, all-project access and an independently archived child.
- [ ] 2.3 Update both keyword result paths through their common vault formatter. Follow the displayed latest-exchange instruction for a hit whose term occurs in a different turn, with default and explicit cross-project scope. Check title-only and split-window hits, a sidecar with the same line as the main transcript, an unusable ID preview and a knowledge control. With compact replies present, check the action fits in its full hit and is reachable via a repeat reference.

## Task 3. Verify archive changes and publish the interface documentation

- **Status:** pending
- **Depends on:** Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification](implementation.md#verification)

- [ ] 3.1 Hold a reader snapshot across a writer commit and check metadata and excerpts come from the same revision. A fresh call after a divergent replacement returns the new exchange. Repeat after rename, reassignment and remembered deletion. Verify the registered tool and focused CLI/MCP tests with FTS5 and synthetic keys. Run the applicable retrieval checks for the search-output change.
- [ ] 3.2 Document the command, tool, cutoff limitation, scope, stale-index hint and distinction from `vault show` and `vault resume`. Add the tool to the actual installed inventory and stdio registration checks. Update the architecture, `CapyToolNames` and `GenerateRoutingInstructions` in `internal/platform/routing.go` and its generated `.capy/AGENTS.md` together. Check the existing platform artifact-drift tests.
- [ ] 3.3 Review the implementation against the design and record the encrypted workload results and any remaining release issue before closing the feature.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3
```
