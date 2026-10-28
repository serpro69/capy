# Run recovery contracts

> Status: proposed implementation contract; independent review pending
> Parent: [design.md](design.md)
> Plan: [implementation.md](implementation.md)

## Authority and invariants

1. Execution cleanup stays a validated `(scope, state)` pair. No new accepted cleanup state is introduced. `terminal == (CompletedAt != nil && CleanupAccepted(cleanup))` remains the execution invariant.
2. An operator disposition is a separate, authenticated administrative record. It may authorize operational closure and future admission without making original cleanup complete or setting an original exit code, completion timestamp or terminal-persistence bit.
3. One shared `OperationallyClosed` rule accepts valid execution-terminal evidence or a valid disposition bound to the exact run/store/generation. Use it only for the administrative consumers identified here; never replace execution-proof validation with it.
4. Pending unresolved execution never expires or becomes safe merely because a lock is free, a PID is absent, or time passed. Unsupported identities/proof combinations remain errors.
5. Diagnostics/planning do not initialize, reconcile, maintain, publish policy, index or claim execution owners. Apply/upgrade use dedicated administrative attachment and close paths, not `execution.Service` or normal `Store.Close`.
6. Recovery is all-or-nothing for the reviewed store inventory. It does not execute, signal, adopt or resume application work. Existing permanent lock inodes remain in place.
7. No recovery/upgrade operation evicts retained history to fund itself. Normal subsequent TTL/quota maintenance remains separate and explicitly described.

## Persistent model

Schema 10 adds the following logical records. Implement explicit constrained SQL with indexed subject/dependency lookup; no full schema implementation is prescribed here. All records live in the same encrypted run database.

| Record | Required meaning and bounds |
| --- | --- |
| Incident | UUID; stable subject kind `run`, `owner` or `store`; subject identity and known slot/generation/run/batch IDs; bounded reason enum; attribution `recorded`, `inferred` or `unattributed`; first detection time; detection stage; last durable phase; optional recovery receipt reference. No program, output or raw backend error. |
| Recovery receipt | UUID; unique reviewed-plan hash; applied time; method `operator`; declared basis `groups_stopped` or `host_restarted`; current OS UID as local provenance, not identity proof; required sanitized explanation at most 1,024 UTF-8 bytes; immutable aggregate counts; charged bytes and audit deadline. |
| Run disposition | Unique run ID bound to store, original generation/slot and optional batch; receipt and incident references; original result snapshot (existing 8-KiB limit), cleanup pair and terminal bit; admission TTL/policy revision; fixed administrative retention deadline; charged bytes. References must survive deletion of the run payload row. |
| Incident resolution membership | Immutable receipt-to-incident association, including owner/store incidents with no run. Preserve the subject/reason snapshot needed after registry reuse. |
| Batch resolution | Receipt/provenance references and separate operational-close/retention times once every child is operationally closed. Preserve original batch completion/expiry fields. |
| Metadata additions | Store recovery revision; last durable phase for newly written runs; disposition references on retained child manifests where needed. Do not add defaults to the existing cleanup columns. |

Deduplicate only unresolved incidents by subject identity within the store, including historical generation. An unchanged repeated observation reuses the existing incident and original detection time. A resolved incident is immutable: a later incident for the same owner/store subject receives a new UUID/episode and revision, never the previous receipt. A disposed run cannot be executed again; a later identity/proof inconsistency in that run must fail validation rather than be excused by its old disposition. Add bounded reason/stage information only when new evidence materially changes an unresolved incident; increment recovery revision then. Never infer the original detection timestamp from a migration time. Historical phase may be `unknown`.

Supported last durable phases are `unknown`, `admitted`, `queued`, `authorized`, `compile` and `run`. Set admitted/queued in admission, authorized in the existing pending-proof update, and compile/run in the existing start callback. Do not add an extra capture write for every event. Detection stage separately identifies startup, helper wait, finalization, reconciliation or legacy inspection; it must not pretend to be a durably recorded execution phase.

New incidents use typed causes such as `pending_cleanup`, `finalization_unpersisted`, `supervisor_lost` and `startup_uncertain`. `generation_mismatch`, invalid proof/manifests and unresolved structural inconsistencies remain diagnostic reasons that refuse application. `legacy_unattributed` is permitted for a structurally valid historical marker whose exact causal run is unavailable.

