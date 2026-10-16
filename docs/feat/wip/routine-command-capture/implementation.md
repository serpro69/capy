# Implementation: routine command capture

> Status: planned — implementation has not started
> Design: [design.md](design.md) · Tasks: [tasks.md](tasks.md)
> Created: 2026-10-16

## 1. Orientation and sequencing

Capy is a Go CLI and MCP server. `cmd/capy` owns Cobra commands and credential /
project resolution; `internal/server` adapts MCP requests; `internal/executor`
owns child execution; `internal/security` owns shared policy scanning. The
knowledge database and session vault are independent encrypted stores. This
feature adds a third, temporary run store rather than a fourth knowledge kind.

The first-time contributor should read AGENTS.md, architecture sections on
execution/security/credential flow, ADR-016 (close/checkpoint), ADR-020 (rekey),
ADR-028 (retrieval corpus), ADR-032 (credentials) and ADR-036 (nested Codex owners).
Use existing encrypted SQLite and process-group conventions; this plan introduces
no dependency upgrade, public HTTP server or persistent daemon.

The design's user decisions are settled; numeric defaults and detailed contracts
are proposals pending design review. All implementation tasks remain pending.
Do not enable strict routing while any supported execution interface can still
discard output silently or lacks retrieval. Tasks 1–9 establish capture paths;
Tasks 10–11 enable routing; Tasks 12–13 complete recovery and release verification.

## 2. Module boundaries and public surfaces

| Component | Responsibility |
| --- | --- |
| `internal/executor` | Runtime selection, spawn/wait, ordered output-event delivery, cancellation and group reaping; no SQLite dependency |
| New `internal/execution` | Shared request validation/admission, active-run ownership, capture writer coordination, deadlines, bounded result assembly |
| New `internal/runstore` | Encrypted run/event metadata, raw bytes, optional derived FTS, cleanup, quota and owner reconciliation |
| `internal/server/tool_execute*.go`, `tool_batch.go`, new `tool_runs.go` | MCP adapters; do not duplicate lifecycle or formatter policy |
| New `cmd/capy/execute.go`, `runs.go` | CLI adapters, JSON/text rendering and exit mapping; do not construct an MCP server |
| `internal/hook` + `internal/platform` | Credential-free routine recognition/denial, instructions and generated-artifact parity |

Expose the design's result envelope as a transport-neutral Go type. MCP wrappers
measure the fully serialized tool result because `structuredContent` and text
both cost bytes. CLI rendering measures its own total output. Do not use MCP's
JSON-fallback helper if it repeats the full result in both channels. Invocation
previews are redacted and bounded; full admitted program metadata stays encrypted.

Use the existing checked `cwd` and execute-file admission paths. CLI implicit
project selection must use the nearest validated Codex owner when installed
bindings are present, preserving the owner/ambiguity rules; otherwise use normal
CLI project discovery. An explicit project is honored according to the existing
CLI contract, never changed by `cwd`. Test ordinary subdirectories, nested owners,
worktrees, symlinks and an unrelated process cwd. Do not resolve a different run
store merely because `store.path` points to a separate database repository.

## 3. Task 1 — prove owner-bound execution and capture delivery

This is a risk-first vertical fixture: a command emits an early marker, continues
after a short response wait, emits a late marker and exits. Exercise real
subprocess ownership before committing to storage/transport internals.

- Add an output-event sink seam in `internal/executor/types.go` and
  `executor.go`, with bounded block ownership and per-stream byte order. Keep
  legacy callers working during migration → verify: existing executor tests plus
  `TestStreamedExecutionCapturesLateOutput`.
- Separate a response wait from command lifetime in a minimal execution-owner
  harness. No released endpoint may claim capture guarantees from this harness
  alone → verify: the late marker appears after the wait has returned.
- Prove owner shutdown, parent loss, explicit cancellation, deadlines and SIGKILL
  behavior with descendants holding stdout open. Reuse wait/reap safeguards in
  `wait_linux.go`, `wait_darwin.go`, `background_test.go` → verify:
  `TestOwnedExecutionLifecycle` observes every descendant exit and no PID reuse
  signalling. If the existing guard fails abrupt-owner-death cases, implement a
  per-run supervisor/liveness pipe and rerun this gate before Task 5. Record the
  actual mechanism and supported OSes in the design; do not waive this check.
- Preserve bounded cleanup even when a descendant never closes a pipe → verify:
  fixture teardown completes under a test deadline with no abandoned reader.

