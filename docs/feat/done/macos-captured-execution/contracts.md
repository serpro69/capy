# Scoped cleanup contracts

> Status: implemented and verified; observational Darwin revision accepted 2026-10-22; Task 13 complete 2026-10-26
> Parent: [design.md](design.md)
> Applies to: run schema 9, public envelopes 2, private helper protocol 2

These are contract decisions. On 2026-10-22 the user accepted observational
Darwin cleanup after the native re-entry experiment. This supersedes the earlier
unimplemented `darwin_process_group_v1` retirement contract; no old Darwin run
data is presumed to exist or automatically reclassified. Linux evidence remains
unchanged. Rebase version numbers explicitly if the implementation baseline changes.

## Storage and typed evidence

`runs.cleanup_scope` is a new `TEXT NOT NULL` column, **without a DEFAULT**, with
`CHECK (cleanup_scope IN ('linux_descendants_v1','darwin_process_group_observation_v1'))`.
The existing `runs.cleanup` column remains `TEXT NOT NULL`, without a DEFAULT,
with `CHECK (cleanup IN ('not_spawned','pending','retired','observed'))`.
Add a row-level CHECK enforcing the exact scope/state pairs below; a known state
with the wrong known scope is still invalid. Neither column has a DEFAULT.

Admission supplies both columns explicitly. Scope is selected by the backend and
immutable for the run; callers cannot choose it. All subsequent transitions bind
the run ID, generation, and unchanged scope. Missing/null/unknown scope is rejected
by SQL and the typed boundary. Do not introduce a trigger or implicit default.

The shared Go value is `executor.CleanupEvidence`, with `Scope` and `State` string-enum
fields serialized as `scope` and `state`. `StreamResult.Cleanup`,
`runstore.Completion.Cleanup`, and `runstore.Result.Cleanup` use it. Replace the
unqualified wire `StreamResult.Retired` in protocol v2; any private convenience
predicate must inspect the typed evidence, not preserve a second authoritative flag.

| State | Meaning |
| --- | --- |
| `not_spawned` | No command phase has started or been left possibly executing; a compile phase counts as a command phase. |
| `pending` | Execution was authorized/pending or may exist without completed applicable proof at the layer being reported. This includes running and cleanup-unverified outcomes; persisted pending remains pending until finalization commits. |
| `retired` | Linux only: the existing backend proved descendant retirement and completed the required pipe drainage. |
| `observed` | Darwin only: every spawned phase completed the bounded observation policy in `design.md`, including leader reaping, required group signalling, final inventory and natural pipe EOF. It does not prove all processes are gone. |

Allowed pairs are Linux with `not_spawned` / `pending` / `retired`, and Darwin
with `not_spawned` / `pending` / `observed`. Darwin `retired` and Linux `observed`
are rejected at SQL, private frames, public decoding, recovery and aggregation.
The old proposed Darwin scope is unsupported, not an alias. Scope is selected
by the backend, never supplied by a public caller.

Local cleanup evidence and stored evidence are separate observations of this same
contract. Persist `pending` before spawn; only a matching helper/backend result
can finalize Linux `retired` or Darwin `observed`. Preserve `not_spawned` on a
known pre-start failure.
Public cleanup reports durable evidence, including a validated helper persistence
acknowledgement. If local cleanup completed but final persistence did not,
keep the public durable state pending, preserve the actual command outcome
separately, and expose the persistence/capture failure. Report `not_spawned` only
when that is the known durable admission/finalization state; a local failed spawn
does not overwrite an already-persisted `pending` state after finalization fails.
Terminal history additionally requires a completed outcome and policy-accepted
evidence (`not_spawned`, Linux `retired`, or Darwin `observed`); `pending` never
enables eviction/reuse. A local result
whose write failed must not overwrite persisted uncertainty at read time.

