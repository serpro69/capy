# Implementation: capy-owned Codex hooks

> Design: [design.md](design.md) · Tasks: [tasks.md](tasks.md)
> Status: done — Tasks 1–6 implemented, verified and reviewed; see [verification limits](verification.md#task-6-installed-runtime-and-release-documentation)
> Baseline inspected: 2026-10-12, Codex CLI 0.162.1
> Revisions: [review-resolution.md](review-resolution.md)

## 1. Orientation and implementation boundaries

Capy hooks are short-lived Go CLI processes: stdin JSON enters
`cmd/capy/hook.go`, an adapter parses it, `internal/hook` selects the project,
evaluates policy and routes the event, and an adapter formats stdout JSON.
There is no long-lived hook server. Guidance and child observations live in
small files under the selected project's `.capy/` directory; hooks do not open
the encrypted knowledge or vault database.

`SetupClaudeCode` and `SetupCodex` live in `internal/platform/setup.go`. The
Claude installer merges six events; the pre-feature Codex installer installed no
hooks. Tasks 1–5 now install all five Codex events described below.
`capyWrapperScript` generates both committed wrappers. The artifact
tests enforce agreement between generators and committed configuration.

Use these existing components:

- `internal/adapter/{adapter,claudecode}.go`: adapter interface, event type,
  Claude transport behavior. Keep Claude's public behavior stable.
- `internal/hook/{hook,context,pretooluse,routing}.go`: event dispatch,
  project selection, command/MCP policy and injected guidance.
- `internal/hook/{posttooluse,observations,sessionend,guidance}.go`: child
  evidence and session state. Preserve existing caps, locking and expiry.
- `internal/platform/{setup,doctor}.go`: installation and shared checks.
- New `internal/platform/codex_owner.go`: bounded, read-only inspection of
  ancestor markers and decoded hook/MCP bindings, shared by launch/setup/doctor.
- New `cmd/capy/codex_launch.go`: internal launch command registered in
  `main.go`; validates ownership before invoking the selected project wrapper.
- `internal/security/{settings,eval}.go`: policy loading and match results;
  add rule-source provenance without changing matching precedence.
- `cmd/capy/{setup,doctor}.go` and `internal/server/tool_doctor.go`: user-facing
  setup and diagnostics; both doctor surfaces must agree.
- `internal/platform/setup_artifacts_drift_test.go`: mandatory registration of
  `.codex/hooks.json` and `.codex/config.toml` as merged artifacts, plus
  wrapper/routing equality and the new parameterized `.codex/capy-owner` marker.

Implementation code below is described, not supplied. New test names are
proposed acceptance names. Test packages must use `-tags fts5`; isolate global
settings and use the synthetic `CAPY_DB_KEY` and `CAPY_VAULT_KEY` required by
AGENTS.md. Hook-specific subprocess fixtures must also pass with both keys
unset, proving that the hook path does not initialize a database.
MCP location probes instead use synthetic keys and an isolated database: a
successful hook process is not evidence that its suggested MCP tool is usable.

## 2. Main-session hook path

**Goal:** invoking `capy hook --platform codex pretooluse` or `sessionstart`
directly exercises a complete usable Codex path before installation is added.

Task 1's direct CLI path now exists. `--platform` is local to `hook` and defaults
to `claude-code`; Codex accepts `pretooluse`, `sessionstart`, `subagentstart`, `posttooluse`
and `sessionend` after Task 4 added the child lifecycle. Unsupported events fail
explicitly before state or policy access. Use an explicit `--project-dir` when invoking hooks manually from a
different directory; otherwise validated payload cwd is the Codex owner, with
process-cwd project discovery as fallback. No generated installation is claimed
by this task. Results are tracked in [verification.md](verification.md#task-1-main-session-cli).

1. Add `CodexAdapter` in `internal/adapter/codex.go`. Extend the adapter
   boundary with explicit platform identity; preserve the current default
   Claude selection and update adapter test doubles mechanically. Parse only
   the verified transport fields, including nullable transcript paths, while
   treating the nonempty supplied session ID as authoritative. Prefix stable
   state identities with `codex:`; never use transcript/environment/PID
   fallbacks for Codex. → verify: `TestCodexAdapterIdentity` covers root/child
   payloads, a different UUID in the transcript path, absent identity and
   inherited Claude environment variables.
2. Add the hook-local platform flag in `cmd/capy/hook.go`, validate the allowed
   platform/event combinations, and make platform-aware context selection in
   `internal/hook/context.go`. Explicit project selection wins; Codex ignores
   `CLAUDE_PROJECT_DIR`. Preserve payload-cwd rule anchoring and existing Claude
   precedence. → verify: `TestHookCodexDispatch` and
   `TestHookContextCodexIgnoresClaudeEnvironment` drive the CLI/router with
   different process, explicit-project and payload directories.
3. Preserve provenance in `SecurityPolicy` and `CommandDecision` with a
   source-file field populated by `readSingleSettings`. Keep the first matched
   ask result in both `checkCommandsSecurity` and `checkCodeSecurity` while
   continuing to look for a later deny/scanner error. Change the adapter ask
   formatter to accept a reason; Claude retains its existing JSON shape, while
   Codex reports the matched pattern, actual settings path and unsupported
   approval requirement. Use an explicit in-memory label only for policies
   without a file source. Keep shared compound-request deny precedence; no
   Codex response should contain
   `permissionDecision: ask`, a bare permission grant, or an implicit command
   replacement. Input rewriting is host-supported but deliberately unused;
   reject accidental use of the unimplemented adapter path without claiming a
   host limitation. Do not install PermissionRequest as an ask bridge; it cannot
   be triggered from PreToolUse. → verify: `TestCodexPolicyResponses` and
   `TestCommandDecisionProvenance` cover all three settings sources, identical
   patterns in different sources, embedded/compound commands, a later deny,
   scanner failure and the unmatched default ask that must still pass through.
4. Adapt the malformed-input dispatch branch for Codex only: pre-tool parse or
   required command-shape errors deny before state mutation. Retain Claude's
   existing malformed-input handling. → verify: `TestCodexMalformedPreToolUse`
   asserts a valid denial and no observation/guidance files; existing Claude
   hook tests retain their prior expectations.
5. Inject platform-accurate startup guidance from `internal/hook/routing.go`.
   Share the comprehension/extraction and tool-availability content, but state
   Codex's actual Bash/MCP coverage and avoid claiming interception of hosted
   web calls or child-type rewriting. Keep the old `RoutingBlock` entry point
   as the Claude-compatible surface if extracting common fragments. → verify:
   `TestCodexSessionStartGuidance` checks the event-specific wrapper and actual
   supported routes and asserts at most 4,096 UTF-8 bytes of additionalContext;
   Claude startup/routing tests remain unchanged.

Run adapter and hook packages plus targeted CLI tests. This slice is complete
when a synthetic stdin invocation reaches the real CLI and produces supported
JSON without credentials or a database.

## 3. Generated installation

**Goal:** hooks and MCP launch through the same portable owner binding. Split
this expanded work into Task 2 (launch path) and Task 3 (hook registration),
then extend the event catalog to children in Task 4.

### 3.1 Owner-bound hook and MCP launch

1. Add owner-marker helpers and the common launch generator in
   `internal/platform/setup_codex_launch.go`. `.codex/capy-owner` has the exact
   format `capy-codex-owner-v1:<32 lowercase hex characters>` followed by one
   newline. Generate 16 random bytes once with the Go standard library. Reject
   malformed existing markers; never rotate a valid ID during setup. Use
   exclusive creation and re-read/validate the winning marker if creation
   races. If bound hook/MCP definitions already exist but the marker is missing,
   require restoration instead of assigning a new ID; today's unbound legacy
   MCP block can migrate normally. → verify: `TestCodexOwnerMarker` covers
   stable reruns, malformed/symlink/missing-live markers, legacy migration and
   concurrent creation cases, with a single consistent ID.
2. Add the shared inspector in `internal/platform/codex_owner.go` and internal
   `capy codex-launch` entry point in `cmd/capy/codex_launch.go`, registered in
   `main.go`. Follow design §6.1's complete ancestor scan, including ancestors
   beyond Git boundaries. Inspect markers (80-byte bound), hooks JSON and config
   TOML (1 MiB each, with overflow detection), decoding with the standard JSON
   package and existing TOML dependency. Reject unreadable/malformed/nonregular/
   symlink inputs and symlink `.codex` directories; distinguish absent files
   from failed inspection. Collect directories from the **union of markers and
   generated bindings**, including inline hooks and MCP aliases. Validate each
   binding against its adjacent marker; never deduplicate directories by ID or
   return early at a nested match. Require one directory with the requested ID
   and mode/event binding. Reuse the same decoded canonical-invocation recognizer
   in setup and doctor; reject recognizable invalid/unsupported invocations.
   Valid unrelated config does not require a marker. → verify:
   `TestCodexOwnerBindings` covers JSON escapes/TOML quoting, partial artifacts,
   marker-only stale commands, malformed/oversized/unreadable/symlink inputs,
   invalid versions and marker-free unrelated config. Verify no policy, state,
   credential resolution or DB access and no consumption of event stdin.
3. Generate one inline bootstrap for hook and serve modes, with the versioned
   `capy-codex-launch-v1` identifier, owner ID and mode/event passed as data.
   Extract the existing wrapper binary-search fragment for reuse; the bootstrap
   locates capy and supervises `codex-launch` without running a project wrapper
   first. Only after the complete Go preflight succeeds may the launcher invoke
   the owner's readable regular wrapper with explicit project ownership and
   unchanged process/event cwd. Missing/older launcher binaries cannot fall
   back to a nearer wrapper. → verify: `TestCodexLaunchLocations`
   runs both modes from root, ordinary subdirectories, moved trees, non-Git
   command-only fixtures, paths with spaces and sibling linked worktrees.
   Hook fixtures in Task 2 can supply canonical bindings before Task 3 wires
   hook registration into setup; MCP fixtures use the actual generated table.
4. Reject overlapping installations before wrapper invocation. Setup checks
   ancestor bindings as well as markers before creating/adopting an ID; runtime
   checks catch nested descendants when they become ancestors of launch cwd.
   Setup may create a genuinely new installation; runtime requires a binding
   for the invoked mode/event. Validate both distinct and copied IDs.
   → verify: `TestCodexOverlappingInstallations` installs different harmless
   root/nested deny rules and separate state directories, invokes both generated
   handlers and MCP, and asserts rejection with no policy/state/DB side effects.
   Remove the root wrapper and then its marker: root's command must never run
   as the nested owner. Include a copied definition with the wrong owner ID.
   `TestCodexCopiedOwnerMissingMarker` must also copy root's complete `.codex`
   into `packages/service` **within the same Git repository**, retain both
   owners' hook and MCP definitions, then delete only the outer marker. Invoke
   both pre-tool commands from the nested directory: require exact exit 2 and
   a nonempty reason naming the outer binding, with no wrapper invocation,
   policy access, observation write/consumption or database open. Both MCP
   commands must fail with stderr and no protocol stdout. Repeat retaining only
   the outer hook binding, only the outer MCP binding, and equivalent escaped
   serialized commands. Run these with a sentinel nested wrapper so rejection
   by the nested wrapper cannot masquerade as successful preflight enforcement.
5. Replace `codexMCPBlock`'s cwd-relative wrapper arguments with the same
   bootstrap and owner ID, preserving credential forwarding and unrelated
   TOML. Serve receives `--project-dir` for that owner. Keep stdout exclusively
   for MCP. Update the committed TOML and parameterize pure merge helpers so
   drift tests use the existing marker rather than generating a new ID in a
   temporary copy. → verify: `TestCodexMCPLaunchLocations` initializes the actual
   MCP server, lists tools and makes a harmless capy call from every supported
   location, using synthetic credentials; assert execution/storage owner and
   test a visible setup/runtime failure when the expected marker is absent.
6. Supervise the Go launcher in the bootstrap and wrapper invocation in the
   launcher, mapping binary-discovery/config/owner/lookup/open/launch failures
   for PreToolUse to **exit 2 with nonempty repair-oriented
   stderr**. This includes ordinary subprocess exits 1/126/127 and a wrapper
   disappearing after validation. Other hooks/MCP fail diagnostically. Add
   the corresponding narrow Codex branch to `capyWrapperScript` for missing
   binaries and failed Go hooks; keep legacy Claude behavior and serve exit
   propagation. → verify: `TestCodexBootstrapFailure` and
   `TestWrapperCodexHookFailure` assert exact status/text for missing,
   unreadable, vanished and invalid-owner wrappers, outside-tree launch,
   missing/incompatible launcher or hook binary, launcher crash, wrapper crash,
   and the successful exit-0 JSON denial. Assert event stdin reaches the final
   handler unchanged and preflight never contaminates MCP stdout. Preserve
   cancellation/signal delivery to the wrapper and server across the extra
   launcher process; subprocess checks must not leave a server running after
   the host terminates its launcher.
   Use controlled readers/launchers or unprivileged fixtures for unreadability
   and the disappearance race; do not rely on root-user permission tests.
7. Commit the marker with its generated configuration, update both wrapper
   copies, and extend artifact tests for a parameterized owner marker. Its
   serialized form must equal the generator given the preserved ID; fresh-setup
   tests separately prove creation and propagation. Do not exempt it from
   completeness coverage or expect independent installations to share a random
   value. → verify: whole-file, TOML idempotency and completeness guards pass.

### 3.2 Hook registration and coexistence

1. Add Codex event descriptors and merge helpers in
   `internal/platform/setup_codex_hooks.go`; leave the Claude catalog intact.
   Start with SessionStart and PreToolUse. Generate anchored regex matchers and
   owner-bound commands from §3.1; match every startup source. Omit
   `additionalContextLimit` from all generated handlers. → verify:
   `TestCodexHookMatchers` covers capy aliases/unrelated tools and `fork`;
   `TestCodexHookContextConfiguration` checks omitted limit fields.
2. Merge only positively identified handlers for the current installation ID;
   preserve unknown metadata, other groups and mixed-group siblings. Collapse
   owned duplicates; reject copied/mismatched bindings and malformed structures
   before writing. → verify: `TestMergeCodexHooks` covers those cases and an
   unchanged second run, including preserved independent toolbox hooks.
3. Wire the merger into `SetupCodex` and report two activation gates in
   `cmd/capy/setup.go`: project-layer trust and trust of exact hook definitions.
   Do not change platform defaults or edit feature overrides, inline/global
   hooks or trust records. → verify: `TestSetupCodexHooks` covers Codex-only,
   default-both and partial/invalid installations with a consistent marker/MCP
   binding; invalid existing files remain intact.
4. Add committed `.codex/hooks.json` entries without removing toolbox entries,
   register the merged artifact, and synchronize any shared generated routing
   wording. Record the temporary duplicate capy context and follow-up in
   `verification.md`; preserving it is deliberate pending upstream cleanup.
   → verify: all three drift checks pass; the mixed startup fixture records
   actual context sizes while retaining provider/extra instructions.

Shell/bootstrap text owns binary discovery and process-failure handling; Go
owns bounded config/marker inspection before wrapper selection. Neither becomes
a second policy evaluator. Non-Git fixture success proves command
resolution only; automatic Codex discovery there remains unverified.

## 4. Child guidance, evidence and cleanup

**Goal:** installed Codex hooks cover a complete child lifecycle with the
existing observation safeguards.

1. Add Codex `SubagentStart` dispatch and event-specific context formatting.
   Extend the adapter's startup formatting boundary with the minimum method
   needed to emit that event; do not route it through a SessionStart-labelled
   response. Register it for all child types. → verify:
   `TestCodexSubagentStart` checks the event name, child guidance and lack of
   `updatedInput`, type changes or parent prompt rewriting, and asserts the
   4,096-byte context bound without relying on a raised spill limit.
2. Register PostToolUse for execute/fetch and SessionEnd for all reasons in
   the Codex catalog. Reuse `handlePostToolUse` and `handleSessionEnd` with the
   adapter's namespaced session and unmodified child identity. → verify:
   `TestCodexChildObservationLifecycle` uses the installed CLI path to record
   success, consume a suitable redirect once, allow subsequent native fallback
   and clean up only that parent session.
3. Add Codex payload fixtures for root vs child, null transcript paths,
   successful/failed/serialized MCP results, missing identity and type-only
   identity. Preserve the host's supplied parent identity even when a child
   transcript has its own UUID. → verify: `TestCodexObservationPayloads` and
   existing observation expiry, lock-contention, cap and sibling-session tests.
4. Ensure the Codex pre-tool path never invokes Claude Agent/Task mutation for
   `spawn_agent` or its matcher aliases; child instructions use SubagentStart.
   Unknown tools remain outside capy's selected event coverage. → verify:
   `TestCodexNoAgentArgumentRewrite` checks that submitted child arguments are
   preserved and no unsupported response is produced.
5. Extend committed `.codex/hooks.json` and its generator tests together; update
   accurate host-coverage wording in runtime and generated routing surfaces
   where needed. → verify: the three artifact-drift checks and complete hook
   suite pass with all five expected events.

The observation algorithm, storage schema, TTL, caps and lock inode do not
change. If runtime parent/child payload association differs from the verified
contract, resolve it in the adapter and its fixtures before claiming this
slice complete; do not relax evidence or identity requirements.

## 5. Diagnostics, release verification and documentation

### 5.1 Shared diagnostics

1. Add platform-specific hook and MCP checks in
   `internal/platform/doctor_codex.go`, reusing the existing TOML dependency
   and the installed-event catalog. Detect configured platforms by actual
   capy artifacts. Report missing, partial, malformed, duplicate and complete
   registration; reuse the runtime inspector for marker validity, each binding's
   adjacent marker and source/command owner-ID agreement. Check wrapper/
   invocation/matcher shape, MCP bootstrap argv and ancestor overlap. A retained
   ancestor config with a missing marker is broken registration, even when a
   nested copy has the same ID.
   → verify:
   `TestDoctorPlatformRegistrations` covers Claude-only, Codex-only, both,
   neither, incomplete installations and the copied-ID/missing-outer-marker
   fixture; setup, doctor and runtime must agree on that failure.
2. Inspect statically visible project feature disabling and inline-hook
   overlap without mutating configuration. Report registration independently
   from activation; do not infer runtime trust or reconstruct all Codex
   configuration precedence. → verify: `TestDoctorCodexActivationLimits`
   ensures a valid file check never says either project or hook trust is
   established or that hooks ran, and explicit project disabling is reported.
3. Wire the shared results into both `cmd/capy/doctor.go` and
   `internal/server/tool_doctor.go`. Keep existing credential/store/vault
   diagnostics intact and separate from registration checks. → verify:
   existing CLI/MCP doctor tests plus a Codex-only fixture produce equivalent
   platform findings and no spurious missing-Claude error.

### 5.2 Live acceptance and release checks

1. Create a disposable project, install the built capy hooks, establish project
   trust and inspect/trust their definitions
   through Codex's supported interface. Use
   a pinned recorded Codex version; do not bypass trust or reuse production
   capy credentials for the probes. Prefer an isolated policy home. When the
   interactive host retains its existing authentication/trust home, use empty
   temporary capy configuration, synthetic project rules/keys and no vault key;
   record global-policy inheritance and verify each denial's exact fixture
   pattern/source plus a successful control. Keep isolated policy-home coverage
   in the installed-command tests. This explicit Task 6 environment adjustment
   replaces the original requirement for a fully isolated live policy home;
   it does not relax policy enforcement or either host trust gate.
   → verify: record visible capy startup guidance and
   hook delivery, distinct from the toolbox-script test done during research.
   Launch Codex from an ordinary project subdirectory too, initialize MCP and
   make a harmless capy call with synthetic keys; hook delivery alone does not
   satisfy the end-to-end launch contract.
2. Execute a harmless control and a harmless command covered by a synthetic
   deny rule; repeat with a synthetic ask rule. Exercise a tool call nested in
   code mode where available. → verify: the control marker appears; denied
   markers do not; the ask case explains the unsupported approval requirement
   and identifies its pattern/source file without emitting `ask` or suggesting
   an alternate-tool bypass. A failed trusted installation must block until
   operator repair; exercise the missing-binary collaborator recovery flow.
3. Exercise one child with successful capy execute/fetch evidence followed by
   a suitable native routing probe and then fallback. Preserve unrelated
   hooks in a second mixed-installation run. → verify: observed parent/child
   IDs, one redirect only, and the independent hook's behavior. Use synthetic
   stdin tests for any cleanup path not observable live and label that limit.
   Record the bytes emitted by both startup hooks on startup/resume, confirm
   capy's own compact payload is not spilled, and link the outstanding toolbox
   cleanup. Do not count host truncation as deduplication.
4. Run the repository-required FTS5 test suite and race checks for adapter,
   hook and platform state paths; run the existing generated-artifact checks.
   No search/indexing/executor changes are planned, so quality benchmarks are
   not required unless implementation expands into those areas. → verify:
   record commands and outcomes in the existing feature `verification.md`,
   including genuine blockers or unsupported runtime paths.
5. Run `$kk:document` to update README installation/activation guidance,
   `docs/architecture.md`, CLI help, and AGENTS.md's generated-artifact list and
   wrapper exception. Add a historical clarification to ADR-024 if needed:
   its previous Codex bypass description reflected the integration at that
   time. Preserve historical decisions and links. → verify: documentation
   agrees with actual installed events and does not claim #132 is fixed. Cover
   collaborators trusting hooks without installing capy, the deliberate
   difference from Claude's crash behavior, repair through an external terminal,
   and user-controlled `/hooks` disabling during infrastructure repair. Distinguish
   that from bypassing policy denials. Document marker portability, overlap
   rejection, runtime config-read failures, two trust gates and the non-Git
   discovery limitation. Require stopping/reloading affected sessions before
   removing/moving loaded installation artifacts; the launcher cannot recover
   an origin deleted entirely while the host retains its command in memory.
6. Run `$kk:review-code` with Go input and `$kk:review-spec`; resolve or durably
   record findings. → verify: review artifacts and disposition are linked from
   verification/tasks, and each claimed behavior has corresponding evidence.

### 5.3 Rollback

Document stopping affected sessions before removing loaded bindings, removal
of only positively identified capy-owned Codex entries, then
restoration of the prior binary if needed. Preserve toolbox/global/inline hooks
and all stored data. Retain the owner marker while any generated hook or MCP
entry still refers to it; do not rotate IDs or delete a live binding as a repair.
No automatic uninstall command or cross-project migration
is introduced by this design. → verify: a fixture removes the owned entries
without changing external handlers, or record the documented manual procedure
against the mixed-installation fixture.

## 6. Follow-ups

### Claude-toolbox enhancement PR

**Follow-up filed on 2026-10-14:** at the user's request, opened
[claude-toolbox #170](https://github.com/serpro69/claude-toolbox/issues/170)
instead of an enhancement PR. The [saved issue description](toolbox-enhancement-issue.md)
explains duplicate instruction injection and how toolbox's standalone HTTP
denials mask capy's observed-success routing and advisory fallback.

The issue proposes optional cleanup after activation verification, preserving
provider instructions, `.claude/CLAUDE.*.md` extras, file restrictions and
unrelated hooks. Projects without the new hooks retain legacy behavior; static
registration or marker presence cannot prove activation. Capy setup must not
edit toolbox scripts. No upstream implementation was changed by this follow-up.

**Release and implementation remain pending upstream.** Capy PR #135 is open
and the latest release, v0.17.0, does not contain the integration. Issue #170
requires identifying the first compatible release, documenting setup/trust and
rollback, implementing explicit opt-in, and verifying both legacy and migrated
installations. This completes the requested issue handoff, not that cleanup.

Until cleanup lands and a project opts in, duplicate startup content remains.
[verification.md](verification.md#startup-context-baseline) retains the original
12,740-byte projection and the later actual mixed-run measurement of 13,801
bytes, including toolbox provider/extras. The final v2 capy block is 3,532 bytes;
its live run used independent fixture hooks, so it is not a new toolbox total.

### Issue #132: bounded routine command output

**Deferred because hook delivery and output policy have separate acceptance
criteria.** The user chose bounded routine output regardless of execution tool,
with complete ordered logs recoverable for diagnosis. After this feature,
compare routine install/test tasks under the actual capy hooks and toolbox setup;
measure model-visible output, exit-status preservation and log recovery.
Then design or implement the smallest guidance/capture change supported by that
evidence. Do not close #132 just because hooks are registered.

### Issue #29 tracking

Its closure by PR #30 did not represent completion of hook support. Link this
design from a reopened or replacement issue when maintaining GitHub tracking;
avoid using a closing keyword until the implementation and live verification
actually satisfy that issue. No issue mutation was performed during this design.
