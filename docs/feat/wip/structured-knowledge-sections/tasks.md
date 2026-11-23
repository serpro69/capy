# Structured knowledge section tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: vault/run repartitioning, bulk historical rebuild

## Task 1. Bound Markdown sections

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [Implementation plan](implementation.md#markdown-ingestion)

- [ ] 1.1 Update `chunkMarkdown` and knowledge format detection to track the actual fence, heading path and unclosed-code state. Preserve interior indentation and newlines.
- [ ] 1.2 Pack Markdown blocks, title-only fallback and oversized continuations within the stated body and title budgets. Leave the vault caller of `SplitOversized` on its existing algorithm.
- [ ] 1.3 Index and retrieve the inner-fence tutorial, large Unicode table row and heading-only fixture. Assert byte limits, title paths, and code/prose classification in the stored rows.

## Task 2. Keep JSON records identifiable

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Docs:** [Implementation plan](implementation.md#json-ingestion)

- [ ] 2.1 Change `chunkJSON` and `chunkJSONArray` to validate the complete input, preserve numeric values and charge the actual encoded array size.
- [ ] 2.2 Handle oversized objects and scalar fragments with deterministic row/field titles. Keep empty containers and the malformed-input plaintext fallback searchable.
- [ ] 2.3 Index a record with an oversized value followed by an identifying field. Verify the field and all fragment parts are recoverable and that packing small objects stays economical.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Check Markdown and JSON retrieval quality, dedup of historical rows, and vault chunk parity. Compare `make bench-quality` with the immediate baseline before accepting new section boundaries.
- [ ] 3.2 Run `$kk:document`. Update the chunk limits in `docs/reference/tools.md` and the scope of ADR-002, ADR-003 and ADR-022.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 3
Task 2 -> Task 3
```