For a multi-phase run, scope is fixed across phases. Combine local phase proofs:
the whole run is `not_spawned` only if no phase started and none is unresolved;
it is `pending` if any phase is unresolved; otherwise it is the scope's completed
state (`retired` for Linux, `observed` for Darwin) if any phase started. Thus a
completed Rust compiler followed by failure to spawn the compiled program yields
whole-run `retired`/`observed` according to scope, not `not_spawned`, while
preserving the runtime failure. Do not launch a later phase before the previous
phase completes its applicable cleanup policy. Persist whole-run completion only
at finalization, never between compile and run phases.
This aggregation governs new execution outcomes. Migration preserves existing v8
safe cleanup values, including legacy `retired` records for no-start failures;
`retired` alone is not evidence that a command actually started.

**Reuse and retention are deliberate policy decisions.** Once a completed outcome
and accepted evidence are durably stored, apply the existing lease, owner, quota
and TTL rules equally to Linux retirement and Darwin observation completion.
Darwin's inherent possibility of an unobserved racing survivor does not itself
cause quarantine or prevent normal admission/expiry. Required signal/observation
errors, missing leader identity, visible remaining members at deadline, forced
reader closure, and unconfirmed reaping remain `pending` and preserve quarantine.
Helper loss cannot create `observed`; only validated durable evidence can recover
a lost terminal notification. An `observed` row is not proof of machine or
filesystem quiescence. Capture loss can coexist with completed cleanup evidence;
it still prevents claiming complete capture, as on Linux.

The run row is authoritative while present. `syncBatchChild` must copy the scoped
proof into `run_batch_children.result.cleanup` in the **same transaction** as the
run transition. That durable manifest copy remains authoritative after child log
eviction. Existing `terminal` bits remain a separate completion condition.
Readers compare the copy with a retained row and reject inconsistencies; they do
not infer proof from `capture.state`, `CompletedAt`, or output EOF.
Perform that comparison in one SQLite read snapshot (a join or read transaction),
not separate reads straddling a valid concurrent completion. Derive each batch
aggregate from the same child proof snapshot used for its comparisons. Close that
snapshot before lazy indexing or any other operation needing the one-connection
pool. Search output/index coverage can remain live and partial; proof metadata is
a bounded snapshot, not a promise that the entire search operation is frozen.

Every admitted batch uses one backend scope for all children. Mixed scopes are
invalid; no `mixed` scope is introduced. Aggregate batch cleanup state is:

1. `not_spawned` when every child is `not_spawned`;
2. the scope's completed state (`retired` or `observed`) when every child is
   terminal with accepted evidence and at least one has that completed state;
3. `pending` otherwise, including a completed child with queued siblings.

Aggregate state never substitutes for the per-child terminal/quarantine checks.

## Public version-2 envelopes

`cleanup` is a sibling of `capture` on a run result, with exactly two required
string values: `scope` and `state` from the enums above. A loaded/admitted run
always has a non-null object, including `admitted` and `queued` runs. A rejection
with no admitted run ID has `cleanup: null`; it does not invent a scope.

| View | Required location |
| --- | --- |
| Execute/execute-file run result | `cleanup` beside `capture` |
| Batch execute result | `cleanup` containing the homogeneous aggregate |
| Status/cancel query wrapper | `run.cleanup` or `batch.cleanup`; no duplicate wrapper proof |
| Read page | Existing `run.cleanup`; fragments/cursors carry no proof |
| Run or batch list | `entries[].cleanup`; list root has no aggregate |
| Standalone search page | `cleanup` for the selected run |
| Batch search page | `cleanup` for the batch and `children[].cleanup` for each returned child coverage entry |
| Rejection/query error before a run/batch is loaded | `cleanup: null`; no assertion about an unknown run |
| Capacity ID/age hints, individual search hits, knowledge hits | No cleanup field; these are not status views |

All corresponding result/query envelope `version` values are integer `2`,
including nested run/batch results. Existing non-cleanup field names and types
remain unchanged. Cursor `v`/redaction versions do not change merely because the
outer envelope changed. Proof fields are essential metadata and cannot be
truncated away; whole bounded child/entry collections retain their existing
omission indicators. No proof from a knowledge hit is implied by batch federation.

These hook-facing excerpts show the v1/v2 distinction; unchanged owner, timing,
projection, and byte-counter fields are omitted here, not removed from the API.

Version 1, existing Linux-era success:

