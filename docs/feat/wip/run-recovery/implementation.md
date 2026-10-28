# Run recovery implementation plan

> Status: pending implementation; fresh-read design readiness check completed
> Design: [design.md](design.md)
> Contract: [contracts.md](contracts.md)
> Execution checklist: [tasks.md](tasks.md)

## Starting context

The baseline is `591bf33`, schema 9, public envelopes 2 and helper protocol 2. The issue occurred on Linux with schema 8/version 0.18.1; its original run database is unavailable here. Existing archive transcripts and the [investigation](investigation.md) explain the observed symptoms but do not prove the initiating storage failure.

Execution owners and private helpers retain permanent file leases. `runstore.Open` and ordinary query attachment can reconcile/maintain history; `Store.Close` can do the same. Do not reuse those paths for administrative inspection or recovery. Start from the read-only `Inspect` and exclusive migration patterns instead. An owner generation, a run UUID and the database UUID are different identities.

`runs.cleanup_scope/cleanup` are authoritative. `result` is a historical response blob; batch children retain their own proof/result copy. Keep execution `terminal` and the scoped cleanup predicates unchanged. Add one distinct operational-closure rule and independent audit state. Look for all consumers, not only admission: owner reuse, live service latches, query annotation, lazy indexing, manifests and retention use related metadata.

Do not publish recovery apply before Tasks 8–13 establish every consumer. Intermediate commits may expose read-only inspection, upgrade and planning, but must not register an executable apply branch. The transactional package API in Task 8 is an internal contract-first boundary exercised with real encrypted fixtures until public activation in Task 14.

## Files and responsibilities

| Area | Existing sources and responsibilities |
| --- | --- |
| Storage attachment and schema | `internal/runstore/store.go`, `migrate.go`, `migrate_proof.go`, `policy.go`; path/authentication, leases, schema and policy identity |
| Admission and recovery | `owners.go`, `recovery.go`, `runs.go`, `claims.go`; registry serialization, generation publication, quarantine and indexing claims |
| Run/query state | `types.go`, `result_json.go`, `list.go`, `read.go`, `read_projection.go`; original results, cursor and projection semantics |
| Batch history | `batch.go`, `batch_snapshot.go`, `batch_list.go`, `batch_search.go`; retained child copies and aggregation |
| Retention/accounting | `retention.go`, `search_accounting.go`; bounded tombstones, quota charges and index debit |
| Runtime attribution | `internal/execution/service.go`, `stored_run.go`, `handoff.go`, `batch.go`; stage/cause, in-memory latch, late worker completion |
| Public transport | `internal/server/tool_runs.go`, `tools.go`, execution response/batch/search budgeting, `cmd/capy/runs*.go`; validation, formatting and bounded envelopes |
| Hook observation boundary | `internal/hook/posttooluse.go`, `run_observations_test.go`; reject recovery-bearing envelopes without extending execute/fetch observation to run queries |
| Shared diagnostics and docs | `internal/platform/doctor_runs.go`, `internal/server/tool_doctor.go` (`runChecks`), `tool_stats.go`, `docs/reference/runs.md`, `docs/architecture.md`, root `AGENTS.md`; shared doctor/stats snapshots, operator workflow and authoritative invariants |

Proposed new files below are responsibility boundaries, not a demand to scatter trivial helpers. Keep coherent implementations together and avoid new dependencies. Shared predicates belong in runstore; CLI/MCP must not reimplement their SQL or lifecycle decisions.

## Task 1: Inspect blockers without changing history

Create `internal/runstore/inspection.go` for shared authenticated read-only attachment/snapshots and `incident_view.go` for bounded legacy/current subject views. Refactor `diagnostics.go` to consume these without ordinary store opening/closing. Add `cmd/capy/runs_inspect.go`; registration in `runs.go` is mechanical. Update shared doctor formatting, preserving the existing `runChecks` consumer path from both `internal/server/tool_doctor.go` and `tool_stats.go` rather than adding duplicate snapshot logic.

