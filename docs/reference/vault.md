# Session vault reference

Start with the [vault quick start](../../README.md#session-vault). This page covers filtering, exports, viewer controls, metadata, and cross-machine behavior. Every vault command requires `CAPY_VAULT_KEY`; the vault does not need the knowledge database or its key.

## Import and filtering

Import discovers Claude sessions under `~/.claude/projects` and Codex rollouts under `~/.codex/sessions` and `~/.codex/archived_sessions`, subject to the environment overrides below. Codex discovery accepts plain `rollout-*.jsonl` and compressed `rollout-*.jsonl.zst`. Revert variants are skipped with a warning.

```bash
capy vault import --project myproject --dry-run
capy vault import --platform codex
capy vault import --source /path/to/agent-home
capy vault import --source ~/Downloads/rollout-2026-05-01T10-00-00-019e22ba-4c15-7ae0-a903-255537a6a1b3.jsonl
capy vault import --min-size-bytes 102400 --dry-run
capy vault list --project myproject --platform codex --include-children --limit 0
capy vault search "timeout" --project myproject --role assistant --after 2026-01-01 --before 2026-02-01
```

Import is mutating by default; unchanged sessions are skipped and smaller copies do not replace fuller archives. `--source` autodetects a Claude config/projects directory or a Codex home. `import --project` filters physical discovery locations. List, search, and merge instead match a custom project label when set, otherwise the imported path.

Pass a single transcript to `--source` to import just that session. Use its original filename. A Claude `<uuid>.jsonl` includes sidecars from the adjacent `<uuid>/` directory. Claude uses the containing directory name for default restore. If you imported a copy from Downloads, use `capy vault restore <id> --output <project-directory>` to choose its destination.

Codex accepts both plain and compressed rollout files. A rollout in its original layout retains its restore path. A loose copy, such as a download, keeps the path already recorded in the vault. On first import of a loose copy, restore uses `sessions/YYYY/MM/DD/` and the local date from the filename.

Single-file import ignores the configured `vault.min_session_bytes`. Pass `--min-size-bytes` to apply a threshold for that invocation. Project and platform filters, the zero-message exclusion, and remembered deletions still apply.

`vault list` shows top-level sessions by default. Use `--include-children` to list all sessions or `--children-only` to list just independently archived children. Choose one child flag per invocation. A child can appear even when its parent is missing or filtered out.

`--name` matches the displayed title as a Unicode-aware, case-insensitive literal substring. `--project` uses literal ASCII case-insensitive substring matching. Percent, underscore, backslash, and star characters in CLI project filters are ordinary characters. The MCP selector `project: "*"` selects all projects. Project, platform, title and child filters all apply before the list limit. Plain output defaults to the newest 50 matches and the TUI defaults to unlimited. An explicit `--limit 0` is unlimited on either surface. Negative limits are invalid. Ties in end time are resolved by UUID ascending.

Transcript search accepts plain keywords, or FTS5 MATCH syntax with `--raw`. Roles are `user`, `assistant`, `tool`, and `system`; date flags take `YYYY-MM-DD`. Project/role/date filtering occurs before ranked result selection and limits. Names and project labels are not added to transcript search text.

The minimum-size setting applies only to new archives, counting uncompressed transcript and sidecar bytes. Equality qualifies; negative limits fail. Existing sessions continue updating/reindexing, and sessions excluded for size can qualify after growing. Zero-message sessions remain excluded even with the limit disabled. The selected project's configuration applies to the entire import, merge, or startup sweep, including other projects; use global configuration for a consistent policy. This is a byte-size filter, not a conversation-quality measure.

## Delete a session

`capy vault delete <id> [<id> ...]` deletes a reviewed selection in one transaction. Supply full UUIDs or unambiguous prefixes of at least eight characters. Prefixes that resolve to the same UUID select it once. If any argument is too short, missing or ambiguous, the command reports the bad arguments in input order and leaves the entire selection intact.

```bash
capy vault delete 3f8a1c2b 734d92ef
capy vault delete --with-descendants 3f8a1c2b
```

The first command selects exactly the named sessions. The second also follows archived parent links to every depth, even across project labels. A batch preview lists explicit selections first, then each added descendant with its parent and effective project. It also tells you how many direct children will stay archived. Independently archived children survive a parent deletion by default. Claude sub-agent sidecars belong to their containing archive and are removed with it.

Read the preview on stderr. Press Enter to decline, or answer `y` to confirm the entire selection. `--yes` still prints the preview. A single ID with descendants off keeps the detailed preview and short-ID success line. Multiple arguments or `--with-descendants` produce a count and the distinct committed UUIDs on stdout.

At confirmation, capy rechecks each reviewed UUID and its effective project. If either check fails, the whole deletion fails. Review again before retrying. A title edit or growing transcript still qualifies, as does a UUID removed and reimported under the same project before the check. A child archived after the preview survives.

A write failure rolls back the entire selection, including sidecars, search entries, title/project overrides and deletion records. After commit, a reporting or viewer refresh error leaves the deletion in effect. If the process stopped before reporting the outcome, inspect the vault before retrying. Original files and backups stay on disk. Deletion provides no guarantee of physical erasure.

By default the encrypted vault keeps a permanent deletion record containing only the UUID and its effective project at deletion time (custom label if set, otherwise the imported path). A manual import reports that UUID as `excluded` with reason `deleted`, even if its transcript grows or moves. The server's startup sweep counts it in the platform's excluded total. A merge can carry the deletion to other vaults. Capy has no undelete operation.

For a one-time local removal, set the following in the invocation project's `.capy.toml`. This policy applies to the entire CLI or TUI selection, including targets in other projects. Use `--project-dir` to select the configuration project explicitly.

```toml
[vault]
remember_deletions = false
```

That local delete records no pair. A later disk import or a merge from a live copy can restore the session. Changing the setting preserves existing records, and new incoming deletions are always honored. Deletions made before capy supported recording have no record to forward.

### Mark and delete in the viewer

1. Open `capy vault list --tui`. To start with only archived child sessions, add `--children-only`. Set any project, platform, name or limit flags before launch. The local `f` finder narrows those rows further.
2. Press `m` to toggle the highlighted row, `a` to mark every match across all pages, or `A` to clear all marks. Check the marked count in the list title. Paging preserves marks. Accepting a filter or refreshing the list drops marks that no longer match. Widening the list leaves those rows unmarked. Single-session actions still use the highlighted row.
3. Press `x` with at least one mark to review the selection. Descendants start off. Press `d` to toggle them and read the fresh preview. **Outside current list** identifies an added descendant absent from the entire filtered list when review opened. An unmarked child on another page is still inside that list.
4. Scroll with Up/Down or PgUp/PgDn. Before confirmation, Escape returns to the list with your marks. Lowercase `y` confirms the displayed plan. Once deletion starts, the viewer ignores Escape and further confirmations. Ctrl+C waits for the result.

If the preview becomes stale, the viewer reloads under the current filters and reports dropped marks. If that reload fails, `x` retries it. Review the surviving selection and confirm again. After a successful delete, the viewer removes the deleted rows and refreshes an active search. If the terminal misses or clips the result, the viewer reports the full outcome on exit, including any output error.

### Preview and propagate a deletion

Checkpoint the source vault, copy it to a separate writable file, then preview the same selection you intend to apply.

```bash
capy vault merge --from /path/to/copied-vault.db --project personal --dry-run
capy vault merge --from /path/to/copied-vault.db --project personal
```

`--project` matches the recorded source project as a literal, ASCII case-insensitive substring. Omit it to select all projects. A deletion recorded under `personal` removes the exact UUID even if the receiver labels its copy `work`. The receiver retains the original `personal` pair for future merges, even if it never held the archive. Other UUIDs in either project are independent.

The preview shows the full UUID, distinct live archives to remove, new UUID/project pairs to add, and selected pairs already present. Two machines can independently delete the same UUID under different labels. Receiving both records for one live archive removes **1** archive and adds **2** pairs. Repeating the merge removes **0**, adds **0**, and reports **2** already present. A failed merge can report earlier committed batches. Retry the same selection to apply the outstanding work.

**Upgrade before the first deletion.** Stop every writer attached to a shared vault, including MCP servers and scheduled imports. Upgrade capy on participating machines to a release supporting reader version 4, then restart the writers before recording or receiving a deletion. Creating the empty deletion table preserves the current reader requirement. Recording the first pair locally or through merge raises it to 4. The version check runs on open, so it cannot protect an already-attached old writer or a binary predating the check.

## Exports and restore locations

```bash
capy vault show 3f8a1c2b --format markdown
capy vault show 3f8a1c2b --format json
capy vault list --json
capy vault search "rate limiter" --json
capy vault stats --json
```

Plain `show` uses `$PAGER` when available. `--format json` emits the archived JSONL verbatim, not a metadata object; Markdown/text are rendered views. Archives and raw exports preserve secrets. Search snippets are secret-stripped; no redacted raw-export command is provided.

List/search JSON contains the displayed `title` and effective `project`, while retaining the imported `project_path`, platform, and parent identity. Stats retains raw-path groups under `projects` and adds effective-label groups under `project_groups`; empty collections are arrays. Effective-project grouping uses exact strings, so `capy` and `Capy` remain separate groups despite case-insensitive filtering. Children count once like other sessions.

Lookups accept unambiguous UUID prefixes of eight or more characters. Codex displays 12-character short IDs because shorter prefixes commonly collide; ambiguous input prints candidates.

Restore writes Claude transcripts and preserved sidecars beneath the Claude projects directory. Codex restores plain JSONL at the stored relative path under `CODEX_HOME`, even if the source was compressed; an existing compressed twin is left untouched and reported. `--output` changes the restore root. Existing files require confirmation, and unsafe absolute or parent-escaping sidecar paths are skipped.

Claude resume chooses `--dir`, then a usable recorded project path, then the current directory, with an interactive fallback if needed. Codex automatic resume rejects the command before restoring; use the manual steps in the [README](../../README.md#restore-and-resume).

## Viewer search and navigation

`list`, `search`, and `show` accept `--tui`; mutating commands such as `restore`, `resume`, `delete`, and `project` do not. Platform/project launch filters stay active during browsing and live search.

In the session list, `s` switches between roots and all sessions. From child-only mode it shows all sessions. `S` switches to child-only and back to the previous mode, returning to roots on a direct `--children-only` launch. The fixed launch filters and cap apply on each switch. Visible children group under their parents. Top-level sessions and siblings are each ordered by end time, newest first. If a parent is missing or filtered out, its visible child appears at the top level with the parent's short ID.

In a transcript, `/` finds case-sensitive literal text. Typing previews a match; Enter commits. `n` / `N` visit every occurrence, including overlaps, and wrap. Spaces and punctuation are literal; queries are single-line and limited to 256 Unicode code points.

`ctrl+f` opens a case-insensitive fuzzy line picker: `ndl` can match `needle`, with spaces still significant. Empty input browses nonempty lines. Use arrows, `ctrl+p` / `ctrl+n`, or PageUp/PageDown to select, then Enter. Accepting a fuzzy result replaces exact find, so `n` / `N` return to marker navigation.

Both modes include full collapsed tool bodies, summaries, and reconstructed diffs. A hidden match opens its detail. They search the parsed transcript, excluding raw-only metadata and content omitted by the decoder. Unopened sidecars and child sessions are outside that scope.

| Action | Result |
| --- | --- |
| Esc while editing a query | Cancel and restore the prior query, target, and reading position. |
| Esc after accepting | Clear find and restore normal rendering; a hidden detail stays open. |
| Later Esc, or `q` immediately outside query editing | Go back. Manual detail/child navigation restores the parent's search; returning from a search-opened detail clears its owner's query. |
| `]` / `[` or Tab / Shift+Tab outside query editing | Move between markers, including while exact find is active. |
| Resize or return from raw view | Preserve the selected search occurrence. |

Find uses “find · plain text” presentation in both default and glamour builds. Selected text is bracketed; `>` marks the fuzzy selection. Printable action keys type into an active query; `ctrl+c` still quits. Search is in-memory, requires no reindex, and does not save queries or excerpts.

Press `v` in list/view mode to inspect archived raw JSONL, including unknown fields and malformed records with line diagnostics. In a child, sidecar, or tool detail, it opens the corresponding containing transcript. From global search, open a result first. Scroll with `j` / `k`, pan with `h` / `l`, use `0` for the left edge and `g` / `G` for top/bottom. Esc, `q`, or `v` returns with position preserved. Terminal controls are escaped; this view works after the original session file is gone.

### Codex edit and input markers

Select a patch marker with `]` / `[` and press Enter for sorted per-file add/delete/update/move details. Paths under the recorded cwd are shortened; moves show both paths.

| Recorded evidence | Marker | Detail heading |
| --- | --- | --- |
| Completed, complete diffs | `N files changed (+A −R)` | `File changes · completed` |
| Completed, some diffs unavailable | `N files changed · diff incomplete` | `File changes · completed` |
| Completed, empty file list | `Patch completed · no file changes recorded` | `File changes · completed` |
| Completed, unreadable changes | `Patch completed · diff unavailable` | `File changes · completed` |
| Failed | `Patch failed` | `File changes · failed` |
| Declined | `Patch declined` | `File changes · declined` |
| Missing/conflicting outcome or identity | `Patch unconfirmed` | `File changes · unconfirmed` |

A single file uses singular wording. Failed/declined/unconfirmed groups show diagnostics and known paths without presenting candidate hunks as completed edits. Failure does not prove that no partial edit occurred, and completion does not verify today's filesystem.

Executable inputs over 20 lines or 2,000 bytes become `exec · input` markers with full `Tool input` details. Correlated outputs use `exec · output`; associated successful patch results use `apply_patch · output`. Code is displayed, never executed, and code/diff details bypass Markdown interpretation in both builds. Copy, find, and raw-view controls work there too.

These are display changes: reopening existing archives is enough. Raw bytes, plain/Markdown exports, and global search indexing do not change. See [display implementation](../architecture.md#tool-result-display-show-vs---tui) for event normalization and associations.

## Names, labels, and merge behavior

Names and project labels are trimmed, nonempty valid UTF-8 without control characters, limited to 120 Unicode code points. Known credential patterns are redacted before storing this metadata. Duplicates are allowed; the UUID remains the identity. CLI `--clear`, or an emptied TUI editor, restores the current imported value.

A name overrides the imported title throughout CLI, MCP results, and TUI. It does not rename the source agent's session. Labels replace paths for explicit project filtering but leave restore/resume locations and default MCP path scoping unchanged. Edits affect one UUID, not its parent or children. Reassigning a session can remove it from the current filtered list.

Titles and project assignments survive import growth, reindex, compact, rekey, and merge; deleting a session deletes its overrides. Clear operations persist as tombstones so they propagate through merge. On overlap, transcript size wins independently of metadata: the latest title edit/clear and latest project edit/clear each win, with timestamp, machine ID, and deterministic value tie-breaks. This permits a title change on one machine and project change on another to survive together.

Older vaults lacking metadata contribute no edits. Older capy binaries may omit metadata when merging from a newer vault; upgrade and rerun merge to carry it. `merge --project` now selects the source's effective project, not its rollout-location hint. The source must be writable because merge checkpoints it before reading; the destination can tolerate a concurrent server sweep. See [metadata reconciliation](../architecture.md#session-project-assignments) and [archival paths](../architecture.md#archival-paths).

## Environment and maintenance

| Variable | Purpose |
| --- | --- |
| `CAPY_VAULT_KEY` | Required vault passphrase in the actual launch environment. |
| `CAPY_VAULT_PATH` | Database path; per-invocation `--path` wins. |
| `CAPY_VAULT_SWEEP_ALL` | Any nonempty value enables startup import across all projects. |
| `CAPY_VAULT_MERGE_KEY` | Source merge key; `--key` wins, otherwise fallback is `CAPY_VAULT_KEY`. |
| `CAPY_VAULT_NO_COMPRESS` | Any nonempty value writes new blobs raw; `compact` refuses to run. Intended for debugging/benchmarking. |
| `CAPY_MACHINE_ID` | Override stable import/merge machine identity, for example in CI. |
| `CLAUDE_CONFIG_DIR` | Override Claude discovery/restore root. |
| `CODEX_HOME` | Override Codex discovery/restore home, default `~/.codex`. |

Machine identity otherwise comes from `$XDG_CONFIG_HOME/capy/machine-id` (default `~/.config/capy/machine-id`), generated if missing. Failure to persist it logs a warning and uses a derived hostname/user identity. Copying a vault does not change this machine's identity.

Run scheduled imports in an environment that supplies the vault key; setting it in an interactive shell alone may not configure a scheduler. Imports cannot recover files deleted before they were archived.

Checkpoint before copying a vault, and prefer merge to replacing another machine's archive. Stop attached processes before key rotation. `rekey` requires a new environment key different from the prompted old key and leaves an old-key backup unless `--remove-backup` is used. Neither deletion nor that flag promises secure erasure. `compact` reclaims older uncompressed storage; stop MCP first.

Storage is encrypted, compressed where useful, and has no automatic expiry. The per-row encoding and reader-version guards are described in [vault storage](../architecture.md#blob-compression--storage-encoding); use `stats` for size rather than assuming a fixed monthly growth rate. The [reader-version contract](../architecture.md#reader-version-min_reader_version) prevents unsupported older binaries from opening newer vault formats.
