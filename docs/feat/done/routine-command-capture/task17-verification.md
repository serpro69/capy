# Task 17: release verification

Automated verification and trusted live Codex/Claude acceptance are recorded
below. This report distinguishes subprocess tests from actual model-host
observations and retains the limits of each measurement.
Task 17 is complete; the final independent evidence review is conformant with
no remaining findings.

## Environment and provenance

The initial source is `3dc03a267c0acd3e6ccbc5b21b50b672ca97d3f5` on
`feat/routine_command_capture`, Linux amd64, Go 1.27.2 with CGO and `fts5`.
The installed PATH capy is `v0.17.3` and rejects `capy execute`; it is not the
candidate binary. The prior Task 16 development binary bootstrapped the initial
candidate through captured execution into `/tmp/capy-task17/bin/capy`. The reviewed
final build is `/tmp/capy-task17/final-bin/capy`; its SHA-256 is in
[the acceptance measurements](task17-acceptance.json).

Codex CLI reports `0.163.0`, Claude Code `2.1.296`, and bubblewrap `0.6.1`.
Tests use synthetic knowledge/vault credentials, isolated XDG config/data/cache
directories under `/tmp/capy-task17`, and `/tmp/capy-go-cache`.
No installed binary, production database or hook feature override was changed.
After explicit user approval, normal host UIs saved trust only for the disposable
fixture and its reviewed generated hooks/MCP binding. Deferred MCP discovery was
unavailable through Codex's search entry point; the exposed tool registry worked.

## Byte recovery and real transport

[task17-acceptance.json](task17-acceptance.json) records six executed synthetic
fixtures, exact stdout/stderr SHA-256 values, child status, captured bytes,
serialized MCP result bytes and JSON-RPC framing bytes separately:

| Fixture | Raw bytes | Initial result bytes | Exit |
| --- | ---: | ---: | ---: |
| Passing tests | 180,000 | 2,905 | 0 |
| Failure after 130,000 bytes | 260,035 | 2,989 | 1 |
| Install-shaped synthetic output; no network install | 168,030 | 2,925 | 7 |
| Stderr only | 144,000 | 2,928 | 1 |
| Giant line, binary bytes and multibyte text | 150,920 | 2,860 | 0 |
| Private block and synthetic API key | 115 | 1,350 | 0 |

All captures are complete and raw bytes match the expected generated streams.
No-intent responses initially report `index=not_requested`. Default reads suppress
the protected fixture values. The largest execution/retrieval MCP result is
7,141 bytes, below 8,192. These are wire/result measurements, not UI folding.

The failure beyond the old truncation boundary is recovered by run search and a
CLI read at raw line 10,001. First/repeated search took 250.0/26.8 ms in this run;
these single observations under test load are not a statistical comparison.
A fresh MCP process recovers all statuses, including failed commands, and every
execution marker remains exactly one byte after recovery. The persisted run-store
files contain none of the checked plaintext output/private markers.

## Actual filesystem confinement

`TestRunCLIWorkspaceWriteSandbox` passes with `CAPY_CLI_SANDBOX=1` (2.308 s).
Bubblewrap makes the filesystem/home/cache read-only while permitting only the
test owner and its runtime temporary directory. Owner-local capture succeeds;
excluded-owner admission fails before its marker; an independent home-write
probe fails. This establishes OS confinement, not model-host hook activation.

The first namespace probe in the outer sandbox failed creating a NETLINK_ROUTE
socket (`operation not permitted`). Running the existing test with namespace
creation permitted passed without changing assertions or bubblewrap confinement.
Captured result: `/tmp/capy-task17/sandbox-result.json`.

## Live host acceptance

[Selected live evidence](task17-live-acceptance.json) records actual tool calls and
observed results from Codex CLI 0.163.0 and Claude Code 2.1.296. Both hosts launched
on `/tmp/capy-task17/host-owner` with the final binary first on PATH, synthetic
knowledge credentials, isolated capy XDG directories and no vault key. Existing
host authentication was retained. No account data, reasoning records or unrelated
session content is included in the selected evidence.

The user explicitly approved the fixture-only exception to the plan's exclusion
of host trust mutation. Codex's normal folder prompt and hook browser were used;
the browser showed all five generated hooks active/trusted. A fresh conversation
established startup delivery. Claude's normal folder/MCP prompts selected this
project and only its capy server. All subsequent approvals were one-call choices;
no trust bypass, broad allow rule, auto-approval mode or feature override was used.
This follows the [normal Codex review flow](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks),
not a setup-generated trust claim.

