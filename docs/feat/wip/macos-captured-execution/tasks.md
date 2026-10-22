# Tasks: native macOS captured execution

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Evidence: [verification.md](verification.md)
> Status: planned — design sections approved; implementation and native proof pending
> Created: 2026-10-22
> Not Doing: Intel support, complete Darwin detached-descendant cleanup, best-effort descendant tracker, VMs, persistent services, privileged helpers, restricted entitlements, other OS ports, PTY/stdin, persistent jobs, cross-owner cancellation, knowledge/vault migration, run rekey, automatic discard/downgrade, trust/security changes

Proof gates are required functionality, not scope exclusions. No code or native
tests were produced during design. Runtime checks need the explicitly authorized
bootstrap described in the implementation plan. The macOS version floor remains
an explicit Task 1 decision; it must be settled before enabling the backend.

## Task 1: Prove Darwin process retirement

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Strategy:** Risk-First
- **Docs:** [process proof plan](implementation.md#task-1-prove-darwin-process-retirement)

### Subtasks

- [ ] 1.1 Establish the authorized native test route, minimum macOS/deployment target, and Apple Silicon matrix; verify actual API availability and record pinned evidence in `process-proof.md`.
- [ ] 1.2 Implement isolated Darwin process fixtures around `wait_darwin.go`, a dormant `supervised_darwin.go`, and disjoint unsupported stubs → verify: retained leader identity, bounded terminal observation, compiler/runtime phases, and actual pipe drainage.
- [ ] 1.3 Resolve fork/spawn publication and group re-entry before defining the retirement certificate → verify: source argument plus native adversarial cases, including children that close output but remain alive.
- [ ] 1.4 Correct the directly relevant kqueue waitability assumption → verify: exiting/stopped children and cancellation do not cause an unbounded final wait based only on ESRCH/NOTE_EXIT.
- [ ] 1.5 Record the certificate and scope limits; if unproved, keep admission disabled and revise design → verify: rejection-only behavior is never marked successful implementation.

## Task 2: Prove lease identity and startup exclusion

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Strategy:** Risk-First
- **Docs:** [lease proof plan](implementation.md#task-2-prove-lease-identity-and-startup-exclusion)

### Subtasks

- [ ] 2.1 Add acquisition-proven reference construction in `internal/runstore` around the existing owner/lifecycle locks → verify: unlocked/wrong-mode source descriptions cannot become production launch inputs.
- [ ] 2.2 Build the private Darwin shared-offset exchange in `internal/execution/lease_darwin.go` and a real helper re-exec fixture → verify: causal two-way identity, same-inode independent/shared holders, role swaps, replay/reflection, and malformed frames.
- [ ] 2.3 Give the shared acquisition context cancellation-safe exchange-gate ownership → verify: concurrent starts and a timed-out/stopped helper resuming later cannot corrupt another exchange.
- [ ] 2.4 Exercise coordinator/helper death, concurrent reset, and slot claims through every exchange boundary → verify: continuous protection until backend close and unchanged permanent-file contents/size and lock modes.
- [ ] 2.5 Record `lease-proof.md`, including identity-only acceptance of the same unlocked description and the separate acquisition invariant; keep unsupported alternatives outside automatic fallback.

## Task 3: Preserve Linux history with scoped storage

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [storage upgrade plan](implementation.md#task-3-preserve-linux-history-with-scoped-storage)

### Subtasks

- [ ] 3.1 Add authoritative immutable cleanup scope and centralized supported-state validation in `internal/runstore`; retain current Linux admission/completion behavior → verify: scoped store operations and unknown-scope rejection.
- [ ] 3.2 Implement authenticated exclusive-lifecycle 8→9 migration on normal owner attachment, with matching policy markers → verify: IDs, logs, cursors, states, timestamps, accounting, pending proof, and quarantine survive.
- [ ] 3.3 Cover wrong keys, crash/rollback, busy readers/owners, replaced stores, capacity effects, and unsupported versions → verify: no recreation, automatic discard, or diagnostic/supervisor migration.
- [ ] 3.4 Apply scope validation to recovery/reuse/retention and batch child evidence → verify: valid scoped terminal history is reusable while unknown/pending siblings preserve quarantine and evidence.

## Task 4: Expose and consume version-2 recovery results

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [public contract plan](implementation.md#task-4-expose-and-consume-the-version-2-recovery-contract)

### Subtasks

- [ ] 4.1 Publish essential cleanup scope/state and version-2 run/batch/query results through shared result assembly and `tool_runs.go` → verify: all recovery views agree, including normalized historical blobs and bounded responses.
- [ ] 4.2 Preserve compatible cursor/redaction formats and CLI/MCP status behavior → verify: pre-upgrade cursors recover identical retained bytes and incomplete cleanup retains actual exit/signal evidence.
- [ ] 4.3 Update `healthyRunEnvelope` for explicit v1/v2 rules → verify: correct scoped v2 observations, unknown/malformed rejection, no output-text fallback, and unchanged one-use routing semantics.

## Task 5: Carry scoped proof through live Linux execution

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 4
- **Docs:** [live proof plan](implementation.md#task-5-carry-scoped-proof-through-live-linux-execution)

### Subtasks

- [ ] 5.1 Require known scope across `StreamResult`, private protocol v2, backend/admission binding, and store completion → verify: missing/mismatched proof cannot spawn or certify cleanup.
- [ ] 5.2 Wire acquisition-proven references into launch while preserving Linux fdinfo checks → verify: real inherited leases and meaningful malformed-start rejection.
- [ ] 5.3 Preserve Linux execution/capture/compile behavior through scoped outcomes → verify: escaped descendants, cancellation, capture loss, lost terminal IPC, and local-versus-durable proof cases.
- [ ] 5.4 Run stored recovery and delayed-close fixtures → verify: owner/helper death never releases reset/reuse on lock availability alone.

## Task 6: Integrate Darwin single-run capture

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 4, Task 5
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Darwin integration plan](implementation.md#task-6-integrate-darwin-single-run-capture)

### Subtasks

- [ ] 6.1 Connect the proven executor to Darwin launch/serve helpers and shared capture/storage; extract common mechanics only where useful → verify: real CLI/MCP encrypted stdout/stderr recovery and nonzero exits, once per request.
- [ ] 6.2 Enforce complete identity/binding/start/pending ordering and preserve failed-helper ownership → verify: owner-loss/start races, incomplete capture, and no premature gate/lease reuse.
- [ ] 6.3 Enable capability only for the proven OS/architecture/version matrix → verify: supported native execution and accurate pre-spawn rejection elsewhere; compile success alone is insufficient.

## Task 7: Verify yielded, file, and batch execution

- **Status:** pending
- **Depends on:** Task 6
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [public mode plan](implementation.md#task-7-verify-yielded-file-and-batch-execution-on-darwin)

### Subtasks

- [ ] 7.1 Exercise compiled MCP fixtures for `wait_ms`/legacy background, execute-file, and serial/parallel batches through shared service code → verify: one execution, retained output, correct deadlines and recovery selectors.
- [ ] 7.2 Verify startup-gate serialization against scheduler concurrency and capacity → verify: children run concurrently after exchange; stalled actors cannot corrupt siblings or bypass quarantine.
- [ ] 7.3 Exercise shutdown/cancel during admission, startup, execution, and finalization → verify: joins/reservations persist through cleanup and batch expiry requires scoped terminal children.
- [ ] 7.4 Preserve file/credential/owner/security boundaries in CLI/MCP and routing fixtures → verify: native support does not broaden permissions or alter owner selection.

## Task 8: Expose capability and enforce native release checks

- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [diagnostics and rollout plan](implementation.md#task-8-make-capability-and-native-release-checks-visible)

### Subtasks

- [ ] 8.1 Add truthful backend capability and upgrade reporting to CLI/MCP doctor → verify: read-only behavior, no storage creation/owner claims/probes, and no trust/activation claims.
- [ ] 8.2 Add the proven Apple Silicon matrix to CI and retain Linux checks; select supervisor integration tags explicitly → verify: native tests execute rather than skip.
- [ ] 8.3 Exercise the actual macOS release artifact with isolated synthetic capture/recovery → verify: command count, retained bytes, and status, beyond `--version`.
- [ ] 8.4 Update operator docs and affected setup/routing generators plus committed counterparts → verify: whole-file/idempotency checks and no hidden trust/enforcement changes.

## Task 9: Complete release verification and documentation

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [final verification plan](implementation.md#task-9-final-verification-and-documentation)

### Subtasks

- [ ] 9.1 Run `kk:test` for proof, migration, full FTS5/race/vet/build, focused integration, and artifact parity checks on declared platforms; record actual OS/toolchain versions and unavailable evidence explicitly.
- [ ] 9.2 Run required executor quality/performance comparisons against a recorded immediate baseline; use matching platforms/settings and distinguish new Darwin capture measurements from comparisons.
- [ ] 9.3 Run `kk:document` for run workflow, compatibility/rollback, architecture, help, AGENTS, and proof records; remove the temporary test exception according to its authorization.
- [ ] 9.4 Run `kk:review-code` with Go input and `kk:review-spec`; durably resolve or track findings before release, and revisit architecture review if ownership topology changed.
- [ ] 9.5 Confirm both proof gates and every public mode passed native acceptance, Linux behavior/history is preserved, and capability claims match tested support; only then mark the feature complete.

## Dependency Graph

```text
1 ─┐
   ├─→ 3 ─┬─→ 4 ─┐
2 ─┘      └─→ 5 ─┴─→ 6 ─→ 7 ─→ 8 ─→ 9
1,2 ─────────────────→ 6
1..8 ──────────────────────────────→ 9
```

Parallel markers describe dependency freedom only. They do not authorize
overlapping edits or delegation. Tasks 4/5 share some type/adapter consequences;
coordinate ownership before any parallel implementation, or execute sequentially.
