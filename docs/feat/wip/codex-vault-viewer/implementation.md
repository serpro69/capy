# Implementation: readable Codex vault edits

> Design: [design.md](design.md)
> Tasks: [tasks.md](tasks.md)
> Status: Tasks 1–3 complete — [compatibility evidence](verification.md); Tasks 4–6 pending
> Review resolution: [supplied findings](.reviews/review-resolution-2026-10-03.md)

## Starting point and task boundaries

Read the [research](research.md) and the design's status/identity rules first.
The pipeline is `DecoderFor` → `Transcript` → `ScanTranscript` (persisted search),
`displayMessages` (text/Markdown), or `transcriptMessages` (interactive viewer).
The TUI consumes `TranscriptMessage`; it never parses Codex wire records.

Two independent user-facing branches follow a compatibility-baseline task:
structured edits (Tasks 2–4) and long executable inputs (Task 5). Tasks 2 and 3
split the former broad paginated task into a working update-only path and its
remaining operations/resilience. Task 5 no longer fragments assistant bodies,
introduces a role, or changes detail-frame/search-corpus ownership.

The primary files under each task name the substantive logic. Small enum/field
registrations, fixture builders, and explicit consumer skip cases are accounted
for separately; they are not hidden unfinished layers. Tasks 2 and 5 share the
specified optional `TranscriptMessage.Heading` and rendering override: whichever
lands first introduces it, and the other reuses it. This is an integration point,
not a dependency on the legacy-edit branch. Coordinate shared-file edits if tasks
run concurrently; no new dependency or library is needed.

## Task 1 — Capture compatibility before production changes

Primary files: new `internal/vault/codex_parity_test.go` and existing
`codex_fixtures_test.go`/`codex_consumers_test.go`. This is an explicit test-only
prerequisite, not a production-layer task.

1. Add neutral synthetic fixtures for the wrapped four-file example, several
   nested events in one exec, and a legacy direct patch. Freeze complete current
   scanner output/metadata and text/Markdown outputs. → verify: all expectations
   pass with the unchanged decoder, including source lines and `ToolNames`.
2. Implement the opt-in Codex digest canary described below, reusing the existing
   baseline reader/writer helpers where compatible. Test its deterministic
   serialization and baseline comparison with synthetic data. Do not include
   viewer output in the digest. → verify: an output change with identical input
   fails; changed/new/removed inputs are separately reported.
3. Capture a machine-local baseline before any production edit, then compare it
   again to prove the harness works. Record the production revision, corpus
   categories, baseline-file digest, and comparison totals in `verification.md`.
   → verify: unchanged recordings compare byte-identically, with the required
   representative categories covered and no unexplained zero-comparison pass.

### Corpus parity gate

Use new `TestCodexParityCanary`, gated by `CAPY_CODEX_PARITY_BASELINE`, with an
absolute baseline path under ignored `bench-results/`. Discover eligible main and
child rollouts under both Codex roots using the same discovery/decompression rules
as `TestCodexCanary`, including `.jsonl.zst` and excluding revert variants. Read
each file once for a run. Reuse `parityEntry`, `readParityBaseline`, and
`writeParityBaseline` from `parity_canary_test.go`; do not reuse its Claude-only
`readerOutputs` set, which also hashes the intentionally changing TUI output.

Task 1 also stores a machine-local `.tsv.json` companion beside the TSV baseline.
It records each input's uncompressed byte length and category membership and is
bound to the TSV's SHA-256. Keep both files: prefix hashing distinguishes true
appends, and captured categories must retain unchanged representatives on later
runs. A missing or invalid companion fails instead of recapturing the baseline.

For each root-relative logical rollout path, store SHA-256 of the uncompressed
input and a combined SHA-256 of three independently named output digests:

- The deterministic serialization of the complete `ScanTranscript`/`ScanSession`
  `ScanOutput`, including every row, metadata field, `ToolNames`, and line index.
- `RenderText(PlatformCodex, raw)` as complete UTF-8 bytes.
- `RenderMarkdown(PlatformCodex, raw)` as complete UTF-8 bytes.

Use unambiguous reader-name/length framing or a fixed serialization of named
digests before combining them. Do not sanitize, truncate, sort scan rows, or omit
metadata for this comparison. A valid baseline is created by the new test while
production code still matches the recorded pre-feature revision; ordinary test
success after decoder changes is not a substitute for that baseline.

After each production task and at final verification, compare against that same
baseline. Identical input with changed output is a failure. Count appended,
new, removed, and vanished recordings separately; never regenerate the baseline
to bless a mismatch. At least one unchanged recording must be compared, and
unchanged representatives must cover completed paginated edits, failed edits,
declined edits, real moves, and legacy direct edits when those categories were
available at capture. If live growth removes category coverage, select another
unchanged representative and report the limitation rather than claiming coverage.
Permission or decompression errors fail the run.

