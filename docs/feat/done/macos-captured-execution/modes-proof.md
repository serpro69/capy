# Task 11 yielded, file and batch execution evidence

> Baseline: `6f79af4e31f0b61d7273d8db09b28a5e667ab593`
> Scope: [Task 11](tasks.md#task-11-verify-yielded-file-and-batch-execution)
> Contract: [scoped cleanup](contracts.md)
> Date: 2026-10-26

The compiled transport fixtures now run on supported macOS hosts as well as
Linux. They verify yielded and legacy-background capture, execute-file,
serial/parallel batches, deadlines, cancellation, stable owner/run/batch
identities, byte-exact recovery and execution counts. Native batch fixtures
exercise startup gate contention while another command runs, shutdown during
backend close, and failed finalization with queued children. Cleanup proof
remains per child; unfinished or unverified batches cannot expire as completed
history. The existing scoped retention tests exercise proof after child eviction.

## Cancellation correction

The eight-active-run transport fixture exposed an intermittent `interrupted`
result with persisted pending cleanup. A direct concurrent executor fixture
isolated the error to a repeated group SIGKILL returning `EPERM` after the
required signal had succeeded. The correction sends the required group kill
once per phase, then continues bounded observation. It does not reinterpret an
error as success or infer termination from signal delivery. Natural output EOF,
a final inventory containing only the retained leader, and the sole reap remain
mandatory. Any visible survivor, required-signal failure, or observation error
keeps cleanup pending. A newly appearing member can therefore still cause a
timeout and quarantine; no stronger descendant guarantee is added.

The deterministic regression supplies transient live-member observations after
the first successful signal and fails any redundant signal. Both cancellation
and natural-exit cases fail on the baseline with only that fixture added. Both
pass on the candidate, alongside ten repetitions of eight-way cancellation under
the race detector. Existing real survivor, escaped-pipe, signal-error and
inventory/wait/reap failure fixtures retain their assertions.

## Host, commands and results

Native host: macOS 26.6.2 (25G83), Darwin 25.6.0 arm64, Go 1.27.2, SDK 27.0
(26A425), deployment target 26.0, CGO enabled. Verification used synthetic
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`, temporary
fixture roots, and the temporary build cache from Task 10.

The installed MCP binary still rejects native capture before admission. All
checks instead used the verified Task 10 binary
`/private/tmp/capy-task10-validation.M1BbL6/capy execute` with its isolated owner
at `/private/tmp/capy-task10-validation.M1BbL6/owner` and an explicit candidate
or baseline cwd. No routing configuration or trust setting changed. Readiness
capture `7284f1b9-e25d-4822-83b7-770910ad6257` passed first.

| Captured run | Check | Result |
| --- | --- | --- |
| `a54975c6-3b44-41c3-bed7-9a845bb9ca86` | Initial compiled modes and server/file-policy checks | Failed: concurrent cancellation plus fixture physical-path/filename assumptions. |
| `4137bc71-b8ce-4414-837f-743bbb9ccd01` | Three focused maximum-capacity transport repetitions with response diagnostics | One failure, two passes; pending cleanup was preserved. |
| `680c20d4-a68e-4531-9a41-2f27f8b2ffb6` | Ten direct concurrent cancellation repetitions | Reproduced redundant-signal `EPERM`. |
| `2e09f96a-ef96-4ffd-99d5-55306f261f29` | Three new native batch phase fixtures with race detection | Passed, 7.023s. |
| `7104c7c2-b7c0-4c7f-a70c-bfb714e5ad42` | Corrected concurrent cancellation and deterministic signal fixtures, race/count=10 | Passed, 12.803s. |
| `b9963ea5-fae4-4403-9527-1abab949cadf` | Baseline plus deterministic regression only | Both expected cases fail; no production baseline edits. |
| `77fb76e4-1ba8-4644-9829-3f51a463ef65` | Corrected server/file-policy focused race suite | Passed, 176.465s; started before the signal correction. |
| `65d6339f-f684-476b-a499-c8440ff5c327` | Final core integration race, compiled modes race, affected vet and full build | Passed: executor 29.763s, execution 50.895s, runstore 90.082s, CLI 77.924s. Build emitted a nonfatal denied module-stat-cache write warning and exited zero. |
| `a57fb67b-5920-4cc6-92b4-b09177729725` | Final server cancellation/batch race and serial baseline/candidate quality/performance | Passed: server 67.616s; both quality runs and both six-sample benchmark runs. |

Final core command: `go test -tags fts5,integration -race -count=1 -p 1 -timeout 10m ./internal/executor ./internal/execution ./internal/runstore`.
Compiled command: `go test -tags fts5 -race -count=1 -timeout 10m ./cmd/capy -run 'Test(MCPRunHandoffTransport|MCPExecuteFileCapturedLifetime|MCPSerialBatchCapture|MCPParallelBatchControls)'`.
Vet: `go vet -tags fts5,integration ./internal/executor ./internal/execution ./internal/runstore ./internal/server ./cmd/capy`.
Build: `go build -tags fts5 ./...`.
The server selector was `Test(RunHandoff|CapturedBatch|ParallelBatch|ExecuteFile|PreparedReadPolicy)` with FTS5, race, count=1 and a ten-minute package timeout.
After the signal correction, the server selector
`Test(RunHandoff|CapturedBatch|ParallelBatch)` passed on the final source.
`core-command.sh` and `final-command.sh` in the evidence directory preserve the
exact captured shell programs; `&&` propagates vet/build failure to the envelope.

Sanitized logs and actual recovered envelopes are preserved under
`bench-results/macos-captured-task11-validation/`, including failed attempts.
The encrypted originals remain in the isolated owner's run store, subject to its
retention policy. The immutable baseline archive is
`/private/tmp/capy-task11-baseline/`; its only addition is the regression fixture.

## Fixture portability, benchmarks and review

File allow rules and direct store attachment now use physical fixture paths,
avoiding macOS `/var` versus `/private/var` alias mismatches. The existing
invalid-UTF-8 filename regression retains its Linux-only scope explicitly:
Darwin cannot construct its filename. Valid literal-path, symlink policy and
before-admission denial cases run on macOS. No policy condition was relaxed.

Both baseline and candidate `make bench-quality` runs passed, including all four
capture thresholds. Retrieval metrics, fixture hashes and indexing flags match.
Differences are Git/time metadata, sampled latency and one bounded response length
(3624→3631 bytes). `comparison.json` retains every difference. The archive's Git
identity reports `unknown`; its actual baseline revision is named above.

Matching `BenchmarkExecutorOverhead` measurements used six samples,
`-benchmem`, `-benchtime=100ms` and `-run '^$'`. Median latency changes:

| Runtime | Serial executor | Direct control | Parallel executor |
| --- | --- | --- | --- |
| Shell | −5.76% | −27.00% | +0.73% |
| JavaScript | −2.24% | −0.35% | −0.03% |
| Python | −0.34% | +0.19% | −2.63% |

These are descriptive samples, not a significance or cancellation-latency claim.
The existing overhead benchmark exercises the ordinary executor; direct controls
also vary. Real captured cancellation is verified by the native/compiled tests
above. Both quality reports, logs, performance samples, comparison and the exact
`bench-command.sh` are retained together.

Final independent internal review approved native Task 11 with no findings after
inspecting actual logs/envelopes and command chains, comparing quality reports,
and independently recomputing every benchmark median. Tests remain executed by
the implementation session, not rerun by the reviewer. `kk:test` and
`kk:document` are complete for this slice. No CLI schema/help,
dependency, migration or generated artifact changes are needed for this slice.
CI/release configuration and Linux runtime verification remain Tasks 12–13;
these local native checks do not certify those environments.

The initial internal isolated review found no source defects and returned COMMENT
while verification evidence was pending. External discovery selected Claude
(Anthropic assumed from the shipped mapping); `agy` was missing, authoring-provider
`codex` was excluded, and `gemini` is retired. Automatic approval rejected the
Task 11 source/specification bundle export to Claude because that payload and
destination lacked explicit user authorization. No external source receipt or
completed review is claimed. Owner: project operator for optional export approval;
next action, only if requested/approved, is the read-only manifest review at
`/private/tmp/capy-task11-review/manifest.md`; success requires an actual review
response and source coverage. Internal review and local verification continue.

The operator subsequently replied “Approve” to the explicit bundle/destination
question. This authorizes the Task 11 source/specification export to Claude.
The refreshed submission at `/private/tmp/capy-task11-claude.X3LXCJ/` contains
42 files: all 13 criteria, 26 byte-identical source/specification snapshots, the
Go diff, a Task 11 excerpt and factual manifest. Earlier review conclusions are
omitted. PAL `clink` was invoked with `cli_name=claude`, `role=default` and
`allow_edits=false`, without a model override or requested test execution.
Its result is pending; no external approval or source receipt is claimed yet.

The authorized review subsequently completed with observed model `claude-opus-5`.
Successful criterion/diff/source reads are present in returned tool events;
some unchanged definitions were absent and remained explicit coverage gaps.
Claude corrected its initial ESRCH assumption to the recorded EPERM after a
no-new-files clarification, withdrew its P2 correctness concern and substantially
withdrew the related test criticism. The remaining availability documentation is
now explicit in `design.md`. Two minor code/test corrections passed focused
native/compiled race checks (`f7683d7d-802b-4d1f-b549-762980ae6e6a`) and independent
internal delta review. An expanded source follow-up was rejected as outside the
approved bundle; the safe clarification used only already-approved facts.
See [the native reports and final disposition](external-review.md). External
verdict: COMMENT, no P0/P1/P2; internal implementation/delta verdict: APPROVE.
Task 11 remains complete with its existing Linux/CI/release evidence limits.
