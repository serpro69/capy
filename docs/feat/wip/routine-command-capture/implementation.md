# Implementation: routine command capture

> Status: Tasks 1–16 complete and verified; Task 17 pending
> Architecture: [design.md](design.md) · Contract: [contracts.md](contracts.md) · Tasks: [tasks.md](tasks.md)
> Reviews: [first dispositions](review-resolution.md) · [readiness corrections](readiness-review.md)

## Orientation

Read AGENTS.md and ADR-016/020/028/032/036 before changing lifecycle, storage,
retrieval or owner selection. All builds/tests use FTS5 and synthetic keys; tests
isolate configuration/data/cache homes. No dependency upgrade, persistent daemon
or public HTTP service is proposed. New dependencies/APIs require version-matched
lookup. Existing mcp-go request cancellation and stdio behavior must be tested
through the actual pinned transport.

The design defines topology; contracts.md owns inputs/results/defaults. This plan
names integration seams and verification without duplicating those definitions.
The final feature needs all slices; partial feature commits are not release
readiness and must not enable the routine gate early.

| Package / command | Responsibility |
| --- | --- |
| `internal/executor` | Runtime/process groups, pipes, phase capture and wait/reap |
| New `internal/execution` | Admission, supervisor control, handoff, owner capacity and result assembly |
| New `internal/runstore` | Encrypted runs, projection/checkpoints, policy/owners, quota and lazy FTS |
| `internal/sanitize` | Streaming redaction without changing existing whole-string callers |
| `internal/server/tool_execute*.go`, `tool_batch.go`, new `tool_runs.go` | MCP adapters using shared contracts |
| New `cmd/capy/{execute,runs,execute_supervisor}.go` | CLI adapters and hidden private helper; no MCP server construction |
| `internal/hook`, `internal/platform` | Credential-free recognition/denial, instructions and generated parity |

## Task 1: Prove supervisor ownership and lease transfer

Risk-first proof before storage/control interfaces are frozen.

Implementation evidence and supported-platform limits: [supervisor proof](supervisor-proof.md).

- Add a bounded event sink/control seam to executor types and a hidden supervisor
  entry point in the same executable. Transfer synthetic credentials/control and
  test leases only through deliberate private FDs → verify: command children
  inherit none of them; malformed/oversized/private-protocol input cannot spawn.
- Prove early/late output, parent liveness EOF, deadline/cancel and real process
  group retirement, including compiler phases → verify: all owned descendants
  and pipe readers terminate without stale-PID signalling.
- Retain inherited owner/lifecycle lock references in the helper through drain,
  persistence acknowledgement, reap and close → verify: owner SIGKILL with delayed
  helper cleanup cannot allow concurrent reset or generation reuse.
- Exercise broken IPC, partial startup, helper failure and forced process-tree
  loss; record supported OS mechanisms and any explicitly bounded failure limit
  → verify: no unknown cleanup outcome is reported as safe quiescence. This gate
  must pass before Task 2; do not replace it with a direct executor unit test.

## Task 2: Capture and recover one synchronous MCP run

Implementation boundaries and evidence: [Task 2 verification](task2-verification.md)
and [isolated code review](task2-code-review.md).

- Add minimal encrypted run/event metadata and schema version in runstore using
  sqliteutil. Resolve the selected owner/key without opening knowledge/vault →
  verify: empty/wrong credentials and bad storage fail before command side effects;
  invalid selected credentials cause no filesystem side effects.
- Add shared admission and supervisor startup with committed metadata/write probe;
  helper captures/finalizes one small run → verify: real MCP execution retains
  exact stdout/stderr bytes, actual status and one execution counter.
- Register initial capy_runs status and explicit raw read, with contracts' versioned
  envelope. Until Task 4, default text views return unavailable projection rather
  than exposing raw output → verify: recovery by ID and safe incomplete states.
- Resolve owner-local paths/config defaults and full-chain validation → verify:
  nested/subdirectory/worktree selection and sandboxed storage-admission harness.

## Task 3: Bound capture, results and failure outcomes

Evidence: [verification](task3-verification.md) · [isolated review](task3-code-review.md).

- Stream bounded events/queues/write batches and record per-stream byte order and
  observed merged order → verify: binary, UTF-8 split, stderr-only, giant-line and
  middle-marker fixtures beyond the old truncation boundary.
- Add a shared final response budgeter and explicit execution/capture state
  mapping → verify: encoded metadata/errors fit the contract on every return;
  exit 1, spawn failure, compiler failure, timeout and signal are distinct.