```json
{
  "version": 1,
  "kind": "run",
  "run_id": "11111111-1111-4111-8111-111111111111",
  "state": "exited",
  "exit_code": 0,
  "signal": null,
  "capture": {"state": "complete", "terminal_persisted": true},
  "error": null
}
```

Version 2, successful Darwin execution with explicitly observational cleanup:

```json
{
  "version": 2,
  "kind": "run",
  "run_id": "22222222-2222-4222-8222-222222222222",
  "state": "exited",
  "exit_code": 0,
  "signal": null,
  "cleanup": {"scope": "darwin_process_group_observation_v1", "state": "observed"},
  "capture": {"state": "complete", "terminal_persisted": true},
  "error": null
}
```

`healthyRunEnvelope` keeps the existing v1 branch. Its v2 branch requires a
nonempty run ID, `kind=run`, no error/failure/signal, and known non-null cleanup.
For `running`, require `cleanup.state=pending`, `capture.state=recording`, and
null exit code. For `exited`, require the scope's completed cleanup state
(Linux `retired` or Darwin `observed`), exit code zero, complete capture, and
`terminal_persisted=true`. Reject cross-scope state pairs, including Darwin
`retired`, before eligibility. Accepting Darwin `observed` establishes usable
capture evidence under its declared policy, not host quiescence or a new
permission grant. Every other state/version/invalid shape
fails observation eligibility. Never parse a command's printed JSON as proof.

## Private protocol v2

**Linux and Darwin both run the same offset exchange.** Linux additionally
validates its fdinfo locks before participating. A Linux validation failure ends
startup; it does not select a shorter protocol. Both platforms use the same frame
sequence, malformed-frame fixtures, and pre-start recovery state machine. Their
process cleanup algorithms and accepted completion states remain distinct.

Retain FDs 3–7 and the existing four-byte big-endian length framing, 16-KiB control
limit, bounded credential frame, three-second startup deadline, and 250-ms write
deadlines. The credential FD still carries only the existing base64 byte-string
JSON frame and closes afterward; it never carries a proof or program.

Acquire the exchange gate **before spawning a helper**. Compute the coordinator's
absolute startup deadline before waiting for the gate: the earliest of three
seconds from launch admission, the caller/owner context deadline when present,
and the run's nonzero execution deadline. Gate waiting is cancellable and consumes
that same startup budget; acquiring the gate does not reset it. If waiting expires,
no helper exists: finalize the known-not-spawned attempt under the applicable
startup/cancellation/deadline outcome, with no kill or cleanup quarantine.

The startup frame keeps `version`, `run_id`, `generation`, `store_path`, and
`deadline`, adding required `exchange_id` (fresh UUID) and `cleanup_scope` (known
scope string). IDs retain their UUID validation; deadline retains the existing
RFC3339 Go time encoding, including its zero-time sentinel. No field is supplied
by the public execute caller. Backend, startup, and admitted row scope must agree.

Every later control/notice frame carries `version:2` and this `exchange_id`.
Control uses `action`; notice uses `type`. The decoder rejects unknown fields,
wrong roles, duplicates, wrong exchange IDs, out-of-order messages, and wrong
versions. The following are the allowed additional fields:

| Direction and discriminator | Fields and meaning |
| --- | --- |
| C→H `action=lease_observe` | `lease` = `owner` or `lifecycle`, `step` = 1 |
| H→C `type=lease_observed` | Same `lease`, `step=1`, integer `observed` and `written` |
| C→H `action=lease_confirm` | Same `lease`, `step=2`, integer `expected` |
| H→C `type=lease_confirmed` | Same `lease`, `step=2`, integer `observed` |
| C→H `action=leases_confirmed` | No additional fields; both roles completed |
| H→C `type=ready` | Required `cleanup_scope`, no outcome; store/backend binding succeeded |
| C→H `action=start` | Required `cleanup_scope` matching ready/admission |
| C→H `action=cancel` | No additional fields; existing liveness/cancellation semantics |
| H→C `type=terminal` | Required `outcome`; no top-level duplicate cleanup |

