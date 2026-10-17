# Execution and run-store contracts

> Status: proposed v1 contract — incorporates validated review corrections
> Architecture: [design.md](design.md) · Plan: [implementation.md](implementation.md) · Tasks: [tasks.md](tasks.md)

This file is authoritative for public inputs/results, state transitions,
pagination, scheduling and configuration. Transport adapters must not invent
different meanings. The design owns architecture and failure guarantees; the
implementation plan names code seams and tests.

## 1. Shared execution inputs and transport mapping

| Input | Contract |
| --- | --- |
| `language` | Existing supported executor language name; required in MCP, shell by default in CLI |
| `code` | Nonempty program, passed unchanged to the selected runtime; mutually exclusive with CLI code-file input |
| `cwd` | Optional existing execution directory; relative to selected project; never selects the run store |
| `timeout` | Positive integer milliseconds on MCP; explicit request value overrides configured seconds; zero, negative, fractional and overflow values are errors |
| `intent` | Optional nonblank search intent; requests lazy indexing/preview selection, never controls whether capture or output bounds apply |
| `wait_ms` | MCP response-wait budget, 0–30,000; independent of execution deadline |
| `background` | Legacy MCP execute mode only; false by default; true makes timeout the initial response wait and leaves execution owner-bound without a deadline |

MCP `capy_execute` uses these inputs. `capy_execute_file` retains its current
checked `path` and FILE_CONTENT behavior, with the same deadline/wait semantics.
`background=true` combined with `wait_ms` is rejected; background is not an
execute-file or batch input.
Retain current presence-aware boolean parsing instead of silently treating strings
as truthy. All numeric conversions are checked; omission differs from explicit zero.

CLI `capy execute` accepts `--language`, exactly one of `--code`/`--code-file`,
`--cwd`, `--timeout-ms`, `--intent`, and `--json`, plus inherited `--project-dir`.
Code-file input follows checked file admission. It waits for completion and
remains the process owner; the host can yield/poll that CLI process. CLI does not
accept `--background` or `--wait-ms` in v1: those are MCP response controls, not a
request to start a daemon. Both interfaces call the same execution service.

Programs are not silently suffixed with filters, redirections, retries or shell
options. Existing runtime selection and sanitized environment semantics apply;
interactive shell state and aliases are not inherited by contract. Requests are
non-idempotent. A lost response or post-spawn capture error requires status/list
recovery before any retry. CLI fallback is safe only when the original execution
is known not to have started.

MCP batch inputs retain `commands` (nonempty array of label/command objects),
`queries` (omitted/empty means no search), `concurrency` (default 1, range 1–8),
`query_scope` (batch/global, default batch), `cwd` and `timeout`, and add `wait_ms`.
Labels are display metadata, not unique identities; ordinals and run IDs identify
children. Validate the whole inventory and its pending-capacity limit before
admission. Existing bounded shell scanning applies to every command. Nonempty
queries use the same length/count constraints as run search.

## 2. Defaults and validation

| Setting | Default | Bounds / meaning |
| --- | --- | --- |
| `executor.timeout` | 300 seconds | Positive integral seconds; overflow-checked conversion to milliseconds |
| `runs.wait_ms` | 25,000 | 0–30,000; MCP only |
| `runs.max_response_bytes` | 8,192 | 2,048–65,536; complete serialized result, not just preview text |
| `runs.max_active` | 4 | 1–8; per execution owner |
| `runs.max_pending` | 64 | 1–256; total pending batch-child admission per owner |
| `runs.ttl_hours` | 24 | Positive integer; recorded for a run at admission |
| `runs.max_run_bytes` | 64 MiB | Positive integer; no larger than retained-data limit |
| `runs.max_retained_bytes` | 512 MiB | Positive integer; accounted data, not a hard filesystem-byte ceiling |
| `routing.routine.enabled` | true | Boolean; applies to the Codex routine gate only; explicit false must survive config merging |
| `routing.routine.native_allow` | empty array | Existing capy `Bash(pattern)` matching syntax, validated as Bash rules; no new permission grant |

Native exceptions match recognized routine executable elements using the existing
Bash-pattern semantics. Every uncaptured routine element must be covered to skip
the whole-request routine denial; covering one element never exempts its siblings.
Security evaluation is independent and earlier. These hook settings are not part
of the run-store retention policy and cannot enable Claude enforcement in v1.

