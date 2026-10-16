# Design Review and Resolution: routine-command-capture

**Scope:** Full reread of design.md, implementation.md, tasks.md and review-resolution.md after the two supplied reviews were addressed.

**Initial assessment:** CONCERNS_FOUND — seven gaps identified before the follow-up fixes.

**Resolution status:** All seven findings below have been incorporated into the revised documents; see the resolution table. Independent re-review and implementation evidence remain separate.

**Mode:** Standard author re-review. This is not an independent assessment. The reviewer authored the documents and previous fixes.

**Documents:**

- [Design](design.md)
- [Implementation plan](implementation.md)
- [Tasks](tasks.md)
- [Previous dispositions](review-resolution.md)

**Review summary:** Seven findings: zero critical, two high, five medium, zero low.

Section/line/task references in the findings describe the reviewed snapshot.
The current contracts and resliced task numbers are linked in the resolution table.

All four documents were reread, including the cross-effects of the preceding
fixes. Source checks used current repository code and the installed mcp-go
v0.46.0 module. Local Markdown links/anchors and metadata/dependency checks for
all 13 tasks passed. Runtime tests were not run: the proposed feature does not
exist yet, and the findings below concern its contract and implementation plan.

## Findings

### P0 — Critical

(none)

### P1 — High

#### F1 · INCONSISTENT — request cancellation is defined relative to an unobservable delivery boundary

- **Section:** design.md §7, lines 357–362; §5, lines 177–188; implementation.md §7.
- **Confidence:** 10/10 — the installed transport cancels the request context before writing its response.
- **Description:** The design says request cancellation cancels a run before the
  running response is delivered, while later cancellation requires a run action.
  mcp-go's `request_handler.go` creates a request context and defers its
  cancellation; `stdio.go` calls `HandleMessage` and writes the returned response
  afterward. Ordinary successful handler completion therefore cancels that context
  before transport write. There is also no client-delivery acknowledgement at the
  execution-service boundary. Simply watching that context until delivery could
  kill every yielded job; detaching at an unspecified earlier point creates a
  different cancellation contract.
