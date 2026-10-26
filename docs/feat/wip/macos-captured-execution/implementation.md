# Implementation: native macOS captured execution

> Status: Tasks 1–12 implemented and verified; Task 13 final acceptance in progress
> Design: [design.md](design.md) · Contract: [contracts.md](contracts.md)
> Tasks: [tasks.md](tasks.md)

## Execution rules

The process-policy gate is Tasks 1–2; the lease proof gate is Tasks 3–4. Their
evidence files are `process-proof.md` and `lease-proof.md`. Task 2 now verifies
bounded observational cleanup, not certified group retirement. The user's
2026-10-22 acceptance supersedes that former gate while preserving its failed
candidate and native counterexample as historical evidence. API availability and
runtime observations still require verification. Public/storage/frame shapes are
fixed in [contracts.md](contracts.md), not delegated to experiments.

For Codex with routine enforcement, follow [bootstrap.md](bootstrap.md) before a
native test/build that cannot use capture. The operator authorizes exact scope;
record authorization, configuration and removal there. Claude routine guidance
and terminals/CI without Capy's enforcement need no such exception. Other host
permissions remain applicable. No exception has been applied in this design work.

Use existing dependencies/toolchain, FTS5 tags, synthetic knowledge/vault keys, and
isolated roots. Look up any new native interface against the pinned dependencies
and chosen OS matrix. Do not guess a Darwin wrapper from Linux. Gate failures must
be recorded and resolved, not hidden by skips, relaxed assertions, or automatic
fallbacks. No intermediate commit enables Darwin before the process-policy and lease gates; no
schema-changing release ships before preservation/compatibility work is complete.

These are thirteen bounded slices. Failure fixtures verify the same mechanism
rather than constitute separate production subsystems. Task boundaries separate
process waiting from observation, lease acquisition from transfer, fresh-store
constraints from migration, and private integration from public activation. This
lets each mechanism be verified independently. Tasks sharing type or adapter
files are sequential; no unresolved parallel ownership is delegated to the
implementer.

## Task 1: Select hosted support and prove bounded child waiting

Primary seams: `internal/executor/wait_darwin.go`, a dedicated child fixture,
and `process-proof.md`.

- Use the user-selected macOS 26-only Apple Silicon scope, deployment target
  26.0, and stable hosted `macos-26` label. Record verified API/toolchain needs,
  native OS/kernel/SDK builds, and the runner-reference retrieval date
  → verify: this single supported major can run in CI. Older-major compatibility
  and self-hosted provisioning are outside scope.
- Establish bounded direct-child terminal observation and exactly one reaper;
  correct the ESRCH/NOTE_EXIT waitability assumption → verify: stopped, exiting,
  and cancellation cases never presume an immediate blocking final wait.
