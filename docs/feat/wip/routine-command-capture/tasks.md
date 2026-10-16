# Tasks: routine command capture

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Status: pending — draft awaiting design review
> Created: 2026-10-16
> Issue: [#132](https://github.com/serpro69/capy/issues/132)
> Not Doing: automatic shell rewriting, arbitrary-script recognition, PTY/stdin, cross-owner cancellation, daemon survival, trust mutation, knowledge/vault migration, #8 durable retention, remote upload, toolbox migration, universal platform support

## Task 1: Prove owner-bound execution and capture delivery

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — resolve process ownership before exposing yielding
- **Docs:** [Implementation §3](implementation.md#3-task-1--prove-owner-bound-execution-and-capture-delivery)

### Subtasks

- [ ] 1.1 Add a bounded event-sink seam in `internal/executor/{types,executor}.go` → verify: existing callers and `TestStreamedExecutionCapturesLateOutput`.
- [ ] 1.2 Exercise a real command across a response-wait boundary in an owner harness → verify: early and late markers are captured once, in per-stream order.
- [ ] 1.3 Prove descendant cleanup under deadline, cancellation, owner shutdown, parent loss and SIGKILL; add per-run supervision if required → verify: `TestOwnedExecutionLifecycle`, with no stale-PID signalling or orphan reader.
- [ ] 1.4 Record actual supported OS/lifecycle evidence and update the mechanism in the design before Task 5 → verify: no unresolved owner-bound acceptance failure.

## Task 2: Execute and recover a synchronous MCP run

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation §4](implementation.md#4-task-2--capture-and-recover-one-synchronous-mcp-run)

### Subtasks

- [ ] 2.1 Add encrypted metadata/event storage in `internal/runstore` using reviewed schema and existing `sqliteutil` primitives → verify: `TestRunStoreEncryptedAdmission`, no plaintext or knowledge/vault DB creation.
- [ ] 2.2 Add shared admission/spawn/finalization in `internal/execution` and adapt `handleExecute` → verify: `TestExecuteRunRoundTrip` preserves exact emitted bytes and status.
- [ ] 2.3 Register initial `capy_runs` status/read and new run configuration/cache resolution → verify: real MCP execution and recovery, strict config/key precedence and project isolation.

## Task 3: Bound large results and report capture failures

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation §5](implementation.md#5-task-3--bounded-large-output-and-failure-semantics)

### Subtasks

- [ ] 3.1 Implement bounded sequencing/writing and byte/line metadata → verify: multibyte, binary, huge-line, stderr-only and middle-marker fixtures.
- [ ] 3.2 Add complete serialized-response budgeting to every execution result branch → verify: `TestExecutionResponseBudget`, including missing intent and escaped metadata.
- [ ] 3.3 Replace migrated soft-failure handling with explicit command/capture states → verify: exit 1, signal, timeout, start failure and pipeline semantics.
- [ ] 3.4 Inject append, queue, quota and finalization failures; retain bounded draining → verify: `TestCaptureFailureDoesNotKillCommand` and raw recovery despite FTS failure.

## Task 4: Expose CLI execution and retrieval

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Implementation §6](implementation.md#6-task-4--cli-execution-and-retrieval-parity)

### Subtasks

- [ ] 4.1 Add `cmd/capy/{execute,runs}.go` using shared service/types and owner-aware project resolution → verify: CLI/MCP cross-interface run reads and nested/worktree fixtures.
- [ ] 4.2 Add checked code-file admission and exact ask/deny diagnostics → verify: fallback cannot weaken command/file policy.
- [ ] 4.3 Implement bounded text/JSON and typed exit mapping → verify: command failure, capture-only failure, timeout, signal and start-error matrix.
- [ ] 4.4 Add paginated text/raw reads and listing → verify: binary/base64 page reconstruction and raw line/event anchors.

## Task 5: Support long-running MCP jobs and owner recovery

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 4, Task 10
- **Docs:** [Implementation §7](implementation.md#7-task-5--long-running-mcp-jobs-and-owner-recovery)

### Subtasks

- [ ] 5.1 Separate owner lifetime, execution deadline and response wait in `internal/execution` → verify: a run exceeding 30 seconds retains output after its running response.
- [ ] 5.2 Add same-owner cancellation and explicit cross-owner error → verify: `TestRunCancellationOwnership` never signals a persisted PID.
- [ ] 5.3 Preserve legacy background semantics with continued capture and argument conflict checks → verify: `TestExecuteBackgroundCompatibility`.
- [ ] 5.4 Add held owner locks, interrupted recovery and ordered server/CLI shutdown → verify: installed lifecycle matrix and concurrent live-owner preservation.

## Task 6: Enforce run retention and quota

- **Status:** pending
- **Depends on:** Task 4, Task 5
- **Size:** M
- **Can run in parallel with:** Task 7, Task 10
- **Docs:** [Implementation §8](implementation.md#8-task-6--retention-and-quota-during-real-execution)

### Subtasks

- [ ] 6.1 Add transactional quota admission/append accounting to `runstore` → verify: independent CLI/MCP writers cannot overspend the remaining logical quota.
- [ ] 6.2 Evict expired/old completed runs while protecting active runs → verify: `TestRunQuotaPressure`, including active-only exhaustion and explicit unavailable reads.
- [ ] 6.3 Enforce completion-based TTL during reads and bounded automatic maintenance → verify: injected-clock expiry, no TTL refresh on reads and crash/idle reopening.
- [ ] 6.4 Add safe checkpoint/reclaim and logical/physical accounting → verify: no active WAL loss, silent wrong-key recovery or durable-store changes.

## Task 7: Search captured output and select previews

- **Status:** pending
- **Depends on:** Task 4, Task 5
- **Size:** M
- **Can run in parallel with:** Task 6, Task 10
- **Docs:** [Implementation §9](implementation.md#9-task-7--run-scoped-search-and-useful-previews)

### Subtasks

- [ ] 7.1 Add bounded sanitized FTS windows with raw anchors/high-water state in `runstore` → verify: `TestRunSearchAnchors` recovers middle-of-log failures.
- [ ] 7.2 Extend retrieval's fixed table allowlist and propagate layer errors → verify: existing corpora remain unchanged and search failures are explicit.
- [ ] 7.3 Add MCP/CLI run-scoped search and intent/failure previews → verify: knowledge-search throttling cannot block run recovery.
- [ ] 7.4 Cover giant lines, binary data, pagination and concurrent reads → verify: exact raw reconstruction and bounded work/results.

## Task 8: Capture execute-file output

- **Status:** pending
- **Depends on:** Task 6, Task 7
- **Size:** M
- **Can run in parallel with:** Task 9, Task 10
- **Docs:** [Implementation §10](implementation.md#10-task-8--execute-file-capture)

### Subtasks

- [ ] 8.1 Adapt `tool_execute_file.go` to the shared service while preserving file admission → verify: execute-file policy and full capture fixtures.
- [ ] 8.2 Stop new `file:*` intent writes into knowledge storage → verify: new run recovery and unchanged pre-existing source rows.
- [ ] 8.3 Preserve failure/compilation diagnostics and budget all paths → verify: parity with ordinary execution.

## Task 9: Capture and recover batch executions

- **Status:** pending
- **Depends on:** Task 6, Task 7
- **Size:** M
- **Can run in parallel with:** Task 8, Task 10
- **Docs:** [Implementation §11](implementation.md#11-task-9--batch-capture-and-bounded-recovery)

### Subtasks

- [ ] 9.1 Add batch and child run identities, retaining all-command preflight → verify: a denied final command prevents every side effect.
- [ ] 9.2 Preserve serial/parallel deadline and result-order semantics → verify: mixed failure, capture loss, timeout and skipped-child cases.
- [ ] 9.2a Support yielding a live batch with `wait_ms` → verify: later status distinguishes pending/running/skipped children and retains late output.
- [ ] 9.3 Adapt `batch_response.go` with paginated status/recovery inventories and scoped queries → verify: complete serialized bounds and explicitly separate global knowledge results.
- [ ] 9.4 Remove new batch knowledge writes and update descriptions → verify: no silent selector migration or lost legacy content.

## Task 10: Recognize routine commands and capture wrappers

- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** Task 5, Task 6, Task 7, Task 8, Task 9
- **Docs:** [Implementation §12](implementation.md#12-task-10--routine-recognition-and-captured-program-admission)

### Subtasks

- [ ] 10.1 Add bounded catalogue/wrapper parsing in `internal/hook/routine.go`, initially inactive → verify: executable-position positives and quoted/heredoc/dynamic negative fixtures.
- [ ] 10.2 Recognize literal capture CLI calls and inspect underlying programs for policy → verify: mixed captured/uncaptured chains and malformed lookalikes.
- [ ] 10.3 Add explicit operator native exceptions and configuration validation → verify: exceptions cannot override security deny/ask decisions.
- [ ] 10.4 Cover exact issue #132 examples and published command families → verify: `TestRoutineIssue132Commands`.

## Task 11: Enable strict Codex routine routing

- **Status:** pending
- **Depends on:** Task 8, Task 9, Task 10
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation §13](implementation.md#13-task-11--strict-codex-routing-and-generated-instructions)

### Subtasks

- [ ] 11.1 Enforce routine capture after security/existing HTTP routing → verify: native side effects never occur before redirection, including unavailable MCP/CLI.
- [ ] 11.2 Preserve HTTP/child one-use evidence contracts and distinguish routine guidance → verify: existing observation/fallback fixtures remain valid.
- [ ] 11.3 Synchronize runtime guidance, generators, committed artifacts and descriptions → verify: whole-file equality, idempotency, complete drift guard and external-handler preservation.
- [ ] 11.4 Update tool catalogues/matchers/counts for `capy_runs` → verify: no read/status call generates execution-success evidence; nested-owner/alias fixtures pass.

## Task 12: Complete diagnostics and compatibility documentation

- **Status:** pending
- **Depends on:** Task 11
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation §14](implementation.md#14-task-12--diagnostics-compatibility-and-operator-recovery)

### Subtasks

- [ ] 12.1 Extend CLI/MCP stats/doctor with safe capture and storage diagnostics → verify: no credential/log leaks, unexpected creation or activation claims.
- [ ] 12.2 Test/document configuration, timeout, background, result and source-selector compatibility → verify: real examples and explicit migration behavior.
- [ ] 12.3 Document quiescent rekey/removal and rollback → verify: old-key logs are preserved or explicitly discarded, with routing disabled before downgrade.
- [ ] 12.4 Update README, architecture, CLI help, AGENTS.md and predecessor links → verify: docs match targeted coverage and failure/retention limits.

## Task 13: Verify installed behavior and release readiness

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9, Task 10, Task 11, Task 12
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation §15](implementation.md#15-task-13--installed-acceptance-and-release-verification)

### Subtasks

- [ ] 13.1 Record trusted live Codex/MCP/CLI acceptance with side-effect counters and both unavailable paths → verify: denial, one execution, full retained recovery, policy preservation and external hooks.
- [ ] 13.2 Record issue-like fixtures and a real project test run in `verification.md` → verify: raw/captured/returned bytes, status, recovery hashes and retention.
- [ ] 13.3 Run `$kk:test` with FTS5, synthetic keys and isolated config/data/cache homes → verify: full suite, focused race/subprocess checks and no owned descendants/plaintext spool.
- [ ] 13.4 Run executor/retrieval performance checks and `make bench-quality` with baseline comparison → verify: regressions fixed or explicitly dispositioned.
- [ ] 13.5 Run `$kk:document`, `$kk:review-code` with Go input and `$kk:review-spec` for the whole feature → verify: actionable findings resolved/recorded before claiming #132 complete.

## Dependency Graph

```text
1 -> 2 -> 3 -> {4, 5}
{4, 5} -> {6, 7}
{6, 7} -> {8, 9}
4 -> 10
{8, 9, 10} -> 11 -> 12 -> 13
1..12 -> 13
```

Parallel markers describe dependency freedom, not authorization to spawn agents
or edit shared modules concurrently. Task 1's owner-loss gate and all proposed
contract/default review decisions must be resolved before their dependent work.
