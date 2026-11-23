# Session byte accounting tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: durable analytics, per-agent attribution, billing

## Task 1. Report measured inputs and workload reduction

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#workload-totals)

- [ ] 1.1 Split fetch and knowledge inputs in `SessionStats`, its snapshot/reset, and the actual producers in `tool_fetch.go` and `tool_index.go`.
- [ ] 1.2 Render measured inputs, captured subset and estimated cache savings in separate fields. Derive the estimate from measured input and counted replies.
- [ ] 1.3 Assert the worked 80% example, zero/small inputs, fetch conversion failure, repeated submissions and a yielded batch completing once per child.

## Task 2. Account for reading the report

- **Status:** pending
- **Depends on:** Task 1
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#report-overhead)

- [ ] 2.1 Partition the `capy_stats` calls and bytes from the workload maps during report construction. Expose preceding-report overhead explicitly.
- [ ] 2.2 Exercise back-to-back reports, subsequent run reads, reset and concurrent completion. Verify per-tool workload rows reconcile with the totals.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Run report, fetch and owner-accounting tests with synthetic keys and exercise snapshot/reset under the race detector. Compare the example against the actual rendered numbers.
- [ ] 3.2 Run `$kk:document`. Update `capy_stats` help and the operator/tool references with units, sampling interval, counter scope and the separate cache estimate.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
