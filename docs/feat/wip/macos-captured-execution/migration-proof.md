# Task 6: preserving schema-8 run history

> Status: implementation and Linux verification complete; isolated code review approved
> Date: 2026-10-23
> Review base: `120abebed9ea4b3d42e88b7906cf25b7dcb4f01a`
> Legacy source: `828ee23bc33e1a18d4a848cdd75feb793c76375d`
> Contract: [upgrade representation](contracts.md#upgrade-representation)
> Work: [Task 6](tasks.md#task-6-preserve-version-8-histories-through-migration)

Normal configured execution attachment now upgrades schema 8 to 9. Readers,
supervisors and diagnostics return an authenticated version mismatch and never
migrate. No new CLI flag, configuration key, dependency or platform capability is
introduced. Operator instructions and downgrade loss are documented in
[run operations](../../../reference/runs.md#run-schema-upgrades).

## Ownership and atomicity

The initial shared attachment authenticates read-only, records the store UUID and
file identity, then closes its probe and lease. Upgrade acquires a fresh exclusive
lifecycle lease, reauthenticates and checks both identities and the schema again.
Live readers, owners and inherited lease references block this step. An intervening
replacement is rejected even if it has the same UUID/key. The identity remains bound
through subsequent shared reattachment and any configured policy-change attachment;
a disappeared store is not recreated during that sequence. A competing successful
upgrade of the same store is revalidated without rebuilding again.

The migration handle performs no reconciliation or retention. It pins one SQLite
connection, selects memory-only temporary sorting, disables foreign keys before
the transaction and follows SQLite's documented [general table-rebuild procedure](https://www.sqlite.org/lang_altertable.html#otheralter): create the replacement,
copy explicitly named columns with a literal Linux scope, drop the old table,
rename the replacement, restore indexes, check foreign keys and commit. It never
edits `sqlite_schema` directly. The pinned driver bundles SQLite 3.51.2; no newer
ALTER capability is assumed. The schema and policy markers change in the same
transaction; database handles close before releasing the lifecycle lease.

Execution attachment uses its own serialization mutex while opening the store.
The general service mutex is released during migration so shutdown can cancel the
registered request. A successfully opened store is closed instead of published if
shutdown won that interval. Ordinary reconciliation/retention resumes only through
the normal attachment path after successful upgrade and revalidation.

## Preserved evidence and capacity

The rebuild preserves rowids, all original run columns, indexes/FKs, program/result
blobs, output/projection/FTS content, cursors, timestamps, owner generations,
quarantine and accounting. Both fresh and upgraded tables reject missing/null/
unknown scopes and cross-scope state pairs. Legacy `retired` is never converted to
Darwin `observed` or inferred into `not_spawned` for a retained row.

Private child results become scoped version 2. Retained rows supply authoritative
proof; an evicted child requires its terminal bit and a valid legacy terminal
result. Only an evicted `capture.state=not_started` maps to `not_spawned`; other
valid evicted terminal results map to Linux `retired`. Missing nonterminal rows,
inconsistent identities/outcomes, malformed results, contradictory capture and
completion states, or inconsistent parent completion abort the entire upgrade.

The v8 indexing path could update only the retained result. It could also persist
`loadRun`'s cleanup-unverified annotation if already-claimed work continued after
incomplete finalization. Migration permits those specific differences, preserving
the authoritative pending state and quarantine. It does not infer cleanup from
advisory error text. Public v1 serialization remains separate from the private v2
manifest; Task 7 owns public publication.

Bounded run/child reservations cover the new metadata. Final index debit is
refreshed after dropping the old table and renaming its replacement, so conservative accounting does not
charge two permanent copies. The filesystem preflight budgets three database
images plus 2 MiB separately from retained quota. It is a conservative estimate,
not a reservation or guarantee against competing filesystem use. SQLite write or
commit failures still roll back; migration never calls retention to obtain space.

## Verification evidence

Host: Linux x86_64, kernel `7.1.1-76070101-generic`, Go 1.27.2, CGO enabled.
Commands used synthetic knowledge/vault keys and isolated stores, captured through
the installed Capy service. The installed owner/store was not upgraded by these
tests. All execution results below are author-attributed.

The fixture [schema_v8.sql](../../../../internal/runstore/testdata/schema_v8.sql)
contains original SQL constants from the named legacy revision, with original
paths and Git blob hashes. Tests construct actual v8 tables rather than only
changing a current-schema version marker. Historical Go writers/readers were also
supplied independently to review, including recovery and indexing code.

| Check | Result and captured run |
| --- | --- |
| Existing scoped/policy/inspection regression subset | Passed 24.609s; `456bdc89-c176-4337-9615-ab295cae40c8`. |
| First new-test compile | Failed on a new unused import, removed; `43b9600a-436d-40ed-bea3-6ff1273eab1d`. |
| Initial migration suite | Failed the causal-cancellation assertion after `database/sql` reported a completed rollback; cancellation is now retained in the error chain; `84a4c4f4-9444-447a-a4f9-d3750c12a477`. |
| Corrected migration race suite | Passed 17.370s; `ad943ebc-4b5a-4a2e-a836-101c02d5c2d3`. |
| Expanded failure/private-wire migration race suite | Passed 20.716s; `553ad59f-4583-4c37-81ea-609dc44903b6`. |
| Full affected integration race | Passed runstore 120.866s / execution 62.035s; `a59ad62e-5591-45d7-8460-6650d03b2428`. |
| Legacy annotation compatibility race suite | Passed 23.371s; `94098712-6d4c-47bc-8f9d-ec731c04aadc`. |
| Selected public capture/doctor/policy regression, vet and FTS5 build | Passed server 67.420s / CLI 17.538s; `48589def-2854-44d9-a853-19552fce63b7`. |
| Final migration/recovery integration race, vet and FTS5 build | Passed runstore 125.013s / execution 62.084s; `5cedd5e4-8488-4b9c-b9ff-2b1036be1a98`. |
| Final diagnostic guidance race, vet and FTS5 build | Passed runstore 6.339s / platform 2.780s / CLI 8.131s; `77033981-75b0-4b7c-b30f-6ed1739f63d8`. |

The final diagnostic change was verified separately after the full integration
run began. It adds authenticated numeric schema metadata and safe shared doctor
formatting; migration/recovery code did not change after that integration run.

Principal commands, with `CAPY_DB_KEY=test-key-for-development`,
`CAPY_VAULT_KEY=test-key` and `CGO_ENABLED=1`:

```sh
go test -tags fts5,integration -race -count=1 -p 1 -timeout 20m ./internal/runstore ./internal/execution
go test -tags fts5 -race -run 'TestCaptured|TestDoctorRun|TestRunPolicy|TestRuns' -count=1 -p 1 -timeout 20m ./internal/server ./cmd/capy
go test -tags fts5 -race -run 'TestCheckRunsSchemaMismatchGuidance|TestDoctorRun|TestInspect' -count=1 -p 1 ./internal/runstore ./internal/platform ./cmd/capy
go vet -tags fts5,integration ./internal/runstore ./internal/execution ./internal/server ./cmd/capy
go vet -tags fts5 ./internal/platform ./internal/runstore
go build -tags fts5 -o /tmp/capy-task6-build ./cmd/capy
```

Fixtures exercise real `SQLITE_FULL` during table copying, a real WAL write error
at a disposable helper's 64-KiB file-size ceiling, cancellation during the final
marker write, a late statement failure and abrupt process death before commit.
Reopening confirms schema 8 and retained history survive. These are controlled
failure fixtures; they do not claim to have filled the host filesystem. Separate
tests cover the free-space preflight, retained-quota refusal, busy inherited leases,
replacement races, another upgrader winning, indexes/FKs, encryption, old raw/text
cursors and retained/evicted batch proofs. No retrieval/executor algorithm changed
and no performance improvement is claimed.

## Independent review and remaining work

Isolated `kk:review-code` (`gpt-6.1-sol`) initially found two P2 issues: accepting
capture states inconsistent with completion, and losing schema-version guidance
in the shared doctor adapter. Both are fixed with rollback and CheckRuns-facing
regressions. The reviewer inspected both corrections and approved the scoped code
with no remaining findings. PAL was unavailable; the reviewer did not run tests.
This record owns the findings and disposition, so no duplicate knowledge entry is
needed.

Task 6 is Linux-verified. Its migration fixtures are generic and included by the
existing macOS runstore CI job, but no native macOS Task 6 result is claimed here.
Task 5's earlier successful native run is baseline evidence only. Native migration
validation remains part of feature release verification in Task 13; owner: feature
implementer, next action: run these tests in native CI and resolve failures before
release. Public v2, production helper v2 and Darwin activation remain Tasks 7–10.