- Inject capture/write/finalization/queue failure after spawn → verify: the final
  command marker still occurs, pipes drain, memory/output remain bounded, retained
  prefixes are exact and terminal_persisted is truthful.

## Task 4: Recover sanitized output across arbitrary boundaries

Evidence: [verification](task4-verification.md) · [isolated review](task4-code-review.md).

- Add the bounded per-stream recognizer to internal/sanitize and persist encrypted
  projection/checkpoints/raw anchors through runstore → verify: every partition
  of synthetic secret/private patterns is suppressed with bounded state.
- Cover long values/whitespace, unfinished blocks, UTF-8 and conservative EOF /
  overflow → verify: raw hashes are unchanged and no prefix leaks into views.
- Implement raw versus projection cursors and merged coverage barriers from the
  contracts → verify: pages beginning in protected spans, one pending stream with
  another advancing, huge lines, reset/version-stale cursors and pagination progress.
- Expose default text previews/reads only from projection; failed redaction returns
  metadata/error, not a newly sanitized tail → verify: diagnostic, page and future
  FTS consumers cannot bypass that boundary.

## Task 5: Expose shared CLI execution and retrieval

Evidence: [verification](task5-verification.md) · [isolated review](task5-code-review.md).

- Add execute/runs Cobra commands with contracts' exact flags, selected-owner
  resolution, checked code-file input and CLI matched-ask handling → verify: no
  interface switch bypasses denied/approval-required code or file admission.
- Use shared execution/status/read/list and JSON/text renderers; CLI stays attached
  and rejects unsupported background/wait flags → verify: child/wrapper exit-code
  matrix, bounded responses and cross-interface reads.
- Exercise actual workspace-write CLI capture with home-cache writes unavailable
  → verify: owner-local success, excluded-owner pre-spawn failure, nested/worktree
  parity and no implicit alternate database.

## Task 6: Yield and cancel MCP runs through the real transport

Evidence: [verification](task6-verification.md) · [isolated review](task6-code-review.md).

- Implement contracts' atomic attached-to-owner handoff before handler return;
  detach the request-cancellation observer under the same control latch → verify:
  mcp-go's ordinary deferred request cancellation does not kill a yielded run.
- Test explicit cancellation on both sides of handoff, serialization failure,
  response-write failure and natural completion races → verify: one control
  outcome, correct owner cancellation and no inference of client receipt.
- Wire exact timeout/wait precedence and legacy MCP background mode, retaining
  capture after return; add own-run cancellation and standalone active slots →
  verify: a run longer than the old default retains its tail, capacity errors
  expose recovery, and status/read/cancel do not require execution slots.

## Task 7: Reopen safely after owner crashes

The reviewed implementation adds one fixed recovery.lock around scanning,
generation publication and admission, preventing concurrent scanners from treating
temporary scan leases as live-owner evidence. See [verification](task7-verification.md)
and [review corrections](task7-code-review.md).

- Integrate fixed owner slots/generations and shared lifecycle leases with the
  supervisor proof and real DB handles → verify: no premature reset/reuse while
  a cleanup helper remains alive; owner-file count is bounded under churn.
- Add bounded all-slot reconciliation at open/maintenance, not only allocation →
  verify: never-reused stale high slots become interrupted and receive recorded
  TTLs, while live replacement generations remain untouched.
- Reconcile abandoned index/work claims by the same generation proof and retain
  explicit errors for unproven cleanup → verify: no adoption/resume/PID signalling
  and no stale claim permanently preventing later access.

## Task 8: Make store policy consistent across processes

Evidence: [verification](task8-verification.md) · [isolated review](task8-code-review.md).
This slice publishes authoritative lower limits; completed Task 9 adds quota
accounting and bounded eviction to the exclusive policy-change phase.

- Persist authoritative policy revision/accounting mode and make every writer
  consume it → verify: old-config MCP/new-config CLI cannot apply different
  project limits concurrently.
- Implement quiescent policy-change admission via close/release/exclusive retry,
  with bounded contention handling; do not depend on atomic flock conversion →
  verify: live owners/helpers block changes and concurrent reopen/reset is safe.
- Snapshot each run's TTL/revision at admission; keep completed expiry immutable
  → verify: new policy affects new runs, not previous expiry or active-generation
  recovery; exact duration/presence/overflow validation matches contracts.

## Task 9: Retain, evict and explicitly reset runs

Implementation and verification: [Task 9 evidence](task9-verification.md).
Large program admission commits bounded preparation; large-log eviction uses
hidden tombstones and bounded row deletion. Append savepoints preserve raw
prefixes while committing cleanup progress independently of a rejected retry.

