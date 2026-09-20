# Tasks: In-session vault search

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Issue: [#101](https://github.com/serpro69/capy/issues/101)
> Status: pending
> Created: 2026-09-20
> Design review: pending — run $kk:review-design vault-in-session-search
> Not Doing: raw JSONL search, recursive child/subagent search, regex/multiline queries, full fzf syntax, persistent history, global FTS/MCP/CLI changes, rich Markdown during search, decoder-policy changes, general lazy rendering

## Task 1: Exact find in the main transcript

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove exact source positions before adding detail transitions
- **Docs:** [Implementation: Task 1](implementation.md#task-1--exact-find-in-the-main-transcript)

### Subtasks

- [ ] 1.1 Add find_text.go corpus identities and overlapping literal matching for initially visible main-transcript fields → verify: TestFindText covers duplicates, case, spaces, punctuation, overlaps, and stable results across widths.
- [ ] 1.2 Add find_render.go tracked plain wrapping, control escaping, source spans, and marker-compatible rows; promote pinned x/ansi if directly imported → verify: TestFindRender maps Unicode/soft-wrap matches to actual visible text.
- [ ] 1.3 Add viewer_find.go slash editor, preview/commit/cancel snapshots, counter, n/N, and Esc clear; wire viewer.go → verify: TestViewerFind and TestViewerFindResize pass in both builds.
- [ ] 1.4 Add cancellable latest-pending query/projection commands with root epochs and stale-result handling → verify: TestViewerFindAsync covers delayed, cancelled, submitted-pending, and reopened-session work.
- [ ] 1.5 Route local input ahead of app.go action keys and bound footer layout → verify: TestAppFindRouting proves shortcut text cannot act on the session or call store.Search.
- [ ] 1.6 Run both focused TUI suites and retain existing global-search/normal-view behavior → verify: [package commands](implementation.md#verification-matrix) pass.
- [ ] 1.7 Record the temporary hidden-body gap at the affected site as scheduled Task 2 work; remove the note when Task 2 lands → verify: no partial coverage is presented as feature completion.

## Task 2: Exact find opens collapsed results at the match

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 2](implementation.md#task-2--exact-matches-inside-collapsed-tool-details)

### Subtasks

- [ ] 2.1 Include full collapsed Body and distinct ToolSummary fields in find_text.go → verify: TestFindTextCollapsed reaches excluded/large tool text and reconstructed diffs.
- [ ] 2.2 Add viewer_targets.go tool-detail identity and source mapping for summary/body; integrate viewer.go/find_render.go → verify: TestViewerFindCollapsed and TestViewerFindDiff land deep inside results.
- [ ] 2.3 Keep the originating corpus/query while replacing temporary search targets → verify: visible/hidden/hidden/visible navigation wraps without growing the return stack.
- [ ] 2.4 Implement cancellation and clear/back behavior in viewer_find.go → verify: TestViewerFindCollapsedCancelClear restores snapshots and never revives a cleared query.
- [ ] 2.5 Update invalidated return-to-main comments and remove Task 1's partial-coverage note → verify: existing collapse, marker, and viewer regressions pass under both tags.

## Task 3: Preserve search through nested views and suspension

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 3](implementation.md#task-3--search-across-nested-viewer-transitions)

### Subtasks

- [ ] 3.1 Replace single-detail state with local frames in viewer_targets.go/viewer.go → verify: TestViewerFindScopes returns through main/sidecar/tool layers with correct search and platform.
- [ ] 3.2 Integrate app.go child-stack push/pop, suspension cancellation, and fresh execution epochs → verify: TestAppFindChildScopes rejects stale results and preserves the parent.
- [ ] 3.3 Keep global FTS jumpTo and manually opened detail scopes correct → verify: existing global-search-to-sidecar tests pass and local result counts exclude other transcripts.
- [ ] 3.4 Adapt raw.go/root raw selection to containing-transcript provenance and preserve suspended search → verify: TestAppFindRawReturn covers sidecar-owned tools, Codex children, and resize.
- [ ] 3.5 Preserve source position across copy/status and rename; implement q/Esc precedence → verify: TestAppFindActions checks original Body copying, metadata-only rename, and correct clear/back transitions.
- [ ] 3.6 Migrate tests inspecting replaced private flags without weakening behavior assertions → verify: both full TUI suites pass.

## Task 4: Fuzzy line picker opens the selected passage

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 4](implementation.md#task-4--fuzzy-line-selection)

### Subtasks

- [ ] 4.1 Add pinned fuzzy matching with explicit score/order sorting and byte-offset conversion in find_text.go → verify: TestFindFuzzy covers Unicode, ties, duplicate lines, punctuation, and full-line matching.
- [ ] 4.2 Add find_picker.go scrolling selection, paging, metadata, excerpts, empty browsing, and matched-character highlighting → verify: TestFindPicker keeps selected results reachable after paging/resizing.
- [ ] 4.3 Wire Ctrl+F accept/cancel into viewer_find.go and shared detail navigation → verify: TestAppFindPicker opens ordinary/hidden/nested hits and restores exact state on Esc.
- [ ] 4.4 Add key fixtures, dynamic help, and any necessary styles; promote pinned fuzzy to a direct requirement → verify: routing/no-color tests pass and module versions remain unchanged.
- [ ] 4.5 Confirm accepted fuzzy selection clears exact n/N navigation → verify: n/N return to marker behavior and Esc clears the fuzzy highlight.

## Task 5: Final verification, performance evidence, and documentation

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation: Task 5](implementation.md#task-5--final-verification-and-documentation), [Performance protocol](implementation.md#performance-verification)

### Subtasks

- [ ] 5.1 Add find_bench_test.go deterministic 10,000-line fixture, component benchmarks, and opt-in latency harness → verify: counts/digest/machine are reported and the complete measured path matches the documented protocol.
- [ ] 5.2 Measure both builds, fix latency failures, and write performance.md with actual results → verify: every recorded reference operation meets 100 ms; larger/long-line stress findings and memory behavior are recorded.
- [ ] 5.3 Run $kk:test with full suite, race tests, both TUI build tags, and relevant vet/linkage checks → verify: record exact commands and outcomes; no real vault fixture dependency.
- [ ] 5.4 Run make bench-quality and compare against an explicit baseline → verify: no unexplained retrieval-quality regressions; detached report naming handled correctly.
- [ ] 5.5 Run $kk:document and update README.md and docs/architecture.md with implemented controls, scopes, plain rendering, and return semantics → verify: manual Claude/Codex walkthrough agrees with app tests.
- [ ] 5.6 Run $kk:review-code with Go language input → verify: findings resolved or durably recorded with concrete next steps.
- [ ] 5.7 Run $kk:review-spec over the complete feature documents → verify: implemented behavior matches all acceptance criteria; status/evidence updated without hiding unfinished requirements.

## Dependency Graph

~~~text
Task 1 → Task 2 → Task 3 → Task 4
   │        │        │        │
   └────────┴────────┴────────┴──→ Task 5
~~~

## Evidence and remaining work

Design agreed in conversation on 2026-09-20. Production implementation, performance measurement, and independent design review have not run. All task checkboxes are intentionally open.

The staged gaps are named in the implementation plan: hidden-body coverage is Task 2, nested/suspended scopes Task 3, and fuzzy selection Task 4. Issue #101 remains incomplete until Task 5 verifies the whole feature. Add any later deferred review findings here with what remains, why, and the concrete next step.
