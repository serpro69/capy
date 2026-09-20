# In-session vault search — design

> Status: Agreed design; implementation pending; design review pending
> Created: 2026-09-20
> Issue: [#101 — vault: searching within a session](https://github.com/serpro69/capy/issues/101)
> Companions: [Implementation](implementation.md), [Tasks](tasks.md)
> Parent: [Vault](../../done/vault/design.md), [Vault v2](../../done/vault/v2/design.md)
> Code baseline: 0ed4a94 (v0.16.3)

## Problem and user

An individual developer reading an archived Claude Code or Codex session wants to find a remembered passage and continue reading its context. Global FTS search finds sessions and messages; it does not provide precise, repeated navigation inside the already-open transcript. Long tool results make manual scrolling especially cumbersome.

How might we help that reader locate a remembered passage without leaving the viewer?

The user confirmed exact find and a fuzzy line picker, inclusion of collapsed tool-result bodies, separate search scopes for separately opened transcripts, the acceptance targets below, and the temporary plain-text presentation described here. This document specifies proposed behavior, not functionality already implemented.

## Acceptance criteria

1. Every case-sensitive literal occurrence of a supported single-line query in the current transcript's searchable fields is reachable, including overlapping occurrences and text in collapsed bodies. No results cap silently loses occurrences.
2. A fuzzy result identifies one content line and opens the selected passage, including its full tool detail when necessary.
3. Resizing while a match is selected preserves that occurrence, rather than merely its containing message.
4. Search updates meet the agreed 100 ms input-to-applied-result target on a reproducible 10,000-content-line fixture and a documented machine. [Performance verification](implementation.md#performance-verification) defines the measurement; no result is claimed before implementation.
5. Both Claude Code and Codex sessions, and both default and glamour builds, obey the same search contracts.
6. Editing a query cannot trigger copy, rename, restore, resume, raw view, marker opening, or viewer exit. Cancellation restores the previous reading/search state.
7. Typing, navigating matches, and searching collapsed bodies perform no store search, archive write, reindex, or external-process invocation.

## Current system and constraints

The following are observed facts at the code baseline:

| Existing component | Relevant behavior |
| --- | --- |
| [transcript.go](../../../../internal/vault/transcript.go) | ParseTranscript uses the platform decoder and returns TranscriptMessage values. A collapsed result still carries its full Body. SourceLine is a JSONL record anchor and is not unique per display message. |
| [render.go](../../../../internal/vault/tui/render.go) | renderedTranscript records message start rows. rowForLine locates a message, not a character within it. Each rows element must contain exactly one display line. |
| [viewer.go](../../../../internal/vault/tui/viewer.go) | Owns no store. n/N, brackets, and Tab currently navigate markers. Main, subagent, and inline details use mutually exclusive state and a single saved main position. |
| [app.go](../../../../internal/vault/tui/app.go) | Root routing intercepts r/R, e, c, and v before the viewer; viewerStack suspends parent sessions when opening Codex children. |
| [search.go](../../../../internal/vault/tui/search.go) | Global FTS panel, with a 200 ms debounce and its own result messages. It is not the in-session search implementation. |
| [raw.go](../../../../internal/vault/tui/raw.go) | Separate raw archive mode suspends the viewer, uses cancellable commands, and escapes terminal controls. |
| [render_glamour.go](../../../../internal/vault/tui/render_glamour.go) | Optional Markdown transformation; it does not expose source-character-to-row mapping. |
| [ADR-031](../../../adr/031-transcript-model-seam-and-multi-platform-vault.md) | Platform decoders own format; transcript consumers own display policy. This feature consumes existing TranscriptMessage values. |

Keep implementation inside the TUI and its tests, apart from direct-dependency bookkeeping and user documentation. Archive schema, encryption, reader/index versions, parser policy, global FTS, CLI flags, MCP tools, and setup-generated artifacts do not change. Existing FTS-to-view jumps retain their SourceLine/subagent contracts.

No design-profile token matched the issue prose. Generic design guidance was used; implementation must resolve the Go profile against its actual target files. The Go design index has no applicable database, gRPC, or observability requirement for this in-memory viewer change.

## Chosen interaction

### Exact find

- Slash opens a single-line footer editor, prefilling the current exact query if present. Query length is limited to 256 Unicode code points, consistent with the existing search input. Leading/trailing spaces are meaningful; do not trim the query.
- Matching is literal and case-sensitive. No regex, word-token syntax, normalization, or cross-newline matching. Soft display wraps do not introduce searchable newlines.
- Editing previews the first occurrence at or after the saved reading anchor, wrapping to the first occurrence when needed. In an already-plain search view, capture the source position at the viewport top. On first entry from the normal renderer, use the beginning of its top containing message: the current renderer, especially glamour, does not expose a finer source coordinate. This can select an earlier occurrence within that same message. All edits in this editing transaction use the original anchor; previews must not move the search origin.
- Enter commits the latest query and preview. If its computation is pending, record the submit intent for that revision and commit only its result. Another edit cancels that intent. Never accept results for an older displayed query.
- Esc during editing cancels pending work and restores the complete pre-edit query, selected occurrence, display target, position, and rendering mode. A resize during editing restores the saved logical anchor at the new width.
- Empty draft input previews the pre-edit location without matches. Enter commits a clear; Esc restores the pre-edit state.
- A nonempty query with no results displays “no matches”; it does not jump. It may be committed. n/N remain search operations and are no-ops in this state, rather than unexpectedly navigating markers.
- After commit, n/N advance through occurrences in transcript order, wrapping with a visible wrapped indication. Store the selected occurrence index; viewport clamping near the bottom must not cause repeated selection of the same hit.
- Show a one-based occurrence counter and a non-color indication of the active hit. Highlight all visible occurrences, distinguishing the selected occurrence. Overlapping ranges remain distinct navigation results even when their painted spans overlap.
- Manual scrolling retains the selected occurrence. Subsequent n/N advance from that occurrence, a deliberate distinction from the existing viewport-relative marker algorithm.
- With no active exact query, n/N retain their existing marker aliases. Brackets and Tab/Shift+Tab always remain marker navigation outside a query editor.

### Fuzzy line picker

Ctrl+F opens a temporary picker over the current search scope. When an exact search is presenting a hidden result, its original scope remains the scope used by the picker.

- Present a bounded full-screen picker within the viewer area: scope header, input, scrollable result rows, and help/count footer. This is not a persistent docked panel.
- The query is a case-insensitive ordered subsequence, not edit-distance spelling correction, FTS, or the full fzf query language. Spaces are literal subsequence characters. Use the same 256-code-point limit.
- Return one result per matching content line. Repeated identical lines remain separate, addressable results. Sort by descending score, then original corpus order for ties.
- Empty input lists nonempty content lines in transcript order without scoring. A nonempty query with no matches shows an empty state.
- Each row carries role, message ordinal, content-line number, a field label when needed, and a clipped text excerpt. Tool-body results say they open a detail. Numbers refer to content lines, not physical JSONL records.
- Render snippets by terminal-cell width. Center the excerpt on the first match; highlight all matching characters within it and use ellipses when other matched characters lie outside the excerpt. Match against the full line, never the clipped snippet.
- Up/Down and Ctrl+P/Ctrl+N select, PageUp/PageDown page. Keep the selected row visible; do not repeat global search.go's fixed first-page rendering pattern.
- Enter closes the picker and jumps to the first matched character of the selected line, highlighting that line's matched characters. For empty-query browsing, jump to the start of the selected line.
- Enter is disabled until results belong to the current query; it is a no-op on an empty result set. A successful choice replaces the previous exact search with a fuzzy-selection highlight, including in any saved owner frame for a search-selected detail. Clearing/back must not resurrect that old exact query. n/N then have their ordinary marker meaning.
- Esc restores the pre-picker exact query, selection, target, reading position, and presentation. There is no live viewer jump while merely selecting rows in the picker.

### Key precedence and clearing

Ctrl+C remains the application quit key. While an editor/picker owns input, other printable shortcuts are query text. Rename's existing modal routing retains priority when rename is already open; search cannot open concurrently with it.

Outside editing, Esc first clears the active exact query or accepted fuzzy highlight and restores normal rendering at the containing message. A later Esc performs normal detail/session back navigation. q performs normal back navigation immediately; it discards active search in the scope being left. From a temporary search-selected detail, q returns to its owner with that search cleared in one action. From a manually opened detail or child, q restores the parent's separately suspended search. This preserves a direct back key.

A header/footer label explicitly says “find · plain text” while the plain presentation is active. The help line changes with state. At very small sizes, prioritize the query and match counter, then visible content; never exceed the terminal's row/cell budget or split a grapheme to fit. Lack of room may clip content temporarily; enlarging the terminal must recover the selected occurrence.

## Searchable text and identities

Build the corpus from immutable parsed messages, before wrapping or ANSI styling:

- Every TranscriptMessage.Body participates, including full collapsed bodies, reconstructed diffs, system display messages, and subagent launch-label bodies.
- For a collapsed tool result, also include ToolSummary as a distinct field. A non-collapsed Body already contains its summary prefix; do not add a duplicate summary field.
- Do not add role headings, navigation hints, generated line counts, session titles, file metadata, or raw JSON keys as searchable text.
- Apply the existing decoder/display policy. Thinking or metadata omitted by ParseTranscript, shortened generic tool inputs, and original pre-dedup snapshots do not become available through this feature.
- A main session does not include sidecar or child transcript bodies. Opening a sidecar, child, or manually opened inline detail creates its own scope. A tool detail opened by a search hit is a presentation target within the originating search scope, not an automatic scope change.

A content-line identity consists of scope identity, message ordinal, field kind (Body or ToolSummary), and zero-based line ordinal within that field. Match locations add half-open byte ranges in the original field. Fuzzy positions are converted to ranges for the matched runes. JSONL SourceLine remains provenance, not the unique search key.

Corpus order is message order, then ToolSummary before Body when both are separate fields, then line order, then occurrence start offset. This also breaks fuzzy score ties. Split fields on LF, preserving empty lines and original byte offsets. An empty line cannot match a nonempty query and is omitted from empty fuzzy browsing. Exact matching advances one decoded rune after each match start to include overlaps without starting inside a valid UTF-8 encoding. It does not normalize canonically equivalent Unicode strings.

## Plain presentation and precise jumps

Searching source message text while retaining arbitrary glamour layout would require a new source map through Markdown rendering. The user chose temporary plain text instead.

The search renderer bypasses the renderBody build-tag seam; normal renderers remain the exit path. It retains role styling, actionable markers, and per-line diff coloring, while rendering original content text and recording row-to-source spans.

Use a tracked, grapheme-aware wrapper. Prefer whitespace boundaries; retain every whitespace/source span, and hard-wrap an overlong token only between graphemes. Every searchable byte range must resolve to a row, including spaces consumed at a visual break. A selected match spanning rows scrolls its first character into view and paints every visible part. Wrap changes do not change occurrence count or identities.

Escape terminal control characters and invalid UTF-8 for presentation with source-span provenance, using the policy in rawDisplayText as precedent. The escaped expansion maps back to its original bytes; generated escape spelling is not an additional searchable field. Do not feed transcript-provided ANSI sequences into the styling engine. Highlight entire graphemes when a literal or fuzzy byte range intersects one; do not break a combining sequence.

The normal one-line actionable marker can remain structural. In the search presentation, render a subagent label's actual Body as mapped text alongside that marker so even multiline labels remain searchable without violating the one-row-per-element invariant. A hit on a collapsed ToolSummary opens its detail with the full summary rendered as a mapped field above the body; a truncated header is insufficient.

Search caches are keyed by scope and width, independent of query highlighting. Do not rerun the platform decoder or glamour renderer per keystroke. Store immutable base rows and mappings; apply highlights without modifying shared backing arrays. Height-only changes must not rewrap the transcript.

Clearing an accepted search returns to normal rendering at the selected containing message. Exact character fidelity after clearing glamour is deliberately not promised. Cancellation restores the original exact viewport offset if the layout did not change; after resize it restores the appropriate logical anchor (message-level when the original view was glamour).

## Scope, details, and return state

Replace the single saved-main detail assumption with explicit local view frames as part of adding searchable details. Keep the root app's Codex session viewerStack as the outer session stack.

A frame records the target kind, source transcript identity, parsed messages, original message/field identity for an inline detail, position, focused marker, presentation mode, and committed search state. Immutable corpus/render data can be shared; mutable input/result selection state cannot alias a suspended frame.

Manual opening pushes a frame and starts the new transcript/detail's own scope. It must support main → Claude sidecar → inline tool detail → sidecar → main. Copy still uses the displayed original message Body; rename/restore/resume still act on the owning session. Raw view from an inline detail uses that detail's containing transcript, including a Claude sidecar when the tool belongs to that sidecar. This provenance correction is required by the new nested detail path.

Search navigation has different semantics from manual opening:

1. Save the search owner's frame once.
2. A hidden-body hit presents that tool detail at the match while retaining the owner's corpus/query.
3. Moving to another hidden hit replaces this temporary target; moving to a visible hit shows the owner. Neither operation pushes another return frame.
4. Cancelling draft find or the picker removes the preview and restores its transaction snapshot.
5. Clearing an accepted search while showing a hidden detail leaves that detail open in normal rendering. Its next back action returns to the saved owner with that search cleared, preventing a cleared query from reappearing.
6. Starting slash from that search-selected detail continues the existing owner's scope while the exact query is active. After clearing, slash searches the now-open detail itself.

A manual marker open while exact search is active suspends the parent's committed query; returning restores it. A Codex child open similarly suspends the parent at root level and starts fresh in the child. Raw mode temporarily suspends the current search. Cancelling pending work on suspension must not erase committed state. On resume, issue a new execution identity before any recomputation.

Rename is metadata-only and retains search identities. Copy/status changes may alter available height but must not reset the selected occurrence. Opening a different session from list/global search resets all local scopes and pending work.

## Execution and error handling

A query job reads only an immutable scope snapshot and returns a typed result. Bubble Tea Update alone owns model mutation.

Use a per-controller scheduler with one running command and one replaceable latest pending request. An edit invalidates the previous revision and requests cancellation; when the running command completes, discard stale output and start only the latest pending request. Never spawn one uncancelled scan per keystroke. Cancelled old controllers may finish their one in-flight command after a scope switch, but cannot apply it.

Tag work with a root-issued, monotonically increasing execution epoch, query revision, and layout revision when it includes wrapped rows. A query sequence alone is insufficient: it can restart at the same number in a new viewer. Retire completions even when stale, so a cancelled worker cannot leave the scheduler permanently busy. Do not reuse global FTS debounceMsg/searchResultsMsg types or its 200 ms delay.

Check cancellation during corpus/projection construction and between bounded scan batches. Fuzzy's pinned matcher does not accept context; call it over bounded groups of whole content lines and check between groups. Do not split a line into independent fuzzy candidates. A single extreme line may finish its current library call after cancellation; this limitation must be measured, not hidden behind a responsiveness claim for arbitrary-sized input.

Rendering preparation that scales with transcript size also runs outside the UI handler; workers produce immutable prepared data and never mutate the live viewport. Result application and View cost count toward the latency budget. Preserve query text and the previous stable location on computation/projection error, display a visible error, and allow retry/cancel. Cancellation of superseded work is normal control flow, not a user-facing failure.

Release corpus/projection state when its viewer frame is discarded. Do not log, index, persist, or send query text or excerpts anywhere.

## Dependencies

Keep existing module versions. Direct imports may promote currently indirect requirements; no package upgrade is required.

- sahilm/fuzzy v0.1.1 is already in go.mod. Use its unsorted matching entry point, then a strict comparator: score descending, original line order ascending. Its matched indexes are byte offsets, and matching uses Unicode simple folding. Context7 did not cover this package; these details were checked against the [tagged implementation](https://github.com/sahilm/fuzzy/blob/v0.1.1/fuzzy.go). This is subsequence matching, not a promise to reproduce fzf scoring.
- charmbracelet/x/ansi v0.11.6 is already present. Its FirstGraphemeCluster with GraphemeWidth provides the original cluster and terminal-cell width for tracked wrapping. Context7 returned unversioned material; verify against the [tagged source](https://github.com/charmbracelet/x/blob/ansi/v0.11.6/ansi/parser_decode.go) and [width method definitions](https://github.com/charmbracelet/x/blob/ansi/v0.11.6/ansi/method.go), also inspected in the local module cache.
- Continue using the pinned Bubble Tea, bubbles input/viewport, and lipgloss types already used in this package. No external fzf process, new Markdown renderer, or source-map dependency.

Do not use rune-based truncate or whitespace-collapsing oneLine for searchable content/snippet mapping. They are existing presentation helpers with a different contract.

## Alternatives and decision

| Direction | User value | Feasibility | Distinct benefit | Cost |
| --- | --- | --- | --- | --- |
| A: Separate exact find and fuzzy picker — chosen | High | High | In-place exact reading plus ranked fuzzy recall | Two interfaces |
| B: Shared exact/fuzzy picker | High for selecting, weaker for repeated reading | High | One interface | Exact navigation repeatedly enters a picker |
| C: Docked results with preview | High on large terminals | Medium | Simultaneous results/context | Space, focus, and resize complexity |

### Rejected alternatives

- Shared picker: loses uninterrupted exact-match reading.
- Docked panel: reduces transcript space and adds layout machinery unnecessary for this task.
- Search only visible rows: misses collapsed bodies and text outside the viewport.
- Use scoped vault FTS: indexed text is filtered/bounded and token-based; it cannot deliver every literal occurrence or full excluded tool bodies.
- Match styled/wrapped rows: match identities and even results would depend on width and rendering; Markdown punctuation could disappear.
- Preserve rich Markdown during find with approximate jumps: violates exact landing. A complete Markdown source map is too large a change for this feature.
- External fzf: introduces process/terminal lifecycle work for a picker the TUI can own.

## Assumptions

- Parsed TranscriptMessage fields contain the content the reader expects to search; raw-only metadata and omitted decoder content are intentionally outside the agreed scope. Validate fixtures against actual parsed messages.
- The plain search renderer can preserve byte provenance through wrapping/escaping. This is the first implementation risk to prove, especially for repeated text, Unicode, and soft-wrap boundaries.
- Current pinned fuzzy scoring is useful for prose and tool-output lines as well as code-like text. Validate representative ranked examples; deterministic tie handling is part of our adapter.
- A complete update on the defined 10,000-line fixture can meet 100 ms without data-store changes. This remains unmeasured; Task 5 must report evidence or leave the feature incomplete.
- Sharing immutable corpus/render data across a shallow navigation stack keeps memory reasonable. Measure a stress corpus and repeated open/back cycles; do not retain a new full copy per query.

## Not Doing

- Raw JSONL search: separate mode and source semantics, explicitly excluded.
- Recursive subagent/child aggregation: each separately opened transcript has its own scope.
- Regex, multiline queries, typo correction, or full fzf syntax: literal find and subsequence recall are the chosen interactions.
- Persistent query history, telemetry, or new configuration: transient viewer functionality.
- Global FTS/MCP/CLI search changes and reindexing: this is local navigation.
- Rich Markdown preservation during active search: user accepted temporary plain text.
- Recovering content omitted or shortened by existing transcript policy: requires decoder/display-policy work outside this feature.
- General lazy rendering of the normal viewer: an existing separate optimization; search must meet its own bounded workload target.

## Implementation and review status

No production code or tests are changed by this design. All implementation tasks are pending. User-facing behavior has been agreed; independent design review and measured feasibility remain to be completed.

The ordered task slices intentionally expose a partial feature during development: Task 1 covers exact find over main-transcript visible fields, Task 2 adds full collapsed-body coverage, Task 3 adds nested scope preservation, and Task 4 adds fuzzy selection. Issue #101 is not complete until Task 5 verifies all paths. These are scheduled work items, not silently deferred requirements.

Existing stale source-map/return-to-main comments in the affected viewer must be updated with their replacements. Unrelated repository documentation debt and pre-existing unused code are not part of this task.
