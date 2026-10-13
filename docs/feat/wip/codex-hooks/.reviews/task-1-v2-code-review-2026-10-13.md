# Codex hooks v2 Task 1 — isolated code review

Review mode: mid-implementation; only Task 1 of `tasks-v2.md` is in scope.
Reviewer: independent `code-reviewer` agent, Go profile. Initial assessment:
**REQUEST_CHANGES**. Reviewed 33 files / 1,341 changed lines in the supplied
snapshot, plus the subsequent launcher/shutdown adjustments.

The external PAL review (`gemini-3.1-pro-preview`) failed at expert analysis
because its configured API key was rejected. It produced no findings; this is
not a second approval. The isolated workflow continues with the independent
code-reviewer. The reviewer inspected tests but did not run them.

## Initial findings

1. **P1 — canceled tool responses can trigger SIGPIPE before cleanup**
   (`internal/server/server.go`, serve lifecycle; Go concurrency checklist;
   confidence 98%). The lifecycle guard now cancels rather than exiting.
   mcp-go v0.46.0 workers still write returned responses after cancellation and
   `Listen` joins those workers before returning. A closed stdout reader causes
   Go's default fatal SIGPIPE on fd 1, skipping deferred shutdown and leaving
   previously detached executor groups alive. Handle broken output through the
   existing lifecycle, then test a detached executor plus an active call with
   closed stdout and signal/EOF shutdown.
2. **P1 — output errors do not cancel workers before their join**
   (`internal/server/server.go`, stdio `Listen`; Go error-handling checklist;
   confidence 97%). Input cancellation alone cannot cover a synchronous response
   write failure. mcp-go returns from input processing and waits active workers
   before the deferred cancel runs. Cancel immediately when writing fails,
   preserve that error/nonzero status, and test a synchronous `tools/list`
   failure with an active executor and stdin still open.

No P0/P2/P3 findings. Wrapper removal is consistent across generator, committed
artifact, runtime requirements and drift inventory. No additional removals.

## Disposition and re-review

Both findings reproduced before the fix: all three
`TestCodexMCPClosedOutputCleanup` cases exited via SIGPIPE and left a detached
executor alive (the fixture cleaned up its process groups). The implementation
now handles SIGPIPE through the existing lifecycle guard and uses a stdio
transport that cancels immediately on read/write failure, retaining the first
transport error for diagnostic exit 1. The guard remains installed during
deferred cleanup. The same three regressions now pass, along with the existing
EOF/signal/credential/setup fixtures. `TestStdioWriteFailureCancelsWorkers`
separately exercises a non-SIGPIPE output failure and the actual mcp-go worker
join with stdin still open.

Independent re-review: **APPROVE**, 37 files / 1,611 changed lines, Go profile.
Both original P1 findings are resolved, with no new P0/P1/P2/P3 findings.
The reviewer confirmed wrapper removal, direct credential/setup fixture coverage,
and the two regression paths. Tests were inspected, not independently executed.
The evidence record referenced by README/architecture was absent during review;
it is now supplied as [verification-v2.md](../verification-v2.md). Final package
results are recorded there before Task 1 completion.

Tasks 2–5 (stdin bounds, atomic guidance, main-session evidence and final live
acceptance) remain intentionally outside this review. Documentation/fixture
updates after the first snapshot require reconciliation in the final review.