Five minutes bounds forgotten jobs while allowing ordinary tests; known longer
builds use an explicit deadline. A 25-second wait avoids an extra poll for many
short tests and leaves headroom beneath a 30-second client timeout. That is a
default rationale, not a claim about every host: live acceptance measures the
actual host limit and configures a smaller wait if necessary.

Currently `executor.timeout` is loaded but not consumed: execute/file hardcode
30,000 ms and batch 60,000 ms. The new service wires it for the first time:
explicit per-call milliseconds → configured seconds × 1,000 → default above.
This changes existing effective behavior and must be documented. Presence-aware
config validation rejects explicit zero even though the old merger ignored it.
The deprecated `executor.max_output_bytes` only further restricts optional preview
text; it cannot discard stored bytes or essential result fields.

Owner-local settings are deadline, wait, response size and active/pending capacity.
Store-wide settings are TTL, per-run capture limit and retained-data limit.
Schema/redaction versions and quota-accounting mode also belong to the store
policy. Their authority and change protocol are defined in §9.

## 3. Result envelopes and state transitions

All results carry `version=1` and `kind=run|batch|query|rejection`. Fields that do
not apply are omitted except explicitly nullable identity/status fields below.
MCP carries structured content and a concise text fallback; CLI JSON carries the
same semantic envelope. User-controlled strings are bounded before serialization.
Sanitize complete admitted invocation metadata before shortening its preview;
cutting a token first must not evade redaction. Streamed output follows the
persisted projection contract instead of whole-string slicing.

### Run result

| Field | Type / meaning |
| --- | --- |
| `run_id` | UUID string; null for pre-admission rejection |
| `batch_id` | Optional parent UUID |
| `owner_id`, `owner_type` | Generation UUID and mcp/cli; identify control ownership, never authorize signalling by persisted PID |
| `state` | One of the states below |
| `exit_code`, `signal` | Nullable actual child status; never inferred from text |
| `reason` | Nullable stable reason code such as queue_timeout, deadline, owner_lost or spawn_failed |
| `started_at`, `completed_at`, `elapsed_ms` | Nullable UTC timestamps and nonnegative monotonic elapsed duration |
| `capture` | Object: state, observed_bytes, captured_bytes, reason, last_committed_cursor, terminal_persisted |
| `redaction` | Object: version, ready/pending/unavailable, coverage cursor |
| `index` | Object: not_requested/building/ready/quota_blocked/failed, coverage cursor, optional error |
| `expires_at` | Null before terminal completion; persisted terminal-time expiry afterward |
| `preview`, `next_cursor` | Bounded selected text and optional read continuation |
| `invocation_preview`, `cwd_preview` | Optional redacted display values; `metadata_omitted` identifies shortened/omitted metadata; full canonical cwd remains stored provenance |
| `error` | Nullable object with stable code and bounded safe message |

| Transition / state | Meaning |
| --- | --- |
| `not_started` | Rejected before run admission; null run ID and child status |
| `admitted` | Metadata committed, but process not yet started |
| `queued` | Admitted batch child waiting for an active slot |
| `running` | At least the first execution phase has spawned |
| `exited` | Execution finished normally; exit_code may be nonzero |
| `start_failed` | Admitted run could not start its first process |
| `timed_out`, `cancelled` | Deadline/control terminated an already-started execution; actual child status remains separate |
| `interrupted` | Owner was lost and terminal outcome is not durably known |
| `skipped` | Batch child did not start, with an explicit scheduling/deadline reason |

Normal transitions are not_started (terminal rejection), admitted → running →
terminal, or admitted → queued → running/terminal. Start failure has an ID;
pre-admission rejection does not. Queued cancellation produces cancelled with
null child status. Skipped/cancelled-before-spawn children keep their IDs.
Compiler subprocesses belong to the same run lifetime/capture, with phase
metadata; a failed compiler is an executed nonzero outcome, not an unstarted run.

Capture state is `not_started` before any process, `recording` after spawn,
`complete` only after drained bytes and terminal metadata commit, or `incomplete`
after lost capture/finalization. Thus unstarted/queued results have a meaningful
capture state. An incomplete run may still be running. Redaction/index failures
are separate: complete raw capture does not imply a usable default projection/index.

For execution-tool responses, MCP `isError=true` means rejection, start failure,
nonzero terminal execution, deadline/cancellation/interruption, or incomplete
capture. Healthy admitted/queued/running responses are not errors. For run-query
tools, `isError` reports the query operation's success, not the queried command's
exit status. A successful status/read of a failed command is a successful query
whose nested run still reports failure. Unsupported selectors, expired data,
unavailable redaction and denied operations are query errors.

