# Tasks: routine command capture

> Architecture: [design.md](design.md)
> Contract: [contracts.md](contracts.md)
> Implementation: [implementation.md](implementation.md)
> Status: in-progress — Tasks 1–2 done; Tasks 3–17 pending
> Created: 2026-10-16
> Issue: [#132](https://github.com/serpro69/capy/issues/132)
> Reviews: [original dispositions](review-resolution.md) · [readiness corrections](readiness-review.md)
> Not Doing: arbitrary-shell rewriting/recognition, PTY/stdin, cross-owner cancellation, persistent jobs, trust mutation, knowledge/vault migration, #8 durable retention, Claude routine enforcement, remote upload, toolbox migration, run-log-preserving rekey, universal platform support

## Task 1: Prove supervisor ownership and lease transfer

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove supervisor/control/lease behavior before storage interfaces
- **Docs:** [Implementation Task 1](implementation.md#task-1-prove-supervisor-ownership-and-lease-transfer)

### Subtasks

- [x] 1.1 Add the private supervisor/event/control seams in executor, execution and the hidden CLI helper → verify: real early/late output, protocol bounds and no private FD inheritance by commands.
- [x] 1.2 Prove group retirement and retained owner/lifecycle leases through owner SIGKILL and delayed cleanup → verify: concurrent reset/reuse cannot proceed early; compiler phases follow the same lifecycle.
- [x] 1.3 Exercise IPC/startup/helper failures and record supported cleanup proofs before Task 2 → verify: no unknown outcome is certified as quiescence.

**Evidence:** [supervisor proof, support limits and checks](supervisor-proof.md) ·
[isolated review and resolved findings](task1-code-review.md). Linux helper and
synthetic persistence proof passed; production storage/public execution remains
pending. Full FTS5 suite passed across the initial run and two socket-enabled
package reruns; focused integration/race checks, build, vet and quality comparison
passed. The independent code reviewer approved; external review was unavailable.

## Task 2: Capture and recover one synchronous MCP run

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation Task 2](implementation.md#task-2-capture-and-recover-one-synchronous-mcp-run)

### Subtasks

- [x] 2.1 Add encrypted run metadata/events and selected-owner/key admission in runstore/execution → verify: no knowledge/vault writes or pre-spawn side effects on invalid credentials/storage.
- [x] 2.2 Adapt ordinary execution and initial run status/raw-read MCP actions → verify: exact bytes, child status, one execution and recovery by ID.
- [x] 2.3 Add strict config/path resolution and safe unavailable default projection until Task 4 → verify: nested/worktree identity and sandboxed admission harness.

**Evidence:** [Task 2 verification and limits](task2-verification.md) ·
[isolated code review](task2-code-review.md). Real MCP restart/exact-byte/status
recovery and native sandbox admission passed. The full FTS5 run plus corrected
focused reruns, race/build/vet, generated-artifact checks and baseline quality
comparison passed. Independent review approved; external review was unavailable.
Default output projection and later run actions remain explicitly unavailable.

## Task 3: Bound capture, results and failure outcomes

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation Task 3](implementation.md#task-3-bound-capture-results-and-failure-outcomes)

### Subtasks

- [ ] 3.1 Implement bounded blocks/queues/writes and byte/order metadata → verify: binary, UTF-8, stderr-only, giant-line and middle-marker fixtures.
- [ ] 3.2 Budget complete serialized responses and map command/capture states → verify: every error/timeout/start/compile path, exact exit 1 and null-ID rejection.
- [ ] 3.3 Inject append/queue/finalization failure after spawn → verify: execution finishes, pipes drain, memory stays bounded and retained-prefix/final-state claims are truthful.

## Task 4: Recover sanitized output across arbitrary boundaries

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation Task 4](implementation.md#task-4-recover-sanitized-output-across-arbitrary-boundaries)

### Subtasks

- [ ] 4.1 Add bounded streaming redaction and encrypted projection/checkpoints/raw anchors → verify: all synthetic token/tag splits and unchanged raw hashes.
- [ ] 4.2 Handle overflow, unfinished private blocks, EOF and state/version failure conservatively → verify: no default-surface prefix or diagnostic-tail leak.
- [ ] 4.3 Implement projection/raw cursors and merged pending-coverage barriers → verify: arbitrary protected-span pages, independent stream progress, huge lines and stale cursors.

## Task 5: Expose shared CLI execution and retrieval

- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** Task 6
- **Docs:** [Implementation Task 5](implementation.md#task-5-expose-shared-cli-execution-and-retrieval)

### Subtasks

- [ ] 5.1 Add CLI execute/runs using shared contracts, owner selection and checked inputs → verify: cross-interface parity and exact deny/matched-ask behavior.
- [ ] 5.2 Implement bounded text/JSON and exit mapping; reject unsupported CLI wait/background flags → verify: child/capture/signal/timeout matrix and attached ownership.
- [ ] 5.3 Run CLI in actual workspace-write confinement → verify: no home-cache dependency, excluded-owner rejection and nested/worktree store parity.

## Task 6: Yield and cancel MCP runs through the real transport

- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** Task 5, Task 14
- **Docs:** [Implementation Task 6](implementation.md#task-6-yield-and-cancel-mcp-runs-through-the-real-transport)

### Subtasks

- [ ] 6.1 Implement atomic request-to-owner handoff before handler return → verify: ordinary mcp-go request-context cleanup preserves yielded jobs.
- [ ] 6.2 Exercise before/after-handoff cancellation, serialization/write failure and natural completion races → verify: one control outcome and no false receipt guarantee.
- [ ] 6.3 Wire duration/wait/background contracts, own-run cancel and standalone active slots → verify: late output and recovery actions remain available under capacity exhaustion.

## Task 7: Reopen safely after owner crashes

- **Status:** pending
- **Depends on:** Task 5, Task 6
- **Size:** M
- **Can run in parallel with:** Task 14
- **Docs:** [Implementation Task 7](implementation.md#task-7-reopen-safely-after-owner-crashes)

### Subtasks

- [ ] 7.1 Integrate fixed slots/generations and supervisor-held lifecycle leases with real stores → verify: delayed cleanup blocks reset/reuse and file growth stays bounded.
- [ ] 7.2 Scan all slots during bounded opening/maintenance → verify: never-reused stale high slots recover and live replacement generations stay untouched.
- [ ] 7.3 Apply the same owner proof to abandoned work claims → verify: no adoption, stale-PID signalling or permanent claim blockage.

## Task 8: Make store policy consistent across processes

- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** Task 14
- **Docs:** [Implementation Task 8](implementation.md#task-8-make-store-policy-consistent-across-processes)

### Subtasks

- [ ] 8.1 Persist authoritative store policy/revision and require matching attached writers → verify: old-config MCP/new-config CLI cannot enforce conflicting limits.
- [ ] 8.2 Implement quiescent close/release/exclusive-update/reopen validation with bounded retries → verify: helpers/live handles block changes and reset races are safe.
- [ ] 8.3 Record per-run TTL/revision and immutable terminal expiry → verify: non-retroactive TTL, lower-quota restart and exact config validation.

## Task 9: Retain, evict and explicitly reset runs

- **Status:** pending
- **Depends on:** Task 8
- **Size:** M
- **Can run in parallel with:** Task 14
- **Docs:** [Implementation Task 9](implementation.md#task-9-retain-evict-and-explicitly-reset-runs)

### Subtasks

- [ ] 9.1 Add transactional quota admission/append accounting and terminal eviction → verify: independent writers, protected active runs and continued execution after capture exhaustion.
- [ ] 9.2 Add expiry-on-read and bounded maintenance/checkpoint/reclaim → verify: reads do not renew TTL and active WALs survive concurrent owners.
- [ ] 9.3 Implement keyless reset --discard under exclusive lifecycle protection → verify: explicit rotation recovery, live-helper refusal, preserved locks/unrelated data and stale cursors.

## Task 10: Search run output lazily

- **Status:** pending
- **Depends on:** Task 9
- **Size:** M
- **Can run in parallel with:** Task 14
- **Docs:** [Implementation Task 10](implementation.md#task-10-search-run-output-lazily)

### Subtasks

- [ ] 10.1 Add sanitized dual-FTS windows/raw anchors and strict retrieval errors → verify: no-search paths create no FTS rows and middle failures are readable.
- [ ] 10.2 Add resumable coverage/claims and measured/conservative quota rollback → verify: claimant death, partial results and no raw eviction solely for indexes.
- [ ] 10.3 Expose run/batch search and intent previews → verify: knowledge-search throttling cannot block recovery; first/repeat cost is measured under capture load.

## Task 11: Capture execute-file runs with the shared lifetime

- **Status:** pending
- **Depends on:** Task 10
- **Size:** M
- **Can run in parallel with:** Task 12, Task 14
- **Docs:** [Implementation Task 11](implementation.md#task-11-capture-execute-file-runs-with-the-shared-lifetime)

### Subtasks

- [ ] 11.1 Migrate execute-file through shared admission/supervision, preserving path/FILE_CONTENT policy → verify: complete bytes, compiler/runtime status and policy fixtures.
- [ ] 11.2 Add wait_ms schema/help and continuation → verify: late output, deadline, cancellation and paging parity.
- [ ] 11.3 Stop new file:* knowledge writes without migrating old rows → verify: storage isolation and bounded results.

## Task 12: Recover a serial batch through MCP

- **Status:** pending
- **Depends on:** Task 10
- **Size:** M
- **Can run in parallel with:** Task 11, Task 14
- **Docs:** [Implementation Task 12](implementation.md#task-12-recover-a-serial-batch-through-mcp)

### Subtasks

- [ ] 12.1 Add batch/child identities, all-command preflight and serial captured execution → verify: denied final input prevents all effects; shared deadline/skipped ordinals are correct.
- [ ] 12.2 Expose aggregate status, paginated children, explicit batch search and yielding recovery → verify: every child status remains recoverable beyond the preview and independently of child-log eviction until manifest expiry.
- [ ] 12.3 Keep full batch replacement/routing gated on Task 13 → verify: serial MCP fixtures do not claim parallel migration complete.

## Task 13: Schedule parallel batches within owner capacity

- **Status:** pending
- **Depends on:** Task 12
- **Size:** M
- **Can run in parallel with:** Task 14
- **Docs:** [Implementation Task 13](implementation.md#task-13-schedule-parallel-batches-within-owner-capacity)

### Subtasks

- [ ] 13.1 Apply bounded pending inventory and fair shared scheduling → verify: eight children plus two standalone jobs respect max_active=4 and report effective/current counts.
- [ ] 13.2 Add parallel spawn deadlines/dispatch cutoff and pending cancellation while retaining serial behavior → verify: atomic rejection and no spawn after cancellation.
- [ ] 13.3 Complete batch replacement and scoped/global query recovery → verify: all batch paths use run storage, preserve provenance and obey complete-response bounds.

## Task 14: Recognize routine commands and captured programs

- **Status:** pending
- **Depends on:** Task 5
- **Size:** M
- **Can run in parallel with:** Task 6, Task 7, Task 8, Task 9, Task 10, Task 11, Task 12, Task 13
- **Docs:** [Implementation Task 14](implementation.md#task-14-recognize-routine-commands-and-captured-programs)

### Subtasks

- [ ] 14.1 Add bounded catalogue/wrapper parsing with the gate inactive → verify: issue #132 shapes, quoted/heredoc/dynamic negatives, exceptions and scanner bounds.
- [ ] 14.2 Parse actual capture-CLI arguments and underlying policy → verify: lookalikes/captured siblings cannot exempt uncaptured routines or bypass deny/ask.

## Task 15: Enable strict Codex routing and synchronize artifacts

- **Status:** pending
- **Depends on:** Task 11, Task 13, Task 14
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation Task 15](implementation.md#task-15-enable-strict-codex-routing-and-synchronize-artifacts)

### Subtasks

- [ ] 15.1 Enforce security → mandatory routine capture → HTTP-only routing → verify: root/child mixed cases and every evidence state, with no routine fallback or evidence consumption.
- [ ] 15.2 Implement healthy-running availability observation rules → verify: failure/unstarted/polling cannot create or refresh evidence.
- [ ] 15.3 Synchronize catalogues, runtime/generated/committed guidance and platform scope → verify: drift/idempotency, Claude guidance-only behavior, nested owners, aliases and external hooks.

## Task 16: Document and diagnose the complete operator workflow

- **Status:** pending
- **Depends on:** Task 15
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation Task 16](implementation.md#task-16-document-and-diagnose-the-complete-operator-workflow)

### Subtasks

- [ ] 16.1 Extend safe bounded diagnostics/stats without creating stores or claiming activation → verify: CLI/MCP parity, capacity/policy visibility and secret-free output.
- [ ] 16.2 Update README, architecture, help, AGENTS and predecessor pointers; publish reset-only v1 rotation/rollback → verify: every documented operator action exists and unsupported preservation promises are removed.

## Task 17: Verify installed acceptance and release readiness

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9, Task 10, Task 11, Task 12, Task 13, Task 14, Task 15, Task 16
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation Task 17](implementation.md#task-17-verify-installed-acceptance-and-release-readiness)

### Subtasks

- [ ] 17.1 Record live trusted Codex/MCP/sandboxed-CLI and Claude guidance-only acceptance → verify: one execution, policy composition, owner/worktree parity, exceptions and unavailable paths.
- [ ] 17.2 Record issue-like fixtures plus a real project test run → verify: byte/status/hash recovery, redaction, expiry, handoff, supervisor and policy-change cases.
- [ ] 17.3 Run $kk:test with FTS5, synthetic keys and isolated homes, including race/subprocess checks → verify: full suite and no orphaned descendants/plaintext spools.
- [ ] 17.4 Run executor/retrieval performance and make bench-quality with baseline comparison → verify: memory, no-search/first/repeat cost, WAL/index growth and quality regressions dispositioned.
- [ ] 17.5 Run $kk:document, $kk:review-code with Go input and $kk:review-spec for the whole feature → verify: findings resolved/recorded before claiming #132 complete.

## Dependency Graph

```text
1 -> 2 -> 3 -> 4 -> {5, 6}
{5, 6} -> 7 -> 8 -> 9 -> 10
10 -> {11, 12}; 12 -> 13
5 -> 14
{11, 13, 14} -> 15 -> 16 -> 17
1..16 -> 17
```

Parallel markers describe dependency freedom, not permission to spawn agents or
make overlapping edits. The Task 1 risk gate must settle cleanup/lease proofs
before Task 2. Public contracts are authoritative; completed slices do not enable
routine gating or establish release readiness ahead of their dependencies.