- Preserve the two-second diagnostic budget, no path/lock creation, no owner claim, and no reconcile/maintenance/policy/index writes → verify: existing `TestInspect*` plus before/after logical store metadata, path inventory and accounting assertions.
- Read schema-8/9 identity and bounded evidence through explicit adapters; label missing attribution and supported upgrade requirements → verify: actual legacy fixtures, wrong key and incompatible schema preserve history.
- Show blocker inventory independently of recent runs, with typed missing evidence and paginated continuations → verify: an older unresolved run behind more than eight successful runs, plus a healthy held-owner pending run that is not mislabeled.
- CLI/doctor return precise next steps and omit command/output/stored error strings → verify: synthetic secrets and unknown persisted state strings do not leak; complete totals and omitted/detail status remain honest.
- Cover the inherited `capy_stats` surface explicitly → verify: its blocker summary matches doctor, uses read-only inspection, creates no owner, and does not change once-only observed/captured/completion totals.
- Distinguish recorded quarantine from unattributed pending observations and define per-page versus epoch-stable membership; implement `--plan` as indexed receipt lookup only → verify: no false live/healthy assertion, known legacy registry changes invalidate cursors, and an unapplied/pruned token is not interpreted as proof of nonapplication or as an applicable plan.

## Task 2: Upgrade offline while preserving evidence

Add `internal/runstore/migrate_v10.go` and `cmd/capy/runs_upgrade.go`. Extend the existing attachment/schema dispatcher and policy helpers with automatic configured-execution migration and the explicit offline alternative. Introduce the schema-10 records/indexes, including disposed-run index state, retaining existing original proof/results and immutable store identity. Establish required phase insertion in `prepareRun`/`insertRun` and batch admission in this task so fresh schema-10 execution works before Task 4 adds later phase updates.

- Support fresh schema 10, automatic configured-execution 9→10/sequential 8→9→10 and the same explicit offline paths; helpers/readers stay non-migrating → verify: first healthy execute upgrades then runs, first quarantined execute upgrades but refuses before admission/spawn, and offline upgrade submits no command. Frozen fixtures preserve IDs, cursors, payload, TTLs, batch proof, accounting and quarantine through migration.
- Freeze the schema-9 destination DDL/columns and stage-version checks used by `migrateSchema8` instead of sharing schema-10 fresh-store DDL → verify: real schema-8 input completes both stages, interrupted schema-9 state resumes, and a concurrent upgrader completing either stage is handled by exact identity/target-version checks.
- Rebuild `runs` with no-default `last_phase NOT NULL` and its enum CHECK, explicitly copying legacy `unknown` and preserving row IDs/indexes/FKs/result bytes → verify: omitted/NULL phase is rejected by SQLite and the store boundary after both fresh creation and migration, invalid enum/new-admission `unknown` is rejected, and admission inserts admitted/queued explicitly. Include full copy/WAL headroom.
- Reuse exclusive authentication/identity rechecks and private bounded migration work, closing handles before leases → verify: live owners/readers/helpers prevent upgrade; wrong keys, cancellation, disk/write failure and process interruption leave a valid old/intermediate/current schema with preserved evidence.
- Permit only explicit configured quota growth through an old/current-schema policy transaction before migration capacity checks, without retention or changing TTL/per-run limits; reload and report policy revision → verify: a quarantined schema-8 store whose migration would exceed its old quota can first gain headroom, decreases/mismatched fields fail without eviction, and later migration failure accurately reports a committed quota increase and valid remaining schema.
- Add a private recovery-record access layer and accounting bounds used by later tasks, but no disposition creation through public commands → verify: old binaries refuse schema 10; current legacy inspection still gives a usable offline command.

## Task 3: Persist reconciliation incident attribution

Implement `internal/runstore/incidents.go` and schema-10 subject queries, integrating `recovery.go` and the shared inspection model. Incident publication shares the same transaction as quarantine markers. Enumerate allowed reason/stage/attribution values centrally.