Incident/receipt fields are versioned and bounded. Subject IDs cannot become filesystem targets. Sanitization occurs before storing the explanation; reject an explanation that is empty/invalid/oversized after normalization. Do not print that stored explanation in admission errors or doctor. Explicit recovery-detail inspection may display its sanitized form.

## Incident publication and fail-closed fallback

`Service.quarantine` receives the known admitted run/generation and a typed stage/cause. It retains the current in-memory quarantine latch even if the storage write fails. Persist incident attribution and store quarantine together where possible. Reconciliation persists its incident and owner/global marker together under the existing registry lock and transaction.

An attribution failure must not lose the underlying refusal. Cache a bounded context for this owner's subsequent errors; do not claim it is durable. On restart, authenticated pending records/legacy markers remain the fallback. Inspection explicitly distinguishes known durable attribution, inferred candidates and unavailable attribution.

A still-live run with pending cleanup and no quarantine evidence is not an incident merely because it is unfinished. Ordinary read-only inspection never acquires owner-generation leases to speculate about liveness. Offline planning, after excluding all cooperating owners/helpers, may include well-formed abandoned pending rows as inferred recovery subjects even when an earlier attribution write failed.

A valid legacy global marker with no identifiable pending row is represented by a store-scoped incident. Operator recovery can resolve that reviewed store concern after the same shutdown/cleanup responsibility; it must not invent a run ID. Registry-generation mismatches, missing nonterminal batch children, malformed results and inconsistent proof copies are outside this override and fail with actionable `recovery_inconsistent` diagnostics.

## Read-only inspection

Public surfaces:

- `capy runs inspect [--run-id ID | --recovery-id ID | --plan TOKEN] [--cursor CURSOR] [--limit N] [--json]`.
- `capy_runs(action="inspect", ...)` with the same read-only selectors, cursor and limit; no apply, confirmation or cleanup-basis arguments.
- CLI/MCP doctor and stats reuse the bounded summary snapshot.

Use an existing shared lifecycle lease, read-only authentication and a read transaction. Never create missing paths/locks or invoke normal attachment/close maintenance. Inspection has the existing two-second budget; pages use 1–64 entries and the configured serialized response limit. Bind continuations to store identity and the inspected recovery revision; changed evidence returns a stale-view instruction instead of silently skipping new subjects.

Return selected owner/store identity, schema compatibility, effective admission status, durable/inferred/unattributed counts, bounded subjects and missing evidence, last-recorded state/phase, receipt/disposition status, availability of retained output and a fixed action code plus safe next-step text. Doctor shows blockers separately from its recent sample. A partial/unavailable detail query remains visibly incomplete; it never reports an empty blocker inventory as a healthy store.

Schema-8/9 adapters provide bounded diagnostic facts already supported by those schemas and `upgrade_required` guidance. They do not pretend to contain new attribution or dispositions. They never migrate and cannot issue a schema-10 application token.

Store paths shown as text are sanitized and bounded. Machine-readable owner selection is a validated argument value, not shell code. Human command examples must quote physical paths correctly; an unsafe/unrepresentable path gets explicit selection instructions rather than an executable-looking truncated command.

## Recovery plan

`capy runs recover [--cursor CURSOR] [--limit N] [--json]` without `--apply` performs nonmutating planning. `--project-dir` follows the existing explicit owner resolver. There are no partial run/owner selectors in v1.

Planning requires schema 10, the selected credential and exclusive administrative access. Acquire the permanent lifecycle lock exclusively without converting an existing shared flock; then acquire the permanent recovery lock and nonblocking owner-slot locks in the established order. Validate their identities and retain them through the read-only snapshot. A held/missing/unsafe lease refuses readiness. These locks are not process-quiescence proof and do not claim new owner generations.

Inventory every unresolved incident, relevant registry marker, abandoned nonterminal proof and work claim. Validate run identities and manifest copies. Include original outcome/proof, last durable phase, captured-prefix counters and TTL inputs needed by recovery; exclude command/output bodies. Proven safe nonterminal rows are reported separately as eligible for ordinary later reconciliation, not administratively rewritten.

