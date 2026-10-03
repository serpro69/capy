# Readable Codex edits in the vault viewer

> Issue: [#121](https://github.com/serpro69/capy/issues/121)
> Status: revised draft — supplied review addressed; implementation has not started
> Created: 2026-10-03
> Implementation: [implementation.md](implementation.md)
> Tasks: [tasks.md](tasks.md)
> Evidence: [research.md](research.md)
> Review resolution: [finding-by-finding verdicts](.reviews/review-resolution-2026-10-03.md)

## Problem and outcome

Help a developer reviewing an archived Codex session understand which edits were
reported as completed, inspect the changes, and continue reading the conversation.
The issue's roughly 4.5 KB JavaScript tool input currently appears as wrapped,
escaped patch text. The next archived record already contains structured diffs
for the same four files, but Capy ignores it.

Success means that this example has one expandable completed-edit summary, with
four readable file sections and accurate aggregate counts (+23 / −14). The long
input and its result label must not repeat the escaped patch across the main
transcript. Every original input remains accessible; failed or uncertain edits
must not appear as confirmed applied changes. Search, back navigation, resize,
and raw-archive access must work from the new detail views.

The user selected design after investigation established the structured-event
approach. This document settles implementation choices for that approach. Two UI
preferences were requested during drafting: grouping and export scope. Pending
different preferences, the draft uses one summary per patch and limits the change
to the interactive viewer. These are draft defaults, not recorded user approvals.

## Scope and constraints

- Read both recorded forms: legacy `event_msg/patch_apply_end` and paginated
  `event_msg/item_completed` whose item is `FileChange`. Detect record shapes;
  do not gate support on a CLI version or `history_mode` value.
- Use existing Go, Bubble Tea, Lip Gloss, and diff rendering. No new dependency,
  external process, network call, or evaluation of archived code is needed.
- Respect [ADR-031](../../../adr/031-transcript-model-seam-and-multi-platform-vault.md):
  decoders normalize formats; consumers choose indexing and display policy.
- Preserve existing `ScanTranscript` output, including metadata, row text,
  `ToolNames`, order, and physical source-line anchors. Preserve `RenderText` and
  `RenderMarkdown` output. Add explicit handling for new model entries to those
  consumers; do not rely on their unknown-kind fallback.
- Preserve Claude decoding and viewer behavior. Only the Codex decoder initially
  supplies the new edit events and executable-input metadata.
- Stored bytes, encryption, compression, schema, reader version, and index version
  do not change. Per [ADR-025](../../../adr/025-vault-index-version-and-reindex.md),
  existing archives gain the viewer behavior when reopened without reimport or
  reindex. This claim is conditional on synthetic parity checks and a before/after
  digest comparison over unchanged real Codex recordings passing. Capture the
  machine-local baseline before changing the decoder; see the implementation
  plan's [corpus parity gate](implementation.md#corpus-parity-gate).

## Alternatives and decision

This is a non-trivial change because event identity, shared consumers, and local
navigation have different contracts. The useful design lenses are adapting the
upstream presentation and testing failure cases before committing to it.

| Direction | User value | Feasibility and cost | Decision |
| --- | --- | --- | --- |
| Normalize structured edit events; reuse Capy's viewer | Recorded status and real diffs, including wrapped calls | Existing renderer and two small wire adapters; requires careful identity handling | Selected |
| Extract patches from JavaScript input | Can preview some event-less inputs | Escapes, variables, multiple calls, and execution outcomes require heuristics | Rejected as the primary path |
| Port the Codex Rust renderer | Rich diff presentation | Ratatui-specific code and styling do not recover the skipped event data | Rejected for this fix |

The first option provides the issue's missing behavior using data already in the
archive. Reuse the existing direct-`apply_patch` converter as a compatibility
fallback; it does not need JavaScript parsing.

## Viewer behavior

### Recorded edit groups

One recorded patch produces one collapsed marker. A successful example reads
`4 files changed (+23 −14)`. Enter opens a single detail containing every file,
sorted by original path for stable ordering. Each section has an operation,
display path, its own counts, and the diff. A rename displays source and
destination explicitly. Esc returns to the same parent position and marker.

Use `Meta.CWD` from the archived session to shorten descendant paths lexically.
Do not use the current process directory, the session's custom project label, or
live filesystem access. Keep paths outside that directory, or with incompatible
path syntax, in their original form. Full paths stay in the normalized model.

Added, deleted, and updated lines retain `+`, `-`, and hunk headers, with the
existing colors. Color is supplementary: the signs and operation labels convey
the change without color. The first version retains existing diff wrapping and
displays recorded hunk line numbers rather than adding a new line-number gutter.
File headers use the existing plain-section convention, not `---`/`+++` lines
that could be mistaken for removed/added content by the simple renderer.

Failed, declined, and unconfirmed events get explicit status markers. Expanding
them shows known affected paths, status explanation, and recorded diagnostics.
Candidate changes on these events are not displayed as applied diffs. A failed
event does not prove that no partial filesystem changes occurred.

### Labels and headings

The table specifies the stored summary and expanded message heading. The existing
marker glyph, non-diff line-count suffix, and Enter hint remain renderer chrome;
their omitted-line count is distinct from the file count in the summary. Use
`file` for one and `files` otherwise. `A`/`R` are exact line counts; `N` is the
number of recorded file entries, including unsupported entries.

| Condition | Summary | Expanded heading (after `▌ `) |
| --- | --- | --- |
| Completed; every file diff/count available; N > 0 | `N files changed (+A −R)` | `File changes · completed` |
| Completed; some file diffs/counts unavailable | `N files changed · diff incomplete` | `File changes · completed` |
| Completed; empty changes map | `Patch completed · no file changes recorded` | `File changes · completed` |
| Completed; changes data cannot be decoded | `Patch completed · diff unavailable` | `File changes · completed` |
| Failed | `Patch failed` | `File changes · failed` |
| Declined | `Patch declined` | `File changes · declined` |
| Missing, unknown, conflicting status/identity | `Patch unconfirmed` | `File changes · unconfirmed` |
| Long executable input | `exec · input` | `Tool input` |
| Correlated executable output | `exec · output` | `Tool result` |
| Associated, positively successful direct patch output | `apply_patch · output` | `Tool result` |

Completed file sections use `*** Add File: {path} (+A −0)`,
`*** Delete File: {path} (+0 −R)`, or
`*** Update File: {path} (+A −R)`. A move uses
`*** Move File: {source} → {destination} (+A −R)`.
For unavailable diffs replace the count suffix with `(diff unavailable)` and
follow it with the reason; an unknown operation uses `*** File: {path}`.
Non-completed groups use the same known-operation/path header with `(reported)`
instead of counts, followed by status explanation and labeled `stdout:`/`stderr:`
sections when present. They never include candidate hunk bodies. Unreadable
changes use an explicit unavailable-data explanation without inventing `N`.

Introduce an optional `TranscriptMessage.Heading` display override, omitted from
JSON when empty. Keep these details as `RoleTool` messages: the existing collapsed
tool path already supplies the appropriate styling, Markdown bypass, search, and
local frames. Normal and find-mode rendering honor the same heading override;
existing messages keep their role-derived heading. The detail frame's title keeps
using the marker summary. No new input role or target kind is needed.

### Long executable inputs

The Codex decoder marks the verbatim executable text of custom `exec` calls in a
new optional, platform-neutral `ToolCall.CodeText` field. Keep `Input`, `Name`,
and `Summary` unchanged; in particular do not shorten `codexCustomCallSummary`,
which is also used by indexing and exports.

The viewer collapses this input when it exceeds the existing 20-line or
2,000-byte tool-body thresholds. The compact label is exactly `exec · input`,
with no first-line excerpt, even for multiline scripts. The body is the complete
decoded JavaScript source, including its original escape sequences.
Opening the input does not execute it or pretend that its patch strings succeeded.
Short calls retain their current presentation.

Keep one assistant display body and one Codex heading per entry. At a collapsed
call's original part position, render the placeholder `→ exec · input`; surrounding
text and other calls retain their order. Append its openable input marker after
the complete body, following the existing launch-marker pattern. When an entry
has both launches and inputs, append their markers in original call-part order.
All markers share the entry's physical `SourceLine`. Entries without long inputs
keep their existing composition and launch-marker behavior.

For a correlated result of a collapsed input, use `exec · output` as its viewer
label, including an inline result's prefix. Its full result body is unchanged.
Otherwise the original long summary would still flood either the marker or the
inline output. Resolve this display alias by the response call's exact ID; it is
unrelated to the independent IDs of nested file-change events.

Use `RoleTool` with the `Tool input` heading override and keep `viewerTargetTool`
and the current frame stack. Tool input bypasses Markdown in both builds, as tool
output already does. The after-body marker alone does not fix source-line ties;
the owning assistant body is the explicit search landing point described below.

## Transcript model and ownership

Add the semantic `EntryFileChange` entry kind, with an optional `FileChangeSet`
payload on `Entry`. This represents a recorded operation independently of any
visible response call. It is not a synthetic assistant call or an `exec` result.

The normalized payload carries:

- The recorded operation ID, completion state (`completed`, `failed`, `declined`,
  or `unconfirmed`), and any format/status diagnostic.
- Ordered `FileChange` records: original path, optional move destination,
  operation kind, and converted diff/counts when the recorded content supports
  them. Keep unsupported file records identifiable rather than omitting them.
- Recorded stdout and stderr, unsanitized and untruncated.

`Entry.LineIndex` and `Timestamp` belong to the event record. Conversion may
produce candidate diffs for any state; the viewer owns whether they are shown as
applied. Missing or malformed diff data has an explicit unavailable state, not
invented zero counts. Unknown wire fields remain compatible with JSON decoding.

Add an optional `FileChangeID` association to a direct `EntryToolResult` when a
canonical structured edit with that exact non-empty ID exists. The association
records provenance; it does not change the raw result body or scanner summary.
No field is persisted in SQLite.

For updates, absent or JSON-null `move_path` normalizes to no destination. A
nonempty string is a real destination; an empty string or another JSON type is
malformed data, not a rename to an empty path. Tests include absent, null, a real
destination, empty, and wrong-type values. Keep the original recorded content or
unified-diff text and structured diagnostic reasons available for event equality;
rendered text/counts alone cannot distinguish all different recordings.

Carry a direct result's positive `structured && success` fact into an optional
`EntryToolResult.ReportedSuccess` boolean (false omitted). This does not equate a
false value with failure. It allows the viewer to collapse an associated success
response even after the structured event has replaced its fallback `Diff`.

The new format work belongs in `codex_changes.go` with wire types alongside the
existing Codex types. `codex_decoder.go` collects and reconciles these entries.
`transcript.go` composes their viewer messages. The scanner and export renderer
explicitly skip `EntryFileChange`; neither consumes `CodeText` or the result's
new association. This keeps the viewer improvement out of persisted search.

Adding an event slot must not change the decoder's `openAsst` state. Closing that
slot early would regroup ordinary tool calls and change assistant FTS rows even
if the scanner skipped the new event itself.

## Status, identity, and fallback rules

### Status normalization

| Wire evidence | Normalized state |
| --- | --- |
| Paginated status `completed`, `failed`, or `declined` | That explicit state |
| Paginated status absent, null, or unknown | Unconfirmed |
| Legacy `success: true` with status `completed`, or with status absent | Completed |
| Legacy `success: false` with status `failed`, or with status absent | Failed |
| Legacy `success: false` with status `declined` | Declined |
| Legacy missing success, unknown status, or conflicting success/status | Unconfirmed |

Use presence-aware fields for legacy success and status. The legacy absent-status
rule supports older recordings using the explicit success boolean. Never infer
success from the name `item_completed`, `Script completed`, an enclosing exec's
exit status, or a success-looking string in JavaScript/output.

### Identity and duplicates

1. Both event adapters populate the recorded operation ID. Identical normalized
   events with the same non-empty ID produce one canonical entry at the first
   event's physical line. Equality compares normalized completion state; sorted
   per-file original paths, operation, optional destination, and verbatim content
   or unified-diff bytes; stdout and stderr; and normalized format/status
   diagnostic reasons (code, field, and offending value where retained).
   Diagnostic source-line annotations are not part of equality. Optional absent
   or null stdout/stderr normalize to empty text; absent/null move destinations
   normalize alike. Do not trim nonempty text or normalize CRLF for equality.
   Timestamp, source line, CLI version, event family, and JSON object member order
   do not participate. Unknown fields not consumed by the adapters are ignored.
   Do not deduplicate by paths, content alone, or proximity.
2. A conflicting duplicate ID becomes one unconfirmed entry at that same anchor,
   with a conflict diagnostic and references to the conflicting source lines.
   It has no applied diff or aggregate counts. Raw view retains every record.
3. Empty IDs remain independent events and do not participate in association.
4. Associate only an unambiguous direct `apply_patch` response call/result whose
   ID equals the event ID. Retain all unmatched events independently. Never
   attach an `exec-…` ID to a surrounding `call_…` ID by location or prefix.
   Duplicated response-call IDs make the association unconfirmed: preserve raw
   results and do not construct successful input-fallback diffs for that ID.
5. A structured event supersedes the direct call-input diff for that operation.
   The matched response result has no fallback `Diff`. When its associated event
   is completed and `ReportedSuccess` is true, force its nonempty body into a
   collapsed `apply_patch · output` marker, even below normal size thresholds.
   The full body remains accessible and searchable; there is one edit card and
   no repeated inline success boilerplate. All other results keep their existing
   plain/size-based policy. A contradictory explicit result exit status makes the
   edit unconfirmed before this decision. Only an explicitly decoded exit code
   establishes that contradiction; `structured && !success` is not proof of
   failure. This policy uses decoded outcome facts, not output-text heuristics.
6. If no usable structured event identifies the direct operation, preserve the
   current successful direct-`apply_patch` fallback through `codexPatchToDiff`.
   A recognized failed/unconfirmed event must never be bypassed by that fallback.

Malformed outer JSON and oversized lines follow existing decoder resilience
rules. A recognized event with unusable changes becomes a diagnostic entry where
its envelope/identity are recoverable. It must not suppress a usable direct
result merely because an unrelated record could not be parsed.

### Diff construction

For add/delete records, use the archived `content`; do not read the working tree.
Preserve empty files and missing final newlines, and derive counts from actual
content lines. For updates, preserve the archived unified hunks and validate
their line prefixes and old/new lengths when counting changes. File headers and
"no newline" annotations do not count as edits. Preserve move destinations.

Keep malformed or unsupported file records in the group as labeled diagnostics.
Valid files may still have individual diffs when the event is completed, but a
partially renderable group must say that some diffs/counts are unavailable and
must not present partial totals as the complete patch's totals. An empty changes
map gets a status/diagnostics view without fabricated file or line counts.

## Navigation and search compatibility

The collapsed patch group is one `RoleTool` detail with a heading override. Its complete display
body, including all file sections, participates in `/` find and the fuzzy picker.
The long input detail also supplies its full body to the existing find corpus.
Do not index the same detail separately when opening it through a search hit.

Preserve the existing `(scope, message ordinal, field, byte offset)` identity.
Bodies and appended markers can share a `SourceLine`; it is not a local-detail
key. Add an optional `TranscriptMessage.SourceAnchor` flag (false omitted) on the
single assistant body owning appended input markers. Within the latest source-line
group at or before the requested global hit, `rowForLine` prefers that marked body;
without an explicit anchor it retains the existing last-message rule. There is
at most one explicit anchor per such group. This protects the new Codex input
case without changing existing Claude launch-only behavior or marker navigation.

Pin a regression with an assistant body taller than the viewport, an early
searchable phrase, and appended input/launch markers: jumping to the assistant
source line must show its heading and early phrase, not scroll to the trailing
markers. Exact character selection still belongs to local find. Rewrap and local
return continue to use message ordinals, so this source-hit preference must not
alter their restoration behavior. Never renumber physical JSONL lines.

All body changes happen before the immutable find corpus is built. Labels and
headers introduced by these views must obey the one-render-row/one-viewport-line
invariant. Tests cover marker focus, Enter/Esc, repeated hidden/visible matches,
resize during pending search, and return through raw and child-session views.
Existing `c` copy semantics apply to the selected message body; input details copy
the original code text and edit details copy their complete displayed diff body.

## Observability and performance

Use the existing `decoderLogger` and `log/slog` convention. Warn for contradictory
identity/status data and malformed recognized changes, with source label when
available, physical line, event category, and reason. Never log patch/code bodies
or diagnostics copied from tools. Ordinary failed/declined operations are viewer
content, not parser warnings. Unknown future values must remain visible as
unconfirmed/unavailable data and contribute a bounded drift diagnostic.

Keep the existing 16 MiB physical-line cap and eager viewer model. Build event
and call maps once, sort each event's file paths once, and scan diff text linearly.
Do not perform a whole-transcript search for every tool result. New network
services, metrics, tracing, and product telemetry are unnecessary for this local
decoder/viewer change; existing tests and profiling/benchmarks cover its costs.

The issue reproduction and synthetic large-edit cases must run in both build
variants. Compare parse/render costs before and after on the same archive, and
rerun the existing in-session find latency checks for hidden input/diff bodies.
Keep that protocol's existing 100 ms maximum-latency gate for its reference
workload in both builds. Additional large-edit stress measurements are reported
separately; do not impose new timing numbers without a measured baseline.

## Assumptions

1. Structured events are available for the motivating nested calls. Confirmed
   for the issue's local recording; it is not a claim about every Codex archive.
   Event-less nested code remains an expandable input without an applied diff.
2. An operation's non-empty recorded ID normally identifies one operation within
   a session. Exact duplicates are supported; conflicts take the unconfirmed
   path rather than silently choosing a successful version.
3. Existing detail frames can serve one grouped diff and one code-input body.
   The current ordinal-based search mapping supports this; navigation tests must
   verify heading overrides, after-body markers, and source-hit preference in
   both builds.
4. Skipping the new semantic entries and retaining existing summaries preserves
   scanner/export output. Freeze baseline outputs before decoder changes and
   compare the same fixture bytes afterward. Also compare SHA-256 digests of
   complete scanner, text, and Markdown outputs for unchanged real Codex inputs;
   a shape-only canary cannot establish byte parity.
5. One group per patch and viewer-only scope are the drafting defaults described
   above. They can be revised during design review without changing the event
   identity or status rules.

## Not Doing

- JavaScript parsing/evaluation or inference of edits from shell commands: the
  archive's recorded operations are the supported evidence.
- Rich syntax highlighting, a line-number gutter, or a Ratatui port: existing
  diff styling is sufficient to address the unreadable escaped patch.
- Per-file picker or independent file-detail navigation: one existing detail
  view keeps the interaction compact.
- Global FTS enrichment, reindex/version changes, or export-format changes:
  this iteration improves the interactive viewer while preserving those outputs.
- General tool-input presentation across all platforms: only recorded executable
  inputs supplied by the Codex decoder opt in initially.
- Verification against current filesystem contents or Git history: an archived
  event describes a historical operation, not the present working tree.

## Rejected alternatives

- Extracting nested string literals as the primary source loses status and
  demands a partial JavaScript interpreter to cover variables and wrappers.
- Assigning nested changes to the nearest `exec` call invents provenance; one
  exec can contain several operations, and the observed identifiers differ.
- Shortening the shared decoder summary would alter indexed text and exports.
- Hiding all wrapper input/output would discard useful code and diagnostics;
  local disclosure keeps both available.
- Splitting assistant text around input markers duplicates headers and complicates
  source hits. A placeholder plus after-body detail follows the existing launch
  pattern; an explicit source anchor also closes that pattern's tie-rule risk.
- Treating edit events as ordinary response tool results would force export/FTS
  behavior to depend on fabricated call names. A semantic entry keeps those
  consumer policies explicit.

## Acceptance and review

The [implementation plan](implementation.md#verification-matrix) names the
fixtures and checks for each contract. The final review must examine the event
identity/status rules, unchanged search/export outputs, and the complete
input/result display path together. This is an authored proposal, not an
independently verified architecture or an implementation completion report.