CLI preserves a normal nonzero child exit. A zero child exit with incomplete
capture returns 74; admission/usage failures return 1/2, spawn errors 126/127,
timeout 124 and signal termination 128+signal. The envelope always retains actual
child status, disambiguating a child's own 74/124 from wrapper status.

### Batch result

A batch has its own UUID and state `queued|running|completed|cancelled|interrupted`.
Its envelope includes requested/effective/current concurrency, counts by child
state, `failed_count`, `incomplete_count`, and a child-list continuation rather
than an unbounded inventory. `completed` means every child is terminal, not that
all succeeded. A completed execution-tool response has `isError=true` if any
child failed, was skipped, timed out or has incomplete capture. A still-running
batch exposes any failures already observed and sets isError accordingly; its
state and recovery instructions must prevent accidental resubmission.

Store each child's original ordinal, command metadata and result. Pending,
skipped and failed children remain recoverable even when absent from the preview.
Keep a bounded batch manifest with terminal child summaries independently of
raw-log eviction. Active batch manifests/pending children are not evicted; a
completed manifest gets its recorded TTL from batch completion. If a child's
logs expire or are evicted first, listing still reports its status with
output_available=false, while read returns run_unavailable. Manifest/summary
bytes count against quota. Expired/evicted completed manifests become
batch_unavailable; individually retained runs remain accessible by run ID.
Initial batch queries run against available captured data within the response
budget; a yielded result supplies the exact batch search request needed later.
Later search uses current retained run/knowledge data, not an implied snapshot
of the original response.

## 4. Run operations and selectors

MCP tool name: `capy_runs`, with required `action`. CLI subcommands map to the
same operations except cancel (MCP-own runs only) and reset (CLI only).
CLI selectors/options are named flags formed by replacing underscores with
hyphens: run_id becomes --run-id, batch_id becomes --batch-id, and so on. Repeated
--query supplies queries; repeated --state supplies states. All query subcommands
support --json and inherited --project-dir. reset accepts --discard; it does not
implicitly accept run/batch selectors or enumerate other projects.

| Action | Inputs and result |
| --- | --- |
| `status` | Exactly one of run_id or batch_id; returns current envelope, including terminal failures; never starts work |
| `list` | Optional batch_id and states array of known state names; limit default 16, range 1–64, optional cursor; returns bounded entries plus continuation |
| `read` | run_id required; stream stdout/stderr/merged (default merged), raw false by default, encoding text/base64, optional cursor or start_line, max_bytes default 4,096 and range 1–65,536 |
| `search` | Exactly one run_id or batch_id; queries array of 1–8 nonblank strings (each ≤512 UTF-8 bytes), result limit default 5, range 1–20 |
| `cancel` | Exactly one run_id or batch_id owned by this receiving service; requests cancellation and returns current state plus cancel_requested |
| CLI `reset --discard` | Selected owner's disposable DB only; exclusive lifecycle lock; no key required, never automatic |

Reject unknown actions, unsupported flags/argument combinations, malformed IDs,
fractional limits and oversized cursor/query input. Limit zero never means
unbounded. All operations resolve the store through the selected owner; a cursor
cannot choose another path/project.

Batch child listing is `list(batch_id=...)`; no batch ID is passed as a run ID.
Search of a batch is an explicit bounded group search. Omitting queries for a
batch search reuses its original nonempty query array; with no stored queries,
it is invalid. `query_scope=batch|global` is valid only with batch_id and defaults
to batch. Global adds separately labelled knowledge results without persisting
logs there. Initial batch responses reproduce the caller's requested scope in
their recovery instructions. A run-scoped search cannot silently widen scope.

Read's `start_line` is a positive raw per-stream line number and requires an
explicit single stream; it is mutually exclusive with cursor. Encoding defaults
to text for normal reads and base64 for explicit raw=true. Reject raw/text and
sanitized/base64 combinations: raw retrieval remains byte-exact, and choosing
base64 alone never bypasses redaction. Merged raw
reads return labelled event fragments, not an unlabelled concatenation implying
program write order. Each fragment includes stream and raw byte/line anchors.

