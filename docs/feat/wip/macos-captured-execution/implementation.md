# Implementation: native macOS captured execution

> Status: planned; no implementation or native proof completed
> Design: [design.md](design.md) · Tasks: [tasks.md](tasks.md)
> Evidence: [verification.md](verification.md)

## Execution rules

The first two tasks are risk-first gates. A successful compile or happy-path
process fixture cannot close them. Write proof evidence in `process-proof.md` and
`lease-proof.md` alongside these documents, including the chosen APIs, OS/kernel
and SDK versions, assumptions, failure cases, and actual commands/results. These
files are future task outputs, not evidence that already exists.

Before the first native test/build, use an explicitly operator-authorized
`routing.routine` exception for the required development commands, or another
approved capture route. Record its scope and removal. Keep security checks intact;
do not install a speculative backend merely to bypass the capture requirement.
This bootstrap is a prerequisite for runtime checks, not permission to change
the user's configuration now.

Use the existing Go toolchain/dependencies unless the verified native interface
requires a bridge. Apply dependency handling before recommending new APIs or
versions, and record private/deprecated interface risks. Do not guess a Darwin
`x/sys/unix` wrapper from its Linux counterpart. Every check uses FTS5 and synthetic
knowledge/vault keys where storage is involved, isolated configuration/data paths,
and appropriate temporary-path canonicalization on macOS.

Size estimates below count substantive mechanisms. Mechanical version propagation,
registrations, and test data do not create an additional subsystem. If a task
expands beyond its stated seam, split it before coding rather than conceal an L
task under an M label.

## Task 1: Prove Darwin process retirement

Primary seams: `internal/executor/wait_darwin.go`, new
`internal/executor/supervised_darwin.go`, and `supervised_other.go` build coverage.
Keep public macOS admission disabled during this proof.

- Specify the support floor/deployment target and native matrix; pin the source
  evidence to the APIs actually used → verify: evidence names every supported
  version and distinguishes source arguments from exercised kernels.
- Build the dedicated-helper process fixture around native spawn, retained
  direct-child identity, validated terminal observation, group cleanup, and
  bounded pipe drainage → verify: it handles real subprocesses and compiler/run
  phases without a plaintext command-output spool.
