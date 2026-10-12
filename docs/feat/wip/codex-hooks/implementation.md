# Implementation: capy-owned Codex hooks

> Design: [design.md](design.md) · Tasks: [tasks.md](tasks.md)
> Status: planned; no implementation performed
> Baseline inspected: 2026-10-12, Codex CLI 0.162.1

## 1. Orientation and implementation boundaries

Capy hooks are short-lived Go CLI processes: stdin JSON enters
`cmd/capy/hook.go`, an adapter parses it, `internal/hook` selects the project,
evaluates policy and routes the event, and an adapter formats stdout JSON.
There is no long-lived hook server. Guidance and child observations live in
small files under the selected project's `.capy/` directory; hooks do not open
the encrypted knowledge or vault database.

`SetupClaudeCode` and `SetupCodex` live in `internal/platform/setup.go`. The
Claude installer merges six events; the Codex installer currently installs no
hooks. `capyWrapperScript` generates both committed wrappers. The artifact
tests enforce agreement between generators and committed configuration.

Use these existing components:

- `internal/adapter/{adapter,claudecode}.go`: adapter interface, event type,
  Claude transport behavior. Keep Claude's public behavior stable.
- `internal/hook/{hook,context,pretooluse,routing}.go`: event dispatch,
  project selection, command/MCP policy and injected guidance.
- `internal/hook/{posttooluse,observations,sessionend,guidance}.go`: child
  evidence and session state. Preserve existing caps, locking and expiry.
- `internal/platform/{setup,doctor}.go`: installation and shared checks.
- `cmd/capy/{setup,doctor}.go` and `internal/server/tool_doctor.go`: user-facing
  setup and diagnostics; both doctor surfaces must agree.
- `internal/platform/setup_artifacts_drift_test.go`: mandatory registration of
  `.codex/hooks.json` as a merged artifact, plus wrapper/routing equality.

Implementation code below is described, not supplied. New test names are
proposed acceptance names. Test packages must use `-tags fts5`; isolate global
settings and use the synthetic `CAPY_DB_KEY` and `CAPY_VAULT_KEY` required by
AGENTS.md. Hook-specific subprocess fixtures must also pass with both keys
unset, proving that the hook path does not initialize a database.

## 2. Main-session hook path

**Goal:** invoking `capy hook --platform codex pretooluse` or `sessionstart`
directly exercises a complete usable Codex path before installation is added.

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
3. Format Codex denials and advisory context in its adapter. Map `FormatAsk`
   to a supported denial with an approval-required explanation. Keep shared
   compound-request deny precedence; no Codex response should contain
   `permissionDecision: ask`, a bare permission grant, or an implicit command
   replacement. Make unsupported rewrite attempts explicit. → verify:
   `TestCodexPolicyResponses` covers allow, deny, matched ask, later deny after
   an earlier ask, malformed command input and scanner failure.
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
   supported routes, and Claude startup/routing tests remain unchanged.

Run adapter and hook packages plus targeted CLI tests. This slice is complete
when a synthetic stdin invocation reaches the real CLI and produces supported
JSON without credentials or a database.

## 3. Generated installation

**Goal:** setup installs the tested main-session path, preserving all other
configuration. The next slice extends the same event catalog to children.

1. Add Codex-specific event descriptors and merge helpers in
   `internal/platform/setup_codex_hooks.go`; leave the existing Claude event
   catalog intact. Start with SessionStart and PreToolUse. Generate anchored
   regex matchers using the known capy tool names and an explicit Codex
   platform argument. SessionStart matches every source. → verify:
   `TestCodexHookMatchers` checks canonical Bash, capy names under multiple
   server aliases and unrelated MCP names; startup tests include `fork`.
2. Implement the common generated command's upward wrapper lookup from the
   hook process's physical cwd. Pass the discovered installation explicitly
   as project ownership. Bound the walk by filesystem root, quote paths, and
   report lookup failure without guessing another checkout. → verify:
   `TestCodexHookCommandLocations` executes generated commands from root,
   nested, moved and non-Git project trees, including spaces in paths; add a
   real linked-worktree fixture and an outside-tree failure fixture.
3. Merge only capy-owned handlers into `.codex/hooks.json`. Recognize the
   canonical wrapper/platform/event invocation; preserve sibling handlers in
   mixed groups. Upsert/collapse owned duplicates without replacing unrelated
   groups. Reject malformed document shapes or ambiguous owned entries before
   writing. → verify: `TestMergeCodexHooks` covers empty/fresh files, unrelated
   top-level metadata, mixed groups, owned duplicates, similar-looking external
   commands, malformed shapes and a second semantically identical run.
4. Call the merger from `SetupCodex` and report registration plus Codex trust
   instructions in `cmd/capy/setup.go`. Do not change the existing platform
   defaults or override disabled hooks, inline hooks, global hooks or trust
   records. → verify: `TestSetupCodexHooks` checks Codex-only and default-both
   setup, preservation of MCP configuration, and unchanged invalid hook files
   when setup reports an error.
