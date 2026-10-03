# Task 2 code review — grouped paginated updates

Mode: `kk:review-code:isolated`, mid-implementation. Reviewers: independent
`code-reviewer` and PAL `gemini-3.1-pro-preview` (two-step external review).
The review snapshot included 14 files and 777 changed lines, including new files.

Task 2 and its Task 1 compatibility contract were in scope. Tasks 3–6 remain
pending: additional operations/identity reconciliation, legacy/direct correlation,
long inputs and final feature verification. Their absence was explicitly excluded
from missing-implementation findings. No approval is implied for those tasks.

The Go profile activated on `.go` extensions. Both reviewers received
`solid-checklist`, `removal-plan`, `security`, `code-style`, `error-handling`,
`security-injection-ref`, `naming` and `performance` checklists. Conditional
coverage concerned archive input, exported model types and eager decoder costs;
there are no new database, concurrency or gRPC mechanisms.

## Independent code reviewer

**APPROVE. No P0–P3 findings or removal candidates.** The source review covered
hunk validation/counts, status gating, unavailable data, ordering, archived-CWD
paths, physical anchors, `openAsst`, heading rendering, existing tool navigation,
copy/find ownership and scanner/export skips. The reviewer did not independently
run tests; execution evidence is in [verification.md](../verification.md).

## External reviewer findings and author context

No findings were corroborated by both reviewers.

1. **[HIGH] `codex_changes.go`, `codexUpdateDiff`: blank context lines rejected.**
   The external reviewer recommended accepting an empty string as a context line,
   citing `codexPatchToDiff`'s permissive handling of direct `apply_patch` input.
   Profile: Go; checklists: error handling and input validation.

   **Author context:** the two functions read different grammars. `codex_patch.go`
   reads model-authored `*** Begin Patch` input with unnumbered hunks; Task 2 reads
   archived `unified_diff` with numbered ranges. The design explicitly requires
   validating prefixes and range lengths. The current parser accepts a space-only
   context line, including CRLF, and rejects a missing prefix. A read-only aggregate
   corpus check found 505 canonical recordings and 953 completed non-move update
   records: 1,400 prefixed blank context lines and zero unprefixed blanks. Three
   malformed outer lines were skipped; no compressed recordings or oversize lines
   were present. These live counts are separate from the earlier parity run.

   The independent reviewer rechecked this specific concern and found the HIGH
   claim unsupported by the cited comparison (96% confidence). Strict validation
   is retained; explicit valid-prefixed and invalid-unprefixed regression cases
   were added. Revisit only if an actual structured recording supplies contrary
   format evidence; preserve its numbered-range validation in any adaptation.

2. **[LOW] `codex_changes.go`, `codexChangeOutput`: JSON-null check allocation.**
   Recommendation: replace `strings.TrimSpace(string(raw)) == "null"` with
   `bytes.Equal(bytes.TrimSpace(raw), []byte("null"))`.
   Profile: Go; checklist: performance.

   **Author context:** no allocation measurement or regression was supplied.
   Retained for Task 6's existing same-corpus parse/render measurement: profile
   large stdout/stderr records and apply the byte-based check if it improves
   measured allocations. It has no bearing on current output correctness.

3. **[LOW] `codex_changes.go`, `codexUpdateDiff`: builder capacity.**
   Recommendation: call `body.Grow(len(text))` before composing validated hunks.
   Profile: Go; checklist: performance.

   **Author context:** no measured regression was supplied; eager reservation also
   allocates the full input size for malformed diffs rejected near their start.
   Task 6 should compare valid large hunks and early-rejected malformed inputs,
   then choose capacity behavior using those results. No speculative allocation
   change was made in Task 2.

The external narrative also referred to event equality tracking and extracting
`renderDiffBody`. Equality remains Task 3 work, and `renderDiffBody` predates this
patch; those remarks are not implementation evidence.

## Completion

No required Task 2 fix remains outstanding. Full repository tests, focused race
tests, the glamour TUI suite, synthetic goldens, real-corpus parity and retrieval
quality checks passed. The two optional performance suggestions have concrete
next actions in Task 6's implementation plan. No qualifying systemic P0/P1 finding
was established, so there are no findings to index. No new project convention
requires indexing; the grammar distinction and task boundaries are recorded here.
