# Codex hooks: design review corroboration and disposition

> Date: 2026-10-12
> Scope: the two supplied reviews and subsequent readiness finding; documentation revision only
> Updated: [design.md](design.md), [implementation.md](implementation.md), [tasks.md](tasks.md)
> Evidence record: [verification.md](verification.md)

All ten findings identify a design issue requiring a change. Eight are valid as
stated; two contain technical overstatements corrected below. The changes are
design dispositions, not claims that the future runtime implementation passed
its acceptance tests. Review 2 had no IDs; `R2-1` through `R2-7` below identify
its findings in the supplied order.

## Review 1

### RD-1 — Nearest-wrapper lookup loses config ownership

**Valid; corrected after the readiness re-review.** Pinned discovery appends handlers from every
config layer, and command execution uses event cwd. Capy's settings loader
reads the selected project's policy files, not every ancestor project's files.
Thus both root and nested handlers would select the same nearest nested wrapper
under the previous algorithm. This is a corroborated inference from code; the
unimplemented integration was not tested live.

The first revision used `.codex/capy-owner` and a marker-only ancestor scan.
That was insufficient: the [readiness finding](readiness-review.md) demonstrated
that copied IDs plus a missing outer marker could select the nested owner while
the outer generated config remained installed. The earlier unqualified "fixed"
disposition was incorrect.

The corrected §6.1 requires a Go preflight before any project wrapper runs.
It decodes ancestor hook/MCP configuration as well as markers, counts physical
installation directories, and validates each binding against its **adjacent**
marker. Retained outer definitions therefore cause rejection when their marker
is missing, even if a nested copy has the same ID. The inline bootstrap locates
and supervises `capy codex-launch`; it never selects a project wrapper first.
Setup and doctor share the inspector. Pre-tool lookup/config/launcher failures
retain RD-3's exact exit-2/nonempty-stderr contract; MCP fails diagnostically.

Task 2.6 specifies the combined same-repository/copied-ID/missing-outer-marker
fixture, with both hook/MCP configs retained, sentinel wrappers and assertions
that policy/state/DB access never begins. Hook-only/MCP-only retained binding
variants and decoded escaping prevent a partial or text-grep-based fix. Valid
unrelated config and normal subdirectory/move/worktree cases remain supported.
These are design requirements, not passing implementation tests. The contract
also explicitly requires loaded definitions to remain on disk until host reload:
an origin deleted entirely while its command is cached cannot be recovered from
a copied portable ID.

