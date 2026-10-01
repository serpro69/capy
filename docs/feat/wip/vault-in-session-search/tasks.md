# Tasks: In-session vault search

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Issue: [#101](https://github.com/serpro69/capy/issues/101)
> Status: in-progress
> Created: 2026-09-20
> Design review: [Re-review passed](.reviews/review-design-2026-10-01.md); Task 1 feasibility gate passed in both builds
> Reconciliation: [Consolidated findings](.reviews/consolidated-findings-2026-09-20.md)
> Not Doing: raw JSONL search, recursive child/subagent search, regex/multiline queries, full fzf syntax, persistent history, global FTS/MCP/CLI changes, rich Markdown during search, decoder-policy changes, general lazy rendering

## Task 1: Exact find with mandatory early feasibility evidence

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove source positions and the measured exact path before dependent work
- **Docs:** [Implementation: Task 1](implementation.md#task-1--exact-find-in-the-main-transcript), [Performance protocol](implementation.md#performance-verification)

### Subtasks

- [x] 1.1 Add find_text.go full corpus identities and overlapping literal matching, including hidden Body computational coverage; temporarily expose only supported visible fields in the viewer → verify: TestFindText covers duplicates, case, spaces, punctuation, overlaps, and full hidden text without FTS bounds.
- [x] 1.2 Add find_render.go tracked plain wrapping, escaping and source spans; reserve two cells for generated selection brackets and promote pinned x/ansi only if imported → verify: TestFindRender maps Unicode/wrapped hits, and stripped-ANSI output brackets the chosen occurrence on same-row and wrapped cases.
- [x] 1.3 Add viewer_find.go slash preview/commit/cancel, counter, n/N and Esc clear; wire viewer.go → verify: TestViewerFind and TestViewerFindResize pass under both tags.
- [x] 1.4 Add latest-pending cancellable commands and root epochs → verify: TestViewerFindAsync covers stale results, cancellation, pending submit and same-session reopen.
- [x] 1.5 Route local input before app.go actions and bound layout → verify: TestAppFindRouting prevents unintended actions and store searches.
- [x] 1.6 Create find_bench_test.go's deterministic reference corpus and opt-in harness; record full-corpus computational measurements and implemented exact UI operations in performance.md under both tags → verify: all location checks and every recorded implemented operation pass the 100 ms gate before Task 2 starts.
- [x] 1.7 Run both focused suites and label the temporary hidden-navigation gap as Task 3 work → verify: normal/global search regressions pass; early evidence explicitly identifies unimplemented operation classes.

### Task 1 evidence — 2026-10-01

- [Performance report](performance.md): 100 samples per operation in both builds,
  full hidden-field computational scans, Unicode/long-line stress and retained
  heap measurements. Final maximum 27.052 ms against the 100 ms gate.
- [Isolated code review](.reviews/review-code-task1-2026-10-01.md): approved after
  six reproduced restoration/mapping findings were fixed. PAL's external reviewer
  returned 503; the independent code-reviewer completed the review and follow-ups.
- Both complete TUI suites passed with -count=1 under fts5 and fts5,glamour;
  final race runs passed under both tags (2.475 s / 2.752 s). They include existing
  global-search, viewer, collapse, marker, raw, child and metadata regressions.
- make test passed with synthetic keys and isolated XDG_CONFIG_HOME; make vet
  and glamour TUI vet passed. Both binaries built and native nm confirmed
  default excludes glamour while the tagged build includes it. This Go
  installation lacks go tool nm, so the system symbol tool was used.
- BenchmarkFind ran six repetitions under both tags. make bench-quality and
  qualstat comparison against an independently archived f99d758 baseline showed
  no retrieval-quality or context-reduction change.
- No schema, decoder, persisted state, CLI flags, setup artifacts or CI pipeline
  changes. x/ansi v0.11.6 was promoted to a direct requirement without a version
  or go.sum change. All new behavior is confined to the TUI.

The query editor also consumes Ctrl+G before the root project editor and disables
the widget's external clipboard utility binding. Terminal bracketed paste remains
supported and is covered by the Unicode 256-code-point limit regression.

The next task is Task 2. Slash currently searches main-transcript ordinary bodies,
launch labels and full summaries. Manual detail scopes/parent-query suspension
remain Task 2; hidden Body navigation remains Task 3 despite full computational
coverage. Root/raw suspension remains Task 4. Fuzzy and final public documentation
remain Tasks 5/6. The inline eligibility and main-only comments name these tasks.

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

Task 1 and its exact feasibility gate are complete. Tasks 2–6 remain pending;
the measured early path does not certify their interactions. The original review
files remain historical evidence; their repeated P1 findings are tracked once in
the reconciliation.

Pre-existing test-environment follow-up: some CLI import/merge fixtures inherit
the user's vault.min_session_bytes policy (192 KiB on this machine), excluding
their tiny archives. The full suite passed with XDG_CONFIG_HOME pointed at an
empty temporary directory. This TUI task leaves unrelated CLI fixtures unchanged;
isolate XDG_CONFIG_HOME in setupCodexVaultEnv and the remaining custom
import/restore/merge fixtures in a separate test-hermeticity fix. Local TCP/Unix
listener tests also require socket access beyond the restricted sandbox; the
successful full run used that access. No production failure was hidden by
changing assertions or skipping tests.