## 4. Task 2 — capture and recover one synchronous MCP run

- Add `internal/runstore/store.go` with a reviewed minimal schema for versioned
  metadata/events and encrypted opening through `sqliteutil`. Reuse captured
  credentials; do not initialize knowledge/vault → verify:
  `TestRunStoreEncryptedAdmission` proves a wrong/empty key cannot spawn a marker
  command or create plaintext files.
- Add `internal/execution/execute.go` to validate inputs, admit storage, commit
  initial metadata, spawn, consume output events, drain and finalize → verify:
  `TestExecuteRunRoundTrip` compares raw stdout/stderr hashes and explicit status.
- Adapt `handleExecute`, register initial `capy_runs` status/read actions and
  update the tool inventory → verify: a real MCP client executes a small command
  and reads it by run ID without querying or writing `knowledge.db`.
- Add `[runs]` config resolution/defaults and anchored cache paths; preserve
  existing strict precedence and selected-key failure semantics → verify:
  configuration layering and project/cwd/key-source fixtures.

## 5. Task 3 — bounded large output and failure semantics

- Implement sequenced bounded buffering/writing and append accounting; retain
  exact emitted bytes before view filtering. Include compiler diagnostics and
  start errors in the execution result path → verify:
  `TestRunCaptureBytesAndOrdering` for alternating streams, stderr-only output,
  split UTF-8, binary bytes, huge lines and a marker in the middle of a 2-MiB log.
- Add one response budgeter for every result branch. Essential status/cursor
  fields survive truncation; all budgets count encoded output → verify:
  `TestExecutionResponseBudget` includes JSON escapes, giant input previews,
  missing intent, timeout, start error and metadata-only errors.
- Replace soft-failure inference for migrated execution with explicit terminal
  states and nullable child status → verify: exit 1 with stdout and stderr,
  signals, timeout and pipeline exit semantics remain distinguishable.
- Inject append failure, full queue, byte-limit exhaustion and finalization
  failure. Continue draining and execution after capture failure → verify:
  `TestCaptureFailureDoesNotKillCommand` observes the final side-effect marker,
  incomplete state, bounded memory and no full-output fallback.
- Keep raw capture successful if FTS is unavailable → verify: raw reads work and
  search/index status reports the specific failure rather than empty success.

## 6. Task 4 — CLI execution and retrieval parity

- Add `cmd/capy/execute.go` and `runs.go`, registering them in `main.go`. Use
  common request/result types and core admission; CLI remains attached to its
  running process → verify: CLI-produced runs are readable over MCP and vice versa.
- Resolve projects/keys without knowledge DB creation, validate checked code
  files, and apply CLI matched-ask diagnostics as specified → verify: no CLI
  fallback bypasses a denied command or matched ask; unmatched default ask passes.
- Implement text/JSON and typed exit errors, preserving child failure while
  making capture-only failure nonzero → verify: an exit-code matrix covers
  success, exit 1/74/124, signal, start failure, timeout and failed capture.
- Expose bounded read/status/list. Explicit binary reads are base64 and paginated;
  default views sanitize secrets and terminal controls → verify: concatenated
  raw pages match source bytes and line/event anchors remain tied to raw data.

## 7. Task 5 — long-running MCP jobs and owner recovery

- Add active-run management in `internal/execution` using an owner lifetime
  context, execution deadline and independent response wait → verify: a command
  lasting over 30 seconds returns `running` early, completes once, and retains
  output emitted after that response.
- Add own-run cancellation to `capy_runs`; status/read across other owners remain
  supported, cancellation across owners returns a specific error → verify:
  `TestRunCancellationOwnership` cannot signal another process from persisted PID.
- Migrate legacy `background` to continued capture, with explicit argument
  conflict validation. Normal yielding does not inherit daemon-mode behavior →
  verify: `TestExecuteBackgroundCompatibility` distinguishes wait, deadline and
  owner shutdown while preserving late output.
- Add service owner locks and interrupted-state recovery. Integrate shutdown
  order in `server.go` / `lifecycle.go` and CLI signal handling → verify:
  active independent owners are never marked interrupted; dead owners are never
  automatically resumed; the Task 1 lifecycle matrix passes through installed
  MCP and CLI paths, not only a direct executor call.

## 8. Task 6 — retention and quota during real execution

