# Darwin process proof

> Task 1 complete, 2026-10-22; direct-child waiter evidence is contract-backed
> Implementation: `1000db2`; comparison baseline: `13204a9a68a3def965950e765ca910c615232913`
> Contract: [Darwin process lifecycle](design.md#darwin-process-lifecycle-and-feasibility-gate)

The Darwin waiter observes a direct child's terminal state without reaping it.
Task 2 uses this primitive to retain the leader's identity while completing group
cleanup and output drainage. **Terminal leader observation alone does not prove
that its process group is gone or that capture is complete.**

## Selected matrix and deployment target

The accepted initial scope is **macOS 26 on Apple Silicon**, deployment target
**26.0**, with hosted CI label **`macos-26`**. Hosted availability was checked on
2026-10-22 against the [runner image manifest](https://github.com/actions/runner-images/blob/main/images/macos/macos-26-arm64-Readme.md).
Task 12 configures and verifies hosted CI and release artifacts; the evidence
below comes from a local native host.

## Child observation mechanism and assumptions

[waitForExitContext](../../../../internal/executor/wait_darwin.go) requires a
retained direct child, normal SIGCHLD retention (no automatic reaping), and a
single owner of the eventual reap. Do not run `cmd.Wait` concurrently with it.
Use a context deadline when observation needs a time budget.

- **Success:** the requested child has terminal status and remains waitable.
  Its unreaped PID stays reserved. The caller retains responsibility for output
  drainage, group cleanup and the final reap, serialized with group signalling.
- **Cancellation or error:** the observer neither signals nor reaps the child.
  The caller must not presume that `cmd.Wait` will return immediately. `ECHILD`
  reports missing child ownership; it is not successful retirement evidence.
- **Timing:** event waits are capped at 25 ms and shortened to the remaining
  context deadline. After an exit hint, nonblocking status probes use a 1 ms
  polling interval. These bounds assume normal scheduler/kernel progress, not
  hard realtime guarantees. No waiter goroutine is abandoned on cancellation.

Kqueue `NOTE_EXIT` and registration `ESRCH` can precede terminal waitability, so
both are wakeups only. Success requires
`waitid(P_PID, …, WEXITED | WNOWAIT | WNOHANG)` to report the requested PID,
`SIGCHLD`, and `CLD_EXITED`, `CLD_KILLED` or `CLD_DUMPED`. The status buffer is
cleared before each call; stopped/continued reports are rejected and `EINTR`
retries check cancellation. Kqueue avoids continuous polling while the child runs.

The [libc binding](../../../../internal/executor/waitid_darwin.go) uses cgo and
the SDK's `siginfo_t`. The pinned `x/sys v0.43.0` has no Darwin `Waitid` wrapper.
Repository builds require CGO; the non-cgo binding returns `ENOSYS`.

The evidence is **contract-backed**: documented API guarantees plus native
fixtures. Supporting sources are:

- [Apple wait definitions](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/wait.h): `WNOWAIT` leaves the child waitable.
- [XNU waitid implementation](https://github.com/apple-oss-distributions/xnu/blob/xnu-12377.121.6/bsd/kern/kern_exit.c), lines 3049–3280: terminal-status handling, non-reaping behavior and clearing the buffer for `WNOHANG`.
- [Go's Darwin stopped-child issue](https://github.com/golang/go/issues/19314): why `WEXITED` alone is insufficient without checking the returned status.

The published XNU revision is **not matched to the tested kernel**, so this is
not source-aligned evidence. Passing fixtures do not establish undocumented
kernel ordering. The legacy `waitForExit` wrapper uses an unlimited context, and
the existing executor's observation-error reap has no bounded-reap guarantee;
Task 2 must use the bounded observation contract explicitly.

## Fixtures and verification status

The [native fixtures](../../../../internal/executor/wait_darwin_test.go) cover
exiting/already-terminal children, SIGSTOP followed by deadline and SIGCONT or
SIGKILL, cancellation, repeated non-reaping observations, one terminal reap,
post-reap `ECHILD`, and invalid/non-child identifiers. Their
[stop synchronization](../../../../internal/executor/wait_fixture_darwin.go) uses
`WSTOPPED | WNOWAIT | WNOHANG`: consuming the stop with `Wait4(WUNTRACED)` would
mask the stopped-status regression. Unverifiable fixture cleanup fails the test
process rather than abandoning a waiter.

Verified on macOS **26.6.2 / 25G83**, Darwin **25.6.0**,
**xnu-12377.161.14~5/RELEASE_ARM64_T6041**, arm64; Go **1.27.2**; SDK
**27.0 / 26A425**. Runs used `CGO_ENABLED=1`, synthetic `CAPY_DB_KEY` /
`CAPY_VAULT_KEY`, and `MACOSX_DEPLOYMENT_TARGET=26.0`. For hosts enforcing routine
capture, follow the [verification bootstrap](bootstrap.md) before running these
commands.

| Check | Recorded result |
| --- | --- |
| `go test -tags fts5,integration -run=TestDarwinWait -count=20 -timeout 5m ./internal/executor` | Passed, 5.604s |
| `go test -tags fts5,integration -race -count=1 -timeout 10m ./internal/executor` | Passed, 15.714s |
| `go vet -tags fts5 ./internal/executor` | Passed |

Six-sample comparisons against the stated baseline measured serial executor
median changes of −0.68% to +0.30% and parallel changes of −2.24% to +2.33%, with
about three extra allocations per run. These are descriptive measurements of the
legacy executor, without a statistical-significance claim.

`make bench-quality` failed the same four server capture-threshold cases on
both revisions because public Darwin capture remains disabled. Store/vault
checks passed with identical quality results. The feature implementer must rerun
the complete capture benchmark after Task 10 activation; these failures are not
successful capture evidence.

## Task 2 obligations remain unresolved

Task 2 must establish the group-retirement certificate: fork/spawn publication,
complete membership enumeration, group exit/re-entry, and the interval before
reaping. It must combine that evidence with bounded pipe drainage. Signal success,
leader exit, EOF and repeated snapshots cannot substitute for the certificate.
Until that work and the later integration tasks pass, public Darwin capture
remains disabled. See [Task 2](implementation.md#task-2-prove-the-darwin-group-certificate).