Cancel is idempotent for terminal targets and reports their existing outcome.
For active targets it acknowledges a request, not completed termination: poll
until cleanup/finalization is terminal. Cancelling a batch cancels pending children
without spawn and requests cancellation of active children. If natural completion
wins the race, preserve that result. A different owner returns `different_owner`
with its identity/type; do not signal a stored PID or contact another server
implicitly. Query/cancel operations never require an execution slot.

Core error codes include invalid_argument, run_unavailable, batch_unavailable,
cursor_invalid, cursor_stale, different_owner, capacity_exhausted,
pending_capacity_exhausted, owner_slots_exhausted, policy_mismatch,
storage_unavailable, wrong_key, cleanup_unverified, redaction_unavailable and index_quota_blocked.
Messages contain safe repair information, never keys/raw source/output. Expired
rows are unavailable immediately even before deletion. After metadata deletion,
do not invent whether a missing ID expired, was evicted or never existed.

## 5. Pagination, ordering and coverage

Cursors are opaque to clients, bounded to 1,024 encoded bytes and decoded with
strict length/type/range validation. Version 1 binds store UUID, operation,
selector, view/stream, redaction version where relevant, and a continuation
position. They are locators, not credentials; paths/SQL from cursor input are
never used. Reset creates a new store UUID, making old cursors stale.

List orders runs by immutable creation timestamp and UUID, or batch children by
ordinal. The first page's cutoff and filters are embedded in its cursor. Newly
created entries do not appear mid-pagination; mutable states can change and
evicted rows can disappear. There is no long-lived database snapshot.

Raw read continuation uses event sequence and byte offset. Default text reads
use finalized projection segments and raw anchors; they never sanitize slices
independently. `next_cursor` advances only over returned or explicitly redacted
coverage. Long lines may continue across pages with explicit continuation flags. Text pages
keep UTF-8 code points atomic: when max_bytes is smaller than the next code point,
a page may return that one code point (at most four UTF-8 bytes). The complete
serialized response budget remains strict; raw reads retain the exact max_bytes
limit. A code point split between raw events belongs to its first-byte event.
`end_of_available=true` means no more current bytes, whereas `eof=true` also
requires a terminal run and finalized coverage.

Merged default reads cannot advance past the oldest unresolved raw event in
either stream. If stdout has undecided secret/private state while stderr advances,
the merged cursor stops at that boundary and returns `redaction_pending`; a
stream-specific stderr read can still advance. Projection records are published
in raw-event order once resolved, so a later redaction decision cannot insert
text behind an already-issued merged cursor. Previews may select labelled safe
per-stream snippets without advancing a read cursor.

Search indexes only finalized sanitized coverage. Results carry raw read anchors,
index state and searched coverage. A bounded request may return partial matches
or building state; repeat search resumes stored index work. No search cursor is
required in v1: result limits apply to ranked hits, and exact recovery uses read.
`index_quota_blocked` is explicit, with raw/default reads still available when
their respective coverage is usable. No failed/partial query implies zero
matches in unsearched output.

## 6. Complete response budgets

Apply max_response_bytes to the serialized MCP result, including structured
content, fallback text, metadata, escaping and errors. CLI JSON or text plus its
own diagnostics obeys the same per-invocation bound. Host-added framing is measured
separately. Essential version/kind/IDs/state/status/coverage/error identifiers
are reserved first; optional metadata and previews shrink or disappear with
explicit omission flags. Canonical stored paths/programs are not mandatory
unbounded result fields. UTF-8 slicing must not corrupt serialization.

The same final bound applies to admission, compile/start errors, timeout,
background results, cancellation, query errors and giant single lines. Pagination
must make progress even when JSON/base64 expansion consumes the remaining budget.
Failure excerpts are hints; do not infer complete diagnosis or exit status from
their text. The deprecated preview limit never removes required fields.

## 7. Request handoff and cancellation

The execution owner/deadline owns the job from spawn. While a call is attached,
an explicit request cancellation is forwarded to the supervisor. At wait expiry,
the handler prepares a valid bounded running result, then atomically commits a
handoff under the run's control lock: cancellation and handoff have one serialized
winner. A request already cancelled at that boundary cannot win handoff. Remove
the request-cancellation observer as part of the transition before returning
from the handler. A racing observer that already started must check the same
latch and cannot cancel a handed-off job.

After handoff, normal request-context cancellation caused by handler cleanup does
not affect the job. Owner shutdown, execution deadline and explicit run cancel
still apply. This boundary is handler handoff, not client receipt or successful
pipe write. The current mcp-go request handler cancels its context before stdio
writes the result, so client delivery cannot be the service's control boundary.
Transport write failure follows capy's existing owner-cancellation path. If the
response is lost, callers recover by listing/status and do not retry execution.

