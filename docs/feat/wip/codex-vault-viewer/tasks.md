# Tasks: readable Codex vault edits

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Research: [research.md](research.md)
> Issue: [#121](https://github.com/serpro69/capy/issues/121)
> Status: pending
> Created: 2026-10-03
> Design review: pending; draft defaults are one edit group per patch and viewer-only scope
> Not Doing: JavaScript interpretation, shell-edit inference, Rust renderer port, rich syntax highlighting, line-number gutter, per-file picker, global FTS changes, export changes, schema/version changes, general cross-platform tool-input UI, live filesystem verification

## Task 1: Paginated edits open as grouped diffs

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Strategy:** Risk-First — prove the motivating archive-to-viewer path and consumer parity
- **Docs:** [Task 1 implementation](implementation.md#task-1--paginated-edits-from-archive-to-expandable-diff)

### Subtasks

- [ ] 1.1 Add synthetic wrapped-call fixtures and freeze current scanner/export outputs in the existing Codex fixture/consumer tests → verify: baseline expectations pass before decoder changes.
- [ ] 1.2 Add `EntryFileChange` and its normalized payload in `transcript_model.go`, wire parsing in `codex_changes.go`/`codex_types.go`, and decoder registration without changing `openAsst` → verify: paginated state, identity, metadata, and physical-line tests pass.
- [ ] 1.3 Convert add/delete/update/move data with explicit unavailable states for malformed files → verify: converter tests cover counts, ranges, empty files, final newlines, and mixed valid/unsupported records.
- [ ] 1.4 Compose grouped viewer messages in `transcript_changes.go`/`transcript.go`; explicitly skip the new kind in scanner and export consumers → verify: grouped expansion works and frozen consumer output remains identical.
- [ ] 1.5 Verify completed/failure/decline/unconfirmed presentation and exact duplicate/conflicting event behavior → verify: no uncertain applied diff and no repeated identical event card.

Legacy/direct-call reconciliation remains Task 2; long input/result summaries
remain Task 3. Task 1 alone does not complete the issue.

## Task 2: Legacy events and direct patch results reconcile correctly

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Task 2 implementation](implementation.md#task-2--legacy-edits-and-direct-call-reconciliation)

### Subtasks

- [ ] 2.1 Add the legacy adapter and presence-aware status normalization in `codex_changes.go`/`codex_types.go` → verify: every status-table row and mixed history formats.
- [ ] 2.2 Reconcile exact non-empty operation IDs and link direct results in `codex_decoder.go`/`transcript_model.go` → verify: multiple nested edits stay independent and no path/proximity matching occurs.
- [ ] 2.3 Prefer structured event diffs while retaining original plain result bodies and detecting explicit outcome conflicts → verify: one direct-operation edit card, visible failures, and no inferred success.
- [ ] 2.4 Retain successful direct-input fallback only without an associated usable event → verify: existing direct-patch tests plus malformed/event-less compatibility cases and unchanged consumer outputs.

## Task 3: Long executable inputs and their results stay readable

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Task 3 implementation](implementation.md#task-3--expand-long-executable-inputs-without-repeated-summaries)

### Subtasks

- [ ] 3.1 Populate optional `ToolCall.CodeText` for Codex custom `exec` without altering shared summaries → verify: byte-exact input and scanner/export parity tests.
- [ ] 3.2 Add ordered input-marker composition and result display aliases in `transcript_inputs.go`/`transcript.go` → verify: both sides of the motivating wrapper stay compact while their bodies remain complete.
- [ ] 3.3 Add `RoleToolInput` labels/styles and generalized existing detail routing in TUI rendering, `viewer.go`, and find headers → verify: correct input heading, Enter/Esc, copy/raw return, and both build variants.
- [ ] 3.4 Verify exact/fuzzy find over full hidden input and grouped diff bodies using existing corpus/frame semantics → verify: duplicate source lines, pending resize/cancel, repeated navigation, and source anchors.

## Task 4: Final verification, documentation, and review

- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Final verification](implementation.md#task-4--final-compatibility-documentation-and-reviews), [Verification matrix](implementation.md#verification-matrix)

### Subtasks

- [ ] 4.1 Run `$kk:test`, focused/full/race checks, and default/glamour builds with synthetic encryption keys → verify: all applicable checks pass; genuine canary skips are recorded.
- [ ] 4.2 Check the motivating archive read-only, compare parse/render costs, and exercise the existing find latency protocol → verify: correct edit counts/navigation with no unexplained regression.
- [ ] 4.3 Run retrieval-quality benchmarks against an identified baseline → verify: unchanged scanner outputs and no quality regression; preserve prior reports.
- [ ] 4.4 Run `$kk:document` for `README.md`, `docs/architecture.md`, and model comments → verify: keys, scope, state semantics, and links match the implementation.
- [ ] 4.5 Run `$kk:review-code` (Go) and `$kk:review-spec`; save verification/review evidence locally → verify: findings fixed or durably recorded with concrete next actions; no required work silently deferred.

## Dependency Graph

```text
Task 1 → Task 2 → Task 3 → Task 4
```

All tasks are serial because they share decoder, transcript, or viewer contracts.
The two user-facing slices remain structured edit display (Tasks 1–2) and long
input disclosure (Task 3), followed by complete verification.