- Deduplicate unresolved episodes by historical subject/generation and increment revision only for meaningful evidence changes → verify: repeated reconciliation does not multiply incidents or move first-detected times, while a later store/owner incident cannot inherit an old receipt.
- Make the monotonic recovery epoch a shared transaction helper used by incident/marker changes and later apply, batch-closure and audit-GC paths → verify: no wraparound or read-induced increments, and membership-changing commits invalidate affected inspection continuations.
- Preserve known run/owner/batch identity, valid last durable phase and missing proof; represent a structurally valid legacy global marker without a known run → verify: fresh and legacy incident views distinguish recorded, inferred and unattributed subjects.
- Diagnose generation/proof/manifest inconsistencies as unsupported recovery causes → verify: no confirmation-ready plan can be derived by treating inconsistency as ordinary pending cleanup.
- Preserve quarantine if attribution fails and expose incomplete detail honestly → verify: injected transaction failure neither authorizes work nor fabricates persisted attribution.

## Task 4: Attribute live-owner failures and durable phases

Pass admitted identity and typed cause/stage from `internal/execution/service.go` into incident publication. Use the existing pending/start writes in `internal/runstore/runs.go` and `internal/execution/stored_run.go` to persist authorized/compile/run phases. Keep the helper protocol and executor cleanup algorithms unchanged.

- Attribute startup uncertainty, helper loss and unpersisted terminal outcomes at the known detection stage → verify: existing real-helper failure fixtures gain useful incident identity without weakening pending proof.
- Preserve the in-memory `Service.unverified` latch when writes fail, with bounded volatile context for subsequent refusal messages → verify: same-owner refusal remains safe without claiming that failed attribution was durable.
- Preserve handed-off completion semantics and once-only byte accounting → verify: yielded runs and batch children publish neither fabricated terminal success nor duplicate observed/captured totals.

## Task 5: Expose the shared inspection view over MCP

Add the `inspect` action schema in `internal/server/tools.go` and dispatch in `tool_runs.go`. Use a dedicated read-only service adapter rather than `execution.Service.attached`. Share formatting/validation with the CLI wherever that keeps the contract coherent.

- Implement the exact selectors and pagination from the contract, including receipt-only `--receipt-id`/`receipt_id`; reject apply/basis/confirmation arguments → verify: incident UUIDs are not accepted as receipt lookups, and MCP cannot mutate recovery state through this action.
- Return incident/disposition details, legacy upgrade guidance and total/omission semantics within the configured envelope budget → verify: CLI/MCP/doctor agree on the same fixture, including stale continuation and unavailable-detail cases.

## Task 6: Carry actionable recovery hints through admission errors

Add a bounded structured hint to typed runstore errors/results. Centralize construction from the shared incident view. Integrate store-wide and owner-local admission refusals, then preserve the hint through execution rejection, MCP and CLI formatting. Most admission call sites should delegate to the shared helper; avoid duplicating subject-selection queries.

- Cover standalone, file and batch rejection before spawn, including the second quarantine check inside admission transactions → verify: no new run ID or process appears and the known blocker/next step survives.
- Preserve essential codes, no-admission statement and next inspection action at the 2-KiB minimum; shorten only optional subjects/explanations → verify: serialized complete-envelope budgets and synthetic secret/path cases.
- Distinguish cleanup, live leases, credentials, schema, stale plan and inconsistent evidence → verify: actionable repair text does not default to discard, native bypass or a misleading execution retry.
- Build transactional admission hints through the existing transaction/query interface or captured incident context, never a nested inspector/opener or the occupied one-connection pool → verify: refusal under an active transaction/registry lock returns promptly, sees its relevant state and retains the next action when optional detail fails.

## Task 7: Produce a complete offline recovery plan

Implement `internal/runstore/recovery_plan.go` with the dedicated administrative lock/attachment helper and add the preview-only branch in `cmd/capy/runs_recover.go`. No apply branch is registered yet.

