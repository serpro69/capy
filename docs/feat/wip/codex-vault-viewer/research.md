# Evidence for Codex vault viewer changes

> Issue: [#121](https://github.com/serpro69/capy/issues/121)
> Investigated: 2026-10-03
> Repository baseline: `d8008a130c6fa07c8a70992937149073b8661097`
> Design: [design.md](design.md)

## Local observations

The published example matches a locally inspected Codex 0.159.0 paginated
recording from September 29. The custom `exec` input is 4,571 bytes across three
physical code lines. Its first line embeds a large escaped `apply_patch` input.
Immediately following it is `item_completed/FileChange`, status `completed`,
with four update records and real numbered unified hunks.

| File from the issue's public example | Added | Removed |
| --- | ---: | ---: |
| `docs/feat/wip/clarify-docs/design.md` | 5 | 2 |
| `docs/feat/wip/clarify-docs/tasks.md` | 2 | 2 |
| `klaude-plugin/skills/_shared/document-clarity.md` | 10 | 8 |
| `klaude-plugin/skills/clarify-docs/evals/destination-visibility/eval.json` | 6 | 2 |
| Total | 23 | 14 |

The wrapper result includes a script-completion header, an empty JSON object,
and a separate command result. Its text is not a reliable source for the nested
patch's status or complete diff. Later in the same recording, one `exec` encloses
two separate `FileChange` events, establishing that the relationship is not
necessarily one-to-one.

A bounded scan of 116 uncompressed rollouts dated September 29 found 300
`FileChange` events, all completed. None of their IDs matched a recorded response
function/custom-tool call ID. Their file operations comprised 208 updates and
365 adds. This supports independent event rendering; it does not establish the
absence of matching IDs in other clients or direct-call recordings. Failure,
decline, conflicting status, and deletion cases need synthetic fixtures because
this sample did not cover them.

These were read-only local observations. No raw private rollout, session ID, or
absolute local source path is included here. Implementation should encode a
small synthetic reproduction using neutral paths and independently authored text.

## Existing implementation

- [codex_decoder.go](../../../../internal/vault/codex_decoder.go):
  `codexCustomCallSummary` uses the first `exec` input line. Pass 2 constructs a
  diff only for a successful structured direct `apply_patch` result. Event
  handling consumes user messages and subagent activity, skipping file changes.
- [codex_types.go](../../../../internal/vault/codex_types.go): both patch event
  families are in known-type sets, so their omission is intentional rather than
  reported format drift.
- [codex_patch.go](../../../../internal/vault/codex_patch.go): the existing
  direct-input converter handles add/update/delete/move and deliberately does
  not invent line numbers absent from the patch input.
- [transcript.go](../../../../internal/vault/transcript.go) and
  [tui/render.go](../../../../internal/vault/tui/render.go): collapsed diff
  markers and colored line rendering already exist. `viewerToolMessage` repeats
  the call summary on result markers/prefixes, so input-only shortening is
  insufficient.
- [tui/find_text.go](../../../../internal/vault/tui/find_text.go) and
  [tui/viewer_targets.go](../../../../internal/vault/tui/viewer_targets.go):
  complete hidden bodies participate in search and detail identity uses message
  ordinals, supporting several messages with the same physical source line.

## Upstream evidence

The following source locations were checked at OpenAI Codex commit
[`b741e480e203f037ca726bc2a76d99a8e8668e66`](https://github.com/openai/codex/commit/b741e480e203f037ca726bc2a76d99a8e8668e66),
dated 2026-10-03. They describe observed upstream implementation, not a guaranteed
stable public rollout specification.

- [FileChangeItem](https://github.com/openai/codex/blob/b741e480e203f037ca726bc2a76d99a8e8668e66/codex-rs/protocol/src/items.rs#L407)
  has an ID, a changes map, optional status, and optional stdout/stderr. Optional
  status is why a completed envelope alone cannot establish successful edits.
- [PatchApplyEndEvent and status](https://github.com/openai/codex/blob/b741e480e203f037ca726bc2a76d99a8e8668e66/codex-rs/protocol/src/protocol.rs#L3738)
  carry call identity, success, completion state, diagnostics, and file changes.
  Add/delete records carry content; updates carry unified diffs and a possible
  move destination.
- [Persistence policy](https://github.com/openai/codex/blob/b741e480e203f037ca726bc2a76d99a8e8668e66/codex-rs/rollout/src/policy.rs)
  keeps legacy patch-end events for legacy history and uses completed turn items
  for paginated history. Supporting only one family would miss valid archives.
- [Patch history cells](https://github.com/openai/codex/blob/b741e480e203f037ca726bc2a76d99a8e8668e66/codex-rs/tui/src/history_cell/patches.rs#L13)
  pass structured changes and a working directory to diff rendering. The useful
  precedent is the separation of recorded changes from their UI representation.
- [Diff renderer](https://github.com/openai/codex/blob/main/codex-rs/tui/src/diff_render.rs)
  was inspected on 2026-10-03 for sorted file sections, counts, relative paths,
  wrapping, and line styling. It uses Rust/Ratatui facilities; the design adopts
  presentation ideas using Capy's existing renderer and copies no upstream code.

## Boundaries of verification

Research verified the motivating wire data and the existing code paths. It did
not run an implementation, benchmark the proposed changes, or perform independent
design review. The status table's conservative conflict/missing-data behavior is
a Capy design decision. Scanner/export parity and navigation through new input
markers remain explicit implementation checks.
