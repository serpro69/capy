# Task 11 external review

Date: 2026-10-26. User explicitly approved the named Task 11 source/specification bundle to Claude. Backend: PAL `clink`, `claude`, `role=default`, `allow_edits=false`. Observed model: `claude-opus-5`, Anthropic first-party according to returned model metadata. Initial request completed in 520.148s with a COMMENT verdict. The final clarification withdraws the P2 correctness finding and substantially withdraws P3-2 after correcting ESRCH to the actually observed EPERM. No P0/P1/P2 remains. Native reports are preserved below; the final disposition supersedes the initial report's incorrect errno premise.

## Final disposition

- **P2-1, revised by Claude to P3 documentation:** the proposed ESRCH-tolerant remedy does not fix the captured EPERM failure. The existing error policy remains intact. `design.md` now explicitly documents the one-successful-signal implementation and its availability tradeoff: a late visible member can remain pending and quarantine the owner. This records implementation behavior, not a new user decision or stronger cleanup guarantee.
- **P3-1 and P3-3:** fixed locally by separating `killErr` from prior errors and adding nil/length checks before test dereference/indexing. Focused integration/compiled race checks passed in capture `f7683d7d-802b-4d1f-b549-762980ae6e6a` (executor 11.889s, execution 6.962s, CLI 6.448s). Independent internal delta review approved all three files, with no findings. Claude's final clarification reviews the original snapshot and does not certify these later changes.
- **P3-2, substantially withdrawn by Claude:** its residual suggestion is already satisfied in the original supplied test: `require.NoError(t, err)` and equality with `{CleanupDarwinObservation, CleanupObserved}` accompany the signal-count assertion. This is author context verified against the source, not a rewritten external verdict; no assertion was relaxed.
- **Requested additional source:** automatic approval rejected the expanded follow-up because extra source files and corrected snapshots exceeded the specifically approved bundle. No expanded export occurred. A narrower continuation supplied no files or new source and corrected only the errno premise from already-approved facts. External conclusions about the omitted support/store/type definitions remain Unknown; these boundaries were inspected by the internal reviewer. No expanded external approval is claimed or needed to report this completed bounded review.
- **Remaining work:** no material Task 11 fix is deferred. Tasks 12–13 retain their existing Linux/CI/diagnostics/release prerequisites, owned by the feature implementer. No systemic P0/P1 finding requires indexing. The external verdict remains COMMENT with limited source coverage, while the internal implementation/delta review is APPROVE.

Receipt: returned tool events show successful reads of all 13 criteria, the selected diff and relevant source snapshots. Some unchanged source was read in bounded excerpts; the reviewer requested additional definitions. A Bash directory-listing attempt was denied; subsequent allowed reads succeeded. No test/build or project write was requested or observed. The tool's broad statement that every source snapshot was read is not taken as proof of whole-file coverage. Native execution results are author-attributed. This is a completed, source-evidenced review, not a new runtime verification.

## Initial native report

I have read all 13 criteria files, the diff, and the available source/spec snapshots. Report follows.

---

# Task 11 external code review — macOS captured execution

**Intent:** verify native Darwin yielded/background, execute-file and serial/parallel batch execution; startup-gate serialization vs. running capacity; shutdown/cancel with scoped child proof. One production correction (single group SIGKILL) plus test activation/strengthening.

**Base:** `6f79af4e31f0b61d7273d8db09b28a5e667ab593`. **Candidate:** supplied snapshots + `changes.diff` (used as the review target; no working-tree or `git diff` substitute consulted).
**Scope:** Tasks 1–10 done, Task 11 in scope, Tasks 12–13 pending (diagnostics/CI/release) — absences attributable to them are not flagged.
**Execution:** read-only inspection only. **I ran no tests, builds, or any command.** All timing/pass results in the manifest are author-attributed inputs, not independently verified.

---

## Behavior trace (contract → transition → result)

