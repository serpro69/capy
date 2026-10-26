# Final native captured-execution verification

> Task: [13](tasks.md#task-13-complete-release-verification-and-documentation)
> Date: 2026-10-26
> Candidate baseline: `ec6586a382135164f92835a8d70551fe1a10ad14`
> Status: verification and independent review in progress; no release published

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

Full uncached FTS5 and FTS5 race verification is running in capture
`88af46e0-0df6-4c72-8617-88f423f58147`. Completion, benchmark results and independent
review remain required before closing Task 13. Owner: feature implementer.

The ordinary CLI package failed seven vault import/restore/merge fixtures after
inheriting the host's personal `vault.min_session_bytes=196608`; this is retained
as a failed attempt. Capture `38d0285e-e394-42b7-a1cb-bb5b8a633db0` reproduced
`TestVaultCodex_ImportShowRestore` on unchanged pre-feature source with that exact
synthetic config (exit 1, 2.443s), then passed on baseline with an empty isolated
XDG config (2.884s). All seven failing candidate cases passed under that isolated
config (14.924s), without code or assertion changes. Remaining packages in the
initial full run continue; its `set -e` will prevent the planned local race phase.
The required full race check also runs in hosted CI on the published candidate;
prior hosted race results are already recorded above. Fixture self-isolation
follow-up is explicitly recorded in Task 13; personal configuration is untouched.

## Review corrections and remaining hosted gate

The independent spec review identified an exact CI requirement missed by Task 12:
the design requires both integration targets in native CI. Its local non-race
result does not satisfy that CI condition. Task 13 adds `test-integration` before
the existing native race target, retains its JSON artifact and still runs race
and downstream evidence steps if the non-race step fails. No test or assertion
is removed. Owner: feature implementer; publish the reviewed workflow candidate
when authorized, inspect both native target results and the existing Linux/full
checks, then close this gate. Prior hosted success does not certify this addition.

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
Go checklist. No substantiated code defect was found. Its provisional COMMENT
reflects outstanding verification, benchmark and hosted evidence, not approval
of unfinished acceptance. The spec reviewer (gpt-6.1-sol) found no remaining
source conformance issue after the native non-race CI correction; completion
evidence is still pending. Neither reviewer executed tests.

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