- Implement transactionally checked logical accounting and active-run admission
  in `runstore`; expose the proposed limits through config → verify: concurrent
  independent CLI/MCP writers cannot both spend the same remaining quota.
- Evict expired terminal runs, then oldest completed runs under pressure, never
  active runs. Appends that cannot continue mark capture incomplete → verify:
  `TestRunQuotaPressure` covers active-only pressure, early eviction, quota races
  and complete isolation from durable knowledge/vault data.
- Enforce terminal-time TTL during reads and bounded maintenance at admission,
  periodic activity and shutdown → verify: an injected clock proves reads cannot
  refresh TTL; crash/restart and idle reopen remove overdue data without relying
  on SessionEnd.
- Checkpoint/reclaim under the documented quiescence rules and report logical
  versus physical usage → verify: deletion makes pages reusable, no active writer
  loses its WAL, and wrong-key/corruption never triggers automatic replacement.

## 9. Task 7 — run-scoped search and useful previews

- Add bounded line/event windows and Porter/trigram FTS in `runstore`. Store
  sanitized searchable text with raw anchors and an explicit indexed high-water
  mark; append durability does not depend on index success → verify:
  `TestRunSearchAnchors` retrieves the middle failure and reads its original span.
- Register only the new fixed table names in `internal/retrieval/retrieval.go`;
  use `CorpusConfig.OnError` so search failures are not represented as no hits →
  verify: retrieval allowlist tests and error injection retain knowledge/vault
  behavior.
- Add MCP/CLI search actions requiring a run ID. Use intent/default failure terms
  for previews after capture; hard status data always remains → verify: exhausted
  `capy_search` budget cannot prevent run reads/search, and no matching query
  still returns a recoverable run reference.
- Validate bounded work for huge/no-newline/binary logs and concurrent paging →
  verify: chunk sizes, cursor progression and source-byte recovery remain correct.

## 10. Task 8 — execute-file capture

- Route `tool_execute_file.go` through the shared capture service without changing
  project containment, external allow handling or `FILE_CONTENT` admission →
  verify: execute-file policy tests plus a complete captured file-analysis run.
- Remove new intent-generated knowledge writes from this path; retain old source
  lookup compatibility → verify: new runs are read through run retrieval, while
  pre-existing `file:*` rows remain unchanged.
- Cover runtime failures and compilation diagnostics → verify: result bounds,
  explicit status and full recovery are the same as ordinary execution.

## 11. Task 9 — batch capture and bounded recovery

- Replace knowledge-store aggregation with a batch record and child run IDs,
  keeping security admission over all commands before any execution → verify:
  a denied last command prevents every earlier side-effect marker.
- Preserve serial shared deadlines/skips and parallel independent deadlines/input
  ordering → verify: existing scheduling tests adapted to run envelopes, plus
  mixed successful/failed/incomplete child captures.
- Support a response-wait boundary for the live batch while keeping scheduling
  and child capture owned by the service → verify: polling a yielded batch
  distinguishes running, pending and skipped children and retains their late output.
- Adapt `batch_response.go`: paginate inventories, bound the complete serialized
  response, scope default searches to batch run IDs, and keep explicit global
  knowledge results visibly separate → verify: large batches cannot overflow the
  budget or lose status/recovery references for omitted children.
- Keep the migration explicit in tool descriptions and integration tests →
  verify: no new `batch:*` knowledge sources and no silent source-selector alias.

## 12. Task 10 — routine recognition and captured-program admission

- Add `internal/hook/routine.go` with the design catalogue and bounded wrapper
  parsing based on the shared shell scanner. Keep the gate disabled until Task
  11 → verify: table tests for literal commands, chain/substitution boundaries,
  quoted mentions, comments, heredocs, dynamic executables and scan overflow.
- Add exact capture-CLI argument recognition; inspect its code for command policy
  before granting the output-routing exemption → verify: captured command plus
  an uncaptured sibling denies before either executes, and malformed/lookalike
  CLI commands do not become exemptions.
- Add operator-controlled routing configuration and native exceptions → verify:
  exceptions skip output routing only; security deny/ask precedence is retained.
- Include the exact #132 shapes as named fixtures → verify:
  `TestRoutineIssue132Commands` covers `mise exec ... && mise run test`, a virtualenv
  Python pip install, pytest, make, and every published catalogue family.

## 13. Task 11 — strict Codex routing and generated instructions

