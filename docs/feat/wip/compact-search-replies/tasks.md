# Compact search reply tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: persistent rank cursors, CLI format changes

## Task 1. Bound and deduplicate federated replies

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#federated-search)

- [ ] 1.1 Replace `handleSearch`'s running block count with an end-to-end text budget. Reserve notice and continuation space before issuing the next query.
- [ ] 1.2 Retain source and passage identity through `federateHits`. Reference only an earlier passage actually shown and keep per-query failure information visible.
- [ ] 1.3 Exercise huge/no-hit queries, partial corpus errors and two distinct excerpts of the same chunk. Assert the complete payload, omitted query number and normal admission notice.

## Task 2. Apply the reply contract to vault searches

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#dedicated-vault-search)

- [ ] 2.1 Wire the writer into `handleVaultSearch`, preserving enabled/disabled, backlog and project-scope feedback.
- [ ] 2.2 Carry a unique vault chunk coordinate where available. Avoid collapsing sibling sessions or a sidecar/main hit that happen to render alike.
- [ ] 2.3 Verify an all-empty batch, repeated hits and an omitted suffix through the MCP handler; use a call-count fixture to prove the suffix was skipped.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Measure UTF-8 bytes for the complete text and equivalence of hit order for small replies. Run the search budget/federation tests and `make bench-quality` on the pre/post versions.
- [ ] 3.2 Run `$kk:document`. Document query numbering, duplicate references and retrying an omitted suffix in `docs/reference/tools.md`.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