For `owner` and then `lifecycle`: C sets marker A before `lease_observe`; H samples
its own FD, changes it to B distinct from A, and reports both. C checks the
reported A and **its own FD's** transition to B, changes its FD to C distinct
from A/B, then sends `lease_confirm`. H checks its own FD equals C before replying;
C checks that reply. No peer may write offsets outside this phase. Markers are
positive integers no greater than 2^53−1, represented without fractions or
exponents; use fresh values and reject seek/overflow errors. Nonce reflection
without the local FD transition cannot pass.

After `leases_confirmed`, H permanently leaves the offset-mutating state, opens
and validates the admitted store, and sends ready. C releases the exchange gate
after validated ready, exactly once, using that exchange's ownership token. It
then authorizes start. Cancellation in the interval after ready but before a start
write may still kill/reap this helper, but must not release the already-returned
gate or disturb a later exchange. Before ready, failure retains
the gate until pre-start teardown below; no other startup may reuse it.

Terminal `outcome` retains `captured_bytes`, `last_sequence`, `result`, `reason`,
and `terminal_persisted`. `result.cleanup` is the required typed local evidence;
`result.retired` is forbidden in v2. Store completion compares its scope with the
admitted row. `terminal_persisted` separately acknowledges persistence. Lost or
invalid terminal data never grants retirement or observation completion by default.

## Pre-start teardown

Track `start_write_attempted` privately on the coordinator, initially false. Under
one cancellation/authorization lock, set it true **before the first write attempt
of the start frame**. A failed/partial write is therefore post-authorization
uncertainty; never infer safety from lack of a ready/start acknowledgement.

On exchange timeout/cancellation while it is false, prevent further authorization,
close startup channels, SIGKILL the retained direct helper, and arrange exactly
one reap. Killing this helper is safe because its trusted state machine could not
spawn a command. Use a separate three-second teardown observation budget, not an
unbounded `cmd.Wait` on the response path. Retain its identity through the final
signal and allow exactly one waiter; do not race another reaper with signalling.
After confirmed reap, release the gate only if this exchange still holds it
and finalize known-not-spawned admission without quarantining it as possible work.
Keep the owner usable; a SIGSTOP fixture must demonstrate recovery without restart.

SIGKILL is not a wall-clock reap guarantee. If exit/waitability/reaping cannot be
confirmed in budget, retain the actor, its gate, leases, and reservation under the
owner's cleanup tracking; report failure and refuse affected-owner startup until
safe. Do not abandon a waiter or release references just to meet a response deadline.
Here "its gate" means only a token it still owns; teardown after ready cannot
reacquire or release another run's token.
This rare pre-start fallback also applies to signal/wait errors. After any start
write attempt, use existing cooperative cancellation/join and durable proof rules;
do not force-kill the helper as a startup shortcut.

## Upgrade representation

Rebuild `runs` inside the encrypted DB transaction with the final no-DEFAULT scope
constraint; copy old columns explicitly and supply literal `linux_descendants_v1`
only in the v8-copy operation. Preserve dependent indexes/FKs and check referential
integrity before commit. Do not hand-edit `sqlite_schema`, use a persistent Linux
default, or rely on ALTER features newer than the pinned SQLite 3.51.2.

During that transaction, normalize every batch-child saved result to scoped v2:
use its retained run row if present. If absent, require the existing child terminal
bit: `capture.state=not_started` yields `not_spawned`, otherwise a valid legacy
terminal result yields `retired`, all under Linux scope. A missing nonterminal
row or malformed/inconsistent legacy result aborts upgrade; it is not guessed
safe. Subsequent child updates persist the canonical proof transactionally.

Test both fresh and upgraded tables with an INSERT omitting scope: it must fail
at SQL, not merely in a Go validator. Cover SQL NULL and unknown scope too.
Budget logical metadata/accounting changes separately from transient table-copy
and WAL/disk headroom. The existing quota is not a hard filesystem ceiling; do
not double-count copied payload as retained logs. Disk-full/capacity failure rolls
back without evicting logs to make the migration fit. Follow SQLite's documented
[general table-rebuild procedure](https://www.sqlite.org/lang_altertable.html#otheralter).
