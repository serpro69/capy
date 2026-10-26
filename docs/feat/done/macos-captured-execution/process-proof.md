# Darwin process proof

> Task 1 complete, 2026-10-22; direct-child waiter evidence is contract-backed
> Task 2 complete: observational contract accepted; dormant backend implemented, natively verified and independently reviewed
> Implementation: `1000db2`; comparison baseline: `13204a9a68a3def965950e765ca910c615232913`
> Contract: [Darwin observational cleanup](design.md#darwin-process-lifecycle-and-observational-cleanup)

The Darwin waiter observes a direct child's terminal state without reaping it.
Task 2 uses this primitive to retain the leader's identity while completing group
cleanup and output drainage. **Terminal leader observation alone does not prove
that its process group is gone or that capture is complete.**

## Accepted observational contract

On 2026-10-22, after native reproduction and discussion of the residual-process
risk, the user accepted the recommendation to adopt observational Darwin cleanup:
“Agree. Let's do that.” The current [design](design.md) and
[contract](contracts.md#storage-and-typed-evidence) use
`darwin_process_group_observation_v1` / `observed`. Successful observations permit
normal reuse and retention, even though a racing process may survive. Actual
signal, inventory, drainage or ownership failures remain incomplete/pending.
Linux retirement and the separate lease proof are unchanged.

This supersedes the earlier requirement to prove atomic group retirement and the
historical “decision remains open” dispositions below. The counterexample remains
valid; it is now a required limit on claims, not a failed implementation gate.
The private-libproc experiment does not select a production API. Task 2's
implementation verifies the bounded policy using the documented public sysctl
selector, including successful ordinary workloads and failures.
The implementation and its native evidence are recorded in
[the backend verification section](#dormant-observation-backend-verification).

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

## Historical Task 2 retirement obligations

Before the accepted revision above, Task 2 required a group-retirement certificate: fork/spawn publication,
complete membership enumeration, group exit/re-entry, and the interval before
reaping. It must combine that evidence with bounded pipe drainage. Signal success,
leader exit, EOF and repeated snapshots cannot substitute for the certificate.
Until that work and the later integration tasks pass, public Darwin capture
remained disabled. The current replacement is [Task 2](implementation.md#task-2-implement-bounded-darwin-group-observation).

## Task 2 investigation: the candidate does not close group membership

**2026-10-22 — initial source investigation, before the native fixture below.** Repository base:
`a93aa9830f04fbf18bb0295bba8f8e1bd64a0a21`. The host still reports macOS 26.6.2 /
25G83, Darwin 25.6.0 / xnu-12377.161.14~5, arm64. No Task 2 fixture, build or
benchmark has run. No supervisor implementation or public admission change was
made. Task 1's waiter remains usable but does not resolve these obligations.

The inspected Apple source is tag **`xnu-12377.121.6`**, peeled commit
**`ac9718fb1af618d5ce8678d0dc6e8a58f252216f`**, retrieved 2026-10-22.
It is **not matched to the host kernel**. These observations challenge the
candidate; they are neither source-aligned proof for this host nor a claim that
every possible native mechanism is impossible. Context7 returned kernel-driver
APIs, not a userland membership-closure contract; Apple's public manuals and
published source supplied the evidence below.

### Same-session re-entry counterexample

The relevant boundary case is a descendant that changes process group without
changing session. It differs from a descendant that calls `setsid` and cannot
join a group in its former session.

1. Leader L starts a new session and group G. Descendant D moves to its own group
   in that session, closes both captured output descriptors, and remains alive.
2. L exits. Capy observes terminal status with `WNOWAIT` and retains L unreaped.
3. Capy sends its final group signal and obtains a complete enumeration listing
   only L. D is outside G during both operations; captured pipes can reach EOF.
4. Before L is reaped, D calls `setpgid(0, G)` and rejoins G.
5. Capy reaps L. D remains a live member of G after the proposed certificate.

This is a **source-supported interleaving**, not a recorded execution trace.
The published [`setpgid` implementation](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_prot.c#L575)
permits joining an existing group in the caller's session; it does not require a
live group leader. The leader leaves its group during
[`reap_child_locked`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_exit.c#L2967),
so retaining the zombie also retains the target group through this interval.
Apple's [public setpgid manual](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setpgid.2.html)
documents the same-session restriction, but provides no operation combining the
last membership check with reaping or preventing subsequent admission.

Another pre-reap check or signal moves the last-observation boundary; it does
not close it. Reaping first loses the retained leader's identity protection for
subsequent numeric group signals, but **does not prohibit a read-only absence
check**. The approved design permits retaining L through the final signal,
reaping it, then observing. That alternative needs its own analysis:

1. D begins joining G while L remains unreaped.
   [`enterpgrp`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_proc.c#L2988)
   obtains a reference to G before calling `fixjobc` and `pgrp_replace`.
2. L is reaped before D is inserted. G's member list becomes empty, but D's
   reference keeps the group object alive. A process-list scan can see no member;
   `pgrp_find` rejects groups marked `PGRP_REF_EMPTY`.
3. The in-flight join completes through
   [`pgrp_replace`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_proc.c#L4530)
   and [`pgrp_add_member`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_proc.c#L2752),
   which clears the empty flag when inserting D.

This second **source-supported interleaving is also untested on the host**.
It distinguishes preventing a new join to an absent group from excluding a join
already holding a group reference. No public observation contract that excludes
those in-flight operations was established. Permanently excluding D after its
first departure would be a proposed scope change, not proof that the original
group retired; it also leaves the separate creation/publication obligation open.

### Creation/publication remains a separate obligation

In the inspected source, [`forkproc`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_fork.c)
adds the new process to the inherited group with `pgrp_enter_locked`, then
releases `proc_list_lock` while initialization continues. Later,
[`pinsertchild`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_proc.c#L2895)
inserts it into `allproc` and clears `P_REF_NEW`.
[`proc_listpids(PROC_PGRP_ONLY)`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/proc_info.c#L361)
filters `allproc` and `zombproc` under the process-list lock. That lock protects
the scan; it does not make the scanned lists identical to all group members
throughout process creation. No applicable public guarantee closing this gap
was established in this investigation.

This ordering alone is **not a demonstrated missed-child execution**: a complete
argument must also account for the creating parent's visibility, concurrent
group changes, and the fork/spawn synchronization rules. Those obligations remain
unresolved. Passing a repeated native stress test would not establish the missing
ordering guarantee.

The inspected [`pgrp_iterate`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_proc.c#L4443)
collects member IDs under the group lock, releases it, then resolves each process
and calls the signal callback. Thus
[`killpg1`](https://github.com/apple-oss-distributions/xnu/blob/ac9718fb1af618d5ce8678d0dc6e8a58f252216f/bsd/kern/kern_sig.c#L1676)
is not a group-freeze or terminate-and-join primitive. Success of signalling,
terminal leader status, and EOF still establish separate facts.

### Historical disposition and proposed choices

**Disposition before the accepted observational revision above:** Task 2 stayed
in progress with no subtask marked complete. The former design's stop
condition applies: this investigation has not justified the proposed certificate.
Implementing a dormant supervisor that always refuses retirement would not meet
Task 2. Production integration is deferred until the mechanism or contract is
resolved. The following were proposals at that point; the user subsequently
accepted the second choice, as recorded above:

| Choice | Concrete consequence | Required next step |
| --- | --- | --- |
| Preserve certified group retirement | Keep the current scope/state and block Darwin activation until an applicable membership-closure mechanism is justified. | Feature implementer: identify a mechanism addressing re-entry and creation, verify its supported API/source contract, then add native adversarial fixtures before implementing the supervisor. Repeating the same snapshots is insufficient. |
| Adopt explicitly observational group cleanup | Completion would record a terminal/reaped leader, attempted group cleanup, a final successful membership observation and natural pipe EOF, without asserting that all group processes are gone. A process racing creation/re-entry could survive. This changes the approved retirement guarantee. | Operator must explicitly accept that limit. Then revise the Darwin scope/outcome representation and every normal-reuse, quarantine and retention consumer so observation is not presented as certified retirement; retain Linux's existing proof. Review the revised contract before implementing it. |

The second choice is not the previously rejected recursive descendant tracker:
it changes what a group-only result promises. It is still a requirement change
and at this stage had not been applied. The first choice is not evidence that a suitable
ordinary-CLI mechanism exists. Expanding to a VM, privileged service or restricted
entitlement would require a separate explicit deployment decision.

The proposed native reproduction of the simpler pre-reap sequence is now
implemented and recorded below. Native reproduction of the post-reap in-flight
join and creation/publication cases remains deferred (owner: feature implementer):
these require separate synchronization or instrumentation within kernel operations,
which this fixture does not provide. Next action: evaluate a viable instrumented
fixture if the mechanism investigation continues; verification requires an actual
trace of those specific interleavings, not another passing pre-reap experiment.

## Deterministic native re-entry fixture

**2026-10-22 — native reproduction on the supported host.** The operator requested
this experiment and approved the two exact native test exceptions recorded in
[bootstrap.md](bootstrap.md#task-2-deterministic-re-entry-fixture). This does not
authorize weakening the cleanup contract or enabling Darwin capture.

[`TestDarwinGroupReentry`](../../../../internal/executor/group_darwin_test.go)
re-execs a real session leader L and its real child D. Dedicated pipe handshakes
control group departure and re-entry. No delay schedules the re-entry window:
D cannot attempt re-entry until the parent test has recorded its observations.

The fixture tests the **cancellation variant** of the counterexample:

1. Verify that L and D are members of the new group G.
2. Tell D to leave G with `setpgid(0, 0)`, retaining the session. D closes its
   captured stdout/stderr but keeps separate fixture control descriptors.
3. Successfully send SIGKILL to G while L is alive, then observe terminal L with
   Task 1's non-reaping waiter. The final group signal causes L's exit; this is
   not a reproduction of natural leader exit followed by a later group signal.
4. Read the expected stdout/stderr bytes through natural EOF. Three bounded
   membership snapshots must contain only retained L.
5. Explicitly command D to rejoin G; verify the snapshot contains L and D.
6. Reap L exactly once, request a fresh ping from D, and verify G contains only D.
   The response demonstrates that D executes user code after the candidate's
   completion point; a zero signal or stale acknowledgement is not substituted.

An early-abort control leaves L alive to kill/reap its own child. After deliberate
leader death, closing the member's control pipe asks it to exit, and its own
deadline also bounds its wait. The parent waits for D's PID to become absent
(including init's reap), with a bounded failure, rather than treating pipe EOF as
death. It sends no terminating signal to a non-child PID and no group signal
after reaping L. PID reuse can cause a conservative cleanup timeout, never a
signal to an unrelated process. These bounds assume normal kernel/scheduler
progress, not a hard realtime guarantee.

The integration-only [membership binding](../../../../internal/executor/group_fixture_darwin.go)
uses a fixed 16-entry `proc_listpids(PROC_PGRP_ONLY)` buffer and rejects errors,
malformed lengths and a full buffer. The local SDK's `libproc.h` labels its
interfaces **private**. Its use here is diagnostic and does not select it as a
supported production primitive or promote any snapshot to a retirement proof.
Both new files require `darwin && cgo && integration`; ordinary builds and
Linux runtime code are unchanged. No new module dependency was added.

Host: macOS **26.6.2 / 25G83**, Darwin **25.6.0** /
**xnu-12377.161.14~5/RELEASE_ARM64_T6041**, arm64; Go **1.27.2**; SDK
**27.0 / 26A425**; deployment target **26.0**, CGO enabled, synthetic knowledge
and vault keys. The previously unmatched published source remains unmatched;
this experiment adds direct runtime evidence for this particular sequence.

| Verification | Result |
| --- | --- |
| `go test -tags fts5,integration -run=TestDarwinGroupReentry -count=20 -timeout 5m -v ./internal/executor` | Passed all 20 re-entry and early-abort repetitions; package time 0.869s. |
| `go test -tags fts5,integration -run=TestDarwinGroupReentry -race -count=1 -timeout 5m -v ./internal/executor` | Passed, 3.416s; no race-detector finding. |

Representative native observation: leader/group **29614**, member **29615**;
successful group SIGKILL, terminal unreaped leader, three snapshots `[29614]`,
and natural stdout/stderr EOF; after commanded re-entry and leader reap, member
29615 answered the fresh ping in group 29614. Every run confirmed member PID
absence and leader reaping during fixture cleanup. Original logs are retained at
`/private/tmp/capy-task2-validation/native-initial.txt` and `race-initial.txt`.

This reproduces the pre-reap re-entry counterexample; it does **not** measure its
frequency in ordinary commands, reproduce the post-reap in-flight join, or settle
fork/spawn publication. Under the original contract, Task 2's retirement
certificate remained unresolved; the accepted observational policy supersedes it.
Executor performance/quality comparisons and full release checks were not rerun:
only integration-fixture code changed, with no production execution-path change.

Isolated code review approved this experiment with no findings, inspecting the
fixture, supporting source and author-supplied logs; it did not independently run
the tests or certify retirement. PAL was unavailable. Both native exceptions were
removed, original configuration bytes verified, and installed-hook enforcement
confirmed restored. The mechanism/contract decision was still open at this review;
the later explicit acceptance is recorded at the top of this report.

## Dormant observation backend verification

The private [Darwin backend](../../../../internal/executor/supervised_darwin.go)
implements the accepted policy for macOS 26 arm64. It reuses runtime preparation,
safe environment construction and ordered output events, with its own bounded
phase observation. Its unexported result separates command/capture metadata
from observation completion and never sets the old `Retired` field true for
Darwin. Compiler/runtime aggregation preserves a completed compiler if runtime
spawn fails, and never starts a later phase after failed cleanup.

The [production inventory binding](../../../../internal/executor/group_inventory_darwin.go)
uses the documented `sysctl(KERN_PROC_PGRP)` selector and SDK `kinfo_proc`
layout, with 16→4096-entry bounded growth. It discards `ENOMEM` partial tables,
rejects invalid lengths/identities/states/duplicates, and validates retained
leader presence in the phase controller. Native tests observed live members,
the retained terminal leader and an empty group, and confirmed a deliberately
undersized buffer returns an error rather than an absence observation. The
separate private-libproc re-entry fixture remains integration-only.

The [native phase tests](../../../../internal/executor/supervised_darwin_test.go)
cover ordinary/nonzero execution, cancellation/owner loss/control failure,
capture-sink failure without stopping execution, real Rust compile/run and
compiler failure, runtime spawn failure after successful compilation, and
late cancellation after terminal observation. They also cover a real live child
with closed captured output, a real escaped pipe holder, a visible survivor
under suppressed termination, injected signal/inventory failures and a missing
leader. The escaped pipe case returns incomplete at the three-second observation
budget and retains the real leader exit status; forced reader closure is not EOF.

Independent review initially returned COMMENT solely for missing wait/reap
failure evidence and pending benchmark comparisons, with no source defect.
Added fixtures now exercise: a native stopped child plus injected observer
`EMFILE`; cancellation followed by a real bounded wait timeout while termination
is suppressed; and a real `ECHILD` at the sole `cmd.Wait` after a deliberately
competing native reap/stale inventory. They verify incomplete evidence, no unsafe
later signals or blocking reap, and joined drains. Unconfirmed branches follow
the existing dedicated-helper failure model: `Process.Release` is not reaping
or proof of death. Their fixtures explicitly reclaim sole native wait ownership
for cleanup. Task 9 must retain the incomplete/pending outcome through helper
teardown and storage integration; it cannot upgrade it because a handle closed.

Verification host/settings match the preceding fixture: macOS 26.6.2 / 25G83,
Darwin 25.6.0 / xnu-12377.161.14~5, arm64, Go 1.27.2, SDK 27.0 / 26A425,
CGO enabled, deployment target 26.0 and synthetic keys. Immediate comparison
baseline is detached `a93aa9830f04fbf18bb0295bba8f8e1bd64a0a21`; candidate is this
working tree, not a released/deployed build.

| Check | Final recorded result |
| --- | --- |
| Focused `TestDarwinObserved` integration suite, five repetitions | Passed, **19.216s**, including all three added wait/reap failure cases |
| Full executor `fts5,integration` suite with race detector | Passed, **26.696s**, including the earlier waiter/re-entry fixtures |
| `go vet -tags fts5 ./internal/executor` | Passed; later changes only added integration tests and a helper-scope comment |
| Six-sample legacy executor performance comparison | Completed on both revisions; descriptive medians below |
| `make bench-quality` | Both revisions exit 2 at the same four public-capture threshold cases; all non-metadata report fields identical |

Initial focused/race rounds passed 18.973s/28.112s. A source check then aligned
compiler-failure/late-cancellation classification with the existing executor and
added a deterministic late-cancellation case; that round passed 19.225s/27.891s.
The final round adds reviewer-requested failure coverage. All attempts, both
performance logs and both quality JSON/text reports are retained in
`bench-results/macos-observed-task2-validation/`.

Performance runs were sequential with matching toolchain, target, keys, cache
and temporary root, six samples per case. Legacy executor serial median changes
were shell **+2.53%**, JavaScript **−4.47%**, Python **+1.63%**; parallel medians
were **+0.84%**, **−2.59%**, **−4.45%** respectively. Allocation medians were stable
or slightly lower. `benchstat` was unavailable: these are descriptive samples,
not statistical-significance claims or measurements of the new Darwin backend's
throughput. The exercised legacy execution code is unchanged.

Both quality reports have the same dataset hash/Go version and identical
`by_content_type`, `failures`, `overall`, `post_processing_deltas` and
`threshold_5000` values. Store/vault checks pass. The four
`TestBench5000ByteThreshold` sizes (4999, 5001, 10000, 50000 bytes) fail because
public Darwin capture remains disabled; no assertions/skips changed. Owner:
feature implementer. Rerun after Task 10 activation; acceptance then requires
those real capture cases to pass. These are inherited public-activation failures,
not evidence that encrypted Darwin capture already works.

Native public admission, shared typed evidence, persistence/quarantine and wire
integration remain Tasks 5–10. The remaining release/CI matrix checks remain
Tasks 12–13. Atomic group closure and post-reap/fork-publication race proofs are
excluded by the accepted observational contract, not hidden pending requirements.
The temporary verification exceptions were removed from both checkouts, original
bytes matched, and installed-hook denial was verified restored.

Final isolated review approved the dormant source and evidence documentation
with no findings; the earlier COMMENT evidence gaps are resolved. No reviewer
test execution or PAL review is claimed. Task 2 is complete under the accepted
observational contract; public Darwin capture remains disabled pending the
explicit later integration/activation tasks.
