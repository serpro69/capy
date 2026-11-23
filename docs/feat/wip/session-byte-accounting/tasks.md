# Session byte accounting tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: ready for implementation, tasks pending
>
> Created: 2026-11-23
>
> Not Doing: durable analytics, per-agent attribution, billing

## Task 1. Account for index and fetch replies

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [Index and fetch replies](implementation.md#index-and-fetch-replies)

- [ ] 1.1 Carry submission evidence from `knowledge.FileResult`. In `handleIndex`, track inline and file errors after submission, and each directory summary once. Compare a rejected inline submission, unchanged files, a refused oversize file, an empty submitted file with a store error, and a partial directory against their actual reply and input deltas.
- [ ] 1.2 In single and batch fetch, show exact admitted body and successful index-input sizes. Count eligible single-fetch error replies and keep one final batch reply. Keep per-URL cache outcome accounting in its existing paths.
- [ ] 1.3 Verify HTML conversion, TTL hit, unchanged revalidation, conversion and store failures, and a transport failure against the two receipt sizes where applicable, input counters, cache outcomes and returned text. Include an invalid request as a control. [Acceptance cases](implementation.md#verification)

## Task 2. Read the knowledge inventory safely

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 1
- **Docs:** [Concurrent knowledge inventory](implementation.md#concurrent-knowledge-inventory)

- [ ] 2.1 Protect the knowledge pointer in `server.go`, publish the configured handle once and use the synchronized optional accessor in `handleStats` and shutdown. Release the pointer lock before inventory queries or close.
- [ ] 2.2 Run a cold stats-only control and a concurrent first index and report under `-race`. Check that the subsequent report shows the initialized knowledge inventory. Retain the read-only run inspection tests.

## Task 3. Report the workload and the cost of reading it

- **Status:** pending
- **Depends on:** Tasks 1 and 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Workload totals](implementation.md#workload-totals) · [Report overhead](implementation.md#report-overhead)

- [ ] 3.1 Split knowledge and HTTP input counters in `SessionStats` and their producers. Retire the duplicate sandbox accumulator. Keep owner-completion accounting for observed and captured bytes. Add the interval start to construction, snapshot and reset.
- [ ] 3.2 In `handleStats`, derive workload rows, exact byte totals and report overhead from one snapshot. Show the byte reduction for positive input, the zero-input state and the separate cache heuristic. Replace the legacy multiplier, token estimates and context claims. Assert the example, equality, excess returns, rounding edge, cache-only interval, empty and input-only reports, and two 60 B tool rows.
- [ ] 3.3 Compare two idle reports and their exact overhead delta. Exercise dry-run and real purge, a yielded run completing after reset, and repeated structured reads through the public MCP handler. Verify incomplete capture and multi-child completion once, then exercise concurrent snapshot/reset and completion with `-race`. [Acceptance cases](implementation.md#verification)

## Task 4. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 3
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification](implementation.md#verification) · [ADR-013 decision](../../../adr/013-configurable-fetch-ttl.md#workload-byte-reporting-decision-2026-11-23)

- [ ] 4.1 Run the implementation's `$kk:test` checks for the affected knowledge and server suites with CGO, `-tags fts5`, and both synthetic keys. Include the focused race cases and the repository test suite. Update assertions that used the retired counters and labels. Compare the worked example with the rendered report.
- [ ] 4.2 Run `$kk:document` and update `toolStats` in `internal/server/tools.go`, the README tool summary, `docs/architecture.md`, and the tool and run references. Document interval attribution, tracked error boundaries, both MCP response measurement conventions, the two fetch receipt sizes and the cache heuristic. Mark the ADR decision implemented when the new report ships.
- [ ] 4.3 Run `$kk:review-code` for Go and `$kk:review-spec` against the design. Record results and resolve discrepancies before closing the tasks.

## Dependency Graph

```text
Task 1 ─┐
        ├─> Task 3 ─> Task 4
Task 2 ─┘
```