Compute a canonical fingerprint over store UUID, schema version, policy revision, recovery revision, global/owner quarantine markers and the complete ordered recovery subject/evidence inventory, including each affected run's original generation, cleanup/result and prefix counters. Stream rows in batches of at most 64; do not retain every program or payload in memory. The five-second planning budget may return `recovery_inventory_incomplete`; it must not return an applicable token for an incomplete inventory.

The token is an opaque, versioned, bounded representation of those identity/revision/hash fields (at most 1,024 bytes), not a permission credential or a claim that a human read the output. The complete fingerprint is computed even when response pagination omits subjects. Show total counts, omitted/continuation status and exact inspection commands so the operator can review all subjects. Pages and apply reject a changed snapshot. No token is persisted by planning.

The plan identifies dispositions to be recorded, all remaining unsupported blockers, effective output/audit deadlines, that quota can evict output sooner, and whether adequate metadata capacity exists. A non-applicable plan never includes a runnable apply instruction that bypasses its refusal. No preview success means that old execution groups have been verified stopped.

## Apply protocol

Syntax: `capy runs recover --apply --plan TOKEN --confirm-stopped --cleanup-basis groups_stopped|host_restarted --reason TEXT [--json]`.

All four confirmation/basis inputs are mandatory for apply; reject missing, unknown or conflicting arguments before storage changes. The basis describes an operator-established fact. `host_restarted` does not claim that Capy matched historical boot identity. The operator must establish that the relevant originating execution was stopped, including when a store was moved from another machine. When that is unknown, do not apply.

Application uses a 30-second total context and the same exclusive lock order as planning. It authenticates and rechecks schema/store identity under those locks. It never opens through the MCP service or ordinary maintenance attachment.

Within one RESERVED write transaction:

1. Look up an existing receipt by exact plan hash. If present, verify its store binding and return that receipt without renewing timestamps or charges. Also report current unresolved blockers; old success does not imply no later incident exists.
2. Otherwise recompute and compare the full plan. Any material change, unsupported inconsistency or incomplete inventory aborts without dispositions or cleared flags.
3. Reserve/transfer bounded audit charges and check physical/quota capacity without retention. Capacity failure names the offline quota-increase/space procedure; it never discards evidence.
4. Materialize missing inferred/legacy incident subjects and insert one receipt plus immutable memberships and dispositions for the reviewed uncertain runs. Preserve original result blobs, cleanup pairs, execution terminal/completion/expiry, programs, raw events and existing projection coordinates.
5. Mark the reviewed incidents administratively resolved. Derive affected batch operational metadata. Clear relevant registry markers only when that generation has no unresolved incident or unsafe undisposed work. Recompute the global admission marker from all unresolved operational blockers. A plan containing unsupported blockers cannot partially succeed.
6. Commit. Close database handles before releasing permanent locks, without reconciliation, retention, checkpoint-driven reclamation or command execution.

Execution writes and batch dispatch must reject an administratively closed historical run. Index writes must reject stale historical owner generations and cannot mutate execution facts; fresh healthy owners may perform the narrowly scoped index work defined below. Normal slot reuse can publish a fresh generation only after its old operational blockers are gone. Existing service latches are not cleared in place; operators restart affected services.

Matching abandoned index claims can be released by ordinary generation-validated reconciliation after restart; a disposition must not make a mismatched claim safe or authorize its deletion.

If commit outcome is uncertain, return `recovery_outcome_unknown` with a read-only receipt lookup by plan token. Repeating the same apply is safe while its receipt is retained. Once audit history has legitimately been pruned, an old token is stale; it cannot create another disposition or retention window.

## Operational consumers and public results

Keep public envelope version 2, cursor/redaction versions and private helper protocol 2. Add optional structured `recovery` metadata to affected run/batch summaries, read subjects and search coverage, and a bounded `recovery_hint` to relevant errors. The fields distinguish unresolved from operator-resolved, expose receipt ID/time/basis, administrative retention/closure and whether the incident blocks admission. They are essential when present, not optional preview text.

