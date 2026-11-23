# Compact search reply tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: persistent rank cursors, CLI format and scoring changes

## Task 1. Show failures alongside surviving search results

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Retrieval failures](implementation.md#surface-partial-retrieval-failures)

- [ ] 1.1 Collect a query-local diagnostic through the existing retrieval error callback in the knowledge and vault chunk adapters. Feed surviving ranked hits and failure status to both MCP handlers. Retain strict CLI and ordinary library behavior.
- [ ] 1.2 Exercise one failing FTS layer with a surviving hit, loss of both layers with zero hits, and a failing vault alongside healthy knowledge. Check a clean miss as a control. Verify the concurrent callbacks under the race detector and keep the scoped availability failure visible once.

## Task 2. Bound federated replies and support a fresh retry

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Reply budget](implementation.md#bound-the-complete-reply) · [Tool guidance](implementation.md#wire-the-tools-and-document-recovery)

- [ ] 2.1 Route every keyword return path in `handleSearch` through bounded rendering. Reserve the group frame, request notices and admission footer. Preserve the error flag, budget and stats behavior on validation, empty-store, blocked and normal paths.
- [ ] 2.2 Sanitize complete display fields before extraction and clipping. Exercise an interior multibyte cut, a secret crossing a cut, an oversized label, a long invalid kind, and a long batch of misses. Check valid UTF-8, the complete JSON-decoded text byte count and tracked response bytes.
- [ ] 2.3 With a fixed clock and ranked corpus, cut call 3 after one of two hits in a boundary query. Verify the notice, then retry from that query on call 4 and after reset. Check the effective limits and ranks on both retries. Count query dispatches to show that the original suffix never ran.
- [ ] 2.4 Update empty-store, miss, block, footer and tool-schema guidance. Check one shared hint for many genuine misses, both historical ephemeral selectors, `capy_runs` for new output, and absence of whole-batch miss claims for an unsearched suffix.

## Task 3. Cite repeated passages in federated rank order

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Passage identity](implementation.md#identify-passages-through-ranking)

- [ ] 3.1 Carry the winning knowledge rowid and table, and the vault session/sidecar and chunk coordinate, through retrieval and the existing rank merge. Reuse the browse coordinate if present. Attach navigation to the first full block and allocate its reference only after it fits.
- [ ] 3.2 Compare a duplicate-heavy reply with its expanded source/rank sequence. Cover a different window of the same chunk, colliding titles or clipped prefixes, distinct FTS tables sharing a rowid, and identical passages in different corpora. Verify that a later query can consist entirely of references.
- [ ] 3.3 Label the chunk start and show its sidecar ID when present. If the brief has landed, verify the latest-exchange action fits in the first eligible full hit and can be reached from a reference. Use main and sidecar hits from the same session with equal line numbers and text, and split windows with a shared first line. Verify distinct attribution and correct reference reuse. Exercise chunk index zero and a missing coordinate that still produces a full hit.

## Task 4. Apply the contract to dedicated vault search

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Reply budget](implementation.md#bound-the-complete-reply) · [Vault arguments](implementation.md#wire-the-tools-and-document-recovery)

- [ ] 4.1 Use the writer and vault hit formatter in `handleVaultSearch`. Exercise disabled, an oversized invalid date, clean miss with backlog, partial FTS failure, and main/sidecar attribution. Check existing project/date filters and the absence of federated admission or footer.
- [ ] 4.2 Reject malformed native and serialized query arrays before storage or tracking. Cover a non-string member with a valid fallback supplied, a valid serialized array and the single-query fallback. Confirm original input positions for a boundary query.
- [ ] 4.3 Exercise a boundary query that ran with zero hits fitting, a partly shown query and a boundary query skipped before dispatch. Assert counts and untouched suffixes. Retry the boundary query alone with ten distinct maximum-size vault hits and confirm all ten fit within 40,960 decoded bytes.

## Task 5. Verify and document the public behavior

- **Status:** pending
- **Depends on:** Task 4
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification](implementation.md#verification)

- [ ] 5.1 Run focused and full FTS5 tests with synthetic knowledge and vault keys. Compare pre/post `make bench-quality` results and record fixed-fixture rank parity, reply bytes saved on duplicates, single-query overhead and timing.
- [ ] 5.2 Update both tool descriptions and `docs/reference/tools.md` with query numbering, references and retrying the inclusive suffix. Document field omissions and the vault validation change. Walk through a mixed batch with a repeated passage, distinct excerpt, partial failure and interrupted final group, then issue the advertised retry.
- [ ] 5.3 Review the Go changes and check both shipped handlers against the reply contract before closing the tasks.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3 -> Task 4 -> Task 5
```
