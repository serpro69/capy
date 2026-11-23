# Implement runtime availability diagnostics

## CLI and MCP diagnosis

Build a read-only view of the ordered candidates and selected executable from `internal/executor/runtime.go` and `PolyglotExecutor.Runtimes`. Keep the existing available-runtime map contract for its callers. Give `internal/platform/doctor.go` a small typed input with language, selected basename and candidate names. Sort once for presentation. Both `cmd/capy/doctor.go` and `internal/server/tool_doctor.go` already obtain the executor's detected map. Render the language rows as details of the single aggregate check so optional missing interpreters do not create warnings or failures of their own. Step → verify a synthetic PATH with multiple valid candidates, a language with no candidates found, a shim, and a report with no available languages. Lookup must never execute the fixture binary.

## Execution error

Use the same candidate list when `internal/executor/prepare.go` returns `ErrRuntimeUnavailable` for a supported language. Preserve wrapping and the distinction between unknown language and unavailable interpreter at the public admission layer. In captured execution the supervisor translates this error to spawn code 127, and `internal/runstore/runs.go` constructs the public `runtime_unavailable` message. Add a static pointer to `capy doctor` in the newly constructed message. Never copy a private supervisor error to the public result or rewrite historical results. The server's cached discovery and a per-run supervisor are separate samples. Step → verify CLI exit 127, MCP error code, the candidate order in doctor, the cached-server restart hint and selection of a shim using the existing fixture.

## Verification

Check runtime detection and both doctor entry points with the same controlled PATH. Check the aggregate status against the existing zero/one/many fixtures and existing execution failure contracts. Use repository FTS5 test settings and run the quality comparison required for executor changes. Update the operator reference with one missing-runtime example. [Operator reference](../../../reference/operations.md) · [Runtime lookup](../../../../internal/executor/runtime.go)