| Contract source | Transition | Expected | Candidate | Evidence/limit |
|---|---|---|---|---|
| `design.md:177-182` | cancel/deadline → group termination | signal immediately; already-terminal members need no additional signal; required signal errors stay incomplete | pre-loop `killGroup` at `supervised_darwin.go:168`, error still joined → `pending` | source-only |
| `design.md:165-168` | drainage → final inventory | leader present, no other members, both natural EOFs | `supervised_darwin.go:233` unchanged (`len(members)==1 && remaining==0`) | source-only |
| `design.md:238-242` | visible survivor | prevents `observed` until it disappears in budget | preserved — `observed` is still gated on the inventory, never on signal delivery | `supervised_darwin_test.go:304-314` |
| `contracts.md:38` | `observed` scope | leader reap + **required group signalling** + final inventory + natural EOF | see P2-1 — "required signalling" is now latched after the first success | source-only |
| `implementation.md:260-268` | Task 11 three bullets | compiled fixtures, startup vs. running concurrency, shutdown/finalization | all three have new or newly-activated coverage (table below) | source-only |

**Key safety property verified by inspection:** suppressing a signal can only *delay or prevent* `observed`; it cannot fabricate it, because `result.Cleanup.State = CleanupObserved` (`supervised_darwin.go:246-248`) still requires `observed && cleanupErr == nil && reapErr == nil`, and `observed` is set only from the inventory snapshot. Missing/malformed inventory (`:217`, `:201-208`), forced reader closure (`:142-153`), unconfirmed reap (`:245`, `reapDarwinLeader`) and required-signal errors (`:168`, `:222-225`) all still fail proof. This is the central thing the manifest asked me to check, and it holds.

---

## Findings

### P2-1 — Single-signal latch stops cleanup signalling for members that join after the first SIGKILL
`internal/executor/supervised_darwin.go:221` (`if otherLive && !signalled`)

**Trigger:** a descendant forks/joins the process group *after* the one successful `killGroup`, during the ≤3 s observation window (`darwinCleanupTimeout`). Reachable because SIGKILL delivery is not instantaneous and a member mid-`fork` can complete it; `design.md:171` explicitly names "an in-flight fork/spawn or group join".

**Before:** the loop re-signalled on every 10 ms tick while any live non-leader existed, so a late joiner was killed on the next tick and the phase could still reach `observed`.
**After:** it is never signalled. The loop spins to the budget → `cleanupErr` → `Cleanup.State` stays `CleanupPending` → `outcome.Reason = "cleanup_unverified"` (`supervisor_unix.go:94-96`) → owner quarantine, refusing all new work until `capy runs reset` (`service.go:88-90`, exercised at `batch_darwin_integration_test.go:192-196`).

**Consequence:** proof correctness is preserved, but the *recoverable* set narrows and a live descendant is now left behind with no further termination attempt. Net availability regression for teardown patterns that spawn short-lived grandchildren (make recipes, npm scripts).

**Why it happened:** the real defect being fixed is a *spurious failure* — the inventory reports a live member, it exits before `killGroup` runs, the group holds only the zombie leader, and BSD `killpg` skips zombies and returns `ESRCH`, which `:222-225` converted into a cleanup failure. That diagnosis is correct; the remedy is broader than the defect.

**Narrower correction preserving both properties** — keep attempting termination, tolerate the benign race:

```go
if otherLive {
    if err := ops.killGroup(cmd.Process.Pid); err != nil && !errors.Is(err, syscall.ESRCH) {
        cleanupErr = fmt.Errorf("signalling darwin group: %w", err)
        break
    }
}
```

`ops.killGroup` returns the bare `syscall.Errno` (`:40`), so `errors.Is` works directly. This matches `design.md:180-182` more closely than the latch does: "already-terminal members need no additional signal" and "do not send a gratuitous signal to zombies" describe *not failing* on terminal members, not *ceasing to signal live ones*. Permission errors still fail, per `design.md:183`.