- Add transactional quota accounting, expired/old-completed eviction and append
  checks under the authoritative policy → verify: independent writers cannot
  spend the same capacity; active runs remain protected and mid-run exhaustion
  produces incomplete capture without killing execution.
- Enforce expiry at read time and bounded admission/periodic/shutdown maintenance;
  checkpoint/reclaim under documented quiescence → verify: no read refreshes TTL,
  idle reopen handles overdue logs and busy WALs are not lost.
- Add keyless CLI reset --discard under exclusive lifecycle ownership, deleting
  only known DB/sidecars while preserving locks → verify: wrong-key recovery,
  active-helper/reader refusal, symlink rejection, stale cursors and new-key capture.

## Task 10: Search run output lazily

Implementation and verification: [Task 10 evidence](task10-verification.md) ·
[isolated review](task10-code-review.md). Schema 7 adds bounded sanitized windows,
dual FTS, per-stream progress and allocated-page debit. Run search and intent
previews are available; batch selectors are validated without widening scope,
with Task 12 manifests and Task 13 global federation. Failed local claim
releases retry before later work; eviction refreshes debit between victims;
projection windows resume with indexed keyset seeks.

- Add bounded sanitized windows and dual FTS with raw anchors; extend retrieval's
  fixed allowlist and collect layer errors → verify: no unsearched no-intent run
  creates FTS rows, and middle failures map to exact readable spans.
- Add resumable per-run indexing/coverage claims and measured/conservative quota
  debit; roll back optional index batches that exceed quota → verify: both
  accounting modes, claimant death, partial/building/error responses and no raw
  log eviction solely for indexing.
- Add run/batch search adapters following contracts; previews use finalized
  projection → verify: exhausted knowledge-search budget cannot block recovery,
  and first/repeat search performance is measured under concurrent capture.

## Task 11: Capture execute-file runs with the shared lifetime

Evidence: [Task 11 verification](task11-verification.md) · [isolated review](task11-code-review.md).
Both execute adapters now share validation, bounded response preparation and
owner handoff. The supervisor applies existing file boilerplate after admission;
request serialization preserves non-UTF-8 physical-path bytes instead of replacing
them with a different filename. New file output uses run storage exclusively.

- Move execute-file to shared admission/supervision without weakening checked path
  or FILE_CONTENT behavior → verify: existing file-policy fixtures plus complete
  capture, compiler/runtime diagnostics and explicit status.
- Add wait_ms schema/help and continuation → verify: late output, cancellation,
  deadline and pagination behave like ordinary runs.
- Stop new file:* knowledge writes while preserving old rows/selectors → verify:
  only new run storage changes and all responses remain bounded.

## Task 12: Recover a serial batch through MCP

Evidence: [Task 12 verification](task12-verification.md) · [isolated review](task12-code-review.md).
Serial calls now use atomic run/manifest admission, shared supervision and bounded
MCP/CLI recovery. Task 13 completes parallel capture and global federation.

- Add batch/child metadata, all-command preflight and serial child capture through
  the shared execution service → verify: a denied final command prevents every
  side effect; serial admission-time deadline and skipped children are correct.
- Expose batch status/list/read/search addressing, aggregate results, query recovery
  instructions and yielding → verify: late recovery shows every ordinal/state,
  including failures omitted from the bounded immediate result; child-log eviction
  retains manifest status with output_available=false until manifest expiry.
- Keep parallel migration as Task 13's required completion before releasing the
  batch replacement or enabling routing. This slice's real MCP fixtures prove
  the serial path without presenting partial migration as feature completion.

## Task 13: Schedule parallel batches within owner capacity

Evidence: [Task 13 verification](task13-verification.md) · [isolated review](task13-code-review.md).
Parallel and serial paths share owner scheduling and captured run storage. Global
queries add separately labelled knowledge results through MCP and CLI.

- Integrate bounded pending inventory and fair owner-wide scheduling, reporting
  requested/effective/current concurrency → verify: eight children alongside two
  standalone jobs respect max_active=4 and no admission bypasses the ceiling.
- Preserve serial behavior while adding parallel spawn deadlines/dispatch cutoff
  and pending cancellation → verify: full-queue rejection is atomic, cancelled
  pending children never spawn, and ineligible children do not reserve free slots.
- Complete batch replacement, bounded inventories, lazy scoped/global queries and
  source migration → verify: all batch paths use runs rather than new batch:*
  knowledge sources, with explicit provenance and recoverable statuses.