- Integrate the routine classifier after security and existing HTTP routing, with
  an explicit default policy for recognized routine commands → verify: native
  routine execution is denied before side effects even when MCP is unavailable;
  the denial explains MCP and CLI choices without pretending availability.
- Keep the existing HTTP observation/one-use contracts isolated from this new
  requirement → verify: all main/child HTTP fallback tests still pass, and a
  failed routine capture never grants a native retry.
- Update `internal/hook/routing.go`, `internal/platform/routing.go`, tool
  descriptions and generated/committed artifacts together. Remove the blanket
  instruction that routine test logs always use native tools → verify:
  `TestGeneratedWholeFileArtifacts`, `TestMergedArtifactsAreIdempotent` and the
  complete setup drift guard; provider/external instructions and owner IDs survive.
- Update known-tool matchers/counts for `capy_runs`, without treating read/search
  as execution-success evidence → verify: installed handler and doctor fixtures,
  including nested installations and different MCP aliases.

## 14. Task 12 — diagnostics, compatibility and operator recovery

- Extend existing stats/doctor with capture/result bytes, store usage, limits and
  safe error sources. Static checks must not create storage or claim activation →
  verify: CLI/MCP parity, credential-free hooks and secret-free diagnostics.
- Document and test timeout/result/source-selector changes and legacy background
  behavior → verify: published examples execute through both interfaces and
  default/explicit config precedence is unambiguous.
- Document quiescent cache rekey/removal using existing primitives and release
  rollback order. Do not add a second independent rekey implementation → verify:
  a fixture retains or explicitly discards old-key logs without automatic deletion,
  and disabling routine routing before downgrade restores the prior host path.
- Update README, architecture, CLI help, AGENTS.md generator/invariant notes and
  the predecessor #132 disposition → verify: docs distinguish shipping behavior,
  targeted coverage, logical quota, host limits and accepted failure semantics.

## 15. Task 13 — installed acceptance and release verification

- Record an actual trusted Codex version and compatible installed capy binary.
  Verify startup/child guidance, native denial, MCP execution, CLI fallback,
  strict unavailability, policy denial, exceptions and unrelated hooks → verify:
  side-effect counters demonstrate one execution; all failure messages are visible.
- Run deterministic command fixtures corresponding to issue #132 without real
  package downloads. Include a passing test-style log, failure in the middle,
  stderr-only output, installation-like progress and a long-running command →
  verify: raw/captured/returned bytes, exit status, recovery hashes and cleanup
  are recorded in `verification.md`. Add a real project test command for host
  acceptance; an install fixture is not presented as a real network install.
- Run `$kk:test` with both synthetic keys, isolated config/data/cache homes and
  `-tags fts5`; include focused race and subprocess lifecycle suites → verify:
  no unexplained failure, leaked descendant, plaintext spool or active-run eviction.
- Run executor/store/retrieval performance checks and `make bench-quality`, then
  compare a baseline as required by AGENTS.md → verify: record output size,
  memory/throughput and retrieval-quality changes; fix or explicitly disposition
  regressions before release.
- Run `$kk:document`, `$kk:review-code` with Go input, and `$kk:review-spec` against
  this whole directory → verify: findings resolved or written as actionable tasks;
  no claim that drafting or hook registration alone closes #132.

## 16. Deferred and independent work

- **Vault fixture isolation:** remains in
  [codex-hooks/tasks.md](../codex-hooks/tasks.md#follow-ups-outside-feature-completion).
  It is pre-existing and independent; isolate `XDG_CONFIG_HOME` in the three
  named fixtures when that task is taken. New tests here must isolate homes now.
- **Historical tracking:** PR #135 merged and toolbox #170 closed after the
  predecessor notes were written. Reconcile that historical text and #29 closure
  when maintaining tracking; this design makes no external issue changes. The
  predecessor's implementation/task documents also link to the absent
  `toolbox-enhancement-issue.md`; restore that saved artifact or replace those
  historical links with issue #170 during that tracking correction. This
  pre-existing link problem does not affect the new #132 design links.
- **Hard physical disk quota:** logical accounting plus explicit disk-full
  behavior is proposed here. If review requires an exact filesystem-byte ceiling,
  revisit storage segmentation/reservation before implementation rather than
  disguising SQLite/WAL allocation as a strict disk bound.

PTY interaction, daemon survival, cross-owner job cancellation and arbitrary
shell interpretation are excluded by the design, not unfinished tasks hidden
behind this implementation plan.