Original `state`, exit/signal, `CompletedAt`, `Capture.TerminalPersisted`, cleanup and execution-terminal validation retain their historical meaning. Read projections of uncertain runs continue to identify capture as incomplete and cleanup as uncertain, with explicit operator-resolution context; the stored original result is not rewritten. Human output uses “last recorded” language. A resolved historical run must never qualify as healthy running/recording or successful completed tool evidence in hooks.

Reconciliation excludes exact disposed historical rows from unfinished operational work and generation-mismatch quarantine; it does not ignore genuinely inconsistent unrelated rows. Owner reuse resets only the derived marker for the reusable slot, while the incident/disposition preserves original generation attribution.

For batches, keep original per-child result/proof/terminal copies. A missing nonterminal child is allowed only when a retained disposition snapshot proves its exact identity and approved payload eviction; otherwise keep the existing error. Compute administrative closure separately when all children are terminal or disposed. Historical cleanup aggregation remains pending when any original child proof is pending. Report recovered-child counts separately, and exclude disposed children from any current-concurrency presentation. Never set original `CompletedAt` or describe administrative closure as successful execution.

`status/list/read/search` consistently use administrative availability/retention for disposed payload. Ordinary execution APIs do not accept a recovery receipt as execution proof. After payload deletion, ordinary run reads report unavailable with an incident-inspection pointer while the audit record remains. Batch summaries retain their existing `output_available=false` behavior plus disposition context.

### Capture and projection boundaries

Apply does not append output, flush an unfinished sanitizer checkpoint or regenerate projection. Preserve only the existing safe projection; withheld tails remain withheld. Raw access still requires explicit unsanitized base64. Search indexes only existing sanitized coverage and reports partial coverage where projection is unfinished.

Keep the existing execution-based `eof` meaning. A separate `recovery.capture_closed=true` tells updated readers that no further capture is expected even when original completion is unknown; `end_of_available` can then stop polling without fabricating EOF/terminal proof. Existing cursor coordinates remain valid for available bytes. New status/help text explains this distinction.

Index workers may claim a disposed retained run through a fresh healthy owner once store admission is healthy. They must not serialize derived recovery annotations over the original historical result. Retain the existing index-progress tables and update only the result's existing index fields, preserving every recorded execution/capture fact and the immutable disposition snapshot. Disposition validation compares bound identity, proof and execution facts, not mutable index progress, so a legitimate later search cannot invalidate recovery. Update final availability checks and batch coverage to use the same retention predicate.

## Retention and accounting

- Unrelated completed-run deadlines and eligibility do not change. Recovery does not renew them. Output already legitimately evicted cannot be restored.
- For each disposed uncertain run, fix `retention_expires_at = applied_at + its admission-recorded TTL`. This administrative field does not replace original `ExpiresAt` or `CompletedAt`. Effective payload expiry uses it once disposition exists.
- Disposed payload becomes quota-evictable under the existing oldest-eligible policy; use administrative resolution time for its ordering. Recovery's own transaction never calls retention. Later ordinary maintenance/admission can evict expired or pressure-selected history, as the plan states.
- A compact receipt/incident/disposition remains protected until its administrative deadline and while any retained run, child manifest or other incident-resolution reference requires it. A receipt deadline is at least the maximum of its member deadlines; owner/store-only members use the stored policy TTL at application. Deadline/identity dependencies cannot be renewed by reads, retries or later policy edits.
- Once all children are operationally closed, a batch with disposed children gets separate administrative closure/expiry using its admission-recorded batch TTL. Existing terminal batches keep their original expiry. Its manifest can be evicted under quota/expiry only when every child is operationally closed and every needed disposition is independently retained.
- Delete payload/index rows in the existing bounded tombstone batches. Keep the full payload debit until removal completes. Before deleting the parent run, transfer the retained audit debit to independent metadata accounting in the same transaction; do not lose it through a cascading foreign key or charge it twice.
- Charge bounded records explicitly, including recovery receipts and memberships. Existing run reservations may fund transferred bounded metadata, but the implementation must verify that bound rather than assume all cases are free. Recompute the current dbstat/conservative debit after changes. Independent audit metadata and schema/index overhead must not become an unbounded uncharged store.
- Prune an entire receipt group only after its maximum deadline and after retained run/batch references to its members are absent. Mark a group as pruning before bounded deletion, retain its plan-hash tombstone/receipt identity until all dependent rows are removed, and remove the root receipt last. A partially pruned group cannot be reapplied or mistaken for a new unresolved incident. Retained audit may exhaust quota; report that accurately. Do not destroy protected evidence or misreport usage to admit work.
- The offline `upgrade` quota-increase mode permits explicit configured growth without eviction even while quarantined. It refuses TTL/per-run-limit changes and quota decreases; it reports revision changes and invalidates old plans. Disk-full or metadata-capacity failure keeps history and quarantine intact.