If the latch is kept deliberately, record the accepted narrowing in `tasks.md` / `design.md` — the in-code comment at `:228-232` documents the symptom but not that this is a deliberate reduction in termination effort versus the written policy.

---

### P3-1 — `signalled` conflates "signal delivered" with "no prior error"
`internal/executor/supervised_darwin.go:169`

```go
cleanupErr = errors.Join(cleanupErr, ops.killGroup(cmd.Process.Pid))
signalled = cleanupErr == nil
```

`cleanupErr` already carries `closeErr` (`:159`, the joined pipe-close result). If `closeErr != nil` but the kill succeeded, `signalled` is `false` even though the group *was* signalled. Harmless **today only** because the observation loop is gated on `for cleanupErr == nil` (`:183`), so the latch is never read in that state. But the latch is now load-bearing at `:221`; anyone who later relaxes the loop gate (e.g. to keep observing through a drain error) silently reintroduces the double-signal. Fail-loud fix:

```go
killErr := ops.killGroup(cmd.Process.Pid)
signalled = killErr == nil
cleanupErr = errors.Join(cleanupErr, killErr)
```

---

### P3-2 — The new regression test pins the mechanism, not the outcome
`internal/executor/cancel_concurrent_darwin_test.go:96` (`require.Equal(t, 1, signals)`)

The injected second-signal failure is `syscall.EPERM` (`:74`), but the failure actually observed in production was the `ESRCH` zombie-group race. The test therefore asserts "exactly one syscall is issued" rather than "a redundant signal cannot corrupt cleanup proof". Consequence: the narrower correction in P2-1 — which is also correct — would **fail** this test, so the test over-constrains the implementation and will obstruct the fix.

Suggest keeping the signal count as a soft check and adding the outcome assertion: inject `syscall.ESRCH` on the second call and assert `CleanupObserved` with `require.NoError`.

Otherwise this test is well-built: the `{pid: -1}` sentinel cannot collide with a real inventory entry (`group_inventory_darwin.go:37` rejects `p_pid <= 0`), both the cancelled and natural-exit entry paths are covered, `killGroup`/`inventory` are only touched from the phase goroutine so there is no race, and the ordering (`signals == 1 && afterSignal < 2`) makes the fake member appear only after the first real signal — which is exactly the baseline-failing condition.

---

### P3-3 — Unguarded indexing/deref in new test assertions
- `cmd/capy/mcp_batch_test.go:55` — `*child.Ordinal` with no `require.NotNil`.
- `internal/execution/batch_darwin_integration_test.go:124-125` and `:165-166` — `Entries[0]`/`Entries[1]` with no `require.Len`, while the sibling tests in the same file do guard (`:77`, `:188`).

A nil `Ordinal` or a short result set panics instead of producing a readable assertion failure. Consistent with some pre-existing style in these files, hence low.

---

## Items checked and found sound (no finding)

