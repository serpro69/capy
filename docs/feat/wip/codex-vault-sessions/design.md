# Codex Sessions in the Vault — Design

**Status:** Draft (design complete, awaiting `/kk:review-design`)

**Created:** 2026-09-13

**Issue:** [#77 — vault: support for codex sessions](https://github.com/serpro69/capy/issues/77)

**Research:** [research.md](./research.md) — Codex storage model, rollout wire format, verified counts over 164 local rollouts, and the Claude Code coupling points in today's vault. This document settles the decisions §4 of that file lists.

**Active profile:** Go (`database/sql` / SQLite; `log/slog`)

## Problem

The vault archives, indexes, displays and restores Claude Code sessions. Every layer assumes Claude Code's layout and wire format: discovery walks `~/.claude/projects/<mangled-dir>/<uuid>.jsonl`, three independent JSONL readers (`scanner.go` for FTS, `render.go` for `show`, `transcript.go` for the TUI) each switch on Claude line types, the schema stores a Claude-specific `claude_project_dir`, and `resume` launches `claude --resume`. The original vault design listed Codex support under *Not Doing*.

A developer who splits work between Claude Code and Codex CLI therefore has half a history in the vault. Codex rollouts live under `$CODEX_HOME/sessions/YYYY/MM/DD/`, use a different envelope (`{timestamp, type, payload}`), keep tool results on their own lines, write sub-agents as separate top-level rollouts, and compact append-only. None of that is reachable by `capy vault import`, `capy_search`, or the TUI.

There is a second, pre-existing cost this feature exposes: the three Claude readers duplicate the same pass-1 logic, and the project's knowledge base records the recurring bug shape — a new line type handled in one reader is silently lost from the other two. Adding a Codex trio the same way would double that surface.

### How Might We

How might we give a developer who works across agent CLIs (Claude Code and Codex today) one durable, searchable archive of every conversation from any of them, with full archive/search/view/restore parity for Codex, ingested through one pluggable format seam so a third tool is a new decoder rather than a refactor, all without weakening the vault's existing guarantees (verbatim bytes, idempotent import, DB-driven reindex, cross-machine merge)?

## Target User

The **dual-tool solo developer**: one person using both Claude Code and Codex CLI, often on the same projects, possibly across machines, who wants one unified history and cross-tool search by default. Both platforms are first-class; defaults assume both roots may exist on disk. A Codex-only developer and a Claude-first developer who dabbles in Codex are served by the same defaults, not by separate modes.

## Success Criteria

Must-haves for the first release:

1. **Import round-trip on the real corpus.** `capy vault import` archives every rollout under `$CODEX_HOME/sessions` and `$CODEX_HOME/archived_sessions` (including a future `.jsonl.zst`) verbatim; a second run reports 0 new and 0 updated; `capy vault restore` writes the rollout back to its original relative path with a SHA-256 equal to the source. Verifiable against the 164 local rollouts.
2. **Search and index parity.** Codex human turns index as `role=user`, assistant text as `assistant`, tool outputs as `tool` with their call label; no Codex session that contains a human turn scans to zero messages across the local corpus; `capy_search` and `capy_vault_search` return Codex hits rank-merged with Claude hits.
3. **Zero Claude regression.** Existing vault tests stay green; `currentIndexVersion` is not bumped (no forced reindex of Claude rows); `ScanSession`, `RenderText`, `RenderMarkdown` and `ParseTranscript` produce byte-identical output for Claude sessions before and after the refactor; `make bench-quality` shows no regression against `master`.

Nice-to-have, deferred with a recorded follow-up (see [implementation.md § Deferred](./implementation.md#deferred-work)): `capy vault resume` launching `codex resume`.

## Constraints

Given by existing decisions and invariants:

- `raw_jsonl` is verbatim; `content_hash`, `size_bytes` and all FTS text are computed on the **uncompressed** bytes (vault v2, `encoding` column).
- Unknown line and payload types are skipped, never fatal (ADR-021).
- Schema changes go through the name-keyed vault migration runner with DDL shared between `schemaSQL` and the migration (ADR-025 D5, migration 0005 pattern).
- Encryption is mandatory; WAL checkpoint discipline is unchanged.
- The vault is the sole session store and feeds `capy_search` via RRF federation (ADR-027, ADR-028).
- User-owned names live in `vault_session_names`; platform-native rename records are not promoted (ADR-030).

Confirmed for this design:

- **Additive schema only.** New columns and tables via migration `0006`; no rename or drop of existing columns. `claude_project_dir` keeps its name. Merge feature-detects the new columns.
- **Rollout files are the only source of truth.** `state_5.sqlite`, `thread_history_1.sqlite`, `history.jsonl` and `session_index.jsonl` are never read.
- **Retrieval core untouched.** `chunker.go`, the `vault_fts` / `vault_chunks` / `vault_chunks_trigram` schema, RRF federation, the `capy_search` result shape and `currentIndexVersion` do not change. Codex plugs in above `ScanResult`.
- **No new setup or env var.** Codex sessions are discovered whenever `$CODEX_HOME` (default `~/.codex`) exists; no `CAPY_PLATFORM`, no `capy setup` step, no config key.

## Architecture Overview

The vault gains a **format seam** at exactly one layer: turning archived bytes into an ordered, format-agnostic **transcript model**. Everything above the seam (FTS scanner, `show` renderer, TUI transcript, chunker, retrieval, federation) is platform-blind; everything below it (discovery, decoders) is per-platform.

```text
disk ── Discoverer(claude-code) ─┐                          ┌─ scanner    → ScanResult → chunker → vault_fts / vault_chunks
        Discoverer(codex)       ─┴→ SessionFile → import ──┤
                                          │                 ├─ render     → `show` text / markdown
DB raw_jsonl ── platform column ──→ Decoder(platform) ─────┤
                (DetectFormat fallback)   │                 └─ transcript → TUI messages
                                    Transcript{Meta, []Entry}
```

Two `Decoder` implementations live in `internal/vault`:

- **Claude decoder** — extracted from the pass-1 logic the three readers currently duplicate: type inference from `message.role`, progressive-snapshot merging by `message.id`, `queued_command` attachment normalization, `pr-link` and `away_summary` system text, `<system-reminder>` / command-tag noise stripping, `toolUseResult.structuredPatch` diffs.
- **Codex decoder** — new; see [The Codex Decoder](#the-codex-decoder).

A decoder is **pure and pre-policy**: bytes in, entries out, no secret stripping, no truncation, no exclusion policy, no collapse decision. Those stay in the consumers because the scanner sanitizes and bounds while `show` and the TUI deliberately render verbatim.

Dispatch happens at two points. At **import**, the discoverer knows the platform from the root it walked and import stamps it into the new `platform` column. At **read time** (reindex, merge, show, TUI, restore, resume) the store row supplies the platform, so no blob sniffing sits on the hot path. `DetectFormat(firstLine)` is the fallback for rows without a usable value (a pre-0006 row is Claude by construction, but it is sniffed rather than assumed so a wrong value fails loud).

A third agent CLI later means one `Discoverer` and one `Decoder`, plus a platform constant.

## The Transcript Model

A new file (`transcript_model.go`) defines the seam. A decoder returns a `Transcript`: session-level `Meta` plus an ordered `[]Entry`.

### Meta

| Field | Meaning | Claude source | Codex source |
|---|---|---|---|
| `Platform` | `claude-code` \| `codex` | constant | constant |
| `ExplicitTitle` | platform-recorded title | last `ai-title` | none (empty) |
| `TitleFallback` | decoder-chosen fallback title text, untruncated, unsanitized | first `user` entry whose content is a plain string and does not start with `<` (today's rule, preserved for byte-identical output) | first human entry text |
| `CWD` | project path | first `user` line with `cwd` | `session_meta.cwd` |
| `Branch` | git branch | first `user` line with `gitBranch` | `session_meta.git.branch` |
| `StartTime`, `EndTime` | first / last timestamped line | same | same (every line is timestamped) |
| `ParentUUID` | parent session for a child rollout | empty | lifted `parent_thread_id`, else `source.subagent.thread_spawn.parent_thread_id` |
| `Source` | platform's session origin | empty | `cli` \| `exec` \| `vscode` \| `mcp` \| `subagent` \| other, as a string |

The scanner derives the stored title as `sanitize(ExplicitTitle)`, else `truncate(sanitize(TitleFallback), 120)` — exactly today's ordering (sanitize before truncate).

### Entry

Every entry carries `Kind`, `LineIndex` (physical 0-based line; the canonical **first** line for a merged Claude snapshot) and `Timestamp`. Kind-specific payload:

- **Human** — `Text` with format noise already stripped; `Queued` (Claude A2).
- **Assistant** — `Text` and `[]ToolCall`. A `ToolCall` has `ID`, `Name`, `Summary` (platform label: `"Bash <cmd>"`, `"exec_command <cmd>"`, `"apply_patch <files>"`, `"Agent <prompt>"`, bare MCP name), `Input` (raw JSON, for future generic rendering per ADR-025 § Deferred) and an optional `Launch{Label, ChildUUID}` marking a sub-agent spawn.
- **ToolResult** — `CallID`, `CallName`, `CallSummary` (resolved by the decoder), `Body` (verbatim, untruncated) and optional `Diff{Text, Added, Removed}` — unified-diff text. The Claude decoder fills `Diff` from `toolUseResult.structuredPatch`; the Codex decoder fills it from the matching `apply_patch` call input.
- **System** — `Text` and `SearchOnly`. `SearchOnly` preserves a deliberate asymmetry that exists today: the scanner indexes generic `attachment` message content (`attachmentText`) while `show` and the TUI do not display it. The flag keeps the refactor byte-identical; unifying the three surfaces is a separate, recorded decision (see [Open Questions](#open-questions)).

Decoders resolve call-to-result correlation themselves, so consumers receive the call's `Name` and `Summary` on every result without rebuilding the id map.

### Consumer contracts

The three consumers become thin loops over `[]Entry`:

- **Scanner** (`ScanTranscript`): a Human entry starts a new turn (the shared heuristic; explicit Codex turn ids are not used); a ToolResult whose `CallName` is in `ftsExcludedResult` is dropped; all other text is `sanitize.StripSecrets`-ed, ToolResult bodies are prefixed with `CallSummary` then head/tail bounded to `maxToolResultChars`; Assistant rows carry `ToolNames` for the chunker title. `MessageCount` counts user and assistant rows. Output for Claude sessions is byte-identical to today.
- **Render** (`show`): verbatim bodies; ToolResults for `excludedResultTools` collapse to the one-line marker; every ToolCall (including launches) renders as an arrow line from `Summary`; the assistant heading is platform-aware (`Claude` / `Codex`).
- **Transcript** (TUI): adds `SourceLine`, the collapse thresholds, `Diff`, and launch markers from `ToolCall.Launch`. A marker with `ChildUUID` is `Openable` and opens a **session** by UUID; Claude markers keep today's count-based mapping onto sidecar subagent ids.

Public signatures gain a platform parameter: `ScanSession(platform, r)`, `RenderText(platform, raw)`, `RenderMarkdown(platform, raw)`, `ParseTranscript(platform, raw, subagentIDs)`. `ScanSubagent` stays Claude-only (sidecars are a Claude concept).

## Format Identification

`Platform` is a string type with two constants, `claude-code` and `codex`, validated in Go (`ParsePlatform`) against a closed set. A SQL `CHECK` constraint is deliberately **not** used: a third platform must be a constant, not a migration.

`DetectFormat(firstLine []byte) (Platform, error)`:

- Codex when the line parses as JSON and its top-level `type` is `session_meta` (always the first line of a rollout; verified on 164/164 local files).
- Claude when the line parses and carries Claude-shaped keys (`type` in the known set, or `message` / `sessionId` / `uuid` present).
- Otherwise an error. Callers surface it (import: `StatusError`; reindex/merge: per-session error) rather than defaulting to Claude.

## The Codex Decoder

`codex_decoder.go` (with wire types in `codex_types.go`) reads a rollout through the shared `scanLines` and switches on the envelope `type`, then `payload.type`. Every physical line advances `LineIndex`, so anchors stay physical; the envelope `ordinal` is ignored.

### Metadata

Line 0 (`session_meta`) supplies `CWD`, `Branch` (`git.branch`, absent for non-git cwd), `Source`, and `ParentUUID`. `StartTime` / `EndTime` are the first and last envelope timestamps. `cli_version` and `history_mode` are read only for logging.

### Human turns come from events

`response_item` messages with `role == "user"` are dominated by injected context (`<environment_context>`, AGENTS.md bodies, `<skill>`, `<turn_aborted>`, `<subagent_notification>`). The reliable human-turn source is the event stream, present across the whole 0.124 → 0.154 range:

| History mode | Source of the human prompt |
|---|---|
| legacy (< 0.147, `history_mode` absent or `legacy`) | `event_msg` / `user_message` → `payload.message` |
| paginated (≥ 0.147) | `event_msg` / `item_completed` with `item.type == "UserMessage"` → join `item.content[].text` |

Both paths are always attempted; `history_mode` does not gate them. Only when a whole file yields **zero** human entries from events does the decoder fall back to `response_item` user messages, applying a noise filter: drop `developer` role entirely, and drop `input_text` whose trimmed text starts with `<environment_context>`, `<skill>`, `<turn_aborted>`, `<subagent_notification>`, `<INSTRUCTIONS>`, or `# AGENTS.md instructions`.

Legacy `agent_message` events and paginated `AgentMessage`, `CommandExecution`, `McpToolCall`, `FileChange`, `WebSearch` items are skipped: they duplicate `response_item` content byte-for-byte (verified).

### Assistant entries and tool calls

`response_item` / `message` with `role == "assistant"` becomes an Assistant entry (`output_text` parts joined with newlines; `phase` ignored). There are no progressive snapshots to merge.

`function_call`, `custom_tool_call` and `web_search_call` attach as `ToolCall`s to the most recent Assistant entry that has not yet been followed by a ToolResult or Human entry; otherwise they open a text-less Assistant entry at their own line. Summaries:

| Codex call | `Name` | `Summary` |
|---|---|---|
| `function_call` `exec_command` | `exec_command` | `exec_command <cmd>` — `cmd` parsed from the **stringified** `arguments` JSON |
| `custom_tool_call` `exec` | `exec` | `exec <first line of input>` |
| `custom_tool_call` `apply_patch` | `apply_patch` | `apply_patch <files>` — paths from `*** Add File:` / `*** Update File:` / `*** Delete File:` lines |
| `function_call` `spawn_agent` | `spawn_agent` | `spawn_agent <message, bounded 200 runes>`; also `Launch{Label, ChildUUID}` |
| `web_search_call` | `web_search` | `web_search <query>` |
| any other `function_call` (MCP tools appear as bare names, e.g. `capy_search`) | as given | bare name (generic input rendering stays deferred per ADR-025) |

`Launch.ChildUUID` is resolved from a later `event_msg` / `collab_agent_spawn_end` (`call_id` → `new_thread_id`, legacy) or `item_completed` with `item.type == "SubAgentActivity"` (`item.id == call_id` → `agent_thread_id`, paginated). Unresolved launches keep an empty `ChildUUID` and stay visible but non-openable.

### Tool results

`function_call_output` and `custom_tool_call_output` each become a ToolResult entry at their own line, correlated by `call_id`. Body normalization:

- `function_call_output.output` is a string or a content array (`input_text` parts joined; image/audio parts skipped).
- The `exec_command` wrapper header (`Chunk ID: …`, `Wall time: …`, `Original token count: …`, `Output:`) is stripped; the `Process exited with code N` line is **kept** as the first body line because an exit code is search signal; the text after `Output:` follows.
- `custom_tool_call_output.output` is a JSON string decoded once more; `.output` is the body. Its `metadata.exit_code`, when present, is prepended as `Process exited with code N` for parity with `exec_command`.
- An `apply_patch` result gets `Diff` built from its call's patch text by a new helper beside `diff.go` that converts Codex's `*** Begin Patch` format to unified-diff text with add/remove counts. On a conversion failure the result keeps its plain body and no `Diff`.

### Skipped

`reasoning`, `compacted` (its `replacement_history` duplicates earlier lines), `turn_context`, `world_state`, `token_usage_record`, `token_count`, `task_started`, `task_complete`, `turn_aborted`, `thread_settings_applied`, `inter_agent_communication*`, `security_risk_score`, `retained_context`, `realtime_item`, `tool_search_*`, `agent_message` response items, and any unknown type (ADR-021). Unknown envelope and payload types are collected per file and logged once at debug level as the format-drift fingerprint.

## Discovery and Import

### Discoverer

A `Discoverer` interface (`Discover(root) ([]SessionFile, error)`) with two implementations:

- **Claude** wraps today's `DiscoverSessions` / `detectProjectDirs` / `discoverProject` logic unchanged.
- **Codex** walks `$CODEX_HOME/sessions` and `$CODEX_HOME/archived_sessions` (new `config.CodexHome()`, honoring `CODEX_HOME`, default `~/.codex`) for regular files (symlinks skipped, as today) whose name parses as `rollout-<YYYY-MM-DDThh-mm-ss>-<uuid>[_<rollout_id>].jsonl[.zst]`. A filename that does not parse is skipped with a warning.

`SessionFile` grows: `Platform`; `RelativePath` (Codex: the rollout path relative to `$CODEX_HOME`, normalized to its plain `.jsonl` name — the only way to restore a **local-time** filename faithfully, since the UTC metadata cannot rebuild it); `ProjectPath` hint (Codex: `cwd` from a bounded first-line read, so project filtering needs no full scan); `Compressed`. For Claude, `ProjectDir` keeps its mangled-dir meaning and `RelativePath` is unused.

### Import

`Import` reads a `.zst` rollout through the existing zstd decoder (`codec.go`) before anything else, so `raw_jsonl`, `content_hash` and `size_bytes` are always the plain JSONL bytes. Consequences, all falling out of the existing idempotency rule:

- An archived copy (`archived_sessions/…`) or a later compressed copy of the same thread hashes equal → `StatusSkipped`.
- A revert rollout (`_<rollout_id>` suffix, same thread uuid) competes under larger-wins; the stored `claude_project_dir` is the winning file's relative path. Unobserved locally — see [Assumptions](#assumptions).

`buildRecord` selects the decoder by `sf.Platform` and fills `Session.Platform`, `Session.ParentUUID`, `Session.ClaudeProjectDir` (Claude: mangled dir; Codex: `RelativePath`) and `Session.ProjectPath` (`Meta.CWD`, else the discovery hint, else today's unmangle fallback). Codex sessions have no sidecars; `vault_files` stays empty for them. The zero-message exclusion is unchanged: a Codex child rollout carries one human turn (the spawn prompt) and is archived.

`capy vault import` defaults to **every platform root that exists on disk**. `--source <dir>` autodetects layout: a `projects/` child or `*.jsonl` files means Claude (today's rules); a `sessions/` or `archived_sessions/` child holding `rollout-*` files means Codex. `--project` matches either the mangled dir or the `ProjectPath` hint (substring, as today). A new `--platform claude-code|codex` restricts the run.

### Server startup sweep

`vaultSweep` sweeps both platforms for the current project: the Claude dir as today, plus Codex rollouts whose first-line `cwd` equals the server's project dir (both `filepath.Abs` + `EvalSymlinks` + `Clean`). `CAPY_VAULT_SWEEP_ALL` widens both. A missing `$CODEX_HOME` is a debug line, not a warning. Discovery cost for Codex is one bounded first-line read per rollout.

## Storage Model

Migration `0006_platform` (DDL shared with `schemaSQL`, applied through the existing name-keyed runner with the read-only fast path):

| Change | Contract |
|---|---|
| `vault_sessions.platform TEXT NOT NULL DEFAULT 'claude-code'` | Validated in Go against the closed constant set. Existing rows read as Claude. |
| `vault_sessions.parent_uuid TEXT` | Nullable. **No foreign key**: a child can be imported before its parent, and deleting a parent must not cascade or fail. Empty string in Go == NULL. |
| `CREATE INDEX idx_sessions_parent ON vault_sessions(parent_uuid)` | Child lookups (`Children(parentUUID)`) and the `parent_uuid IS NULL` list default. |

`claude_project_dir` keeps its name and `NOT NULL` and gains a documented generalized meaning: **the platform's location hint** — the mangled project dir for Claude, the relative rollout path under `$CODEX_HOME` for Codex. `sessionMetaColumns`, the insert and update statements, `GetSession`, `ListSessions`, `Search`, `SearchChunks` and the merge reader carry the two new columns. The schema and query plan require normal human review before the implementation merges (Go profile, database checklist).

`min_reader_version` is **not** bumped. The blob encoding is unchanged; an older binary reading a 0006 vault treats a Codex row as Claude and renders it empty, but corrupts nothing. This is a documented known gap, not a compatibility break.

## Sub-agent Model

Codex writes each spawned agent as its own top-level rollout (13 of 164 local files). The vault mirrors Codex's own model on the backend and its own presentation on the surface:

- **Backend:** each child is its own `vault_sessions` row, byte-verbatim, with `parent_uuid` set from its `session_meta`. It is discovered, hashed, indexed and merged like any session.
- **Presentation parity with Codex:** Codex's agents overview and resume picker hide threads that have a `parent_thread_id` or a `SubAgent(ThreadSpawn)` source and reach them from the parent via the `/subagents` picker in spawn order (verified in `codex-rs/tui/src/app/agents_overview_threads.rs` and `codex-rs/rollout/src/lib.rs` `INTERACTIVE_SESSION_SOURCES`). The vault therefore hides children from `list` and the TUI list by default (`ListOptions.IncludeChildren`, `--include-children`), shows a parent's children in `show` and opens them from the parent's launch markers in the TUI in spawn order.
- **Search:** Codex has no transcript search, so no parity constraint applies. Children are searchable as their own hits; `SearchResult` carries `Platform` and `ParentUUID` so a hit can be labeled as a child.
- **Delete:** deletes only the addressed session and warns when children exist. Codex's `/delete` cascades to descendants; capy does not (see [Not Doing](#not-doing)).

## Read Surfaces

### CLI

- `vault list`: platform column; `--platform`; `--include-children` (children show their parent's short id); JSON adds `platform`, `parent_uuid`.
- `vault show`: header prints the platform; a child names its parent; a parent lists its children with short ids. Assistant heading `Codex` / `🤖 Codex` for Codex rows.
- `vault search`: each hit is tagged with its platform; child hits are marked.
- `vault stats`: per-platform breakdown (`VaultStats.ByPlatform`) and a child count.
- `import` / `merge` summaries report per-platform counts.
- `vault restore`: Codex default root is `$CODEX_HOME`; the main file is written at `claude_project_dir` (the relative path), always as plain `.jsonl`. `--output` overrides the root as today. The existing path-safety validation (`validateSidecarRel`, `safeChildPath`, symlink re-check) applies to the relative path.
- `vault resume`: a Codex session **fails loudly** with an actionable message naming `capy vault restore` and `codex resume <uuid>`; it must never launch `claude --resume` on a Codex UUID. Real integration is deferred.
- `vault delete`: warns about children, no cascade.

### Identity and display

`minUUIDPrefix` stays 8 (an ambiguous prefix is already an error path with candidates). Short-id **display** becomes platform-aware: 12 characters for Codex rows in `list`, `show`, search output, ambiguous-candidate listings and the TUI, because UUIDv7 prefixes collide at 8 (22 of 164 local files) and not at 12.

### TUI

- List: hides children by default; a toggle key, chosen at implementation time to avoid `f`, `e`, `c`, `r`, `R`, `/`, `]`, `[`, `n`, includes them.
- Viewer: role label and header are platform-aware (`Codex · child of <parent short id>`). A launch marker with `ChildUUID` opens the child **session** as a new open-target kind beside the existing subagent (`openSubagent`) and inline-content (`openInlineContent`) targets, following the documented extension points (`renderTranscript`'s marker branch, `markerRowFor`, `openFocusedMarker`'s role switch); `esc` / `q` returns to the parent. Claude markers are unchanged.

### MCP

- `capy_search` / `capy_vault_search` result metadata adds `platform` (and `parent_uuid` when set); the `session:<uuid>` tag is unchanged (`formatVaultHit`).
- `capy_stats`: the vault section adds per-platform counts.
- `capy_doctor` / `capy doctor`: a new vault check reports which platform roots exist on disk and how many sessions of each are archived, so a dual-tool user can see that Codex is being swept.

## Reindex, Merge, Restore

- **Reindex** passes each row's stored platform to `scanSessionAndSubagents`. An unrecognized value falls back to `DetectFormat` on the blob's first line; if that fails too, the session is recorded as an error, never scanned as Claude by default.
- **Merge** feature-detects `platform` and `parent_uuid` on the source (absent → Claude, no parent, confirmed by sniffing the first line), carries them verbatim, and rebuilds FTS with the destination's decoder for that platform. Its `--project` filter matches `claude_project_dir` **or** `project_path`.
- **Restore** branches on platform (above). Claude is unchanged.

## Observability

- Every import, reindex and merge log line gains a `platform` attribute; per-platform counts appear in the CLI summaries and the sweep's info line.
- The Codex decoder emits one **debug** line per file listing unseen envelope/payload types (format-drift fingerprint), and one **warning** with `cli_version` and `history_mode` when a file has `task_started` events but yielded zero human entries — the Codex analog of ADR-021's zero-turns signal.
- A missing `$CODEX_HOME` is debug; an unparseable filename or first line is a warning plus skip.
- Errors are logged **or** returned, never both (Go profile, observability checklist). No transcript text is logged.

## Failure, Concurrency, Security

All new reads and writes use context-aware, parameterized `database/sql` calls; multi-statement writes stay inside the existing `BeginImmediateContext` discipline; NULL columns map to `sql.NullString`. The decoder never panics on malformed input: a malformed line is skipped (logged), an oversize line advances `LineIndex` without content (existing `scanLines` contract). Restore path safety is unchanged and now also covers the Codex relative path. Secret stripping applies to Codex FTS rows and titles exactly as to Claude. The sweep's concurrency posture (busy-timeout absorption, cooperative cancellation) is unchanged.

## Verification Strategy

Full detail in [implementation.md § Verification](./implementation.md#verification-and-gates). In summary:

1. **Refactor gate.** Committed golden outputs for every existing fixture across the four public readers, plus an env-gated real-corpus digest baseline over `~/.claude/projects` (run on `master`, compare on the branch). The refactor slice ships only when both are unchanged.
2. **Codex fixtures** for both history modes, a parent/child pair, `apply_patch`, the `exec_command` header, `.jsonl.zst`, the filename parser with `_<rollout_id>`, and identical content under `sessions/` and `archived_sessions/`.
3. **Real-data canary** over `~/.codex/sessions` (skipped when absent) asserting [Assumptions](#assumptions) 1–3 and the zero-human-turns warning never fires.
4. **Migration and merge matrix**: pre-0006 vault open; merge from a pre-0006 source; merge of Codex rows; dry run.
5. **End to end**: import → search (per-line, chunk, federated) → show → restore with SHA-256 equality; cwd-scoped sweep.
6. `make test-race`; `make bench-quality` and `make bench-compare BASE=master` (search result columns change).

## Assumptions

Each is a testable bet the design depends on.

1. `session_meta` is always line 0 of a Codex rollout, so `DetectFormat` reads one line. *Test:* canary asserts it over the local corpus.
2. Every Codex session with a human prompt carries it as `event_msg/user_message` (legacy) or `item_completed.UserMessage` (paginated). *Test:* canary asserts zero files where events yield nothing but the `response_item` fallback yields text.
3. Every `function_call_output.call_id` / `custom_tool_call_output.call_id` matches an earlier call in the same file. *Test:* canary counts unmatched ids and asserts zero.
4. The shared-model refactor is byte-identical for Claude across `ScanSession`, `RenderText`, `RenderMarkdown` and `ParseTranscript`. *Test:* goldens plus the real-corpus digest baseline.
5. Revert rollouts (`_<rollout_id>`) share the thread uuid and are correctly handled by larger-wins replacement. *Zero local samples:* only the filename parser is testable; the decoder must not fail on them. Flagged for verification when a sample appears.
6. `.jsonl.zst` rollouts are plain zstd frames readable by `klauspost/compress/zstd`. *Test:* a fixture compressed with the vault's own encoder round-trips through discovery and import.
7. Hashing decompressed bytes makes an archived or compressed copy of the same thread hash-equal. *Test:* fixture with identical content under both roots reports one `new` and one `skipped`.
8. Storing the original relative path makes restore path-preserving with no timezone arithmetic. *Test:* restored path equals the discovered path; bytes equal.
9. Migration 0006 is additive: pre-existing rows read `platform = 'claude-code'`, `parent_uuid = NULL`, and every existing test passes against a migrated legacy vault. *Test:* migration test on a pre-0006 fixture vault.
10. Comparing the server's project dir to `session_meta.cwd` by cleaned, symlink-resolved equality scopes the sweep correctly for the common case. *Test:* sweep integration test with a symlinked project dir.

## Not Doing

Genuine scope decisions, not deferred work:

- **Promoting Codex `/rename` names** (`session_index.jsonl`) into `vault_session_names`: mirrors ADR-030's decision not to promote Claude's `custom-title`, and the sources-of-truth constraint.
- **Reading Codex state** (`state_5.sqlite`, `thread_history_1.sqlite`, `history.jsonl`): live, version-suffixed, derived from rollouts.
- **Indexing** `reasoning` summaries, `world_state`, `token_usage_record`, `turn_context`, or `compacted.replacement_history`: noise or duplicates.
- **Per-platform `index_version`**: over-engineering; a Codex-only scanner change reindexes the whole vault, accepted per ADR-025.
- **Cascading child deletion** on parent delete: Codex's own semantics; capy deletes only the addressed row and warns.
- **Generic MCP tool-input rendering** in summaries: stays deferred per ADR-025 § Deferred.
- **A `/subagents`-style cycling picker** in the TUI: existing marker navigation covers open-from-parent.
- **PreCompact-style archival for Codex**: Codex compaction is append-only; nothing is lost.
- **A SQL `CHECK` on `platform`**: a third platform must not require a migration.

## Deferred (recorded follow-ups)

- **`capy vault resume` for Codex** — launch `codex resume <uuid>` with `-C <dir>` following the existing cwd fallback chain, after manually verifying Codex's DB-less filesystem fallback resumes a restored rollout. Tracked in [implementation.md § Deferred](./implementation.md#deferred-work).
- **Unifying the `SearchOnly` attachment asymmetry** across the three surfaces — a deliberate behavior change to decide separately once the refactor has landed.

## Rejected Alternatives

- **Parallel readers (Codex scanner/render/transcript trio behind sniff-and-dispatch).** Fastest to ship and zero Claude edits, but it institutionalizes the recorded parser-drift bug at twice the surface: six readers to keep in sync, every future Codex format change fixed in three places.
- **Translate Codex to Claude-shaped lines on read.** Zero reader changes, but translated lines no longer map to physical `line_index` anchors (breaking search-to-view jumps), and Codex semantics with no Claude equivalent (tool outputs as separate lines, `apply_patch` diff in the call input, explicit turn ids) are lost behind a leaky shim.
- **Fold Codex children into the parent's `vault_files` as `subagents/agent-<child>.jsonl`.** Reuses the sidecar open path and chunk tagging, but the same bytes are also a standalone discovery hit (dedup), a child can arrive before its parent (ordering), and it diverges from Codex's own first-class thread model.
- **Sniff-only format detection, no `platform` column.** Zero schema change, but every dispatch (list rendering, reindex, merge, restore) would need a blob read, and `list --platform` would be impossible without a scan.
- **Rename `claude_project_dir`.** Cleaner name, but a wide refactor across store, merge, restore and tests for no behavior gain; violates the additive-schema constraint.
- **`ScanSession` derives the title fallback itself.** Simpler contract, but today's rule (plain-string user content only, not text blocks) is Claude-specific; moving it into the decoder as `Meta.TitleFallback` is what keeps the refactor byte-identical.

## Open Questions

Carried from [research.md § 6](./research.md#6-open-questions--unverified-assumptions), narrowed to what still matters:

1. Does `codex resume <uuid>` find a rollout restored into `sessions/YYYY/MM/DD/` when `state_5.sqlite` has no row for it? Gates the deferred resume follow-up only.
2. Revert rollout shape and semantics — zero local samples (Assumption 5).
3. Whether the `SearchOnly` attachment asymmetry should be unified (display attachment text) — decide after the refactor lands.
4. Windows `CODEX_HOME` path handling — the relative-path design avoids timezone math; path separators are normalized to slashes in storage as `vault_files.relative_path` already is.

## References

- [research.md](./research.md) — evidence base, counts, redacted sample lines (fixture seeds), reproduction commands
- [Vault architecture](../../architecture.md#vault)
- [Original vault design](../../done/vault/design.md) — HMW, target user, schema; its *Not Doing* entry for Codex is superseded by this document
- [ADR-021](../../adr/021-session-jsonl-format-resilience.md) — silent degradation with observability
- [ADR-025](../../adr/025-vault-index-version-and-reindex.md) — `index_version`, DB-driven reindex, deferred generic input rendering
- [ADR-027](../../adr/027-vault-is-sole-session-store.md), [ADR-028](../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md) — vault as sole session store; federation
- [ADR-030](../../adr/030-vault-session-names-and-latest-wins-merge.md) — names table; platform rename records not promoted
- Codex sources (`openai/codex` `main`): `codex-rs/rollout/src/{list,lib,policy,recorder,rollout_file_name,compression}.rs`, `codex-rs/protocol/src/{protocol,models,items}.rs`, `codex-rs/tui/src/app/{agents_overview_threads,agent_navigation}.rs`
- [Issue #77](https://github.com/serpro69/capy/issues/77)
