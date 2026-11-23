# Knowledge source browsing tasks

> [Design](design.md) · [Implementation plan](implementation.md) · [Design review](review-design-2026-11-23.md)
>
> **Status** Pending · **Created** 2026-11-23
>
> **Not Doing** Full-body export, source catalog, rank pagination, vault or run envelope changes.

## Task 1. Open an indexed source in the terminal

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Terminal page](implementation.md#terminal-page) · [Index receipt](implementation.md#index-receipt)

- [ ] 1.1 Add the typed position and snapshot page query in `internal/store/inventory.go`. Validate membership in the selected FTS table, bind both IDs as integers, seek strictly past the rowid and read bodies only for displayed rows.
- [ ] 1.2 Add `--sections-of` and `--after-section` to CLI search. Share field rendering for redaction, controls, byte limits and UTF-8 clipping. Preserve cancellation, close and writer errors.
- [ ] 1.3 Return the retained count with the source ID in the index transaction. Show both on single-file CLI receipts and use each committed count in directory totals. Exercise unchanged content with historical chunks and a replacement between commit and reporting. Verify the receipt and directory sum describe the committed results.
- [ ] 1.4 Follow emitted CLI continuations through colliding label substrings, a numeric label, an empty source and a final partial page. A cursor at the last row succeeds empty. Check exit 1 for an orphan cursor, mixed flags, a foreign cursor, an evicted/replaced ID, a forced query error, cancellation and writer failure. Verify a concurrent replacement yields one coherent snapshot and that browse performs no backing-file refresh or access update.

## Task 2. Continue directly from a ranked hit

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Continue after a ranked hit](implementation.md#continue-after-a-ranked-hit)

- [ ] 2.1 Carry the actual knowledge FTS table and `c.rowid` of the winning row through search. Print both the start-of-source ID and the after-hit position in CLI ranked results. Keep retrieval scores and ordering.
- [ ] 2.2 Put repeated headings around section 80 in a longer fixture and deliberately offset rowids across the mirrors. Cover winners from each table, including a trigram winner after fusion. Use the emitted position to obtain exactly section 81. Check the same positions after unrelated deletion and store optimize/rebuild/VACUUM. A hit at the end yields a valid empty page.

## Task 3. Browse and discover source IDs over MCP

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [MCP page](implementation.md#mcp-page) · [Single-URL fetch receipt](implementation.md#single-url-fetch-receipt)

- [ ] 3.1 Add the schema and mode switch in `tools.go` / `tool_search.go`. Validate by argument presence and raw browse limit before budget admission. Check orphan cursors, mixed selectors (including empty arrays), limits 1/8/9, malformed tags, leading zeros, overflow, numeric JSON IDs and a real matching ID above 2^53. Keep ordinary keyword behavior for the same literal-numeric label.
- [ ] 3.2 Emit source IDs and positions on MCP ranked knowledge hits. Follow a hit to the next section through stdio, then page through taper, block, retry after reset and EOF. Check call counts, effective size, reset notice and `IsError` for each [outcome](design.md#outcomes). A legacy knowledge session row can be opened with the vault disabled. A vault hit uses its own identity.
- [ ] 3.3 Show safe labels, source IDs and accurate counts on inline/file index receipts and single-URL fetch receipts. Extend the existing cache metadata SELECT. Fetch two URLs under one friendly label and open each from its fresh, cache-hit and unchanged receipt. Replacing the source makes its former ID unavailable. Check directory and multi-URL aggregate behavior.
- [ ] 3.4 Exercise a long multibyte label and title, quotes and backslashes that expand when escaped, a synthetic secret straddling the cutoff, an embedded NUL/newline and an oversized legacy body. Check finished metadata bytes in browse, receipts and MCP headers/hints, and the existing pre-quote CLI keyword limits. Copy the source/position and verify the full stored body length and the bounded preview.

## Task 4. Verify cost and publish the operator contract

- **Status:** pending
- **Depends on:** Task 3
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification workload](implementation.md#verification)

- [ ] 4.1 Record first, interior, final partial and EOF query plans and full-page medians on the encrypted 50,000-section fixture, plus the oversized-field result. Resolve a missed interactive target before closing the task.
- [ ] 4.2 Run `$kk:test` on the affected Go suites with FTS5 and both synthetic keys. Compare `make bench-quality` to the pre-change baseline. Verify keyword hit order, source filters, fetch freshness and directory aggregates through their ordinary entry points.
- [ ] 4.3 Use `$kk:document` to update help and the public tool reference. Show how to start, continue after a hit, retry a blocked page in the same DB and recover a missing source. Run `$kk:review-code` and `$kk:review-spec` and record the verification results here.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3 -> Task 4
```