## Task 14: Recognize routine commands and captured programs

Evidence: [Task 14 verification](task14-verification.md) · [isolated review](task14-code-review.md).
The classifier/config are implemented with the gate inactive. Code-file inspection
requires a checked owner-aware reader and rejects ambiguous launch contexts;
Task 15 supplies its production hook integration.

- Add bounded literal catalogue/wrapper parsing on the existing shell scanner,
  initially without enabling the gate → verify: exact issue #132 commands,
  quoted mentions/heredocs, dynamic forms, operator exceptions and scanner limits.
- Recognize actual capture-CLI arguments and inspect underlying program policy
  → verify: lookalikes and captured siblings cannot exempt uncaptured routines
  or bypass deny/matched ask rules.

## Task 15: Enable strict Codex routing and synchronize artifacts

Evidence: [Task 15 verification](task15-verification.md) · [isolated review](task15-code-review.md).
Codex now applies security, mandatory routine capture and HTTP-only routing in
that order. Captured CLI code-file inspection shares the CLI reader and owner
resolver; modern availability observations and generated/runtime instructions
follow the capture contract. Claude routine enforcement remains out of scope.

- Enforce whole-request security → mandatory routine capture → HTTP-only routing
  → verify: root/child mixed HTTP/routine cases with present/absent/expired/broken
  evidence cannot allow or suggest native fallback; routine denial consumes none.
- Preserve HTTP-only contracts and define healthy-running execute evidence exactly
  as designed → verify: unstarted/failure states and polling cannot create/refresh
  observations, and root/child identity/TTL behavior remains correct.
- Synchronize tool catalogue/counts, hook/runtime guidance, generators and committed
  artifacts; Claude wording stays guidance-only → verify: equality/idempotency /
  drift guards, nested owners, aliases and external-hook preservation.

## Task 16: Document and diagnose the complete operator workflow

Evidence: [Task 16 verification](task16-verification.md) · [isolated review](task16-code-review.md).
Read-only inspection is shared by CLI/MCP diagnostics; session bytes are counted
once at owner completion. The durable [run guide](../../../reference/runs.md)
owns runnable recovery, policy, rotation and rollback instructions. Review also
corrected the documented legacy preview limit so both execution adapters apply it
without restricting capture or explicit retrieval.

- Extend stats/doctor with safe statuses, policy/capacity and accounted/physical
  usage without creating a store or claiming activation → verify: CLI/MCP parity,
  secret-free output and bounded recovery information.
- Update README, architecture, CLI help, AGENTS and predecessor follow-up pointers;
  name reset/data loss before and after knowledge rotation → verify: every
  published operation exists, no unsupported run-rekey promise remains, and
  version reload/rollback/owner-policy changes have runnable instructions.

## Task 17: Verify installed acceptance and release readiness

- Record trusted actual Codex/MCP/sandboxed-CLI behavior and Claude guidance-only
  scope → verify: one execution, mixed-policy denial, owner/worktree selection,
  unavailable paths, exceptions and external hooks.
- Record synthetic issue-like logs plus a real project test run, not pretend
  network installs → verify: returned/captured/raw bytes, statuses, hashes,
  recovery/expiry and all handoff/supervisor/policy race cases.
- Run $kk:test with FTS5, synthetic keys and isolated homes; focused race and
  subprocess checks → verify: full suite passes without leaked descendants,
  plaintext spools or unexplained failures.
- Run executor/retrieval performance checks and make bench-quality with baseline
  comparison → verify: redaction/capture memory, no-search and first/repeat search
  CPU/storage/latency, index/WAL growth and retrieval quality are recorded.
- Run $kk:document, $kk:review-code with Go input and $kk:review-spec against the
  whole feature → verify: findings resolved or written as actionable dispositions;
  neither drafting nor hook registration alone closes #132.

## Independent follow-ups

Vault fixture isolation remains in [codex-hooks tasks](../codex-hooks/tasks.md#follow-ups-outside-feature-completion):
isolate XDG_CONFIG_HOME in the three named fixtures when taken. New fixtures here
must isolate homes now. Historical PR #135 / toolbox #170 / #29 tracking and the
predecessor's absent toolbox-enhancement-issue.md link remain independent; restore
that artifact or use the actual issue link during that tracking correction.

A hard physical disk ceiling would require a separate storage decision; current
contracts explicitly bound accounted data and handle disk-full failures. PTY,
cross-owner job control, persistent jobs and run-log-preserving rekey are excluded,
not unfinished features hidden behind the final documentation task.
