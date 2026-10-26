# Task 12 diagnostics and native CI evidence

> Baseline: `23fbf90c0b12ceff97f40921dc71d305d6cf3ac8`
> Scope: [Task 12](tasks.md#task-12-add-diagnostics-and-reproducible-native-ci)
> Date: 2026-10-26
> Status: Task 12 complete; hosted acceptance verified at `f9df6ab`; [final Task 13 acceptance](release-proof.md) complete

## Implemented behavior

CLI doctor, MCP doctor and MCP stats share static execution capability reporting.
Linux retains its existing admission check; Darwin checks the compiled backend
and macOS 26/arm64 support without spawning a probe. Storage health, writability,
hook activation and host quiescence remain separate. An owner/configuration
failure does not suppress the CLI capability result.

Authenticated diagnostics report found/supported schema versions and each recent
run's cleanup scope/state from authoritative SQL columns. Saved result blobs
cannot replace that proof. New columns are byte-bounded before decoding and
validated together; invalid or oversized proof fails inspection without echoing
stored content. Existing shared lifecycle leases, read-only authentication,
two-second budget, row limits and no-recovery/no-maintenance rules are unchanged.

Both `make test-integration` and `make test-integration-race` use CGO,
`fts5,integration`, uncached runs, serial package scheduling and a 20-minute
package timeout for executor/execution. The race target adds `-race`.
`INTEGRATION_TEST_FLAGS=-json` lets CI retain actual test events without exposing
Go-wide flags to fixture subprocesses. Synthetic keys and isolated test roots
remain required, as for existing tests.

CI adds Linux lifecycle coverage beside the unchanged full FTS5/race/vet and
glamour checks. It records the immutable pre-feature Linux baseline
`13204a9a68a3def965950e765ca910c615232913`; historical failure is retained as
comparison evidence, while current integration and required-fixture failures
block the job. The native macOS job now covers executor, execution, runstore,
compiled modes and doctor parity. Named pass-event guards reject absent/skipped
required fixtures. Existing five-repeat retention coverage remains.

Release builds use explicit `macos-26`/deployment target 26.0 and run
`.github/scripts/capture-smoke.sh` against the exact binary before packaging.
The script also applies to Linux artifacts. It isolates configuration/storage,
uses synthetic keys, verifies exit 7, exact stdout/stderr, one invocation,
encrypted history and recovery by later CLI processes. No release was published.

The explicit arm64 runner label remains documented by [GitHub's hosted-runner
reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
(checked 2026-10-26). Existing checkout/setup-go/upload-artifact versions are
unchanged. Operator guidance and its `.capy/AGENTS.md` generated counterpart are
synchronized; no trust or routine-enforcement setting changes.

## Local execution

Host: macOS 26.6.2/25G83, Darwin 25.6.0 arm64, Go 1.27.2, SDK 27.0/26A425,
deployment target 26.0, CGO enabled. Tests use synthetic `CAPY_DB_KEY` and
`CAPY_VAULT_KEY`, isolated fixture directories and the temporary Go cache from
Task 10. All routine commands are captured by
`/private/tmp/capy-task10-validation.M1BbL6/capy`, with its isolated owner at
`/private/tmp/capy-task10-validation.M1BbL6/owner`; no bootstrap exception was
needed. Evidence lives in `bench-results/macos-captured-task12-validation/`.

| Capture | Check | Result |
| --- | --- | --- |
| `e6a4e2be-78a8-4d88-9249-eb62f38d6996` | Focused diagnostics and generator parity with FTS5/race | Passed: runstore 4.365s, platform 2.342s, CLI 7.765s, server 3.494s. |
| `733689fe-0c8c-41cd-9031-f7c50f277114` | Fresh trimpath release-style build and actual-binary smoke | Passed; nonfatal denied module-stat-cache warning, exit zero. |
| `fa5893c3-d9cb-483b-8953-496ffe9a3140` | Both new Make targets, integration vet, full platform race | Both targets and vet passed. Full platform tests exposed pre-existing physical-path assertions. |
| `2bd153fa-7802-4d8f-b600-7c6a0108f0a2` | Immutable immediate baseline, five affected platform fixture groups | Reproduced the same `/var` versus `/private/var` failures. |
| `0f6a3eda-241c-429e-837b-70a3f39d6af2` | Corrected full platform race, focused diagnostics/CLI/server race, affected vet | Passed: platform 9.772s, runstore 5.482s, CLI 7.518s, server 3.512s; vet passed. |
| `41ff338b-84f4-452f-8df6-53d4470516fb` | Final native runstore and compiled-mode race, exact CI fixture guard, missing-log control and integration vet | Passed: runstore 87.770s, CLI 92.092s; all 50 required native fixture events present; empty log correctly rejected; vet passed. |

The integration JSON reports executor/execution passes of 18.190s/30.348s,
and race passes of 29.815s/50.706s. Optional unavailable runtime fixtures and the
unsupported-host control retain their existing skips; required Darwin lifecycle
tests executed. The five platform fixture groups now create physical temporary
roots, preserving equality, ownership, nearest-installation and trust assertions.
No production owner-resolution behavior changed.

Both workflow YAML files parsed with Ruby's standard YAML library, shell syntax
passed `bash -n`, and Git whitespace checks preserve mandatory YAML/Markdown
spaces. PyYAML and actionlint were unavailable; no schema-linter pass is claimed.
Expanded native runstore/compiled-mode execution and the exact fixture guard
passed locally, including the new CLI invalid-config capability check and a
missing-log rejection control. `macos-store-tests.jsonl` and
`macos-mode-tests.jsonl` preserve actual events. The guard was extracted verbatim
from the workflow into `/private/tmp/capy-task12-check-native.py`; no assertion or
required fixture was omitted. Expected missing-log diagnostics are stderr from
the negative control, not a failed final run.

## Historical retention follow-up

[Hosted run 40013391863](https://github.com/serpro69/capy/actions/runs/40013391863/job/120619187316)
at `31c0ef259ad8dbcc5f67fc12c7fc9cd5ec15730c` completed after the controlled
operation-budget corrections. Downloaded artifacts contain no failure/skip
events: full native runstore/execution passed 229.733s/32.493s; all seven
recovery/accounting/retention regressions passed five repetitions (186.809s).
Environment: macOS 26.6.2/25G83, Darwin 25.6.0 arm64, Xcode 26.6/17F113,
SDK 26.5/25F70, Go 1.27.2, deployment target 26.0. Original artifacts are retained
under `retention-ci-40013391863/` in this task's evidence directory.

Together with the previously recorded controlled baseline failures and fixes,
this closes 12.6's historical native-confirmation requirement. It does not identify
the precise SQL statement behind older opaque hosted errors or certify the new
Task 12 workflow. The old failed attempts remain retained in their prior task
directories and are not reclassified as passes.

## Initial review and acceptance gate

Independent internal review found no source defects, but returned COMMENT while
current hosted acceptance remained unknown. It inspected both local integration
JSON files and the source/dependency paths; tests were executed by the author.
Final independent review covered all 20 changed/new files and the local evidence,
found no defects, and confirmed the source is ready for candidate CI. It checked
the final capture envelope/command/log, exact guard extraction and all 50 required
pass events. Its overall COMMENT is solely the current hosted acceptance gap;
no source changes are requested. No systemic finding requires indexing.

External discovery found no API model; Claude was available, `agy` absent,
authoring-provider Codex excluded, and Gemini retired. No Task 12 source export
was attempted: the earlier explicit approval and subsequent export rejection
were limited to the Task 11 bundle. The internal review provides independent
source coverage; no external corroboration is claimed.

Task 12 initially stayed in progress pending authorization to
publish the candidate to existing PR #140. The next action was to publish the reviewed
revision, require the new Linux baseline/current integration evidence and expanded
native job/guards/smoke to execute, inspect all results, and fix failures without
dropping tags or assertions. At that point the remote PR pointed to `31c0ef2`, so its
old success could not satisfy this condition. Task 13 retains the separate final
feature/release verification; no hidden deployment prerequisite is marked done.

The operator subsequently authorized the commit and push to PR #140 by replying
“Yes,” including the three existing local Tasks 9–11 commits. Candidate publication
and hosted verification are now authorized; this does not authorize a release.

## First candidate CI and fixture corrections

Commit `6fd1a437fa6816adfa444fc750c01ecab5e1faf4` triggered
[run 40325180177](https://github.com/serpro69/capy/actions/runs/40325180177).
All downloaded platform artifacts are preserved under `ci-40325180177/` in this
task's evidence directory. Linux pre-feature integration race passed (executor
19.529s, execution 34.896s), including the queue case at 7.59s. Candidate non-race
executor passed 6.518s; execution failed its queue fixture at 12.01s before the
race phase could run. The required-fixture guard correctly failed as well.

The fixture's 100-ms delay applied to every pipe fragment. A 1-MiB retained
prefix split into 128 fragments already requires 12.8 seconds to persist;
full 16-KiB reads are not guaranteed. Replace this timing dependency with a
bounded first-append gate released by `command-finished`, then flush the prefix
without per-fragment delays. Four MiB of command output still must saturate the
queue while the writer is held, continue draining, retain a nonempty prefix,
and report `queue_saturated` and completed cleanup. The original 12-second wait
and production deadlines stay unchanged. The independent deadline-flush fixture
keeps its separate `AppendDelay`. CI adds five race repetitions of the repaired
queue case. This is a test-backend correction, not a runtime queue change.

On macOS, executor/execution passed 36.106s/79.948s, runstore passed 222.416s,
and retention repetitions passed 166.470s. Vet, build and actual-binary smoke
also passed. Compiled modes failed only the eight-attached-call fixture's
startup-marker condition (10.06s); the precise startup failure is not identified
by that log. Establish each started call before submitting the next, within the
same shared ten-second setup budget. All eight handlers still wait attached for
60 seconds, so recovery/capacity/cancellation assertions retain their meaning.
Separate lifecycle fixtures continue to cover startup-gate contention. Local
three-repeat race verification passed 12.377s in capture
`ec661a51-d559-4e3f-a7d6-dc8e88212afa`.

Independent review found no source defects in these corrections. Corrected
hosted Linux/non-race/race/repetition and native execution remain required;
the first failed runs are not reclassified as passes. No unchanged job retry
or capture-deadline enlargement was used.

## Corrected candidate Linux evidence

Commit `f9df6ab213e9ad89bf1273d6f630da09a7367cc9` triggered
[run 40328223610](https://github.com/serpro69/capy/actions/runs/40328223610).
Linux tested the PR merge revision `2db0edfe9bc5e223578feeedac299383a8e52398`
on Linux 6.17.0-1022-azure x86_64, Go 1.27.2, CGO enabled. Candidate
executor/execution passed non-race 5.624s/40.880s and race 9.331s/71.713s.
All five queue race repetitions passed (1.06–1.11s each); guard and vet passed.
Artifacts are retained under `ci-40328223610/linux/` in this task's evidence
directory. No candidate failure event is present.

The immutable `13204a9` baseline passed executor 17.244s but failed execution at
`TestStoredOwnerSIGKILLRetainsLeasesUntilHelperClose`: `st.Close()` reported a
generic maintenance-on-close error, also observed by fixture cleanup. The same
baseline suite passed in the first hosted run. Both outcomes remain evidence;
historical failure does not disable any candidate test. This predates the
operation-budget corrections recorded above, but the opaque error does not
establish a precise failing SQL stage. Candidate ownership/close assertions
remain intact and pass in both modes. Corrected native and full Linux CI are
still pending.

## Corrected candidate macOS evidence

The same [run 40328223610](https://github.com/serpro69/capy/actions/runs/40328223610/job/121590405179)
completed its native job successfully after waiting for a runner. Executor and
execution passed 33.536s/76.468s, runstore 223.381s, and compiled modes 140.173s.
The corrected eight-attached-call case passed 4.42s. All seven retention
regressions passed five repetitions (169.049s). The 50-fixture guard, integration
vet, native build and actual-binary capture/recovery smoke steps succeeded.
No candidate failure event is present in the downloaded JSON artifacts.

Host: macOS 26.6.2/25G83, Darwin 25.6.0 arm64, Xcode 26.6/17F113,
SDK 26.5/25F70, Go 1.27.2, CGO and deployment target 26.0.
Artifacts and proof-job metadata are retained under `ci-40328223610/` in this
task's evidence directory. At the initial artifact inspection, the existing full
Linux race check remained pending; lint, glamour and both dedicated execution
proof jobs were green. The completion record below closes that last check.

## Completion

[Run 40328223610](https://github.com/serpro69/capy/actions/runs/40328223610)
completed successfully for head `f9df6ab213e9ad89bf1273d6f630da09a7367cc9`.
The existing full Linux FTS5/race/vet job completed successfully at
2026-10-26 13:43:14 UTC. Linux integration, macOS proof, glamour and both lint
checks are green. `final-run-metadata.json` retains exact job/step results;
`native-final-steps.log` records the 50-fixture guard and actual-binary smoke
success messages. Earlier failed attempts and baseline limitations remain as
recorded above.

Final independent review APPROVED Task 12 with no findings after inspecting
the candidate artifacts, all 50 native and 13 Linux required fixture events,
repetition counts, full-suite metadata and smoke output. Reviewers did not run
tests themselves. These are the same source/workflow files as the tested head;
the completion commit changes only `tasks.md` and this evidence record. No
runtime test repetition is needed solely for that documentation update.

Task 12 is complete. Task 13 retains final feature/release acceptance and its
documentation/review requirements; no release or full-feature completion is
claimed here. No new systemic finding requires separate knowledge indexing.