5. Add narrow Codex failure handling to `capyWrapperScript` for the generated
   `hook --platform codex <event>` prefix. Preserve existing Claude zero-exit
   behavior and non-hook exit propagation. Codex pre-tool launch failures
   block using exit 2; non-pre-tool failures retain an appropriate nonzero
   status. Apply the generator to both wrapper copies. → verify:
   `TestWrapperCodexHookFailure` uses a controlled fake/missing binary and
   checks successful JSON denial, subprocess failure, missing binary and
   startup failure; existing wrapper exit-code tests remain green.
6. Add the generated entries to committed `.codex/hooks.json` without removing
   the toolbox entries. Register it in `jsonMergeArtifacts`; synchronize any
   common generated routing wording with `.capy/AGENTS.md`. → verify:
   `TestGeneratedWholeFileArtifacts`, `TestMergedArtifactsAreIdempotent` and
   `TestDriftGuardCoversEverySetupArtifact` all pass without exemptions for the
   new committed artifact.

Shell/bootstrap text is one generator-owned implementation reused across event
descriptors. It must not evolve into a parallel shell policy evaluator.

## 4. Child guidance, evidence and cleanup

**Goal:** installed Codex hooks cover a complete child lifecycle with the
existing observation safeguards.

1. Add Codex `SubagentStart` dispatch and event-specific context formatting.
   Extend the adapter's startup formatting boundary with the minimum method
   needed to emit that event; do not route it through a SessionStart-labelled
   response. Register it for all child types. → verify:
   `TestCodexSubagentStart` checks the event name, child guidance and lack of
   `updatedInput`, type changes or parent prompt rewriting.
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
   registration; check wrapper/invocation/matcher shape. → verify:
   `TestDoctorPlatformRegistrations` covers Claude-only, Codex-only, both,
   neither and incomplete installations.
2. Inspect statically visible project feature disabling and inline-hook
   overlap without mutating configuration. Report registration independently
   from activation; do not infer runtime trust or reconstruct all Codex
   configuration precedence. → verify: `TestDoctorCodexActivationLimits`
   ensures a valid file check never says hooks are trusted or observed running,
   and explicit project disabling is reported.
3. Wire the shared results into both `cmd/capy/doctor.go` and
   `internal/server/tool_doctor.go`. Keep existing credential/store/vault
   diagnostics intact and separate from registration checks. → verify:
   existing CLI/MCP doctor tests plus a Codex-only fixture produce equivalent
   platform findings and no spurious missing-Claude error.

### 5.2 Live acceptance and release checks

1. Create a disposable project with an isolated policy home, install the built
   capy hooks and inspect/trust them through Codex's supported interface. Use
   a pinned recorded Codex version; do not bypass trust or reuse production
   secrets for the probes. → verify: record visible capy startup guidance and
   hook delivery, distinct from the toolbox-script test done during research.
2. Execute a harmless control and a harmless command covered by a synthetic
   deny rule; repeat with a synthetic ask rule. Exercise a tool call nested in
   code mode where available. → verify: the control marker appears; denied
   markers do not; the ask case explains the unsupported approval requirement
   without emitting `ask` or suggesting an alternate-tool bypass.
3. Exercise one child with successful capy execute/fetch evidence followed by
   a suitable native routing probe and then fallback. Preserve unrelated
   hooks in a second mixed-installation run. → verify: observed parent/child
   IDs, one redirect only, and the independent hook's behavior. Use synthetic
   stdin tests for any cleanup path not observable live and label that limit.
4. Run the repository-required FTS5 test suite and race checks for adapter,
   hook and platform state paths; run the existing generated-artifact checks.
   No search/indexing/executor changes are planned, so quality benchmarks are
   not required unless implementation expands into those areas. → verify:
   record commands and outcomes in a new feature `verification.md`, including
   genuine blockers or unsupported runtime paths.
5. Run `$kk:document` to update README installation/activation guidance,
   `docs/architecture.md`, CLI help, and AGENTS.md's generated-artifact list and
   wrapper exception. Add a historical clarification to ADR-024 if needed:
   its previous Codex bypass description reflected the integration at that
   time. Preserve historical decisions and links. → verify: documentation
   agrees with actual installed events and does not claim #132 is fixed.
6. Run `$kk:review-code` with Go input and `$kk:review-spec`; resolve or durably
   record findings. → verify: review artifacts and disposition are linked from
   verification/tasks, and each claimed behavior has corresponding evidence.

### 5.3 Rollback

Document removal of only positively identified capy-owned Codex entries, then
restoration of the prior binary if needed. Preserve toolbox/global/inline hooks
and all stored data. No automatic uninstall command or cross-project migration
is introduced by this design. → verify: a fixture removes the owned entries
without changing external handlers, or record the documented manual procedure
against the mixed-installation fixture.

## 6. Follow-ups

### Claude-toolbox enhancement PR

**Deferred until the capy integration is implemented and its activation path is
verified.** The user agreed that an enhancement PR should explain that capy now
owns its instruction injection and supported HTTP routing.

Prepare that PR in the claude-toolbox repository, citing the released capy
version and setup/activation instructions. Explain the optional conditional
read of `.capy/AGENTS.md` and the standalone HTTP-routing block, preserve
unrelated provider context and file restrictions, and explicitly address users
without the new capy hooks before proposing removals. Capy setup must not make
those edits itself. This design neither submits the PR nor asserts those
upstream behaviors have already been removed.

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
