# Implementation: readable Codex vault edits

> Design: [design.md](design.md)
> Tasks: [tasks.md](tasks.md)
> Status: planned — no implementation or test results claimed

## Starting point

Read the [research](research.md) and the design's status/identity rules before
editing. The relevant pipeline is `DecoderFor` → `Transcript` → three consumers:
`ScanTranscript` for persisted search, `displayMessages` for text/Markdown, and
`transcriptMessages` for the interactive viewer. The TUI renders the last
consumer's `TranscriptMessage` slice; it must not parse Codex wire records.

There are two user-facing slices: displaying structured edits, and disclosing
long executable inputs. The first is split into paginated and legacy/direct-call
tasks so each change remains reviewable. Implement every task as a complete,
testable path; do not finish all model work before integrating the viewer.

Use the repository's existing libraries. The proposed model field/type names
below make contracts explicit; the implementer may refine names consistently
without changing behavior. File assignments include required mechanical
registrations, which do not constitute separate horizontal tasks.

## Task 1 — Paginated edits from archive to expandable diff

Primary files: `internal/vault/transcript_model.go`, new
`internal/vault/codex_changes.go`, new `internal/vault/transcript_changes.go`, and
`internal/vault/transcript.go`. Wire types and decoder/consumer switch cases are
small registrations in `codex_types.go`, `codex_decoder.go`, `scanner.go`, and
`render.go`.

1. Before changing decoding, add a synthetic paginated fixture with a long
   custom `exec`, two nested `FileChange` events with distinct `exec-…` IDs,
   ordinary command output, and the final custom-tool output. Include a smaller
   four-file issue reproduction. Freeze their current scanner output, metadata,
   and text/Markdown output as compatibility expectations. Use neutral paths and
   authored text, never copied private rollouts. → verify: the baseline tests
   pass with the current decoder; expectations include all source-line indices.
2. Extend the model with `EntryFileChange`, `FileChangeSet`, per-file normalized
   data, and completion states. Add the new kind to `EntryKind.String` and
   entry-description test helpers. Preserve absent/invalid data distinctly from
   valid empty content and zero counts. → verify: model/converter tests exercise
   zero-length adds/deletes, unsupported operations, and unknown status.
3. Decode `item_completed/FileChange` through a dedicated raw wire type rather
   than expanding unrelated user/subagent parsing. Store its ID, diagnostics,
   physical line, timestamp, status, and changes; do not close `openAsst` or
   invent a call. Normalize exact duplicate events and conflicting IDs according
   to the design. → verify: `TestCodexFileChangePaginated` checks completed,
   failed, declined, absent/unknown status, empty IDs, repeats, and conflicts.
4. Convert archived content and unified hunks without filesystem reads. Validate
   update hunk counts; preserve rename targets and missing-final-newline
   annotations. Mark unsupported/malformed files as unavailable without dropping
   valid siblings or reporting incomplete totals as complete. → verify:
   `TestCodexFileChangeDiff` covers add/delete/update/move, multiple hunks,
   optional unified-file headers, empty files, CRLF, and malformed hunk lengths.
5. Compose one collapsed `RoleTool` message per canonical event in
   `transcript_changes.go`, called from `transcriptMessages`. A completed group
   gets its full body and `Diff` styling; other states get plain diagnostics.
   Use archived `Meta.CWD` for lexical display paths. The marker is compact and
   the expanded body has stable per-file operation/path/count headers. → verify:
   `TestCodexFileChangeTranscript` asserts grouping, ordering, paths, counts,
   failure labels, and exact event `SourceLine`; a TUI test opens and closes it
   with the existing marker navigation.
6. Add explicit new-kind skip cases to `ScanTranscript` and `displayMessages`.
   Keep assistant composition and all existing call/result text unchanged.
   → verify: the frozen compatibility expectations still pass byte-for-byte;
   the new entries do not trigger unknown-kind consumer warnings.

Task 1 delivers paginated event display. Legacy events and matching direct-call
deduplication are explicitly Task 2 work; the feature is not complete before that
task. Keep that status visible in `tasks.md` rather than treating the first
fixture's success as completion of #121.

## Task 2 — Legacy edits and direct-call reconciliation

Primary files: `codex_changes.go`, `codex_decoder.go`,
`transcript_model.go`, and `transcript.go`/`transcript_changes.go`.

