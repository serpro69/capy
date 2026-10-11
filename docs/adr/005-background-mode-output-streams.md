# ADR-005: Background mode does not destroy output streams

**Status:** Accepted, amended 2026-10-11 (output accumulation superseded)
**Date:** 2026-03-20

## Amendment: drain and discard after detachment

Task 9 of [upstream-sync-v1.0.169](../feat/wip/upstream-sync-v1.0.169/tasks.md)
supersedes the memory-accumulation decision below. Keeping a pipe open does not
require retaining the bytes read from it.

At background return, each `safeBuffer` snapshots its captured output under its
mutex, releases the backing allocation and permanently switches to discard mode.
Later writes return their full length without allocating capture storage. The
returned snapshot still goes through the existing output truncation. Go reader
goroutines continue draining the same pipes; no descriptor is closed or replaced
at detachment, so the child can continue writing normally.

One waiter calls `cmd.Wait` and owns temporary-directory removal after a detached
process exits. The executor owns the read ends from process creation and drains
them before reaping. Linux `waitid(WNOWAIT)` and Darwin `kqueue(NOTE_EXIT)` observe
the leader's exit without reaping it. Its unreaped PID remains reserved while
descendants hold the pipes, so their group can still be signalled. Once exit and
EOF are observed, reaping and registry removal share a mutex with every group
signal and background registration. There is no liveness-check/kill gap or stale
entry after reaping. The foreground call owns timeout/hard-cap monitoring;
returning stops that monitor. Shutdown still sends SIGTERM to tracked groups,
and the waiter retires their entries and files when they exit. This is best-effort
termination, not a new SIGKILL escalation policy for children that ignore SIGTERM.

This refines the initial Task 9 plan: `exec.Cmd`'s internal readers cannot supply
this ordering, because its `Wait` reaps before waiting for those readers. A
`Process.Signal(0)` guard both races with reaping and refuses to signal surviving
descendants after the leader is reaped. Explicit reader ownership is required;
replacing or closing descriptors **at detachment** remains unnecessary. APIs
were checked against the pinned [x/sys v0.43.0 source](https://github.com/golang/sys/blob/v0.43.0/unix/zsyscall_linux.go)
and Apple's [kqueue reference](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/kqueue.2.html).

Post-detach output no longer grows retained Go memory or remains available for
later retrieval. Scripts needing durable logs must write them to an explicit
file. The foreground 100 MiB monitor and partial-result truncation are unchanged.
Tests cover concurrent writes during the transition, zero allocations for later
writes, continued child progress beyond pipe capacity, natural/shutdown cleanup
and exit/cancellation races, including exited leaders with surviving descendants;
see [verification](../feat/wip/upstream-sync-v1.0.169/verification.md#task-9-bounded-background-output).

## Original decision (2026-03-20; historical)

The following records the original reasoning and consequences. Its acceptance
of unbounded accumulation is superseded by the amendment above.

## Context

The design doc (§4.8) specifies that when a process is backgrounded on timeout, "output streams [are] destroyed." In the Go implementation, `exec.Cmd` connects stdout/stderr to `safeBuffer` writers via OS pipes. Once `cmd.Start()` is called, the OS pipe remains open until the process exits — Go provides no API to detach or close the write end of a pipe owned by a child process.

After returning the partial output to the caller, the `cmd.Wait()` goroutine and the `safeBuffer` instances remain alive. The running process continues writing to the pipes, accumulating data in memory that will never be read.

## Decision

Accept the memory accumulation. Do not attempt to work around Go's process I/O model.

## Rationale

- Background mode is rare — it only triggers when `background: true` is set AND the process exceeds its timeout. In normal capy usage, this is an edge case.
- The process will be killed by `CleanupBackgrounded()` on server shutdown (lifecycle guard), bounding the accumulation to one server session.
- The hard cap monitor goroutine does NOT run after the background return (it exits on `ctx.Done()`), so a runaway process could theoretically accumulate >100MB. However, the OS pipe buffer (typically 64KB) provides backpressure — the process will block on writes once the buffer fills if nothing is reading, which `cmd.Wait()`'s internal goroutine does drain into the `safeBuffer`.
- Workarounds (closing pipe FDs via `os.File`, using `cmd.StdoutPipe()` with manual management) add significant complexity and fragility for a marginal benefit.
- The reference TypeScript implementation can close Node.js streams explicitly — this is a Go/Node impedance mismatch, not a design flaw.

## Consequences

A backgrounded process that produces large output will consume memory until server shutdown. Worst case: one long-running process producing continuous output at pipe speed (~GBs). Mitigated by the fact that `CleanupBackgrounded` kills all tracked PIDs on shutdown, and background mode is opt-in per request.
