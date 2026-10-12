# Tasks: capy-owned Codex hooks

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Status: pending — design review required before implementation
> Created: 2026-10-12
> Not Doing: output capture/budgets, native Codex policy emulation, hook trust mutation, toolbox/global/plugin hook migration, database changes, hosted web interception, child argument rewriting, universal runtime support

## Task 1: Run main-session Codex hooks through the Go CLI

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove host response and identity semantics first
- **Docs:** [Main-session hook path](implementation.md#2-main-session-hook-path), [Adapter contracts](design.md#5-adapter-policy-and-state-contracts)

### Subtasks

- [ ] 1.1 Add `internal/adapter/codex.go` and explicit adapter platform identity; use supplied session/child IDs with a `codex:` session namespace and no Claude/transcript/PID fallback → verify: `TestCodexAdapterIdentity`.
- [ ] 1.2 Add hook-local `--platform` selection in `cmd/capy/hook.go` and platform-aware `internal/hook/context.go` resolution, retaining Claude defaults and cwd policy anchoring → verify: `TestHookCodexDispatch` and `TestHookContextCodexIgnoresClaudeEnvironment`.
- [ ] 1.3 Format supported Codex denials/advisories; turn matched asks into explanatory denials and reject malformed pre-tool command input before state writes → verify: `TestCodexPolicyResponses`, `TestCodexMalformedPreToolUse`, and existing compound-policy tests.
- [ ] 1.4 Add platform-accurate startup routing through `internal/hook/routing.go` and the existing session-start path → verify: `TestCodexSessionStartGuidance` plus a credential-free synthetic CLI invocation.
- [ ] 1.5 Run focused adapter/hook/CLI checks with FTS5 and preserve existing Claude payload/response expectations → verify: no Claude regression and no hook database creation.

## Task 2: Install portable capy-owned main-session hooks

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Generated installation](implementation.md#3-generated-installation), [Ownership and failures](design.md#6-installation-ownership-and-failure-behavior)

### Subtasks

- [ ] 2.1 Add `internal/platform/setup_codex_hooks.go` with Codex event descriptors, anchored matchers and one portable command generator → verify: `TestCodexHookMatchers` and `TestCodexHookCommandLocations` cover nested, moved, non-Git and worktree paths.
- [ ] 2.2 Merge owned handlers while preserving other groups, mixed-group siblings and unknown metadata; reject malformed/ambiguous documents without replacing them → verify: `TestMergeCodexHooks` includes duplicates and repeated setup.
- [ ] 2.3 Wire installation into `SetupCodex` and `cmd/capy/setup.go`, reporting registration and the required Codex trust step without changing trust/feature overrides → verify: `TestSetupCodexHooks` for Codex-only/default-both configuration.
- [ ] 2.4 Add Codex-specific launch-failure behavior to `capyWrapperScript`; synchronize both committed wrappers while preserving Claude and non-hook behavior → verify: `TestWrapperCodexHookFailure` and existing wrapper exit-code tests.
- [ ] 2.5 Add committed `.codex/hooks.json` entries, preserve the current external hooks, and register the file in the drift guard → verify: all three generated-artifact/idempotency/completeness checks pass.

## Task 3: Complete child guidance and observation lifecycle

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 4
- **Docs:** [Child lifecycle](implementation.md#4-child-guidance-evidence-and-cleanup), [Event coverage](design.md#4-event-coverage-and-routing)

### Subtasks

- [ ] 3.1 Add SubagentStart dispatch and correct event-specific context formatting through the adapter boundary; register all child types → verify: `TestCodexSubagentStart`.
- [ ] 3.2 Register PostToolUse and SessionEnd and reuse the shared evidence/cleanup handlers with namespaced parent identity → verify: `TestCodexChildObservationLifecycle` drives the installed CLI and removes only its session's entries.
- [ ] 3.3 Add Codex success/failure/identity fixtures without weakening TTL, caps, lock or one-use consumption → verify: `TestCodexObservationPayloads` and existing observation race/expiry/contention tests.
- [ ] 3.4 Preserve native child arguments; describe Bash/MCP coverage accurately across runtime and generated instructions → verify: `TestCodexNoAgentArgumentRewrite`, routing tests and generated-artifact checks.
- [ ] 3.5 Synchronize the complete five-event catalog with committed `.codex/hooks.json` → verify: repeated setup preserves external entries and exactly one owned handler per event.

## Task 4: Diagnose Codex registration on both doctor interfaces

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 3
- **Docs:** [Shared diagnostics](implementation.md#51-shared-diagnostics), [Diagnostic limits](design.md#7-diagnostics-and-verification)

### Subtasks

- [ ] 4.1 Add shared platform-labelled checks in `internal/platform/doctor_codex.go` for hooks, wrapper, matchers and the Codex MCP table, deriving expected events from the catalog → verify: `TestDoctorPlatformRegistrations` covers single/dual/no/incomplete installations.
- [ ] 4.2 Report visible feature disabling and inline-source overlap, while explicitly leaving trust/execution unverified → verify: `TestDoctorCodexActivationLimits`.
- [ ] 4.3 Wire shared results into `cmd/capy/doctor.go` and `internal/server/tool_doctor.go` without changing credential/vault checks → verify: CLI/MCP Codex-only fixtures agree and do not fail for absent Claude configuration.

## Task 5: Verify the installed runtime and document the release

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Live acceptance](implementation.md#52-live-acceptance-and-release-checks), [Rollback](implementation.md#53-rollback), [Follow-ups](implementation.md#6-follow-ups)

### Subtasks

- [ ] 5.1 Verify a trusted disposable installation in an actual recorded Codex version: startup, allowed/denied markers, matched ask, child evidence/redirect/fallback and an unrelated hook → verify: record observed results and limitations in `verification.md`, distinguishing host execution from synthetic stdin replay.
- [ ] 5.2 Run `$kk:test` for the FTS5 full suite, focused race checks and generated-artifact guards → verify: record results and resolve failures; run quality benchmarks only if implementation touches their required areas.
- [ ] 5.3 Run `$kk:document` for README, architecture, CLI help, AGENTS.md generator/wrapper invariants and any historical ADR clarification → verify: documented events, activation, policy limits and rollback match the implementation.
- [ ] 5.4 Run `$kk:review-code` with Go input → verify: fix findings or record actionable dispositions durably.
- [ ] 5.5 Run `$kk:review-spec` against all feature documents → verify: each promised behavior has implementation evidence and no output-budget or toolbox-PR completion is claimed.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3 --+
                 -> Task 4 --+-> Task 5
```

Task 4 uses the event catalog generically so Task 3 can extend it without
duplicating a list in diagnostics. Parallel markers describe dependencies, not
authorization to spawn agents or make concurrent overlapping edits.

## Follow-ups outside feature completion

- [ ] **Claude-toolbox enhancement PR:** after capy's integration is verified,
  explain its ownership of instruction injection and supported HTTP routing;
  propose optional cleanup with a migration path for users lacking the new
  hooks. Preserve unrelated provider instructions and file restrictions. See
  [the follow-up brief](implementation.md#claude-toolbox-enhancement-pr).
- [ ] **#132 output policy:** separately reproduce and measure routine command
  output, exit status and full-log recovery before changing guidance/capture.
  Hook support alone is not a closing condition for #132.
- [ ] **#29 tracking:** reopen or supersede the incorrectly closed hook issue
  and link the completed design/implementation when updating GitHub tracking.
  No external issue or PR was changed during design.
