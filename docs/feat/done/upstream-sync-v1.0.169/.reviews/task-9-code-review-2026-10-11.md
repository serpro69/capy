# Task 9 isolated code review

Reviewed 2026-10-11 using `kk:review-code:isolated`.

## Scope and method

- Independent `code-reviewer` agent with no implementation-session history.
  PAL tools were unavailable, so the workflow used this reviewer alone.
- Task 9 and its intersections with completed Tasks 1–8, including lettered
  prerequisites. Remaining sync tasks were explicitly out of scope.
- Go profile: SOLID, removal, security, code style, error handling, injection,
  naming, performance and concurrency checklists. The reviewed implementation
  includes the new Linux/Darwin exit observers and the ADR/design amendment.
- Three passes: initial implementation, revised process ownership, and the
  infrastructure-error fix. Reviewer inspection was read-only; it did not run
  tests independently.

## Findings and resolution

Initial assessment: **REQUEST_CHANGES**, with two P1 findings:

1. A `Process.Signal(0)` guard skipped group termination after the leader was
   reaped, even when descendants still held stdout/stderr. Foreground timeouts
   and background shutdown could then wait for the descendants indefinitely.
2. The separate liveness check and raw group signal still allowed reaping and
   PID reuse between those operations. Registry locking alone did not protect
   reaping inside `cmd.Wait`.

Author changes: own the pipes from process creation, drain them before reaping,
observe exit without reaping via Linux `waitid(WNOWAIT)` or Darwin
`kqueue(NOTE_EXIT)`, and serialize every group signal with the final reap and
registry transitions. The unreaped leader reserves the identifier while its
descendants hold pipes. The reviewer confirmed both P1s resolved. New fixtures
exercise an exited leader with surviving descendants under timeout, hard cap
and shutdown, and verify that the leader remains unreaped before cleanup.

The second assessment was **COMMENT**, with one P2: observer/drainer errors were
reduced to an exit code, hiding infrastructure failures such as EPERM or EMFILE.

Author changes: retain capture errors separately from ordinary command exit
status, return them on every attached completion path, and log them only if the
request already detached. An instance-local observer seam injects EPERM before
and after detachment and checks error visibility, logging ownership and cleanup.
The reviewer confirmed the P2 resolved and issued **APPROVE**, with no remaining
P0–P3 findings. No finding was deferred.

The process-ownership findings and changed rationale are preserved in
[ADR-005](../../../../adr/005-background-mode-output-streams.md) and the feature
design, so repository knowledge-label conventions require no duplicate indexing.

## Evidence limits

Linux executor and focused server/CLI tests ran under the race detector; the
parent recorded commands/results in [verification](../verification.md#task-9-bounded-background-output).
Darwin arm64 and Linux arm64 executor tests were cross-compiled. Darwin event
delivery was not run on this Linux host. The reviewer did not independently
execute tests or force operating-system PID reuse; the fixtures verify the
unreaped-leader reservation used to prevent that race.