Keep raw data, per-session paths, and the baseline file machine-local. Commit only
aggregate counts, categories, revisions, baseline-file SHA-256, commands, and
comparison results in `verification.md`. CI without a local corpus reports a
skip and still runs synthetic compatibility tests; a local acceptance run with
the known available corpus must exercise it. This is the evidence supporting the
no-reindex claim, alongside unchanged retrieval-quality benchmarks.

## Task 2 — Completed paginated updates open as grouped diffs

Primary files: `transcript_model.go`, new `codex_changes.go`, new
`transcript_changes.go`, and `tui/render.go`. Registrations are the wire adapter
in `codex_types.go`, event dispatch in `codex_decoder.go`, the new-kind branch in
`transcript.go`, and explicit skips in `scanner.go`/`render.go`.

1. Add `EntryFileChange`, completion state, `FileChangeSet`, and per-file normalized
   data. Retain original text and diagnostic reasons for equality, and represent
   unavailable diffs/counts separately from empty files and zero counts.
   → verify: model and entry-description tests distinguish those states.
2. Decode paginated `item_completed/FileChange` at its own physical line without
   changing `openAsst` or fabricating an assistant call. Map explicit status;
   missing/unknown status remains unconfirmed. This task renders valid completed
   update operations; other operations receive labeled unavailable details until
   Task 3. Failed/declined/unconfirmed events have diagnostics, never applied
   hunks. → verify: an update-only wrapped fixture opens correctly, and non-success
   records cannot enter the success path.
3. Convert archived update hunks, validate line counts, and build one grouped
   detail with stable path ordering and archived-CWD-relative headings. Add the
   optional `Heading` override to normal and find-mode rendering if Task 5 has not
   already done so. Use the exact design label table. → verify: updates with
   several files/hunks show complete bodies and correct aggregate counts in both
   builds; malformed hunks produce explicit unavailable data.
4. Keep scanner/export consumers explicitly neutral to the new entry kind and
   preserve old assistant composition. → verify: frozen synthetic outputs and
   the Task 1 real-corpus comparison both remain identical; consumers emit no
   unknown-kind warnings for valid edit entries.

Task 2 is a complete path for completed paginated updates. Full operation support
and event-identity resilience remain Task 3, legacy/direct reconciliation Task 4,
and long-input disclosure Task 5. Do not mark #121 complete at this intermediate
point. The task adds no new frame kind, role, or search matcher.

