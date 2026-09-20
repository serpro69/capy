# In-session vault search — implementation plan

> Status: Pending implementation; review corrections documented; re-review pending
> Created: 2026-09-20
> Issue: [#101](https://github.com/serpro69/capy/issues/101)
> Design: [design.md](design.md)
> Tasks: [tasks.md](tasks.md)
> Baseline: 0ed4a94
> Findings: [Consolidated verification](.reviews/consolidated-findings-2026-09-20.md)

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

- find_text.go: immutable corpus/field lines, logical anchors and exact matching. It consumes message text and context; no viewport, store, or key handling.
- find_fuzzy.go: independently implemented local subsequence alignment, bounded scoring, cancellation, and source ranges under the design's matcher contract. No sahilm/fuzzy import.
- find_render.go: tracked source-to-row rendering, control escaping, Unicode widths, visible match styling, and prepared projection lookup. It does not invoke glamour.
- viewer_find.go: viewer-local editor state, committed selection, transaction snapshots, asynchronous scheduling, result messages, and target navigation.
- viewer_targets.go: complete local target and frame contract introduced and migrated in Task 2, before search-selected tool details in Task 3; preserve root session-stack ownership in app.go.
- find_picker.go: fuzzy picker input/selection/window and cell-bounded snippets; consumes immutable line results.

A logical anchor includes target and scope identity, message ordinal, field, and source byte range. The original source JSONL line is supplementary metadata only. A query result is tied to execution epoch and query revision; width-sensitive prepared data is additionally tied to layout revision.

## Task 1 — exact find in the main transcript

**Size:** M. **Depends on:** none. **Strategy:** Risk-First.

Deliver a working slash → edit → Enter → n/N path for main-transcript fields currently shown without opening a tool detail. This bounded first slice proves precise source mapping and keyboard routing; full hidden-body coverage remains explicitly incomplete until Task 3.

Primary files: new find_text.go, find_render.go, viewer_find.go; viewer.go and app.go. Promoting x/ansi in go.mod/go.sum is mechanical dependency bookkeeping, not an upgrade.

1. Add corpus/anchor types and a case-sensitive occurrence scan in find_text.go, retaining field/message identity and overlaps. Build all searchable fields now, including full collapsed Body and distinct ToolSummary, so the early computational gate measures the real corpus. For this first UI slice, expose ordinary Body, launch labels and summaries; hidden-body navigation remains explicitly gated until Task 3, with a comment at that temporary eligibility predicate. → verify: TestFindText table cases for overlaps, same SourceLine on different messages, repeated lines, literal metacharacters, empty/space-only queries, case sensitivity, and soft-wrap-independent results.
2. Implement find_render.go's grapheme-aware plain projection with source spans, safe control display, whitespace retention, and one display line per row. Use the verified pinned x/ansi helper; add direct requirement bookkeeping without version churn. For labels/summaries that need extra rows, keep a separate structural marker anchor so marker navigation remains valid. → verify: TestFindRender with combining marks, CJK, emoji, tabs/controls, invalid UTF-8, trailing spaces, long tokens, multiline labels, narrow widths, and hits spanning a visual wrap; check the selected source byte resolves to the row containing its displayed representation.
3. Wire slash editing, exact preview/commit/cancel, counters, overlap navigation, wrap indication, and Esc clear in viewer_find.go and viewer.go. Implement the mandatory selected-span ASCII brackets, reserving two cells before wrapping, independently of optional styles. Capture an immutable pre-edit snapshot before any preview; query edits always search from its reading anchor. → verify: TestViewerFind drives repeated Enter/Esc, empty commit, no-match commit, n/N near the clamped final page, manual scroll then n/N, restoration after cancellation, and exact selected brackets after stripping every ANSI attribute (multiple hits on one row, overlaps, and soft wraps).
4. Add the cancellable, latest-pending scheduler and result messages. Build expensive projections/matches off the Update handler; give each execution a root-issued epoch and retire stale completions without applying them. Enter while a current query is pending must not commit old results. → verify: TestViewerFindAsync delivers results out of order, cancels while running, replaces multiple pending queries, submits while pending, and closes/reopens the same session with reused local query numbers.
5. Route query input before app.updateView's printable shortcuts and preserve root Ctrl+C. Bound input/footer dimensions and keep selected source anchors on resize. Height-only status/layout changes reuse the current wrapping. → verify: TestAppFindRouting types all shortcut letters and asserts unchanged action, renameCalls, raw mode, clipboard output, and stubStore.searchCalls; TestViewerFindResize checks both rendering builds and narrow/short layouts.

6. Create find_bench_test.go's deterministic 10,000-line corpus and opt-in harness now. Build/scan the full corpus, including hidden fields, as a computational check; exercise the implemented visible exact-search path from input through View, including first projection, edits, commit, n/N, and resize. Record both builds in performance.md with the final protocol's fixture/machine metadata and 100 samples per implemented operation. → verify: every recorded implemented operation meets 100 ms and all source-location checks pass before Task 2 starts. Mark hidden-target and fuzzy operations as not implemented, not as passing.

This is a mandatory go/no-go gate. On failure, optimize and remeasure or obtain a concrete design revision from the user; do not begin dependent tasks or drop required results. The normal viewer and global search must also pass the focused suites under both tags. Task 6 repeats the complete feature measurements; it is no longer the first measurement.

## Task 2 — local frames and manual nested return

**Size:** M. **Depends on:** Task 1, including its measurement gate. **Strategy:** Risk-First.

Deliver main → sidecar → manually opened tool result → sidecar → main with correct position, platform and scope. Establish the complete frame representation before search-selected detail navigation uses it.

Primary files: new viewer_targets.go, viewer.go, viewer_find.go. app.go/raw.go reader adaptations and existing tests that inspect old flags are required compatibility changes within this migration; they must not introduce a second state authority.

1. Define the full frame fields from design.md: target kind, source transcript/owning session identity, original message/field provenance, parsed messages, viewport/logical position, marker focus, presentation mode, and committed search state. Define immutable sharing and copied mutable state explicitly. → verify: TestViewerTargetsClone proves query/selection updates in a child cannot mutate a suspended parent.
2. Migrate loadSession, openSubagent, openInlineContent, return/back, activePlatform, header, currentMessage, marker opening, and global-search jumpTo to active-frame/local-stack ownership. Remove the independently mutable legacy detail fields when these replacements land. A derived inDetail-style getter is fine; a synchronized shadow boolean is not. → verify: TestViewerTargetsNested drives the complete manual path, resize and global FTS sidecar opening, plus existing viewer/collapse/marker tests.
3. Update every existing app.go/raw.go read of the replaced fields to a derived target provenance accessor in this same commit. Preserve current child-stack ownership and existing normal actions. Raw selection from a sidecar-owned inline tool must already resolve that sidecar's archive. → verify: TestViewerTargetsConsumers checks header, copy, owning-session actions and correct raw bytes; a source search finds no legacy field writes left behind.
4. Create each manually opened target's own local corpus using Task 1's generic source machinery; suspend and restore its parent's committed search and snapshots. Migrate tests asserting private flags to target or observable assertions without relaxing existing guarantees. → verify: TestViewerFindScopes checks isolated counts and the same selected match after nested return.

The frame shape and ownership are now final for this feature. Task 3 adds the transient search-selected target using these fields; Task 4 adds root lifecycle behavior, not another representation migration.

## Task 3 — exact matches inside collapsed tool details

**Size:** M. **Depends on:** Task 2.

Deliver the user path slash → hidden-body occurrence → full detail at that occurrence → n/N across the original main transcript → clear/back.

Primary files: find_text.go, viewer_find.go, viewer_targets.go, and find_render.go; viewer.go delegates through the migrated frame operations.

1. Enable Task 1's full collapsed-Body corpus fields in the viewer's result navigation and remove the temporary eligibility predicate. Retain separate ToolSummary fields without duplicate summary indexing for ordinary tool messages. Never apply FTS exclusion/truncation policy here. → verify: TestFindTextCollapsed includes a Read result, a large Bash/exec_command result, a reconstructed diff, a summary-only match, and a match beyond any FTS head/tail boundary.
2. Use Task 2's complete tool-detail target and frame contract, carrying its original owner/message identity and platform. Do not add a parallel partial frame or restore legacy state fields. The plain detail projection must map the full summary and body separately and open directly on the match, not at the tool header. → verify: TestViewerFindCollapsed lands deep in a multiline result and inside a long wrapped line; TestViewerFindDiff checks exact locations with diff styling.
3. Save the search-owner frame once, and replace the search-selected detail target as matches change. Do not push a frame for every n/N hit. Visible hits restore the owner projection; hidden hits preserve the owner's query/corpus/counter. → verify: cycle through visible → hidden A → hidden B → visible, then repeat; frame depth remains bounded and counters include every occurrence exactly once per cycle.
4. Implement cancel and clear distinctions: draft cancellation restores the complete pre-edit target; clear keeps the selected detail in normal rendering and clears the saved owner's copy of that query. Its next back returns to the owner. → verify: TestViewerFindCollapsedCancelClear covers both a previously normal view and a previously committed exact search, including a resize before return.
5. Replace comments that assume every inline detail returns directly to main, only where these new transitions invalidate them. → verify: existing collapse_test.go, marker_nav_test.go, and viewer_test.go paths pass under both tags without changing expected unrelated behavior.

This slice completes exact hidden-body coverage using the already-migrated local frames, including a tool inside an opened sidecar. Root suspension and asynchronous integration remain scheduled in Task 4.

## Task 4 — preserve search through root suspension and actions

**Size:** M. **Depends on:** Task 3.

Deliver search → child session or raw inspection → return, preserving the parent reading/search state and rejecting stale work. The local frame migration is already complete; this slice focuses on root lifecycle and action interactions.

Primary files: viewer_find.go, app.go, raw.go. viewer_targets.go accessors may be extended without changing the frame contract.

1. Integrate frames with app.openChild/popViewer's outer session stack. Cancel before suspension, preserve committed state, and allocate a fresh epoch on resume. Retire stale completions even when their output cannot be applied. → verify: TestAppFindChildScopes covers two Codex child levels, missing/failed child loads, and late parent/child results after return.
2. Suspend/resume active search around raw inspection using the provenance accessor already migrated in Task 2. Keep the original selected occurrence after resize when returning to a plain search projection. → verify: TestAppFindRawReturn covers main, sidecar, sidecar-owned tool, Codex child, and pending cancellation.
3. Preserve match identity through metadata-only rename and status-height changes. Copy uses original Body; q exits immediately and Esc clears first. → verify: TestAppFindActions covers copy, successful/failed rename, resize/status changes, q/Esc precedence, and normal restore/resume outside input.
4. Recheck cross-mode isolation and lifetime boundaries without changing the local frame representation. → verify: both TUI suites and race tests show no stale result application, query resurrection, or retained stack growth.

## Task 5 — fuzzy line selection

**Size:** M. **Depends on:** Task 4.

Deliver Ctrl+F → ranked content lines → selection → precise jump to ordinary or collapsed text, with Esc restoring the original exact search.

Primary files: new find_fuzzy.go, new find_picker.go, viewer_find.go. Reuse Task 1's corpus and measured projection; no fuzzy dependency import or module change.

1. Implement the [local matcher contract](design.md#fuzzy-matcher-contract) in find_fuzzy.go: leftmost source-rune subsequence alignment, length-based termination, bounded additive int64 scoring, original byte spans, and in-line cancellation. Do not use sahilm/fuzzy, sanitize away NUL, split/drop lines, match generated escapes, or reduce the 256-rune limit. → verify: TestFindFuzzy compares an independent subsequence oracle on small generated strings, covers NUL before/after/across hits and every query length 1–256 in ASCII and Unicode, and explicitly asserts the 39–43/64/128/256 cases. Assert exact count, increasing non-overlapping decoding spans, character correspondence, complete coverage for identical strings, the documented score bounds/ranking examples, and deterministic ties.
2. Implement the picker with the same input-ownership rules as slash, a result window following the cursor, paging, role/location metadata, counts, and cell-bounded excerpts centered on the first match. Empty input browses nonempty lines. → verify: TestFindPicker selects beyond the first screen, pages both ways, resizes, and highlights multibyte characters without corrupting snippets; every selectable row stays reachable.
3. Use a full pre-picker snapshot. Esc restores the previous exact search and renderer. Enter accepts only current results, clears the old exact query, and uses the shared match-target machinery to show the chosen passage and fuzzy character highlights. → verify: TestAppFindPicker paths for ordinary text, collapsed Body, ToolSummary, nested sidecar result, exact-search cancellation, and empty-input browsing.
4. Add Ctrl+F/control-key fixtures and dynamic help. Implement the ASCII > picker prefix and the accepted-span brackets even with all ANSI removed; individual fuzzy positions still drive styled character highlights. Accepted fuzzy selection leaves n/N as marker navigation. → verify: TestAppFindRouting and no-color tests assert exact visible indicators, not merely a style field.
5. Exercise successful worker completion and immediate follow-up queries on NUL/256-rune inputs; check mid-line cancellation and stale retirement. Extend the existing latency harness to the integrated fuzzy path in both builds. → verify: TestViewerFindFuzzyCompletion never leaves a busy controller, and the full implemented fuzzy operation set meets 100 ms before Task 6. A failure blocks final completion; do not claim matcher microbenchmarks certify the UI.

There is no external executable discovery or fallback search mode.

## Task 6 — final verification and documentation

**Size:** M. **Depends on:** Tasks 1–5, including both early gates.

Primary artifacts: extend the existing find_bench_test.go and feature performance.md from Tasks 1/5; update README.md and docs/architecture.md. Record completion/evidence in these feature documents.

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
| Location fidelity | First/middle/last character, repeated text, deep message, soft-wrap boundary, last viewport page clamping, exact ASCII brackets without ANSI (same-row and wrapped occurrences), picker > prefix |
| Unicode and controls | Combining marks, CJK, emoji sequences, invalid UTF-8/control escaping, spaces at wrap, snippet cell limits |
| Fuzzy correctness | NUL before/after/across hits; all lengths 1–256 including 39–43/64/128/256; strictly increasing original spans; correspondence/completeness; bounded scoring and deterministic ranking; worker completion |
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

Create in Task 1, and reuse in Tasks 5/6, a deterministic generator in find_bench_test.go with exactly 10,000 searchable content lines (ordinary Body, ToolSummary and hidden Body fields combined), approximately 1 MiB of UTF-8 text, and a recorded seed. Include repeated needles, no-match queries, Unicode, Markdown, reconstructed diffs, 100 collapsed bodies, and several long lines; compute and report exact line/byte/message counts rather than assuming them. Include exact and fuzzy query sequences of 1, 8, 32 and 256 code points; seed at least one matching long-query case.

Use a documented idle reference machine, recording CPU, OS, Go version, revision, build tags, terminal width/height (100×30), fixture seed/digest, and counts. Warm the loaded normal viewer before each sequence; archive load/decoding is outside the search-update measure because the feature starts in an already-open session.

At the Task 1 gate, measure every already-implemented exact-search operation below; computationally scan hidden fields without pretending hidden-target navigation exists. At the Task 5 gate, extend to the integrated fuzzy path. Task 6 measures the complete set and compares with the earlier evidence.

Measure opening slash/picker to first usable presentation, individual edits, Enter, n/N, hidden-target changes, and selected-match resize. Include scheduling, corpus construction on first activation, matching, sorting, projection preparation, result application, and producing View output. Terminal emulator painting is outside the in-process measure. Do not report only the pure matcher and call it end-to-end.

Run 100 completed samples per operation class, report median, p95, maximum, and allocated bytes. Every recorded reference-fixture operation must meet 100 ms; a failure requires optimization or an explicit design revision with the user, not silently substituting a percentile target. Rapid-typing runs measure final-keystroke-to-current-result and verify obsolete queries never render; cancelled revisions have no completion-latency claim.

Add conventional Go benchmarks for corpus building, exact scan, fuzzy matching/sorting, projection wrapping, and visible highlight rendering to locate costs. Add an opt-in CAPY_FIND_BENCH=1 latency test/harness for the above acceptance protocol; ordinary tests verify behavior without machine-speed assertions.

~~~sh
go test -tags fts5 -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
go test -tags fts5,glamour -run '^$' -bench '^BenchmarkFind' -benchmem -count=6 ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5 -run '^TestFindLatency$' -count=1 -v ./internal/vault/tui/
CAPY_FIND_BENCH=1 go test -tags fts5,glamour -run '^TestFindLatency$' -count=1 -v ./internal/vault/tui/
~~~

Also measure 100,000 lines, a single 1 MiB content line, long grapheme sequences, rapid query replacement, and repeated nested open/back. These stress cases assess cancellation and memory; the agreed 100 ms gate applies to the defined 10,000-line workload, not arbitrary input size. Verify that the local matcher's in-line cancellation checks retire a long-line scan; record observed cancellation latency and retained memory.

Performance evidence is not yet available. Do not create a report with fabricated numbers or mark this gate complete based on library README timings.

## Assumptions and bounded follow-ups

The design's assumptions remain implementation checks. If source mapping is not exact or the target workload misses 100 ms, fix the implementation or return with measured evidence; do not substitute message-start jumps, result caps, FTS matching, or approximate highlighting.

Work scheduled between slices is explicit: Task 1 exact search and mandatory early evidence; Task 2 complete local frames/manual nested return; Task 3 hidden-body search; Task 4 root suspension/actions; Task 5 fuzzy selection and its latency gate; Task 6 complete acceptance/reviews. There are no additional agreed requirements deferred beyond this feature.

The no-rich-Markdown choice, raw-search exclusion, and recursive-search exclusion are scope decisions, not backlog promises. Existing general lazy-viewer work remains separate.

## Coordination and reversibility

All implementation tasks touch shared viewer/search state and are deliberately sequential. This is a task dependency decision, not an instruction to spawn parallel agents.

The unrelated vault-project-names WIP may also modify app.go/viewer metadata paths. Preserve any concurrent work and adapt metadata-only refresh through the existing seam; this design does not depend on project-name implementation.

There is no data migration or persisted search state. Reverting this feature restores previous viewer behavior without archive conversion. Keep normal renderer/global-search tests as compatibility guards. The precise n/N and Esc changes apply only while local search state is active.

The supplied design reviews have been verified and reconciled in the linked findings report. Re-run $kk:review-design vault-in-session-search over the revised design.md, implementation.md, and tasks.md before implementation. The corrections and standalone matcher prototype do not claim production correctness or feature latency.
