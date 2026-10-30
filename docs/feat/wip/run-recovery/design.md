# Run recovery that preserves history

> Status: Tasks 1–15 implemented; final verification and release evidence in progress (Task 16)
> Issue: [#142](https://github.com/serpro69/capy/issues/142)
> Contracts: [contracts.md](contracts.md)
> Implementation: [implementation.md](implementation.md)
> Tasks: [tasks.md](tasks.md)
> Created: 2026-10-28

## Problem and intended outcome

One run with missing durable cleanup evidence can quarantine the selected workspace's entire run store. That refusal protects against unknown surviving execution. Today the supported recovery, `capy runs reset --discard`, removes unrelated completed logs as well as the affected evidence. Admission errors do not identify the blocking runs, and doctor's eight most recent entries can omit the actual blocker.

This feature adds actionable blocker diagnostics and explicit operator recovery within the existing encrypted store. The operator stops store users and old execution groups. Capy validates exclusive access, authenticates the store, checks the reviewed incident set and records an administrative disposition. It preserves the original execution outcome and cleanup evidence. Subsequent admission uses the disposition without claiming that the original command succeeded or that its helper persisted cleanup proof.

Success means that an interrupted owner's incident can be diagnosed and resolved by a documented procedure; a later MCP session can then admit new work; unrelated retained output remains byte-identical and accessible through existing IDs/cursors; and the affected run's uncertainty remains visible. A new execution refusal must provide a usable next step, not leave the operator to abandon capture.

## Evidence and approved decisions

The [investigation](investigation.md) records the incident and archived-session timeline. Failed finalization while the original owner remained alive is the leading explanation, not a proven reconstruction. Version 0.18.2 already fixes one delayed-quarantine path; that does not supply recovery or explain the original write failure.

The [five-agent isolated verification](investigation.md#source-findings-and-verification) confirmed the relevant storage, cursor, query, lease and retention behavior. It did not reproduce the Linux incident or establish which alternative is cheaper or safer.

The user approved:

- Recovery in the existing store, with operator responsibility for stopping old execution groups.
- CLI-only application; MCP and doctor expose diagnostics. No automatic recovery or command retry.
- Review of the exact store/blocker set, exclusive authenticated application, atomic dispositions/admission changes and service restart afterward.
- Preserved execution evidence, unrelated history and existing IDs/cursors; no retention deletion to make recovery succeed.
- Durable incidents independent of recent activity, clear legacy uncertainty and complete operator instructions.
- Separate retention from recovery time for disposed uncertain-run output, using its admission-recorded TTL; normal quota pressure still applies.
- Audit metadata surviving payload eviction and retained dependencies, with accounted and bounded cleanup.
- An explicit offline schema upgrade without submitting an execution command.
- Native Linux and supported macOS verification, plus a documented future machine-verifiable alternative.

## Users and scope

The primary user is a developer operating Capy in a local workspace, including multiple MCP sessions sharing its owner-local run store. Agents need enough bounded information to explain the problem and retrieve retained logs. The human operator performs the recovery action from a terminal after stopping affected sessions.

The scope is the selected physical owner's `.capy/runs/runs.db`. Knowledge, vaults, other worktrees' run stores, platform trust and capture-routing policy are outside the mutation boundary. Recovery does not change encryption credentials or execute application commands.

## Existing architecture

`internal/execution.Service` owns the MCP/CLI generation and process-local quarantine latch. Dedicated helpers inherit owner/lifecycle leases, authorize execution, capture output and attempt final persistence. `internal/runstore` stores authoritative scoped cleanup and coordinates the permanent lifecycle, recovery and owner-slot locks.

Current completion is deliberately strict: `terminal` means a recorded completion time plus accepted scoped cleanup. Pending evidence remains nonterminal. Recovery scans free slots and can set global/owner quarantine. Slot reuse replaces generation metadata; it must never erase historical incident meaning. Batches retain child summaries after output eviction and compare those summaries with retained proof.

Normal query attachment is not nonmutating: it reconciles and maintains storage, and search claims ownership and writes indexes. `Inspect` is the existing read-only exception. New inspection and administrative paths must avoid normal service attachment and close-time maintenance.

## Proposed architecture

### Separate execution truth from administrative resolution

Keep the existing cleanup scope/state pairs and execution-terminal invariant. Add durable incidents, recovery receipts and per-run disposition records. A disposition records an operator action and its declared basis; it is not another accepted cleanup state.

Use one shared operational predicate for admission/reconciliation/retention consumers: a retained run is operationally closed when it has valid execution-terminal evidence or a valid operator disposition bound to that exact historical run/generation. Never substitute this predicate for `CleanupAccepted` or execution-terminal validation.

The disposition holds the immutable result/proof snapshot required to interpret retained batches after child output is evicted. After disposition, the stored `runs.result` blob remains byte-immutable until payload eviction. Mutable search progress/status lives in separate index metadata and is overlaid only in query responses. Historic warnings no longer depend solely on the current mutable owner registry. Administrative metadata remains independently retained and accounted.

### Attribute incidents where they arise

New service and reconciliation quarantine paths persist a bounded incident subject, reason, detection stage, last durable phase and owner/run identity when known. They keep fail-closed in-memory/pending-proof behavior if attribution persistence fails. Repeated observation of the same incident does not create unlimited records or change timestamps merely because another inspection occurred.

Existing records may lack a causal run or detection time. Report that explicitly. A store-wide legacy marker can be a store-scoped incident; well-formed pending rows discovered after exclusive shutdown can be identified as abandoned candidates without inventing the original failure. Identity/proof inconsistencies are diagnostic blockers, not permission to repair arbitrary data through acknowledgement.

### Provide a shared read-only inspection model

Add `capy runs inspect` and `capy_runs(action="inspect")`, backed by the same authenticated, bounded read-only snapshot model used by CLI/MCP doctor and `capy_stats`. Show blocker totals and paginated details independently of recent history. Include retention/disposition information when inspecting a recovered run or receipt; `--receipt-id`/`receipt_id` selects a recovery receipt UUID, never an incident UUID. The model does not create storage/locks, claim an owner generation, reconcile, index, publish policy or maintain history. Stats retains its existing once-only byte accounting; inspection adds no completion observation.

Admission errors carry a small structured recovery hint and an exact next inspection step. An inability to retrieve detail cannot turn a refusal into permission. Optional lists and text may be shortened; the code, no-admission statement and next action must survive the minimum response budget.

### Review and apply an offline plan

`capy runs recover` without `--apply` is a nonmutating plan operation. It obtains exclusive administrative access so it can inventory abandoned work without confusing live jobs with incidents. While owners remain attached it refuses plan readiness and directs the operator to ordinary inspection and shutdown.

The plan binds the selected physical owner/database/lock identities, store UUID, schema/policy versions and the complete reviewed operational incident set. One deterministic inventory builder serves preview, pagination and apply; inferred subjects use stable keys until their UUIDs are allocated at commit. Unchanged evidence produces the same plan, while moving/copying/replacing its target requires a new plan. It includes a bounded summary, total counts, continuation instructions, estimated retention consequences and a token. An empty plan creates no token or audit action. Application is all-or-nothing for that complete set in v1; there is no partial per-run override. Offline planning and application default to five minutes each and accept a positive finite `--timeout` duration. An incomplete scan produces no token and gives an exact longer-timeout command; the two-second interactive inspection budget is separate.

`capy runs recover --apply` requires that token, explicit stopped-process confirmation, a cleanup-basis selection and a short explanation. Under exclusive locks it reauthenticates, validates the full inventory, checks idempotency and capacity, and commits dispositions plus derived quarantine changes in one transaction. A changed plan requires review again. It neither signals stored PIDs nor resumes a command. Restarted services get fresh process-local state.

### Preserve history under explicit retention rules

Recovery never renews unrelated completed-run expiry or evicts logs to fund itself. Disposed uncertain-run payload uses a separate retention deadline, `resolved_at + admission_ttl`, leaving original execution timestamps untouched. Normal post-recovery quota pressure may evict that payload sooner.

Audit metadata survives through its retention window and while a retained run or batch depends on it. It participates in accounting, bounded deletion and capacity checks. An attributed unresolved incident protects its retained run evidence even if terminal proof subsequently arrives; resolving that incident does not renew the run's original expiry or create an unnecessary run disposition. Batch operational closure and retention use separate administrative metadata, recalculated both at apply and when safe siblings are finalized by later reconciliation; historical cleanup remains pending where originally pending. The [contracts](contracts.md#retention-and-accounting) define pruning and dependency order.

### Upgrade without admitting a command

Use schema 10 for the additional state. `capy runs upgrade` provides an authenticated exclusive offline path for schema 8/9 stores. It preserves history and quarantine; it cannot resolve an incident. Legacy read-only inspection can explain the necessary upgrade but does not perform it or issue an applicable schema-10 plan.

The command also supplies a narrowly constrained offline quota-increase path when required to preserve metadata: after the operator raises `runs.max_retained_bytes` in configuration, it validates and commits that increase against the currently authenticated schema before migration capacity checks/copying, without retention, while rejecting configured TTL/per-run-limit changes and quota decreases. This avoids an unrecoverable cycle in which quarantine or migration prevents the only way to fund preservation. It reports the policy revision change and requires a fresh plan afterward. The quota increase can remain committed if a later migration fails; the command reports the resulting policy and schema without claiming a whole-command rollback.

The 8→9 migration uses a frozen schema-9 destination and explicit stage-version checks; it must not inherit the new schema-10 phase requirement through the shared fresh-store DDL. The separate 9→10 rebuild then establishes that requirement while preserving historical bytes and identities.

Normal authenticated, configured execution attachment automatically upgrades eligible schema-8/9 stores to schema 10 before admission, using the same migrations and exclusive lifecycle checks as `capy runs upgrade`. It must attempt that path; it does not require every existing user to run the CLI command first. A busy/failed upgrade refuses execution with repair guidance. Migration does not clear quarantine: a quarantined store can reach schema 10 as a side effect of an execute that is subsequently rejected, with no command admitted or spawned. Operators can instead use the explicit command without submitting application work. Ordinary attachment retains its normal maintenance behavior after migration; the offline administrative path does not run maintenance.

Old binaries must reject schema 10. Public run envelopes, helper protocol, cursor and redaction versions remain unchanged. Hooks continue to observe execute/fetch rather than run queries, and additionally reject any run envelope containing a `recovery` field as healthy execution evidence. Task 9 owns the defensive check and its root/child, v1/v2 and benign-control tests.

## Operator experience

Errors, doctor, inspection and the reference guide share one workflow:

1. Identify the selected owner and inspect blockers; retrieve any needed retained logs.
2. Stop all affected MCP sessions, attached CLI work, readers/helpers and old execution groups. Explain that closing a session alone is not proof all descendants stopped.
3. From an ordinary terminal, inspect the current schema, upgrade if still required and obtain a complete recovery plan. A prior refused execute may already have upgraded the store without clearing quarantine; do not infer schema from the error's timing. If planning times out, use its longer `--timeout` command. Do not run administrative recovery inside `capy_execute`, which would participate in the lifecycle being excluded.
4. Review the exact incidents and retention consequences; apply with the operator's cleanup basis. If cleanup cannot be established, remain blocked and investigate rather than acknowledge speculatively.
5. Restart affected sessions, inspect readiness and deliberately execute a harmless captured verification command. Then separately decide whether to retry the original application command; recovery itself never retries it.

Each failure has its own repair: live leases require shutdown; wrong credentials require restoring the selected credential; a stale plan requires reinspection; incomplete inventory requires resolving its stated limit; inconsistent evidence requires preserving diagnostics and repairing that specific inconsistency separately. Explicit discard is documented separately with its data-loss consequences. The ordinary path does not recommend disabling capture or editing SQLite.

## Assumptions

- The authorized operator can take the selected store offline and can establish that its old execution groups stopped. A false confirmation can authorize unsafe reuse; v1 records responsibility rather than claiming to eliminate this risk.
- The operator has the selected credential and adequate filesystem/quota headroom. Recovery cannot reconstruct unavailable keys or deleted output.
- Cooperative store users follow the permanent lease protocol. An exclusive lease excludes those users; it is not a host process inventory.
- The supported legacy database is structurally valid. Acknowledgement cannot repair malformed proof, mismatched generations/manifests or arbitrary database corruption.
- Ordinary TTL/quota retention after recovery remains acceptable. “Preserve history” means no destructive reset or recovery-driven eviction, not permanent archival.
- A new schema can require compatible binaries to be deployed together. No reverse migration is promised.

Validate these assumptions through the contract tests and operator scenarios in [implementation.md](implementation.md#verification-and-release-evidence). If a condition fails, report the concrete limitation; do not silently weaken a guard or retention rule.

## Not Doing

- Automatic recovery or MCP apply: applying a disposition remains an explicit offline operator action.
- Execution replay, process adoption or PID-based cleanup: recorded metadata is not authority to signal or rerun historical work.
- Changing Linux retirement or Darwin observation guarantees: the existing scoped execution contract remains authoritative.
- Run-key rotation, wrong-key recovery or archive management: keep the credential and storage model bounded to this feature.
- Permanent incident archives: use the agreed retention and dependency rules.
- Generic database repair: inconsistent/corrupt evidence remains a separate repair problem.
- Finalization retries or repairing the original lifecycle defect: investigate those independently so recovery does not depend on explaining every incident.

## Rejected Alternatives

| Alternative | Reason not selected |
| --- | --- |
| Archive the old store and create a new one | Viable, but adds historical routing, immutable-reader/search, compatibility and archive-capacity behavior. Same-store continuity better fits this issue. Neither approach is proven cheaper or safer. |
| Clear the global quarantine bit | Reconciliation rediscovers unresolved evidence; owner markers and process-local latches also matter. It loses causal/audit meaning. |
| Rewrite pending cleanup as retired/observed or fabricate completion | Conflates an administrative action with execution evidence and corrupts batch/retention semantics. |
| Treat an absent PID or free lease as cleanup proof | Neither establishes historical descendant quiescence; the existing record lacks sufficient process-origin identity. |
| Require machine-verifiable quiescence in v1 | Historical records lack necessary provenance. This remains a conditional future improvement below. |

## Conditional future improvement

Revisit machine-verifiable recovery if operators repeatedly cannot identify old groups, confirmations become guesses, unattended recovery becomes necessary, or surviving old execution is demonstrated after operator-authorized recovery. These are future review triggers, not claims about issue #142.

The concrete next step is a separate risk-first investigation of a verifiable process-lifetime boundary and trustworthy execution-origin evidence on each supported platform. Test escaped descendants, identity reuse, supervisor loss, host restart, copied/moved stores and interruption during verification. State precisely which scope/property a candidate mechanism proves and verify its permissions/platform requirements before choosing an API.

Define an explicit legacy policy for records lacking provenance. Do not backfill historical boot identity using the current machine or relabel operator attestations as machine proof. A future verified disposition method may extend this design through a deliberate compatibility change while preserving the original execution evidence. V1 adds no speculative probes or placeholder platform infrastructure.

Finalization reliability and the historical Linux reproduction remain separate follow-up investigations documented in the [investigation](investigation.md#separate-lifecycle-follow-up). They do not block recovery implementation unless a newly reproduced defect invalidates a stated safety invariant.

## Verification and rollout

The defining scenario combines an interrupted owner, retained successful siblings, a newer session, a blocker outside the recent-run sample, useful refusal guidance, continued reads, explicit recovery and successful fresh admission after restart. Verify unchanged original proof/results and byte-exact retained output, not merely a cleared flag.

Use meaningful deterministic fixtures for stale plans, conflicting owners, wrong keys, complete/partial inventory, interrupted transactions, idempotent application, batch copies, slot reuse, cursor compatibility, projection boundaries and bounded retention/accounting. User-visible tests cover exact next actions and minimum response budgets without exposing secrets. Run native lifecycle suites on Linux and supported macOS and required repository race/build checks. Apply the repository's quality-benchmark trigger to the final implementation diff: changes to search, indexing, chunking or executor code require `make bench-quality`, including run-store search changes planned here. Documentation-only corrections do not trigger runtime benchmarks. Evidence limits remain explicit.

Expose apply only after every affected consumer understands dispositions. Read-only diagnostics can be developed first. Implementation tasks include documentation and generated-artifact synchronization where guidance changes. No new dependency is proposed; retain existing Go `database/sql`, encrypted SQLite, context deadlines and permanent-lock conventions. PostgreSQL-oriented profile advice, external migration tools and service telemetry stacks do not replace these repository contracts.

External design-review findings were supplied and corroborated against code; the [resolution record](investigation.md#design-review-finding-corroboration) distinguishes valid gaps from corrected premises. A subsequent [fresh-read check](investigation.md#fresh-read-implementation-readiness) reread all five documents, confirmed those fixes and closed additional migration, planning, view-lifecycle and locking gaps. That historical readiness assessment is not runtime evidence. Tasks 1–15 are implemented; their verification and independent reviews are recorded in [tasks.md](tasks.md). Final release acceptance remains open under Task 16, including native evidence on both supported platforms.
