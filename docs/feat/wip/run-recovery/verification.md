# Run recovery release evidence

Task 16 is in progress. Linux verification is recorded below; supported macOS
26 arm64/CGO acceptance is still required. Keep the feature in `wip` until that
evidence is available. This record does not certify an installed deployment or
reproduce the original incident's initiating failure.

## Candidate and scope

- Date: 2026-10-30.
- Base production source: `02c63f7fa73878d21c012919f29ddea9ef5fb319`.
  Task 16 adds acceptance fixtures, CI corrections and a production optimization
  discovered by native macOS CI: reuse exact disposition validation within one
  held registry lease while its recovery epoch remains unchanged. New authority
  or audit GC invalidates it; no cache survives the lease. Deadlines, bindings,
  dependencies, schema and generated artifacts are unchanged.
- Host: Linux `7.1.1-76070101-generic`, x86_64, Go `1.27.2`, CGO enabled.
- Pre-feature quality baseline: exact archive of
  `16e088081bf9c16c46be64232677e11a86014cbe`, immediately before Task 1.
  Archive reports have no Git metadata; this revision identifies their source.
- Legacy binaries: local tags `v0.18.1` at
  `44ceb813ce2d4f129d058f2534faa2a57b07d8bd` (schema 8), and `v0.18.2` at
  `d9adda6e564e4058ea9d1035dd76f8204040117d` (schema 9), built from exact archives.
  These are source builds of the tags, not downloaded release artifacts.
- All commands use `CAPY_DB_KEY=test-key-for-development` and
  `CAPY_VAULT_KEY=test-key`, isolated temporary stores, FTS5 and Capy capture.
  No live workspace store was recovered, reset or directly edited.

## Execution ledger

| Check and command | Captured run | Outcome |
| --- | --- | --- |
| Uncached full repository: `make test-race BUILD_TAGS='-tags fts5 -count=1 -timeout 30m -p 1'` | `44585e59-647a-400c-afc9-4d365185e3a8` | Reported vault fixture failures from inherited personal import-size policy; remaining packages running. This attempt cannot be a full pass. Missing fixture `XDG_CONFIG_HOME` isolation is corrected; final verification pending. |
| Native Linux: `make test-integration-race INTEGRATION_TEST_FLAGS=-v` | `2d96986d-54ea-4102-9f14-1bb8c80a034e` | Passed, including both `TestNativeRecoveryAfterRuntimeIncident` cases (12.94s combined). |
| Both accounting modes: `CGO_CFLAGS=-DSQLITE_ENABLE_DBSTAT_VTAB go test -race -tags fts5 -count=1 -v ./internal/runstore -run 'TestRecoveryGCAccountingModes|TestRecoveryRetentionBoundedIndexedPayload'` | `a59b9f2e-c038-45cb-83ce-d0c13cbfe115` | Passed (19.021s); conservative and dbstat subtests both executed, none skipped. |
| Legacy builds: `CGO_ENABLED=1 go -C <tag-archive> build -tags fts5 -o <legacy-binary> ./cmd/capy` | `e341ea54-efa9-4488-9547-f8eb62ee4bf0` / `a72e001e-ec91-477a-a08f-891f6aa33632` | Both passed. |
| Operator and rollout: `CAPY_RECOVERY_LEGACY_BINARIES=/tmp/capy-run-recovery-task16/capy-v0.18.1:/tmp/capy-run-recovery-task16/capy-v0.18.2 go test -race -tags fts5 -count=1 -v ./cmd/capy -run 'TestRecoveryRolloutLegacyBinaries|TestRunsRecoverApplyOfflineCLI'` | `ef45ce11-e42d-4f01-a2ed-b09dfcddd92c` | Passed (18.047s), including both legacy binaries. |
| `make vet build` and `go vet -tags fts5,integration ./internal/execution` | `b1b452eb-1812-40f4-87cd-8d329e766fb2` | Passed. |
| `make bench-quality BENCH_BRANCH=run-recovery-task16-baseline`, same candidate target, then `make bench-compare BASE=run-recovery-task16-baseline TARGET=run-recovery-task16-candidate` | `f50e9c39-15f4-425a-bba9-2d1c59ba395b` / `11cf8390-392b-4170-9bec-d5ddfa0dce0b` / `79ea4e4f-2d13-41f6-be25-3bd876c90bb7` | All passed; identical reported quality metrics on verified dataset `7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`. Performance half skipped because benchstat is absent. |

