# Tasks: Codex hooks v2

> Design: [design-v2.md](design-v2.md)
> Implementation: [implementation-v2.md](implementation-v2.md)
> Status: pending — design drafted; implementation and independent review pending
> Created: 2026-10-12
> Baseline: capy `ac60b81` (same source tree as comparison commit `a58f90d`); v1 [tasks](tasks.md) remain done
> Confirmed choice: unverified main-session HTTP tool availability allows advisory fallback; recent suitable success permits one redirect
> Not Doing: new hook events, session continuity, noisy-build/output-budget policy, external-MCP guidance, trivial-command exemptions, readiness sentinels/daemons, trust mutation, database changes, new config knobs/dependencies, command rewriting, universal runtime support
> Implementation entry: explicitly select `tasks-v2.md`; do not resume the completed unsuffixed task list

## Task 1: Bound Codex stdin through the direct and generated hook paths

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Strategy:** Risk-First — prove inherited-pipe deadlines and exit behavior before changing routing
- **Docs:** [Input contract](design-v2.md#4-bounded-codex-stdin), [implementation](implementation-v2.md#2-task-1--bounded-codex-input-through-the-installed-cli)

### Subtasks

- [ ] 1.1 Add `internal/hook/input.go` with named ordinary/SessionEnd idle, total and byte budgets, owned deadline-capable pipe reads, and bounded regular-file support → verify: `TestCodexHookInputDescriptors` proves actual pipe behavior and closes all owned descriptors without a lingering read goroutine.
- [ ] 1.2 Wire Codex-only admission into `internal/hook/hook.go:Run` before handlers; require EOF, reject overflow/stall, and preserve Claude input handling → verify: `TestCodexHookInputBudgets` and `TestCodexHookInputNoSideEffects` cover boundaries, trickle input, complete invalid JSON and no policy/state/DB access on transport failure.
- [ ] 1.3 Map typed input failures in `cmd/capy/hook.go` through existing `exitError`; preserve wrapper/launcher and structured-denial contracts → verify: `TestCodexHookInputExitCodes` checks direct/generated pre-tool exit 2 + stderr + empty stdout, other event errors and valid/invalid completed payloads.
- [ ] 1.4 Exercise production budgets through the installed path and document descriptor/platform limits → verify: ordinary idle/trickle and short SessionEnd subprocess probes terminate with their writers open; normal EOF has no artificial delay; baseline launch/signal/Claude fixtures pass.

## Task 2: Atomically claim existing session guidance

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Docs:** [Guidance contract](design-v2.md#5-atomic-guidance-claims), [implementation](implementation-v2.md#3-task-2--atomic-once-per-session-guidance)

### Subtasks

- [ ] 2.1 Replace new JSON read–modify–writes in `internal/hook/guidance.go` with exclusive zero-byte markers for the three fixed types, preserving `sessionIDComponent` → verify: `TestGuidanceAtomicClaim` and all existing traversal/component isolation cases.
- [ ] 2.2 Recognize bounded regular legacy JSON state read-only, retaining known-key membership behavior; validate existing markers and keep advisory fallback on state errors → verify: `TestGuidanceLegacyState` and `TestGuidanceStateFailureFallback`, including false values, symlinks, oversized/corrupt files and missing identity.
- [ ] 2.3 Update `ResetGuidanceFile` for exact legacy/new paths without touching other sessions or observation locks → verify: `TestGuidanceResetIsolation` and killed-winner behavior; remove only the expected fixture paths.
- [ ] 2.4 Add synchronized process-level coverage for Codex and regression coverage for shared Claude advisories → verify: `TestCodexGuidanceConcurrentCLI` yields exactly one advisory from 32 healthy contenders and one per independent type/session pair.

## Task 3: Apply observed-success HTTP routing to Codex main sessions

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Main-agent contract](design-v2.md#6-main-agent-availability-through-observed-success), [implementation](implementation-v2.md#4-task-3--one-main-agent-http-redirect-per-observed-success)

### Subtasks

- [ ] 3.1 Parameterize `internal/hook/observations.go` narrowly for the separate Codex-main file/lock, reusing the existing bounded transaction algorithm while retaining child format and validation → verify: `TestCodexMainObservationStore`, existing child expiry/cap/contention tests, and independent 128-entry/64-KiB bounds per scope.
- [ ] 3.2 Thread explicit platform identity to PostToolUse and record only stable root Codex `capy_execute` successes → verify: `TestCodexMainObservationPayloads` covers aliases, serialization, failure/cancellation, missing/type-only/child identities, fetch/other-tool exclusions and Claude root no-op.
- [ ] 3.3 Update `routeBash` after security and safe-download checks to consume root evidence once, otherwise return discovery/native advisory; preserve child/Claude handling → verify: `TestCodexMainRoutingEvidence`, `TestCodexMainRoutingSecurityPrecedence`, `TestCodexMainConcurrentRedirect` and `TestCodexMainChildIsolation` cover the complete root lifecycle, race/failure paths and unchanged policy provenance.
- [ ] 3.4 Extend root Codex SessionEnd for independent cleanup of both scopes, preserving permanent locks and database-free behavior → verify: `TestCodexMainSessionEnd` covers sibling retention, one-scope failure, child-labelled end, absent state and missing identity.
- [ ] 3.5 Synchronize `internal/hook/routing.go`, `internal/platform/routing.go` and committed `.capy/AGENTS.md`, then drive setup's generated commands from a subdirectory → verify: `TestCodexMainObservationLifecycle`, existing child/MCP/owner fixtures, 4,096-byte context bounds and all generated-artifact guards.

Task 3's dependencies serialize final handler integration after the transport and
shared-guidance contracts are established. The store and its producer/consumer
are one complete slice; do not commit a main-ready flag without its guarded
consumer or weaken child validation as a shortcut.

## Task 4: Verify and document v2 as an installed integration

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Release verification](implementation-v2.md#5-task-4--release-verification-and-documentation), [compatibility](design-v2.md#7-compatibility-rollout-and-rollback)

### Subtasks

- [ ] 4.1 Run `kk:test` with FTS5, synthetic keys and isolated capy configuration → verify: full suite, focused adapter/hook/platform/CLI races, vet and artifact guards pass; document actual limits in `verification-v2.md`; benchmark only if required implementation areas changed.
- [ ] 4.2 Exercise normal trust in a disposable recorded Codex runtime from a subdirectory → verify: actual main advisory → successful execute → one redirect → advisory sequence, policy deny/ask controls, child isolation, usable MCP and compact guidance. Retain selected host evidence with a `-v2` filename and distinguish controlled transport probes from host delivery.
- [ ] 4.3 Run `kk:document` for README, architecture, AGENTS state invariants and relevant CLI help → verify: first/unobserved-call behavior, security precedence, legacy markers, restart/rollback and platform limitations agree with evidence; generators and committed artifacts match; v1 records remain intact.
- [ ] 4.4 Run isolated `kk:review-code` with Go input → verify: findings fixed or durably dispositioned in a v2-named review record and linked from `verification-v2.md`.
- [ ] 4.5 Run isolated `kk:review-spec` explicitly against all three `-v2` documents, with v1 as inherited context → verify: every claimed behavior has evidence, no deferred functionality is claimed complete, and only then mark these tasks done.

## Dependency Graph

```text
Task 1 --+
         +--> Task 3 --> Task 4
Task 2 --+
```

Parallel markers describe dependency independence, not permission to spawn
agents or make overlapping edits. This design invocation produces documents
only; it does not start Task 1, run independent review, or publish a release.
