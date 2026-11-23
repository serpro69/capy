# Bulk vault deletion tasks

**Status:** in-progress · **Created:** 2026-11-23 · [Design](design.md) · [Implementation plan](implementation.md)

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

**Status:** pending · **Depends on:** Task 1 · **Size:** M · **Can run in parallel with:** Tasks 3, 4

**Docs:** [Descendants through the CLI](implementation.md#descendants-through-the-cli)

- [ ] Add `--with-descendants`, revise `vault delete --help` and examples to distinguish the default from recursive selection, and test the help text. Traverse parent links inside preparation. Deduplicate targets, terminate on cycles and identify retained children against the whole deletion set.
- [ ] Distinguish explicit and added rows in the preview. Show each added UUID's effective project even across projects.
- [ ] Verify the parent/child/grandchild example, a missing parent, overlapping selections, a cycle, and single-ID batch output with expansion enabled. Insert children both during the read snapshot and after preview. Compare default and opt-in outcomes and verify the late arrivals survive.

## Task 3. Browse child sessions on both surfaces

**Status:** pending · **Depends on:** — · **Size:** M · **Can run in parallel with:** Tasks 1, 2

**Docs:** [Browsing sub-agents](implementation.md#browsing-sub-agents)

- [ ] Add `ListOptions.ChildrenOnly`, preserve the zero value and existing `IncludeChildren` callers, and reject both true at CLI and store entry. Wire flags through plain and interactive list launches.
- [ ] Reject negative CLI limits before opening either surface. Verify omission (50 plain, unlimited interactive), explicit zero and a positive cap. Cover name filtering and equal end times at the cap boundary.
- [ ] Show the active mode and preserve fixed project/platform/name/cap constraints. Check grouping and orphan visibility on the initial frame. From separate direct child-only launches verify first `S` gives roots and first `s` gives all. Cover restoration of the previous mode and a failed refresh.

## Task 4. Mark and delete sessions in the TUI

**Status:** pending · **Depends on:** Tasks 1, 3 · **Size:** M · **Can run in parallel with:** Task 2

**Docs:** [Deleting from the TUI](implementation.md#deleting-from-the-tui)

- [ ] Add full-UUID marks and a persistent count in the list title. Test mark-all across pages and after each first toggle from a direct child-only launch. Reconcile marks with every accepted result, including rename/project refresh. Verify that narrowing then widening never resurrects marks, and an error in the status line leaves the count visible.
- [ ] Wire the preview to the store and invocation-project policy. Check scrolling, full UUIDs at narrow widths, escaped fields, opt-out and empty marks. Exercise `x x`, `x Enter`, Escape before confirmation, and a stale prepare reply after cancel/reopen. While apply is pending, verify that repeated `y` schedules one write, Escape cannot report cancellation, `Ctrl+G` cannot open an editor, and a quit request waits for the result.
- [ ] Exercise a target vanishing before prepare and before apply, and a project changing before apply. Reload under current filters, report dropped marks, and review surviving marks on the next `x`. Inject a failed reload and show that retry recovers without automatic confirmation.
- [ ] After commit, remove cached rows and hits, advance the search sequence and rerun a nonempty query independently of list reload. Combine a failed list reload, reopening search and a late old reply. Assert the committed count is reported and stale hits stay gone. A fresh read after one-time deletion and reimport may return the UUID again.

## Task 5. Expand and review descendants in the TUI

**Status:** pending · **Depends on:** Tasks 2, 4 · **Size:** S · **Can run in parallel with:** —

**Docs:** [Descendants and preview](design.md#descendants-and-preview)

- [ ] On opening the preview capture all accepted list UUIDs across pages separately from the marked UUIDs. Use the same membership for every `d` toggle. Label only added descendants absent from that set. Accept `y` only for the currently displayed, ready plan.
- [ ] Mark a parent in an all-sessions list. Put one unmarked child on a later page and exclude its same-project sibling by name. Expand, collapse and expand again. Assert only the excluded sibling carries the outside-list label each time. Cover roots-only membership, overlapping marks, cancel and a late prepare reply after a toggle.

## Task 6. Verify and document the operator workflow

**Status:** pending · **Depends on:** Task 5 · **Size:** S · **Can run in parallel with:** —

**Docs:** [Between review and commit](design.md#between-review-and-commit)

- [ ] Run `kk:test` with both synthetic vault/knowledge credentials and FTS5. Cover the affected CLI, store and TUI suites, including the existing remembered-delete/import/merge tests. Run the store concurrency cases under the race detector and exercise the TUI with the optional glamour build tag.
- [ ] Use `kk:document` to update the [vault reference](../../../reference/vault.md) and relevant [architecture sections](../../../architecture.md) for selection, parent behavior, confirmation and rollback.
- [ ] Use `kk:review-code` for the Go changes and `kk:review-spec` against the design. Track remaining title/project editing work on [#136](https://github.com/serpro69/capy/issues/136).

## Dependency graph

```text
1 ──→ 2 ─────────┐
│                ├──→ 5 ──→ 6
└──→ 4 ──────────┘
     ↑
     3
```