- Acquire lifecycle/recovery/owner-slot locks without shared-lock conversion or generation publication; compute a read-only validated inventory → verify: a live holder refuses plan readiness, while inspection still works.
- Stream the complete canonical inventory and bind the token to store/schema/policy/recovery revision/evidence; expose pages without presenting a partial token as applicable → verify: changes to subjects, prefixes, proofs, registry identity or policy invalidate the plan.
- Include physical target/lock binding, deterministic inferred-subject keys and canonical ordering while excluding timestamps/estimated expiry/formatting from the fingerprint → verify: unchanged repeated previews/pages produce the same token; a copied/moved/replaced store with the same UUID is stale; different limits/timeouts do not change plan identity.
- Classify pending-disposition, incident-only terminal, proven-safe reconciliation and unsupported subjects explicitly; define empty-plan behavior and page through `recover --cursor` → verify: no spurious dispositions/receipts or TTL renewal, no fabricated candidate UUID/timestamp, and no reliance on two-second receipt inspection to validate a large unapplied plan.
- Report planned retention/audit consequences, unsupported blockers and capacity requirements → verify: complete/no-blocker, legacy-unattributed, inconsistent, oversized inventory and timed-out scans produce the correct next action without mutation.
- Implement five-minute default planning and the positive finite `--timeout` override, propagating it without nested short caps → verify: controlled slow inventory succeeds with sufficient budget, shorter deadlines yield no token, and the reported longer-timeout command completes the same full scan. Use controlled latency/short test budgets, not multi-minute sleeps.

## Task 8: Commit a standalone administrative resolution atomically

Implement the private transactional API in `internal/runstore/recovery_apply.go` and the shared operational predicate/audit bindings in `resolution.go`. Integrate owner reuse/reconciliation via that predicate. Public apply stays unavailable until Task 14.

- Authenticate, validate exact plan, reserve/transfer bounded metadata and insert receipt/memberships/run dispositions through `sqliteutil.BeginImmediateContext(ctx, db, "runs")` → verify: a real encrypted standalone fixture moves from blocked to fresh-owner admission while original proof/result/payload stay unchanged.
- Store the byte-exact original result snapshot/digest and seed separate `run_search_state` during disposition → verify: validation remains bound to original identity/proof/result bytes and later indexing needs no historical blob rewrite.
- Apply the same five-minute default/positive `--timeout` policy to revalidation and the complete transaction → verify: a long valid plan has a usable long apply path, shorter caller deadlines roll back before commit, and uncertain commit still uses receipt lookup.
- Reject unsupported mismatches and protect execution/index mutation of disposed historical rows; clear only derived safe registry/global markers → verify: generation reuse preserves old incident meaning and an unrelated unresolved blocker prevents partial success.
- Make exact retained-plan retries idempotent and distinguish uncertain commit outcome → verify: duplicate apply keeps receipt/time/deadlines/charges fixed; failure before/around commit leaves either the complete committed receipt or the original block, never partial dispositions.
- Reject a changed normalized basis/explanation for an already-applied plan with the retained receipt ID → verify: retries with different output options/timeouts remain idempotent, but cannot silently replace or claim a different operator attestation.
- Return current blocker state alongside an old idempotent receipt → verify: a later incident cannot be hidden by replaying an earlier successful plan.
- Increment recovery epoch in the application transaction and return an existing pruning receipt by immutable summary without reconstructing removed members → verify: inspection cursors stale after apply and partial audit GC cannot cause duplicate recovery or an artificial metadata inconsistency.

## Task 9: Show standalone recovery without rewriting outcomes

Extend shared result/summary projection and `runs.go`/`list.go` with additive essential recovery metadata, historical language and effective retention. Keep the private stored-result decoder and execution-terminal validation authoritative.

- Distinguish last-recorded state, operator closure and current admission effect; retain unknown exit/completion/persistence values → verify: disposed `running` evidence is not reported as live work or successful cleanup.
- Resolve historical warning attribution from incident/disposition records after owner-slot reuse → verify: original generation details survive replacement of the registry entry.
- Keep the original result snapshot immutable, including when derived views are shortened → verify: status/list and minimum-budget envelopes carry the same original evidence and disposition ID.
- Add the defensive `recovery`-field rejection in `internal/hook/posttooluse.go`, including the structured-envelope marker list, and extend `run_observations_test.go` → verify: root/child v1/v2 envelopes across both cleanup scopes reject object/null/malformed recovery fields; marker-only objects cannot fall back to legacy success; healthy controls/fetch behavior remain unchanged and status/read/inspect never record observations.

