# Issue #142 incident evidence and investigation

Investigated 2026-10-28 against source revision `591bf33` and `v0.18.1`. [Issue #142](https://github.com/serpro69/capy/issues/142) reports that one run with missing cleanup evidence blocked another session's captured execution, while the supported reset would discard unrelated completed history.

The archived sessions point to failed finalization while the original MCP owner was still operating. They do not establish why persistence failed or whether old execution processes survived. The incident's Linux database and host evidence were unavailable during this investigation.

This document preserves the incident evidence, source findings and separate lifecycle follow-up. The [design](design.md), [contracts](contracts.md) and [tasks](tasks.md) own the selected recovery approach, requirements and implementation work. Source verification here is not an independent review of that later design.

## Session evidence

Vault sources, accessible with `capy vault show <prefix> --format json`:

- Original session: `01a17db3-213f-7131-bb81-e63bac1a001b`.
- Later diagnosis: `01a17eff-b2aa-7320-a568-bfe8af12d233`.

The affected run is `d853a401-b382-433b-886f-d83853645863`, owned by MCP generation `c1599bb9-e6bd-4dd3-8cc4-5a2742c0a4a0`. The incident used Linux amd64, Capy `v0.18.1` and run schema 8. Times below are UTC on 2026-10-27; line numbers refer to the archived JSONL retrieved during the investigation.

| Time | Recorded observation | Source |
| --- | --- | --- |
| 19:11:23 | Fixture-preparation command starts; its response yields after a requested 1,000 ms wait. Only the 83-byte launch line is retained. Initial indexing reports `index_failed`. | Original, line 2099 |
| 19:11:43–19:12:37 | Read/status still report `running`, capture `recording`, 83 bytes, no terminal result. | Original, lines 2106, 2114, 2124 |
| 19:13–19:18 | Other commands are admitted under the same MCP owner; several have recorded exit 0 and complete their reads. | Original, lines 2133–2254 |
| 19:19:07 | Cancellation of the affected run returns `cancel_requested=false`, no error, and the old `running`/`recording` result. | Original, line 2262 |
| 19:19–19:32 | The same owner continues completing work. Cancellation of two other runs succeeds; subsequent status reports `cancelled`. Another captured command exits 0 at 19:32:42. | Original, lines 2263–2460 |
| 20:35:57 | A later owner's unrelated run completes successfully, according to doctor's retained inventory. | Later, line 716 |
| 20:37:17 | New execution is rejected before admission with `cleanup_unverified` and no blocking run ID. | Later, line 690 |
| 20:38:11 | Doctor reports 207 retained runs, two unfinished records, and the store-wide block. Its eight recent entries omit the older affected run. | Later, line 716 |
| 20:42:12–20:42:39 | List/status/read identify the original run: still last-recorded `running`, now overlaid with `cleanup_unverified`; 83 bytes remain readable and terminal persistence is false. | Later, lines 793, 803, 804 |

The initial indexing error is not proof of a finalization error or its cause. Session activity/end timestamps do not establish when the owner process exited or quarantine was first persisted. No incident-store reset, credential change or manual database modification was performed during the investigation.

## Likely lifecycle path and existing correction

In `v0.18.1`, `Service.Cancel` returns the saved result with `cancel_requested=false` and no error when the run belongs to the current owner but its control entry is absent. A finished entry would return its in-memory completion result instead. The worker removes its control entry after `execute` returns. The same owner's later successful commands show that the missing completion record cannot by itself establish that the MCP owner had already crashed. See the historical [handoff implementation](https://github.com/serpro69/capy/blob/v0.18.1/internal/execution/handoff.go).

The source contains a path matching these observations:

1. The helper completes process retirement but fails to commit `Store.Finish`.
2. It sends its local outcome with `TerminalPersisted=false`.
3. The owner removes the finished worker from its control map. Status/read subsequently reload the older database result.
4. The `v0.18.1` quarantine condition checks helper wait failure or unsuccessful retirement, but omits failed terminal persistence. This case leaves admission open while that owner holds its slot lease.
5. After the lease becomes available, reconciliation sees pending durable proof and quarantines the store.

Sources: historical [service](https://github.com/serpro69/capy/blob/v0.18.1/internal/execution/service.go), [supervisor](https://github.com/serpro69/capy/blob/v0.18.1/internal/execution/supervisor_linux.go) and [recovery](https://github.com/serpro69/capy/blob/v0.18.1/internal/runstore/recovery.go).

**This is the leading explanation, not a confirmed reconstruction.** The transcript contains no terminal helper message or finalization error identifying lock contention, a deadline, disk failure or another cause. The default execution timeout does not establish this run's outcome.

Commit [`3394c99`](https://github.com/serpro69/capy/commit/3394c99b8cb255f6d4708f949c1547769cc8c0cc), included in `v0.18.2`, adds `!outcome.TerminalPersisted` to the quarantine condition. See [service.go](../../../../internal/execution/service.go) and `TestStoredFinalizationFailureQuarantinesLiveOwner` in [stored_recovery_integration_test.go](../../../../internal/execution/stored_recovery_integration_test.go). It corrects delayed blocking; it neither restores missing terminal data nor supplies recovery that preserves history.

## Source findings and verification

Five isolated CoVe agents independently checked identity/cursors, archive access, cleanup gates, disposition consumers and archive transitions against the same source baseline. All completed without access to the initial comparison or its report. They inspected code, tests and contracts without executing tests or inspecting the incident database. The findings below consolidate those checks with the original diagnostic investigation.

### Diagnostics lack durable attribution

`checkQuarantine` returns a generic message, while `Quarantine(ctx)` writes only a store-wide bit. `Inspect` lists at most eight recent runs and has no owner identity or last-phase field in its run diagnostics. Ordinary live pending runs and actual blocking incidents must therefore be distinguished; missing causal attribution cannot be recovered merely by showing every unfinished row. Output events identify their captured phase, but not a later silent phase or failed finalization attempt. Sources: [owners.go](../../../../internal/runstore/owners.go), [diagnostics.go](../../../../internal/runstore/diagnostics.go), [doctor_runs.go](../../../../internal/platform/doctor_runs.go), [event schema](../../../../internal/runstore/store.go).

### Store identity governs cursor continuity

Run UUIDs, owner-generation UUIDs and store UUIDs are distinct. Cursors bind to the store identity; raw/projection cursors also bind the run/view/stream, projection cursors bind redaction version, and list cursors bind pagination/filter state. They do not contain a filesystem path or owner generation. Retaining identity, coordinates and compatible reader semantics can preserve them. An archive would need historical routing, but not inherently a new cursor format. Bare run IDs currently resolve through one selected active store. Sources: [raw reads](../../../../internal/runstore/read.go), [projection reads](../../../../internal/runstore/read_projection.go), [list cursors](../../../../internal/runstore/list.go), [batch cursors](../../../../internal/runstore/batch_list.go), [migration preservation tests](../../../../internal/runstore/migrate_test.go), [reset identity tests](../../../../internal/runstore/retention_test.go).

### Current query attachment is not an immutable archive reader

`create=false` prevents initialization, not mutation. Normal attachment opens SQLite read-write, reconciles, maintains history and starts maintenance; configured attachment can publish policy changes under exclusive ownership. Close also reconciles, maintains and checkpoints. Underlying read helpers apply expiry and projection-compatibility rules. Search claims ownership/index work and writes metadata even with an existing index; ordinary captured output can have no index at all. `Inspect` is nonmutating but exposes bounded metadata rather than historical content. Sources: [attachment and close](../../../../internal/runstore/store.go), [policy](../../../../internal/runstore/policy.go), [search ownership](../../../../internal/execution/search.go), [claims](../../../../internal/runstore/claims.go), [search](../../../../internal/runstore/search.go), [inspection](../../../../internal/runstore/diagnostics.go); existing [policy](../../../../internal/runstore/policy_test.go), [retention](../../../../internal/runstore/retention_test.go) and [search tests](../../../../internal/runstore/search_test.go).

### Locks and cleanup evidence establish different facts

An exclusive lifecycle lock excludes cooperating lease holders, not escaped descendants: commands do not inherit private lease descriptors. Reset enforces explicit discard, filesystem validation and the exclusive lifecycle lease. It does not open SQLite, inspect cleanup rows or verify that old execution groups stopped; that is an operator prerequisite. Sources: [lease references](../../../../internal/runstore/lease_refs.go), [reset](../../../../internal/runstore/reset.go), [operator reference](../../../reference/runs.md#key-rotation-and-explicit-data-loss).

Linux `retired` and Darwin `observed` have different scoped meanings. Darwin observation explicitly permits racing/escaped survivors. Historical `pending` can represent unfinished execution or completed local cleanup whose persistence failed. Structured metadata lacks PID/PGID, process-birth and host/boot identity sufficient to reconstruct descendant ownership. Existing tests cover live Linux descendants after helper loss and pending proof after finalization failure on both platforms. Sources: [Linux executor](../../../../internal/executor/supervised_linux.go), [Darwin executor](../../../../internal/executor/supervised_darwin.go), [scoped contract](../../done/macos-captured-execution/contracts.md), [Linux recovery tests](../../../../internal/execution/stored_recovery_integration_test.go), [Darwin capture tests](../../../../internal/execution/captured_darwin_integration_test.go).

### Administrative resolution affects multiple consumers

Clearing quarantine flags alone is insufficient: reconciliation rediscovers pending evidence or generation mismatches. Slot publication replaces its generation without resetting `cleanup_unverified`, and historical warnings currently depend on the mutable owner registry. `Service.unverified` is an independent process-local latch. Sources: [recovery](../../../../internal/runstore/recovery.go), [owners](../../../../internal/runstore/owners.go), [run views](../../../../internal/runstore/runs.go), [service](../../../../internal/execution/service.go).

The execution invariant is `terminal == (CompletedAt != nil && CleanupAccepted(cleanup))`; pending evidence cannot simply become terminal. Batch manifests require matching proof/terminal copies and reject absent nonterminal children. Retention protects pending records. Any administrative disposition must therefore remain distinct across admission, owner reuse, search claims, diagnostics, batch history and retention/accounting. A transaction can atomically update database state, but cannot establish process cleanup, acquire kernel locks through SQL, clear service-local latches or make old binaries understand new semantics. Sources: [cleanup validation](../../../../internal/runstore/cleanup.go), [batch snapshots](../../../../internal/runstore/batch_snapshot.go), [batch retention](../../../../internal/runstore/batch.go), [retention](../../../../internal/runstore/retention.go), [index claims](../../../../internal/runstore/claims.go).

### Archive replacement requires more than copying the main file

Existing path checks, authentication and permanent locks are useful primitives, but do not provide archive discovery or an interruption-safe replacement protocol. An absent active database permits ordinary initialization. Preserving only `runs.db` cannot guarantee preservation of WAL-resident data; the transition must retain a complete database image or matching usable sidecars and preserve lock identities. Archives also need credential/schema compatibility, expiry/search semantics and capacity rules: a new store's quota does not account for archived files. Generic corruption-backup and rekey helpers do not implement the complete transition. Sources: [path selection](../../../../internal/config/runs.go), [initialization](../../../../internal/runstore/store.go), [anchored reset](../../../../internal/runstore/reset.go), [SQLite helpers](../../../../internal/sqliteutil/sqliteutil.go), [migration identity/headroom](../../../../internal/runstore/migrate.go), [retention](../../../../internal/runstore/retention.go).

### Verification limits

The checks supported the factual integration claims with the cursor, operator-responsibility and transaction-boundary qualifications stated above. They established integration surfaces, not total implementation cost or comparative safety: the initial claim that archive-and-replace is necessarily a larger implementation was not established.

The selected direction and rationale belong in [Rejected Alternatives](design.md#rejected-alternatives); the potential machine-verifiable recovery path and revisit triggers belong in [Conditional future improvement](design.md#conditional-future-improvement). CoVe did not reproduce the incident or independently approve the detailed feature design.

## Test evidence

During the original investigation on macOS, these existing tests passed with `-tags fts5 -count=1`, both synthetic environment keys and isolated stores:

- `TestRecoveryQuarantinesUnknownCleanupWithoutExpiringEvidence`
- `TestRecoveryMixedProofsAndClaims`
- `TestInspectReadOnlySnapshot`
- `TestInspectFailurePreservesStore`

Captured run: `0747ab5d-0f08-419a-945c-d2b27388ebaf`; exit 0, complete capture; runstore test time 2.021 seconds. These verify storage behavior, not the historical Linux interruption. Linux helper-loss/finalization tests were inspected, not executed natively on this host. CoVe added source verification, not additional runtime test results.

## Separate lifecycle follow-up

At the inspected baseline, [supervisor_unix.go](../../../../internal/execution/supervisor_unix.go) makes one complete finalization call under a 250-ms deadline. The SQLite transaction-start helper can retry busy errors within that deadline, but there is no separate retry of the complete finalization operation. The helper reduces the returned error to `TerminalPersisted=false`, and broad outcome reasons lose specific cleanup errors. Once a yielded worker is removed, ordinary status/read cannot retrieve its in-memory finalization-failure result.

The following investigations remain outside the recovery feature's implementation scope. Owner: unassigned. They are not release prerequisites unless new evidence invalidates a stated safety invariant.

- [ ] **Linux lifecycle reproduction:** exercise yielded execution with injected finalization failure on `v0.18.1` and current code; compare cancellation/status, subsequent same-owner and second-owner admission, and retained successful sibling output. This distinguishes the known delayed-quarantine path from the still-unknown initiating failure.
- [ ] **Finalization robustness:** inject transient writer contention and permanent storage failure around completion; evaluate bounded idempotent persistence retries or a small durable outcome record while the authenticated helper retains its leases. Preserve typed failure telemetry without exposing backend errors/credentials, repeating execution or weakening quarantine.

Neither candidate establishes that the incident was caused by the 250-ms deadline. Diagnostics and operator recovery are tracked only in the feature [task list](tasks.md), avoiding a competing implementation checklist here.

## Design-review finding corroboration

The supplied design-review findings were checked against `593a74b` on 2026-10-28. The corrections below change the proposed contracts and implementation/test ownership; they do not constitute runtime fixes or reviewer approval of the revised design.

| Finding | Corroboration | Resolution |
| --- | --- | --- |
| P1 migration entry points | Valid ambiguity. The `open` orchestrator in [store.go](../../../../internal/runstore/store.go) invokes migration before reconciliation/admission when configured execution attachment is enabled; `openAttachedSchema` itself reports incompatibility. | Choose automatic configured-execution 8→9→10/9→10 upgrades plus the explicit offline alternative. Specify busy/failure behavior and that a quarantined refused execute can already have migrated. Task 2 verifies both paths; Task 15 owns the future AGENTS.md invariant update. |
| P2 disposed result mutation | Valid immutability/guard-contract concern, but the claimed need for a new weaker UPDATE path is incorrect. [ClaimIndex/recoverClaim](../../../../internal/runstore/claims.go) and [saveIndexResult](../../../../internal/runstore/search_index.go) already update results outside the execution-generation-guarded `Store.update`; index batches validate their worker claim. | Choose byte-immutable disposed `runs.result`, with snapshot/digest validation and separate mutable index state. The current per-stream progress table lacks aggregate state/error fields, so schema 10 also supplies bounded `run_search_state`. Tasks 2, 8, 9 and 13 cover creation, seeding, overlay and all writer/claim-recovery paths. |
| P2 fixed planning budget | Valid operational gap. A complete offline scan could repeatedly exceed five seconds, and the old 30-second apply cap would also undermine a longer plan. | Give planning and apply five-minute defaults plus positive finite `--timeout`; name exact longer-budget retry commands, retain all-or-nothing validation and preserve earlier caller deadlines. Tasks 7, 8 and 14 test the usable escape path. |
| P2 phase-column default | Valid missing schema constraint. SQLite requires a non-NULL default for ADD COLUMN with NOT NULL; a nullable enum CHECK alone also accepts NULL. See [ALTER TABLE](https://www.sqlite.org/lang_altertable.html#alter_table_add_column) and [CHECK constraints](https://www.sqlite.org/lang_createtable.html#check_constraints). | Choose a rebuilt runs table with `last_phase NOT NULL`, enum CHECK and no default; backfill historical `unknown` explicitly and require admitted/queued at new admission. Task 2 covers omission/NULL rejection, row/FK/index preservation and copy headroom. |
| P2 hook coverage | Valid unowned acceptance claim. [posttooluse.go](../../../../internal/hook/posttooluse.go) observes execute/fetch, not run queries, and `healthyRunEnvelope` currently ignores recovery metadata. | Task 9 adds a defensive presence rejection and structured-marker handling, with healthy controls and explicit query-tool exclusion. Healthy envelopes omit the optional field. Hook sources and regression suites are listed in the plan. |
| P3 recovery UUID selector | Valid public ambiguity even though the current disposition model uses the run ID as its key. | Use `--receipt-id`/`receipt_id` and `recovery.receipt_id`, accepting only receipt UUIDs; incident IDs cannot resolve through that selector. Tasks 5 and 14 own validation. |
| P3 benchmark scope | The proposed narrowing is not valid under the repository contract. [AGENTS.md](../../../../AGENTS.md#benchmarks) requires quality benchmarks after search/indexing/chunking/executor changes, including the planned run-store changes. [Makefile](../../../../Makefile) runs store/server/vault fixtures, not only knowledge retrieval. | Make the final-diff trigger explicit while preserving the required gate. Documentation-only changes do not trigger it. Focused run-store tests establish eligibility/coverage/immutability; the broad benchmark is not claimed to prove those contracts. |
| P3 transaction naming and stats ownership | RESERVED is technically accurate: [BeginImmediateContext](../../../../internal/sqliteutil/sqliteutil.go) documents acquiring it. Naming the helper is clearer. [handleStats](../../../../internal/server/tool_stats.go) already uses the shared [runChecks](../../../../internal/server/tool_doctor.go) path, but the plan omitted its coverage. | Name `sqliteutil.BeginImmediateContext(ctx, db, "runs")` in apply and Task 8. Task 1 and the files/evidence tables explicitly cover existing stats diagnostics and unchanged once-only byte accounting. |

All valid gaps have contract and task changes. The indexed `kk:arch-decisions:run-recovery:disposed-row-index-mutation` note's conclusion about requiring a new weaker index UPDATE is not supported by the existing writers above; use the corrected source reading and immutable model when implementing. The subsequent author recheck is recorded below; implementation tasks remain unstarted.

## Fresh-read implementation readiness

All five feature documents were reread from `2642a5f` on 2026-10-28, without loading additional skills or delegating review. The eight earlier findings remain addressed, including the corrected premises concerning index writers, benchmarks and transaction terminology. The following additional contract gaps were found and resolved before declaring the plan implementation-ready:

| Gap | Evidence and correction |
| --- | --- |
| Legacy migration could inherit schema-10 DDL | [migrateSchema8](../../../../internal/runstore/migrate.go) currently derives its target from shared fresh-store `runTableSchema`. Freeze schema-9 DDL/columns and stage-version checks so its old INSERT cannot inherit the new required phase column. Task 2 now verifies both real migration stages, interruption and concurrent completion. |
| Quota escape could require the migration it must fund | The existing migration checks the stored policy limit before publishing its new schema. Define offline quota growth on the authenticated old/current schema before copy/capacity checks, with explicit reporting when that policy commit survives later migration failure. |
| Stateless plans lacked complete identity/determinism rules | Define one canonical builder, deterministic inferred-subject keys, physical owner/database/lock binding, excluded presentation values, repeated-page validation, empty-plan behavior and separate receipt-by-plan lookup. Moved/copied targets with unchanged UUIDs require a new plan; no random ID or preview time can make an unchanged plan stale. A different normalized attestation on an already-applied plan conflicts instead of silently changing audit meaning. |
| Inspection and audit lifecycle could give stale or overstated views | Define recovery-epoch increments for incident/marker changes, apply, batch closure and audit GC; distinguish stable incident membership from per-page live samples and legacy observations. Read-only inspection does not predict admission or infer liveness. Retained/pruning receipts remain inspectable without recreating removed details. |
| Mixed batches and incident-only rows lacked transition coverage | [recoverRun](../../../../internal/runstore/recovery.go) finalizes safe siblings later, and [syncBatchChild](../../../../internal/runstore/batch.go) currently closes only execution-terminal batches. Require administrative closure on these later transitions too. Protect attributed unresolved run evidence even if terminal proof arrives, and resolve that incident without fabricating a run disposition or renewing its original TTL. Persist a disposition payload-eviction marker with final run deletion so a missing nonterminal batch child is not excused by a receipt alone. |
| Shared diagnostics/rendering could re-enter protected work | Admission holds a transaction in the store's one-connection pool; [Budget](../../../../internal/execution/response.go) may call builders repeatedly. Specify same-transaction/context hint queries and pure response rendering after each administrative operation executes once, avoiding nested-inspector deadlocks or repeated mutations. |

Each correction is carried into the contracts and the responsible implementation/task steps, including regression conditions. No remaining design blocker was identified in this in-session check. This is an author readiness assessment, not a fresh independent review or evidence that runtime behavior passes; implementation and the planned native/fault-injection checks remain pending.
