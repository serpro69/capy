# Codex hooks owned by capy

> Status: Tasks 1–2 implemented and reviewed; Tasks 3–6 pending
> Created: 2026-10-12
> Review dispositions: [review-resolution.md](review-resolution.md)
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
6. Hooks and MCP bind to the same installation owner from root, ordinary
   subdirectories, moved trees and supported worktrees. Ambiguous overlapping
   installations fail before policies, observations or database access.

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
- [Pinned discovery](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/discovery.rs)
  and [command runner](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/command_runner.rs):
  config-layer hooks compose and run from event cwd; regular project hooks do
  not receive their config-source directory as an owner binding.
- [Pinned MCP launcher](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/rmcp-client/src/stdio_server_launcher.rs):
  arguments are passed through and an absent server cwd uses the runtime cwd.
- [Pinned context spiller](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/output_spill.rs):
  the default approximate threshold is 2,500 tokens; larger context becomes a
  saved file and a head/tail preview.

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

Argument rewriting with `updatedInput` is supported by Codex but deliberately
unused here: SubagentStart provides child guidance without altering a launch
request or coupling capy to child-tool arguments. Direct `updatedMCPToolOutput`
replacement remains unsupported at the verified baseline and is also unused.
Adapter capability descriptions must distinguish host capability from what this
integration implements.

Keep each generated SessionStart/SubagentStart `additionalContext` at or below
**4,096 UTF-8 bytes**, with all essential routing instructions in that payload.
Assert this bound in both startup tests and check that the default host spiller
does not replace it during live acceptance. This is a capy byte budget, not a
claim about exact model token counts. Omit `additionalContextLimit` from every
generated handler and retain Codex's default; do not uniformly add a field to
events that cannot emit context. Codex 0.162.1 accepts it on PostToolUse, but
capy's observation handler emits no context; SessionEnd does not accept it.

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
  requires approval which this Codex hook cannot request. Include the exact
  matched pattern and the settings-file path that supplied it. Preserve this
  provenance through loading, evaluation and formatting instead of reducing
  the result to a boolean. It must not suggest
  disabling safeguards or retrying through a different tool to evade the rule.
- Ordinary allow/advisory paths return no decision or `additionalContext`;
  they do not grant a host permission or return a bare `allow` decision.
- HTTP routing reuses the existing quote handling, silent-download exceptions
  and one-use child evidence. No replacement command is approved implicitly.
- Malformed Codex pre-tool payloads, including invalid Bash command input,
  produce a denial before state writes. This is specific to the new adapter;
  retain the existing Claude malformed-input behavior.
- Argument rewriting is deliberately unused, as explained in §4. A mistaken
  internal request to that unimplemented adapter path fails explicitly; the
  message must not claim that Codex lacks input rewriting.

Denies still take precedence over matched asks in a compound request. A returned
policy message must distinguish an actual deny from an unsupported approval
requirement. Native host approval cannot substitute for the latter automatically:
the two systems may evaluate different policies.

`PermissionRequest` cannot bridge these rules: it runs only when Codex is
already about to request approval, and PreToolUse cannot invoke it. See the
rejected alternative in §10. An unmatched evaluator default of `ask` with no
matched pattern remains the existing pass-through case, not a new denial.

Add source-path metadata to `SecurityPolicy` and the returned `CommandDecision`.
Keep the evaluator's current ordering; retain the first matched ask across
commands/embedded commands while continuing to look for later denies or scanner
failures. Pass a formatted reason into the adapter's ask response; Claude can
ignore it and keep its existing response shape. File-backed rules must retain
their real path; in-memory test policies use an explicit in-memory label. Do
not dump command bodies or settings contents to explain a match. Today's deny
message names only its pattern; source-file provenance is a new requirement,
not an existing capability to assume.

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

