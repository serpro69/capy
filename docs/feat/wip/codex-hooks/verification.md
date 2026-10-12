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

## RD-1 readiness correction

The [readiness finding](readiness-review.md) invalidated the marker-only lookup.
This revision changes the written contract and acceptance plan; it does not
implement the launcher. Re-read the existing policy loader, explicit-project
hook context, wrapper binary lookup and CLI dispatch. They support the stated
consequence (a substituted owner selects different policy/state) and a separate
read-only launch command without initializing storage. The existing TOML
dependency is available; no dependency change is proposed.

The [official hook reference](https://learn.chatgpt.com/docs/hooks) was rechecked
for composition and cwd: matching hooks from multiple files run, and command
hooks use the session cwd. That supports checking all ancestor bindings rather
than assuming the closest config replaced its ancestor. This recheck does not
replace the pinned baseline research or constitute live capy verification.

Design-level traces of the revised preflight, assuming unchanged on-disk
generated bindings during an invocation:

| Fixture | Required outcome before any project wrapper |
|---|---|
| One directory with matching marker and invoked binding | Select that directory |
| Ordinary subdirectory or valid unrelated marker-free config | Select the same sole installation |
| Root/nested installations with distinct IDs | Reject overlapping directories |
| Root/nested installations with copied IDs | Reject overlapping directories; never deduplicate by ID |
| Copied ID, outer marker missing, both outer configs retained | Reject outer binding without its adjacent marker |
| Same case with only outer hook or only outer MCP binding retained | Reject the remaining broken outer binding |
| Equivalent JSON escapes or TOML quoting | Decode to the same binding and reject the same missing-marker case |
| Unreadable, oversized, malformed or symlink config | Fail inspection; do not skip to a nested candidate |
| Marker without the invoked binding, or outside-tree launch | Reject missing binding/owner |
| All outer artifacts removed but its command cached in the host | Outside contract; stop/reload before removal, because copied IDs cannot recover the origin |

The [standard design re-review](readiness-review.md#resolution-and-re-review)
found no remaining design blocker. It explicitly checked the new launcher
boundary, shared inspector, failure supervision, unchanged event stdin, MCP
stdout/status and cancellation requirements. The added subprocess cost and
conservative rejection of uninspectable ancestor configs are documented trade-offs.
No independent reviewer was used.

All six implementation tasks remain pending. Task 2.6 adds the combined
copied-ID/missing-outer-marker acceptance fixture with sentinel wrappers;
Tasks 2/5 cover parsing and setup/doctor parity. No Go suite, runtime test or
benchmark was run for this documentation-only revision. Documentation checks
passed: **45 local links/anchors across six documents**, all six pending task
records, 28 sequential subtasks, acyclic dependencies and `git diff --check`.
The checks read repository files only; no setup, hook activation or trust change
was performed.

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

- [ ] Stable owner marker, bound hook/MCP argv and shared config/marker preflight
  before any project wrapper; correct explicit project owner.
- [ ] Root, ordinary subdirectory, move, spaces and sibling-worktree launches.
- [ ] Non-Git command resolution labelled separately from unverified host discovery.
- [ ] Two-owner/copy/missing-owner rejection before policy, evidence or DB effects.
- [ ] Combined copied-ID/missing-outer-marker fixture within one Git repository,
  both configs retained: pre-tool exit 2 with repair stderr, MCP diagnostic
  failure with no protocol stdout, neither wrapper invoked, no policy/state/DB
  access. Repeat with only either outer binding and equivalent escaped strings.
- [ ] Bounded config decoding, invalid/unreadable/symlink input rejection, valid
  unrelated config support and setup/runtime/doctor agreement.
- [ ] Missing/unreadable/vanished-wrapper and outside-tree bootstrap failures:
  exit exactly 2 plus nonempty repair text for PreToolUse.
- [ ] Binary failure/recovery, including a collaborator without capy; exit-0
  structured denials remain distinct from infrastructure failures.
- [ ] Synthetic-credential MCP initialization, tools/list and harmless call from
  root and an ordinary subdirectory; hooks separately work without keys.
- [ ] Launcher preserves event stdin, MCP stdout/status and cancellation/signal
  delivery without leaving a server process behind.
- [ ] Ask denial identifies matched pattern/source, preserves later-deny priority
  and does not turn an unmatched default ask into a denial.
- [ ] Startup/child context budget, omitted limit fields and live no-spill result.
- [ ] Both trust gates, actual hook delivery and parent/child observation lifecycle.
- [ ] Actual mixed-installation startup byte measurements and toolbox follow-up link.
- [ ] Required FTS5/race/artifact checks and implementation review dispositions.