- **Protocol v2 preserved.** `Version == 2` asserted on both batch paths (`mcp_batch_capture_test.go` +`:25`, `mcp_batch_test.go:44`). No schema, dependency or wire change in the diff.
- **Cross-platform proof parameterisation.** New assertions use `capturedCleanup(t)` (`captured_public_test.go:16-30`, pre-existing) and build not-spawned evidence as `{Scope: wantProof.Scope, State: CleanupNotSpawned}`, so Linux keeps `{linux_descendants_v1, retired}` and Darwin gets `{darwin_process_group_observation_v1, observed}`. Matches `initialStreamCleanup()` (`supervised_darwin.go:69-71`). Linux retirement contract is untouched — the only production edit is inside a `//go:build darwin && cgo` file.
- **One-execution proof.** The new `once-*` append markers (`mcp_batch_test.go:32,69-73`; `batch_darwin_integration_test.go:97-101,169-171,199-201`) assert exactly `"x"` *after* a server restart, correctly proving recovery does not re-run commands. Complements the stronger `require.Equal(t, children, p.batchChildren(t, id))` at `mcp_batch_test.go:67`.
- **Parallel concurrency is genuinely proven.** `mcp_batch_test.go:39-42` waits for both `first` and `second` markers before releasing the gate, so concurrency 2 is observed, not inferred.
- **Startup gate vs. running capacity** (`TestDarwinBatchStalledStartupPreservesSibling`). `active == 3 == MaxActive` → `capacity_exhausted` is correct: `Execute` rejects on `s.active >= s.policy.MaxActive` (`service.go:94`) and pending slots are batch-only (`batch.go:51-54`). Sibling capture continues, `forbidden`/`bypass` never appear, `s.unverified` stays false. The post-construction `s.policy`/`s.launch` mutation is safe — `NewService` (`service.go:60-66`) starts no goroutines.
- **Shutdown join** (`TestDarwinBatchShutdownJoinsFinalizer`). `t.Cleanup` ordering is right (LIFO: release-close runs before the service close registered inside `newDarwinCaptureService`), and the double `s.Close()` is safe via the `closed`/`closeDone` re-entry path (`service.go:416-419`), with the `close(s.closeDone)` edge establishing happens-before for `closeErr`.
- **Quarantine/expiry** (`TestDarwinBatchFinalizationFailureQuarantinesPendingChildren`). Asserts `ExpiresAt == nil` for unverified proof, per-child `pending`/`not_spawned`, and that both `Execute` and `ExecuteBatchResponse` return `cleanup_unverified`.
- **Fixture dispatch** (`captured_darwin_integration_test.go:88-105`). The `"batch phases"` demux resets `mode` to `""` before matching, so only the intended child is wrapped; failure paths return `(nil, errors.Join(err, run.Store.Close()))` and `serveInherited`'s deferred closer is `run != nil`-guarded (`supervisor_unix.go:30-34`), so there is no double close.
- **Linux-only fixture narrowing** is correctly scoped. Every assertion in `TestExecuteFileCapturedPhysicalPathBytes` depends on the `safe-\xff.txt` name (`tool_execute_file_run_test.go:193-196`), so skipping the whole test on Darwin loses nothing that could have run; valid-literal path coverage remains host-agnostic in `TestExecuteFile_LiteralPhysicalPath` (`tool_execute_file_test.go:166-228`, incl. `é`, U+0085, U+E0001, newline).
- **Symlink corrections** are minimal and correctly targeted. `TestExecuteFile_AdmissionBeforeSpawn` deliberately keeps unresolved temp dirs because its assertions are "outside the project" either way; `NoDirExists(s.projectDir/.capy/runs)` at `tool_execute_file_run_test.go:109` still works unresolved because `stat` follows `/var → /private/var`.

**Pre-existing, not introduced (inherited):** `writeExecuteFileRules` builds `"Read(/" + testFile + ")"` where `testFile` is already absolute, yielding a doubled leading slash (`tool_execute_file_test.go:34`, `:115`). Unchanged by this diff; flagged only so it is not re-attributed later.

---

## Coverage of Task 11 subtasks

| Subtask | Covering artifact | Verdict |
|---|---|---|
| 11.1 one execution / continued capture / deadlines / stable identity / recovery selectors | `mcp_handoff_test.go`, `mcp_execute_file_test.go`, `mcp_batch_capture_test.go`, `mcp_batch_test.go` (host skips replaced by `capturedCleanup`) | Covered by inspection |
| 11.2 startup serialization vs. running concurrency, no sibling corruption / quarantine bypass | `TestDarwinBatchStalledStartupPreservesSibling` | Covered |
| 11.3 shutdown/cancel joins, batch expiry, scoped child proof, file/policy boundaries | `TestDarwinBatchShutdownJoinsFinalizer`, `TestDarwinBatchFinalizationFailureQuarantinesPendingChildren`, `TestDarwinConcurrentCancellation` | Covered |
| Single-SIGKILL correction | `TestDarwinCleanupDoesNotResignalExitingMembers` | Covered but over-constrained (P3-2); the late-joiner path in P2-1 is **not** covered |

