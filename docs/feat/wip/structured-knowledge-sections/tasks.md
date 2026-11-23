# Structured knowledge section tasks

> [Design](design.md) · [Implementation plan](implementation.md) · [Review resolution](review-design-2026-11-23.md#resolution-after-recheck)
>
> Status: ready for implementation, tasks pending
>
> Created: 2026-11-23
>
> Not Doing: vault/run repartitioning, historical rebuild, search-result formatting

## Task 1. Bound Markdown sections

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Markdown ingestion](implementation.md#markdown-ingestion) · [Shared titles](implementation.md#titles-shared-by-both-formats)

- [ ] 1.1 Implement the physical-line detector, shared fence recognition and ATX heading stack. Test the first-line indentation, inline-span, late-fence and shebang cases alongside ordinary prose.
- [ ] 1.2 Keep the full source heading and fitting fenced example in bounded ordered bodies. Cover heading-only input, a Unicode table row, an oversized/unclosed fence, spare-space overlap and code/prose filtering of continuations. Preserve the vault splitter.
- [ ] 1.3 Finalize bounded titles once per newly partitioned structured source. Exercise duplicate headings, a literal collision suffix and long common prefixes. Retrieve both distinct answers in one shared-term search and a heading word past byte 256.

## Task 1b. Select the parser on refresh and fetch

- **Status:** pending
- **Depends on:** Task 1
- **Size:** S
- **Can run in parallel with:** Tasks 2 and 2b
- **Docs:** [File and fetch selection](implementation.md#file-and-fetch-selection)

- [ ] 1b.1 Pass the fresh detection result from the bounded changed-file read through the existing guarded replacement. Check changed and same-hash historical files, selection on file/directory indexing and the concurrent replacement guard.
- [ ] 1b.2 Route native and converted Markdown, JSON and plaintext by normalized media type in single and batch fetch. Exercise a cache hit and unchanged revalidation. Preserve retained type/count, validation time semantics and indexing age.

## Task 2. Keep JSON records identifiable

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 1b
- **Docs:** [JSON ingestion](implementation.md#json-ingestion)

- [ ] 2.1 Decode one complete sanitized document, keep numeric spellings, redact complete decoded strings/keys and handle invalid input with the existing plaintext fallback. Test identity selection using a large numeric ID and valid empty containers.
- [ ] 2.2 Encode and pack complete fitting array elements by actual compact bytes. Preserve array order, named child paths, duplicate-ID title uniqueness and the exact singleton wrapper boundary. Verify one batch for the small-row fixture in both FTS tables.

## Task 2b. Split oversized JSON fields and values

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 1b
- **Docs:** [JSON contract and worked example](design.md#json) · [Encoding and packing](implementation.md#json-ingestion)

- [ ] 2b.1 Group fitting fields of an oversized object and descend on over-budget fields/elements. Verify two groups for the 161-field row, including both ends, and exercise a nested/mixed array and distinct keys with the same redacted spelling. Bound intermediate encoding and path construction.
- [ ] 2b.2 Keep row identity on all pieces. Retain detached key text, escape-safe string/number fragments, ordered part titles and code classification. Check long-key suffix retrieval and shared field identity for clipped siblings, synthetic redaction, exact reassembly and the public `upland` result.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Tasks 1b and 2b
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Acceptance checks](implementation.md#verification)

- [ ] 3.1 Compare ordered title/body/type tuples and byte lengths in the two FTS mirrors. Verify public CLI and MCP search text, historical same-hash retention and vault window parity using FTS5 and both synthetic keys.
- [ ] 3.2 Compare `make bench-quality` with an immediate pre-change baseline using the same fixtures. Investigate and resolve any flagged Markdown/JSON quality regression before release. Record actual packing counts from the focused checks separately.
- [ ] 3.3 Update the tool reference and ADR-002, ADR-003 and ADR-022 with the shipped format, byte limits and exact-label reindex procedure. Record the implementation review and test results here.

## Dependency Graph

```text
Task 1 ─┬─ Task 1b ────────────┬─ Task 3
        └─ Task 2 ── Task 2b ──┘
```
