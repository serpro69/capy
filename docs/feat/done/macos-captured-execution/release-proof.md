# Final native captured-execution verification

> Task: [13](tasks.md#task-13-complete-release-verification-and-documentation)
> Date: 2026-10-26
> Candidate baseline: `ec6586a382135164f92835a8d70551fe1a10ad14`
> Published candidate: `bcf1c04c7fc456e4bc7657597b76d069e1218ef3`
> Status: complete — required verification and independent reviews passed; no release published

## Scope and source identity

The final contract supports Linux and macOS 26 on Apple Silicon with CGO.
Darwin completes `darwin_process_group_observation_v1` as `observed`, permitting
normal retention/reuse with the accepted possibility of racing survivors. Linux
continues to report `linux_descendants_v1` / `retired`. Neither unlocked leases
nor a local result with failed final persistence replace durable proof.

The immediate pre-feature baseline is
`13204a9a68a3def965950e765ca910c615232913`; the original design baseline
`44ceb813ce2d4f129d058f2534faa2a57b07d8bd` is a separate revision. Historical
schema-8 DDL/provenance is retained with the [migration evidence](migration-proof.md).

Hosted-tested head `f9df6ab213e9ad89bf1273d6f630da09a7367cc9` and `ec6586a`
differ only in Task 12 task/evidence documentation. Task 13 changes documentation,
CLI help strings and adds the missing native non-race integration CI step.
Runtime, fixtures and generators retain the tested source; help compilation is
checked locally. No CI success is inferred
from another runtime revision, and no release tag/artifact is published here.

## Acceptance evidence

| Requirement | Evidence and limit |
| --- | --- |
| Bounded Darwin terminal observation and cleanup failures | [Process proof](process-proof.md), native re-entry counterexample, and current hosted executor tests. Evidence is contract-backed; matching XNU implementation is unavailable. Observed completion does not exclude later creation/re-entry. |
| Acquired lease continuity and cancellation-safe exchange | [Lease proof](lease-proof.md); native and Linux real-helper exchange/provenance/teardown fixtures. Both platforms use the common protocol; Linux retains fdinfo checks. |
| Schema-8 history and failure preservation | [Migration proof](migration-proof.md) and hosted native full runstore suite. All 14 top-level `TestMigration*` events pass, including the crash helper; preservation, wrong-key/busy/replacement, disk/WAL failure and abrupt-process cases run. |
| Ordinary, yielded/background, file and serial/parallel public modes | [Activation](activation-proof.md), [mode evidence](modes-proof.md), and hosted compiled-mode JSON. The eight-attached-handler test retains concurrent handlers after sequential startup. |
| Linux lifecycle and history | Both current hosted integration targets, full FTS5 race/vet CI, and local final checks below. The immutable baseline's intermittent close-maintenance failure remains a failure, not a waived candidate assertion. |
| Response bounds, v1/v2 hooks and generated parity | Existing full hook/platform/server/CLI suites and Task 13 checks; scoped evidence remains essential metadata and cursor/redaction versions stay unchanged. |
| Diagnostics and release-binary behavior | [CI proof](ci-proof.md); native read-only diagnostics, build/vet and actual-binary capture/recovery smoke passed. A future tagged release still runs the smoke gate on its exact packaged binary. |

## Hosted evidence inspected for this task

[CI run 40328223610](https://github.com/serpro69/capy/actions/runs/40328223610)
reports success for the tested head above. Both proof artifacts were downloaded
again during Task 13, rather than relying only on the prior report. The Linux
checkout was PR merge revision `2db0edfe9bc5e223578feeedac299383a8e52398`.
Fetching that exact commit and comparing its complete tree with `f9df6ab` returned
an empty diff. Artifacts are retained locally under
`bench-results/macos-captured-task13-validation/{native-ci,linux-ci}/`.

Native environment: macOS 26.6.2 / 25G83, Darwin 25.6.0 arm64, Xcode 26.6 /
17F113, SDK 26.5 / 25F70, Go 1.27.2, CGO, deployment target 26.0. Linux hosted
environment: Linux 6.17.0-1022-azure x86_64, Go 1.27.2, CGO.

| Artifact | Actual package results |
| --- | --- |
| `macos-proof-tests.jsonl` | Executor 33.536s; execution 76.468s; race/integration. |
| `macos-store-tests.jsonl` | Runstore 223.381s; race/integration; no failed/skipped tests. |
| `macos-mode-tests.jsonl` | CLI/compiled MCP modes 140.173s; race; no failed/skipped tests. |
| `macos-retention-tests.jsonl` | Seven regression groups, five repetitions each; 169.049s. |
| `linux-integration-tests.jsonl` | Executor 5.624s; execution 40.880s. |
| `linux-proof-tests.jsonl` | Executor 9.331s; execution 71.713s; race. |
| `linux-queue-tests.jsonl` | Five queue saturation repetitions; 6.360s; race. |
| `linux-baseline-tests.jsonl` | Executor passed 17.244s; execution failed 32.381s at `TestStoredOwnerSIGKILLRetainsLeasesUntilHelperClose`. Preserved historical failure; current fixture passes. |

All current artifacts contain no failed test events. Optional unavailable runtime
fixtures retain skips (native TypeScript/PHP/R/Elixir, Linux TypeScript/R/Elixir);
the unsupported-host control skips on these supported hosts. Required lifecycle,
migration and public-mode fixtures execute. Native non-race integration also ran
locally in Task 12 (18.190s/30.348s); the subsequent changes only corrected the
Linux queue and portable attached-handler fixtures. No new native execution is
claimed from the Linux Task 13 host.

## Local final checks

Host: Linux 7.1.1-76070101-generic x86_64, Go 1.27.2, CGO. Commands run through
encrypted capture with synthetic `CAPY_DB_KEY=test-key-for-development` and
`CAPY_VAULT_KEY=test-key`, using isolated fixture stores.

Capture `72c66b8b-9f15-4e2b-8c43-929f8cd5ed9a` completed exit zero with complete
capture. It ran, in order, `make test-integration`, `make test-integration-race`,
integration-tagged vet for executor/execution/runstore, full FTS5 vet, a trimpath
FTS5 CLI build, and the actual-binary capture smoke script. Non-race packages
passed 5.201s/45.098s; race packages passed 7.670s/74.774s. Smoke verified exactly
one invocation, exact stdout/stderr, exit 7, encrypted history and restart
recovery with Linux retired proof. This build preceded the help-only edits.

Full uncached non-race verification in capture
`88af46e0-0df6-4c72-8617-88f423f58147` completed with exit 1 solely for the seven
configuration-sensitive CLI fixtures below. Every other package passed, including
runstore 129.490s, server 255.296s, store 83.379s, vault 275.116s and TUI 21.511s.
The local race command did not execute because the shell stopped after this
failure. The candidate's hosted full FTS5/race/vet job subsequently passed; no
skipped local race execution is represented as a pass.

The ordinary CLI package failed seven vault import/restore/merge fixtures after
inheriting the host's personal `vault.min_session_bytes=196608`; this is retained
as a failed attempt. Capture `38d0285e-e394-42b7-a1cb-bb5b8a633db0` reproduced
`TestVaultCodex_ImportShowRestore` on unchanged pre-feature source with that exact
synthetic config (exit 1, 2.443s), then passed on baseline with an empty isolated
XDG config (2.884s). All seven failing candidate cases passed under that isolated
config (14.924s), without code or assertion changes.
The required full race check runs in hosted CI on the published candidate;
prior hosted race results are already recorded above. Fixture self-isolation
follow-up is explicitly recorded in Task 13; personal configuration is untouched.

Capture `00c64673-a16d-4e1f-90af-2265e8c58a96` passed the final help build and
rendered execute/runs help, reran the actual-binary smoke successfully, and passed
`TestGeneratedWholeFileArtifacts|TestMergedArtifactsAreIdempotent` (0.004s).
All four Task 13 verification logs, including the failed initial run and controlled
baseline failure, are retained under `bench-results/macos-captured-task13-validation/`.
The workflow parsed with PyYAML; source checks verified both native target commands,
their order, failure-preserving race execution and the new artifact path.
All 202 local Markdown link targets checked after documentation edits resolve;
whitespace validation preserves required YAML spaces.

## Review corrections and remaining hosted gate

The independent spec review identified an exact CI requirement missed by Task 12:
the design requires both integration targets in native CI. Its local non-race
result does not satisfy that CI condition. Task 13 adds `test-integration` before
the existing native race target, retains its JSON artifact and still runs race
and downstream evidence steps if the non-race step fails. No test or assertion
is removed. Owner: feature implementer; publish the reviewed workflow candidate
when authorized, inspect both native target results and the existing Linux/full
checks, then close this gate. Prior hosted success does not certify this addition.

Candidate `bcf1c04` triggered [run 40353055119](https://github.com/serpro69/capy/actions/runs/40353055119).
Its Linux integration proof passed: baseline executor/execution 14.671s/32.149s;
candidate non-race 5.277s/34.445s, race 8.729s/64.242s, and five queue repetitions
6.254s. The full FTS5 race/vet and glamour jobs passed. Tested merge revision
`0e3c5ca1a7b1f98f0cdf1490cadfeaded770b663` has an identical tree to `bcf1c04`.
Native job `121678027361`
was cancelled on 2026-10-26 at 16:04:11 UTC without a runner or any test step.
GitHub's annotation says the job was not acquired after multiple attempts and
reports macOS arm64 capacity constraints. This is unavailable execution, not a
test failure or pass. GitHub rejected the job retry request while the workflow
was active (`job 121678027361 cannot be rerun`); no second execution occurred.
After the workflow completed, GitHub accepted the one native-only retry.
Attempt 2 acquired a runner and completed successfully; the initial capacity
cancellation remains retained and is not reclassified as executed verification.

### Final hosted acceptance

[Run 40353055119, attempt 2](https://github.com/serpro69/capy/actions/runs/40353055119/attempts/2)
is successful for candidate `bcf1c04`, including the full Linux FTS5 race/vet,
glamour and both Linux integration targets. The full Linux check completed in
attempt 1; the native-only retry retained that result rather than rerunning it.
Both lint runs for the candidate also passed.

Native environment remains macOS 26.6.2/25G83, Darwin 25.6.0 arm64,
Xcode 26.6/17F113, SDK 26.5/25F70, Go 1.27.2, CGO and deployment target 26.0.
Downloaded attempt-2 events contain these actual passes:

| Check | Result |
| --- | --- |
| New non-race integration target | Executor 13.290s; execution 43.949s. |
| Race integration target | Executor 24.860s; execution 65.550s. |
| Full runstore race suite | 161.836s; no failed/skipped tests. |
| Compiled native public modes | 117.485s; no failed/skipped tests. |
| Retention/recovery repetitions | All seven groups passed five times; 143.322s. |
| Required-fixture guard | All 50 native fixtures executed and passed. |
| Integration vet/build | Passed. |
| Actual-binary capture/recovery | Passed: exactly one invocation, byte-exact stdout/stderr, exit 7, encrypted history and durable Darwin observed proof. |

The same optional runtime/control skips recorded above remain; no required
lifecycle, migration or public-mode fixture was skipped. Raw events, environment,
job metadata, final-step logs and the capacity annotations are retained under
`bench-results/macos-captured-task13-validation/ci-40353055119/`.
Final independent evidence review approved Task 13 closure after inspecting these
events, logs, source identity and benchmark results.

The API's attempt-2 native `completedAt` value predates its `startedAt` value.
Execution timing therefore comes from the actual JSON events and native log,
which records successful artifact smoke at 16:24:50 UTC, not that anomalous
metadata field. No end-to-end job duration is inferred from it.

## Final matching-baseline comparison

Capture `c01cbadf-117d-46d7-92b3-b40ae520df42` completed exit zero with complete
capture after all other local test runs finished. It compared pre-feature
`13204a9` and candidate `bcf1c04` serially on the same Linux host/Go 1.27.2,
CGO, `GOMAXPROCS=4`, synthetic keys and isolated XDG configuration. The baseline
detached worktree adds only the current `startup_bench_test.go`, byte-identical
SHA-256 `d57c8fd1d1864e9595a42336eefaa1cb5aa3c6da737e5ad588f897d3f116daa8`.
The ordinary executor benchmark file is unchanged across the two revisions.

Both `make bench-quality` runs passed. Every retrieval metric, dataset hash,
diagnostic mismatch list and indexing flag agrees. The only differences are
Git/time metadata, sampled capture latency and four bounded response lengths,
each 61 bytes larger for scoped proof (3561/3561/3565/3568 →
3622/3622/3626/3629). No new knowledge indexing or threshold failure appeared.

Performance commands used `-run '^$' -benchmem -count=6`: ordinary
`BenchmarkExecutorOverhead` with `-tags fts5 -benchtime=100ms`, and real warm-owner
`BenchmarkStoredStartup` with `-tags fts5,integration -benchtime=10x`.

| Runtime | Serial executor median change | Direct control | Parallel executor |
| --- | --- | --- | --- |
| Shell | −0.31% | +2.34% | +0.67% |
| JavaScript | −1.89% | −2.18% | −0.75% |
| Python | +0.01% | +2.85% | +0.24% |

Real captured startup median was 459.453 → 458.425 ms (−0.22%). The new strict
exchange/scoped-proof path allocates more per run: 153617 → 190468.5 B/op and
2627 → 3139 allocations/op. The ordinary serial Python sample also reports
278 → 306 allocations/op (95068 → 96085.5 B/op); no latency increase is apparent
in these samples. These are descriptive measurements, not significance claims;
benchstat was unavailable. All samples and quality reports are retained under
`bench-results/macos-captured-task13-validation/`, with independently recomputable
medians in `performance-comparison.json`.

Darwin measurements remain native evidence from [activation](activation-proof.md)
and [all-mode verification](modes-proof.md), under their recorded SDK/toolchain
and matching immediate-task baselines. Activation's new four capture cases
returned 3628/3628/3634/3640-byte bounded responses for
4999/5001/10000/50000-byte outputs, all passing. Earlier native pre-feature
ordinary-executor comparisons are in [process-proof.md](process-proof.md).
The pre-feature public Darwin capture path rejected execution, so no comparable
before/after capture-latency improvement is claimed. Task 13 adds Linux
pre-feature comparisons; it does not relabel earlier native measurements as new
local macOS executions.

The older native raw benchmark directories are absent on this Linux checkout,
so their arithmetic is attributed to the dated native records and their earlier
reviews, not independently recalculated here. Actual Git comparisons establish
ordinary-executor continuity from Task 1's `a93aa9830f04fbf18bb0295bba8f8e1bd64a0a21`
to `bcf1c04`: `executor.go`, `wait_darwin.go`, `waitid_darwin.go` and `bench_test.go`
are byte-identical (blobs respectively `a7eb98b`, `5e87c42`, `fe8123d`, `f63137e`).
Production executor/execution source is also unchanged from final Task 11
`23fbf90` to `bcf1c04`; the Darwin phase blob is `e3f3ce2`. Task 11's benchmark
preceded its documented final error-path corrections, so that commit comparison
does not reconstruct every uncommitted measured byte. Historical native capture
measurements remain attributed; current native CI establishes final behavior,
not a new latency certification.

## Documentation and bootstrap

README and run operations retain the support matrix, residual-process limit,
upgrade and explicit-discard rollback. Architecture now describes schema 9,
v2 envelopes, acquired references, common exchange and platform-specific cleanup.
AGENTS and CLI help carry the same invariants. Existing routing generator/output
already agree and require no wording change. [Bootstrap](bootstrap.md) is closed;
all granted exceptions were removed and enforcement probes are recorded there.
No Task 13 exception, trust change or new dependency is introduced.

## Independent review

Internal isolated code review (gpt-6.1-sol) inspected 67 production/documentation
files and focused fixtures, plus workflows/smoke configuration, under the full
Go checklist. No substantiated code defect was found. Its initial COMMENT
identified outstanding verification, benchmark and hosted evidence. The spec
reviewer (gpt-6.1-sol) found no remaining source conformance issue after the native
non-race CI correction. Final review returned APPROVE (code) and CONFORMANT
(spec), independently checking all final native fixtures, repetition counts,
Linux full-suite output, smoke and benchmark arithmetic. Neither reviewer
executed tests. Task 13 is complete; archival/status/link edits do not change the
tested runtime or workflow candidate.

All thirteen tasks are complete. The feature is archived under
`docs/feat/done/macos-captured-execution/`; incoming operator/agent links were
updated. Post-archive validation resolves all 206 local Markdown targets and
101 anchors, and whitespace checks pass. The closure diff changes documentation
only. No release tag, deployment or installed-client upgrade is performed.

The [ownership architecture review](architecture-review.md) (gpt-6-astra)
returned SOUND after the ready-token wording clarification, with the full
53-claim extraction and per-dimension verdicts preserved. It verifies topology,
not runtime correctness; delegated security/runtime claims remain explicitly
outside that review's coverage.

External discovery found no API model, Claude available (Anthropic assumed from
PAL's mapping), agy absent, authoring-provider Codex excluded/absent, and Gemini
retired/absent. Prior source-export approval is limited to Task 11; no Task 13
payload was exported and no external corroboration is claimed. No systemic
P0/P1 finding or new convention requires knowledge indexing; accepted decisions,
corrections and evidence are retained in repository documentation.

## Implementation reflection

The largest changes were acquired lease continuity and durable scoped proof,
which fit the existing execution/runstore boundaries without introducing another
service. Native CI exposed operation-budget and fixture-timing assumptions;
immutable comparisons and retained failures kept those repairs separate from
the cleanup contract. Final verification also exposed inherited personal-config
assumptions in unrelated vault fixtures. Darwin's creation/re-entry limitation
remains explicit accepted behavior, with actual observation failures still
quarantined. No requirement was weakened to close the feature.