Codex root session: `01a15918-540a-7a52-baa8-e69ec6d74665`.
Child: `01a15926-71b1-7491-801b-34492a9e1af7`,
`/root/task17_child_acceptance`.
Claude session: `6992a39a-2706-41dc-9549-344400663199`.
Codex root/child received capy's 3,809-byte guidance block. The normal Codex
launch used `--no-daemon --no-alt-screen --sandbox workspace-write
--ask-for-approval on-request`; Claude used `--permission-mode default`.

| Live check | Observed outcome |
| --- | --- |
| Whole-request routine denial | Root `printf x > native-before; make test`, root `curl https://example.invalid; make test`, and child `make test` were denied by PreToolUse. The prefix marker is absent; the HTTP command never ran. |
| Execute once and recover | MCP `make test` exited zero with 224,000 captured bytes. MCP status/read recovered its output. The execution counter remained exactly `x`. |
| Failure beyond truncation boundary | MCP `make fail` exited **2**, retaining 240,059 bytes and the recipe's error-7 stderr. Five bounded searches reached the marker; the returned cursor recovered it at a window beginning at byte 118,272. The failure counter remained exactly `x`. |
| Handoff and late output | `wait_ms=0` returned an admitted run; later status/read recovered both early and late text after a two-second command, exit zero and complete capture. No execution retry occurred. |
| Child path | The child captured `TASK17_CHILD_CAPTURE` once, then successfully used status/read; the default native routine gate remained enforced. |
| Exception still composes with security | The exact `Bash(make -n test)` routing exception allowed the dry run. Adding a synthetic denied or matched-ask `printf` sibling still denied the entire Codex request with the exact pattern and settings source. |
| Actual sandboxed CLI parity | Native CLI capture from `subdir/` used the parent owner's store; parent MCP status/read recovered that CLI run. No sandbox escalation was requested by the test agent. |
| Worktree/cwd separation | MCP execution with `cwd=linked/` retained the parent owner/store. Explicit CLI `--project-dir linked/` created a separate run DB and was recovered with that same selector. `linked/` is a real Git worktree. |
| Unavailable owner | Native CLI capture with `/proc` as owner returned exit 1, `not_started`, `storage_unavailable`, and null run ID. The intended command marker is absent; no fallback or retry occurred. |
| Claude guidance-only scope | Native `make -n test` was allowed after a normal one-call approval, with guidance and no execution markers. Actual MCP capture/status/read recovered `TASK17_CLAUDE_CAPTURE` with exit zero and 21 exact bytes. |
| Claude deny/ask behavior | The synthetic deny was blocked by its PreToolUse hook. The matched ask presented its rule in the normal approval UI; the one approved harmless `printf` ran once. |

Codex's `tool_search_tool` entry point returned `unsupported call`. Actual MCP
execute/runs schemas and tools were available through the exposed code-mode
registry; the host used those tools successfully. This discovery limitation did
not grant native routine fallback. `wait_ms=30000` calls completed normally and
zero-wait recovery worked.

The final review identified that those short calls did not measure default-wait
viability. Both trusted hosts were therefore resumed for one additional
27-second command with `wait_ms=25000` and `timeout=40000`. Both returned a healthy
`running`/`recording` response before command completion, without a host timeout.
Status/read then recovered both markers from the same run with exit zero and
complete capture:

| Host | Run ID | Terminal duration | Captured bytes |
| --- | --- | ---: | ---: |
| Codex | `70ec2b9a-37e3-4dcc-9651-c34fe0c764a5` | 27,011 ms | 44 |
| Claude | `34426298-5d84-42cf-9194-43c9bfe057a4` | 27,012 ms | 46 |

The durable JSON retains the requested inputs, admission/start timestamps,
initial-response timestamps, terminal timestamps and recovered markers. The
response arrived about 25 seconds after admission in both cases; the response
timer starts before admission. Codex's outer tool-call clock included time
awaiting operator approval and is not used as command latency. These probes
establish that the default attached wait works in the recorded hosts; no smaller
wait was needed. They do not claim a universal maximum client timeout.

The live matrix complements full compiled nested-owner, external-handler merge,
evidence-state and generated-artifact tests. No unrelated hook was removed or
rewritten. Claude's UI also showed its existing additional PreToolUse handler
alongside capy's hook. Exhaustive combinations of every host, hook, owner chain
and evidence state were not rerun through model sessions.

