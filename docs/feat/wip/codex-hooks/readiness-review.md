# Codex hooks: fresh implementation-readiness check

> Date: 2026-10-12
> Original scope: fresh read of all five feature documents; no new skills or agents
> Original verdict: **not ready — RD-1 remained partially unresolved**
> Current verdict: **design blocker resolved; implementation acceptance pending**
> Resolution: [revision and re-review below](#resolution-and-re-review), 2026-10-12

The original assessment is preserved below. The shared Go adapter/router
direction, event coverage, policy handling and
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

The fix was deferred in the original assessment because that request asked for
readiness review, not another architecture revision. The follow-up correction
below closes this design gap and adds the concrete implementation acceptance case.

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

## Resolution and re-review

The follow-up request authorized resolving the issue. Design §6.1,
implementation §3.1, Tasks 2/5/6 and the RD-1 disposition now agree on a
config-aware runtime preflight. A small installed Go launcher inspects ancestor
markers and decoded hook/MCP bindings **before any project wrapper runs**.
Directories containing retained bindings remain installation candidates after
their marker disappears; a matching marker in another directory cannot satisfy
the adjacent-marker check. Installation directories are counted separately even
when their IDs are identical.

For the original counterexample, `/repo/.codex/hooks.json` and the MCP config
still bind A, but `/repo/.codex/capy-owner` is absent. Preflight must report that
broken outer binding. It cannot return the nested wrapper, regardless of which
of the two generated commands invoked it. Task 2.6 requires exact pre-tool exit
2, nonempty repair text and a sentinel proving neither wrapper started; MCP
must fail without protocol output or DB access. The fixture stays within one
Git repository and also covers retaining only either outer binding.

**Standard kk:review-design assessment: SOUND for the revised design scope;
no outstanding P0/P1/P2 design finding.** This was an in-session review, not an
independent or live runtime verification. Checked:

- Full-chain scanning, per-directory binding validation and no early selection.
- JSON/TOML decoding rather than serialized-text matching; invalid/unreadable/
  oversized/symlink inputs fail, valid unrelated config remains supported.
- Shared setup/runtime/doctor inspection and literal versioned invocation
  recognition, with no config execution or new parser dependency.
- Failure supervision before the launcher and before the wrapper, MCP stdout/
  status preservation, unchanged event stdin, and cancellation acceptance.
- Copied-ID/missing-marker, distinct-ID, partial binding, ordinary subdirectory,
  move and sibling-worktree cases; original findings RD-2/RD-3 and R2-1–R2-7
  retain their dispositions and acceptance requirements.
- Explicit limits: current on-disk generated bindings are required; stop/reload
  sessions before removing or relocating loaded installations. This does not
  recover deleted config-source provenance from an old host-cached command.
- Six pending implementation tasks retain dependencies, scope exclusions and
  named acceptance checks. No task is marked done by this documentation fix.

The review assessed the written algorithm and implementation plan. Test names
are future acceptance requirements; no Go launcher, generated installation or
live Codex execution was introduced or tested by this correction. See
[verification.md](verification.md#rd-1-readiness-correction) for design-stage checks.
