# Session vault reference

Start with the [vault quick start](../../README.md#session-vault). This page covers filtering, exports, viewer controls, metadata, and cross-machine behavior. Every vault command requires `CAPY_VAULT_KEY`; the vault does not need the knowledge database or its key.

## Import and filtering

Import discovers Claude sessions under `~/.claude/projects` and Codex rollouts under `~/.codex/sessions` and `~/.codex/archived_sessions`, subject to the environment overrides below. Codex discovery accepts plain `rollout-*.jsonl` and compressed `rollout-*.jsonl.zst`. Revert variants are skipped with a warning.

```bash
capy vault import --project myproject --dry-run
capy vault import --platform codex
capy vault import --source /path/to/agent-home
capy vault import --min-size-bytes 102400 --dry-run
capy vault list --project myproject --platform codex --include-children --limit 0
capy vault search "timeout" --project myproject --role assistant --after 2026-01-01 --before 2026-02-01
```

Import is mutating by default; unchanged sessions are skipped and smaller copies do not replace fuller archives. `--source` autodetects a Claude config/projects directory or a Codex home. `import --project` filters physical discovery locations. List, search, and merge instead match a custom project label when set, otherwise the imported path.

`list` defaults to the newest 50 sessions; `--limit 0` lists all. `--name` matches the displayed title as a Unicode-aware, case-insensitive literal substring. `--project` uses literal ASCII case-insensitive substring matching. Percent, underscore, backslash, and star characters in CLI project filters are ordinary characters; the MCP `project: "*"` all-project selector is a separate convention. List filters combine before its limit.

Transcript search accepts plain keywords, or FTS5 MATCH syntax with `--raw`. Roles are `user`, `assistant`, `tool`, and `system`; date flags take `YYYY-MM-DD`. Project/role/date filtering occurs before ranked result selection and limits. Names and project labels are not added to transcript search text.

The minimum-size setting applies only to new archives, counting uncompressed transcript and sidecar bytes. Equality qualifies; negative limits fail. Existing sessions continue updating/reindexing, and excluded sessions can qualify after growing. Zero-message sessions remain excluded even with the limit disabled. The selected project's configuration applies to the entire import, merge, or startup sweep, including other projects; use global configuration for a consistent policy. This is a byte-size filter, not a conversation-quality measure.

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