Sources: [discovery](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/discovery.rs),
[command runner](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/command_runner.rs),
[project layers](https://learn.chatgpt.com/docs/config-file/config-advanced#project-config-files-codexconfigtoml),
`internal/security/settings.go:ReadBashPolicies`.

### RD-2 — MCP argv remains relative to launch cwd

**Valid; reproduced and fixed in the design.** The generated MCP block passes
`.codex/scripts/capy.sh` to bash without a cwd. The pinned client passes args
through and the launcher selects its runtime cwd when none is configured.
Replaying that argv from this repository's `internal/` returned exit 127 with
the wrapper-not-found error. No server was started by this path-only probe.

Task 2 now changes the MCP generator and committed TOML to the same owner-bound
bootstrap as hooks, retaining credential forwarding and explicit project
ownership. It requires MCP initialization, tool listing and a harmless call
with synthetic credentials at each supported location. Credential-free hook
tests remain separate. Live acceptance includes a subdirectory host launch.

Sources: `internal/platform/setup.go:codexMCPBlock`,
[MCP client](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/codex-mcp/src/rmcp_client.rs),
[stdio launcher](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/rmcp-client/src/stdio_server_launcher.rs).

### RD-3 — Bootstrap failure may not block

**Valid; fixed in the design.** The pinned pre-tool parser blocks exit 2 only
with nonempty stderr. Exits 1/126/127 report failure and allow the tool path to
continue. A wrapper cannot handle a failure to locate/open itself.

§6.3 and Task 2 explicitly cover both bootstrap and wrapper boundaries, including
the check/open race. Pre-tool failures require exact exit 2 and a nonempty
repair reason; other events/MCP fail diagnostically. Tests name missing,
unreadable and vanished wrappers, outside-tree launches and successful exit-0
structured denials. Infrastructure failure is distinguished from policy denial.

Source: [PreToolUse parser](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/events/pre_tool_use.rs).

## Review 2

### R2-1 — PermissionRequest alternative is missing

**Valid; fixed.** The host event participates in approvals Codex already decided
to request; it is not a PreToolUse-triggerable prompt bridge. The pinned event
module and official reference agree. §5.3 explains the distinction, §10 rejects
the bridge explicitly, and Task 1 keeps the approved matched-ask denial behavior.

Sources: [PermissionRequest](https://learn.chatgpt.com/docs/hooks#permissionrequest),
[pinned event module](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/events/permission_request.rs).

### R2-2 — Crash policy and operator recovery are unstated

**Valid; fixed.** `capyWrapperScript` currently suppresses a Claude hook binary's
nonzero exit, while its missing-binary path emits a deny. The proposed Codex
failure behavior is intentionally stricter, and a trusted collaborator without
capy can block every matched tool call.

§6.3 now states that trade-off and requires failure messages naming installation
repair, external-terminal recovery and the user's `/hooks` control. Task 2 tests
the messages; Task 6 documents and exercises the collaborator scenario. These
remedies are for unavailable infrastructure and are not advice to evade matched
policy rules. The existing Claude behavior remains unchanged.

Sources: `internal/platform/setup.go:capyWrapperScript`, pinned PreToolUse parser.

### R2-3 — Matched-ask rule provenance is missing

**Valid; fixed.** `SecurityPolicy` and `CommandDecision` currently omit a source
path; hook code reduces matched asks to a boolean and calls reason-less
`FormatAsk()`. The current deny message names its pattern, not a source file.
Source-file reporting cannot be obtained merely by changing Codex formatting.

§5.3 and Task 1 now require provenance through settings loading, evaluation,
compound/embedded-command handling and adapter formatting. Acceptance covers
all three files, identical patterns in different sources, later-deny precedence
and unmatched default asks. Claude's JSON response shape remains unchanged.

Sources: `internal/security/{settings,eval}.go`,
`internal/hook/pretooluse.go:checkCommandsSecurity/checkCodeSecurity`,
`internal/adapter/claudecode.go:FormatAsk`.

### R2-4 — Rewrite capability terminology

**Partly valid; corrected with a distinction.** Input rewriting through
`updatedInput` is supported by Codex and deliberately unused by this integration.
The review's broader implication that all output rewriting is host-supported
is not established: `updatedMCPToolOutput` is unsupported at this baseline.
§4/§5.3 and Task 1 now distinguish those cases and explain the SubagentStart
choice. Capability comments must not confuse capy's selected behavior with host
capability.

Source: [PreToolUse/PostToolUse reference](https://learn.chatgpt.com/docs/hooks).

### R2-5 — Context spilling and handler limits

**Partly valid; the missing budget is fixed, one premise rejected.** Pinned
`output_spill.rs` confirms the default approximate 2,500-token threshold and
file-backed preview. However, pinned discovery explicitly accepts
`additionalContextLimit` for **PostToolUse**; the review's claim that it warns
there is incorrect. SessionEnd does not support it.

The design omits the field for every generated handler and sets a 4,096-byte
capy startup/child-context acceptance bound. Tasks 1/3/4/6 cover bounded payloads,
field omission and live no-spill verification. The chosen bound is not an exact
model-token estimate and does not rely on raising the host limit.

Sources: [output spiller](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/output_spill.rs),
[discovery normalization](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/hooks/src/engine/discovery.rs).

### R2-6 — Non-Git launcher versus host discovery

**Valid; fixed.** A successful ancestor-resolution test cannot prove Codex loads
that project's config. The host independently gates project layers and exact
hook definitions on trust.

§6.1, §6.3, §7, §8 and the implementation/tasks now label non-Git tests as
command-only evidence and leave automatic discovery unverified. Live acceptance
uses supported Git-worktree sessions and explicitly covers both trust gates.

Sources: [project configuration](https://learn.chatgpt.com/docs/config-file/config-advanced#project-config-files-codexconfigtoml),
[hook trust](https://learn.chatgpt.com/docs/hooks#review-and-trust-hooks).

### R2-7 — Duplicate capy startup guidance

**Valid; addressed by measurement and an explicit follow-up, not automatic
toolbox edits.** The committed startup script reads `.capy/AGENTS.md`, and its
startup/resume matcher overlaps capy's planned startup hook. Current source
sizes are 9,500 and 3,240 UTF-8 bytes, totaling 12,740 before other toolbox text.
That is a projected composition until both hooks are installed.

`verification.md` records those measurements and the follow-up. §6.2/§11 and
Tasks 3/6 require actual implemented sizes and a future upstream PR link.
Preserving unrelated template behavior was agreed with the user; the duplicate
injection remains temporary, explicitly tracked overhead. No runtime removal
or upstream submission is falsely claimed by this documentation fix.

Sources: `.codex/hooks.json`, `.codex/scripts/session-start.sh`,
`.capy/AGENTS.md`, `internal/hook/routing.go:RoutingBlock`.

## Result and remaining verification

The design retains six implementation tasks; Task 2 now includes the shared
config-binding inspector and pre-wrapper launcher, and Task 5 reuses it for
diagnostics. No implementation, generated configuration, hook trust or external
issue/PR was changed. The [focused re-review](readiness-review.md#resolution-and-re-review)
closes the outstanding design blocker; live capy hook/MCP verification and all
named acceptance tests remain pending implementation.