- Record source-aligned or contract-backed evidence per the
  [evidence standard](design.md#evidence-standard) → verify: unmatched sources are
  disclosed and critical undocumented ordering is still an unresolved obligation.

Do not add a full group supervisor in this slice. The outcome is a justified
support matrix and usable bounded waiting primitive, with native fixtures.
Preserve the existing shared executor behavior; Task 1 does not refactor Linux
retirement. Its native waiter proof is separate from observational group cleanup in
Task 2 and full hosted/release verification in Tasks 12–13.

## Task 2: Implement bounded Darwin group observation

Primary seams: dormant `internal/executor/supervised_darwin.go`, a bounded SDK
`sysctl` inventory binding, and native integration fixtures. Follow the exact
[observational policy](design.md#darwin-process-lifecycle-and-observational-cleanup).

- Verify the public process-group sysctl on macOS 26 arm64, with SDK-native
  structures, bounded allocation/retries and strict response validation → verify:
  actual live/terminal leader/member observations; partial buffers, missing leader
  and unavailable/invalid observations fail. Keep private libproc fixture-only.
- Combine Task 1's waiter with new-session leadership, safe group signalling,
  the final post-drain/pre-reap inventory and the three-second cleanup budget
  → verify: normal/nonzero/signal/cancelled phases, live child with closed output,
  escaped pipe holder, capture-sink failure and observation/signal errors.
- Exercise compiler then runtime phases through the same dormant phase function
  → verify: no next phase before observation completion, and an unstarted later
  phase does not erase completed earlier work. Runtime preparation remains shared.
- Preserve actual command status, capture loss and observation completion as
  distinct facts. Never set the Linux-era `StreamResult.Retired=true` for a spawned
  Darwin phase. Use a private phase outcome with a separate observation-complete
  value; it is not serialized or consumed by current v1 execution/storage. Task 9
  adapts it to `CleanupEvidence` after Tasks 5–8 establish the shared contract.
- Retain and exercise the deterministic re-entry counterexample → verify:
  completion is described as `observed` with the accepted race limitation, not
  as proof of retirement. No post-reap/in-flight-join proof is required to finish
  this task. Actual errors/visible survivors cannot pass the observation policy.

Complete `process-proof.md` with native policy outcomes, API status and limits.
Public `PrepareSupervisor`/stream dispatch and `execution.Supported` continue to
reject Darwin in this slice. Shared stream/stub integration moves to Task 9 so
an observational result cannot leak into the old unqualified v1 retirement path.
This is a dormant successful backend, not rejection-only completion.

## Task 3: Construct acquisition-proven lease references

Primary seams: new `internal/runstore/lease_refs.go`, `owners.go`, and `store.go`.

- Encapsulate successful EX owner/SH lifecycle acquisitions, exact descriptors,
  borrowing and closure lifetime → verify: arbitrary/unlocked/wrong-mode source
  files cannot form production launch inputs.
- Bind the reference set to its store/slot and shared acquisition context; own an
  exchange gate there, across all aliases → verify: reference lifetime and gate
  identity survive concurrent borrowers and normal store shutdown.
- Preserve existing Linux inheritance and close-not-unlock rules → verify: real
  reset/slot probes stay excluded while references remain; no exchange protocol
  or platform backend is added yet.

Document the trusted-acquisition boundary in `lease-proof.md`. The next task
proves transfer identity, not lock mode.

## Task 4: Prove the common exchange and pre-start recovery

Primary seams: new portable `internal/execution/lease_exchange.go`, the launch
phase state machine, and private helper fixtures. Follow the exact
[frame contract](contracts.md#private-protocol-v2) on Linux and Darwin.

- Implement causal two-way offset changes for both roles → verify: inherited refs
  succeed; independent same-inode/shared holders, swaps, reflected/replayed frames,
  invalid markers, seek failures and wrong phases reject.
- Serialize cancellation with `start_write_attempted`; before that boundary kill
  the retained direct helper, observe/reap within the separate teardown budget,
  then release the gate → verify: a SIGSTOPped helper recovers without owner
  restart; a subsequent run can start.
- Acquire the gate before helper creation under one startup deadline, and return
  its ownership token exactly once → verify: gate-wait expiry spawns nothing and
  cancellation after ready cannot release a subsequent run's gate.
- Cover partial-start writes, late actors, signal/wait failure and unreapable
  helpers → verify: no false bounded-reap promise, orphaned waiter, premature gate
  release, or forced helper kill after possible authorization.
- Retain Linux fdinfo validation in addition to the common sequence → verify:
  portable fixtures on both platforms, unchanged lock modes/file bytes, and reset
  exclusion through backend close.

Complete `lease-proof.md`, including the identity-only unlocked-alias control.
Same-description identity cannot replace Task 3's actual acquisition provenance.

## Task 5: Enforce scoped proof on fresh stores

Primary seams: new shared `executor.CleanupEvidence`, `internal/runstore/cleanup.go`,
`store.go`, and `runs.go`; batch copy propagation follows the same transition.

- Implement [storage fields and state rules](contracts.md#storage-and-typed-evidence)
  for fresh schema-9 stores, with no column defaults → verify: SQL rejects omitted,
  NULL, unsupported scope/state and cross-scope pairs, and ordinary Linux capture/recovery stays usable.
- Centralize scope/state validity and policy-completion predicates; bind immutable scope to admission
  and synchronize child manifest proof in the existing transaction → verify:
  recovery/reuse/expiry accept Linux `retired` and Darwin `observed` under their
  distinct contracts, while refusing pending or inconsistent evidence. Do not
  name the shared predicate as proof that all processes are gone.
- Preserve proof after child-log eviction and aggregate homogeneous batch scopes
  → verify: terminal manifests remain interpretable without a live run row;
  comparisons/aggregates use one read snapshot and do not falsely reject a valid
  completion between separate queries.

This slice does not upgrade existing stores or publish new wire versions.
Existing-v8 attachment may remain an explicit incompatible-store refusal on this
unreleased branch until Task 6 lands; never recreate it.

To keep this slice buildable with the current Linux-only protocol v1, introduce
an explicitly named, temporary Linux adapter at the capture/completion boundary:
the known Linux backend supplies scope; the existing `Started`/`Retired` pair maps
  to the typed local evidence, and the new row supplies persisted evidence on reads.
This is not a default for unknown scope or a Darwin path. Task 8 replaces the
adapter with native scoped protocol-v2 outcomes and removes it. Do not pretend
that an unqualified v1 helper outcome already carries the new fields.

## Task 6: Preserve version-8 histories through migration

Primary seams: new `internal/runstore/migrate.go`, attachment in `store.go`,
policy markers, and batch-copy conversion.

- Implement the [transactional rebuild](contracts.md#upgrade-representation) under
  authenticated exclusive lifecycle ownership; explicitly backfill old Linux scope
  without leaving a DEFAULT → verify: both upgraded and fresh SQL reject omission.
- Preserve IDs, outputs, cursors, timestamps, quarantine and accounting; convert
  saved batch proofs by the specified retained/evicted-child rules → verify:
  unsupported or missing nonterminal evidence aborts rather than infers safety.
- Cover wrong key, busy owners/readers, interruption/rollback, replaced stores,
  unexpected versions, FK/index preservation and table-copy/WAL space exhaustion
  → verify: no plaintext spool, automatic eviction/discard or partial upgrade.
- Separate transient disk requirements from retained-data quota and refresh any
  actual accounting debit → verify: copied payload is not counted twice.

Only normal execution-owner attachment migrates. Supervisor/doctor paths refuse
or report compatibility without migration. New authenticated version-mismatch
errors name found/supported versions, rather than only path permissions.

## Task 7: Publish version-2 recovery views and hook parsing

Primary seams: result assembly in `internal/runstore`,
`internal/server/tool_runs.go`, `internal/execution/response.go`, and
`internal/hook/posttooluse.go`. Use [public field placements](contracts.md#public-version-2-envelopes).

- Expose `cleanup` at the specified run/batch/list/search locations and normalize
  old saved blobs from authoritative proof → verify: all views agree and essential
  proof survives bounded serialization.
- Keep compatible cursors/redaction formats and actual CLI/MCP command outcomes
  → verify: prior cursors recover identical bytes and incomplete cleanup does not
  erase actual exit/signal information.
- Implement explicit v1 and v2 healthy-envelope rules → verify: valid v2 pending,
  Linux retired and Darwin observed pairs; cross-scope/malformed/unknown rejection,
  no printed-JSON fallback, and
  unchanged one-use/security-first routing.
- Exercise null proof on unadmitted rejection and aggregate/evicted batch cases
  → verify: empty errors or missing children cannot manufacture cleanup completion.

## Task 8: Bind protocol-v2 proof through Linux execution

Primary seams: `stream.go`, `protocol.go`, `supervisor_linux.go`, and
`stored_run.go`; outcome adapters follow the fixed contract.

- Wire acquisition references and the common exchange into Linux launch, retain
  fdinfo checks, and require scope across startup/backend/row/outcome
  → verify: missing/mismatched frame fields cannot start or certify work.
- Remove unqualified wire `Retired`; distinguish local proof from durable proof
  in completion and known-outcome recovery → verify: lost notices/finalization
  failures, Rust phases, actual exit status and capture loss remain truthful.
- Remove Task 5's transitional Linux adapter; combine all phase proofs before
  whole-run finalization → verify: retired compiler plus runtime spawn failure,
  unresolved compiler cleanup, and failed final persistence cannot manufacture
  not-spawned or durable-retired outcomes.
- Preserve the Linux descendant algorithm → verify: escaped descendants,
  owner/helper death, delayed backend close and reset/reuse fixtures.

Task 8 depends on Task 7: shared response/type consequences are not concurrent work.

## Task 9: Integrate a private Darwin captured run

Primary seams: `launch_darwin.go`, `supervisor_darwin.go`, and the proven executor.
Extract common launch/serve mechanics only where required by the one frame sequence.

- Connect exchange, authentication, pending persistence, cleanup evidence, queue and
  store finalization through a private service fixture → verify: real encrypted
  stdout/stderr recovery, nonzero exit and secret-safe default reads.
- Connect Task 2's private phase outcome to shared `CleanupEvidence`, stream phase
  dispatch and disjoint platform stubs → verify: only the Darwin backend emits
  `observed`; normal and compile/run paths never use a Linux `Retired` shortcut.
- Exercise owner death with a buffered start frame and capture/finalization failure
  → verify: once-only execution and no premature gate/lease reuse.
- Keep production `Supported` rejection until this integration passes; public
  adapters/capability activation are the next slice.

## Task 10: Enable native CLI and ordinary MCP execution

Primary seams: backend capability in `stored_run.go`, shared service construction,
`cmd/capy/execute.go`, and `internal/server/tool_execute_run.go`.

- Activate only the Task 1 OS/architecture matrix after the process-policy/lease gates and
  private integration → verify: compiled CLI/MCP runs capture once and recover
  correct bytes/status through the installed contract.
- Preserve owner, credential, security and execution-directory selection
  → verify: unsupported hosts reject before spawn and no storage fallback appears.
- Keep adapter changes thin and shared → verify: CLI exit codes, MCP failure flags,
  response limits and actual runtime errors match the contract.

## Task 11: Verify yielded, file and batch execution

Primary seams: `handoff.go`, `batch.go`, `scheduler.go`, and `service.go`.

- Exercise `wait_ms`/legacy background, execute-file and serial/parallel batches
  through compiled fixtures → verify: one execution, continued capture, deadlines,
  stable owner identity and recoverable selectors.
- Separate startup exchange serialization from running concurrency → verify:
  parallel children run concurrently after ready; stalled startup cannot corrupt
  siblings or bypass pending capacity/quarantine.
- Exercise shutdown/cancel across startup/execution/finalization → verify: joins,
  capacity and batch expiry retain per-child scoped proof; file/policy boundaries
  remain unchanged.

## Task 12: Add diagnostics and reproducible native CI

Primary seams: platform doctor/setup wording, `Makefile`, and CI/release workflows.

- Report capability, the observational Darwin limit and authenticated schema
  compatibility without diagnostic writes/probes/trust claims → verify: scope
  wording does not claim all processes are gone; unsupported hosts and mixed versions are
  actionable under the [rollback procedure](design.md#rollback-and-mixed-versions).
- Add `make test-integration` and `make test-integration-race`: CGO, tags
  `fts5,integration`, packages `./internal/executor ./internal/execution`, uncached,
  serial package scheduling, and a 20-minute package timeout; race target adds
  `-race`. Both require the same synthetic keys/isolated roots as other checks.
- **Add** these integration targets to Linux CI while retaining existing full
  FTS5/race/vet checks; add native arm64 jobs for the already-exercisable matrix
  → verify: named suites execute rather than skip. Record the pre-feature Linux
  integration baseline and give discovered fixture flakiness an explicit fix and
  evidence entry; do not remove tags or assertions to claim success.
- Extend the actual macOS release artifact smoke gate to isolated capture/recovery
  → verify: invocation count, stdout/stderr and status, beyond `--version`.
- Update operator docs and affected generators plus committed copies → verify:
  parity/idempotency and no hidden permission, trust or enforcement changes.

The Makefile targets and CI coverage are implemented; their execution evidence
is recorded in [ci-proof.md](ci-proof.md).

## Task 13: Final verification and documentation

Run `kk:test` for the observational process-policy and lease proof gates, migration, every public mode, full required
FTS5/race/vet/build checks, the new integration targets on both platforms, and
artifact parity. Record actual OS/toolchain/runner builds. Unavailable evidence
is not a pass; contract-backed evidence uses the declared standard.

Run executor quality/performance comparisons against the immediate pre-feature
baseline with matching platform/settings. Report new Darwin measurements without
claiming that a previously rejected capture path is comparable. Broaden checks
only for a named risk, shared change or failure.

Run `kk:document`, `kk:review-code` with Go input, and `kk:review-spec`. Record
unfixed findings durably. Final docs include run operations/rollback, README,
architecture, AGENTS, CLI help, relevant generated artifacts, and proof reports.
Close the bootstrap authorization record and remove only its added exception
entries when their purpose ends, preserving unrelated user changes.

Release requires the implemented observation policy, proved lease ownership,
native all-mode acceptance, preserved Linux behavior/history, and CI execution
evidence. The accepted creation/re-entry limitation is documented behavior, not
an unresolved release gate. Unknowns needed for the promised observations or
lease/identity guarantees remain blockers; do not infer them from stress tests.
