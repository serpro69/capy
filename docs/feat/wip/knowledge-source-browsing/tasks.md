# Knowledge source browsing tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: full-body export, cross-store IDs, rank pagination

## Task 1. Browse one stored source in the terminal

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#terminal-page)

- [ ] 1.1 Implement a strict context-aware page read in `internal/store/inventory.go`, checking source and cursor in one snapshot with bounded row lookahead.
- [ ] 1.2 Add `--sections-of` and `--after-section` to `cmd/capy/search.go`. Publish the knowledge source ID in search and single-file index output.
- [ ] 1.3 Test collisions, a numeric label, an old oversized chunk, last-page behavior, output failure and the old ID after content replacement.

## Task 2. Browse one stored source over MCP

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#mcp-page)

- [ ] 2.1 Declare the string ID parameters in `tools.go` and add a separate request branch in `tool_search.go` before keyword validation.
- [ ] 2.2 Validate incompatible arguments before shared budget admission and fetch only the effective page size. Emit source IDs on index results and knowledge hits.
- [ ] 2.3 Read consecutive pages through MCP stdio, including taper/block and stale-cursor paths. Verify literal substring selection still works in ordinary keyword searches.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Check context cancellation and the late-page query cost on a populated store. Run the knowledge search suites and `make bench-quality` against their baseline.
- [ ] 3.2 Run `$kk:document`. Update terminal help and the public tool reference with exact arguments, ID lifetime, continuation and unknown-source outcomes.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
