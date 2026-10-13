# Tasks: Codex hooks v2

> Design: [design-v2.md](design-v2.md)
> Implementation: [implementation-v2.md](implementation-v2.md)
> Status: in-progress — Task 1 done, verified and reviewed; Tasks 2–5 pending
> Created: 2026-10-12
> Baseline: capy `112f2c8` (includes the Codex `hooks.state` parser fix); v1 [tasks](tasks.md) remain done
> Confirmed choices: direct owner-validated dispatch; compatible capy on PATH; one final launch format with no v1 backward compatibility; unverified main-session HTTP availability allows advisory fallback and recent suitable success permits one redirect
> Not Doing: v1 launch compatibility/migration, new hook events, session continuity, noisy-build/output-budget policy, external-MCP guidance, trivial-command exemptions, readiness sentinels/daemons, trust mutation, database changes, new tunable config/dependencies, command rewriting, universal runtime support
> Implementation entry: explicitly select `tasks-v2.md`; do not resume the completed unsuffixed task list

## Task 1: Simplify the installed Codex launch path

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — establish the final launch, PATH and failure contracts before input/routing work
- **Docs:** [Launch contract](design-v2.md#4-simpler-owner-bound-launch), [implementation](implementation-v2.md#2-task-1--direct-launch-with-an-explicit-path-prerequisite)

### Subtasks

- [x] 1.1 Replace the generated/recognized launch grammar in `setup_codex_launch.go`, `setup_codex_mcp.go` and `codex_owner.go` with the single final hook-supervisor/direct-MCP form → verify: `TestCodexFinalLaunchBindings` covers strict invocation fields, equivalent serialization, opaque trust state, adjacent markers and overlaps; obsolete forms fail diagnostically without compatibility parsing or conversion.
- [x] 1.2 Change `cmd/capy/codex_launch.go` to invoke the hook handler directly and share serve initialization with `cmd/capy/serve.go`, passing the validated owner/context and preserving cwd → verify: `TestCodexDirectDispatch` exercises actual hook and initialized MCP paths from root/subdirectories, external storage, no hook credentials/DB, and a never-invoked sentinel project wrapper.
- [x] 1.3 Add the bounded metadata-only protocol probe and PATH prerequisite before setup writes, including default-both setup; enforce the stated `--binary` match rule → verify: `TestCodexSetupPathPrerequisite` covers missing/invalid/timeout/overflow candidates, no partial setup on prerequisite failure and unaffected Claude-only/DB-repo modes.
- [x] 1.4 Keep a compact hook failure/signal supervisor and direct MCP launch; integrate existing server lifecycle/context/executor cleanup without a second guard → verify: `TestCodexLaunchFailureAndCancellation` checks exit 2, exit-0 denials, stdin, MCP stdout/status and signal/EOF cleanup of actual Go/server/executor processes.
- [x] 1.5 Remove Codex wrapper generation/requirements and Codex-only branches from the shared wrapper generator; update doctor/setup output and committed/generated artifacts together → verify: final-format setup is idempotent with the same owner ID, unrelated handlers/trust survive, absent Codex wrapper is normal, Claude behavior and all drift guards pass.
- [x] 1.6 Rebase location, copied-ID/missing-marker and overlap fixtures onto the final execution path → verify: existing portability/owner protections still pass; replace obsolete wrapper-execution expectations with direct-dispatch assertions, with no v1 compatibility fixture set.

**Evidence:** [Task 1 verification](verification-v2.md#task-1-acceptance-evidence);
[isolated code review: APPROVE](.reviews/task-1-v2-code-review-2026-10-13.md).
Both initial lifecycle findings were reproduced and fixed; final package/race/vet
and artifact checks pass. Live v2 host acceptance remains Task 5.

## Task 2: Bound Codex stdin through the direct and generated hook paths

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 3
- **Docs:** [Input contract](design-v2.md#5-bounded-codex-stdin), [implementation](implementation-v2.md#3-task-2--bounded-codex-input-through-the-installed-cli)

### Subtasks

- [ ] 2.1 Add `internal/hook/input.go` with named ordinary/SessionEnd idle, total and byte budgets, owned deadline-capable pipe reads, and bounded regular-file support → verify: `TestCodexHookInputDescriptors` proves actual pipe behavior and closes all owned descriptors without a lingering read goroutine.
- [ ] 2.2 Wire Codex-only admission into `internal/hook/hook.go:Run` before handlers; require EOF, reject overflow/stall, and preserve Claude input handling → verify: `TestCodexHookInputBudgets` and `TestCodexHookInputNoSideEffects` cover boundaries, trickle input, complete invalid JSON and no policy/state/DB access on transport failure.
- [ ] 2.3 Map typed input failures through shared error handling for direct `hook` and `codex-launch`, preserving the final supervisor and structured-denial contracts → verify: `TestCodexHookInputExitCodes` checks direct/generated pre-tool exit 2 + stderr + empty stdout, other event errors and valid/invalid completed payloads.
- [ ] 2.4 Exercise production budgets through the final installed path and document descriptor/platform limits → verify: ordinary idle/trickle and short SessionEnd subprocess probes terminate with their writers open; normal EOF has no artificial delay; direct launch/signal/Claude fixtures pass.

## Task 3: Atomically claim existing session guidance

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [Guidance contract](design-v2.md#6-atomic-guidance-claims), [implementation](implementation-v2.md#4-task-3--atomic-once-per-session-guidance)

### Subtasks

- [ ] 3.1 Replace new JSON read–modify–writes in `internal/hook/guidance.go` with exclusive zero-byte markers for the three fixed types, preserving `sessionIDComponent` → verify: `TestGuidanceAtomicClaim` and all existing traversal/component isolation cases.
- [ ] 3.2 Recognize bounded regular older shared-guidance JSON read-only, retaining known-key membership behavior; validate existing markers and keep advisory fallback on state errors → verify: `TestGuidanceLegacyState` and `TestGuidanceStateFailureFallback`, including false values, symlinks, oversized/corrupt files and missing identity.
- [ ] 3.3 Update `ResetGuidanceFile` for exact legacy/new paths without touching other sessions or observation locks → verify: `TestGuidanceResetIsolation` and killed-winner behavior; remove only the expected fixture paths.
- [ ] 3.4 Add synchronized process-level coverage for Codex and regression coverage for shared Claude advisories → verify: `TestCodexGuidanceConcurrentCLI` yields exactly one advisory from 32 healthy contenders and one per independent type/session pair.

## Task 4: Apply observed-success HTTP routing to Codex main sessions

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Main-agent contract](design-v2.md#7-main-agent-availability-through-observed-success), [implementation](implementation-v2.md#5-task-4--one-main-agent-http-redirect-per-observed-success)

### Subtasks

- [ ] 4.1 Parameterize `internal/hook/observations.go` narrowly for the separate Codex-main file/lock, reusing the existing bounded transaction algorithm while retaining child format and validation → verify: `TestCodexMainObservationStore`, existing child expiry/cap/contention tests, and independent 128-entry/64-KiB bounds per scope.
- [ ] 4.2 Thread explicit platform identity to PostToolUse and record only stable root Codex `capy_execute` successes → verify: `TestCodexMainObservationPayloads` covers aliases, serialization, failure/cancellation, missing/type-only/child identities, fetch/other-tool exclusions and Claude root no-op.
- [ ] 4.3 Update `routeBash` after security and safe-download checks to consume root evidence once, otherwise return discovery/native advisory; preserve child/Claude handling → verify: `TestCodexMainRoutingEvidence`, `TestCodexMainRoutingSecurityPrecedence`, `TestCodexMainConcurrentRedirect` and `TestCodexMainChildIsolation` cover the complete root lifecycle, race/failure paths and unchanged policy provenance.
- [ ] 4.4 Extend root Codex SessionEnd for independent cleanup of both scopes, preserving permanent locks and database-free behavior → verify: `TestCodexMainSessionEnd` covers sibling retention, one-scope failure, child-labelled end, absent state and missing identity.
- [ ] 4.5 Synchronize `internal/hook/routing.go`, `internal/platform/routing.go` and committed `.capy/AGENTS.md`, then drive setup's generated commands from a subdirectory → verify: `TestCodexMainObservationLifecycle`, existing child/MCP/owner fixtures, 4,096-byte context bounds and all generated-artifact guards.

Task 4 integrates routing after the final launch, transport and shared-guidance
contracts are established. The store and its producer/consumer are one complete
slice; do not commit a main-ready flag without its guarded consumer or weaken
child validation as a shortcut.

## Task 5: Verify and document v2 as an installed integration

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Release verification](implementation-v2.md#6-task-5--release-verification-and-documentation), [rollout](design-v2.md#8-rollout-and-development-checkouts)

### Subtasks

- [ ] 5.1 Run `kk:test` with FTS5, synthetic keys and isolated capy configuration → verify: full suite, focused adapter/hook/platform/CLI races, vet and artifact guards pass; document actual limits in `verification-v2.md`; benchmark only if required implementation areas changed.
- [ ] 5.2 Exercise the final PATH-based installation and normal review/re-trust in a disposable recorded Codex runtime from a subdirectory → verify: direct hook/MCP launch, actual main advisory → successful execute → one redirect → advisory sequence, policy deny/ask controls, child isolation and compact guidance. Retain selected host evidence with a `-v2` filename and distinguish controlled transport probes from host delivery.
- [ ] 5.3 Run `kk:document` for README, architecture, AGENTS artifact/state invariants and relevant CLI help → verify: PATH prerequisites, single final format, first/unobserved-call behavior, security precedence, older shared-guidance JSON, restart/re-trust and platform limitations agree with evidence; generators and committed artifacts match; historical v1 records remain intact.
- [ ] 5.4 Run isolated `kk:review-code` with Go input → verify: findings fixed or durably dispositioned in a v2-named review record and linked from `verification-v2.md`.
- [ ] 5.5 Run isolated `kk:review-spec` explicitly against all three `-v2` documents, with v1 as historical/inherited context where unchanged → verify: every claimed behavior has evidence, no interim compatibility or deferred functionality is claimed complete, and only then mark these tasks done.

## Dependency Graph

```text
Task 1 --> Task 2 --> Task 4 --> Task 5
Task 1 --> Task 3 --> Task 4
```

Parallel markers describe dependency independence, not permission to spawn
agents or make overlapping edits. The original design invocation produced
documents only; subsequent implementation and review are recorded above.
No release publishing or commit is implied.
