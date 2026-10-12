# Tasks: capy-owned Codex hooks

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Status: pending — revised after reviews; implementation not started
> Created: 2026-10-12
> Review dispositions: [review-resolution.md](review-resolution.md)
> Readiness: [RD-1 correction and re-review](readiness-review.md#resolution-and-re-review)
> Not Doing: output capture/budgets, native Codex policy emulation, hook trust mutation, toolbox/global/plugin hook migration, overlapping installations, database changes, hosted web interception, child argument rewriting, universal runtime support

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
- [ ] 1.3 Carry source-file metadata through `internal/security/settings.go`, `eval.go` and both command/embedded-command hook paths; pass an ask reason to the adapter, retaining Claude's response shape and deny precedence. Codex denials name the exact pattern and source file; an unmatched default ask still passes → verify: `TestCommandDecisionProvenance`, `TestCodexPolicyResponses`, `TestCodexMalformedPreToolUse` and existing policy tests.
- [ ] 1.4 Add platform-accurate startup routing through `internal/hook/routing.go`; treat input rewriting as deliberately unused and do not build a PermissionRequest bridge → verify: `TestCodexSessionStartGuidance` asserts supported output and at most 4,096 context bytes, plus a credential-free CLI invocation.
- [ ] 1.5 Run focused adapter/hook/CLI checks with FTS5 and preserve existing Claude payload/response expectations → verify: no Claude regression and no hook database creation.

## Task 2: Bind hook and MCP launch to the same portable owner

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Owner-bound launch](implementation.md#31-owner-bound-hook-and-mcp-launch), [Ownership and failures](design.md#6-installation-ownership-and-failure-behavior)

### Subtasks

- [ ] 2.1 Add `internal/platform/setup_codex_launch.go` with a stable versioned `.codex/capy-owner` ID and exclusive creation. Add shared bounded config/marker inspection in `internal/platform/codex_owner.go`; decode JSON/TOML and validate each generated binding against its adjacent marker → verify: `TestCodexOwnerMarker` and `TestCodexOwnerBindings` cover concurrent setup, preservation, missing/invalid artifacts, equivalent escaped commands, unsupported invocation versions and valid unrelated configs.
- [ ] 2.2 Add `cmd/capy/codex_launch.go`, register it in `main.go`, and generate a bootstrap that locates/supervises capy before any project wrapper. Reuse the wrapper's binary-search fragment. Scan all physical ancestors for the union of marker and binding directories; require exactly one valid owner with the invoked mode/event binding → verify: `TestCodexLaunchLocations` covers ordinary subdirectories, moves, spaces, sibling worktrees and non-Git command-only cases; `TestCodexOverlappingInstallations` rejects distinct/copied IDs before policy/state/DB access.
- [ ] 2.3 Update generated and committed Codex MCP argv to use the same bootstrap/ID and explicit project owner while preserving credential forwarding → verify: `TestCodexMCPLaunchLocations` initializes MCP, lists tools and runs a harmless capy call with synthetic keys at each supported location.
- [ ] 2.4 Supervise bootstrap/Go-launcher/wrapper errors. Codex PreToolUse failures must exit exactly 2 with nonempty repair text; structured denials still exit 0. Preserve Claude behavior, serve status, stdin and cancellation/signal propagation → verify: `TestCodexBootstrapFailure` and `TestWrapperCodexHookFailure` cover missing, unreadable, vanished wrappers, outside-tree cwd, missing/incompatible launcher binaries and crashes without orphaned servers.
- [ ] 2.5 Synchronize both wrappers, the owner marker and `.codex/config.toml`; add parameterized marker coverage without rotating IDs in temporary-copy tests → verify: generated-artifact equality, idempotency and completeness checks pass.
- [ ] 2.6 Add `TestCodexCopiedOwnerMissingMarker`: in one Git repository copy root's `.codex` to `packages/service` with the same ID, retain both hook/MCP definitions and delete only the outer marker. Invoke both pre-tool commands and both MCP commands from the nested cwd → verify: pre-tool exit 2 with nonempty outer-binding repair text; MCP diagnostic failure without stdout; neither wrapper invoked and no policy/state/DB access. Repeat with only the outer hook binding or only its MCP binding retained, and escaped equivalent config strings.

## Task 3: Register main-session hooks without taking over external hooks

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Hook registration](implementation.md#32-hook-registration-and-coexistence), [Merge ownership](design.md#62-merge-ownership)

### Subtasks

- [ ] 3.1 Add `internal/platform/setup_codex_hooks.go` with SessionStart/PreToolUse descriptors, anchored matchers and owner-bound commands; omit `additionalContextLimit` → verify: `TestCodexHookMatchers` and `TestCodexHookContextConfiguration`.
- [ ] 3.2 Merge only this installation's owned handlers, preserving mixed siblings and metadata; reject malformed or mismatched bindings and collapse owned duplicates → verify: `TestMergeCodexHooks` and repeat-setup checks.
- [ ] 3.3 Wire the merge into setup and explain both project-layer and exact-hook trust gates without changing trust, feature overrides or other hooks → verify: `TestSetupCodexHooks` covers Codex-only/default-both and incomplete installations.
- [ ] 3.4 Update committed `.codex/hooks.json` and drift coverage, preserve toolbox entries, and record projected/actual duplicate startup bytes with the follow-up link in `verification.md` → verify: all artifact checks and a mixed startup fixture retaining provider/extra instructions.

## Task 4: Complete child guidance and observation lifecycle

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Child lifecycle](implementation.md#4-child-guidance-evidence-and-cleanup), [Event coverage](design.md#4-event-coverage-and-routing)

### Subtasks

- [ ] 4.1 Add SubagentStart dispatch and correct event-specific context formatting; register all child types → verify: `TestCodexSubagentStart` includes the 4,096-byte context bound.
- [ ] 4.2 Register PostToolUse and SessionEnd and reuse shared evidence/cleanup with namespaced parent identity → verify: `TestCodexChildObservationLifecycle` drives the installed CLI and removes only its session's entries.
- [ ] 4.3 Add Codex success/failure/identity fixtures without weakening TTL, caps, lock or one-use consumption → verify: `TestCodexObservationPayloads` and existing observation race/expiry/contention tests.
- [ ] 4.4 Preserve native child arguments; describe Bash/MCP coverage accurately across runtime and generated instructions → verify: `TestCodexNoAgentArgumentRewrite`, routing tests and generated-artifact checks.
- [ ] 4.5 Synchronize the complete five-event catalog with committed `.codex/hooks.json` → verify: repeated setup preserves external entries and exactly one owned handler per event.

## Task 5: Diagnose Codex registration on both doctor interfaces

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 4
- **Docs:** [Shared diagnostics](implementation.md#51-shared-diagnostics), [Diagnostic limits](design.md#7-diagnostics-and-verification)

### Subtasks

- [ ] 5.1 Add shared platform-labelled checks in `internal/platform/doctor_codex.go` using the runtime owner inspector for hook/MCP binding, adjacent markers and overlap; check wrapper/matchers using the event catalog → verify: `TestDoctorPlatformRegistrations` covers single/dual/no/incomplete/misbound installations and agrees with runtime/setup on copied IDs with a missing outer marker.
- [ ] 5.2 Report visible feature disabling and inline-source overlap, while explicitly leaving project trust, hook trust and execution unverified → verify: `TestDoctorCodexActivationLimits`.
- [ ] 5.3 Wire shared results into `cmd/capy/doctor.go` and `internal/server/tool_doctor.go` without changing credential/vault checks → verify: CLI/MCP Codex-only fixtures agree and do not fail for absent Claude configuration.

## Task 6: Verify the installed runtime and document the release

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Live acceptance](implementation.md#52-live-acceptance-and-release-checks), [Rollback](implementation.md#53-rollback), [Follow-ups](implementation.md#6-follow-ups)

### Subtasks

- [ ] 6.1 Verify a trusted disposable installation in an actual recorded Codex version, including an ordinary subdirectory host launch and initialized MCP: startup, allowed/denied markers, ask pattern/source, child evidence/redirect/fallback and unrelated hooks → verify: record results/limits in `verification.md`, including actual duplicate context bytes and no spill of capy's compact guidance.
- [ ] 6.2 Run `$kk:test` for the FTS5 full suite, focused race checks and generated-artifact guards → verify: record results and resolve failures; run quality benchmarks only if implementation touches their required areas.
- [ ] 6.3 Run `$kk:document` for README, architecture, CLI help, AGENTS.md marker/generator/wrapper invariants and historical ADR clarification. Include collaborator repair via external terminal or `/hooks`, two trust gates, config/marker validation, overlap rules and non-Git discovery limits. Require stopping/reloading affected sessions before removing/moving loaded installations → verify: docs agree with the tested failure/recovery path and retain policy-denial protections.
- [ ] 6.4 Run `$kk:review-code` with Go input → verify: fix findings or record actionable dispositions durably.
- [ ] 6.5 Run `$kk:review-spec` against all feature documents → verify: each promised behavior has evidence and no output-budget or toolbox-PR completion is claimed.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3
Task 3 -> Task 4 -> Task 6
Task 3 -> Task 5 -> Task 6
```

Task 5 uses the event catalog generically so Task 4 can extend it without
duplicating a list in diagnostics. Parallel markers describe dependencies, not
authorization to spawn agents or make concurrent overlapping edits.

## Follow-ups outside feature completion

- [ ] **Claude-toolbox enhancement PR:** after capy's integration is verified,
  explain its ownership of instruction injection and supported HTTP routing;
  propose optional cleanup with a migration path for users lacking the new
  hooks. Preserve unrelated provider instructions and file restrictions. See
  [the follow-up brief](implementation.md#claude-toolbox-enhancement-pr) and
  [the measured duplication baseline](verification.md#startup-context-baseline).
- [ ] **#132 output policy:** separately reproduce and measure routine command
  output, exit status and full-log recovery before changing guidance/capture.
  Hook support alone is not a closing condition for #132.
- [ ] **#29 tracking:** reopen or supersede the incorrectly closed hook issue
  and link the completed design/implementation when updating GitHub tracking.
  No external issue or PR was changed during design.
