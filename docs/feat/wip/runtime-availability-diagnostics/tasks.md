# Runtime availability tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: installing interpreters, version probes, environment dumps

## Task 1. Show selected and missing runtimes in doctor

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#cli-and-mcp-diagnosis)

- [ ] 1.1 Expose the executor's registered candidate order and cached selection for display, using the detection in `internal/executor/runtime.go`.
- [ ] 1.2 Wire one shared runtime check into CLI/MCP doctor, preserving aggregate severity and showing concise candidate names for each missing language.
- [ ] 1.3 Use a controlled PATH to verify ordering, fallbacks, a shim, and an empty runtime map. A binary which writes a marker on invocation must leave no marker after doctor.

## Task 2. Explain preparation failures

- **Status:** pending
- **Depends on:** Task 1
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#execution-error)

- [ ] 2.1 Use the shared candidate inventory when `prepare` reports a missing supported interpreter. Preserve `errors.Is(ErrRuntimeUnavailable)` and existing public failure classes.
- [ ] 2.2 Check a cached miss after PATH changes, a new process that sees the installed executable, and the diagnostic path exposed to a caller of captured execution.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Run CLI/MCP doctor, executor detection and failure-classification checks. Use the FTS5 repository suites and `make bench-quality` for the executor change.
- [ ] 3.2 Run `$kk:document`. Update `docs/reference/operations.md` with the difference between PATH discovery and running an interpreter, including server restart guidance.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