---

## Evidence limits, unread source, and requests

Files **not** supplied, so the corresponding conclusions are **Unknown**:

1. **`execution.Supported()`** — defined nowhere in the bundle (`internal/execution/{service,batch,scheduler,lease_launch,supervisor_unix}.go` do not contain it). It is now the sole gate for `newHandoffServer` (`run_handoff_test.go:19`) and `capturedCleanup` (`captured_public_test.go:18`), replacing `runtime.GOOS != "linux"`. **I cannot verify it agrees with `executor.darwinObservationMatrix` (arm64 + macOS major 26, `supervised_darwin.go:53-62`).** If it is laxer, an unsupported Darwin host runs the suite instead of skipping, and `capturedCleanup`'s `darwin → observed` mapping will produce confusing failures rather than a skip. *Request: `internal/execution/supported*.go` (all build-tag variants).*
2. **`internal/runstore` implementation** (only `cleanup_test.go` was supplied). I cannot confirm whether `runstore.Open` has a physical-path precondition that makes the `filepath.EvalSymlinks` fix at `tool_batch_parallel_test.go:219-221` *required*, or whether it was cosmetic. The change is safe either way. *Request: `runstore.Open` and `runstore.Reset`.*
3. **`executor.CleanupEvidence`, `Complete()`, `StreamResult`, `eventWriter`** — the `Complete()` semantics behind `supervisor_unix.go:94` and `:113` are assumed, not read.
4. **`runstore.Result` / `BatchResult` field definitions** (`Version`, `OwnerID`, `Ordinal`, `Cleanup`, `ExpiresAt`) — usage is internally consistent but the types were not inspected.
5. **No deployment or hosted evidence**, and `modes-proof.md` was not in the bundle. All native-run results (10 cancellation repetitions, race suites, vet, FTS5 build, benchmark comparisons) are author-attributed.

---

## Compatibility and delivery

- **Public v2 wire / old consumer ↔ new provider:** **Supported** — `Version == 2` asserted; no protocol or schema edit.
- **Linux `retired` contract:** **Supported** — production change is `darwin && cgo` only; Linux evidence mapping unchanged.
- **Darwin `observed` contract:** **Supported with caveat** — proof integrity intact; termination effort narrowed (P2-1).
- **Credential / security / file-policy:** **Not applicable** — no policy edits; the execute-file tests changed only path resolution, and admission/deny coverage (`TestExecuteFile_AdmissionBeforeSpawn`, `_FilePathDeny`, `_CodeDeny`) is untouched.
- **Host gating (which suites run where):** **Unknown** — blocked on item 1 above.
- **Tasks 12–13 (diagnostics / native CI / release gates):** out of scope; their absence is not a finding. Note only that the new `darwin && cgo && integration` tests have no CI runner until Task 12 lands, so P2-1's untested path stays unexercised in automation meanwhile.

**Verdict: COMMENT.** No P0/P1 and no demonstrated violation of a hard acceptance requirement. P2-1 is actionable and should be resolved — or its narrowing explicitly recorded in `tasks.md`/`design.md` per the repository's "document deferred work explicitly" rule — before Task 12 freezes this behavior into CI. Approval is scoped to code as inspected; it does not certify the author's execution results, which I did not reproduce.

<SUMMARY>
Performed the authorized read-only Task 11 review. Read all 13 manifest criteria first, then changes.diff and the supplied snapshots (26 files). Ran no tests, builds, or commands; used only the provided diff/snapshots, not a new git diff.

Findings (native P0–P3 severities):

