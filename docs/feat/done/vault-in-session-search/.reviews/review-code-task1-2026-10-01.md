# Task 1 isolated code review

Final assessment: APPROVE for Task 1. Tasks 2–6 were explicitly excluded.
The independent code-reviewer inspected 13 files cumulatively, with focused
follow-ups after each fix. It did not run tests; the implementation session
reproduced every reported sequence before fixing it and ran the regressions.

The second reviewer, PAL with gemini-3.1-pro-preview, failed with 503 UNAVAILABLE
after four provider attempts. It produced no independent findings. The isolated
workflow proceeded with the code-reviewer, as its failure policy permits.

## Findings and resolution

| Finding | Severity | Fix and regression |
| --- | --- | --- |
| Cancel after resize jumps to selected hit despite manual scrolling | P2 | Preserve reading anchor independently of selected identity; TestViewerFindCancelRestoresReadingAnchorAfterResize |
| A second resize replaces a pending normal restore or installs old-width rows | P2 | Keep the latest desired presentation until application and invalidate old layouts; TestViewerFindResizeDuringNormalRestore |
| Clearing restores an obsolete marker-focus overlay | P2 | Cache normal focus separately and rebuild its overlay in the worker; TestViewerFindClearMarkerFocus |
| Empty content-line point anchors resolve to the next row | P3 | Resolve empty-line anchors explicitly; TestFindRenderEmptyLineAnchor |
| Marker navigation while a restore runs makes its prepared overlay stale | P2 | Reprepare normal results whose captured focus disagrees with current focus; TestViewerFindMarkerChangeDuringRestore |
| Reopening slash during restoration mislabels old rows with current width | P2 | Snapshot actual prepared width plus outstanding restoration intent; TestViewerFindReopenDuringRestore covers cancelled edits and cleared committed searches |

The reviewer confirmed all six fixes in the final follow-up and reported no
remaining actionable issue. The original cancellation test now checks the saved
reading anchor as well as selected identity: at a narrower width the selected
text can legitimately fall below the restored viewport. No assertions were
removed to conceal a failed behavior.

No P0/P1 systemic findings were identified, so there are no findings to index.
No code removal was proposed. Existing per-task scope exclusions are recorded
in tasks.md and inline at the temporary hidden-body and main-only UI gates.

An additional author-sourced contract check found that bubbles v1.0.0's default
Ctrl+V path invokes external clipboard utilities. Local find now disables that
binding while retaining terminal-delivered paste. The independent reviewer
approved this small follow-up; TestViewerFindInputLimitAndPaste verifies command
suppression, the 256-rune limit and overlapping Unicode results.