## Upgrade and compatibility

Schema 10 adds the records/indices without rewriting original run payload/results or changing store UUID. A new current-schema fixture and the actual frozen schema-8/9 fixtures must validate the transition, including pending history and retained batch copies.

`capy runs upgrade [--json]` resolves the existing selected credential, acquires exclusive administrative ownership and performs no execution admission. It never initializes a missing store. Supported paths are 9→10 and 8→9→10 using the existing validated 8→9 transformation followed by the new migration. Each committed step preserves quarantine/history; interruption between steps leaves a valid supported intermediate schema and an actionable retry. Do not claim that both separate migration commits roll back together.

Use bounded contexts, identity rechecks, private temporary behavior and preflight headroom. Publish schema/policy markers last in each transaction. A failure cannot trigger deletion to make migration fit. Capacity preflight and optional explicit quota growth must work without normal policy-change reconciliation/retention. Inspection/planning on old schemas only reports the offline command; helpers and ordinary readers never migrate.

New normal execution attachment may invoke the same migration functions under exclusive ownership, preserving fail-closed quarantine before any spawn. The operator workflow uses `upgrade`, not a sacrificial execution command. Older binaries refuse schema 10. Downgrade requires a compatible reader for retained data; existing explicit discard remains the destructive option after cleanup. No reverse migration or credential rotation is introduced.

## Refusals and operator guidance

| Condition | Required action and meaning |
| --- | --- |
| `cleanup_unverified` before admission | State that no command was admitted; identify known blockers/counts and direct to `capy runs inspect` for that owner. Retained completed logs remain recoverable subject to ordinary retention. |
| `storage_busy` / recovery lease held | Stop the identified store users/helpers and retry inspection/planning after shutdown. Do not equate a stored running row with a live PID. |
| Credential unavailable/wrong key | Restore the selected matching credential and reload affected processes. Do not automatically reset, migrate, fall back to a different key or call this a cleanup fix. |
| `upgrade_required` / unsupported schema | Use the compatible binary and explicit offline upgrade; never submit a command to force repair. |
| `recovery_plan_stale` | Reinspect and review the changed set; do not apply the old confirmation to new incidents. |
| `recovery_inventory_incomplete` | No applicable token; explain the bounded query/time or unavailable metadata cause and how to obtain a complete inspection. |
| `recovery_inconsistent` | Preserve diagnostics and resolve the named identity/proof/manifest inconsistency separately. Operator confirmation cannot override it. |
| `recovery_capacity` | Show required/available accounting or filesystem headroom and the explicit configuration-increase/offline-upgrade procedure. No recovery-driven eviction. |
| Cleanup cannot be established | Remain blocked. Explain that the operator must investigate/stop originating execution; a guess, free lease or missing PID is insufficient. |
| `recovery_outcome_unknown` | Inspect receipt by plan token; do not assume success or repeat execution. Reapplying the same retained token is idempotent. |
| Applied/already applied | Show receipt and current admission status, original uncertainty, retention deadlines and restart/verification instructions. Later incidents can still block admission. |

An applied receipt reports the cleanup gate's current state, not a guarantee of future quota, credential, sandbox or command success. Proven-safe nonterminal siblings may still require ordinary bounded reconciliation after restart; report that work separately from unresolved cleanup rather than fabricating their completion during apply.

Preserve actionable code and next step in the minimum 2-KiB serialized response budget. Error data must not contain programs, captured output, raw stored/backend errors or secrets. MCP has no recovery-apply capability; it instructs the operator to use an ordinary terminal.