## Task 10: Preserve mixed batch history and administrative closure

Update `batch_snapshot.go`, `batch.go` and `batch_list.go` to distinguish execution-terminal children from administratively disposed children. Add separate batch operational metadata while preserving original result/proof/terminal copies.

- Compute closure and recovered counts without manufacturing batch completion or scoped cleanup → verify: terminal, disposed, pending, queued and never-spawned combinations retain accurate historical results and do not overstate current concurrency.
- Permit an evicted nonterminal child only with an exact surviving disposition snapshot and durable `payload_state=evicted`; reject other missing/mismatched children → verify: retained manifests remain meaningful after legitimate child deletion, while an absent retained/evicting row is not excused merely because a receipt exists.
- Snapshot proof, disposition and dependencies consistently → verify: concurrent reads cannot see a half-updated administrative interpretation.
- Integrate the same administrative-closure calculation into `recoverRun`/`syncBatchChild` and normal child-terminal transitions, not only apply → verify: a disposed child plus a safe queued sibling closes exactly once when that sibling is reconciled after restart, without setting original batch completion or renewing administrative expiry on repeat.

## Task 11: Retain and evict disposed run output safely

Extend `retention.go` and recovery accounting helpers with separate administrative expiry/ordering. Integrate effective availability in the shared run view; retain existing raw/projection/index deletion budgets.

- Fix a disposed run's output deadline once from recovery time and admission TTL; leave unrelated completion TTLs unchanged → verify: retries/reads/policy changes cannot renew either clock.
- Protect attributed unresolved-incident run evidence from expiry/quota deletion even when later terminal proof arrives; expose that protected evidence without modifying expiry → verify: planning can resolve the incident only, and original expiry resumes afterward without a run disposition or new payload window.
- Allow subsequent TTL/quota eviction of disposed payload without accepting its cleanup as execution proof; atomically mark `evicting` with the run tombstone and `evicted` with final parent deletion → verify: output bytes/FTS rows disappear in bounded work, full debit remains until completion, and crashes never leave ambiguous legitimate-vs-missing child evidence.
- Transfer independently retained audit charges before parent deletion and preserve dependencies → verify: exact no-double-charge/no-free-audit behavior under dbstat and conservative accounting, including cancellation mid-eviction.
- Refuse apply/upgrade capacity shortages without invoking retention → verify: unrelated completed output cannot be sacrificed to make an administrative transaction succeed.

## Task 12: Retain batch dispositions and prune audit records

Extend batch retention in `batch.go` and add bounded audit GC in `recovery_retention.go`, invoked by ordinary maintenance only. Use indexed references rather than repeatedly scanning complete histories.

- Set separate batch expiry only when all children become operationally closed; preserve terminal-batch original expiry → verify: mixed batches can age out without rewriting cleanup and cannot expire while an unresolved child remains.
- Keep receipts/incidents/dispositions through their deadlines and all retained run/manifest references → verify: output-first, manifest-first and policy/pressure permutations never drop needed attribution or re-quarantine old evidence.
- Prune receipt groups in bounded dependency order, keeping plan identity until the root receipt is deleted, and decrement charges exactly once → verify: interrupted cleanup resumes safely; partial/expired/pruned receipts cannot recreate old dispositions or extend retention.
- Increment recovery epoch on group marking/deletion and distinguish pruning summaries from complete detail views → verify: paginated inspection detects membership changes and exact plan retries never validate or recreate members already pruned.

## Task 13: Recover safe output and search after disposition

Update raw/projection read subjects and lazy search eligibility/filtering, including `claims.go`, `search.go`, `search_index.go` and `batch_search.go`. Reuse existing ranking/chunking algorithms. Disposed-run indexing uses existing per-stream progress plus the separate aggregate `run_search_state`; `runs.result` stays byte-immutable.