- **Evidence:** Installed `github.com/mark3labs/mcp-go@v0.46.0/server/request_handler.go:84`
  and `server/stdio.go:480`; the versioned source is available at
  [request handling](https://github.com/mark3labs/mcp-go/blob/v0.46.0/server/request_handler.go#L84)
  and [stdio response dispatch](https://github.com/mark3labs/mcp-go/blob/v0.46.0/server/stdio.go#L480).
  Capy's `cancelingStdio` additionally turns transport failures into owner
  cancellation; that existing path must remain distinct from per-call cleanup.
- **Recommendation:** Specify one atomic handoff point inside the service/handler,
  such as committing the running result and transferring cancellation ownership
  before returning. Detach request-cancellation observation at that point, retain
  owner/deadline cancellation, and treat lost responses as unknown outcomes
  recoverable by listing. Do not describe handler return or a successful pipe
  write as proven client receipt. Add race fixtures for explicit cancellation
  immediately before/after handoff, ordinary deferred context cancellation, and
  response-write failure. Include this contract in Task 5 before its implementation.

#### F2 · TECH_RISK — supervisor lifetime and quiescence locks are not connected

- **Section:** design.md §7, lines 364–389; §8 reset, lines 490–496; implementation.md Task 1 and Task 5.
- **Confidence:** 9/10 — current cleanup executes inside the owner process, and the design leaves supervisor lease ownership unspecified.
- **Description:** The existing lifecycle guard cannot execute cleanup after its
  own process is SIGKILLed. `runProcess` creates a separate process group and has
  no parent-death mechanism, so the proposed abrupt-owner-death guarantee needs an
  external supervisor or an explicitly equivalent mechanism. Task 1 correctly
  identifies that risk, but the later fixed owner-slot and lifecycle-lock design
  does not say which process retains those leases while a supervisor terminates,
  drains and reaps the group. If only the MCP/CLI process holds them, its death
  releases the locks while the group/supervisor may still be active. Reset or
  generation reuse can then infer quiescence too early.
- **Evidence:** [Lifecycle guard](../../../../internal/server/lifecycle.go),
  `StartLifecycleGuard`; [executor](../../../../internal/executor/executor.go),
  `runProcess` and `Setpgid`; the design defines reset admission solely through
  an exclusive lifecycle lock.
- **Recommendation:** Resolve the topology and ownership contract in Task 1,
  before storage/lifecycle interfaces are frozen: identify who spawns, owns the
  pipes, holds each lease, waits/reaps, persists completion and releases leases.
  A surviving cleanup supervisor must keep the relevant quiescence proof valid
  until the process group is retired; ordinary tool children must not inherit
  leases accidentally. Test owner SIGKILL while supervisor cleanup is deliberately
  delayed, concurrent reset, and slot reuse. Update Tasks 2/5/6 to consume that
  settled contract rather than treating these mechanisms as independent additions.

### P2 — Medium

#### F3 · INCOMPLETE — orphan reconciliation is only specified for the slot being reused

- **Section:** design.md §7, lines 377–380; §8 TTL, lines 550–568; Task 5.4.
- **Confidence:** 9/10 — no all-slot recovery pass or equivalent trigger is specified.
- **Description:** Reusing a slot reconciles that slot's old generation. The
  documents do not require sweeping other unheld slots. After a period of high
  concurrency followed by crashes, subsequent low-concurrency owners can keep
  reusing low-numbered slots while unfinished runs in higher slots remain
  logically active. Active runs cannot expire or be evicted, so those rows can
  retain quota indefinitely even though every corresponding process is dead.
- **Recommendation:** Require a bounded recovery scan over the fixed slot set
  during opening/maintenance, checking each slot nonblockingly and revalidating
  its generation while locked. Coordinate it with F2's actual cleanup leases.
  Test a stale high-numbered slot that is never selected for a new owner, and
  prove its runs become interrupted and eventually eligible for TTL/quota cleanup.

#### F4 · AMBIGUOUS — the shared store has no authoritative retention-policy revision

- **Section:** design.md §§8/10; implementation.md Task 6; config snapshot behavior in `server.NewServer`.
- **Confidence:** 9/10 — independent processes can load different configurations while opening the same physical store.
- **Description:** The documents promise a per-project quota and transactional
  arbitration across MCP and CLI writers, but do not identify which policy wins
  when their configuration snapshots differ. For example, an MCP server can keep
  a 512-MiB limit while a newly started CLI reads a reduced 128-MiB limit.
  Serializing writes does not reconcile those two admission/eviction thresholds.
  TTL changes also lack a rule for already-admitted or completed runs, despite
  returning a stored expiry time.
- **Evidence:** [Configuration loading](../../../../internal/config/loader.go)
  loads per process; [server construction](../../../../internal/server/server.go)
  retains the supplied config pointer. The design introduces no store-policy
  version or incompatible-owner admission rule.
- **Recommendation:** Define a store-authoritative policy revision or require
  quiescent policy changes and reject mismatched live owners. State when a run
  captures its retention policy and whether lowering TTL is retroactive. Keep
  owner-local execution capacity distinct from store-wide retention. Add a
  simultaneous old-config MCP/new-config CLI fixture and a config-change/restart
  fixture before calling quota behavior predictable.

#### F5 · INCOMPLETE — the new public API is still a list of capabilities rather than a complete contract

- **Section:** design.md §5, especially lines 190–253; §10 batch continuation; implementation.md Tasks 4/7/9.
- **Confidence:** 9/10 — multiple promised operations have no agreed request/result shape or lifecycle mapping.
- **Description:** `capy_runs` names status/list/read/search/cancel, but does not
  specify their arguments, required selectors, validated limits, cursor shape /
  encoding, batch-versus-run addressing, batch cancellation, or error envelope.
  Batch inventories must be paginated and recovered after a running response,
  while search elsewhere requires a run ID. The aggregate batch outcome and
  result of skipped/failed children are also unspecified. CLI flags and the
  handling of legacy `background=true` are unclear: CLI must remain attached,
  while the unqualified background paragraph says it returns running.
  `capture` also has no defined value for not-started/queued requests.
- **Recommendation:** Add one normative operations/types table shared by MCP and
  CLI, with request validation, state transitions, selector rules, terminal/error
  mappings and cursor semantics. Resolve merged-stream paging when one stream's
  redaction projection is pending and the other advances. Specify the CLI's
  supported background/wait options and the batch completion/query-retrieval
  path. This need not be implementation code or exhaustive JSON examples, but
  consumers should not have to invent public field names or semantics in Task 4.

#### F6 · INCOMPLETE — preserving run logs across key rotation has no executable operator path

- **Section:** design.md §8, lines 482–486; implementation.md §14, lines 315–319.
- **Confidence:** 9/10 — the available helper is not an operator-facing run-store command.
- **Description:** The reset/discard recovery path is now concrete, but the docs
  also promise guidance for preserving logs via backup-copy rekey. `capy encrypt`
  targets knowledge storage and `capy vault rekey` targets the vault. The new CLI
  surface contains reset but no run-store rekey operation, and no supported
  existing command sequence is supplied. An internal `sqliteutil.Rekey` function
  is not by itself something the operator can invoke.
- **Evidence:** [Knowledge encryption command](../../../../cmd/capy/encrypt.go)
  resolves only its knowledge target; [shared rekey primitive](../../../../internal/sqliteutil/sqliteutil.go)
  is a Go API. Tasks currently add documentation, not a runnable preservation path.
- **Recommendation:** Choose explicitly: add a narrowly scoped, quiescent
  `capy runs rekey` using the existing primitive and a defined credential contract,
  or document discard/reset as the supported v1 rotation behavior and remove the
  unsupported preservation promise. Do not smuggle a new command into the final
  documentation task. Verify the chosen workflow end to end with old-key logs.

#### F7 · STRUCTURE — Tasks 3 and 5 are no longer credible medium-sized atomic slices

- **Section:** tasks.md Task 3, lines 41–55; Task 5, lines 72–86; implementation.md §§5/7.
- **Confidence:** 9/10 — the preceding fixes added independent mechanisms without reslicing the plan.
- **Description:** Task 3 now combines streaming capture, whole-result budgeting,
  status changes, capture-failure handling and a new persisted streaming-redaction
  subsystem. Task 5 combines asynchronous ownership, cancellation, timeout wiring,
  legacy background migration, fixed lease slots, crash recovery and a shared
  batch scheduler. Both remain marked M and intended as atomic commits. Their
  complexity is materially larger than the skill's M slice guidance, regardless
  of whether file registrations are counted.
- **Recommendation:** Split these into integrated, testable paths: ordinary run
  capture/results; sanitized run retrieval; yielded run cancellation/ownership;
  stale-owner recovery; shared batch admission/scheduling. Update dependencies
  so public recovery uses completed redaction and crash/quiescence mechanisms,
  and retain the final whole-feature verification. Avoid merely retagging them L.

### P3 — Low

(none retained as separate findings)

## Clean areas and structure assessment

- The central separation of temporary encrypted runs from knowledge/vault data
  is coherent. MCP and CLI share execution/storage responsibility, with host
  routing separate from credentials and DB access.
- The previous HTTP early-return issue is addressed at the specification level:
  whole-request security precedes routine capture, which precedes HTTP-only
  evidence routing. Root/child mixed-command fixtures are named.
- The preceding redaction fix now defines streaming state, conservative overflow /
  EOF behavior, a persisted projection and fail-closed default views. No new
  privacy-boundary contradiction was confirmed during this pass. Implementation
  still needs the stated tests; this is not proof of a working sanitizer.
- Project-local storage, owner-aware CLI selection, sandbox admission testing,
  Claude guidance-only scope, exact timeout units and lazy indexing are explicit.
- Scheduler limits now have a coherent basic meaning: one owner-wide active
  ceiling, bounded pending batch admission, explicit effective concurrency and
  independent response waiting. No further concurrency-limit contradiction found.
- Command failure and capture failure are separated; lost-response retries are
  prohibited until status is known; raw recovery and ordinary sanitized views
  have different explicit purposes.
- Formal document requirements are present: assumptions, alternatives, scope,
  verification, task metadata, dependencies and graph. Local references resolve.

The reviewed set was structurally complete but not yet cleanly factored. At the pre-fix snapshot,
design.md is 837 lines, implementation.md 370, tasks.md 225, and the disposition
log 77. Length alone is not a finding; the problem is that public contracts,
compatibility rules and lifecycle details are distributed across sections and
then substantially repeated in implementation/tasks. The preceding fixes made
individual answers clearer while increasing the integration burden.

Organization recommended by the review and adopted in the follow-up:

1. Keep architecture, decisions, ownership and failure guarantees in design.md.
2. Put normative request/result/state/cursor/config contracts in one clearly
   identified section or a linked contracts.md, with one authoritative definition
   of each default and behavior. Either organization is acceptable.
3. Keep implementation.md focused on code seams, migration and verification.
4. Make tasks.md a concise set of actual commit-sized vertical slices; refer to
   contracts instead of repeating them. Keep review history in review reports.

## Initial readiness decision

The initial review required resolving F1/F2 before freezing the service/storage
interfaces, settling F3–F6 and reslicing F7 before the main feature work. Task 1 can be used as a deliberately
bounded investigation to prove the supervisor/lease design, but passing that
investigation is not equivalent to approving all later tasks.

The review initially recorded findings only. The user then explicitly requested
fixes; the follow-up below updates the contracts and plan without marking any
implementation complete.

## Resolution after user-authorized fixes

| Finding | Incorporated correction |
| --- | --- |
| F1 | [Contract §7](contracts.md#7-request-handoff-and-cancellation) defines an atomic handler handoff, detached request-cancellation observation, owner/write-failure behavior and race fixtures. It does not equate handoff with receipt. |
| F2 | [Design §5](design.md#5-supervisor-cancellation-and-quiescence) makes per-run supervision mandatory, assigns spawn/drain/reap/persist duties and deliberate lease-FD retention, and gates storage work on real delayed-cleanup/reset/reuse proofs. Unknown helper cleanup cannot certify quiescence. |
| F3 | [Owner registry](design.md#owner-registry-and-abandoned-runs) requires bounded scans of every fixed slot on open/maintenance, generation revalidation and recovery of never-reused stale slots. |
| F4 | [Contract §9](contracts.md#9-store-policy-authority-and-retention) defines an authoritative policy revision, quiescent mismatch/update protocol, fixed accounting mode and non-retroactive per-run TTL. |
| F5 | [Contracts](contracts.md) centralize exact inputs/results, CLI controls, run/batch selectors, query/cancel errors, aggregate state, cursor/merged-redaction coverage and scheduling. |
| F6 | [Rotation/reset](design.md#rotation-and-reset) explicitly supports discard/reset in v1; no preservation/rekey command is promised. Operator guidance names the existing-to-be-built reset path and data loss. |
| F7 | [Tasks](tasks.md) now contain 17 slices: bounded capture, sanitized recovery, yielded control, owner recovery, policy changes and serial/parallel batch paths are separate. Dependencies and implementation references were rewritten together. |

The architecture document now focuses on decisions, ownership and guarantees;
contracts.md is the normative API/config/state reference; implementation.md covers
code seams and verification; tasks.md tracks execution. Historical findings stay
in review documents rather than being restated as current unresolved requirements.

All tasks remain pending. The next implementation action is the deliberately
bounded Task 1 supervisor/lease proof; passing that gate is required before Task 2.
No runtime, race, sandbox or performance result is claimed by this document pass.

Final document checks passed: local links/anchors across all six documents,
17 task dependency/parallel declarations, matching implementation/task IDs,
all seven resolution records, and staged/unstaged whitespace checks.
