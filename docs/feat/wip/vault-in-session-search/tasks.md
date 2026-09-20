# Tasks: In-session vault search

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Issue: [#101](https://github.com/serpro69/capy/issues/101)
> Status: pending
> Created: 2026-09-20
> Design review: Findings verified and corrected in the plan; re-review pending
> Reconciliation: [Consolidated findings](.reviews/consolidated-findings-2026-09-20.md)
> Not Doing: raw JSONL search, recursive child/subagent search, regex/multiline queries, full fzf syntax, persistent history, global FTS/MCP/CLI changes, rich Markdown during search, decoder-policy changes, general lazy rendering

## Task 1: Exact find with mandatory early feasibility evidence

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove source positions and the measured exact path before dependent work
- **Docs:** [Implementation: Task 1](implementation.md#task-1--exact-find-in-the-main-transcript), [Performance protocol](implementation.md#performance-verification)

### Subtasks

- [ ] 1.1 Add find_text.go full corpus identities and overlapping literal matching, including hidden Body computational coverage; temporarily expose only supported visible fields in the viewer → verify: TestFindText covers duplicates, case, spaces, punctuation, overlaps, and full hidden text without FTS bounds.
- [ ] 1.2 Add find_render.go tracked plain wrapping, escaping and source spans; reserve two cells for generated selection brackets and promote pinned x/ansi only if imported → verify: TestFindRender maps Unicode/wrapped hits, and stripped-ANSI output brackets the chosen occurrence on same-row and wrapped cases.
- [ ] 1.3 Add viewer_find.go slash preview/commit/cancel, counter, n/N and Esc clear; wire viewer.go → verify: TestViewerFind and TestViewerFindResize pass under both tags.
- [ ] 1.4 Add latest-pending cancellable commands and root epochs → verify: TestViewerFindAsync covers stale results, cancellation, pending submit and same-session reopen.
- [ ] 1.5 Route local input before app.go actions and bound layout → verify: TestAppFindRouting prevents unintended actions and store searches.
- [ ] 1.6 Create find_bench_test.go's deterministic reference corpus and opt-in harness; record full-corpus computational measurements and implemented exact UI operations in performance.md under both tags → verify: all location checks and every recorded implemented operation pass the 100 ms gate before Task 2 starts.
- [ ] 1.7 Run both focused suites and label the temporary hidden-navigation gap as Task 3 work → verify: normal/global search regressions pass; early evidence explicitly identifies unimplemented operation classes.

## Task 2: Local frames and manual nested return

- **Status:** pending
- **Depends on:** Task 1, including its performance gate
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — settle local state ownership before search-selected details
- **Scope:** Local target migration and required consumer compatibility; root suspension integration is Task 4
- **Docs:** [Implementation: Task 2](implementation.md#task-2--local-frames-and-manual-nested-return)

### Subtasks

- [ ] 2.1 Add the complete frame contract in viewer_targets.go, including provenance, position, presentation and committed search state → verify: TestViewerTargetsClone proves mutable child state does not alias a parent.
- [ ] 2.2 Migrate viewer.go target readers/writers and viewer_find.go scope ownership; remove independently mutable legacy flags/return state → verify: TestViewerTargetsNested and existing viewer/collapse/marker tests cover manual main/sidecar/tool return, resize and global-search entry.
- [ ] 2.3 Adapt app.go/raw.go consumers to derived frame accessors in the same commit → verify: TestViewerTargetsConsumers confirms correct platform, copy source, owning session and sidecar raw bytes, with no shadow-state writes.
- [ ] 2.4 Start each manual detail's own corpus and restore its parent's committed state; migrate private-field test assertions without weakening behavior → verify: TestViewerFindScopes checks isolated counts and selected-occurrence restoration.

## Task 3: Exact find opens collapsed results at the match

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 3](implementation.md#task-3--exact-matches-inside-collapsed-tool-details)

### Subtasks

- [ ] 3.1 Enable full collapsed-body result navigation using Task 1's corpus; remove the temporary eligibility predicate → verify: TestFindTextCollapsed reaches excluded/large outputs, summaries and diffs without duplicate indexing.
- [ ] 3.2 Use Task 2's complete frames to open mapped summary/body passages, including within an opened sidecar → verify: TestViewerFindCollapsed and TestViewerFindDiff land on the precise span.
- [ ] 3.3 Preserve the originating query/corpus while replacing temporary search targets → verify: repeated visible/hidden/hidden/visible navigation wraps without growing the stack.
- [ ] 3.4 Implement cancel/clear/back distinctions and replace invalidated comments → verify: TestViewerFindCollapsedCancelClear never revives a cleared query and both rendering suites pass.

## Task 4: Preserve search through root suspension and actions

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Scope:** Existing frame contract plus root lifecycle/actions; no further frame-model migration
- **Docs:** [Implementation: Task 4](implementation.md#task-4--preserve-search-through-root-suspension-and-actions)

### Subtasks

- [ ] 4.1 Integrate child-session push/pop, cancellation, fresh epochs and stale completion retirement in app.go/viewer_find.go → verify: TestAppFindChildScopes covers nested, missing/failed children and late results.
- [ ] 4.2 Preserve search around raw inspection using Task 2's provenance accessor → verify: TestAppFindRawReturn covers sidecar-owned tools, children, resize and pending work.
- [ ] 4.3 Preserve source position through copy/status and rename; retain q/Esc precedence → verify: TestAppFindActions checks original Body copying, metadata-only updates and action keys outside input.
- [ ] 4.4 Recheck frame lifetime and cross-mode isolation → verify: both TUI suites/race tests pass without query resurrection or stack growth.

## Task 5: Fuzzy line picker with safe matching and a latency gate

- **Status:** pending
- **Depends on:** Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 5](implementation.md#task-5--fuzzy-line-selection), [Matcher contract](design.md#fuzzy-matcher-contract)

### Subtasks

- [ ] 5.1 Add find_fuzzy.go's local whole-line subsequence matcher with ordinary NUL handling, original byte spans, bounded additive scoring and in-line cancellation; do not import sahilm/fuzzy → verify: TestFindFuzzy covers NUL before/after/across hits, every length 1–256, 39–43/64/128/256 boundary cases, Unicode, oracle agreement, strictly increasing spans, correspondence/completeness, score bounds and ties.
- [ ] 5.2 Add find_picker.go scrolling/paging, role/location metadata, full-line snippets and ASCII > selection prefix → verify: TestFindPicker keeps selected results reachable and has a concrete ANSI-free indicator.
- [ ] 5.3 Wire Ctrl+F accept/cancel into viewer_find.go and shared frame navigation → verify: TestAppFindPicker opens ordinary/hidden/nested hits and restores exact state on Esc.
- [ ] 5.4 Add key fixtures and dynamic help, including accepted fuzzy span brackets and n/N marker behavior → verify: routing/no-color tests pass; go.mod/go.sum have no new fuzzy import/version change.
- [ ] 5.5 Test NUL/long-query worker completion, replacement and mid-line cancellation, then extend the reference harness to integrated fuzzy operations → verify: TestViewerFindFuzzyCompletion retires every job and both builds meet 100 ms before final verification.

## Task 6: Complete verification, performance evidence, and documentation

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, including both early gates
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 6](implementation.md#task-6--final-verification-and-documentation), [Performance protocol](implementation.md#performance-verification)

### Subtasks

- [ ] 6.1 Extend the existing latency harness to all hidden-target and suspension paths; record both builds and stress cases in performance.md → verify: all recorded reference operations meet 100 ms, all correctness invariants pass, and comparisons with Tasks 1/5 are explicit.
- [ ] 6.2 Run $kk:test for full suite, race tests, both TUI tags and relevant vet/linkage checks → verify: record commands/outcomes with synthetic fixtures.
- [ ] 6.3 Run make bench-quality and compare with an explicit baseline → verify: no unexplained retrieval regression and correct detached report naming.
- [ ] 6.4 Run $kk:document; update README.md and docs/architecture.md controls, scope, plain presentation and return semantics → verify: manual Claude/Codex walkthrough agrees with app tests.
- [ ] 6.5 Run $kk:review-code with Go language input → verify: findings resolved or durably recorded with concrete next steps.
- [ ] 6.6 Run $kk:review-spec across all feature documents → verify: all acceptance criteria have implementation evidence; no early probe is mislabeled as feature verification.

## Dependency Graph

~~~text
Task 1 (exact feasibility gate)
  → Task 2 (complete local frames)
  → Task 3 (hidden-body search)
  → Task 4 (root suspension/actions)
  → Task 5 (safe fuzzy + feasibility gate)
  → Task 6 (complete verification; depends on all preceding tasks)
~~~

## Evidence and remaining work

The three supplied review rounds consolidate to six unique claims. Each was checked against the current documents/code; both upstream matcher failures were independently reproduced. [The reconciliation](.reviews/consolidated-findings-2026-09-20.md) records qualified verdicts, fixes and standalone prototype evidence.

All feature implementation checkboxes remain open. The early exact/fuzzy latency gates, actual frame migration, no-color rendering, worker integration and full performance target have not run. Re-review the revised plan before implementation. The original review files remain historical evidence; their repeated P1 findings are tracked once in the reconciliation.
