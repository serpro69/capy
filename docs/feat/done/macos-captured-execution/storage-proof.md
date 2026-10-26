# Task 5: scoped cleanup in fresh run stores

> Status: Task 5 complete; Linux verification and isolated review passed
> Date: 2026-10-23
> Baseline: `828ee23bc33e1a18d4a848cdd75feb793c76375d`
> Contract: [storage and typed evidence](contracts.md#storage-and-typed-evidence)
> Scope: [Task 5](tasks.md#task-5-enforce-scoped-proof-on-fresh-stores)

Fresh run stores use schema 9. Admission supplies an explicit backend scope;
neither SQL cleanup column has a default. SQL and typed storage boundaries reject
unknown scopes/states, Linux `observed`, and Darwin `retired`. An empty scope is
invalid. Production admission still selects Linux explicitly and retains the
existing unsupported-platform check.

`executor.CleanupEvidence` carries the scope/state pair. Runstore completion,
recovery and retention use named-policy acceptance: `not_spawned`, Linux
`retired`, or Darwin `observed`, together with the separate completed-outcome,
owner and lease conditions. Observation acceptance does not establish machine
quiescence. Capture loss and actual process status remain independent of cleanup.

Every mutable run update binds the run ID, generation and original scope.
`Pending` now uses the same transaction as its manifest copy. Manifest cleanup
is authoritative after child-log eviction; retained rows and manifest copies are
compared in one SQLite snapshot. Batch metadata, comparisons and aggregation
share a read transaction, which closes before lazy search indexing uses the
single-connection pool. Ordinary run loads and lists compare the copy in their
own single SELECT. Invalid copies fail explicitly and cannot authorize expiry.

Batch finalization checks for pending children before validating the complete
aggregate. Validating every child on every completion made large-batch recovery
quadratic and exceeded the existing write deadline under the race detector; the
candidate failure and corrected run are recorded below. No deadline or assertion
was relaxed.

The temporary `execution.linuxV1Completion` adapter accepts only explicit Linux
scope. It maps the existing private `Started`/`Retired` result into scoped storage
evidence and preserves durable pending evidence if finalization failed. Task 8
removes this adapter when production protocol v2 carries typed evidence.

Public run/result JSON remains version 1, without a new cleanup field. The private
schema-9 child manifest stores `result.cleanup`; the public Go result carries the
typed value with serialization deferred to Task 7. No CLI flags, configuration,
hook registration, runtime languages or deployment bindings change in this task.
Their operator contract is inherited from [run operations](../../../reference/runs.md).

Existing schema-8 attachment fails with an authenticated `schema_mismatch` error
that identifies the found/supported versions. It does not initialize, migrate,
reset or discard that store. This intermediate branch must not be released before
Task 6 implements preservation. The installed test-capture binary/store was kept
separate from candidate binaries and isolated test stores.

## Verification

Host: Linux x86_64, kernel `7.1.1-76070101-generic`, Go 1.27.2.
Commands use `CGO_ENABLED=1`, `CAPY_DB_KEY=test-key-for-development` and
`CAPY_VAULT_KEY=test-key`, with temporary test roots. All routine commands were
captured through the installed Capy service. Results below are author-attributed.

| Check | Result and captured run |
| --- | --- |
| Baseline full runstore/execution integration race | Passed: 91.930s / 60.746s; `3ec42cac-9c29-44e0-b2a3-498336e7aa08`. |
| First selected candidate suite | Runstore passed 25.128s; execution compile failed on four remaining string comparisons, subsequently corrected; `1995736c-894b-47bf-8305-1668c40fa7ec`. |
| Scoped SQL, transitions, snapshot and adapter race tests | Passed: runstore 7.917s / execution 1.012s; `3995dcd9-26ad-4450-8e77-000e1be8815a`. |
| First full candidate integration race | Runstore failed large queued-batch recovery after 97.173s; execution passed 61.152s; `026630f1-3c79-4227-b554-6e03eec0511f`. |
| Corrected batch recovery plus scoped tests, race/count=2 | Passed 14.796s; `001ab58b-0c2f-4b1e-9668-1ecda81e38dc`. |
| Baseline quality benchmarks | Passed; `dbe3dbe7-44f1-47bb-9c74-b988e4914589`. |
| Review regression plus scoped/storage tests | Passed with race: runstore 7.768s / execution 3.241s; `9470d265-14e7-4355-9971-414d097011ab`. |
| Final full affected integration race | Passed: runstore 102.501s / execution 62.194s; `d7d88dc3-524f-4d5a-8042-ac4ee37ff787`. |
| Public capture regression tests with race | Passed: server 251.099s / CLI 156.617s; `329a0bc6-08ec-42be-b7f0-717232b8a757`. |
| Affected integration vet and full FTS5 binary build | Passed; `fa7bba6e-af05-4293-89e7-62ced9d5e263`. |
| Candidate quality and comparison | Passed: `c221f36f-39cf-4983-a440-e63872540356`; matching dataset hash and every reported metric unchanged, comparison `f46077e7-1f99-460e-90ca-c22f44b22bf9`. |

Full affected integration command:

```sh
go test -tags fts5,integration -race -count=1 -p 1 -timeout 20m ./internal/runstore ./internal/execution
```

Public regression and build checks:

```sh
go test -tags fts5 -race -run 'Test.*(Execute|Run|Capture|Batch)' -count=1 -p 1 -timeout 20m ./internal/server ./cmd/capy
go vet -tags fts5,integration ./internal/executor ./internal/runstore ./internal/execution ./internal/server ./cmd/capy
go build -tags fts5 -o /tmp/capy-task5-build ./cmd/capy
```

The public regression run preceded the final service recovery correction; the
new real-helper regression and final full execution integration suite cover that
correction. Vet/build and candidate quality checks ran after it.

New tests cover omitted/null/unknown SQL values, scope/state pairs, generation and
scope mismatch, illegal transitions, capture-loss independence, pending quarantine,
both accepted retention policies, child-log eviction, mixed and inconsistent
manifest proofs, a deterministic concurrent-completion snapshot, and schema-8
refusal without recreation. Darwin storage cases use synthetic evidence; they do
not execute or certify the native process backend.

### Benchmark comparison

Baseline uses an unmodified archive of the named baseline revision; candidate
uses the final source. Both `make bench-quality` runs passed, with dataset hash
`sha256:7d45338724b05181ebc92bd0b74eb7708bdd4fba27a8d6830b54a127f2b6ba2d`.
The archive has no Git metadata, so its report labels the revision `unknown`;
the archive command's explicit source revision above establishes provenance.
Qualstat printed the candidate as Base and the archive as Target because of the
argument order; all metric deltas are unchanged, so this does not alter the result.
The four capture threshold cases pass; their raw timing measurements vary between
runs, and serialized response lengths differ by at most one byte.

The existing `BenchmarkRunSearch` checks capture-only, first-search, repeat-search
and fragmented-search costs. Each side ran six samples using the same command:

```sh
go test -tags fts5 -run '^$' -bench '^BenchmarkRunSearch$' -benchmem -benchtime=1x -count=6 ./internal/runstore
```

| Mode | Baseline median | Candidate median | Change |
| --- | ---: | ---: | ---: |
| Capture only | 21.137ms | 21.657ms | +2.46% |
| First search | 19.328ms | 18.487ms | −4.35% |
| Repeat search | 2.443ms | 2.377ms | −2.67% |
| Fragmented search | 203.862ms | 192.195ms | −5.72% |

Captured runs: baseline `0c7b12af-36af-4130-8ba3-42c28981f620`, candidate
`6a2cbd8c-1d77-42fc-8a08-14c2750c847a`. These are small-sample measurements,
not a statistical significance claim; `benchstat` was unavailable. The new
executor file adds only shared evidence types and no execution algorithm change.
Quality JSON, complete performance logs and comparison output are retained locally
under `bench-results/macos-scoped-task5-validation/` (ignored artifacts).

### Isolated review

The independent `kk:review-code` reviewer (`gpt-6.1-sol`) approved the scoped
implementation after inspecting 33 changed/new files and the relevant unchanged
consumers. PAL was unavailable. The reviewer inspected source and author-provided
results; it did not execute tests.

One P2 was found and fixed: a committed `not_spawned` completion could be finalized
again after a lost notification/helper close failure, replacing its recorded
runtime/cancellation result with a generic storage error. Both service recovery
branches now honor a durable terminal outcome before finalizing unfinished work.
`TestStoredUnstartedTerminalSurvivesHelperCloseFailure` uses a real inherited
helper and a failing store-close wrapper, preserving `runtime_unavailable`,
terminal persistence, status equality and absence of command side effects or
quarantine. The reviewer independently checked the correction. No open or deferred
code findings remain; no knowledge entry is needed because this record captures
the issue and disposition.

The same independent reviewer approved the final task/evidence closure after
checking both quality reports and the six-sample performance logs. No Go source
changed during that closure check.

## Remaining feature boundaries

Task 6 owns migration and existing-history compatibility; Task 7 owns public v2;
Task 8 owns production protocol v2 and adapter removal; Tasks 9–11 own native
activation/all-mode acceptance. The existing macOS CI job runs the affected
runstore/execution packages, but this Linux session has no new hosted macOS result.
Task 12.6 retains the earlier native retention investigation. These are tracked
feature dependencies, not claims that Task 5 supplies their evidence.

Task 5's storage and intermediate Linux requirements are complete. The remaining
feature tasks retain their existing owners, prerequisites and verification gates.
