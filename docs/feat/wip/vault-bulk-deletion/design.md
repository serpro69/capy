# Delete a selection of vault sessions

**Bulk-deletion milestone · 2026-11-23 · First delivery of [issue #136](https://github.com/serpro69/capy/issues/136)**

A vault owner can review several archived sessions and delete them with one confirmation. **One transaction deletes exactly the reviewed UUIDs.** Canceling before confirmation preserves the archives, search entries and deletion records. An invalid input, or a target missing when the write transaction checks it, prevents the entire deletion. The same rule covers a pair of CLI arguments and a selection spanning many TUI pages.

The [implementation plan](implementation.md) explains the store and interface changes. The [task list](tasks.md) orders the work. The [review resolutions](review-resolution.md) record the evidence behind the decisions.

## Selecting sessions

| Surface | Selection | Review |
| --- | --- | --- |
| CLI | `capy vault delete <id> [<id> ...]` accepts the existing literal UUID prefixes of at least eight characters. Resolve them all in one snapshot. Two arguments resolving to the same UUID select it once. | Report every invalid, missing or ambiguous argument in input order, with candidates for each ambiguity. Any such error ends the command before preview or mutation. On success, list every target on stderr before one default-no prompt. `--yes` still prints the preview. Preserve single-ID output and diagnostics for ordinary printable input. |
| TUI session list | Mark individual rows by UUID or mark all rows matching the current list filters, across every page. The cursor and the marked set are independent. | Open a scrollable preview of the marked set. An empty selection disables deletion. Returning to the list lets the user adjust marks. |

One CLI argument with descendant expansion off keeps the current detailed stderr preview, default-no prompt and short-ID success line. Multiple arguments or `--with-descendants` use the batch format, even if the final set has one UUID. Show the explicit and added counts in the batch preview and report the distinct committed UUIDs on stdout. In the TUI, enter deletion review from the session list. The existing `vault delete --tui` command-line guard still applies.

### List modes and filters

The browsing controls distinguish **roots** (all top-level sessions), **all sessions**, and **sub-agents only**. A child is an archived row with a parent UUID, even when the parent is absent. Claude sub-agent files belong to their containing archive.

`vault list` starts in roots mode by default. `--include-children` starts it in all-sessions mode and `--children-only` starts it in child-only mode. Enabling both flags is an error. In the TUI these flags choose the **initial** mode. Lowercase `s` switches roots to all, all to roots, and child-only to all. Uppercase `S` enters child-only and restores the preceding roots/all mode on the next press. For a direct child-only launch, the return mode is roots. Project, platform, name and any explicit limit remain in force across these transitions. Apply these filters and the child predicate before the limit, then apply the local TUI finder to those rows. Reject negative limits before opening the vault or the TUI. Zero means unlimited. Omitting the flag keeps the plain CLI default of 50 and the TUI default of unlimited.

### Marking rows

The **accepted result** is the set of UUIDs in that filtered list across every page. In list navigation, `m` toggles the highlighted row, `a` marks the entire accepted result, `A` clears all marks, and `x` opens deletion review. The list title shows the marked count independently of transient errors or notices. Existing single-session actions still target the highlighted row.

**Marks are always a subset of the last accepted result.** Paging preserves them. While editing a filter, keep the marks and disable deletion. Enter accepts the filter and Escape accepts the existing clear-filter action. Intersect the marks with each newly accepted result, including a child-mode change or a successful refresh after a rename or project edit. Report how many marks were dropped. Widening the result or importing a session never adds a mark automatically.

## Descendants and preview

The default deletes exactly the selected rows. The operator can opt into **Include descendants** in the TUI preview or pass `--with-descendants` to the CLI. Expand from each selected UUID through the recorded parent links in the same vault, at all depths. Deduplicate overlapping selections and protect traversal against cycles. Explicitly selected rows appear first, followed by added descendants with their effective projects and the parent link that brought them in. An explicitly selected child is counted once even when its parent is also selected.

When the TUI preview opens, capture membership of the entire accepted result. Label an added descendant **outside current list** exactly when its UUID is absent from that snapshot. Use the same snapshot when toggling descendants. An unmarked child on page two is inside the list. Its sibling filtered out by name gets the label even if both have the same effective project. In a roots-only list every added descendant is outside. The CLI takes IDs directly and shows origin, parent and project for each addition without a list-scope label.

For example, select a parent and its child when the child has one archived child of its own. The default preview has two explicit rows and identifies the grandchild as retained. Enabling descendants yields two explicit rows and one added row. The grandchild's project is displayed even if its project differs from the parent's. A child archived after the final preview snapshot survives that apply. Toggling descendants again takes a fresh snapshot and shows the revised set before confirmation.

Use the same store plan for both interfaces. Display the full UUID, effective title, platform and effective project of each target. Preserve single-session detail and the warning about independently archived children that will survive. Count or identify retained direct children relative to the **whole** deletion set. Escape control characters and line separators in every archive-provided display field, including candidate diagnostics. Each value stays within its field and ordinary printable text keeps its formatting. The plan retains the original values for equality checks and deletion records.

### Confirmation

The TUI opens each review with descendants off and displays separate counts for explicit and added targets. Up/Down scroll one line and PgUp/PgDn one page. Long fields wrap so the full UUID remains inspectable at narrow widths. Before confirmation, Escape returns to the list with its marks and `d` requests a fresh preview with descendants toggled. Only lowercase `y` on a ready preview starts deletion. While that write is in progress, the preview shows its pending state and consumes ordinary keys, including Escape and additional `y` presses. An exit or interruption after confirmation cannot promise cancellation.

Present the invocation project's deletion policy once for the batch. The default records each UUID and effective project, excludes future imports and can propagate the deletion on merge. The opt-out performs a one-time local removal and allows the archive to return. Removing a session also removes its sidecars, title/project overrides and search entries. Originals on disk survive. [Existing deletion contract](../vault-session-deletion/design.md) · [Operator reference](../../../reference/vault.md#delete-a-session)

## Between review and commit

1. Resolve the requested IDs and any descendants using metadata from one read snapshot. Freeze the exact target UUIDs, effective projects, and deletion policy in memory. End the read transaction before displaying the preview. Both interfaces use the same open vault for preview and apply.
2. On confirmation, acquire the vault write transaction and look up **only those exact UUIDs**. A missing target or an effective project that differs from the reviewed value invalidates the preview. The project matters because it determines which project-filtered merges can carry a remembered deletion. A title edit or transcript growth preserves the UUID. A one-time removal followed by reimport of that UUID under the same project before the check also qualifies. Filters, prefixes and descendant discovery run only at preview time.
3. Apply ordinary vault deletion semantics to every target inside that one transaction. Publish success only after commit. On a missing target during preparation or a stale plan at apply, the CLI exits with a diagnostic. The TUI discards the plan, reloads the list, retains surviving in-scope marks and reports any dropped count. A refresh failure leaves an error and requires a successful reload before the next review. The TUI requires fresh confirmation after a retry. A new CLI invocation always rebuilds its preview and honors its own `--yes` setting.
4. On commit, remove deleted UUIDs from the cached list and marks and invalidate cached search hits and older search replies immediately. Rerun a nonempty search under its new sequence even if the list refresh fails. A fresh read may legitimately show a one-time deletion that has since been reimported. Report the committed count and any refresh or output error separately. A reporting failure cannot undo the commit.

Normal vault opening can initialize or migrate the database. Preparing a deletion introduces no schema change and records no deletion. The plan lives for one command or one preview screen. If the process exits after confirmation and before reporting the outcome, inspect the vault before issuing a fresh command.

## Assumptions

- UUID is the archive identity across reimports and replicas. The existing deletion mechanism already relies on it.
- Session metadata and the indexed parent links suffice for selection, traversal and previews. The design requires no transcript or sidecar decoding.
- The TUI loads matching metadata before paginating. The preview needs one UUID per accepted row for list membership and metadata for the distinct deletion targets. It need not duplicate transcript or sidecar bytes.

## Not Doing

- Automated cleanup based on inferred roles or value. The operator chooses membership.
- Pruning individual Claude sidecars. That would change the contents and reimport semantics of a retained archive.
- Background deletion, persistent plans and a new MCP mutation tool. The requested interfaces apply one reviewed operation in the foreground.

## Alternatives considered

Calling the single-session command in a loop can commit a prefix of the selection before a later failure and can prompt repeatedly. Re-running a filter at confirmation could remove a session that arrived after review. Automatic descendant deletion would broaden the existing single-ID command. The shared preview and atomic apply address each of those failure modes directly.

Accumulating marks across successive filters would let an ordinary list action include sessions the owner has already filtered away. Binding marks to the accepted result lets the owner inspect the entire explicit selection by paging through one list. Descendant expansion is the deliberate way to add targets outside that list.

Batch project reassignment and session-title editing remain follow-up work on #136. The [implementation plan](implementation.md#follow-up) records when to revisit them.