Failed attempts are retained: `b213d814-13f1-4c8f-ae86-06c4bee3f973` failed the
new fixture's prose assertion that a lease refusal contained “busy”. The
fixture now asserts the actual typed `storage_busy` code, and the full native
race run above passed. Legacy build batch
`a411f2db-6d1d-49dc-b5f4-bf6b3955904f` failed both commands because `-C` was
placed after build flags; the corrected commands above passed. Neither failure
was a production failure or is counted as a passing attempt.

## Contract coverage

| Requirement | Evidence and limits |
| --- | --- |
| Native cross-session recovery | `TestNativeRecoveryAfterRuntimeIncident` uses the existing real-helper failure seams after local cleanup, joins the helper, observes second-owner refusal and retained reads, excludes attached owners from planning, applies offline and restarts for fresh capture. It asserts byte-identical stored results, sibling cursor/output continuity, unchanged pending proof/EOF and no replay markers. Runs on each native backend; Linux observed here. It does not treat a free lease as quiescence proof. |
| Operator workflow | `TestRunsRecoverApplyOfflineCLI` executes candidate CLI/MCP binaries through shutdown, wrong-key/held-reader refusals, plan/apply/receipt lookup, exact retry/conflict/later blockers and fresh capture. Its pending row is synthetic; the separate native test supplies the real-helper failure boundary. |
| Compatible rollout | `TestRecoveryRolloutLegacyBinaries` verifies schema-8/9 binaries refuse execute/status/read against a recovered schema-10 store, admit no command and leave the closed database byte-identical. Candidate reads and fresh execution still work. Set the explicit path-list environment variable to rerun; the ordinary suite skips this release-only test when binaries are absent. No compatibility claim covers intermediate schema-10 builds lacking disposition support. |
| Diagnostics, migration, transactions, history, hooks, stats, retention and indexing | Existing affected package suites are included in the full repository race command. The new native check supplements, rather than replaces, their malformed metadata, complete-inventory, rollback, no-maintenance, partial-projection, snapshot immutability and once-only accounting assertions. Full command result remains pending above. |
| Quality | Final feature changes include run-store search/indexing, so the benchmark trigger applies. Broad store/server/vault quality metrics do not prove disposed-run eligibility or immutability; dedicated recovery-search tests cover those. The contention correction requires refreshed candidate evidence. No benchstat comparison is claimed; measured contention scenarios are reported separately. |
| CI acceptance | Both native jobs now require shared runtime-incident and recovery subcases. The macOS mode selection additionally runs and requires the actual-binary offline walkthrough. Replaced its obsolete `TestDarwinBatchFinalizationFailureQuarantinesPendingChildren` requirement with `TestBatchRuntimeIncidentCompletionOnce`, the shared replacement introduced earlier in this feature. CI execution itself is not observed here. |

## Reviews and remaining acceptance

The isolated `kk:review-spec` reviewer (`gpt-6.1-sol`) reports no remaining
material conformance finding in its targeted source review of Tasks 1–16. It
traced administrative attachment, migration, incident/epoch publication,
inventory/apply, exact bindings, reconciliation, mixed batches, retention/GC,
index side state, inspection/VFS, CLI/MCP, doctor/stats and hook boundaries.
It inspected selected fixtures and the two new acceptance tests. Obsolete
design/implementation status wording was corrected and reread. This was not
an exhaustive review of all 112 feature files or independent test execution.
The isolated internal code reviewer (`gpt-6.1-sol`) found no P0–P3 issue in
18 changed files plus the requested historical gates, helper ordering and
authority writers. It supports the source changes while retaining a COMMENT
verdict for open final runtime acceptance. All its source requests are closed.
External PAL clink `claude` (observed `claude-opus-5-5`, Anthropic) found no
P0–P2 issue in the expanded production correction. Its P3 test-strength and
snapshot-consistency findings are addressed: populate the cache through a
production claim before the fresh-operation negative, establish legacy positive
controls before upgrade, require each version's refusal code, and accurately
describe empty sidecars. Focused final re-review and CI results remain pending.
Discovery selected clink once: no API providers, claude sole eligible executable,
OpenAI/Codex excluded as authoring provider and unavailable, agy unavailable,
gemini retired. All 14 criteria have observed successful Read receipts; external
source coverage is focused/partial. Initial denied historical Git reads were
replaced with exact parent-supplied blobs and provenance. No systemic P0/P1
findings or new undocumented conventions require indexing.

## Failures found during release verification