- **P2-1 — `internal/executor/supervised_darwin.go:221`.** The new `!signalled` latch stops all group signalling after the first successful SIGKILL. The underlying diagnosis is right (BSD `killpg` skips zombies and returns ESRCH when only the zombie leader remains, which `:222-225` turned into a spurious cleanup failure), but the remedy is broader than the defect: a descendant that joins the group after the single signal is never signalled, so it can only time out → `pending` → `cleanup_unverified` owner quarantine, where the old code re-signalled each 10 ms tick and could still reach `observed`. Also leaves a live process behind. Narrower alternative: keep re-signalling but treat `errors.Is(err, syscall.ESRCH)` as benign — closer to design.md:180-183 than the latch.
- **P3-1 — `supervised_darwin.go:169`.** `signalled = cleanupErr == nil` conflates "SIGKILL delivered" with "no prior error incl. pipe `closeErr`"; harmless only because the loop is gated on `cleanupErr == nil`, but the latch is now load-bearing.
- **P3-2 — `cancel_concurrent_darwin_test.go:96`.** The regression test pins the mechanism (exactly one syscall, injected EPERM) rather than the outcome, and injects EPERM where production saw ESRCH; the correct narrower fix above would fail this test.
- **P3-3 —** unguarded `*child.Ordinal` (`mcp_batch_test.go:55`) and unguarded `Entries[0]/[1]` indexing (`batch_darwin_integration_test.go:124-125,165-166`) panic instead of failing cleanly.

Verified sound: the correction cannot fabricate `observed` (gated on inventory, not signal delivery); required signal errors, missing/malformed inventory, forced reader closure and unconfirmed reap all still fail proof; protocol v2 `Version==2` preserved; Linux `retired` contract untouched (darwin&&cgo-only production edit); one-execution `once-*` markers verified across restart; parallel concurrency genuinely observed; capacity/quarantine/shutdown-join semantics correct; the Linux-only invalid-UTF8 skip loses no runnable coverage; symlink corrections are minimal and correctly targeted. All three Task 11 subtasks have covering artifacts; the P2-1 late-joiner path is not covered.

Evidence gaps (requests): `execution.Supported()` is absent from the bundle yet is now the sole host-skip gate — cannot confirm it matches `darwinObservationMatrix` (arm64 + macOS 26); `runstore.Open`/`Reset` absent — cannot confirm the EvalSymlinks fix is required vs. cosmetic; `executor.CleanupEvidence`/`Complete()`/`StreamResult` and `runstore.Result`/`BatchResult` type definitions unread; `modes-proof.md` not supplied. All native run/benchmark results remain author-attributed.

Verdict: COMMENT (no P0/P1). Next action: either apply the ESRCH-tolerant narrowing and adjust the regression test to assert outcome, or record the accepted narrowing durably in tasks.md/design.md before Task 12 freezes it into CI.
</SUMMARY>


## Native clarification (original candidate only)

# Revised findings — Task 11 (correction: second-signal failure was EPERM, author-attributed)

**Corrected fact:** the reproduced second-signal failure is `EPERM` (manifest runs `4137bc71…`, `680c20d4…`), not `ESRCH`. I treat this as author-attributed observation, not a verified kernel cause; I ran nothing.

### P2-1 → **withdrawn as a correctness finding; downgraded to P3 (documentation)**
`internal/executor/supervised_darwin.go:221`

My proposed `errors.Is(err, syscall.ESRCH)` tolerance is **wrong for this defect and forbidden by policy**. `design.md:183` requires permission errors to fail cleanup proof, so an EPERM on the second `killGroup` cannot be swallowed — any tolerance-based remedy either misses the observed failure or breaks the stated contract. Given a redundant signal can return a failure that is indistinguishable, at the call site, from a genuine permission denial, **suppressing the redundant signal is the correct remedy**: it removes the spurious-failure source without weakening the error policy.

What remains is strictly an **availability** property, not a proof property:
- *Proof correctness:* unaffected and still sound. `observed` is gated on the final inventory (`:233`), never on signal delivery; a late joiner keeps the inventory non-empty → `pending` → `cleanup_unverified` → quarantine. Fail-loud, as designed.
- *Availability:* a descendant joining the group after the single SIGKILL receives no further termination attempt, so it survives and the owner quarantines. Old behavior could have killed it on a later tick. This is a real, accepted narrowing.

