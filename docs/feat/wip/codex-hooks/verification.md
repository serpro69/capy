# Codex hooks verification record

> Updated: 2026-10-12
> Current scope: design corroboration only; implementation/live acceptance pending
> Design: [design.md](design.md) · Dispositions: [review-resolution.md](review-resolution.md)

## Design-stage evidence

- Read all three feature documents, existing setup/wrapper, policy loading and
  matching, hook ask/deny formatting, and the current toolbox startup scripts.
- Read Codex source pinned at `rust-v0.162.1` for hook discovery, command cwd,
  pre-tool failure parsing, PermissionRequest, MCP argv/cwd and output spilling;
  checked official project-layer/trust references. Exact source links and
  qualified conclusions are in the disposition record.
- Replayed the current MCP wrapper argv from `internal/`, after checking the
  relative wrapper was absent there. Result: exit **127**, with
  `bash: .codex/scripts/capy.sh: No such file or directory`. This proves the
  current path-resolution failure only; no server or live Codex session was
  started by this probe.
- No production Go code changed, so no Go suite, race suite or quality benchmark
  was run for this documentation revision. A read-only validator checked all
  34 local links/anchors across the five documents. The initial diff whitespace
  check found space indentation in the task graph; the graph was corrected
  before rerunning the check. The final `git diff --check` passed, as did
  validation of all six task records, dependency references and subtask numbers.

## Startup context baseline

Measured directly from repository source on 2026-10-12:

| Capy content | UTF-8 bytes | Producer |
|---|---:|---|
| Generated `.capy/AGENTS.md` | 9,500 | Existing toolbox session-start loader, when present |
| Current `RoutingBlock()` | 3,240 | Existing Go router; baseline for the planned Codex variant |
| Projected combined content | 12,740 | Both startup paths, before toolbox provider/extra instructions |

This is not an observed session with the new integration. The toolbox entry is
currently configured for startup/resume with `additionalContextLimit: 10000`;
the future capy entries will omit that field and keep their own context within
4,096 bytes. Host spill limits operate on individual outputs and do not remove
duplicate instructions from different hooks.

**Accepted temporary overhead:** preserve the external loader until the
[claude-toolbox enhancement follow-up](implementation.md#claude-toolbox-enhancement-pr)
is ready. A published upstream issue/PR link does not exist yet; add it here
when created. During implementation, replace the projected sum with measured
actual startup/resume outputs and preserve the distinction between bytes,
host approximate-token estimates and model-visible previews.

## Pending implementation acceptance

- [ ] Stable owner marker, bound hook/MCP argv, correct explicit project owner.
- [ ] Root, ordinary subdirectory, move, spaces and sibling-worktree launches.
- [ ] Non-Git command resolution labelled separately from unverified host discovery.
- [ ] Two-owner/copy/missing-owner rejection before policy, evidence or DB effects.
- [ ] Missing/unreadable/vanished-wrapper and outside-tree bootstrap failures:
  exit exactly 2 plus nonempty repair text for PreToolUse.
- [ ] Binary failure/recovery, including a collaborator without capy; exit-0
  structured denials remain distinct from infrastructure failures.
- [ ] Synthetic-credential MCP initialization, tools/list and harmless call from
  root and an ordinary subdirectory; hooks separately work without keys.
- [ ] Ask denial identifies matched pattern/source, preserves later-deny priority
  and does not turn an unmatched default ask into a denial.
- [ ] Startup/child context budget, omitted limit fields and live no-spill result.
- [ ] Both trust gates, actual hook delivery and parent/child observation lifecycle.
- [ ] Actual mixed-installation startup byte measurements and toolbox follow-up link.
- [ ] Required FTS5/race/artifact checks and implementation review dispositions.