Implemented and verified; see [Task 2 evidence](verification.md#task-2--grouped-completed-paginated-updates-2026-10-03)
and its [isolated review](.reviews/task-2-code-review-2026-10-03.md). Task 5 reuses
`TranscriptMessage.Heading` and the shared normal/find header helper.

## Task 3 — Complete paginated operations and event resilience

Primary files: `codex_changes.go`, `transcript_changes.go`, and their tests.

1. Add archived-content conversion for adds/deletes and move destinations on
   updates. Treat absent/null `move_path` as no move; preserve nonempty strings;
   diagnose empty/wrong-type destinations. An explicitly empty-string update diff
   has zero changed lines (pure move or no-op); missing/null stays unavailable.
   → verify: add/delete/update/move,
   absent/null/real destinations, empty files, CRLF, and missing final newlines.
2. Finish unavailable/partial group handling and all status/diagnostic views.
   Valid sibling diffs remain available for completed events, but partial totals
   never masquerade as complete totals. → verify: exact label-table assertions
   for failed, declined, unconfirmed, empty, complete, and partial groups.
3. Apply the design's explicit event equality over state, original per-file data,
   stdout, stderr, and location-independent diagnostic reasons. Ignore timestamps,
   physical locations, map order, and event family. → verify: mutations of each
   equality field conflict; different map order/location does not; null and absent
   optional destinations normalize alike. Do not compare only rendered diffs.
4. Deduplicate identical nonempty IDs at the first event anchor. Conflicting IDs
   become unconfirmed with source-line diagnostics; empty IDs remain independent.
   → verify: same-path distinct IDs, family-independent normalized equality, conflicting status,
   output-only/diagnostic-only conflicts, and malformed/oversize neighbors.
   Mixed-family wire fixtures join this contract in Task 4, when the legacy
   adapter exists.
5. Rerun synthetic and real-corpus parity, and exercise observed failures,
   decline, and real moves read-only. → verify: no consumer digest changes and
   actual adverse-event labels follow the same rules as the synthetic fixtures.

Implemented and independently reviewed; see [Task 3 evidence](verification.md#task-3--paginated-operations-states-and-identity-2026-10-03)
and its [isolated review](.reviews/task-3-code-review-2026-10-03.md). The real
corpus exposed pure moves and no-op updates with explicitly empty diff strings;
these now retain exact zero counts without weakening malformed-hunk validation.

## Task 4 — Legacy events and direct results reconcile correctly

Primary files: `codex_changes.go`, `codex_decoder.go`, and
`transcript_changes.go`/`transcript.go`; model association/outcome fields are small
registrations in `transcript_model.go`.

1. Add the legacy adapter with presence-aware success/status fields; reuse the
   normalized model/converters regardless of `history_mode`. → verify: every row
   of the design's status table, including contradictory/missing evidence, and
   mixed-family duplicate/conflicting identities using Task 3's shared equality.
2. Link only unambiguous matching direct `apply_patch` IDs through `FileChangeID`.
   Preserve positive structured success as `ReportedSuccess` and retain explicit
   exit-code presence for contradiction handling. Never connect nested event IDs
   to nearby exec IDs. → verify: 1:1 direct correlation, several independent
   nested edits, ambiguous call IDs, absent codes, and explicit outcome conflicts.
3. Prefer structured edits over the old direct-input `Diff`. When the associated
   event is completed and `ReportedSuccess` is true, force the full nonempty
   result into `apply_patch · output`, even below collapse thresholds. Preserve
   all body bytes and local find/copy access. Build one viewer-local map from
   canonical event ID to its final state before composing results; look up
   `FileChangeID` there instead of rescanning entries per result. Other results
   retain current policy.
   → verify: one edit card plus one compact output marker, no inline success
   boilerplate, and reachable diagnostics without output-text heuristics.
4. Preserve the existing successful direct-input fallback when no associated
   usable event exists. Recognized failure/unconfirmed/conflicting identities
   cannot be bypassed by a success-looking input result. → verify: existing
   `TestCodexDecoder_DiffText`/consumer cases and new malformed/event-less cases.
5. Compare unchanged scanner/export digests and inspect legacy recordings
   read-only. → verify: actual call→event→output sequences have the documented
   order/presentation and no persisted-output changes.

## Task 5 — Long executable inputs stay compact and searchable

Primary files: new `transcript_inputs.go`, `transcript.go`, `tui/render.go`, and
`tui/find_render.go`. Registrations add `ToolCall.CodeText` and populate it in the
Codex custom-exec decoder, plus `TranscriptMessage.Heading`/`SourceAnchor` with
empty/false omission. This task depends on the baseline task, not edit events.

1. Preserve exact custom-exec input as `CodeText`; do not change `Input`, `Name`,
   or the shared summary. → verify: quotes, escapes, pragmas, empty/multiline code,
   threshold boundaries, and unchanged synthetic/real scanner/export output.
2. Compose one assistant body with `→ exec · input` at each collapsed call's part
   position. Append input/launch markers after the body in call-part order. Mark
   only this owning body `SourceAnchor=true`. Keep entries without such inputs
   on the current path. Extend/replace `assistantBodyAndLaunches` to return an
   ordered `[]TranscriptMessage` detail list instead of a launch-only slice;
   register sidecar launch indices only for its `RoleSubagent` elements.
   → verify: one Codex header, text→call→text preservation,
   several inputs, mixed launches, and unchanged Claude fixtures. No fragment
   machinery is introduced.
3. Reuse `RoleTool` and `viewerTargetTool` for complete input bodies, with the
   `Tool input` heading and exact `exec · input` summary (no excerpt). Add/reuse
   the heading override in both normal/find rendering. → verify: existing
   Enter/Esc, copy, raw/child return, and Markdown bypass work in both builds;
   no new role, frame owner, or corpus is introduced.
4. Build a viewer-only map from collapsed input call IDs to `exec · output` result
   aliases, covering inline and collapsed outputs. → verify: neither side repeats
   the large escaped script; unidentified results retain their current labels
   and every output body remains complete.
5. Make `rowForLine` prefer an explicit `SourceAnchor` within the latest eligible
   source-line group, preserving its old fallback when no flag is set. Rewrap and
   detail return keep ordinal-based anchors. → verify: a tall assistant body with
   an early search phrase remains visible after a global jump despite trailing
   markers; existing Claude launch-only ties and local restoration are unchanged.
6. Exercise existing exact/fuzzy navigation against hidden input bodies, duplicate
   source lines, pending resize/cancel, and repeated visible/hidden transitions.
   → verify: complete code occurs once in the owner corpus and opening a detail
   neither duplicates it nor grows the frame stack on repeated search navigation.

The shared heading override is introduced once by whichever of Tasks 2 and 5
lands first; the other reuses it. Shared files require coordination/rebase, not a
fabricated semantic dependency. Task 5's source-anchor rule is independent of
legacy result correlation and has its own explicit viewport regression test.

## Verification matrix

New test names/contracts are planned, not claims of implemented tests. Reuse the
existing Codex fixture and TUI helpers. Proposed focused families are
`TestCodexFileChangePaginated`, `TestCodexFileChangeDiff`,
`TestCodexFileChangeIdentity`, `TestCodexFileChangeLegacy`,
`TestCodexFileChangeDirectResult`, `TestTranscriptCodeInput`,
`TestViewerCodeInput`, and `TestViewerCodeInputSourceJump`.

| Contract | Verification |
| --- | --- |
| Issue reproduction | Four-file grouped diff and compact input/result labels with all bodies retained |
| Wire/status coverage | Both event families; completed, failed, declined, missing/unknown, and conflicting evidence |
| Identity/equality | Exact IDs; every compared field; output/diagnostic-only conflicts; excluded location/map-order differences |
| Diff fidelity | All operations, null/absent/real moves, empty/missing content, ranges, final newlines, partial groups |
| Presentation | Exact label/section/heading table; one assistant header; forced collapse of associated positive patch output |
| Search landing | Tall owning body remains in view; trailing markers do not steal its source anchor |
| Compatibility | Frozen fixtures plus complete scanner/text/Markdown corpus digests; Claude goldens |
| Local detail/find | Existing frames, full hidden text, exact/fuzzy navigation, copy/raw/child return, async resize/cancel |
| Resilience | Malformed/oversize records preserve physical lines and produce bounded diagnostics |
| Cost | Same-corpus parse/render comparison and existing reference-workload latency gate |

Use synthetic `CAPY_DB_KEY` and `CAPY_VAULT_KEY`. Focused suites are
`go test -tags fts5 -count=1 ./internal/vault/...` and
`go test -tags fts5,glamour -count=1 ./internal/vault/tui/...`. The opt-in parity
test compares against the existing absolute `CAPY_CODEX_PARITY_BASELINE` path.
An unavailable real corpus is a documented skip, not a parity success claim.

## Task 6 — Final compatibility, documentation, and reviews

1. Run `$kk:test`, focused/full/race checks, the glamour TUI race subset, and both
   builds. → verify: record actual commands/results and genuine skips in
   `verification.md`; do not rerun passing checks without a relevant change.
2. Compare the pre-change Codex baseline again, recording baseline SHA-256,
   revisions, compared/changed/new/removed counts, mismatch count, and category
   coverage. Exercise completed/failed/declined/move and legacy direct recordings
   read-only as well as synthetic cases. → verify: zero output mismatches for
   unchanged inputs and correct actual-viewer behavior for each available category.
3. Compare parse/render cost on the same input and use the source-owned
   [find latency harness](../../../../internal/vault/tui/find_bench_test.go) and
   [lifecycle checks](../../../../internal/vault/tui/find_lifecycle_bench_test.go).
   Run `TestFindLatency` with `CAPY_FIND_BENCH=1`: 100 samples per operation on its
   fixed reference corpus, 100 ms maximum, in both builds. Record the corpus
   digest/environment; report additional large-edit/input stress separately.
   → verify: the existing reference gate holds and no parse/render regression
   remains unexplained. These source links survive moving the earlier feature
   documentation from `wip` to `done`.
   Task 2's external review suggested byte-based JSON-null checks for large
   stdout/stderr and preallocating the update-diff builder. Neither came with a
   measured regression. Profile those paths here; compare valid large hunks and
   malformed inputs rejected early before choosing builder capacity, and adopt
   either optimization only when the measurements support it.
4. Run `make bench-quality` and compare against an identified baseline using
   `make bench-compare`, preserving previous reports. → verify: no quality
   regression and the independent corpus-output parity gate still passes.
5. Use `$kk:document` for `README.md`, `docs/architecture.md`, and affected model
   comments. → verify: grouping, exact states/headings, source landing, output
   collapse, and unchanged export/global-index scope match both builds.
6. Run `$kk:review-code` (Go) and `$kk:review-spec` against these documents.
   → verify: findings fixed or durably recorded with concrete reasons/actions;
   required behavior must be complete before claiming the issue resolved.

No setup artifact is in scope. An unexpected generator/output change requires
the repository's paired-artifact sync checks and an explicit plan adjustment.

## Deferred work

No accepted requirement is deferred beyond these tasks. Intermediate limits are
stated under their task and are removed by dependent tasks before completion.
The design's Not Doing list defines exclusions, not future promises. Record any
new partial implementation with its reason and next action here and in tasks.md.