**Bind the owner at setup time.** Add a committed `.codex/capy-owner` marker
containing a version tag and a random 128-bit installation ID, represented as
32 lowercase hexadecimal characters. This ID is a portable locator, not a
credential or authorization token. Generate it once using Go's standard
cryptographic randomness; preserve it on every subsequent setup. Invalid
existing markers fail rather than being regenerated. Creation must be exclusive
so concurrent setup cannot silently select two IDs. The marker travels with a
clone, move or worktree; it contains no absolute path. Copies retain the same ID,
so the ID alone cannot identify a physical config-source directory.
If the marker is missing but existing owned hook/MCP definitions already bind
an ID, fail with instructions to restore that marker before rerunning setup;
do not silently rotate the live binding. First migration from today's unbound
MCP block may create an ID normally.

Embed that same ID as a literal argument in every owned hook command and the
owned MCP server command. It is not sufficient to find whichever wrapper is
nearest to cwd or to count surviving markers. Use one generator-owned inline
bootstrap, parameterized by ID, launch mode and event. It locates the installed
capy binary using the wrapper's existing search locations, then calls a narrow
`capy codex-launch` command. That command performs the following read-only
preflight **before invoking any project wrapper** on either launch path:

1. Walk every physical ancestor of process cwd to filesystem root, including
   ancestors beyond Git boundaries. At each directory inspect the fixed paths
   `.codex/capy-owner`, `.codex/hooks.json` and `.codex/config.toml`. Read markers
   with the existing 80-byte limit and strict version/ID validation; bound each
   config to 1 MiB, detecting overflow rather than accepting a truncated prefix.
   Missing files are allowed individually. An existing unreadable, oversized,
   malformed, nonregular or symlink file, or a symlink `.codex` directory, fails
   the launch. Other inspection errors are not treated as absence.
2. Parse JSON and TOML in Go, using the standard JSON decoder and existing TOML
   dependency. Inspect hook command fields in both representations and MCP
   command/argv fields for capy's canonical versioned invocation (§6.2). A
   decoded generated binding is evidence of an installation even when its
   marker is missing. Do not use grep on serialized JSON/TOML, execute config
   text, or require a new shell-side parser dependency. Valid unrelated config
   without a capy binding does not require a marker.
3. Collect **directories**, taking the union of marker locations and generated
   binding locations; never deduplicate by installation ID. Every binding must
   have a valid matching marker in its own `.codex` directory. A missing or
   mismatched adjacent marker is an error even if another directory has that
   ID. Require exactly one installation directory, with the embedded ID and a
   binding for the invoked mode/event. A lone marker cannot authorize a stale
   generated command. Finish the whole ancestor scan before selecting an owner;
   never return early upon finding a usable nested candidate.
4. Use the validated config directory's parent project as the sole owner and
   require its own readable regular `.codex/scripts/capy.sh`. Quote paths and
   keep event stdin intact. For hooks,
   invoke `hook --platform codex <event> --project-dir <owner>`; for MCP invoke
   `serve --project-dir <owner>`. Do not change process cwd merely to hide the
   original event directory from cwd-relative policy evaluation.
5. Supervise wrapper launch so failures before or during invocation receive
   the mode-specific handling in §6.3. Diagnostics go to stderr, never into MCP
   protocol stdout. Preserve serve's actual exit status after it starts.

The bootstrap contains binary discovery and failure supervision only; the Go
preflight owns config decoding and owner validation and opens no policy/state/DB.
Setup and doctor reuse that inspection logic rather than maintaining a weaker
marker-only check. A missing or older binary without `codex-launch` fails under
§6.3; there is no fallback to a project wrapper before preflight. Generate binary
lookup from the same fragment used by the wrappers to avoid diverging search
paths. This adds a short-lived launcher process, with no daemon or parser package.
Validation is conservative: it scans installed artifacts regardless of host
activation/trust, and malformed ancestor config can block launch even if it
would contain no capy binding. Repair that config rather than skipping an
uninspectable potential owner; valid unrelated config remains allowed.
The MCP TOML launches the inline bootstrap via `bash` with structured arguments,
replacing cwd-relative wrapper argv; credential forwarding remains unchanged.
Pass parameters as data, not interpolated executable text, when generating
shell/JSON/TOML forms. The bootstrap supervises the launcher too, so an early
CLI error cannot escape the pre-tool exit-2 contract.

