# Bulk vault deletion tasks

**Status:** done · **Created:** 2026-11-23 · [Design](design.md) · [Implementation plan](implementation.md)

**Not Doing:** automated cleanup, individual Claude sidecar pruning, persisted deletion jobs and MCP mutations.

## Task 1. Delete explicit CLI selections atomically

**Status:** done · **Depends on:** — · **Size:** M · **Can run in parallel with:** Task 3

**Execution context**

2026-11-23, observed at `65b9501`. The existing CLI loads transcript bytes before confirmation. The batch path needs metadata-only selection while preserving the single-session preview and the store’s one-time cleanup of orphan indexes.

2026-11-23, verified. Focused encrypted FTS5 tests passed for `./internal/vault` and `./cmd/capy` (run `26c49cc3`). Group rollback, stale targets and legacy cleanup also passed under `-race` (run `fb749eb7`). Independent review confirmed the fixes for closed stdout pipes, short-only input before vault open, and labeled batch fields.

**Docs:** [Explicit IDs through the CLI](implementation.md#explicit-ids-through-the-cli)

- [x] Add metadata-only preparation and exact-UUID, transactional apply in `internal/vault`. Preserve `DeleteSession`'s existing missing-row behavior and both deletion policies.
- [x] Update `newVaultDeleteCmd` usage, help, examples and argument validation. Check diagnostics in input order for a mixture of valid, short, missing and ambiguous IDs. Verify ordinary single-ID output, duplicate arguments resolving to one UUID, decline and empty stdin.
- [x] Implement field escaping and checked CLI output. Capture stderr with a multiline imported title and a control-bearing project, with and without `--yes`. Compare display text with the original tombstone project. Fail preview/prompt writes before apply and a success write after commit. Task 4 integrates the same escaping into the TUI.
- [x] Use a second connection to remove a reviewed target or change its project before apply. Assert zero batch writes. Select one more target than fits in a SQL parameter group plus a survivor. Verify the last UUID on success. Force failure in group two and assert rollback of both groups' archive rows, sidecars, overrides, all three indexes, deletion pairs and reader marker.

## Task 2. Include archived descendants on request

**Status:** done · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** Tasks 3, 4

**Execution context**

2026-11-23, observed at `cf7bacc`. Explicit targets and retained direct children already share a read transaction. Recursive selection will follow indexed parent links from those explicit identities before releasing that snapshot.

2026-11-23, verified. Encrypted FTS5 race tests passed for the store and CLI (runs `aff32a66` and `1f68f389`). A test-local SQLite function commits a child through a second connection while the production command reads parent metadata. That child and another inserted after preview survive apply. Independent review approved the updated tests and recursive selection.

**Docs:** [Descendants through the CLI](implementation.md#descendants-through-the-cli)

- [x] Add `--with-descendants`, revise `vault delete --help` and examples to distinguish the default from recursive selection, and test the help text. Traverse parent links inside preparation. Deduplicate targets, terminate on cycles and identify retained children against the whole deletion set.
- [x] Distinguish explicit and added rows in the preview. Show each added UUID's effective project even across projects.
- [x] Verify the parent/child/grandchild example, a missing parent, overlapping selections, a cycle, and single-ID batch output with expansion enabled. Insert children both during the read snapshot and after preview. Compare default and opt-in outcomes and verify the late arrivals survive.

## Task 3. Browse child sessions on both surfaces

**Status:** done · **Depends on:** — · **Size:** M · **Can run in parallel with:** Tasks 1, 2

**Execution context**

2026-11-23, observed at `2b58b010`. Plain list and the TUI construct their initial queries separately. The TUI also builds its first frame with grouping off. Both surfaces will use the same list options, including the launch cap and title predicate.

2026-11-23, verified. Full encrypted FTS5 vault and TUI suites, the glamour TUI suite, and vault CLI tests passed (run `78a90385-0728-44cf-83f1-98fe03bd960b`). The final layout passed the default and glamour TUI suites (`6cf8d18e` and `0a8dca98`). Independent review confirmed fixed filter visibility after resize and preservation of the finder and selected row in a 40×9 terminal. In short panes the filter summary yields space to browsing.

**Docs:** [Browsing sub-agents](implementation.md#browsing-sub-agents)

- [x] Add `ListOptions.ChildrenOnly`, preserve the zero value and existing `IncludeChildren` callers, and reject both true at CLI and store entry. Wire flags through plain and interactive list launches.
- [x] Reject negative CLI limits before opening either surface. Verify omission (50 plain, unlimited interactive), explicit zero and a positive cap. Cover name filtering and equal end times at the cap boundary.
- [x] Show the active mode and preserve fixed project/platform/name/cap constraints. Check grouping and orphan visibility on the initial frame. From separate direct child-only launches verify first `S` gives roots and first `s` gives all. Cover restoration of the previous mode and a failed refresh.

## Task 4. Mark and delete sessions in the TUI

**Status:** done · **Depends on:** Tasks 1, 3 · **Size:** M · **Can run in parallel with:** Task 2

**Execution context**

2026-11-23, observed at `dbe6430`. The list owns the full filtered result across pages. Its finder redraws on each keystroke. The deletion workflow will bind marks to the accepted finder and use the existing exact-ID vault plan.

2026-11-23, verified. The default and glamour TUI suites and focused race-enabled deletion/CLI checks passed (run `716fe3af-2ee8-4bf8-9b92-3532bb958b30`). The missing-target and changed-project cases also passed across store, TUI and CLI (`5955e110`). A pseudo-terminal drove the built CLI through marking, previewing and deleting two archives, then verified reimport under the invocation project’s one-time policy (`02104450`). Independent review approved the rendered stale notices and compact help after the fixes.

**Docs:** [Deleting from the TUI](implementation.md#deleting-from-the-tui)

- [x] Add full-UUID marks and a persistent count in the list title. Test mark-all across pages and after each first toggle from a direct child-only launch. Reconcile marks with every accepted result, including rename/project refresh. Verify that narrowing then widening never resurrects marks, and an error in the status line leaves the count visible.
- [x] Wire the preview to the store and invocation-project policy. Check scrolling, full UUIDs at narrow widths, escaped fields, opt-out and empty marks. Exercise `x x`, `x Enter`, Escape before confirmation, and a stale prepare reply after cancel/reopen. While apply is pending, verify that repeated `y` schedules one write, Escape cannot report cancellation, `Ctrl+G` cannot open an editor, and a quit request waits for the result.
- [x] Exercise a target vanishing before prepare and before apply, and a project changing before apply. Reload under current filters, report dropped marks, and review surviving marks on the next `x`. Inject a failed reload and show that retry recovers without automatic confirmation.
- [x] After commit, remove cached rows and hits, advance the search sequence and rerun a nonempty query independently of list reload. Combine a failed list reload, reopening search and a late old reply. Assert the committed count is reported and stale hits stay gone. A fresh read after one-time deletion and reimport may return the UUID again.

## Task 5. Expand and review descendants in the TUI

**Status:** done · **Depends on:** Tasks 2, 4 · **Size:** S · **Can run in parallel with:** —

**Execution context**

2026-11-23, observed at `b49dc07`. The vault already returns explicit targets before recursively added descendants. The TUI has the full accepted item model before pagination and guards prepare replies by request number.

2026-11-23, verified. The deletion race tests and full default and glamour TUI suites passed (run `5e02ed74-dea4-4d80-a459-43f8348d1241`). Independent review identified that Bubble Tea can coalesce frames. Confirmation now waits for a successful terminal write of the current preview. A renderer test held that write and checked both early and subsequent confirmations three times under the race detector (`abf6dc42`). A pseudo-terminal test exercised expansion, collapse, the outside-list label and deletion against a temporary vault (`d7580a2e`). Re-review approved the change.

**Docs:** [Descendants and preview](design.md#descendants-and-preview)

- [x] On opening the preview capture all accepted list UUIDs across pages separately from the marked UUIDs. Use the same membership for every `d` toggle. Label only added descendants absent from that set. Accept `y` only for the currently displayed, ready plan.
- [x] Mark a parent in an all-sessions list. Put one unmarked child on a later page and exclude its same-project sibling by name. Expand, collapse and expand again. Assert only the excluded sibling carries the outside-list label each time. Cover roots-only membership, overlapping marks, cancel and a late prepare reply after a toggle.

## Task 6. Verify and document the operator workflow

**Status:** done · **Depends on:** Task 5 · **Size:** S · **Can run in parallel with:** —

**Execution context**

2026-11-23, observed at `fae57b2`. The CLI and viewer share a reviewed deletion plan. The operator reference still describes single-ID deletion, and the architecture's sub-agent section documents only the roots/all toggle. Issue #136 remains open for bulk project and session-title edits.

2026-11-23, corrected during code review. Bubble Tea can coalesce a committed status or discard a terminal write error. The viewer now keeps the full outcome for reporting on exit until a successful frame displays it. A failed frame requests a quit that waits for any confirmed write. A pseudo-terminal check against a temporary encrypted vault closed stdout after the real preview and confirmed that the process exited on its own, reported one committed deletion and the broken pipe on stderr, and left the vault empty (run `0dea3fa9`).

2026-11-23, reviewed. Added CLI coverage for the original single-ID missing and ambiguous diagnostics, including the candidate rows on stderr. Task 1 explicitly preserves the existing `DeleteSession` API, including its missing-row result and one-time orphan-index cleanup. Both deletion paths share the same write primitive.

2026-11-23, verified with both synthetic keys and FTS5. The full CLI rerun passed (`62be8653`). The full vault store suite passed (`4ecade6a`), including remembered deletion, import and merge. The extra whole-store race run reached its deadline. The requested deletion and concurrency cases passed under the race detector (`28f4426c`). Default, race and glamour TUI suites passed (`388bdec8`, `42918c5e`, `cac2f694`), as did the race-enabled single-ID CLI checks (`1e41464b`). The quit-during-apply test also verifies the count reaches stderr exactly once.

2026-11-23, reviewed against the design. The independent code and spec reviewers approved the reporting fix. The Claude CLI follow-up approved the Task 6 changes and identified the quit-path assertion added above. Bulk project reassignment and title editing remain on the open [issue #136](https://github.com/serpro69/capy/issues/136), with scope and acceptance questions in the [follow-up](implementation.md#follow-up).

**Docs:** [Between review and commit](design.md#between-review-and-commit)

- [x] Run `kk:test` with both synthetic vault/knowledge credentials and FTS5. Cover the affected CLI, store and TUI suites, including the existing remembered-delete/import/merge tests. Run the store concurrency cases under the race detector and exercise the TUI with the optional glamour build tag.
- [x] Use `kk:document` to update the [vault reference](../../../reference/vault.md) and relevant [architecture sections](../../../architecture.md) for selection, parent behavior, confirmation and rollback.
- [x] Use `kk:review-code` for the Go changes and `kk:review-spec` against the design. Track remaining title/project editing work on [#136](https://github.com/serpro69/capy/issues/136).

## Dependency graph

```text
1 ──→ 2 ─────────┐
│                ├──→ 5 ──→ 6
└──→ 4 ──────────┘
     ↑
     3
```
