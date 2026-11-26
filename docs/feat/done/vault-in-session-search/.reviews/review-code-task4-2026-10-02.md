# Task 4 code review — 2026-10-02

## Review summary (isolated mode)

**Assessment:** APPROVE — one P2 finding reproduced, fixed and re-reviewed;
no outstanding findings.

**Reviewers:** independent `code-reviewer` agent and PAL codereview
(`gemini-3.1-pro-preview`, two-step external workflow).

**Scope:** Task 4 root/raw suspension and actions, with compatibility for completed
Tasks 1–3. Task 5 fuzzy functionality and Task 6 complete latency/public-documentation
verification remain outside scope. The independent review covered five Go files
and two feature documents (625 changed lines at the review snapshot). Final
status/evidence edits are editorial updates by the implementation agent.

**Profile:** Go, activated by the authoritative `.go` extension signal. Checklists:
`solid-checklist.md`, `removal-plan.md`, `security.md`, `code-style.md`,
`error-handling.md`, `security-injection-ref.md`, `performance.md`,
`concurrency.md`. Other profiles were inactive. No dependency changes.
`TOOLBOX_PLUGIN_ROOT` was unset; the installed skill root was
`/Users/sergio/.codex/plugins/cache/claude-toolbox/kk/0.22.1`.

## Independent code-reviewer finding

**P2 — resolved: pending resize could hide the selected occurrence after raw return.**
Location: `internal/vault/tui/app.go`, `resumeViewer`; Go `concurrency.md`.
Confidence: 99%.

A pending resize updated the viewer dimensions before its prepared rows applied.
Opening raw mode and returning without another resize bypassed the selection
visibility check. Restoration used the previous reading anchor, leaving a
previously visible selected occurrence below the viewport.

The implementation agent reproduced the reviewer's static finding with a
committed search at 100×10, a withheld resize completion for 25×10, and raw
open/return. The selected row was 15 while the viewport ended at 8.

**Correction:** compare `saved.wrapWidth` with the resumed content width as well
as checking requested dimensions. The reviewer inspected the final correction
and `TestAppFindRawReturnPendingResizeKeepsVisibleSelection`, which verifies
the exact landing and rejects the held stale completion after return.

No P0, P1 or P3 findings. No removal candidates. Popped root/local frames release
discarded backing-array entries; leaving the viewer releases its search state.
No findings to index: the resolved issue is P2 and no systemic P0/P1 pattern was
identified. The existing implementation request authorized the correction.

## External review

PAL returned no findings before the pending-resize correction. Its native
conclusion was:

> No defects, security flaws, or anti-patterns were found in this code.

Per the isolated review protocol, this supplied no additional actionable signal.
The independent code-reviewer finding and follow-up review establish the final
assessment; PAL's earlier approval does not certify the corrected diff.

## Verification and limits

The independent reviewer performed static review only, including cancellation,
execution epochs, stale completions, pending clears, reading/selection state,
nested child/raw returns, frame ownership, original Body copying, metadata
updates, action routing and global-search isolation. It searched prior review
findings and Go idioms and applied all eight supplied checklists.

The implementation agent ran complete TUI suites under `fts5` and
`fts5,glamour` after the correction (0.680 s / 0.942 s), followed by complete
TUI race suites (2.999 s / 3.618 s) and glamour vet. The final repository suite
also passed after the correction. Its results and all quality-benchmark evidence are
recorded in [tasks.md](../tasks.md#task-4-evidence--2026-10-02).

Task 5 and Task 6 remain scheduled work. This review does not claim their
fuzzy behavior or complete suspension latency measurements are finished.