**V1 rejects overlapping installations.** Two installation directories on the
ancestor chain cause both handlers and MCP to fail before wrapper invocation,
policy, observation or DB access. This includes identical IDs copied into
nested directories with one marker subsequently deleted: the retained outer
hook/MCP definitions still identify an installation with a broken binding. A root
policy therefore cannot silently disappear in favor of a nested policy, and
the same event cannot record or consume evidence twice. Ordinary subdirectories
without capy installation artifacts are supported, including valid unrelated
Codex configuration. Setup checks ancestor markers **and config bindings**;
runtime checks also catch a nested installation activated after an ancestor was
set up. It need not recursively scan the whole repository during root setup.
Physically nested worktrees with an ancestor installation are also rejected;
ordinary sibling worktrees remain portable. Supporting multiple active capy
owners and MCP precedence is outside v1, not an implicit policy merge.

The supported command-launch contract is that process cwd is below its owning
marker. An outside-tree launch fails; an explicit direct CLI invocation remains
available for controlled manual use. The generated command can resolve an owner
in a non-Git directory, but **Codex project-hook discovery there is unverified**.
Do not advertise automatic non-Git host integration based on launcher tests.

This is validation of generated configuration on disk, not access to the host's
cached config-source identity or trust state. Keep loaded generated definitions
in place until the host reloads them; stop/reload affected sessions before
removing or relocating an installation. If every originating artifact is removed
while an old command remains cached, a portable copied ID cannot recover that
lost provenance. Concurrent installation mutation and arbitrary hand-rewritten
launch commands are outside this contract; detected read/change errors fail
closed. Do not claim protection against those cases from the missing-marker test.

### 6.2 Merge ownership

Recognize owned handlers through the canonical versioned bootstrap invocation,
embedded installation ID and explicit mode/event arguments, not a loose `capy`
or `hook` substring. Give generated invocations a fixed `capy-codex-launch-v1`
identifier. After JSON/TOML decoding, use a shared recognizer for the generator's
literal command/argv grammar; validate ID, mode, event and bootstrap shape without
shell evaluation. A recognizable capy invocation with an unsupported version or
invalid shape is an installation error, not an unrelated command to skip.
JSON escaping or TOML quoting changes that decode to the same invocation retain
the binding. Read inline hooks and aliased MCP entries for binding detection,
but do not rewrite them. Cross-check the ID with the owning config directory's
marker; a copied/mismatched definition is an installation error, not permission
to adopt another owner's command. Preserve the marker's ID during upgrades.
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

For this repository, preserving the toolbox startup entry temporarily duplicates
capy guidance on startup/resume. The measured pre-implementation baseline is
9,500 bytes from `.capy/AGENTS.md` plus 3,240 bytes from `RoutingBlock()`:
12,740 UTF-8 bytes before unrelated toolbox text. This is a composition
projection, not an observed new-hook session. Record actual post-change sizes
and a link to the toolbox follow-up in `verification.md`; do not silently remove
provider instructions or pretend that host spilling deduplicates the text.

### 6.3 Wrapper errors and activation

**Fail closed for controllable Codex pre-tool launch failures.** Today's Claude
wrapper suppresses a running binary's nonzero exit and continues; only the
missing-binary path emits a denial. Preserve that legacy behavior for Claude.
For Codex, treating missing and failed enforcement consistently is preferable
to silently running a command whose hook could not evaluate it.

The common bootstrap, Go launcher and shared wrapper enforce this contract:

| Outcome | Codex PreToolUse | Other hooks / MCP |
|---|---|---|
| Owner/config validation failure, missing adjacent marker, overlap, outside-tree cwd | Exit **2**, nonempty explanatory stderr | Diagnostic nonzero exit |
| Missing/incompatible launcher binary or failed launcher invocation | Exit **2**, nonempty explanatory stderr | Diagnostic nonzero exit |
| Missing, unreadable or vanished wrapper; launch exit 126/127 | Exit **2**, nonempty explanatory stderr | Diagnostic nonzero exit |
| Missing capy binary or unsuccessful Go pre-tool invocation | Exit **2**, nonempty explanatory stderr | Diagnostic nonzero exit; preserve serve status |
| Successful structured policy denial | Exit **0**, valid denial JSON | Not applicable |
| Successful pass/advisory | Exit **0**, supported output | Normal event/MCP behavior |

