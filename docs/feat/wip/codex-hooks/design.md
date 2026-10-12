# Codex hooks owned by capy

> Status: draft — user-confirmed direction; pending independent design review
> Created: 2026-10-12
> Related: [#29](https://github.com/serpro69/capy/issues/29), [#132](https://github.com/serpro69/capy/issues/132)
> Plan: [implementation.md](implementation.md) · Tasks: [tasks.md](tasks.md)

## 1. Problem and intended outcome

Developers using capy with Codex currently get an MCP server, binary wrapper, and
generated routing instructions from `capy setup`. They do not get capy's hook
integration. This repository happens to have working Codex hooks imported from
the claude-toolbox template. Those scripts load capy's instructions when present
and implement a separate shell policy; they do not invoke capy's Go hook pipeline.

The feature makes capy responsible for installing and running its own Codex
hooks. A developer can configure a project with `capy setup --platform codex`,
review/trust the resulting hooks in Codex, and receive capy's guidance and
supported routing behavior in main sessions and child agents. Installing
claude-toolbox is not a prerequisite.

Success is observable:

1. Fresh setup registers the five events in §4; a repeated setup makes no
   semantic changes and preserves unrelated handlers and configuration.
2. A trusted installation delivers startup/child guidance and intercepts a
   matching Bash or capy MCP call through the actual Codex runtime.
3. A matching deny or approval-required capy policy prevents execution. The
   latter explains Codex's unsupported hook-approval behavior rather than
   emitting an unsupported response or allowing the command through.
4. Child observations retain their existing one-use, expiry, concurrency and
   session-cleanup contracts.
5. CLI and MCP diagnostics identify Codex registration independently of Claude
   registration and distinguish configuration checks from runtime proof.

This work does not, by itself, fix the output-volume examples in #132. Existing
routing permits ordinary test/install commands. Bounded routine output and full
log recovery remain a separate follow-up described in the implementation plan.

## 2. Decisions confirmed with the user

- Use capy's shared Go hook logic with a Codex-specific adapter.
- Keep capy independent of claude-toolbox and preserve unrelated hooks.
- Include main-session and child-agent guidance/observation behavior in v1.
- When an existing capy command policy requires approval, block with an
  explanation because Codex cannot present that approval from `PreToolUse`.
- Propose a separate claude-toolbox enhancement after capy owns these behaviors;
  it is not a dependency of this implementation.
- Proceed using the inspected code and pinned external source; the user
  declined an additional isolated chain-of-verification pass.

This is a non-trivial integration: the existing policy can be reused, but the
host's identity, response and activation contracts differ. The Go design profile
applies. Its database, gRPC and observability checklists do not apply: hooks do
not open databases, no gRPC interface is added, and this feature adds static
installation diagnostics rather than a logging/metrics subsystem.

## 3. Evidence and existing architecture

### 3.1 History

[PR #27](https://github.com/serpro69/capy/pull/27), merged 2026-04-26, imported
the repository's Codex scripts from claude-toolbox. [PR #30](https://github.com/serpro69/capy/pull/30),
merged 2026-04-27, added `SetupCodex` and explicitly deferred hook generation to
#29. Its description nevertheless contained `Closes #29`; GitHub's issue closure
record identifies PR #30 as the closer. #29 is marked completed despite its
unchecked hook-generation work.

The recent [upstream audit](../../done/upstream-sync-v1.0.169/upstream-audit.md)
explicitly excluded introducing a Codex hook adapter. The gap is unfinished
integration, not evidence that this repository lacked all Codex hook support.

### 3.2 Existing seams

| Component | Current behavior | Intended extension |
|---|---|---|
| `cmd/capy/hook.go` | Always constructs `ClaudeCodeAdapter` | Explicit platform selection; Claude remains the default |
| `internal/adapter/` | Claude payload parsing/formatting and shared event type | Codex adapter with host-specific identity and supported responses |
| `internal/hook/` | Shared routing, policies, guidance, observations and cleanup | Reuse these mechanisms; add Codex startup/child dispatch and platform-aware context selection |
| `internal/platform/setup.go` | Codex wrapper, MCP table and routing file only | Merge capy-owned entries into `.codex/hooks.json` |
| `internal/platform/doctor.go` | Checks Claude hook/MCP registration | Separate platform-labelled registration checks |
| `.codex/scripts/session-start.sh` | Optionally injects capy's generated file | External hook; preserved by capy setup |
| `.codex/scripts/pretooluse-bash.sh` | Standalone file restrictions and HTTP rules | External hook; preserved by capy setup |

The current shared hook adapter surface is not a complete portability boundary.
`resolveHookContext` reads `CLAUDE_PROJECT_DIR`, and Claude's session resolution
prefers a UUID parsed from the transcript path over the supplied session ID.
Those assumptions must not leak into the Codex adapter.

`PreCompact` is currently a no-op and `UserPromptSubmit` is a stub. Installing
those events for Codex would add processes without implementing a behavior.

### 3.3 Verified host contract

The design's verified baseline is **Codex CLI 0.162.1**, the installed version
during research. This is a tested baseline, not a claim that earlier versions
cannot work. Earlier versions and other Codex surfaces require their own runtime
verification before compatibility is advertised.

Primary sources, inspected on 2026-10-12:

- [Official hooks reference](https://learn.chatgpt.com/docs/hooks): hook sources,
  trust, matchers, input/output semantics and code-mode coverage.
- [Pinned PreToolUse implementation](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/events/pre_tool_use.rs):
  canonical Bash input, optional child fields, denial aggregation, and a test
  proving that `permissionDecision: ask` fails open.
- [Pinned hook schemas](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/schema.rs):
  `session_id`, `agent_id`, `agent_type`, `cwd`, MCP responses and event shapes.
- [Pinned startup implementation](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/events/session_start.rs):
  separate `SessionStart` and `SubagentStart` targets; startup sources also
  include `fork`, beyond the public reference's listed examples.

The current session passed a real positive-control command and rejected a
harmless print command ending in `.csv` through the existing toolbox hook.
That proves host interception works here. It does **not** verify the proposed
capy adapter, generated installation, or child lifecycle; those are acceptance
tests, not completed findings.

## 4. Event coverage and routing

| Event | Matcher | Behavior |
|---|---|---|
| `SessionStart` | All sources | Inject capy routing guidance on startup, resume, clear, compact and fork |
| `SubagentStart` | All child types | Inject child routing/tool-availability guidance into the child context |
| `PreToolUse` | Canonical `Bash` and capy MCP tool names | Existing command/file policies, HTTP routing and advisory guidance |
| `PostToolUse` | Capy execute/fetch tool names | Record successful child calls as one-use routing evidence |
| `SessionEnd` | All reasons | Remove only that stable session's observations; never open a database |

Matchers are Codex regular expressions, not copied Claude glob-like strings.
Build anchored expressions from the ten known capy tool names. Include names
under different MCP server aliases, such as `mcp__capy__capy_execute` and
`mcp__another_server__capy_execute`; do not subscribe to every MCP tool merely
because its server name contains `capy`. Post-tool matching includes only the
two observation-producing tools, excluding execute-file and batch-execute.

Codex normalizes native shell execution, including `exec_command`, to `Bash`
with `tool_input.command`. The adapter targets that verified hook payload; it
does not guess from model-facing `cmd` fields. Calls nested inside code mode
use the same hook path. Later `write_stdin` polling does not introduce another
pre-tool decision for the already-started command.

Use `SubagentStart` for child instruction injection. Do not rewrite
`spawn_agent` arguments, inject Claude's `subagent_type`, or upgrade child types.
Hosted web/search tools are outside this hook coverage. The injected text and
shared generated routing instructions must describe that boundary accurately,
without weakening the existing Claude-specific behavior.

## 5. Adapter, policy and state contracts

### 5.1 Explicit platform selection

Add `--platform claude-code|codex` to `capy hook`; omitted means `claude-code`.
Generated Codex commands select `codex` explicitly. Do not infer the host from
ambient environment variables or tool names. Unknown platform/event combinations
fail with an actionable error before policy loading or state mutation.

Extend the adapter boundary only for the platform identity and startup event
formatting needed here. Shared handlers select context behavior through that
explicit adapter identity. Keep transport parsing/formatting in `internal/adapter`
and command routing/state in `internal/hook`; do not create a second router.

### 5.2 Identity and project selection

For Codex, the supplied nonempty `session_id` is authoritative. Namespace it
internally with `codex:` before deriving state keys so a synthetic collision
with a Claude session cannot share guidance or cleanup. Preserve host
`agent_id` and `agent_type`; a nonempty agent ID selects child handling.

Never derive Codex identity from a rollout filename, `CLAUDE_SESSION_ID`, or a
process-parent fallback. A missing session ID remains unstable: no observation
recording/consumption/cleanup, and guidance is returned without persisted
throttling. Child tool availability remains unverified unless the existing
observation conditions succeed. An agent type by itself is not a child identity.

Codex project selection is explicit `--project-dir`, then validated payload
`cwd`, then process-cwd discovery. Ignore `CLAUDE_PROJECT_DIR` for this adapter.
Preserve Claude's existing precedence. Project ownership and the current working
directory remain distinct: policies/state belong to the selected project;
cwd-relative rules use the validated event directory. Reuse existing directory
validation, worktree handling and symlink semantics.

### 5.3 Policies and responses

Reuse capy's existing policy inputs and evaluators, including the optional
Claude-format settings files already used by capy's MCP security checks. This
does not require Claude to be installed and does not translate Codex native
rules or approval settings. Codex's own sandbox and permissions still apply.

- A matching deny or command-scanner failure produces a supported structured
  pre-tool denial with a reason.
- A matched approval rule produces a supported denial explaining that the rule
  requires approval which this Codex hook cannot request. It must not suggest
  disabling safeguards or retrying through a different tool to evade the rule.
- Ordinary allow/advisory paths return no decision or `additionalContext`;
  they do not grant a host permission or return a bare `allow` decision.
- HTTP routing reuses the existing quote handling, silent-download exceptions
  and one-use child evidence. No replacement command is approved implicitly.
- Malformed Codex pre-tool payloads, including invalid Bash command input,
  produce a denial before state writes. This is specific to the new adapter;
  retain the existing Claude malformed-input behavior.
- Unsupported argument/output rewrite paths are not advertised or used by
  the Codex integration. A mistaken internal attempt must fail explicitly.

Denies still take precedence over matched asks in a compound request. A returned
policy message must distinguish an actual deny from an unsupported approval
requirement. Native host approval cannot substitute for the latter automatically:
the two systems may evaluate different policies.

### 5.4 Child observations and cleanup

Reuse `PostToolUse` success validation and `observations.go`. Codex's child tool
hooks carry the parent session identity plus the child identity; the adapter
must preserve that association rather than deriving a session from the child's
transcript. Pin representative payloads in tests and verify a real child round
trip during implementation.

All existing invariants remain: independent 60-second execute/fetch expiry,
atomic consumption of all alternatives for a child, maximum 128 entries/64 KiB,
permanent lock inode, maximum 20 ms lock wait, and advisory fallback on unavailable
or invalid observation state. Root SessionEnd removes only its namespaced
session's entries. Missed SessionEnd remains bounded by expiry and capacity.
No hook accesses the knowledge or vault database or requires their keys.

## 6. Installation, ownership and failure behavior

### 6.1 Files and invocation

`SetupCodex` will merge `.codex/hooks.json` in addition to its existing outputs.
Continue using `.codex/scripts/capy.sh`; do not generate copies of toolbox's
session-start or pretooluse scripts.

Generated command invocations have a fixed argument order: `hook --platform
codex <event>`, followed by an explicit project directory. Use a single command
generator for all events. It locates the installed wrapper by walking upward
from the hook process's physical working directory to the nearest ancestor
containing `.codex/scripts/capy.sh`, then passes that directory explicitly.
This works in ordinary repositories, worktrees and non-Git project directories
without embedding a machine-specific absolute path. Stop at filesystem root;
failure to locate the wrapper is explicit. Quote every derived path. Do not
search unrelated directories, inspect transcripts, or use inherited `GIT_*` or
`CLAUDE_PROJECT_DIR` values to select the installation.

The supported launch contract is that the hook process starts within its capy
installation's directory tree. A caller deliberately launching it elsewhere must
invoke the wrapper with an explicit project directory. Test this boundary rather
than falling back to a recently used checkout.

### 6.2 Merge ownership

Recognize owned handlers through the generated capy-wrapper invocation and
explicit Codex platform/event arguments, not a loose `capy` or `hook` substring.
Preserve unknown top-level keys, other event groups and non-capy handlers,
including siblings inside a mixed group. Rewrite/collapse only positively
identified owned handlers into one canonical entry per supported event.

Validate JSON structure before writing. Invalid JSON, wrong types or ambiguous
ownership fail with an actionable error; setup must not silently replace them
with an empty hooks object. Repeating setup is semantically idempotent. An
invalid hook file must remain unchanged; unrelated setup outputs need not be
rolled back if an earlier setup step already completed.

Do not alter inline hooks in `.codex/config.toml`, user/global hooks, plugin
hooks, or the toolbox scripts. Codex composes these sources. Additional matching
hooks may legitimately impose stricter restrictions. Report overlapping sources
when statically visible; do not try to reconcile their policies.

### 6.3 Wrapper errors and activation

Keep the shared wrapper's existing Claude-hook behavior and all non-hook exit
propagation. Add a narrow branch for the generated Codex invocation: if the
binary is missing or a pre-tool invocation exits unsuccessfully, return Codex's
blocking exit code 2 with an explanatory stderr message. For other Codex events,
propagate a nonzero failure for the host to report; do not fabricate a
`PreToolUse` JSON object for a startup event. A successful structured policy
denial continues to exit 0. Apply wrapper-generator changes to both committed
wrapper copies and their tests, as required by AGENTS.md.

This does not make hooks a security boundary against a host killing/skipping
them. Missing trust, disabled hooks, host timeouts and unsupported runtime
versions remain host limitations that diagnostics and activation instructions
must disclose.

Setup reports **registered; activation requires Codex review/trust**. It never
edits trust records, bypasses trust, or overrides an explicitly disabled hooks
feature. Tell users to inspect `/hooks` and restart/resume as needed. A changed
hook definition can require renewed trust. The existing session's trust is not
evidence that a fresh generated definition will run automatically.

## 7. Diagnostics and verification

Both `capy doctor` and `capy_doctor` consume shared platform checks. Report
separate hook and MCP registration results for each configured platform. A
Codex-only installation must not fail solely because `.claude/settings*.json`
or `.mcp.json` is absent. Determine configured platforms from capy registration
artifacts, not the mere existence of a `.claude` or `.codex` directory; with no
registration in either platform, retain an actionable setup failure.

Check expected owned events, matchers, wrapper existence and invocation shape.
Distinguish missing, partial, malformed and duplicate registration. Validate the
Codex MCP table with the repository's existing TOML dependency. A visible
project-level `features.hooks = false` (or deprecated alias) warrants a warning;
do not attempt to reproduce all host config precedence or read private trust
state. Say that trust and actual execution are unverified by the file check.

Verification has three levels:

1. Adapter/router tests with pinned synthetic payloads, covering identity,
   policy decisions, startup formatting, observations and failure paths.
2. Generated-installation tests that run the actual wrapper/CLI in isolated
   project trees, with unset production credentials and isolated policy homes.
   Include repeat setup, mixed third-party groups, paths containing spaces,
   subdirectories, moved trees, non-Git roots and worktrees.
3. A recorded live Codex 0.162.1-or-newer session: startup injection, harmless
   allowed/denied commands, a matched approval rule, a child observation and
   redirect/fallback sequence, and unchanged unrelated hooks. Record the exact
   runtime version and which behaviors were observed. Never call configuration
   tests or manual stdin replay proof that the host delivered an event.

Unit tests can supply synthetic deny patterns and commands that only print
markers. Live probes must not read secrets, make unnecessary HTTP calls or
disable host safeguards. Test cleanup separately if ending the host session
cannot be observed live; clearly label that limit.

## 8. Assumptions

| Assumption | Validation / consequence if false |
|---|---|
| Codex 0.162.1's documented and pinned fields reach installed hooks | Capture synthetic runtime observations in the live check; adjust the adapter before claiming support |
| Child tools share their parent's session ID and supply a distinct agent ID | Verify parent/child payload association; do not release cleanup/evidence claims if it differs |
| Hook processes start inside the configured project tree | Exercise root, nested, moved and worktree launches; fail explicitly outside the supported launch contract |
| The existing Go router can remain policy owner | Router conformance tests must pass for both adapters; avoid a parallel shell implementation |
| Existing policy files can be reused as capy's additional restrictions | Show actual sources in docs; do not claim equivalence with Codex native permissions |
| Independent toolbox hooks may remain installed | Mixed-installation checks preserve their behavior; optional upstream simplification is not required for release |

## 9. Not doing

- Output capture, log retention, summarization, output budgets or new routine
  command routing: these are a separate #132 workstream.
- Full Codex permission-policy emulation or translation of native rules: the
  hook is an additional capy policy layer, not the host's permission engine.
- Runtime instrumentation/receipts or a trust-state database: diagnostics are
  static checks plus documented live verification.
- New archive/compaction behavior, database schema changes or hook database
  access: the existing server lifecycle continues to own storage.
- No-op event registrations, hosted web-tool interception, child-type upgrades
  or `spawn_agent` rewrites: they provide no supported behavior needed here.
- Automatic mutation of toolbox/global/plugin hooks or trust decisions: those
  have separate owners.
- Universal compatibility claims for all Codex versions, desktop/cloud
  orchestration or Windows: advertise only platforms actually verified within
  capy's existing platform constraints.

## 10. Rejected alternatives and trade-offs

| Direction | Benefit | Cost / reason not chosen |
|---|---|---|
| Generate the two toolbox shell scripts as proposed in #29 | Small initial patch using known working scripts | Duplicates routing/security policy and takes ownership of unrelated provider instructions and file restrictions |
| Register the current Claude adapter unchanged | Fewest code changes | Wrong identity/env assumptions and unsupported approval responses; it can turn an intended approval requirement into execution |
| Codex adapter over the shared Go router — chosen | One policy implementation with explicit host semantics | Requires adapter/installation tests and deliberate unsupported-operation handling |
| Introduce a generic new hook framework | Could support more hosts later | Unnecessary abstraction and migration for one additional host |

Returning denial for a matched approval requirement is deliberately stricter
than deferring to Codex's independent policy. It preserves the requirement and
explains the limitation; implementing a native approval bridge is not assumed.

## 11. Rollout and follow-ups

Ship the adapter, setup, committed generated artifacts and diagnostics together.
Upgrade capy, rerun setup, review/trust new hooks in Codex, and perform the live
probe. Existing toolbox scripts can remain throughout. Rollback removes only
the newly generated capy hook entries and restores the previous capy binary;
it does not require reverting toolbox or changing stored data.

The [implementation follow-ups](implementation.md#6-follow-ups) record the
proposed claude-toolbox enhancement and remaining #132 output-policy work. Do
not close #132 solely on completion of this feature or describe the upstream
cleanup as already shipped.
