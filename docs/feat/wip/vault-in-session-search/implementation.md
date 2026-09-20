# In-session vault search — implementation plan

> Status: Pending implementation; design review pending
> Created: 2026-09-20
> Issue: [#101](https://github.com/serpro69/capy/issues/101)
> Design: [design.md](design.md)
> Tasks: [tasks.md](tasks.md)
> Baseline: 0ed4a94

## Read before implementing

Read the [agreed design](design.md), especially its key precedence, scope/return state, and plain-rendering contracts. Search applies to parsed message fields, not physical JSONL lines or FTS hits.

Relevant production files are all under internal/vault/tui unless stated otherwise:

| File | Responsibility and integration point |
| --- | --- |
| viewer.go | Slash/Ctrl+F initiation, Update/View, marker navigation, setSize, copy selection, loadSession and detail transitions |
| app.go | updateView shortcut precedence; Update result delivery; root execution epochs; openChild/popViewer; rename/status layout |
| render.go | Existing renderedTranscript and marker helpers; preserve one-row-per-element and existing global-search anchors |
| raw.go | Suspend/resume search, cancel work, select the archive bytes belonging to the displayed transcript |
| styles.go | Reuse existing styles where sufficient; add active/other-match distinction only if needed |
| internal/vault/transcript.go | Consume TranscriptMessage as-is; no parser or archive policy change |
| search.go | Global FTS model; its message namespace, debounce, and store interface stay separate |

Existing fixtures/helpers: helpers_test.go (sampleSession, codexSession, stubStore and searchCalls), viewer_test.go (keyMsg and loadedViewer), collapse_test.go (tool-result and diff fixtures), children_test.go, raw_test.go, marker_nav_test.go, keybindings_test.go, and render_glamour_test.go.

Do not build a separate parser, change SourceLine semantics, or route local queries through the store. Do not mutate slices shared with a suspended value-model frame.

## Proposed internal organization

These names define useful boundaries, not a requirement to create exported APIs:

- find_text.go: immutable corpus/field lines, logical anchors and exact/fuzzy matching adapters. It consumes message text and context; no viewport, store, or key handling.
- find_render.go: tracked source-to-row rendering, control escaping, Unicode widths, visible match styling, and prepared projection lookup. It does not invoke glamour.
- viewer_find.go: viewer-local editor state, committed selection, transaction snapshots, asynchronous scheduling, result messages, and target navigation.
- viewer_targets.go: local target and frame operations introduced when adding tool details; preserve root session-stack ownership in app.go.
- find_picker.go: fuzzy picker input/selection/window and cell-bounded snippets; consumes immutable line results.

A logical anchor includes target and scope identity, message ordinal, field, and source byte range. The original source JSONL line is supplementary metadata only. A query result is tied to execution epoch and query revision; width-sensitive prepared data is additionally tied to layout revision.

## Task 1 — exact find in the main transcript

**Size:** M. **Depends on:** none. **Strategy:** Risk-First.

Deliver a working slash → edit → Enter → n/N path for main-transcript fields currently shown without opening a tool detail. This bounded first slice proves precise source mapping and keyboard routing; full hidden-body coverage remains explicitly incomplete until Task 2.

Primary files: new find_text.go, find_render.go, viewer_find.go; viewer.go and app.go. Promoting x/ansi in go.mod/go.sum is mechanical dependency bookkeeping, not an upgrade.

1. Add corpus/anchor types and a case-sensitive occurrence scan in find_text.go, retaining field/message identity and overlaps. In this slice, include ordinary Body fields, launch-label Body fields, and collapsed ToolSummary fields that can be represented without body opening; mark hidden Body handling as the Task 2 continuation in the relevant code comment. → verify: TestFindText table cases for overlaps, same SourceLine on different messages, repeated lines, literal metacharacters, empty/space-only queries, case sensitivity, and soft-wrap-independent results.
2. Implement find_render.go's grapheme-aware plain projection with source spans, safe control display, whitespace retention, and one display line per row. Use the verified pinned x/ansi helper; add direct requirement bookkeeping without version churn. For labels/summaries that need extra rows, keep a separate structural marker anchor so marker navigation remains valid. → verify: TestFindRender with combining marks, CJK, emoji, tabs/controls, invalid UTF-8, trailing spaces, long tokens, multiline labels, narrow widths, and hits spanning a visual wrap; check the selected source byte resolves to the row containing its displayed representation.
3. Wire slash editing, exact preview/commit/cancel, counters, overlap navigation, wrap indication, and Esc clear in viewer_find.go and viewer.go. Reuse existing input styles initially. Capture an immutable pre-edit snapshot before any preview; query edits always search from its reading anchor. → verify: TestViewerFind drives repeated Enter/Esc, empty commit, no-match commit, n/N near the clamped final page, manual scroll then n/N, and restoration after cancellation.
4. Add the cancellable, latest-pending scheduler and result messages. Build expensive projections/matches off the Update handler; give each execution a root-issued epoch and retire stale completions without applying them. Enter while a current query is pending must not commit old results. → verify: TestViewerFindAsync delivers results out of order, cancels while running, replaces multiple pending queries, submits while pending, and closes/reopens the same session with reused local query numbers.
5. Route query input before app.updateView's printable shortcuts and preserve root Ctrl+C. Bound input/footer dimensions and keep selected source anchors on resize. Height-only status/layout changes reuse the current wrapping. → verify: TestAppFindRouting types all shortcut letters and asserts unchanged action, renameCalls, raw mode, clipboard output, and stubStore.searchCalls; TestViewerFindResize checks both rendering builds and narrow/short layouts.

Run the focused TUI package checks under both tags after this slice. The normal viewer and global search must still pass. Introduce the reference-corpus generator with the projection tests if useful, so the source-mapping risk receives an early timing check; final evidence belongs to Task 5.

## Task 2 — exact matches inside collapsed tool details

**Size:** M. **Depends on:** Task 1.

Deliver the user path slash → hidden-body occurrence → full detail at that occurrence → n/N across the original main transcript → clear/back.

Primary files: find_text.go, viewer_find.go, viewer.go, new viewer_targets.go, and find_render.go.

1. Add the entire collapsed Body to its owner's corpus and retain ToolSummary as a separate field, without duplicate summary indexing for ordinary tool messages. Never apply FTS exclusion/truncation policy here. → verify: TestFindTextCollapsed includes a Read result, a large Bash/exec_command result, a reconstructed diff, a summary-only match, and a match beyond any FTS head/tail boundary.
2. Introduce a typed tool-detail target carrying its original owner/message identity and platform. The plain detail projection must map the full summary and body separately and open directly on the match, not at the tool header. → verify: TestViewerFindCollapsed lands deep in a multiline result and inside a long wrapped line; TestViewerFindDiff checks exact locations with diff styling.
3. Save the search-owner frame once, and replace the search-selected detail target as matches change. Do not push a frame for every n/N hit. Visible hits restore the owner projection; hidden hits preserve the owner's query/corpus/counter. → verify: cycle through visible → hidden A → hidden B → visible, then repeat; frame depth remains bounded and counters include every occurrence exactly once per cycle.
4. Implement cancel and clear distinctions: draft cancellation restores the complete pre-edit target; clear keeps the selected detail in normal rendering and clears the saved owner's copy of that query. Its next back returns to the owner. → verify: TestViewerFindCollapsedCancelClear covers both a previously normal view and a previously committed exact search, including a resize before return.
5. Replace comments that assume every inline detail returns directly to main, only where these new transitions invalidate them. → verify: existing collapse_test.go, marker_nav_test.go, and viewer_test.go paths pass under both tags without changing expected unrelated behavior.

This slice completes main-transcript exact coverage. Nested sidecar/detail state and suspension paths remain scheduled in Task 3.

## Task 3 — search across nested viewer transitions

**Size:** M. **Depends on:** Task 2.

Deliver search → manual sidecar/tool/child open → search there → return, preserving the parent reading/search state. Also preserve state across raw inspection, copy/status layout, and rename.

Primary files: viewer_targets.go, viewer.go, viewer_find.go, app.go, raw.go.

1. Replace the mutually exclusive single-detail return assumption with typed local frames. Migrate loadSession, openSubagent, openInlineContent, return/back handling, activePlatform, header, currentMessage, and marker opening to the new target model; do not maintain two competing authoritative state representations. → verify: TestViewerFindScopes exercises main → Claude sidecar → inline result → sidecar → main and confirms correct body, title, platform, selected match, and frame depth at every step.
2. Integrate local frames with app.openChild/popViewer's existing outer session stack. Cancel pending work before suspension, preserve committed state, and allocate a fresh epoch on resume. Root result dispatch must discard completions for another mode/scope while still retiring their work. → verify: TestAppFindChildScopes covers two Codex child levels, unarchived children, failed child loads, and a late parent/child result delivered after returning.
3. Keep existing global FTS entry into a sidecar working through jumpTo; once opened, local find searches that sidecar only. An inline detail manually opened from any transcript gets its own search scope. → verify: the global-search-to-subagent app test remains valid; an identical needle in main and sidecar never leaks between their local result counts.
4. Adapt raw.go/root raw selection to the active target's containing transcript. From a tool inside a Claude sidecar, raw view must show that sidecar's bytes, not the main session. Suspend committed search and restore it with exact anchors where the plain presentation permits. → verify: TestAppFindRawReturn checks main, sidecar, nested tool, and Codex child targets, plus resizing while raw is open.
5. Preserve match/target identity through metadata-only rename and status-row changes. Copy uses the original current message Body, not a highlighted snippet. Ensure q exits without the Esc-clear step and does not resurrect a discarded search. → verify: TestAppFindActions covers copy, rename, failed rename, raw shortcut, q/Esc precedence, and normal restore/resume intents outside editing.

Update existing tests that inspect replaced private flags to assert target state and observable behavior; do not weaken their original return or platform assertions.

## Task 4 — fuzzy line selection

**Size:** M. **Depends on:** Task 3.

Deliver Ctrl+F → ranked content lines → selection → precise jump to ordinary or collapsed text, with Esc restoring the original exact search.

Primary files: new find_picker.go, find_text.go, viewer_find.go. Style additions in styles.go and direct dependency bookkeeping are mechanical if existing styles suffice elsewhere.

1. Wrap pinned sahilm/fuzzy's unsorted matching entry point in find_text.go. Match complete content lines in cancellable batches, preserve original line indexes, and sort using a strict score/order comparator. Convert byte offsets to source rune ranges; do not treat them as rune indexes. → verify: TestFindFuzzy covers ASCII/non-ASCII case folding, subsequence gaps, duplicate lines, ties, multiple matched characters, zero results, and punctuation/spaces. Verify no version changes in go.mod/go.sum.
2. Implement the picker with the same input-ownership rules as slash, a result window following the cursor, paging, role/location metadata, counts, and cell-bounded excerpts centered on the first match. Empty input browses nonempty lines. → verify: TestFindPicker selects beyond the first screen, pages both ways, resizes, and highlights multibyte characters without corrupting snippets; every selectable row stays reachable.
3. Use a full pre-picker snapshot. Esc restores the previous exact search and renderer. Enter accepts only current results, clears the old exact query, and uses the shared match-target machinery to show the chosen passage and fuzzy character highlights. → verify: TestAppFindPicker paths for ordinary text, collapsed Body, ToolSummary, nested sidecar result, exact-search cancellation, and empty-input browsing.
4. Add Ctrl+F and appropriate control-key fixtures to keyMsg without changing existing mappings. Update dynamic help for picker, exact, and selected-fuzzy states; accepted fuzzy selection leaves n/N as marker navigation. → verify: TestAppFindRouting and no-color view checks exercise the advertised bindings and state-specific help.

There is no external executable discovery or fallback search mode.

## Task 5 — final verification and documentation

**Size:** M. **Depends on:** Tasks 1–4.

Primary artifacts: find_bench_test.go, feature performance.md created from actual results, README.md, and docs/architecture.md. Record completion/evidence in these feature documents.

1. Complete the [verification matrix](#verification-matrix), including race testing and all acceptance paths on both builds. → verify: the commands below pass and the task record identifies actual runs.
2. Run the [performance protocol](#performance-verification), fix target failures, and record actual workload/machine/results. → verify: performance.md contains measured input-to-applied-result values, with each recorded reference-fixture update at or below 100 ms; no unexplained failing workload is omitted.
3. Run the repository's required quality benchmarks because search code changed. Capture a baseline from 0ed4a94 or a matching existing report; detached output is named HEAD.json and must be renamed before comparison. Keep the actual measurement branch label explicit. → verify: make bench-quality succeeds and qualstat/bench-compare reports no unexplained quality regression.
4. Use $kk:document to update README.md's vault TUI controls and docs/architecture.md's viewer description: scopes, exact/fuzzy semantics, plain rendering, nested return behavior, and no reindex requirement. Do not advertise these before the feature is implemented. → verify: documented keys/scope/clear behavior match app-level tests and a manual TUI walkthrough.
5. Invoke $kk:test for the full suite, $kk:review-code with Go language input, and $kk:review-spec for all three feature documents. Resolve findings or record an actionable remaining item at the affected site and in tasks.md; do not mark the issue complete with an unmet acceptance criterion. → verify: final review/test evidence and remaining work, if any, are durable.

### Verification matrix

| Area | Essential cases |
| --- | --- |
| Literal semantics | Case-sensitive, punctuation literal, spaces preserved, overlapping occurrences, duplicate SourceLine values, multiple matches per line, no implicit normalization |
| Corpus coverage | Ordinary messages, full Read/Bash/exec_command bodies, tool summaries, diffs, launch labels, parsed display-only scope boundaries |
| Location fidelity | First/middle/last character, repeated text, deep message, soft-wrap boundary, last viewport page clamping, no-color selected indicator |
| Unicode and controls | Combining marks, CJK, emoji sequences, invalid UTF-8/control escaping, spaces at wrap, snippet cell limits |
| State | Edit/commit/clear/cancel, pending submit, no-match navigation, accepted fuzzy replaces exact, picker cancellation restores exact |
| Routing | Every printable application shortcut remains typeable; Ctrl+C quits; normal actions still work outside input |
| Scope and lifecycle | Sidecar isolation, nested tool detail, child stack, raw return, renamed metadata, status height changes, repeated open/back memory release |
| Async | Out-of-order revisions, stale epoch after same-session reopen, stale width, one running plus latest pending, no busy-state deadlock, cancelled/suspended completions |
| Rendering | Default/glamour parity while searching, normal glamour restored after clear, one row per element, source position stable on resize |
| Read-only behavior | Local find leaves stubStore.searchCalls and mutation counters unchanged; archived bytes remain identical |

Use the repository's test environment for all commands:

~~~sh
export CAPY_DB_KEY=test-key-for-development
export CAPY_VAULT_KEY=test-key
export CGO_ENABLED=1
go test -tags fts5 -count=1 ./internal/vault/tui/...
go test -tags fts5,glamour -count=1 ./internal/vault/tui/...
go test -race -tags fts5 -count=1 ./internal/vault/tui/...
go test -race -tags fts5,glamour -count=1 ./internal/vault/tui/...
make test
make vet
make test-race
go vet -tags fts5,glamour ./internal/vault/tui/...
~~~

The existing CI glamour linkage check must remain green: the default binary must not acquire glamour linkage. No new test writes to the real user's vault or depends on a live archived session. Use deterministic model-message delivery for concurrency tests rather than sleep-based timing assertions.

### Performance verification

Create a deterministic generator in find_bench_test.go with exactly 10,000 searchable content lines (ordinary Body, ToolSummary and hidden Body fields combined), approximately 1 MiB of UTF-8 text, and a recorded seed. Include repeated needles, no-match queries, Unicode, Markdown, reconstructed diffs, 100 collapsed bodies, and several long lines; compute and report exact line/byte/message counts rather than assuming them. Include exact and fuzzy query sequences of 1, 8, 32 and 256 code points; seed at least one matching long-query case.

Use a documented idle reference machine, recording CPU, OS, Go version, revision, build tags, terminal width/height (100×30), fixture seed/digest, and counts. Warm the loaded normal viewer before each sequence; archive load/decoding is outside the search-update measure because the feature starts in an already-open session.

Measure opening slash/picker to first usable presentation, individual edits, Enter, n/N, hidden-target changes, and selected-match resize. Include scheduling, corpus construction on first activation, matching, sorting, projection preparation, result application, and producing View output. Terminal emulator painting is outside the in-process measure. Do not report only the pure matcher and call it end-to-end.

Run 100 completed samples per operation class, report median, p95, maximum, and allocated bytes. Every recorded reference-fixture operation must meet 100 ms; a failure requires optimization or an explicit design revision with the user, not silently substituting a percentile target. Rapid-typing runs measure final-keystroke-to-current-result and verify obsolete queries never render; cancelled revisions have no completion-latency claim.

Add conventional Go benchmarks for corpus building, exact scan, fuzzy matching/sorting, projection wrapping, and visible highlight rendering to locate costs. Add an opt-in CAPY_FIND_BENCH=1 latency test/harness for the above acceptance protocol; ordinary tests verify behavior without machine-speed assertions.

~~~sh
go test -tags fts5 -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
go test -tags fts5,glamour -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5 -run '^TestFindLatency$' -count=1 -v ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5,glamour -run '^TestFindLatency$' -count=1 -v ./internal/vault/tui/
~~~

Also measure 100,000 lines, a single 1 MiB content line, long grapheme sequences, rapid query replacement, and repeated nested open/back. These stress cases assess cancellation and memory; the agreed 100 ms gate applies to the defined 10,000-line workload, not arbitrary input size. Record the pinned fuzzy library's non-preemptible single-call behavior if an extreme line delays replacement.

Performance evidence is not yet available. Do not create a report with fabricated numbers or mark this gate complete based on library README timings.

## Assumptions and bounded follow-ups

The design's assumptions remain implementation checks. If source mapping is not exact or the target workload misses 100 ms, fix the implementation or return with measured evidence; do not substitute message-start jumps, result caps, FTS matching, or approximate highlighting.

Work scheduled between slices is explicit: hidden bodies in Task 2, nested/suspended scopes in Task 3, fuzzy selection in Task 4, and measured acceptance/reviews in Task 5. There are no additional agreed requirements deferred beyond this feature.

The no-rich-Markdown choice, raw-search exclusion, and recursive-search exclusion are scope decisions, not backlog promises. Existing general lazy-viewer work remains separate.

## Coordination and reversibility

All implementation tasks touch shared viewer/search state and are deliberately sequential. This is a task dependency decision, not an instruction to spawn parallel agents.

The unrelated vault-project-names WIP may also modify app.go/viewer metadata paths. Preserve any concurrent work and adapt metadata-only refresh through the existing seam; this design does not depend on project-name implementation.

There is no data migration or persisted search state. Reverting this feature restores previous viewer behavior without archive conversion. Keep normal renderer/global-search tests as compatibility guards. The precise n/N and Esc changes apply only while local search state is active.

Before implementation, run $kk:review-design vault-in-session-search over design.md, implementation.md, and tasks.md. The present design work does not claim that gate has passed.