The already-running [base macOS CI job](https://github.com/serpro69/capy/actions/runs/41063734889/job/124089679828)
supplied native evidence on 2026-10-30. Both native modes failed an obsolete
assertion that expected the causal run ID in prose rather than the structured
recovery hint. Moved/copied planning fixtures passed symlinked `/var` paths to
APIs that require physical owner paths. These test corrections preserve the
existing requirements; they do not relax path validation or causal identity.

Its 256-disposition owner-claim and legacy-marker cases exceeded the unchanged
three-second recovery deadline (3.127/3.164s); capture writes stayed below
45ms. The production change avoids repeated full validation under the same
registry lease and epoch. Initial corrected Linux race measurements were
314/314ms, versus 638/636ms immediately before, with worst concurrent writes
below 20ms. These are representative samples, not a general performance
guarantee. Epoch invalidation, a fresh registry operation and peer-GC retry
tests continue to require exact validation. Native macOS confirmation is still
required. Baseline measurement/profile runs: `23dd882b-cc0b-4ac6-951c-376753a10417`
and `a872e87e-bacb-47c4-8413-44ba1c8143e3`; initial corrected focused race:
`765497dd-495f-4660-afe9-a54d10846a4c` (passed).

CI shell syntax and guard controls passed in
`c8747879-861a-47f6-834a-cf4066386878`: all 55 macOS and 18 Linux required parent
tests exist, complete synthetic pass logs succeed, and skipping a required test
fails. This is a guard test, not native execution. Its first diagnostic attempt
`50dd9ac7-a280-4f15-8315-3d0ac68e9bac` failed because unnamed action steps were
indexed by a mandatory `name`; the diagnostic now handles those steps.

Final focused recovery corrections passed in
`0e8a7b53-c83b-401f-8007-d4b4df3f566d`; final vet/build and refreshed quality
comparison passed in `aa13179d-4a14-492c-b232-90b34db04d40`. All corrected vault
fixtures passed in `664a5596-5283-48a2-9f7c-748c38a8ab0a`, but that combined run
failed a newly added assertion that legacy probes create no WAL file. Those
released read-only probes predate the inspection VFS and can create empty
WAL/SHM sidecars. The corrected assertion requires no WAL frames, unchanged
database bytes, typed storage/schema refusal, exit 1 and empty stderr; both
legacy binaries passed in `9104112b-e709-45a2-9a47-70bc6bd6a44b` (9.488s).
No older binary behavior was silently relabeled as a current inspection guarantee.

Initial passing test hashes were native
`166890c5bf7cfadaf44a794ee9985172f41df7ee8a53b4cb81d87b296a7674cc` and rollout
`03cef6615741fec8fd7072c9c68ff238c2aede8df31bc33ff2cbe24ad5449d97`.
Current native hash (added nonnil exit assertion) is
`144d5236b03037ca447ca4d67cad4fc669ce2a899202509d217c68cb4984b27e`; the first
corrected rollout hash was `2448d3510dcf318567704fa88408a4adcaaa56a2ba28039f41b40f58249f092c`.
Refreshed complete Linux native and accounting checks passed in
`03cb2210-dbbe-4846-b5f0-7c622c3dee42` and
`8202eeb3-1e7b-427a-b100-143199e43f0a` (batch
`66dd215b-ac5f-4b4d-88a3-a5f1ab8b89b0`). Final test-only strengthening adds
legacy positive controls before upgrading the same store and fills validation
through a production owner claim before testing a new operation. Run
`ae8d7639-425e-4a75-a427-fcf2ef2ef31a` passed both cache negatives and the
schema-9 case but exposed an omitted selected-path suffix in the newly strict
schema-8 message assertion. The corrected final rollout passed (14.131s) in
`e7e72b37-af01-45ba-98be-50c9d462bd5a`, with source hash
`2b4ee25e6baec4d4d22a8a277cfa7baf8197d15a89c8fe2c2623d12b4ea32513`. The pushed
candidate's CI outcomes remain required before task completion.

Remaining gate (owner: unassigned): run `make test-integration-race` and the
actual-binary `TestRunsRecoverApplyOfflineCLI` on macOS 26 arm64/CGO at the final
candidate. Record exact revision, OS/architecture/CGO, captured commands and
unskipped subcase outcomes. The existing macOS CI job now selects these cases;
successful CI artifacts for the final revision can supply the evidence. Earlier
task-level macOS runs and Linux storage fixtures cannot substitute for it.

Deployment prerequisite (operator-owned): stop/reload every attached service
onto a disposition-aware binary before applying recovery. Schema 10 alone does
not certify an intermediate build; no installed-environment rollout was performed
here. Existing nonblocking diagnostic wording follow-ups remain in Tasks 8, 12
and 13 with their original scope and next actions.