1. Add the legacy patch-end wire adapter with presence-aware `success` and
   `status` fields and reuse Task 1's converter/payload. Keep both adapters active
   regardless of `history_mode`. → verify: `TestCodexFileChangeLegacy` covers the
   entire design status table, including status absent with explicit success,
   missing success, contradictory fields, and shape changes.
2. Reconcile events by non-empty recorded ID once per decode. Normalize identical
   events from either family to the first anchor; conflicting records produce an
   unconfirmed event with source-line diagnostics. Use maps, not repeated scans
   over entries. → verify: `TestCodexFileChangeIdentity` includes mixed families,
   repeated identical payloads, same-path distinct IDs, empty IDs, and conflicting
   bodies/statuses. Similar paths or nearby records never establish a match.
3. Associate an unambiguous direct `apply_patch` response result using the exact
   event ID. Add `EntryToolResult.FileChangeID` without modifying existing result
   text or call summaries. Expose presence-aware direct result exit status for
   reconciliation, using `metadata.exit_code` only when explicitly decoded;
   retain the current fallback success rules for event-less direct calls.
   → verify: `TestCodexFileChangeDirectResult` distinguishes explicit nonzero
   exit, missing exit, unstructured output, and the existing success-prefix
   fallback; a missing code is not treated as an explicit failure.
4. Prefer the structured event over the old call-input diff. A recognized
   failed/unconfirmed event suppresses that operation's successful-input fallback.
   Contradictory explicit result/event outcomes yield an unconfirmed presentation.
   Keep every original result body through the ordinary result policy, with no
   fallback `Diff` on a matched result. Do not add output-text equality or
   suppression heuristics. Ambiguous response-call IDs also disable successful
   fallback diffs and leave their raw diagnostics visible. → verify: tests assert exactly one edit card,
   reachable diagnostics, no fake success, and unchanged scanner/export output.
5. Preserve the old direct-input converter for records with no associated usable
   event. A malformed outer record that the decoder skips cannot claim an ID and
   suppress an otherwise valid result. → verify: existing
   `TestCodexDecoder_DiffText`, direct patch consumer tests, and a mixed
   direct/nested fixture retain their expected behavior and physical anchors.

New event-only entries must not fabricate assistant turns, `ToolNames`, or
searchable file summaries. A scanner difference is a design deviation requiring
an explicit scope decision under ADR-025, not a reason to update parity goldens.

## Task 3 — Expand long executable inputs without repeated summaries

Primary files: `transcript.go`, new `transcript_inputs.go`, `tui/render.go`, and
`tui/viewer.go`. Small registrations add `ToolCall.CodeText` in
`transcript_model.go`, populate it for custom `exec` in `codex_decoder.go`, and
add the input-role label/style in `tui/styles.go` and `tui/find_render.go`.

1. Populate `CodeText` with the original decoded custom-exec input. Retain the
   existing raw `Input`, name, and summary, including the first-line summary.
   → verify: `TestCodexCodeInput` tests exact bytes, empty code, pragmas, quotes,
   and multiline input; scanner/export fixtures remain unchanged.
2. Build a viewer-only call-ID map of inputs exceeding the existing collapse
   thresholds. Split only affected assistant entries into ordered text segments
   and `RoleToolInput` markers. Each fragment uses the original entry's
   `SourceLine`; do not use line number as a unique message identity. Preserve
   the old path for entries without collapsed code. → verify:
   `TestTranscriptCodeInput` covers text→call→text, several calls, mixed launch
   parts, exact threshold boundaries, and byte-identical Claude fixtures.
3. Give input markers a compact `exec · input` summary and their complete code
   body. Give their correlated result a compact `exec · output` display alias
   both when collapsed and when inline. Never overwrite the model's shared
   `CallSummary`; unidentified results retain current labels. → verify: the
   motivating main transcript contains no 4.5 KB summary on either side, while
   input and output detail bodies remain complete.
4. Generalize collapsed local-detail rendering and Enter handling to
   `RoleToolInput`; use the existing `viewerTargetTool` frame. Update normal and
   find-mode structural headers to say "Tool input" where appropriate, and map
   its style to the existing tool palette. Glamour's non-message path should
   already bypass Markdown; pin that behavior. → verify:
   `TestViewerCodeInput` exercises marker navigation, expansion, copy, raw-view
   return, and Enter/Esc at narrow widths in both builds.
