# Implementation plan for bulk vault deletion

The code baseline is `e799ce0`. The [design](design.md) defines membership, confirmation and failure behavior for the first delivery of [#136](https://github.com/serpro69/capy/issues/136).

## Explicit IDs through the CLI

Start at `newVaultDeleteCmd` in `cmd/capy/vault.go`. It currently loads the transcript with `GetSession`, looks up direct children, prints a preview and then calls `DeleteSession`. Replace that sequence with two store operations used by both interfaces:

- **Prepare** takes CLI prefixes or exact TUI UUIDs, an expansion choice, and the policy resolved by the caller. Return an in-memory plan containing each distinct target, display metadata, exact effective project, explicit or descendant origin, and retained-child information. Use `sessionIDPrefixPattern` only for CLI input. Collect input-specific errors and ambiguity candidates in argument order. A database failure or cancellation stops preparation immediately. Read all candidates, parent links and retained children through the same read transaction, then release it before rendering. Use the metadata joins and scanner on that transaction. `GetSession` also loads the transcript, and `VaultStore.Children` queries the pool outside the caller's snapshot. Preserve explicit input order (accepted-list order for TUI marks) and use a stable order for descendants.
- **Apply** takes that plan back on the same store. Acquire `sqliteutil.BeginImmediateContext` on `vault_meta`, revalidate exact UUIDs and effective projects, and complete all mutations before commit. For remembered deletions insert each exact UUID/project pair and stamp the existing minimum reader version 4 in the transaction. One-time deletion adds no pair and leaves the reader requirement intact. Clean all three FTS tables using `deleteSessionIndexes`. Delete archive rows by exact UUID and let existing foreign keys remove sidecars and overrides. Bound parameter groups within the transaction. Share the write primitives with `DeleteSession`. Its existing single-ID API returns false on a missing archive, and its one-time path can still clean orphan index rows for that UUID. A missing batch target instead invalidates the whole plan before writes.

The store boundary belongs in `internal/vault/deletions.go` or an adjacent focused file. Use the caller's context throughout. `DeleteSession`, `Children` and `ListSessions` in `internal/vault/store.go` provide the current deletion and lookup rules. The merge path in `internal/vault/merge_deletions.go` demonstrates the SQLite transaction primitives but intentionally commits each merge group separately.

Update the Cobra `Use`, `Short`, `Long`, examples and argument validator together. Adapt `handleLookupError` and its candidate renderer for delete so all bad arguments can be diagnosed before returning failure. Choose the legacy or batch format from the invocation before deduplicating. Exercise two aliases of one UUID, and one ID with expansion enabled but no descendants. Both use batch reporting with a committed count of one.

Use one field-escaping rule for CLI and TUI deletion output, including retained-child warnings and ambiguous candidates. Render newline, carriage return, tab, ESC, other terminal controls and Unicode line/paragraph separators as visible escape text before layout or styling. Preserve ordinary printable values. Revalidation and tombstones use the exact stored project. Check all pre-apply writes, including the prompt when present. The current `promptYesNo` ignores the result of writing the question. Preserve its default-no and affirmative input handling. If writing the success message to stdout fails after commit, return a reporting error identifying the committed count. Never reapply.

**Verify** with encrypted temporary vaults. Use mixed valid, too-short, missing and ambiguous arguments and assert ordered diagnostics and zero deletions. Cover a single ID, duplicate arguments, decline/empty stdin and both policies. Capture stderr for a batch with a multiline imported title and a control-bearing imported effective project, both interactively and with `--yes`. Check the escaped fields and original project in the tombstone. Inject failures writing the preview and the prompt and verify zero deletions. Fail stdout after commit and verify the committed state. Task 4 exercises the same field rendering in the TUI.

Exercise **one more distinct target than fits in an apply parameter group**, plus an unrelated survivor. On success verify every target including the trailing UUID and the survivor's archive and indexes. In a fresh fixture force a write error on the trailing group's target after the first group executed. Compare archives, sidecars, title/project overrides, all three FTS indexes, deletion pairs and the reader-version marker against their pre-apply state. The assertion must detect a commit of the first group as well as omission of the last.

## Descendants through the CLI

Add `--with-descendants` to delete and update the help and examples to explain the default and opt-in behavior. Resolve direct selections first, then visit archived child rows by exact `parent_uuid` in the same read transaction. The parent index already exists. Use a visited UUID set, including on self-links and longer cycles, and collect only metadata. An explicit target takes precedence over its discovered origin. Compute the retained-child warning after the full set is known. The CLI shows each added descendant's parent and effective project. The store has no dependency on TUI list membership.

**Verify** with a parent, child and grandchild, an independently selected child, and an unrelated session. With two connections, insert a child after the read snapshot is established and before expansion finishes. It must stay out of that plan. Also insert a child after preview returns and before apply. Both late arrivals survive. Put the stale-target and changed-project checks in Task 1 so the explicit-ID path has atomicity coverage on its own.

## Browsing sub-agents

Add `ChildrenOnly` to `vault.ListOptions` alongside the existing `IncludeChildren`. Both false selects roots, only `IncludeChildren` selects all, and only `ChildrenOnly` selects `parent_uuid IS NOT NULL`. Reject both true at the CLI boundary and at the start of `ListSessions`, before opening the DB. This preserves existing callers and the zero value. Use a row's own effective project. A missing parent does not hide a child.

Wire list options through `newVaultListCmd`, `tui.Options`, `newModel` and `listModel.listOptions`. Reject negative limits before either the plain-list store open or TUI launch. Distinguish an omitted flag from explicit zero with Cobra's changed-flag state. Omission yields 50 for plain list and unlimited for TUI. Zero is explicitly unlimited and positive values cap the matching rows. Project, platform, name and the numerical cap persist through refreshes. Keep newest-first order and break equal end times by UUID ascending before applying the cap so tied rows give the same membership on an unchanged reload. Apply the local finder to the capped result.

Initialize the first query **and** the list renderer from the same mode. Today `newListModel` renders its initial rows with grouping off. In either mode that displays children, group visible parent/child pairs and keep orphans selectable. Track the return mode for `S`, initializing it to roots on a direct child-only launch. Implement the [mode transitions](design.md#list-modes-and-filters) as successful refreshes. A failed transition keeps both mode values and the previous list. Show the current mode and fixed launch constraints.

**Verify** both conflict checks and all four limit cases, including a negative value before a TUI opens. Check the first frame on direct all/child-only launches, the child-only return keys, title/limit ordering, fixed launch constraints, an orphan and a failed mode change. Task 4 verifies mark-all against these results.

## Deleting from the TUI

### Marks and the accepted result

Extend `internal/vault/tui/list.go` with full-UUID marks, row indicators and a count in the list title. Keep a separate accepted finder value because `setSessions` currently overwrites `applied` on every keystroke. On a reload during editing, reconcile marks under the last accepted value and redraw rows using the draft value. Typing alone never removes marks. Enter or Escape accepts the resulting filter. Select-all reads the full accepted item model.

Task 4 opens the preview with the ordered marked UUIDs. Task 5 also captures a membership set of **every** UUID in the accepted list across pages. Keep that set in the TUI so each added descendant can be checked against the same list on every toggle. Release it when leaving the preview.

### Preview and apply

Resolve `vault.remember_deletions` through `vaultConfigForCommand` for the invocation project when opening review. Pass the resolver through `launchTUI` and freeze the policy for that preview. A configuration error keeps browsing available and disables this deletion. The list inside `list`, `search` and `show` TUIs uses the same in-process workflow and open store. Restore/resume remain deferred CLI actions.

Route preview key messages ahead of the root-level project editor (`Ctrl+G`) and ordinary mode bindings in `Model.update`. Keep handling resize and background replies at the root. Associate each prepare reply with the request that issued it, including across cancel and reopen.

| Preview state | Input and outcome |
| --- | --- |
| Preparing | Escape abandons the request. In Task 5, `d` supersedes it, using the original marks, accepted-list membership and policy. Ignore `y` and any superseded reply. |
| Ready | Scroll or cancel. In Task 5, `d` starts preparation again. Lowercase `y` consumes the displayed plan and enters the applying state before scheduling the write. |
| Applying | Consume ordinary keys, including Escape, `d`, `y` and `Ctrl+G`, until the write returns. A `Ctrl+C` quit request during apply waits for that outcome before releasing the store. Forced process termination follows the existing inspect-before-retry guidance. |

### Returning to the list

On a stale result from **prepare or apply**, discard the plan, return to list navigation and reload under the accepted filters. Reconcile marks and report the reason and dropped count. A changed-project UUID that still qualifies stays marked for a new preview. If reload fails, retain the marks, show the error, and have the next `x` retry the reload before preparing. An empty selection cannot open a preview. For other prepare errors or a rolled-back write, retain the selection, surface the error and require a new preview.

On **commit**, immediately remove committed UUIDs from cached list data and marks. Before reloading the list, clear cached search hits/reset the cursor and use `searchModel.refresh` to advance the sequence. This rejects old debounce ticks and search replies even in list mode. Dispatch the returned search command for a nonempty query even if list reload fails. Since an active query currently keeps its old hits until its reply arrives, calling `refresh` alone is insufficient. Surface the committed count alongside any failure refreshing list or search. A refresh error never retries the deletion.

**Verify** using Bubble Tea messages and the `dataStore` test seam. Cover multi-page marks, metadata refresh during filter editing, stale reload/retry, and the preview states above. Exercise scrolling and narrow-width UUID display. Drive search → list → delete with an older search reply in flight and a failing post-commit list reload. Reopen search before the replacement reply. The late reply cannot restore cached hits. A fresh search uses current store contents, including any legitimate reimport following a one-time deletion. Use a command-wiring test to check that the invocation project's policy opt-out reaches the shared store operation.

## Follow-up

The rest of [#136](https://github.com/serpro69/capy/issues/136) remains open after bulk deletion. **Owner unassigned.** Revisit bulk project assignment and session-title editing after the selection workflow is available. Define how shared titles, individual titles and naming patterns will be entered and reviewed before adding those operations. Verify them against the existing single-session naming, clear and merge behavior.