Do not use an unchecked `exec` that lets a lookup/open race escape as 126/127;
the bootstrap must observe and remap wrapper launch failure. A successful exit-0
JSON denial must not be relabelled as a crash. Apply wrapper-generator changes
to both committed wrapper copies and their tests, as required by AGENTS.md.

Every infrastructure-failure reason names the failed boundary and a repair:
install/upgrade capy for a missing/incompatible binary, or repair permissions
and rerun `capy setup --platform codex` from the intended project for damaged
artifacts. Missing/corrupt live owner markers require restoration from the
committed configuration, rather than arbitrary ID regeneration. A collaborator
who trusted committed hooks without installing capy can lose all matched
Bash/capy calls. Explain that repair can be performed from
an external terminal; the user may disable the capy-owned entries in `/hooks`
while repairing an unavailable installation. Distinguish this operator recovery
from policy denials: never recommend disabling hooks to bypass a matched rule.
Overlap errors identify the conflicting installation directories and require
consolidation to one owner or a separate non-overlapping checkout, not nearest
owner substitution. Do not fabricate PreToolUse JSON for another event.

This does not make hooks a security boundary against a host killing/skipping
them. Missing trust, disabled hooks, host timeouts and unsupported runtime
versions remain host limitations that diagnostics and activation instructions
must disclose.

Setup reports **registered; activation requires project-layer trust and review
of the exact hook definitions**. These are two distinct host gates. It never
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
Check the marker and consistent hook/MCP owner binding with the runtime inspector,
including ancestor generated bindings whose adjacent marker is missing. Report
overlapping installation directories even when their IDs are identical.
Distinguish missing, partial, malformed and duplicate
registration. Validate the Codex MCP table and bootstrap argv, not merely its
presence, with the repository's existing TOML dependency. A visible
project-level `features.hooks = false` (or deprecated alias) warrants a warning;
do not attempt to reproduce all host config precedence or read private trust
state. Say that trust and actual execution are unverified by the file check.

Verification has three levels:

1. Adapter/router tests with pinned synthetic payloads, covering identity,
   policy decisions, startup formatting, observations and failure paths.
2. Generated-installation tests that run the actual wrapper/CLI in isolated
   project trees, with unset production credentials and isolated policy homes.
   Keep hook probes credential-free. For MCP, use synthetic keys and actually
   initialize the server, list tools and make a harmless capy call; assert that
   it uses the same execution project as the hook and the storage target
   configured for that project (which may reside outside the project).
   Include repeat setup, mixed third-party groups, paths containing spaces,
   ordinary subdirectories, moved trees, command-only non-Git fixtures and
   sibling worktrees. A two-owner fixture has different harmless deny rules and
   separate state directories: both generated handlers must reject overlap,
   neither marker may be substituted, and no observation is written/consumed.
   Also remove one owner's wrapper/marker and verify no fallback to the other.
   In one Git repository, copy the root `.codex` configuration unchanged into
   `packages/service`, retain both owners' hook/MCP definitions, and delete only
   the outer marker. Both pre-tool commands must exit 2 with a reason naming the
   broken outer binding; MCP must fail diagnostically. Assert that neither
   wrapper is invoked and neither owner's policy/state/DB is accessed. Repeat
   with only the outer hook binding retained, only its MCP binding retained,
   escaped JSON/equivalent TOML strings and unrelated marker-free ancestor config.
   Assert exact exit 2 plus nonempty repair text for pre-wrapper failures,
   including unreadability and disappearance after lookup.