**Remaining ask (P3):** record that narrowing in `tasks.md`/`design.md`. The comment at `:228-232` explains the EPERM symptom but not that termination effort was deliberately reduced versus `design.md:180-182`. Per the repo's document-deferred-work rule, this must not stay conversational.

### P3-2 → **substantially withdrawn**
`cancel_concurrent_darwin_test.go:74,96` injects `EPERM` — which now matches the reproduced failure, not a mismatched proxy. With tolerance ruled out, asserting `signals == 1` is the only direct expression of the fix, so the test is **not** over-constrained. Residual (low): add an outcome assertion (`CleanupObserved`, `require.NoError`) alongside the count so the test states the guarantee, not only the mechanism.

### Unchanged
- **P3-1** `:169` — `signalled = cleanupErr == nil` still conflates delivery with prior `closeErr`; the latch is now load-bearing, so compute `killErr` separately.
- **P3-3** — unguarded `*child.Ordinal` (`mcp_batch_test.go:55`) and `Entries[0]/[1]` (`batch_darwin_integration_test.go:124-125,165-166`).
- **Unknown** (no new evidence authorized): `execution.Supported()` vs. `darwinObservationMatrix`; `runstore.Open`/`Reset`; `CleanupEvidence`/`Complete()`/`StreamResult`; `Result`/`BatchResult` types; `modes-proof.md`.

**Verdict: COMMENT (lowered).** No P0/P1/P2. Only P3s remain; the production correction stands as written.

<SUMMARY>
Corrected the factual premise: the reproduced second-group-signal failure is EPERM (author-attributed, runs 4137bc71…/680c20d4…), not ESRCH. No new files read; no tools, tests, or builds run.

Revised outcome for the original candidate:

- **P2-1 withdrawn as a correctness finding, downgraded to P3 (documentation only).** My ESRCH-tolerant remedy is invalid: design.md:183 forbids ignoring permission errors, and an EPERM on a redundant killGroup is indistinguishable at the call site from a genuine denial. Suppressing the redundant signal (the `!signalled` latch at supervised_darwin.go:221) is therefore the correct remedy — it removes the spurious-failure source without weakening the error policy. The production correction stands as written.
- **Separation of concerns made explicit.** Proof correctness is unaffected: `observed` remains gated on the final inventory (:233), never on signal delivery, so a late group joiner yields pending → cleanup_unverified → quarantine (fail-loud). What remains is purely an availability narrowing — a descendant joining after the single SIGKILL gets no further termination attempt and survives. Remaining ask: record that accepted narrowing in tasks.md/design.md, since the :228-232 comment documents the EPERM symptom but not the deliberate reduction in termination effort versus design.md:180-182.
- **P3-2 substantially withdrawn.** cancel_concurrent_darwin_test.go:74,96 injects EPERM, which matches the reproduced failure; with tolerance ruled out, asserting `signals == 1` is the only direct expression of the fix, so the test is not over-constrained. Residual low suggestion: add an outcome assertion (CleanupObserved + require.NoError) next to the count.
- **Unchanged:** P3-1 (supervised_darwin.go:169 conflates signal delivery with prior closeErr; compute killErr separately now that the latch is load-bearing) and P3-3 (unguarded `*child.Ordinal` at mcp_batch_test.go:55; unguarded Entries[0]/[1] at batch_darwin_integration_test.go:124-125,165-166).
- **Unknowns preserved** (no new evidence authorized): execution.Supported() vs. darwinObservationMatrix; runstore.Open/Reset; CleanupEvidence/Complete()/StreamResult; Result/BatchResult type definitions; modes-proof.md.

Verdict: COMMENT, lowered — no P0/P1/P2 remain; only P3 items. Next action: document the accepted availability narrowing durably before Task 12 freezes it into CI, and optionally add the outcome assertion to the regression test.
</SUMMARY>