The largest selected observed MCP result reserialized to 6,394 bytes; this
excludes host/code-mode framing and is not a claim that code mode printed every
intermediate search result. The six black-box fixtures above separately measured
their complete result/framing sizes.

After all requested tool probes completed, the parent verified zero unfinished
runs. Codex's final follow-up report generation was interrupted; the parent
assembled follow-up evidence directly from saved tool records instead of relying
on an unfinished model summary. Codex then exited normally through `/quit`
(exit 0); Claude exited through `/exit` (exit 0). Codex SessionEnd cleared one
main and one child observation entry, preserving both permanent lock inodes.
Parent and explicit linked-owner run files were 0600 and contained none of the
checked plaintext output markers. No fixture HTTP command executed.
Both resumed wait-probe sessions subsequently exited normally with code 0.

## Review corrections

The [isolated code review](task17-code-review.md) and
[isolated specification review](task17-spec-review.md) found and re-reviewed six
corrections: nested capture policy composition, exact program/path bytes,
conservative page-growth eviction, live unverified-cleanup retention, sanitized
head/tail previews and cancellation during dispatch. Final source assessments are
**APPROVE** and **CONFORMANT**. No new external dependency or schema was added.
The CLI/configuration contract remains in [the run guide](../../../reference/runs.md).
Existing FTS5/race CI and release conventions apply; CI configuration is unchanged.

## Full suite and real project output

`go test -tags fts5 -count=1 ./...` passed in one fresh invocation with local
sockets permitted: CLI 502.660 s, server 268.013 s, runstore 90.439 s; every other
package passed. This run includes all production corrections and the preview
regression. Captured execution took 503.694 s, exited zero, and retained all
1,211 stdout bytes with no stderr. Raw recovery SHA-256 and the 2,400-byte CLI
envelope are recorded under `real_project_test` in the acceptance JSON.

The initial outer-sandbox run passed all packages except config/server, whose
Unix/TCP fixture sockets were denied. The successful invocation reran the entire
suite with the same assertions and isolated credentials/configuration.
Build, `go vet -tags fts5 ./...`, and whitespace checks pass.

The complete core race run passed execution integration (36.848 s), executor,
sanitize, security, hook and platform. Its sole failure was the previously
recorded `TestRunSearchConcurrentCapture`: it required every search to succeed
while concurrent capture could legitimately exhaust a bounded index write.
The independently reviewed fixture correction accepts only an explicit
`index_failed` with partial/failed metadata during capture, joins the writer,
then strictly requires ready coverage, expected hits, no work claim, and every
captured raw byte. It does not tolerate persistent failure, quota/projection
errors or capture loss. Concurrent-attempt and post-capture timings are separate.
The full functional run preceded this test-only correction.

The final complete runstore race suite passes (84.221 s), including the corrected
concurrent-search fixture. Compiled capture/handoff/execute-file/serial-batch,
policy/rotation/reset and byte-path regressions pass with `GOFLAGS=-race`
instrumenting both tests and subprocess binaries (168.695 s). Focused server
races pass (33.357 s); parallel/captured-batch server races pass (69.841 s) and
compiled parallel MCP controls pass (12.257 s).

The dbstat-enabled search/projection/recovery checks pass (25.528 s), using
`CGO_CFLAGS=-DSQLITE_ENABLE_DBSTAT_VTAB`; the conservative-only regression skips
in that mode as intended. Final private-protocol/request serialization races
pass (1.013/1.012 s), including unknown-field and legacy-reader rejection.
Supervisor integration tests assert descendant retirement and lease release;
no orphan-cleanup assertion failed. Synthetic store-file scans found no output
or protected fixture markers in plaintext.

## Quality and performance

