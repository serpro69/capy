# Tasks: Run recovery that preserves history

> Design: [design.md](design.md)
> Contracts: [contracts.md](contracts.md)
> Implementation: [implementation.md](implementation.md)
> Status: in-progress
> Created: 2026-10-28
> Not Doing: automatic recovery, MCP apply, execution replay/adoption, changed cleanup guarantees, run rekey, archive management, permanent archives, generic database repair, finalization retries

The user approved all four design presentation sections, same-store recovery and operator responsibility. Machine-verifiable recovery is a documented conditional future option, not a v1 gate. Supplied external review findings have been corroborated and incorporated. A subsequent author fresh-read check of all five documents closed the additional gaps recorded in [investigation.md](investigation.md#fresh-read-implementation-readiness); no known design blocker remains. This is an implementation-readiness assessment, not independent reviewer approval or runtime evidence. Task 1 execution and verification are recorded below; later tasks remain pending.

Task sizes count substantive responsibilities, excluding mechanical registration/call-site wiring and fixtures. Each task is a complete testable path or an explicitly identified contract-first slice. Public apply remains unavailable until Task 14; private disposition fixtures do not authorize shipping partial recovery support.

## Task 1: Inspect blockers without changing history

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [inspection plan](implementation.md#task-1-inspect-blockers-without-changing-history), [inspection contract](contracts.md#read-only-inspection)

### Subtasks

- [x] 1.1 Add `internal/runstore/inspection.go` and `incident_view.go`; refactor `diagnostics.go` onto a shared read-only snapshot with bounded legacy adapters → verify: missing store, wrong key and current/old schema inspection create nothing and perform no maintenance.
- [x] 1.2 Add `cmd/capy/runs_inspect.go` and blocker-focused `internal/platform/doctor_runs.go` formatting, retaining `internal/server/tool_doctor.go::runChecks` reuse by `tool_stats.go` → verify: CLI/MCP doctor and `capy_stats` agree on old blockers beyond eight recent runs; stats inspection claims no owner and changes no run observed/captured/completion counters, without suppressing ordinary response accounting.
- [x] 1.3 Add pagination, omission and sensitive-data controls → verify: healthy live pending work is not mislabeled, stale continuations are explicit and command/output/backend secrets stay private.
- [x] 1.4 Define epoch-stable incident/receipt membership versus live pending samples, legacy marker/cutoff continuations and receipt-only `inspect --plan` lookup → verify: no false live/healthy assertion, no invented legacy epoch, no hidden retained audit solely due to expiry, and absent receipts do not prove nonapplication or validate an unapplied plan.

### Execution context

- 2026-10-29 — Observed: started from clean `1bb2cd1d1ada9ed2967bdaa0f8c8556594ea1c85` (schema 9). Scope is Task 1 only: dedicated read-only inspection and shared CLI/doctor/stats diagnostics. Source authority: the linked Task 1 plan and inspection contract. Preserve original rows/results, policy, leases and completion accounting; never attach an execution service for inspection.
- 2026-10-29 — Observed: current `Inspect` rejects schema 8 and only samples eight recent runs; doctor recommends discard for quarantine. Implement bounded schema-8/9 adapters and explicit legacy marker/cutoff continuations. Schema-10 epoch/audit membership and retained receipts depend on Tasks 2/3; this slice defines their distinction and receipt-only selector semantics without exposing application or inventing legacy audit state.
- 2026-10-29 — Verification plan: real encrypted legacy/current fixtures, old blocker and held-owner controls, logical metadata/path preservation, cursor invalidation, sensitive-field exclusion, minimum response budgets and CLI/MCP doctor/stats parity. Use `kk:test` Go guidance and captured FTS5 tests, including focused race checks. Native recovery/application release evidence belongs to later tasks.
- 2026-10-29 — Explicitly accepted: user selected “Exclusive, nonmutating fallback with the documented checks” after the closed-store fixture demonstrated that SQLite `mode=ro` creates WAL/SHM files. The inspection contract now permits a fresh nonblocking exclusive lifecycle lease for immutable reads only after rechecking WAL absence and rejecting a nonempty rollback journal. The shared path additionally uses a dedicated read-only SQLite VFS to deny WAL creation/deletion and `readonly_shm=1` for SHM; ordinary SQLite connections are unchanged. API behavior was checked against pinned bundled SQLite 3.51.2 and its public VFS/URI documentation.
- 2026-10-29 — Observed verification on Darwin arm64 26.6.2, CGO, base `1bb2cd1` plus this working tree; all commands used `CAPY_DB_KEY=test-key-for-development CAPY_VAULT_KEY=test-key` and captured execution:
  - `go test -tags fts5 -count=1 ./internal/runstore -run '^TestInspect'` passed (`b564d62d-1e56-4f41-9c78-b890861cf7c1`). The expanded fixtures then exposed sidecar creation (`e9aafb4b-b97e-494a-84f5-fb8385f8ddfc`). An initial VFS prototype bypassed the encryption wrapper and failed authentication (`9b4f21f9-d489-418a-b2d7-fac3596c4502`); it was corrected to delegate through the existing encrypted VFS. All inspection tests then passed (`2ff564d1-4539-43fb-8118-e91e5792680e`). These failed attempts are retained as evidence, not counted as passes.
  - `go test -race -tags fts5 -count=1 ./internal/runstore ./internal/platform ./internal/server ./cmd/capy -run 'TestInspect|TestCheckRunsSchema|TestRunCapability|TestRunDiagnostics|TestStatsYieldedRunCompletionOnce|TestDoctorRun|TestRunsInspect'` passed (`94ab0eab-03ac-4d18-aa40-ffc0168c0e56`). Includes actual-binary CLI pagination and CLI/MCP doctor/stats parity, healthy held-owner control and once-only run-byte counters with ordinary response accounting.
  - `go test -race -tags fts5 -count=1 ./internal/runstore -run 'TestInspectionOfflineFallbackGuards|TestInspectionMinimumBudgetProgressAndPathPrivacy|TestInspectionSQLiteGuardCreatesNoSidecars'` passed (`2862de28-0de3-4c7f-b9da-ab4a12f56f04`). Covers exclusive contention, rollback-journal refusal, absent-sidecar race guard, path quoting and minimum-envelope progress.
  - Complete `go test -race -tags fts5 -count=1 ./internal/runstore` passed in 101.944s (`8e9dd5ec-2799-451d-b252-a0f24bd3e229`), including updated legacy migration expectations. `make vet build` passed (`846d84cf-a5a1-4e25-8eef-942a3bebeeaa`). No search/indexing/chunking/executor code changed, so the quality-benchmark trigger does not apply.
- 2026-10-29 — Evidence limits: Task 1 was verified on macOS only. Task 16 (owner: unassigned) retains Linux native/release verification and full-feature acceptance; run the inspection/VFS fixtures on Linux alongside that matrix. Schema-10 retained/pruning receipt enumeration and epoch invalidation require Tasks 2/3 and cannot be exercised against the schema-8/9 implementation; their contract is recorded without a fabricated legacy epoch. CI/publishing configuration is unchanged.
- 2026-10-29 — Review corrections: human output now distinguishes owner slot/generation and prints missing evidence/output availability; actual-binary race tests passed (`5a92a33c-2a83-4384-bc67-eb96399dd392`). Admitted/queued `not_spawned` rows and matching accepted-cleanup siblings remain unattributed, with inferred counts restricted to pending cleanup or mismatched generations in marked slots. Executable `inspect_command` is separate from prose, healthy current-schema stores do not get stop-sessions advice, bidi path controls are sanitized, attachment races get retry guidance, and malformed metadata gives explicit `recovery_inconsistent`/incomplete diagnostics. The user-approved exclusion of concurrent attachments is now explicit in the contract/reference; ordinary admission semantics remain unchanged.
- 2026-10-29 — Post-review verification: platform/server/CLI focused FTS5 race tests passed in `f373ab32-6fc3-44a4-8bd2-7e90b06a52e9`; its runstore tests failed compilation on the new fixture's event-sequence integer type. After correcting that fixture, `go test -race -tags fts5 -count=1 ./internal/runstore -run 'TestInspect|TestMigrationOnlyConfiguredExecutionMayUpgrade'` passed in 13.815s (`85985a3f-4fd7-4c70-90ec-fb6af6d7475b`). This includes inspection in another process during WAL appends, healthy admitted/queued controls, accepted exclusive-lease contention and subsequent reattachment, and the prior inspection boundaries. The failed combined attempt is not counted as a full pass.
- 2026-10-29 — Final verification: complete `go test -race -tags fts5 -count=1 ./internal/runstore` passed in 115.644s (`096f1ca3-8a78-484a-97db-7d8bda9cff02`). Final `TestCheckRuns*` platform race tests plus `make vet build` passed (`f96fdf12-078a-40b5-8f0b-6d95c59c3587`); an intervening rebuild also passed (`20e1ac87-9027-438a-925c-b372d126d40c`). Doctor preserves known quarantine as Fail even when detail metadata is damaged, without exposing the malformed payload. `git diff --check` passed.
- 2026-10-29 — Isolated review completed under `kk:review-code`: internal code-reviewer (`gpt-6.1-sol`) approved the initial scope and subsequent corrections; external PAL clink `claude` (observed `claude-opus-5-5`, Anthropic) approved Task 1 after corrections and final test evidence. External backend was clink because no API models were configured; Codex/OpenAI excluded as authoring provider, agy unavailable, gemini retired. Both reviews were source-only and tests were attributed author evidence. Internal review covered 19 files plus focused corrections and requested pinned SQLite excerpts. External first pass omitted several full-file reads, and its verbose initial criteria receipt was not retained in the parent; follow-up Read traces verify corrected source coverage, not an independently executed test suite. No broad production/Linux assurance or finding corroboration is claimed.
- 2026-10-29 — Review findings resolved: owner identity missing from text; healthy queued/safe-sibling mislabeling; mixed shell/prose guidance; attachment-race and malformed-metadata advice; loss of known quarantine severity. The external contention concern was withdrawn after the explicit prior user approval and documented retry behavior were supplied. Cosmetic duplicate incomplete-detail wording was removed. No systemic P0/P1 findings to index; new implementation/testing conventions are documented here and in the operator/architecture references, so no duplicate knowledge note was indexed. Task 1 is complete; Tasks 2–16 and their acceptance gates remain pending.
- 2026-10-29 — Final wording check and candidate rebuild passed: `TestCheckRunsDamagedBlockerPreservesQuarantine` and `make build`, captured as `106af0d8-b8bf-49a1-bd69-270fb4b5a81c`. Optional documentation polish can use `kk:clarify-docs` on `docs/reference/runs.md`; it is not an acceptance prerequisite.

## Task 2: Upgrade offline while preserving evidence

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [upgrade plan](implementation.md#task-2-upgrade-offline-while-preserving-evidence), [persistent model](contracts.md#persistent-model), [compatibility](contracts.md#upgrade-and-compatibility)

### Subtasks

- [ ] 2.1 Add `internal/runstore/migrate_v10.go` and schema-10 records/indexes including separate disposed-run index state; rebuild `runs` with no-default `last_phase NOT NULL`/enum constraint and explicit legacy `unknown`, and wire admitted/queued insertion → verify: fresh/migrated stores reject omitted/NULL/invalid phase at the database/store boundary; new admission rejects `unknown`; frozen schema-8/9 fixtures preserve row IDs, indexes/FKs, cursors, payload/result bytes, TTLs and batch evidence with accounted copy/WAL headroom.
- [ ] 2.2 Add `cmd/capy/runs_upgrade.go` with dedicated exclusive authenticated attachment and no execution/maintenance → verify: live leases, wrong keys, cancellation and interrupted migrations preserve valid history and quarantine.
- [ ] 2.3 Commit validated configured quota growth against the authenticated schema 8/9/10 before migration capacity checks/copying, then reload policy → verify: quarantine and old-schema capacity cannot prevent the increase needed to migrate; decreases/TTL/per-run changes fail; later migration failure reports a retained quota increase and valid remaining schema without eviction.
- [ ] 2.4 Make configured execution attachment automatically attempt the same supported 8→9→10/9→10 migrations before admission; keep readers/helpers non-migrating → verify: healthy first execute upgrades then runs; quarantined first execute upgrades but admits/spawns nothing and stays blocked; busy/failed migration refuses; explicit CLI upgrade submits no command.
- [ ] 2.5 Pin the 8→9 destination DDL/columns and concurrent-stage checks to schema 9 instead of the schema-10 fresh-store constant → verify: actual schema-8 input reaches 9 then 10 with preserved data, an interrupted intermediate resumes, and another upgrader completing either supported stage is recognized by target version and store identity.

## Task 3: Persist reconciliation incident attribution

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [incident plan](implementation.md#task-3-persist-reconciliation-incident-attribution), [publication contract](contracts.md#incident-publication-and-fail-closed-fallback)

### Subtasks

- [ ] 3.1 Add `internal/runstore/incidents.go` with deduplicated unresolved episodes, typed causes/stages and revision rules; integrate `recovery.go` quarantine transactions → verify: repeated observation preserves first detection, while later store/owner incidents cannot reuse a resolved episode or receipt.
- [ ] 3.2 Extend inspection to schema-10 attribution and valid legacy store-scoped incidents → verify: recorded/inferred/unattributed facts remain distinct, including absent causal run IDs.
- [ ] 3.3 Keep unsupported identity/proof/manifest mismatches and failed attribution fail-closed → verify: no partial write or unsupported subject becomes confirmation-ready.
- [ ] 3.4 Add the monotonic recovery-epoch transaction helper for incidents/markers and later apply, batch closure and audit GC → verify: unchanged observations do not increment it, membership changes stale cursors, and exhaustion refuses mutation instead of wrapping.

## Task 4: Attribute live-owner failures and durable phases

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5, Task 7
- **Docs:** [runtime plan](implementation.md#task-4-attribute-live-owner-failures-and-durable-phases), [publication contract](contracts.md#incident-publication-and-fail-closed-fallback)

### Subtasks

- [ ] 4.1 Pass admitted identity/cause/stage from `internal/execution/service.go` to incident publication; retain bounded volatile context if storage fails → verify: same-owner refusal remains authoritative and does not claim failed writes were durable.
- [ ] 4.2 Preserve authorized/compile/run phase in existing writes through `internal/execution/stored_run.go` and `internal/runstore/runs.go` → verify: phase diagnostics remain distinct from detection stage and require no new output-event writes.
- [ ] 4.3 Extend existing helper-loss/finalization/handoff fixtures for attribution → verify: pending proof, helper leases and once-only completion byte accounting remain intact.

## Task 5: Expose inspection over MCP

- **Status:** pending
- **Depends on:** Task 3
- **Size:** S
- **Can run in parallel with:** Task 4, Task 7
- **Docs:** [MCP plan](implementation.md#task-5-expose-the-shared-inspection-view-over-mcp), [inspection contract](contracts.md#read-only-inspection)

### Subtasks

- [ ] 5.1 Register/dispatch the read-only `inspect` action in `internal/server/tools.go` and `tool_runs.go` using the dedicated snapshot service → verify: receipt-only `receipt_id` matches CLI `--receipt-id`, incident UUIDs do not resolve as receipts, and apply/confirmation arguments are rejected.
- [ ] 5.2 Verify CLI/MCP/doctor agreement, legacy guidance, stale cursors and complete serialized budgets → verify: no generation claim, database mutation or hidden incomplete inventory.

## Task 6: Carry recovery guidance through admission errors

- **Status:** pending
- **Depends on:** Task 4, Task 5
- **Size:** M
- **Can run in parallel with:** Task 7
- **Docs:** [guidance plan](implementation.md#task-6-carry-actionable-recovery-hints-through-admission-errors), [refusals](contracts.md#refusals-and-operator-guidance), [query boundary](contracts.md#query-and-response-boundaries)

### Subtasks

- [ ] 6.1 Add shared bounded recovery hints to typed errors and construct them for all store/owner admission gates, including transactional rechecks → verify: execute/file/batch refusal has no run ID or spawned process and identifies the correct incident scope.
- [ ] 6.2 Preserve essential hints in execution rejection and CLI/MCP response budgeting → verify: the 2-KiB envelope still states no admission and a usable next inspection step.
- [ ] 6.3 Cover credentials, held leases, schema, stale plans and inconsistent evidence with specific repair actions → verify: no default discard, capture bypass, backend leakage or blind command retry.
- [ ] 6.4 Query hints through the caller's existing transaction/query interface or captured context, never a nested inspection opener or occupied one-connection pool → verify: transactional/registry-locked refusal remains prompt and authoritative even when optional details are unavailable.

## Task 7: Produce complete offline recovery plans

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 4, Task 5, Task 6
- **Docs:** [plan implementation](implementation.md#task-7-produce-a-complete-offline-recovery-plan), [plan contract](contracts.md#recovery-plan)

### Subtasks

- [ ] 7.1 Add administrative lease/attachment helpers and `internal/runstore/recovery_plan.go` → verify: planning is exclusive, nonmutating and generation-free, and live holders get shutdown instructions.
- [ ] 7.2 Add canonical complete-inventory fingerprinting, token validation and paginated review → verify: changed subjects/proofs/prefixes/policy invalidate the snapshot; incomplete scans never produce an applicable token.
- [ ] 7.3 Add preview-only `cmd/capy/runs_recover.go` with retention/capacity/refusal guidance → verify: no apply branch is registered or advertised yet.
- [ ] 7.4 Implement a five-minute default and positive finite `--timeout` override throughout offline inventory → verify: shorter deadlines return no token, controlled slow scans succeed with more time, and refusal supplies a working longer-timeout command without weakening snapshot validation or using multi-minute test sleeps.
- [ ] 7.5 Use one canonical builder with stable inferred-subject keys and physical owner/database/lock binding; exclude display timestamps, estimated deadlines and pagination/budget parameters → verify: unchanged repeated previews/pages share a token, while copied/moved/replaced targets with the same UUID are stale.
- [ ] 7.6 Classify pending-disposition, incident-only terminal, safe-reconciliation and unsupported subjects; define `no_recovery_needed` without token/audit and page unapplied plans through `recover --cursor` → verify: no spurious dispositions, TTL renewal, randomly changing candidate IDs or reliance on receipt lookup for plan validation.

## Task 8: Commit standalone resolution atomically

- **Status:** pending
- **Depends on:** Task 6, Task 7
- **Size:** M
- **Can run in parallel with:** —
- **Slicing:** Contract-First — private transactional administration boundary, kept off the CLI until all consumers are complete
- **Docs:** [transaction implementation](implementation.md#task-8-commit-a-standalone-administrative-resolution-atomically), [apply contract](contracts.md#apply-protocol)

### Subtasks

- [ ] 8.1 Add `internal/runstore/recovery_apply.go` and `resolution.go` with receipt/membership/disposition insertion through `sqliteutil.BeginImmediateContext`, exact plan/identity/capacity checks, byte-exact result snapshot/digest and separate index-state seed → verify: an encrypted fixture preserves original evidence and moves from blocked to fresh-owner admission without permitting later result-blob rewrites.
- [ ] 8.2 Integrate shared operational closure into owner reuse/reconciliation and prevent mutation of administratively closed historical rows → verify: old generations retain attribution and unrelated blockers prevent partial clearing.
- [ ] 8.3 Add commit fault injection and idempotency handling → verify: old or fully committed state only, no renewed dates/charges, current later incidents remain visible, and a different normalized basis/explanation yields receipt conflict rather than silent audit replacement.
- [ ] 8.4 Propagate the five-minute default/positive timeout override through revalidation and transaction without nested shorter caps → verify: extended-budget plans can be applied, earlier caller deadlines roll back before commit, and ambiguous commit uses receipt lookup.
- [ ] 8.5 Increment recovery epoch with committed application and return existing pruning receipts by immutable summary → verify: apply invalidates inspection continuations and removed audit details cannot trigger duplicate recovery or false inconsistency.

## Task 9: Show standalone recovery without rewriting outcomes

- **Status:** pending
- **Depends on:** Task 8
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [standalone views](implementation.md#task-9-show-standalone-recovery-without-rewriting-outcomes), [public results](contracts.md#operational-consumers-and-public-results)

### Subtasks

- [ ] 9.1 Extend shared result/summary views and `internal/runstore/runs.go`/`list.go` with essential disposition/retention metadata → verify: historical running/unknown completion is distinguished from live work without terminal-proof inflation.
- [ ] 9.2 Resolve warnings through immutable incident/disposition subjects across slot reuse → verify: replacing the registry generation neither loses historical attribution nor flags a healthy replacement owner.
- [ ] 9.3 Preserve stored snapshots and bound derived views → verify: raw original execution fields and minimum-envelope disposition identifiers agree across status/list.
- [ ] 9.4 Add defensive `recovery`-presence rejection and structured-marker recognition in `internal/hook/posttooluse.go`; extend `run_observations_test.go` → verify: root/child v1/v2 and both scope cases reject object/null/malformed recovery fields and legacy fallback, status/read/inspect cannot record evidence, and healthy execute/fetch controls still pass.

## Task 10: Preserve mixed batch history

- **Status:** pending
- **Depends on:** Task 9
- **Size:** M
- **Can run in parallel with:** Task 11
- **Docs:** [batch views](implementation.md#task-10-preserve-mixed-batch-history-and-administrative-closure), [public results](contracts.md#operational-consumers-and-public-results)

### Subtasks

- [ ] 10.1 Update `batch_snapshot.go`, `batch.go` and `batch_list.go` for distinct administrative closure and recovered counts → verify: original child proof/terminal copies and batch completion fields remain truthful.
- [ ] 10.2 Require exact retained dispositions plus durable `payload_state=evicted` for missing nonterminal children → verify: valid historical manifests remain interpretable, but missing retained/evicting rows and mismatched evidence still fail.
- [ ] 10.3 Snapshot all relevant evidence consistently and exclude disposed children from current-concurrency presentation → verify: mixed batches and reused owners expose no half-updated interpretation.
- [ ] 10.4 Recompute administrative closure in later `recoverRun`/`syncBatchChild` and ordinary terminal transitions as well as apply → verify: a safe queued sibling finalized after restart closes a mixed disposed batch exactly once, with no original completion fabrication or repeated expiry renewal.

## Task 11: Retain and evict disposed run output

- **Status:** pending
- **Depends on:** Task 9
- **Size:** M
- **Can run in parallel with:** Task 10
- **Docs:** [run retention](implementation.md#task-11-retain-and-evict-disposed-run-output-safely), [accounting contract](contracts.md#retention-and-accounting)

### Subtasks

- [ ] 11.1 Add separate fixed administrative deadlines/order to `retention.go` and effective run availability → verify: retries/reads/policy changes cannot renew clocks or alter unrelated completed expiry.
- [ ] 11.2 Extend bounded tombstone deletion and audit-debit transfer with atomic disposition `evicting`/`evicted` markers → verify: final parent deletion, durable eviction evidence and audit transfer commit together, preserving pending proof without double counting in either accounting mode.
- [ ] 11.3 Exercise interrupted eviction and recovery capacity refusal → verify: partial deletion retains its debit, and apply/upgrade never evicts siblings to succeed.
- [ ] 11.4 Protect still-retained evidence for an attributed unresolved incident even after terminal proof arrives, and expose it without changing expiry → verify: incident-only resolution creates no run disposition, preserves the old TTL and resumes its normal eligibility afterward.

## Task 12: Retain batch dispositions and prune audit

- **Status:** pending
- **Depends on:** Task 10, Task 11
- **Size:** M
- **Can run in parallel with:** Task 13
- **Docs:** [audit retention](implementation.md#task-12-retain-batch-dispositions-and-prune-audit-records), [accounting contract](contracts.md#retention-and-accounting)

### Subtasks

- [ ] 12.1 Extend batch retention for separate operational expiry while preserving original terminal-batch TTL → verify: unresolved children prevent expiry and disposed children never become accepted execution proof.
- [ ] 12.2 Add `recovery_retention.go` with indexed dependency checks and bounded audit pruning in ordinary maintenance → verify: output-first/manifest-first deletion preserves needed receipts and attribution through their deadline.
- [ ] 12.3 Verify exact accounting and restartable receipt-group pruning with the root identity removed last → verify: referenced/partially pruned receipts cannot be reapplied, and a pruned old token cannot recreate a retention window.
- [ ] 12.4 Increment the recovery epoch on group marking/deletion and expose retained pruning summaries separately from removed details → verify: cursors detect membership changes and plan retries neither reconstruct nor recreate pruned members.

## Task 13: Recover safe output and search after disposition

- **Status:** pending
- **Depends on:** Task 10, Task 11
- **Size:** M
- **Can run in parallel with:** Task 12
- **Docs:** [read/search plan](implementation.md#task-13-recover-safe-output-and-search-after-disposition), [capture boundaries](contracts.md#capture-and-projection-boundaries)

### Subtasks

- [ ] 13.1 Extend raw/projection subjects with administrative capture closure while preserving EOF and cursor coordinates → verify: retained-prefix polling ends without fabricated completion or sanitizer flushing.
- [ ] 13.2 Update `claims.go`, `search.go`, `search_index.go` and `batch_search.go` eligibility/coverage using the worker's current lease/generation and matching claim; route disposed aggregate index state to `run_search_state` and keep per-stream progress separate → verify: first/resumed search, quota/errors, abandoned-claim recovery and execution-owner slot reuse leave exact `runs.result` bytes/snapshot digest unchanged; no weaker historical execution-update path is introduced.
- [ ] 13.3 Cover split-secret, binary/UTF-8 tails, expiry races and missing child output → verify: raw remains explicit base64 and partial/unavailable search never becomes authoritative zero matches.
- [ ] 13.4 Overlay separate mutable index metadata consistently in returned views and remove it with accounted payload → verify: missing disposed index metadata fails explicitly and does not trigger frozen-blob repair or fallback.

## Task 14: Expose explicit CLI apply

- **Status:** pending
- **Depends on:** Task 12, Task 13
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [CLI apply](implementation.md#task-14-expose-explicit-cli-apply-and-receipt-recovery), [apply contract](contracts.md#apply-protocol), [response boundary](contracts.md#query-and-response-boundaries)

### Subtasks

- [ ] 14.1 Enable apply in `cmd/capy/runs_recover.go` with all required flags, optional positive finite `--timeout`, and no MCP mutation capability → verify: malformed/missing confirmations and zero/negative/malformed/overflowing timeouts fail before writes.
- [ ] 14.2 Add bounded receipt/current-blocker output and inspection by plan or `--receipt-id`/`receipt_id`, exposing `recovery.receipt_id` → verify: incident UUIDs yield `receipt_unavailable`, the ambiguous old selector is rejected, and uncertain commit/repeated apply recover evidence without duplicates or hidden later incidents.
- [ ] 14.3 Exercise an actual-binary offline flow through shutdown, plan, apply, restart and fresh capture → verify: sibling bytes/cursors survive, original command is never replayed and help advertises only integrated support.
- [ ] 14.4 Run each administrative operation once before budget rendering and format its captured result without I/O → verify: budget retries/output-delivery failure cannot repeat upgrade, plan computation or apply, while committed receipts remain discoverable.

## Task 15: Publish operator instructions and invariants

- **Status:** pending
- **Depends on:** Task 14
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [documentation plan](implementation.md#task-15-publish-the-operator-workflow-and-revised-invariants), [future option](design.md#conditional-future-improvement)

### Subtasks

- [ ] 15.1 Update `docs/reference/runs.md`, `docs/architecture.md`, root `AGENTS.md` and public help/tool descriptions to the implemented workflow; replace the execution-only migration invariant with automatic authenticated configured-execution upgrades plus explicit offline upgrade, both exclusive and quarantine-preserving, with readers/helpers/doctor/stats non-migrating → verify: first healthy/quarantined/busy execute behavior matches Task 2 and the current-schema check explains that a refused execute may already have migrated.
- [ ] 15.2 Add refusal-specific examples, longer `--timeout` commands for both plan/apply, retention limitations and ordinary-terminal shutdown/restart guidance → verify: no default database edits, guessed PID kills, capture bypass or destructive reset; every case has a usable next step and the offline path is distinguished from normal attachment maintenance.
- [ ] 15.3 Preserve machine-verifiable recovery as a conditional future improvement with revisit triggers and legacy constraints; retain separate lifecycle/finalization follow-ups → verify: these do not silently become v1 implementation or release gates.
- [ ] 15.4 If published routing instructions change, update their generator and committed counterpart together → verify: `TestGeneratedWholeFileArtifacts` and `TestMergedArtifactsAreIdempotent` remain satisfied.

## Task 16: Final verification and release evidence

- **Status:** pending
- **Depends on:** Task 15
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [release evidence](implementation.md#verification-and-release-evidence), [acceptance plan](implementation.md#task-16-final-verification-and-release-evidence)

### Subtasks

- [ ] 16.1 Invoke `kk:test` for the named evidence matrix, affected FTS5/race suites including hook observations and stats consumers, repository build/vet/race checks and Linux/macOS native lifecycle acceptance → verify: captured commands, exact revision/platform and meaningful outcomes are recorded; unrun native checks remain open.
- [ ] 16.2 Apply the repository benchmark trigger to the final diff: search/indexing/chunking/executor changes (including planned Task 13 run-store changes) require `make bench-quality` baseline/candidate comparison; documentation-only edits do not. Verify disposed-run eligibility/coverage/immutability with focused tests; add performance checks only for changed paths or concrete concerns → verify: regressions and each check's limits are explicit, with all attempts retained.
- [ ] 16.3 Invoke `kk:document`, `kk:review-code` with Go and `kk:review-spec`; resolve material findings and record any accepted remaining work durably → verify: implementation, operator guidance and these contracts agree.
- [ ] 16.4 Complete actual-binary operator walkthroughs and compatible-binary rollout checks; archive feature docs only after implementation/acceptance are complete → verify: no historical output loss, automatic execution retry or misleading cleanup-proof claim.

## Dependency Graph

```text
1 -> 2 -> 3 -> 4 -------+-> 6 --+
           |           |      |
           +-> 5 ------+      +-> 8 -> 9 -> 10 --+-> 12 --+
           |                  |       |        |        |
           +-> 7 -------------+       +-> 11 --+        +-> 14 -> 15 -> 16
                                               |        |
                                               +-> 13 --+

10 and 11 both precede 12 and 13.
```