5. Integrate complete hidden input bodies with existing exact/fuzzy find. Retain
   message-ordinal identity, parent query state, and the current global-source
   anchor tie rule. No extra search corpus or persisted index is introduced.
   → verify: `TestViewerFindCodeInput` covers hidden hits beyond FTS truncation,
   duplicate `SourceLine` values, pending resize/cancel, repeated hidden/visible
   navigation, and local detail return without stack growth.

The grouping preference changes only Task 1's composition/navigation if revised.
The viewer/export scope preference changes the explicit `displayMessages` policy
and corresponding compatibility expectations; it must be resolved before any
implementation that changes exports.

## Verification matrix

The named new tests are intended test contracts, not claims that they already
exist. Put decoder/converter cases in new `codex_changes_test.go` and fixture
builders in `codex_fixtures_test.go`; reuse the existing consumer and TUI test
helpers instead of constructing a second transcript reader.

| Contract | Verification |
| --- | --- |
| Issue reproduction | Synthetic wrapped call + four-file event: one group, complete hunks, accurate counts, compact input/result labels |
| Both history formats | Same normalized operations from legacy and paginated fixtures; record-shape dispatch independent of metadata |
| Status truthfulness | Completed, failed, declined, missing/unknown, conflicting events, and explicit direct-result contradiction |
| Identity | Nested ID differs from wrapper ID; several patches per exec; exact repeats; direct event/result pair; no proximity/path matching |
| Diff fidelity | Add/delete/update/move, empty content, absent content, invalid ranges, mixed valid/unsupported files, final-newline and CRLF cases |
| Compatibility | Frozen same-input scanner/export output, metadata, `ToolNames`, physical anchors, and existing Claude goldens |
| Detail behavior | Marker focus/open/back, correct input heading, copy, raw and child return, resize, both rendering builds |
| Local search | Full hidden bodies, duplicates sharing source line, exact/fuzzy navigation, async cancellation and pending operations |
| Resilience | Malformed and oversize records retain current skip/line-count behavior; warnings identify shape/source without payload text |
| Cost | Same-corpus parse/render comparison; existing find latency protocol with hidden code and multi-file diffs |

Use synthetic `CAPY_DB_KEY` and `CAPY_VAULT_KEY` for all checks. Focused commands
are `go test -tags fts5 -count=1 ./internal/vault/...` and
`go test -tags fts5,glamour -count=1 ./internal/vault/tui/...`. The former includes
real-corpus canaries when available; an unavailable corpus is a reported skip,
not evidence that private recordings were tested.

## Task 4 — Final compatibility, documentation, and reviews

1. Run the focused suites above, the repository's full test/race checks, and
   the glamour TUI race subset. Confirm default and glamour builds. Do not
   repeatedly rerun passing checks without a relevant code change or concern.
   → verify: record commands, environment, results, and genuine skips in a new
   feature-local `verification.md`.
2. Exercise the motivating locally available archive read-only and record
   aggregate outcomes without copying private content into the repository.
   Compare decoder/viewer timings on the same machine and run the existing
   [find performance protocol](../vault-in-session-search/performance.md) with
   the new hidden-input/diff cases. → verify: correct counts/locations, the
   existing 100 ms reference-workload maximum in both builds, and no unexplained
   parse/render regression; report additional stress workloads separately.
3. Because shared decoder/scanner code is touched, run `make bench-quality` and
   compare with an identified baseline using `make bench-compare`. This is a
   compatibility guard even though the design requires identical search output.
   Preserve existing reports; record the revisions and result paths. → verify:
   no retrieval-quality regression and scanner parity still exact.
4. Use `$kk:document` to update the Session Vault viewer guidance in `README.md`,
   the transcript description in `docs/architecture.md`, and the relevant model
   comments. Document grouped edits, completion states, input expansion, and
   unchanged global search/export scope. → verify: documented keys and examples
   match both builds, and internal links resolve.
5. Run `$kk:test`, `$kk:review-code` (Go), and `$kk:review-spec` against this
   design and implementation plan. → verify: review findings are fixed or
   explicitly recorded with a reason and next action in `tasks.md` or
   `verification.md`; do not claim completion while required work remains.

No setup generator or committed setup artifact is in scope. If implementation
unexpectedly changes one, apply the repository's generator/counterpart sync rule
and extend the plan rather than treating a generated-file edit as standalone.

## Deferred work

No accepted requirement is intentionally deferred beyond these tasks. The
design's Not Doing list defines exclusions, not promises of later implementation.
If a required behavior cannot be completed, record the exact gap, reason, and
next step in this section and the affected task before calling the work partial.
