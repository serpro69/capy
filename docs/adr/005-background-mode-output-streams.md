# ADR-005: Background mode does not destroy output streams

**Status:** Accepted (bounded risk)
**Date:** 2026-03-20

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
