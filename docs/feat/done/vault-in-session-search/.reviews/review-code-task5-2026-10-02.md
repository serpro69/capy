# Task 5 code review — 2026-10-02

## Review summary (isolated mode)

**Assessment:** APPROVE — all findings below were reproduced or directly
verified, corrected and re-reviewed; no unresolved findings.

**Reviewers:** independent `code-reviewer` agent and PAL codereview
(`gemini-3.1-pro-preview`, two-step external workflow).

**Scope:** Task 5 fuzzy line picker and compatibility with completed Tasks 1–4.
Task 6 final public documentation and complete feature verification remain out
of scope. The initial snapshot covered 10 files and 1,030 changed lines; the
independent reviewer also inspected subsequent corrections and regressions.
Final evidence/status edits are editorial updates by the implementation agent.

**Profile:** Go, activated by the authoritative `.go` extension signal.
Checklists: `solid-checklist.md`, `removal-plan.md`, `security.md`,
`code-style.md`, `error-handling.md`, `security-injection-ref.md`,
`performance.md`, `concurrency.md`. Other profiles were inactive. Plugin root:
`/Users/sergio/.codex/plugins/cache/claude-toolbox/kk/0.22.1`.

## Independent code-reviewer findings

- **P1 — resolved: isolate picker caches from the applied frame.**
  `viewer_find.go`, `applyFindResult`; Go `concurrency.md`; confidence 99%.
  Exact search → hidden result → clear → fuzzy selection mixed a new tool-scope
  corpus with old owner row mappings, producing a reproduced slice-bounds panic.
  Copying invalidated projections too exposed a second reproduced nil-pointer
  panic when the clear was still pending and slash captured the interim frame.
  **Correction:** immutable picker results own their corpus. Acceptance stages
  corpus, projection invalidation and selection together; picker application
  leaves the displayed frame intact. Regressions cover both completed and
  withheld clears, acceptance and a subsequent slash snapshot.

- **P2 — resolved: preserve pending acceptance across resize.**
  `viewer_find.go`, `resizeFind`; Go `concurrency.md`; confidence 99%.
  A width change immediately after Enter used the previous normal/exact view's
  query and operation, losing fuzzy acceptance or restoring the old exact query.
  **Correction:** carry the requested query, anchor, selection and navigation
  operation together. Tests reproduced both starting presentations.

- **P2 — resolved: key routing recognizes pending acceptance.**
  `viewer.go`, `update`; Go `concurrency.md`; confidence 99%.
  Before acceptance applied, n/N treated fuzzy rune spans as exact occurrences,
  while Esc could leave the viewer or detail instead of clearing.
  **Correction:** `pendingFuzzySelection()` informs Esc and `exactFindActive()`.
  Deterministic held-command regressions reproduce and verify both behaviors.

- **P2 — resolved: preserve counters across digit boundaries.**
  `find_picker.go`, `updateFindPicker`/`findPickerView`; generic; confidence 99%.
  Input padding sized for `9/80` truncated the next counter to `10/8`.
  **Correction:** resize the input on selection changes and reuse the shared
  footer layout. The test asserts every complete counter across 80 results.

The P1 coupled-cache publication pattern was indexed as `kk:review-findings`
by this review workflow. No P0 or P3 findings and no removal candidates.
The existing implementation request authorized these corrections.

## External review findings

PAL's native findings were both **LOW**:

- `viewer_find.go`, `sizeFindInput`: use `ansi.StringWidth` instead of byte
  length when reserving cells for the counter.
- `viewer_find.go`, `findViewport`: avoid computing exact highlighting and
  immediately overwriting it with fuzzy highlighting.

**Author context:** both were verified in the source and corrected. The external
review preceded the independent lifecycle corrections and does not certify them.
No findings were corroborated between the two reviewers.

## Verification and limits

The independent reviewer performed static review only, including the final
corrections. It checked Unicode simple folding, NUL and invalid UTF-8 handling,
leftmost alignment, bounded scoring, source spans, control escaping, snippet
bounds, paging, cancellation, current-result acceptance and frame ownership.
The implementation agent ran all reproduction tests and verification commands.

Final full-suite, both-tag TUI/race/vet and quality evidence is recorded in
[tasks.md](../tasks.md#task-5-evidence--2026-10-02). The final integrated latency
gate is recorded in [performance.md](../performance.md#task-5-fuzzy-gate--2026-10-02).
Neither this review nor the early gate certifies Task 6's remaining full-feature
suspension/performance matrix or public documentation.
