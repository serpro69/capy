# Tasks: readable Codex vault edits

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Research: [research.md](research.md)
> Issue: [#121](https://github.com/serpro69/capy/issues/121)
> Status: done
> Created: 2026-10-03
> Design review: [supplied findings corroborated and addressed](.reviews/review-resolution-2026-10-03.md); all six tasks complete and independently reviewed
> Not Doing: JavaScript interpretation, shell-edit inference, Rust renderer port, rich syntax highlighting, line-number gutter, per-file picker, global FTS changes, export changes, schema/version changes, general cross-platform tool-input UI, live filesystem verification

## Task 1: Freeze synthetic and real-corpus compatibility

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — capture compatibility evidence before production changes
- **Docs:** [Task 1](implementation.md#task-1--capture-compatibility-before-production-changes), [Corpus parity gate](implementation.md#corpus-parity-gate)

### Subtasks

- [x] 1.1 Add neutral wrapped/direct fixtures and freeze current complete scanner/text/Markdown outputs in Codex fixture/consumer tests → verify: they pass before decoder changes.
- [x] 1.2 Add `codex_parity_test.go` with opt-in input/output SHA-256 baselines, reusing baseline I/O helpers → verify: deterministic outputs, mismatch detection, changed-input accounting, and no zero-comparison success.
- [x] 1.3 Capture and recompare the local baseline before production edits; record aggregate evidence in `verification.md` → verify: required real categories covered and every unchanged input matches.

Evidence: [verification](verification.md), [isolated code review](.reviews/task-1-code-review-2026-10-03.md).
Task 1 changed no production code.

## Task 2: Completed paginated updates open as grouped diffs

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Task 2](implementation.md#task-2--completed-paginated-updates-open-as-grouped-diffs)

### Subtasks

- [x] 2.1 Add normalized file-change entries and paginated dispatch without changing `openAsst` → verify: physical anchors, status gating, and metadata are correct.
- [x] 2.2 Convert completed update hunks and compose grouped details in `codex_changes.go`/`transcript_changes.go` → verify: counts, paths, several hunks/files, and unavailable-data handling.
- [x] 2.3 Introduce/reuse the optional heading override in normal/find rendering and use exact design labels → verify: one expandable group has the specified heading in both builds.
- [x] 2.4 Register explicit scanner/export skip cases and compare synthetic plus real-corpus outputs → verify: byte parity and no unknown-kind warnings.

This slice handles completed paginated updates. Task 3 completes other operations
and identity resilience; Tasks 4–5 finish legacy edits and long-input disclosure.

Evidence: [verification](verification.md#task-2--grouped-completed-paginated-updates-2026-10-03),
[isolated code review](.reviews/task-2-code-review-2026-10-03.md).

## Task 3: Complete paginated operations, states, and identity

- **Status:** done
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Task 3](implementation.md#task-3--complete-paginated-operations-and-event-resilience)

### Subtasks

- [x] 3.1 Add add/delete/move conversion and absent/null/real destination handling → verify: empty files, line endings, and malformed destination fixtures.
- [x] 3.2 Complete exact failure/decline/unconfirmed/partial labels and diagnostics → verify: no unconfirmed applied hunks or incomplete aggregate totals.
- [x] 3.3 Implement explicit normalized equality and exact-ID dedup/conflict handling → verify: stdout/stderr/diagnostic-only changes conflict; map order/location changes do not.
- [x] 3.4 Compare corpus digests and inspect real adverse/move cases read-only → verify: correct states with unchanged scanner/export bytes.

Evidence: [verification](verification.md#task-3--paginated-operations-states-and-identity-2026-10-03),
[isolated code review](.reviews/task-3-code-review-2026-10-03.md).
Explicit empty-string update diffs cover pure moves and no-op updates; missing
or null remains unavailable. Mixed-family wire fixtures remain with Task 4's
legacy adapter; the normalized equality is already shared.

## Task 4: Legacy events and direct results reconcile correctly

- **Status:** done
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** Task 5
- **Docs:** [Task 4](implementation.md#task-4--legacy-events-and-direct-results-reconcile-correctly)

### Subtasks

- [x] 4.1 Add legacy status normalization through the shared converter → verify: every legacy status-table row and mixed event families.
- [x] 4.2 Associate exact direct IDs and preserve `ReportedSuccess`/explicit exit evidence in decoding → verify: independent nested IDs and ambiguous/missing/conflicting outcomes.
- [x] 4.3 Replace duplicate direct diff cards and force associated positive output into a compact marker → verify: no inline boilerplate, full body/find/copy access, and no text heuristics.
- [x] 4.4 Retain event-less successful direct fallback and compare real legacy output digests → verify: old direct cases remain valid and observed call→event→output records render correctly.

Evidence: [verification](verification.md#task-4--legacy-events-and-direct-results-2026-10-03),
[isolated code review](.reviews/task-4-code-review-2026-10-03.md).

## Task 5: Long executable inputs stay compact and searchable

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Task 2, Task 3, Task 4
- **Docs:** [Task 5](implementation.md#task-5--long-executable-inputs-stay-compact-and-searchable)

### Subtasks

- [x] 5.1 Preserve `CodeText` in Codex normalization without changing shared input/summary fields → verify: exact input bytes and scanner/export parity.
- [x] 5.2 Keep one assistant body, insert ordered placeholders, and append input/launch markers; reuse `RoleTool` with the heading override → verify: one Codex header and complete detail bodies in both builds.
- [x] 5.3 Apply compact output aliases by response call ID, independent of edit event IDs → verify: inline/collapsed labels cannot repeat the escaped patch.
- [x] 5.4 Mark the owning body as `SourceAnchor` and honor it in `rowForLine` → verify: an early phrase in a tall body remains visible after a global jump; old unmarked ties are unchanged.
- [x] 5.5 Exercise existing detail/find/copy/raw/child paths with the new bodies and headings → verify: full hidden text, exact/fuzzy navigation, pending resize/cancel, stable return, and no extra corpus/frame ownership.

Task 5 reuses Task 2's heading helper and the existing tool-detail/find frames.
Evidence: [verification](verification.md#task-5--compact-executable-inputs-and-source-landing-2026-10-03),
[isolated code review](.reviews/task-5-code-review-2026-10-03.md). Final feature evidence follows in Task 6.

## Task 6: Final verification, documentation, and review

- **Status:** done
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Task 6](implementation.md#task-6--final-compatibility-documentation-and-reviews), [Verification matrix](implementation.md#verification-matrix)

### Subtasks

- [x] 6.1 Run `$kk:test`, focused/full/race checks, and default/glamour builds with synthetic keys → verify: applicable checks pass and genuine skips are recorded.
- [x] 6.2 Recompare the pre-change corpus baseline and exercise real completed/failed/declined/move/legacy cases → verify: zero unchanged-input digest mismatches and explicit category coverage.
- [x] 6.3 Measure parse/render costs, run the source-owned find latency harness, and compare retrieval-quality benchmarks → verify: reference latency/quality gates hold; extra stress results are separate.
- [x] 6.4 Run `$kk:document` for public viewer guidance, architecture, and comments → verify: exact labels, source landing, keys, scope, and links match both builds.
- [x] 6.5 Run `$kk:review-code` (Go) and `$kk:review-spec`; save evidence locally → verify: findings fixed or durably recorded with concrete next actions; no required work silently deferred.

Evidence: [final verification](verification.md#task-6--final-verification-and-documentation-2026-10-03),
[isolated code review](.reviews/task-6-code-review-2026-10-03.md),
[isolated spec review](.reviews/task-6-spec-review-2026-10-03.md).
All checks passed. The vault race suite completed in 817.57 seconds with a
30-minute timeout after the initial run exceeded Go's 10-minute default.
Corpus parity compared 478 unchanged recordings with zero mismatches; all
86 × 100 find-latency samples per build stayed below 100 ms. Retrieval quality
matches the pre-feature revision. No required behavior or review fix remains.

## Dependency Graph

```text
Task 1 → Task 2 → Task 3 → Task 4 → Task 6
Task 1 → Task 5 ─────────────────→ Task 6
```