- Preserve all available cursor coordinates and existing EOF meaning; expose administrative `capture_closed` separately → verify: readers stop polling at the retained prefix without fabricated terminal evidence.
- Never flush unresolved sanitizer state during apply; index only existing safe coverage → verify: split-secret/binary/UTF-8 tails remain protected; explicit raw base64 remains independent.
- Permit a fresh healthy indexing owner using its current lease/generation and matching work claim, routing claim-state, save/failure and abandoned-claim updates to separate index metadata for disposed runs → verify: first/resumed searches, quota/errors, worker-loss recovery and execution-owner slot reuse all preserve exact `runs.result` bytes and its snapshot digest. Non-disposed indexing keeps its current contract.
- Overlay mutable index state only in returned views and account/delete it with payload → verify: status/list/read/search agree on current index status, a missing side-state row fails explicitly, and no mutation is smuggled through `Store.update`/`Finish` for a dead execution generation.
- Apply identical effective expiry/output-availability rules to batch coverage and late search rechecks → verify: payload eviction is reported as unavailable, not a false authoritative zero-match result.

## Task 14: Expose explicit CLI apply and receipt recovery

Enable the apply branch in `cmd/capy/runs_recover.go` only after Tasks 8–13. Add required-argument validation, bounded JSON/text responses and receipt lookup to the inspection route.

- Require `--apply`, exact token, `--confirm-stopped`, allowed cleanup basis and bounded reason; validate optional `--timeout` for planning/apply and use a dedicated administrative attachment → verify: omission/invalid combinations, zero/negative/malformed/overflowing timeouts fail before writes and no MCP apply route exists.
- Present current admission status, receipt, unchanged original uncertainty, retention consequences and restart/verification steps → verify: a complete offline CLI flow preserves sibling output and allows a new captured command after restart.
- Handle lost-response/uncertain-commit receipt lookup and idempotent retries → verify: the original application command is never resubmitted and a later blocker is still surfaced.
- Name receipt lookup consistently as `--receipt-id`/`receipt_id` and `recovery.receipt_id` → verify: a receipt resolves, an incident UUID returns `receipt_unavailable`, and unsupported `--recovery-id` is rejected.
- Ensure source/build staging cannot expose partial recovery support → verify: only the fully integrated binary advertises apply in help/tool guidance.
- Execute each administrative operation once before response budgeting; render only the captured result → verify: budget retries and output-delivery failures cannot rerun upgrade, planning or apply, and committed outcomes remain discoverable by receipt/plan lookup.

## Task 15: Publish the operator workflow and revised invariants

Update `docs/reference/runs.md`, `docs/architecture.md`, root `AGENTS.md` and CLI/tool descriptions. Include the actual syntax/errors implemented by the previous tasks. Preserve the conditional future-verification rationale and its legacy-policy requirement in the feature documents.

- Document inspect → stop → upgrade if needed → plan/apply → restart → verify, explicitly distinguishing terminal commands from captured execution → verify: a newcomer can follow the isolated CLI fixture without database edits, guessed PID kills or disabling capture.
- Include old schema, wrong credential, held lease, stale plan, unknown cleanup, capacity and uncertain commit examples → verify: every documented next action exists and matches its error's meaning.
- State normal post-recovery TTL/quota limitations and how to retrieve retained logs/audit records → verify: examples do not promise indefinite history or resurrect evicted output.
- Replace the current execution-only migration invariant in root `AGENTS.md` when implementation lands: normal authenticated configured execution automatically migrates supported schema 8/9 stores under exclusive lifecycle ownership before admission; explicit `capy runs upgrade` uses the same migrations without submitting a command; readers/helpers/doctor/stats never migrate; neither path clears quarantine. Document that a refused execute may already have upgraded the schema and that only the offline path excludes ordinary attachment maintenance → verify: guidance matches Task 2's healthy, quarantined and busy cases.
- Document slow-store recovery using `--timeout` on both plan and apply, separately from bounded interactive inspection → verify: timeout refusals give an executable longer-budget command rather than only suggesting reinspection or discard.
- Update generated routing/help guidance only where implementation needs it; any such change must update `internal/platform/routing.go`/setup generator and the corresponding committed artifacts together → verify: whole-file artifact and idempotence guards if those artifacts change.

## Task 16: Final verification and release evidence