3. A recorded live Codex 0.162.1-or-newer session: startup injection, harmless
   allowed/denied commands, a matched approval rule, a child observation and
   redirect/fallback sequence, and unchanged unrelated hooks. Record the exact
   runtime version and which behaviors were observed. Never call configuration
   tests or manual stdin replay proof that the host delivered an event.
   Include a host launched in an ordinary subdirectory with a working capy MCP
   call, and record actual capy startup-context bytes from both installed hooks.

Unit tests can supply synthetic deny patterns and commands that only print
markers. Live probes must not read secrets, make unnecessary HTTP calls or
disable host safeguards. Test cleanup separately if ending the host session
cannot be observed live; clearly label that limit.

## 8. Assumptions

| Assumption | Validation / consequence if false |
|---|---|
| Codex 0.162.1's documented and pinned fields reach installed hooks | Capture synthetic runtime observations in the live check; adjust the adapter before claiming support |
| Child tools share their parent's session ID and supply a distinct agent ID | Verify parent/child payload association; do not release cleanup/evidence claims if it differs |
| Generated bindings on disk remain present while their host commands are loaded | Stop/reload sessions before removing or moving installations; a cached command after all originating artifacts disappear has no recoverable config-source identity |
| Exactly one installation directory above process cwd has matching local bindings and marker | Exercise root, ordinary subdirectories, moves and sibling worktrees; reject missing-adjacent-marker/copied-ID combinations, mismatches and overlap before wrapper invocation |
| Codex loads the intended project layer after project and hook trust | Verify in live Git-worktree sessions; non-Git command resolution alone establishes no host-discovery support |
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
- Multiple overlapping capy installations: v1 rejects ambiguous composition
  rather than merging project policies or recording duplicate evidence.
- Universal compatibility claims for all Codex versions, desktop/cloud
  orchestration or Windows: advertise only platforms actually verified within
  capy's existing platform constraints.

## 10. Rejected alternatives and trade-offs

| Direction | Benefit | Cost / reason not chosen |
|---|---|---|
| Generate the two toolbox shell scripts as proposed in #29 | Small initial patch using known working scripts | Duplicates routing/security policy and takes ownership of unrelated provider instructions and file restrictions |
| Register the current Claude adapter unchanged | Fewest code changes | Wrong identity/env assumptions and unsupported approval responses; it can turn an intended approval requirement into execution |
| Bridge matched asks through `PermissionRequest` | Could reuse the native approval UI if it were triggerable | Fires only for host-initiated approvals; PreToolUse cannot cause it to run, so it cannot implement capy's independent ask rules |
| Locate the nearest wrapper from cwd | No installation metadata | Loses the defining handler's owner when config layers compose; cannot distinguish a missing owner from a replacement installation |
| Count only matching owner markers | Small bootstrap with bounded reads | A copied nested ID survives deletion of the outer marker; runtime must inspect retained config bindings before selecting a directory |
| Embed absolute installation paths | Direct source-owner binding | Breaks committed portability across collaborators, moves and worktrees; local config/marker validation preserves portability while definitions remain installed |
| Codex adapter over the shared Go router — chosen | One policy implementation with explicit host semantics | Requires adapter/installation tests and deliberate unsupported-operation handling |
| Introduce a generic new hook framework | Could support more hosts later | Unnecessary abstraction and migration for one additional host |

Returning denial for a matched approval requirement is deliberately stricter
than deferring to Codex's independent policy. It preserves the requirement and
explains the limitation; implementing a native approval bridge is not assumed.

## 11. Rollout and follow-ups

Ship the adapter, setup, committed generated artifacts and diagnostics together.
Upgrade capy, rerun setup, retain the generated owner marker with its config,
establish project trust, review/trust new hooks in Codex, and perform the live
probe. Existing toolbox scripts can remain throughout; record their temporary
duplicate startup content and the linked follow-up. Stop affected sessions
before removing or relocating their loaded installation artifacts. Rollback removes only
the newly generated capy hook entries and restores the previous capy binary;
it does not require reverting toolbox or changing stored data.

The [implementation follow-ups](implementation.md#6-follow-ups) record the
proposed claude-toolbox enhancement and remaining #132 output-policy work. Do
not close #132 solely on completion of this feature or describe the upstream
cleanup as already shipped.
