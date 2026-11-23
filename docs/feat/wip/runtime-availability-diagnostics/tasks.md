# Runtime availability tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: ready for implementation. All implementation tasks are pending.
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

- [ ] 1.1 Expose all registered languages, ordered candidate names and selected basenames from the executor. Preserve its cached available-only map and the path used for invocation.
- [ ] 1.2 Feed the CLI and MCP inventories into one `CheckRuntimes` formatter. Keep the existing zero/one/many aggregate and emit the sampling guidance and sorted informational bullets, including the missing-shell explanation.
- [ ] 1.3 Update the aggregate and checklist tests. On both surfaces check the runtime section at zero and with a mixed inventory, including a passing count with no shell. Verify row membership, order, candidate-only disclosure and marker placement.
- [ ] 1.4 Run the controlled PATH and symlink fixture through one live MCP connection, starting before the shims appear. Check unusable and competing entries, fallback order and the untouched marker after diagnosis. Compare the cached doctor and advertised lists with fresh CLI discovery, the basename observed during captured execution, and a restarted server. [Fixture details](implementation.md#verification)

## Task 2. Explain preparation and captured startup failures

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#execution-error)

- [ ] 2.1 Include registered names in the private preparation error for a supported language. Verify sentinel wrapping and candidate order. Update the public spawn-127 message in `runstore.ApplyOutcome` with a cause-neutral explanation and both doctor entry points.
- [ ] 2.2 Check the CLI exit and text, CLI JSON, and MCP text and structured result for a missing runtime and a selected shim whose interpreter is missing. Reuse the phase-failure fixtures to preserve recorded compiler output and cleanup. Check the pre-admission unknown-language case, a child’s own exit 127 and an old stored message.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with CGO and the two synthetic test keys. Run `make test` and the relevant `fts5,integration` fixtures. Record which Linux and Darwin checks ran and the CLI/MCP observations from Tasks 1 and 2.
- [ ] 3.2 Generate a quality report at the pre-implementation revision with `make bench-quality BENCH_BRANCH=runtime-before` and another at the implemented revision with `make bench-quality BENCH_BRANCH=runtime-after`. Collect both JSON files in the same `bench-results/` directory. Run `go run -tags fts5 ./cmd/qualstat bench-results/runtime-before.json bench-results/runtime-after.json` and record both revisions and the comparison.
- [ ] 3.3 Run `$kk:document`. Update `docs/reference/operations.md` with a missing-shell example and guidance for terminal PATH, server startup and per-run discovery.
- [ ] 3.4 Run `$kk:review-code` for Go and `$kk:review-spec` against the design. Record the verification results and resolve discrepancies before closing the tasks.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
