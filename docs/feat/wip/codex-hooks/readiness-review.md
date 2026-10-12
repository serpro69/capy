# Codex hooks: fresh implementation-readiness check

> Date: 2026-10-12
> Scope: fresh read of all five feature documents; no new skills or agents
> Verdict: **not ready — RD-1 remains partially unresolved**
> Changes made by this check: this review record only

The shared Go adapter/router direction, event coverage, policy handling and
task sequencing are coherent. The previous findings have explicit design
dispositions and acceptance checks. However, the new marker algorithm does not
fully satisfy its own missing-owner requirement. This supersedes the earlier
unqualified claim that RD-1 was fixed in [review-resolution.md](review-resolution.md).

## P1 — Copied installation IDs can still substitute the wrong owner

References: [owner lookup](design.md#61-files-and-invocation),
[launch implementation](implementation.md#31-owner-bound-hook-and-mcp-launch),
[Task 2](tasks.md#task-2-bind-hook-and-mcp-launch-to-the-same-portable-owner).

The marker is committed and intentionally travels with copied configuration,
clones and worktrees. Its ID therefore identifies portable installation
configuration, not a unique physical config-source directory.

Concrete counterexample, within **one Git repository**:

1. `/repo/.codex/hooks.json` and `/repo/.codex/capy-owner` contain ID A.
2. The generated `.codex` directory is copied into `/repo/packages/service`,
   retaining A. There is no nested `.git` boundary: both project config layers
   can be active when Codex runs from `packages/service`.
3. The outer marker is lost, but its hook/config definitions remain. The nested
   marker and wrapper are intact.
4. For the outer handler, the prescribed scan now finds exactly one marker
   with the expected ID A and launches the nested wrapper with the nested
   project directory.

Both surviving markers would have caused the intended overlap rejection. The
combination of **copied ID plus missing outer marker** defeats that check.
Rejecting mismatched IDs handles independently initialized nested installations
but does not cover copied configuration. Setup/doctor cross-checks do not help
an already-configured runtime invocation: the specified bootstrap reads markers
only, so it never notices the outer generated config missing its adjacent marker.

The result contradicts the design's guarantee that a missing owner cannot be
substituted and the implementation plan's explicit requirement that the root
command never run as a nested owner. `ReadBashPolicies` reads from the selected
project, so the outer policy can be omitted and observations can use the wrong
directory. This is the original RD-1 failure class in a narrower scenario.

**Evidence:** a read-only model of the exact count-and-ID-match rule selected
`/repo/packages/service` for the outer handler. This is a counterexample to the
written algorithm, not a live failure of an implementation that does not exist.
The current settings loader and explicit-project context path were re-read to
confirm the consequence.

**Required correction:** define a runtime owner check that also detects an
ancestor's generated config binding without its matching adjacent marker, or
choose another mechanism that retains the originating config directory. A
marker-only scan cannot distinguish this case. Add the combined copied-ID /
missing-outer-marker fixture, with both config files retained, to Task 2. Require
the outer pre-tool handler to reject with exit 2 and a reason before invoking
the nested wrapper or accessing its policy/state. Apply the same no-substitution
rule to MCP launch. Update the design, plan and earlier disposition together.

The fix is deferred because this request asks for a readiness assessment, not
another architecture revision. This concrete acceptance case must be closed
before treating the complete plan as ready for implementation.

## Status of the other supplied findings

| Finding | Readiness assessment |
|---|---|
| RD-2 — MCP relative launch | Addressed in scope and tests: hook/MCP bootstrap parity plus actual MCP initialization and harmless calls |
| RD-3 — pre-wrapper failure status | Addressed: exact exit 2 plus nonempty stderr, including lookup/open races |
| R2-1 — PermissionRequest | Addressed: alternative explicitly rejected with the correct limitation |
| R2-2 — failure recovery | Addressed: deliberate Claude/Codex difference and operator recovery are explicit |
| R2-3 — ask provenance | Addressed end-to-end through loading, evaluation and formatting, with ordering tests |
| R2-4 — rewrite terminology | Addressed with the input/output capability distinction |
| R2-5 — context spill | Addressed with a byte bound, omitted limit overrides and live acceptance; the earlier PostToolUse premise was corrected |
| R2-6 — non-Git scope | Addressed: command resolution and host discovery are separate claims; both trust gates are named |
| R2-7 — duplicate guidance | Addressed by explicit measurement and a linked follow-up; duplication is intentionally deferred, not eliminated |

No further implementation-blocking issue was found in this fresh document
read. The explicit restrictions on overlapping installations and physically
nested worktrees, and the temporary duplicate startup context, remain design
trade-offs rather than unrecorded gaps. No Go code, existing design text, staged
changes, hook configuration, trust state or external resources were modified.