- Resolve the enumeration/publication and escaped-child re-entry obligations
  in [Q1](verification.md#q1-what-can-darwin-prove-about-process-group-termination)
  → verify: a causal proof plus adversarial fixtures; no success predicate based
  solely on signal delivery, EOF, or repeated empty-looking snapshots.
- Correct the relevant kqueue ESRCH/NOTE_EXIT waitability assumption as part of
  the bounded waiting path → verify: stopped/exiting children and cancellation
  cannot cause a presumed-immediate unbounded final wait.

Gate outcome: record the precise `darwin_process_group_v1` certificate, its
excluded descendants, unresolved kernel-stall limits, and tests. If no defensible
certificate is established, leave the backend disabled and return to the design;
do not mark this task done on rejection-only behavior.

## Task 2: Prove lease identity and startup exclusion

Primary seams: new `internal/runstore/lease_refs.go`, acquisition paths in
`owners.go`/`store.go`, and new `internal/execution/lease_darwin.go`. The exact new
filenames may follow package conventions; the acquisition boundary is mandatory.

- Represent borrowed lease references with acquisition provenance and controlled
  lifetime; production launch cannot receive arbitrary unproven files → verify:
  actual lock acquisition failure/wrong mode cannot create valid launch inputs.
- Implement a private synthetic helper exchange with causal offset observations
  in both directions for both roles → verify: same-inode independent descriptors,
  separate SH holders, swaps, aliases, replay/reflection, partial frames, and seek
  failures reject; genuine inherited references succeed through real exec.
- Own the exchange gate at the shared acquisition context, not inside one launch
  invocation → verify: concurrent attempts and a stopped helper resuming after
  timeout cannot alter another exchange. Gate ownership survives cancellation
  until the old actor is joined or barred from offset mutation.
- Exercise parent/helper death and reset/slot-claim races → verify: continuous
  exclusion through helper backend close and no contents/size or lock-mode change.

Gate outcome: `lease-proof.md` explains trusted acquisition versus identity proof.
Explicitly demonstrate that aliases of the same unlocked description pass an
identity-only test, then establish why production construction cannot supply it.
Do not weaken that distinction to make the negative fixture pass.

## Task 3: Preserve Linux history with scoped storage

Primary seams: new `internal/runstore/migrate.go` and `cleanup.go`, plus
`store.go` and `runs.go`. Policy-marker and diagnostic compatibility changes are
consequences of the same upgrade, not a new policy mechanism.

- Add authoritative immutable scope metadata and validation for known scoped
  states; current Linux admissions receive their existing scope → verify: store
  admission, load, completion, recovery, and ordinary Linux retention remain usable.
- Implement only the declared 8→9 transactional upgrade under authenticated
  exclusive lifecycle ownership on normal execution-owner attachment → verify:
  prior IDs, bytes, cursors, timestamps, counters, pending evidence, and quarantine
  survive. Supervisor and diagnostic paths never perform migration.
- Cover wrong keys, concurrent owners/readers, interruption, unexpected versions,
  quota/accounting effects, and DB replacement between lease attempts → verify:
  rollback/refusal is explicit and never recreates or discards a run store.
- Validate scope wherever retired evidence enables recovery/reuse/eviction;
  propagate row scope through list/diagnostics/batch inventory as required
  → verify: unknown scope fails closed and pending siblings still prevent reuse.

The migration must preserve unresolved state rather than run destructive cleanup.
Update existing schema/policy checks coherently; never change the canary/key
contract or move storage to a fallback path.

## Task 4: Expose and consume the version-2 recovery contract

Primary seams: `internal/runstore/types.go`/result assembly,
`internal/server/tool_runs.go`, `internal/execution/response.go`, and
`internal/hook/posttooluse.go`. Query-envelope version propagation is mechanical.

- Add essential cleanup scope/state to run and batch/list views; emit public
  version 2 consistently and normalize old saved result blobs from row metadata
  → verify: execute/status/read/list/search and batch recovery agree on proof and
  respect configured serialized-byte bounds.
- Keep cursor/redaction versions and content positions unchanged where compatible
  → verify: cursors issued before the upgrade still retrieve the same retained
  bytes and wrong-store/expired cursors retain their errors.
- Teach observation parsing explicit v1/v2 rules → verify: healthy v2 execution
  records evidence, malformed/unknown scopes and versions do not, and printed
  JSON cannot bypass structured validation or existing one-use rules.
- Preserve CLI exit and MCP failure semantics while exposing actual exit/signal
  alongside incomplete cleanup → verify: existing status/capture matrix and
  versioned protocol fixtures, including legacy compatible Linux responses.

## Task 5: Carry scoped proof through live Linux execution

Primary seams: `internal/executor/stream.go`, `internal/execution/protocol.go`,
`supervisor_linux.go`, and `stored_run.go`; service outcome mapping follows the
new type contract. Linux's process algorithm remains unchanged.

- Require known scope in startup, helper/backend selection, local outcomes, and
  store finalization; use private protocol version 2 → verify: missing/mismatched
  scope or protocol cannot start work or certify cleanup.
- Wire acquisition-proven references from Task 2 into the launcher boundary while
  retaining Linux fdinfo validation → verify: the existing inherited-lock and
  malformed-startup fixtures retain their meaningful rejection guarantees.
- Exercise ordinary, cancelled, failed-capture, lost-notice, and Rust compile/run
  execution → verify: Linux still retires adopted escaped descendants, preserves
  exit status, and distinguishes local proof from durable acknowledgement.
- Run real stored recovery/owner-death fixtures → verify: free locks never replace
  pending proof, and delayed backend close still excludes reset and reuse.

## Task 6: Integrate Darwin single-run capture

Primary seams: new `internal/execution/launch_darwin.go` and
`supervisor_darwin.go`, `stored_run.go` capability selection, and the proven
executor backend. Extract common launch/serve mechanics only where it avoids
duplicating established protocol/capture behavior; leave OS proof operations
explicit. Unsupported build stubs must remain disjoint.

- Connect the proven exchange, acquired references, authentication, pending
  persistence, process certificate, capture queue, and finalization
  → verify: real CLI and MCP runs produce recoverable encrypted stdout/stderr
  exactly once, with truthful nonzero exits and secret-safe default reads.
- Preserve startup/cancellation failure ownership, including a buffered start
  racing owner death and a late failed helper → verify: no premature gate/lease
  reuse, no command before pending persistence, and no silent retry.
- Select capability by verified OS/architecture/version support, not build success
  → verify: unsupported combinations reject before spawn with accurate diagnostics.

Gate outcomes from Tasks 1/2 are prerequisites. Development integration is not
release readiness; all public modes and release checks below must pass before merge.

## Task 7: Verify yielded, file, and batch execution on Darwin

Primary seams: `internal/execution/handoff.go`, `batch.go`, `scheduler.go`, and
`service.go`; adapters should continue delegating to that shared path.

- Verify `wait_ms`/legacy background handoff, execute-file policy and boilerplate,
  serial batches, and parallel batches sharing lease descriptions
  → verify: one execution, continued capture, stable owner identity, deadlines,
  fair bounded capacity, and recoverable child IDs.
- Separate short startup-exchange serialization from command concurrency
  → verify: parallel children run concurrently after startup and a stalled
  exchange cannot corrupt another child or let pending work bypass quarantine.
- Exercise cancellation/shutdown during admission, exchange, execution, and
  finalization → verify: joins and capacity accounting persist through cleanup;
  batch completion/expiry requires every child's valid scoped terminal state.
- Run compiled MCP/CLI fixtures and existing strict security/routing cases
  → verify: file access and credential/owner boundaries remain unchanged.

## Task 8: Make capability and native release checks visible

Primary seams: `internal/platform/doctor*.go`, setup/routing descriptions,
`.github/workflows/ci.yml`, and `.github/workflows/release.yml`.

- Separate backend capability, stored compatibility, and runtime activation in
  diagnostics → verify: unsupported hosts are visible without creating storage,
  acquiring owners, running probes, or claiming hook trust/activation.
- Add native Apple Silicon CI with the Task 1 matrix and retained Linux coverage
  → verify: compiled CLI/MCP execution and explicitly selected supervisor
  integration tests run, rather than being platform-skipped.
- Extend the macOS release smoke gate beyond version printing to synthetic
  capture/recovery → verify: the actual release artifact starts a command once and
  recovers expected output/status in an isolated owner.
- Update operator docs and any affected generated instructions on both sides
  → verify: artifact parity/idempotency, accurate scope/upgrade/rollback wording,
  and no trust, enforcement, or permission changes hidden in setup.

## Task 9: Final verification and documentation

Run `kk:test` for the proof/integration/migration matrix and required full FTS5,
race, vet/build, and generated-artifact checks. Include focused `fts5,integration`
supervisor/executor suites on both OS families. Use established isolated homes,
synthetic keys, and the documented macOS temporary-root convention.

Run required executor quality/performance benchmarks against a recorded immediate
pre-feature baseline. Preserve all results. Compare matching platforms/settings;
report Darwin capture measurements without pretending the old rejected path is
a performance baseline. Broaden testing only for failures, shared-code changes,
or a named unresolved risk.

Run `kk:document`, `kk:review-code` with Go input, and `kk:review-spec`. Record
remaining findings in these tasks/docs, not chat only. Release requires both proof
gates, every public mode, preserved Linux behavior/history, and native CI evidence.
No required check may be called passed because it was unavailable or skipped.
Remove any authorized temporary test exception when its agreed purpose ends.

Relevant final docs: `docs/reference/runs.md`, `README.md`, `docs/architecture.md`,
`AGENTS.md`, CLI help, setup/routing artifacts when changed, and this feature's
proof/verification records. Recommend architecture review of the new proof and
ownership boundaries if implementation changes their documented topology.
