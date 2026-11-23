# Knowledge search excerpt tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: cross-request rank paging, rank changes, run/vault presentation

## Task 1. Accept pasted keyword boundaries

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [Implementation plan](implementation.md#keyword-normalization)

- [ ] 1.1 Normalize embedded NULs on the shared non-raw keyword path through Porter, trigram and fallback. Keep the raw vault MATCH path separate.
- [ ] 1.2 At `handleSearch` reject separator-only queries before throttle advancement. Test a pasted two-token query against a strict encrypted knowledge lookup and the federated adapter.

## Task 2. Show the strongest passage on both surfaces

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Docs:** [Implementation plan](implementation.md#selecting-and-displaying-a-passage)

- [ ] 2.1 Move the excerpt selector to a shared Go package and wire it through `formatKnowledgeHit` and `writeKnowledgeSearch`, carrying query and highlights.
- [ ] 2.2 Select one passage, preserve a fitting fence and spend the byte budget on the excerpt plus omission indicators. Discard highlight offsets when display-safe text differs.
- [ ] 2.3 Exercise late-identifier, title-only, Unicode, credential-boundary and long legacy cases through both CLI and MCP. Assert the answer actually appears.

## Task 3. Show a fitting JSON record

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#selecting-a-json-record)

- [ ] 3.1 Expose source format through the knowledge row mapper, then locate object spans within a complete bounded stored array and score them against the query.
- [ ] 3.2 Emit the winning fitting record with its display label. Exercise legacy multi-record chunks, an oversized row, mixed arrays and JSON-looking plaintext.

## Task 4. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 4.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Compare IDs/ranks, answer-bearing excerpt bytes and `make bench-quality` against the pre-change baseline. Check shared query normalization against vault and run search callers.
- [ ] 4.2 Run `$kk:document`. Update `cmd/capy/search.go` help and `docs/reference/tools.md` with the excerpt limit and an example of a late match.
- [ ] 4.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 2 -> Task 3
Task 1 -> Task 4
Task 2 -> Task 4
Task 3 -> Task 4
```