Fresh `make bench-quality` runs passed for an archive of pre-feature `595018f`
and the current candidate. Dataset SHA-256:
`7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
The archive has no `.git`, so the baseline report labels its revision `unknown`;
the exact archived commit is recorded here instead of inventing provenance.

`make bench-compare` against that baseline exits nonzero because six expected
match-layer labels changed after Task 12's valid-AND correction: curated accuracy
1.000→0.900, Markdown 1.000→0.917. Recall/ranking did not regress; overall R@1
improved 0.910→0.917 and Markdown MRR 0.938→0.951. The existing
[Task 12 disposition](task12-verification.md) applies. Comparison against the
accepted Task 13 report passes with unchanged quality/context metrics. Reports
remain in `bench-results/routine-task17-{baseline,final}.json`.

[Performance measurements](task17-performance.json) contain six samples of three
iterations for executor/retrieval and capture/search/redaction benchmarks. Other
validation was running and benchstat is unavailable; these are descriptive
medians, not statistical proof of an improvement/regression.

| Operation | Median | Accounted bytes | Index debit | WAL bytes |
| --- | ---: | ---: | ---: | ---: |
| Capture only | 24.50 ms | 344,071 | 0 | 927,032 |
| First search | 20.19 ms | 1,413,127 | 1,069,056 | 1,977,632 |
| Repeat search | 2.55 ms | 1,413,127 | 1,069,056 | 2,010,592 |
| Fragmented search | 258.76 ms | 2,978,159 | 1,839,104 | 4,124,152 |

Executor medians changed −0.5% shell, +4.3% JavaScript and +2.2% Python; direct
runtime controls also varied (+8.4%, +4.1%, +13.1%). Search medians changed −16.1%
fuzzy, −18.5% porter and +2.4% trigram. The former RRF tier fixture now resolves
through a different valid layer and is skipped by that tier benchmark; the
quality comparison above measures the semantic result.

Streaming redaction checkpoints stayed 119–121 bytes. Secret/private-block
benchmarks allocated 320 B per 16-KiB block. Paced whole-process probes captured
1 MiB and 16 MiB completely with exit zero: maximum RSS was 29,312 and 32,316 KiB.
This observes bounded growth for those fixtures, not a universal memory ceiling.
No plaintext run spool was introduced; explicit synthetic test-log exports under
`/tmp/capy-task17` are verification artifacts.

## Reproduction and readiness

Every build/test uses `CGO_ENABLED=1`, `-tags fts5`, synthetic `CAPY_DB_KEY` and
`CAPY_VAULT_KEY`, isolated XDG config/data/cache paths and `GOCACHE`.
The captured result envelopes and explicit test-output exports are under
`/tmp/capy-task17`: `full-final.json`, `race-core.json`, `race-final.json`,
`race-server-dbstat.json`, `race-batch.json`, `protocol-final.json`,
`quality-result.json`, `performance-result.json` and
`build-vet-compare.json`. Local run IDs are locators subject to TTL/quota, not
permanent evidence; durable measurements are linked above.

The host fixture points to the final binary through setup-generated bindings,
preserving its installation ID. Reproduction uses `/tmp/capy-task17/final-bin`
first on PATH and `/tmp/capy-task17/host-owner` as the project. The fixture now
contains the exact dry-run routing exception and restrictive synthetic deny/ask
rules described above. Its two execution counters are already populated; do not
repeat those non-idempotent fixture commands to recover their output.

The implementation required six final review corrections, including two exact
byte/accounting regressions and policy composition across nested captures.
Live testing also exposed a Codex discovery-entry-point limitation; the actual
loaded MCP registry remained usable. Keeping direct tool records, command status,
captured bytes and host approval delay separate made those outcomes reviewable.
The shared capture architecture and public contracts otherwise followed the plan.

Optional editorial follow-up:
`kk:clarify-docs docs/feat/wip/routine-command-capture/task17-verification.md`.

## CI timing follow-up (2026-10-20)

[CI run 39427079593](https://github.com/serpro69/capy/actions/runs/39427079593)
failed the Codex MCP shutdown and background-boolean tests.
[The preceding run](https://github.com/serpro69/capy/actions/runs/39426874651)
also failed closed-output cleanup and the configured-deadline transport test.
The fixtures assumed that encrypted-store initialization and supervisor startup
would finish within one second. Background handoff can instead return an
`admitted` run before PID files exist; a foreground deadline can expire before
the first process starts, producing `start_failed` rather than `timed_out`.

The shutdown fixtures now request a one-millisecond background response wait and
wait, within a bounded interval, for complete positive PID values before closing
the transport or signalling its owner. The boolean fixture checks structured run
state and polls until the handed-off background run reaches `running`.
Foreground timeout fixtures use a five-second execution budget and explicitly
require a recorded start before asserting `timed_out`. The override fixture
runs longer than that configured default and must finish under its explicit
ten-second timeout. Production deadlines and lifecycle behavior are unchanged.

Validation:

- Five repetitions of the affected race tests passed with `GOMAXPROCS=2`.
- `go vet -tags fts5 ./...`, formatting, and whitespace checks passed.
- Independent code review found no actionable issues.
- The full `go test -tags fts5 -race ./...` run passed the repaired tests and
  every package except `cmd/capy` and `internal/vault`. The unrelated CLI vault
  failures inherited a personal 192-KiB minimum import size and all passed when
  rerun with an empty `XDG_CONFIG_HOME`. The vault package reached the ten-minute
  limit in `TestCodexCanary`, which scans the real local Codex corpus and skips
  when that corpus is absent in CI. The entire vault package passed with
  isolated session roots (`go test -tags fts5 -race ./internal/vault`, 177.5 s).

Host-test isolation follow-up, outside this CI timing fix: `setupCodexVaultEnv`
and the ad hoc restore/merge CLI fixtures still inherit personal import policy.
Give those fixtures an empty `XDG_CONFIG_HOME`, as `setupVaultEnv` already does.
For local validation, also use an empty `CODEX_HOME`. Limit a shared empty
`CLAUDE_CONFIG_DIR` to the vault package's real-corpus isolation; leave it unset
for config/server tests that create their own Claude roots under temporary
homes. Run real-corpus canaries separately with a time budget appropriate to the
local corpus.

## Remaining CI timing failures (2026-10-20)

[CI run 39435211513](https://github.com/serpro69/capy/actions/runs/39435211513)
on `819d133` exposed the same startup assumptions in two additional transport
fixtures: `TestMCPStdioBooleanArgs/background_and_force_strings` expected
`running` from an initial `admitted` response, and
`TestMCPExecuteFileCapturedLifetime/configured_deadline_and_explicit_override`
expected `timed_out` after a one-second deadline produced `start_failed`.
The run also exhausted Go's default ten-minute **package** timeout in
`cmd/capy`; the active spawn-failure subtest had only just started. The server
package passed in 597.986 seconds, leaving almost no timeout margin there either.

The stdio boolean test now checks structured state, polls background startup
after a one-millisecond response wait, and gives the foreground command five
seconds to start and time out. The execute-file fixture uses a five-second
configured deadline, requires `StartedAt`, and runs its override command for
5.2 seconds under an explicit ten-second deadline. Its exact output, complete
capture and forbidden-marker assertions remain in place. The CI race invocation
now specifies `-timeout 20m` per package; per-operation test deadlines and
production behavior are unchanged.

Validation:

- Three repetitions of the original focused fixtures passed locally (26.097 s);
  the CI failures were load-sensitive and were not reproduced in that run.
- Three repetitions of the repaired fixtures passed with `GOMAXPROCS=2` and
  `GOFLAGS=-race`, instrumenting both tests and subprocesses (80.374 s).
- `go vet -tags fts5 ./...` and Go formatting checks passed.
- Full `go test -tags fts5 -race -timeout 20m -count=1 ./...` validation passed
  `cmd/capy` (523.074 s) and every package except config/server. The invocation's
  shared `CLAUDE_CONFIG_DIR` overrode fixtures' temporary `HOME` roots, breaking
  session-directory resolution and sharing synthetic sessions across tests.
  This was a validation-environment error; that override is absent in CI.
- With `CLAUDE_CONFIG_DIR` unset, every failing config/server test and neighboring
  source-resolution/sweep tests passed (1.021/9.261 s). The race rerun used
  `-run 'TestResolveSourceProject|TestSearch_ProjectOverrideScopes|TestVaultSearch_ProjectOverrideScopes|TestVaultSweep_'`
  against `./internal/config ./internal/server`, with fresh XDG/Codex roots and
  the same synthetic keys. The full suite was not rerun a second time.
- Independent code review approved the three changed test/CI files with no
  actionable findings. The external PAL review could not run because the Gemini
  API rejected its configured API key; it supplies no review evidence.

Fixture-isolation follow-up, outside this CI repair: config's session-directory
tests and server's `setupVaultSweepProject`, `setupVaultSweepMultiProject` and
`setupCodexSweepEnv` rely on an unset inherited `CLAUDE_CONFIG_DIR`. Clear that
variable inside the fixtures so their temporary `HOME` owns discovery even when
a developer exports a custom Claude root. The validation invocation above
unsets it without changing production configuration.

## Response-write startup synchronization (2026-10-20)

[CI run 39459426896](https://github.com/serpro69/capy/actions/runs/39459426896)
on `9416745` failed only
`TestMCPRunHandoffTransport/response_write_failure_cancels_owner`.
The fixture closed stdout before issuing execution and assumed its 1.5-second
response wait guaranteed a `started` marker. Under CI load, the response failed
before the command started. The package finished in 767.445 seconds, within
the new twenty-minute limit; every other package passed.

The fixture now exercises two distinct response failures without a fixed startup
delay. The undelivered-admission case closes stdout before a zero-wait execution
and recovers the cancelled run by listing it from a new owner. Startup is not
promised for that case. The running-command case first receives the handoff,
waits for the command's marker, then closes stdout and requests `tools/list` to
trigger a response failure. It requires the same recovered run ID and recorded
startup. Both cases keep stdin open, require exit 1 with a broken-pipe diagnostic,
forbid the command's late side effect, and require persisted cancellation.
This retains coverage of both missing-receipt recovery and actual process
cleanup. Production behavior and deadlines are unchanged.

Adjacent execute-file, batch and Codex shutdown fixtures already wait for startup
markers or valid PID contents before triggering cleanup.

Validation:

- Ten repetitions of both cases passed with `GOMAXPROCS=2`, `GOFLAGS=-race`,
  `-tags fts5` and synthetic knowledge/vault keys (84.589 s).
- `go vet -tags fts5 ./...`, Go formatting and whitespace checks passed.
- Independent code review approved the change with no actionable findings.
  The external reviewer remains unavailable due to the API-key failure recorded
  above and was not retried.
- Full `go test -tags fts5 -race -timeout 20m -count=1 ./...` validation passed
  every package in one invocation: CLI 516.339 s, server 420.948 s and vault
  203.081 s. The run used synthetic keys, fresh XDG/Codex roots and
  `CLAUDE_CONFIG_DIR` unset. With `-json` added for logging, its complete event
  log is `/tmp/capy-ci-39459426896-tests.jsonl`. These are local results; a new
  hosted CI result requires pushing the repair.

## CI package contention follow-up (2026-10-21)

[CI run 39611870504](https://github.com/serpro69/capy/actions/runs/39611870504)
on `160240f` failed two storage-dependent tests:

- `TestMCPRunHandoffTransport/background_capture_and_explicit_own_cancel`
  received `storage_unavailable` instead of `different_owner`.
- `TestIntegration_Performance_BatchExecuteWithSearch` reported an unpersisted
  child terminal outcome, with one queued child and four skipped children.

Both operations traverse bounded run-storage writes. Opening a second owner
runs maintenance before cancellation can inspect ownership; supervisor
finalization has its own bounded write. The errors do not identify the failing
storage operation or prove CPU contention. The two tests passed three focused
race repetitions normally and ten repetitions on one CPU before any changes.

The main CI race command now uses `-p 1` to reduce competition between test
packages performing encrypted database work. This changes package scheduling;
race instrumentation, concurrency within each package, all assertions, the
`fts5` tag, complete `./...` package selection and the twenty-minute package
timeout remain intact. Production write deadlines are unchanged.

Validation:

- Paired full-package race runs covered `cmd/capy`, `internal/server`,
  `internal/runstore`, `internal/store` and `internal/knowledge`, using `-p 8`
  on CPUs 0–1 and `-p 1` on CPUs 2–3. Both use `-count=1 -timeout 20m`, synthetic
  keys, fresh XDG/Codex roots and an unset `CLAUDE_CONFIG_DIR`. This is a local
  contention comparison, not a reproduction of hosted runner hardware.
- The parallel run failed `TestLargeAdmissionMakesProgressAcrossEvictionBatches`
  during cleanup: both reconciliation and maintenance returned storage errors
  (`internal/runstore`, 189.933 s). CLI (660.255 s), server (519.355 s), store
  (234.285 s) and knowledge (243.901 s) passed. The original two CI failures
  did not reproduce; this is evidence of a different load-sensitive storage
  failure, not proof of their originating operation.
- The serial run exited successfully: CLI (525.957 s), server (406.083 s),
  runstore (81.102 s), store (108.671 s) and knowledge (111.936 s). This includes
  both original failing packages and the runstore cleanup test that failed in
  the parallel comparison. Total elapsed time was 1,235.484 s; serial scheduling
  trades a longer job for less package-level contention. Other packages were
  not rerun for this CI-only change.
- Independent source review found no introduced defect and supports the
  scheduling change. Its CI-repair assessment remains COMMENT because the
  original two failures were not reproduced; local results support a mitigation
  without proving their cause. No systemic findings require indexing.
- `git diff --check` passes. `actionlint` is not installed. PAL has no configured
  model provider, so it supplied no external review evidence.

Hosted verification is pending because this repair has not been pushed. The
branch maintainer should inspect the next CI run after pushing; acceptance is
the unchanged complete race suite passing. If storage failures recur with
serial packages, investigate the originating storage operation and its bounded
deadline rather than weakening ownership or durable-finalization assertions.
