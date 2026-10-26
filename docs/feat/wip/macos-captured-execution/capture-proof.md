# Task 9: private Darwin captured execution evidence

> Date: 2026-10-26
> Scope: [Task 9](tasks.md#task-9-integrate-a-private-darwin-captured-run)
> Contracts: [typed evidence and protocol](contracts.md)
> Baseline: `31c0ef259ad8dbcc5f67fc12c7fc9cd5ec15730c`

The private Darwin helper now uses the same acquisition-proven exchange,
authenticated run binding, pending-before-spawn transition, capture queue and
finalization path as Linux. Darwin phases produce scoped `observed` or `pending`
evidence through the shared stream and compiler/runtime aggregation. Production
CLI/MCP admission remains disabled until Task 10. Linux retains its descendant
retirement implementation.

## Native environment and execution

Host: macOS 26.6.2 (25G83), Darwin 25.6.0 arm64, Apple M4 Pro, SDK 27.0
(26A425), Go 1.27.2. Every command used `CGO_ENABLED=1`,
`MACOSX_DEPLOYMENT_TARGET=26.0`, synthetic `CAPY_DB_KEY`/`CAPY_VAULT_KEY` values,
and the temporary build cache recorded in [bootstrap.md](bootstrap.md).
Fixtures create isolated stores and roots; no operational run store is reset.

The operator authorized the seven exact native commands after installed capture
rejected Darwin before admission. Temporary routing exceptions were removed;
project/baseline configs match their saved originals and a read-only installed-hook
probe confirmed restored denial. See the bootstrap record. The implementation
session executed these checks; reviewers did not run them.

| Check | Result |
| --- | --- |
| `go test -tags fts5,integration -run=TestDarwin -count=5 -timeout 10m -v ./internal/executor ./internal/execution` | Final attempt passes: executor 20.083s, execution 54.284s |
| `go test -tags fts5,integration -race -count=1 -p 1 -timeout 20m ./internal/executor ./internal/execution ./internal/runstore` | Passes: executor 29.036s, execution 43.552s, runstore 87.962s |
| `go vet -tags fts5,integration ./internal/executor ./internal/execution ./internal/runstore` | Exit 0 |
| `go build -tags fts5 ./...` | Exit 0; a non-fatal sandbox warning prevented updating Go's main-module version metadata cache |

Logs are retained under `bench-results/macos-captured-task9-validation/`:
`focused-{1,2,3}.log`, `race.log`, `vet.log`, and `build.log`.

## Exercised contracts

| Boundary | Native fixture evidence |
| --- | --- |
| Capture and recovery | Real encrypted stdout/stderr, zero/nonzero exit, sanitized projection/preview, wrong-key refusal, reopen recovery and once-only side effects |
| Local versus durable evidence | Append failure preserves committed prefix and command exit; failed finalization reports durable pending; live service, durable admission and restart all refuse reuse |
| Lost terminal notice | Successful stored observation and exit recover without repeating the command |
| Phase aggregation | Actual Rust compiler/run success and failure paths; completed compiler plus failed runtime spawn remains observed, not not-spawned |
| Observation failure | Escaped pipe holder prevents natural EOF and persists pending/incomplete evidence; the owner quarantines admission |
| Owner loss and leases | Buffered authorization and running variants; gate returned after ready, leases block replacement/reset until backend close, eventual slot reuse and zero-or-one/once-only execution |
| Public boundary | Darwin service execution still rejects before admission and creates neither run storage nor command side effects |

The independent review found an inherited shared-path gap: a valid local cleanup
result with failed persistence did not quarantine the still-live owner. The
correction checks persistence acknowledgement before accepting reuse, while
preserving accepted stored proof and validated helper acknowledgement recovery.
Both Darwin and Linux regression fixtures cover this transition; the Linux
fixture has not executed on this Mac.

## Retained fixture failures and corrections

The first focused run passed executor checks (20.579s) but failed the owner-loss
fixture before publishing its identity; execution finished with failures in
145.519s. Its consuming `Wait4` synchronization was replaced with the existing
executor fixture's public libc `waitid(WSTOPPED | WNOWAIT | WNOHANG)` pattern.
This preserves the notification and validates the event without a competing
terminal reap. The integration-only cgo file is excluded from ordinary builds.

The second focused run passed executor checks (20.254s) but failed owner-loss
expectations (execution 51.127s). Darwin sends SIGHUP and SIGCONT when a stopped
process group becomes orphaned, which invalidated the fixture's assumption that
its deliberately stopped helper would remain stopped after its owner died.
Apple documents this in [_exit(2)](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/_exit.2.html).
The running variant refused admission with unverified cleanup after helper loss;
it did not manufacture observation completion.

The corrected test helper ignores SIGHUP only in the deliberate-stop fixture and
checks the buffered/running state before owner death. Post-death checks still
require lease retention through backend close, blocked reset, delayed slot reuse
and once-only execution. This proves control-EOF cleanup under the stated fixture
conditions; it does not certify production handling of a stopped orphaned helper
with default SIGHUP disposition. Production signal handling, cleanup budgets and
reuse assertions were not weakened. All five final repetitions pass.

## Benchmarks and remaining evidence

Both six-sample performance runs passed with the same command and environment:
`go test -tags fts5 -run=TestNoSuchTest -bench=BenchmarkExecutorOverhead -benchmem -count=6 -benchtime=1s ./internal/executor`.
The baseline is an isolated `git archive` of the exact revision above; candidate
and baseline share the temporary build cache. Reports are
`performance-{base,candidate}.txt` and `performance-comparison.json` in the log
directory. These medians describe the samples; no statistical significance or
performance equivalence is claimed.

| Benchmark | Baseline median ms/op | Candidate median ms/op | Change |
| --- | ---: | ---: | ---: |
| Serial shell executor | 4.093 | 3.952 | −3.45% |
| Serial shell direct control | 3.644 | 3.330 | −8.61% |
| Serial JavaScript executor | 29.235 | 28.991 | −0.84% |
| Serial JavaScript direct control | 28.076 | 29.402 | +4.72% |
| Serial Python executor | 13.055 | 12.985 | −0.54% |
| Serial Python direct control | 12.047 | 13.205 | +9.61% |
| Parallel shell executor | 0.897 | 0.901 | +0.48% |
| Parallel JavaScript executor | 3.506 | 3.709 | +5.80% |
| Parallel Python executor | 1.576 | 1.594 | +1.15% |

Serial allocation medians are unchanged; parallel JavaScript changes from 300 to
301 allocations/op, with the other parallel medians unchanged. These existing
legacy-executor benchmarks do not measure a prior successful public Darwin
capture backend; that path was and remains disabled in this slice.

Both `make bench-quality BENCH_BRANCH=macos-captured-task9-baseline` and
`make bench-quality BENCH_BRANCH=macos-captured-task9` exit 2 at the same four
`TestBench5000ByteThreshold` cases because public capture still rejects Darwin.
Store and vault quality checks pass. Every non-metadata JSON value, including
retrieval metrics, fixture hashes and diagnostics, is identical; only timestamp
and Git identity metadata differ (the baseline archive reports Git identity as
unknown). The baseline Make invocation also prints a non-fatal missing-Git
metadata warning. Full reports are `bench-results/macos-captured-task9{,-baseline}.json`;
logs and the exact three metadata differences are in `quality-{base,candidate}.log`
and `quality-comparison.json` in the log directory. No assertion or skip was
changed. Owner: feature implementer; Task 10 must rerun these four cases through
the activated backend before public capture acceptance.

Owner: feature implementer. Linux runtime regression execution, including
`TestStoredFinalizationFailureQuarantinesLiveOwner`, remains unverified on this
Mac and must run on Linux/CI before release. Tasks 10–13 retain public activation,
all-mode native acceptance, CI/release-artifact checks and final feature
verification. Darwin observation completion is not host quiescence.

Final isolated internal review approved Task 9's private native scope with no
remaining findings after inspecting these artifacts, comparing the quality
reports and checking restored configuration/enforcement evidence. External
review returned no report before its timeout. Task 9 is complete; the feature's
public and Linux/release prerequisites above remain open.