Invoke the required implementation skills: `kk:test`, `kk:document`, `kk:review-code` with Go, and `kk:review-spec`. Record commands, revision, platforms, captured run IDs, results and limitations in a feature verification record. Do not mark unavailable native execution as a pass.

Run the explicit evidence matrix below. Fix material failures and repeat the affected checks; broader reruns need a concrete changed risk, shared behavior change or repository/release requirement. Carry any accepted unresolved issue into durable tasks/docs. Archive feature documents only after implementation and acceptance are actually complete.

## Verification and release evidence

All builds/tests use FTS5 and both synthetic environment keys. Route routine execution through Capy capture; recover logs by run ID rather than rerunning commands for output. Tests use isolated stores and existing fixture/helper patterns, never the incident database or real credentials.

| Outcome | Sufficient evidence |
| --- | --- |
| Useful read-only diagnostics | Existing `TestInspect*` plus old-blocker/healthy-live-control, legacy schema, secret-free output and strict no-maintenance tests; CLI/MCP/doctor/stats fixtures agree and stats inspection changes no run observed/captured/completion counters, while ordinary response accounting still occurs. |
| Safe upgrade entry points | Actual schema-8/9 fixtures and a pinned schema-9 intermediate; automatic first-execute upgrade for healthy/quarantined stores; explicit no-submission upgrade; pre-migration quota increase on old schemas; exact preserved IDs/cursors/payload/proof, required no-default phase, exclusive-lock contention, bounded capacity and interruption before/between/after policy/migration commits. |
| Safe administration | Real encrypted fixtures for flags/timeouts, deterministic complete snapshots and physical target binding, extended-budget planning/apply, empty/incident-only cases, changed identities/proofs/prefixes/policy, injected write/commit/delivery failures, exactly-once operation execution, idempotency/pruning receipts and later new incidents. |
| Cross-session recovery | Native helper-loss or failed-finalization fixture plus a second owner, successful siblings and older-blocker inventory; safe refusal, explicit recovery and fresh admission after restart. No historical command replay. |
| Accurate history | Byte-immutable original result snapshots/digests across disposed-run indexing and claim recovery; raw byte comparisons, cursor reuse, unresolved-incident evidence protection, mixed manifests with delayed safe-sibling closure and slot reuse; no original execution-terminal inflation. |
| Hook observation boundary | Defensive recovery-field rejection in root/child v1/v2 envelope fixtures with both cleanup scopes; query-tool exclusion, malformed-field/legacy-fallback negatives, healthy execute/fetch controls. |
| Bounded lifecycle/storage | Existing leases/recovery/retention budget tests plus audit transfer/dependency GC, both accounting modes, projection tails and partial search coverage. |
| Operator usability | Actual binary text/JSON help and refusal cases; executable next commands, complete shutdown/restart instructions, 2-KiB minimum-response coverage, no automatic discard/bypass recommendation. |
| Shared regressions | Affected runstore/execution/server/platform/hook/CLI FTS5 race tests, repository `make test-race`, `make vet` and `make build`; existing public capture/batch/handoff/observation tests. |
| Native release behavior | `make test-integration-race` on Linux and supported macOS 26 arm64/CGO, plus an actual-binary offline recovery smoke on each. Do not substitute a host's storage-only tests for the other platform's lifecycle. |
| Search/index behavior | Apply `AGENTS.md`'s final-diff trigger: any search, indexing, chunking or executor code change requires `make bench-quality` and baseline/candidate comparison. Task 13's planned run-store search/index changes trigger it; do not narrow the rule to selected packages. The target exercises store/server/vault fixtures, not only knowledge retrieval. Use focused run-store tests for disposition eligibility, coverage and immutability; do not claim the broad benchmark proves those contracts. Add performance checks only for changed paths or an observed concern. |

A single failed critical safety/transaction assertion blocks completion. A missing platform or unavailable test tool limits the evidence and leaves its required task open. Optional formatting preferences or unmeasured performance speculation are not separate release gates. The original incident's exact trigger and future machine-verifiable recovery are not prerequisites for this feature's acceptance.