Test before/after handoff cancellation, ordinary handler cleanup, serialization
failure before handoff, write failure after handoff and natural completion racing
handoff. No test may infer delivery from handler return.

## 8. Shared scheduling

One scheduler per owner counts every spawned/finalizing standalone or batch child
against max_active. A slot is held through pipe draining and terminal finalization.
Coordinators and queued children do not consume active slots. Standalone requests
start immediately or reject; they never silently queue.

Batch concurrency is validated in 1–8. Report requested concurrency and
`effective_concurrency=min(requested,max_active,child_count)` as a ceiling, plus
the current count. Require one dispatchable slot and atomically reserve pending
capacity for the whole child inventory before any spawn. Reject batches larger
than max_pending or unable to reserve their inventory. Eligible queued children
take precedence over new standalone requests; batches dispatch fairly in round
robin and preserve internal ordinal order. A batch's own concurrency ceiling
does not reserve otherwise free owner slots.

With max_active=4 and two standalone jobs, a batch of eight requesting eight
reports ceiling four, starts two and queues six. Total active work stays at four.
Standalone deadlines begin at spawn reservation. Serial batches have a common
admission-time deadline including queue time. Parallel children get their full
deadline at spawn, but undispatched children are skipped at admission+timeout
with reason queue_timeout. A parallel batch can therefore last roughly two timeout
periods plus bounded cleanup. wait_ms changes none of these rules.

Owner shutdown/batch cancellation remove pending children without spawn and
cancel active ones. Capacity errors include bounded active IDs, ages, remaining
deadlines, pending count and the applicable cancel action/list cursor. Queries
remain available under execution exhaustion. CLI recovery refers to its attached
process rather than promising cross-owner MCP cancellation. Never silently kill
an old/quiet job or grant a native-execution escape.

## 9. Store policy authority and retention

The store persists a versioned authoritative policy record: TTL, per-run capture
limit, retained-data limit, schema/redaction version and quota-accounting mode.
Every attached writer uses that record, not an independent config snapshot.
Initialization chooses and stores the supported dbstat/conservative accounting
mode; a process that cannot honor it fails admission rather than changing modes.
Owner-local response/deadline/capacity settings may differ by process.

Opening takes the permanent shared lifecycle lock before opening DB handles.
If configured store policy matches, attach normally. On mismatch, close handles
and release the shared lock, then try the lifecycle lock exclusively. If any
other user/supervisor still holds it, reject policy_mismatch with old/new revision
and stop/reload guidance. If exclusive acquisition succeeds, reopen/authenticate,
re-read policy, reconcile stale owners, validate/apply the revision transactionally
and perform bounded quota maintenance. Close/release, then retry the normal shared
open and revalidate; do not rely on an atomic flock upgrade/downgrade. Limit retries
under competing openers and report contention instead of spinning. New-store
initialization likewise takes exclusive lifecycle ownership.

A run records its admission policy revision and TTL. Completed expires_at values
are immutable; reads and later policy edits cannot renew or shorten them. The
recorded TTL applies when an admitted run becomes terminal, including interrupted
recovery. Lowering the store quota under quiescence can evict old completed runs
according to the existing eviction policy; report that separately from TTL.

TTL cleanup runs at admission, periodically while a store is open (60 seconds),
and in bounded shutdown maintenance. Reads enforce expiry even on an idle store.
Never evict active runs. Evict expired terminal runs first, then oldest completed
runs under pressure; TTL is a maximum, not a minimum retention guarantee.

Quota counts logical raw data, sanitized projection/checkpoints and metadata,
plus both FTS tables' allocated pages and shadow/posting pages. With dbstat
unavailable, charge all used DB pages once indexing exists as a conservative
additional index debit; report that mode. Check accounting transactionally on
append and index writes. Optional index work rolls back on quota exhaustion
without deleting raw logs to make room. Capture exhaustion after spawn marks
capture incomplete while the command continues draining/executing.

Physical main/WAL allocation is reported separately; this quota is not an exact
filesystem-byte limit. Checkpoint regularly and reclaim pages under quiescence.
Changing store policy requires all handles/supervisors to release lifecycle
ownership, so old-config MCP and new-config CLI cannot concurrently enforce
different project quotas. Test that scenario, TTL non-retroactivity and restart
under a reduced quota.
