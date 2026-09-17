# Codex Sessions in the Vault — Design

**Status:** Implemented (2026-09-16, branch `feat/vault_codex`; Tasks 1–14 in [tasks.md](./tasks.md), decisions recorded in [ADR-031](../../../adr/031-transcript-model-seam-and-multi-platform-vault.md)). Design revised 2026-09-13 after four independent design reviews and a second round of two reviews plus a code-level self-review (corroboration and per-finding evidence in [design-triage.md](./design-triage.md)). Where the implementation deviated from this text, the deviation is recorded per subtask in tasks.md and reflected in the section it touches; the "*implemented as*" notes below mark the corpus-driven ones

**Created:** 2026-09-13

**Issue:** [#77 — vault: support for codex sessions](https://github.com/serpro69/capy/issues/77)

**Research:** [research.md](./research.md) — Codex storage model, rollout wire format, verified counts over the local rollouts (164 at research time, 168 at design revision), and the Claude Code coupling points in today's vault. This document settles the decisions §4 of that file lists.

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

1. **Import round-trip on the real corpus.** `capy vault import` archives every base rollout under `$CODEX_HOME/sessions` and `$CODEX_HOME/archived_sessions` (including a future `.jsonl.zst`) that yields at least one message, verbatim as decompressed JSONL. The existing zero-message exclusion applies unchanged: a rollout with no human turn and no assistant entry reports `excluded`, exactly like an empty Claude session (23 of the 168 local rollouts are such shells — a session aborted at startup, ten lines long, one `task_started` and one `turn_aborted`). Revert variants (`_<rollout_id>` files) are skipped with a warning in v1 (see [Import](#import)); a second run reports 0 new and 0 updated; `capy vault restore` writes a rollout back to its discovered relative path minus any `.zst` suffix, and the restored bytes are **byte-identical** to the decompressed input. (`content_hash` is *not* a plain SHA-256 of the file: `computeContentHash` digests a length-prefixed `key||content` framing keyed `<uuid>.jsonl`, so the round-trip gate compares bytes, or recomputes that framed digest over the restored bytes.) Verifiable against the 168 local rollouts.
2. **Search and index parity.** Codex human turns index as `role=user`, assistant text as `assistant`, tool outputs as `tool` with their call label; no Codex session that contains a human turn scans to zero messages across the local corpus; `capy_search` and `capy_vault_search` return Codex hits rank-merged with Claude hits.
3. **Zero Claude regression.** Existing vault tests stay green; `currentIndexVersion` is not bumped (no forced reindex of Claude rows); `ScanSession`, `RenderText`, `RenderMarkdown` and `ParseTranscript` produce byte-identical output for Claude sessions before and after the refactor; `make bench-quality` shows no regression against `master`.

Nice-to-have, deferred with a recorded follow-up (see [implementation.md § Deferred](./implementation.md#deferred-work)): `capy vault resume` launching `codex resume`.

## Constraints

Given by existing decisions and invariants:

- `raw_jsonl` is verbatim; `content_hash`, `size_bytes` and all FTS text are computed on the **uncompressed** bytes (vault v2, `encoding` column).
- Unknown line and payload types are skipped, never fatal (ADR-021).
- Schema changes go through the name-keyed vault migration runner with DDL shared between `schemaSQL` and the migration (ADR-025 D5, migration 0005 pattern). `openDB` executes `schemaSQL` **before** `migrateVault`, so `schemaSQL` may only reference columns that exist in every historical schema.
- Encryption is mandatory; WAL checkpoint discipline is unchanged.
- The vault is the sole session store and feeds `capy_search` via RRF federation (ADR-027, ADR-028).
- User-owned names live in `vault_session_names`; platform-native rename records are not promoted (ADR-030).

Confirmed for this design:

- **Additive schema only.** New columns via migration `0006`; no rename or drop of existing columns. `claude_project_dir` keeps its name. Merge feature-detects the new columns.
- **Rollout files are the only source of truth.** `state_5.sqlite`, `thread_history_1.sqlite`, `history.jsonl` and `session_index.jsonl` are never read.
- **Retrieval core untouched.** `chunker.go`, the `vault_fts` / `vault_chunks` / `vault_chunks_trigram` schema, RRF federation, the `session:<uuid>` hit tagging and `currentIndexVersion` do not change. Codex plugs in above `ScanResult`. The *formatted* search-hit meta line and `SearchResult` gain `platform` / `parent_uuid` (see [MCP](#mcp)); ranking and plumbing do not.
- **No new setup or env var.** Codex sessions are discovered whenever `$CODEX_HOME` (default `~/.codex`) exists; no `CAPY_PLATFORM`, no `capy setup` step, no config key. **Recorded trade-off:** this also means there is no opt-out for the Codex sweep, and the first Codex row irreversibly raises the reader marker to 3 (see [Reader version](#reader-version)). Vault v2 had an escape hatch for the analogous zstd bump (`CAPY_VAULT_NO_COMPRESS`); this design deliberately does not add one — a dual-tool user who must keep an older binary on another machine upgrades that binary before merging, exactly as the v2 bump required. Revisit if a real staggered-rollout need appears (see [Not Doing](#not-doing)).

## Architecture Overview

The vault gains a **format seam** at exactly one layer: turning archived bytes into an ordered, format-agnostic **transcript model**. Everything above the seam (FTS scanner, `show` renderer, TUI transcript, chunker, retrieval, federation) is platform-blind; everything below it (discovery, decoders) is per-platform.

```text
disk ── Discoverer(claude-code) ─┐                          ┌─ scanner    → ScanResult → chunker → vault_fts / vault_chunks
        Discoverer(codex)       ─┴→ SessionFile → import ──┤
                                          │                 ├─ render     → `show` text / markdown
DB raw_jsonl ── platform column ──→ Decoder(platform) ─────┤
                (DetectFormat only for   │                  └─ transcript → TUI messages
                 an unrecognized value)  Transcript{Meta, []Entry}
```

Two `Decoder` implementations live in `internal/vault`:

- **Claude decoder** — extracted from the pass-1 logic the three readers currently duplicate: type inference from `message.role`, progressive-snapshot merging by `message.id`, `queued_command` attachment normalization, `pr-link` and `away_summary` system text, `<system-reminder>` / command-tag noise stripping, `toolUseResult.structuredPatch` diffs.
- **Codex decoder** — new; see [The Codex Decoder](#the-codex-decoder).

A decoder is **pure and pre-policy**: bytes in, entries out, no secret stripping, no truncation, no exclusion policy, no collapse decision. Those stay in the consumers because the scanner sanitizes and bounds while `show` and the TUI deliberately render verbatim.

Dispatch happens at two points. At **import**, the discoverer knows the platform from the root it walked and import stamps it into the new `platform` column; import never sniffs. At **read time** (reindex, merge, show, TUI, restore, resume) the store row supplies the platform, so no blob sniffing sits on the hot path. `DetectFormat(firstLine)` is used in exactly one situation: a `platform` value that is **present but unrecognized** — which, under the rule that **every new platform constant bumps `min_reader_version`** (see [Reader version](#reader-version)), can only be a corrupted or hand-edited row, never a legitimately newer vault (an older binary refuses that vault before reading any row). An **absent** column — a pre-0006 vault or merge source — is Claude by construction and is never sniffed (see [Format Identification](#format-identification) for why sniffing legacy rows would be harmful).

A third agent CLI later means one `Discoverer` and one `Decoder`, plus a platform constant.

## The Transcript Model

A new file (`transcript_model.go`) defines the seam. A decoder returns a `Transcript`: session-level `Meta` plus an ordered `[]Entry`.

### Meta

| Field | Meaning | Claude source | Codex source |
|---|---|---|---|
| `Platform` | `claude-code` \| `codex` | constant | constant |
| `PlatformID` | the platform's own id for this session, as recorded **inside** the file (informational; the vault row key is always the filename uuid — see [Import](#import)) | empty (not extracted today; keeps the refactor byte-identical) | `session_meta.id` |
| `ExplicitTitle` | platform-recorded title | last `ai-title` | none (empty) |
| `TitleFallback` | decoder-chosen fallback title text, untruncated, unsanitized | first `user` entry whose content is a plain string and does not start with `<` (today's rule, preserved for byte-identical output) | first human entry text; for a subagent-sourced rollout with no human turn (see [Human turns](#human-turns-come-from-events)), `agent_nickname · agent_role` from `session_meta`, else `agent_path` |
| `CWD` | project path | first `user` line with `cwd` | `session_meta.cwd` |
| `Branch` | git branch | first `user` line with `gitBranch` | `session_meta.git.branch` |
| `StartTime` | session start | first timestamped line | `session_meta.payload.timestamp` (the creation time Codex derives the filename from), else line 0's envelope timestamp. The envelope timestamp of line 0 is written when the first turn begins, not at creation: it trails the payload time by up to a minute in 140 of 168 local files and by more than ten minutes in 2 |
| `EndTime` | last activity | last timestamped line | last envelope timestamp (every line is timestamped) |
| `ParentUUID` | parent session for a child rollout | empty | lifted `parent_thread_id`, else `source.subagent.thread_spawn.parent_thread_id` |
| `Source` | platform's session origin | empty | `cli` \| `exec` \| `vscode` \| `mcp` \| `subagent` \| other, as a string |

The scanner derives the stored title as `sanitize(ExplicitTitle)`, else `truncate(sanitize(TitleFallback), 120)` — exactly today's ordering (sanitize before truncate).

### Entry

Every entry carries `Kind`, `LineIndex` (physical 0-based line; the canonical **first** line for a merged Claude snapshot) and `Timestamp`. Kind-specific payload:

- **Human** — `Text` with format noise already stripped; `Queued` (Claude A2).
- **Assistant** — an **ordered** `[]Part`, each either a text part or a `ToolCall`, in block order. Order is load-bearing: today's `extractAssistantText` and `renderAssistantBlocks` interleave text and tool summaries in block order, so a `{Text, []ToolCall}` split would reorder any message that places text after a call (none observed in 464 local sessions at triage time, but the seam must not lose information). A `ToolCall` has `ID`, `Name`, `Summary` (platform label: `"Bash <cmd>"`, `"exec_command <cmd>"`, `"apply_patch <files>"`, `"Agent <prompt>"`, bare MCP name), `Input` (raw JSON, for future generic rendering per ADR-025 § Deferred) and an optional `Launch{Label, ChildUUID}` marking a sub-agent spawn.
- **ToolResult** — `CallID`, `CallName`, `CallSummary` (resolved by the decoder), `Body` (verbatim, untruncated) and optional `Diff{Text, Added, Removed}` — unified-diff text. The Claude decoder fills `Diff` from `toolUseResult.structuredPatch`; the Codex decoder fills it from the matching `apply_patch` call input **only when the result reports success** (see [Tool results](#tool-results)).
- **System** — `Text` and `SearchOnly`. `SearchOnly` preserves a deliberate asymmetry that exists today: the scanner indexes generic `attachment` message content (`attachmentText`) while `show` and the TUI do not display it. The flag keeps the refactor byte-identical; unifying the three surfaces is a separate, recorded decision (see [Open Questions](#open-questions)).

Decoders resolve call-to-result correlation themselves, so consumers receive the call's `Name` and `Summary` on every result without rebuilding the id map.

### Consumer contracts

The three consumers become thin loops over `[]Entry`:

- **Scanner** (`ScanTranscript`): a Human entry starts a new turn (the shared heuristic; explicit Codex turn ids are not used); a ToolResult whose `CallName` is in `ftsExcludedResult` is dropped; all other text is `sanitize.StripSecrets`-ed, ToolResult bodies are prefixed with `CallSummary` then head/tail bounded to `maxToolResultChars`. An **Assistant row's text is its text parts and its `ToolCall.Summary` values joined in part order** — today's `extractAssistantText` — so a tool-only assistant entry is still an assistant row and counts in `MessageCount`. This is parity with Claude tool-only turns, not inflation, and it is what makes `exec_command`, MCP and `web_search` calls searchable for Codex. Assistant rows carry `ToolNames` for the chunker title. `MessageCount` counts user and assistant rows. `ScanOutput` gains `Platform`, `PlatformID`, `ParentUUID` and `Source`, copied from `Meta`, so import persists `Platform` and `ParentUUID` from the single decode and can check `PlatformID` against the filename (`Source` rides along unpersisted in v1 — see [Deferred](#deferred-recorded-follow-ups) — so the future `source` column is a store-only change). Output for Claude sessions is byte-identical to today.

  **Accepted decision — no Read-analog exclusion for Codex.** `ftsExcludedResult` is keyed by Claude tool names (`Read`, `NotebookRead`, `Edit`, `Write`). Codex has no dedicated file-read tool: reads are `exec_command` calls (`sed -n`, `cat`), which cannot be told apart from any other shell command by name, so their file dumps are indexed like every other `exec_command` result — bounded to `maxToolResultChars` (16 KB head/tail) per row, as a large `Bash` output is today. This is accepted for v1: the bound already caps the FTS cost, and parsing `cmd` to guess "this is a read" would be a heuristic exclusion the Claude side deliberately never applied to `Bash`. `apply_patch` is the one Codex analog of `diffResultTools` (Claude `Edit`/`Write`): its body is boilerplate (`Success. Updated the following files:` plus the paths already carried by the `apply_patch <files>` summary) and the signal is the `Diff` built from the call. It therefore **joins `diffResultTools`**, which gives it exactly the Edit/Write treatment: body FTS-excluded (via the `ftsExcludedResult` union), summary indexed on the assistant row, `show` renders the verbatim body, the TUI collapses to the diff marker when `Diff` is set. This is name-keyed policy in the consumers, not a decoder change, and cannot affect Claude output.
- **Render** (`show`): verbatim bodies; ToolResults for `excludedResultTools` collapse to the one-line marker; every ToolCall (including launches) renders as an arrow line from `Summary`, in part order; the assistant heading is platform-aware (`Claude` / `Codex`).
- **Transcript** (TUI): adds `SourceLine`, the collapse thresholds, `Diff`, and launch markers from `ToolCall.Launch`. A marker with `ChildUUID` is `Openable`; opening it is a root-routed action that loads a **session** by UUID (see [TUI](#tui)); Claude markers keep today's count-based mapping onto sidecar subagent ids.

Public signatures gain a platform parameter: `ScanSession(platform, r)`, `RenderText(platform, raw)`, `RenderMarkdown(platform, raw)`, `ParseTranscript(platform, raw, subagentIDs)`. `ScanSubagent` stays Claude-only (sidecars are a Claude concept).

## Format Identification

`Platform` is a string type with two constants, `claude-code` and `codex`, validated in Go (`ParsePlatform`) against a closed set. A SQL `CHECK` constraint is deliberately **not** used: a third platform must be a constant, not a migration.

`DetectFormat(firstLine []byte) (Platform, error)` is **Codex-positive only**:

- Codex when the line parses as a JSON object whose top-level `type` is `session_meta` and whose `payload` is an object (always the first line of a rollout; 168/168 local files).
- Claude for **any other** parseable JSON object. There is no Claude-shaped key test: in 467 of 467 local sessions the first line is not a `user`/`assistant` line. Tally: `last-prompt` 317, `mode` 74, `file-history-snapshot` 41, `permission-mode` 34, `queue-operation` 1 — and `file-history-snapshot` carries none of `uuid`, `sessionId` or `message`, so a "Claude-shaped keys" rule would reject 8.8 % of real sessions. The Claude decoder already tolerates any leading line by skipping what it does not recognize.
- Error only for a line that is not a JSON object (or an empty blob). Callers (reindex, merge) record that session as an error rather than defaulting silently.

`DetectFormat` is consulted only for a **present but unrecognized** `platform` value. An absent column is Claude, unsniffed.

**Why "Claude for any other JSON object" is safe.** The rule only holds while exactly two platforms can exist in a vault this binary opens, and the reader marker guarantees that: **adding a platform constant is a `min_reader_version` bump** (the same argument that bumps it to 3 for Codex — every older code path is platform-blind), so a vault holding a third platform's rows is refused by this binary at open, never sniffed row by row. An unrecognized value therefore means a corrupted or hand-edited column on a Claude or Codex blob, and the Codex-positive test resolves it exactly. Consequences: **reindex** uses the sniffed platform for the rebuild but does **not** rewrite the stored `platform` value (the reindex path is FTS-only by ADR-025 D4) and logs a warning naming the uuid and the bad value so the user can repair it; **merge** writes a full destination row and therefore stores the resolved platform. This rule is recorded in ADR-031 (implementation Slice 13.4).

## The Codex Decoder

`codex_decoder.go` (with wire types in `codex_types.go`) reads a rollout through the shared `scanLines` and switches on the envelope `type`, then `payload.type`. Every physical line advances `LineIndex`, so anchors stay physical; the envelope `ordinal` is ignored.

### Metadata

Line 0 (`session_meta`) supplies `PlatformID` (`payload.id`), `CWD`, `Branch` (`git.branch`, absent for non-git cwd), `Source`, `ParentUUID`, and for subagent-sourced rollouts the `agent_nickname`, `agent_role` and `agent_path` that feed `TitleFallback`. `StartTime` is `payload.timestamp` (falling back to line 0's envelope timestamp when absent); `EndTime` is the last envelope timestamp. `cli_version` and `history_mode` are read only for logging.

### Human turns come from events

`response_item` messages with `role == "user"` are dominated by injected context (`<environment_context>`, AGENTS.md bodies, `<skill>`, `<turn_aborted>`, `<subagent_notification>`, `<recommended_plugins>`). The reliable human-turn source is the event stream, present across the whole 0.124 → 0.154 range:

| History mode | Source of the human prompt |
|---|---|
| legacy (< 0.147, `history_mode` absent or `legacy`) | `event_msg` / `user_message` → `payload.message` |
| paginated (≥ 0.147) | `event_msg` / `item_completed` with `item.type == "UserMessage"` → join `item.content[].text` |

Both paths are always attempted; `history_mode` does not gate them. Corpus reconciliation (168/168 files): the event-derived prompt count equals the count of noise-filtered user `response_item`s in every file, and every legacy event text appears verbatim among the response items — so the event stream is complete, not merely non-empty.

Only when a whole file yields **zero** human entries from events does the decoder fall back to `response_item` user messages, applying a noise filter: drop the `developer` role entirely, and drop any `input_text` whose trimmed text **starts with `<`** (a tag — the same rule the Claude title fallback uses) or with `# AGENTS.md instructions`. The explicit tag list (`<environment_context>`, `<skill>`, `<turn_aborted>`, `<subagent_notification>`, `<recommended_plugins>`, `<INSTRUCTIONS>`) is documentation of what has been observed, not the filter itself; a prefix list would have let `<recommended_plugins>` through as a bogus human turn and title.

**Subagent rollouts may legitimately have no human turn.** Children spawned by CLI ≤ 0.137 carry the spawn prompt as one `user_message` event (10 of 13 local children). From CLI 0.147 the child records **no** human prompt by any path — its only user-role response item is `<recommended_plugins>` noise (3 of 13). Such a file yields zero Human entries; that is expected for `Source == subagent`, not format drift, and the zero-human warning below is suppressed for it. These children are still archived: their assistant rows make `MessageCount ≥ 1`.

Legacy `agent_message` events and paginated `AgentMessage`, `CommandExecution`, `McpToolCall`, `FileChange`, `WebSearch` items are skipped: they duplicate `response_item` content byte-for-byte (verified).

### Assistant entries and tool calls

`response_item` / `message` with `role == "assistant"` becomes an Assistant entry (`output_text` parts joined with newlines; `phase` ignored). There are no progressive snapshots to merge.

`function_call`, `custom_tool_call` and `web_search_call` attach as `ToolCall` parts to the most recent Assistant entry that has not yet been followed by a ToolResult or Human entry; otherwise they open a text-less Assistant entry at their own line. Summaries:

| Codex call | `Name` | `Summary` |
|---|---|---|
| `function_call` `exec_command` | `exec_command` | `exec_command <cmd>` — `cmd` parsed from the **stringified** `arguments` JSON |
| `custom_tool_call` `exec` | `exec` | `exec <first line of input>` |
| `custom_tool_call` `apply_patch` | `apply_patch` | `apply_patch <files>` — paths from `*** Add File:` / `*** Update File:` / `*** Delete File:` lines |
| `function_call` `spawn_agent` | `spawn_agent` | `spawn_agent <task_name> (<agent_type>)`; also `Launch{Label, ChildUUID}`. The `message` argument is **encrypted** from CLI 0.147 (`gAAAAAB…`), so it is used only when both `task_name` and `agent_type` are absent, bounded to 200 runes |
| `web_search_call` | `web_search` | `web_search <query>` — *implemented as* `action.query` › `queries[0]` › `url` › `pattern` › bare name, because 71 of 130 local actions carry no `query` (open_page / find_in_page actions have `url` / `pattern`; 30 carry only `type`) |
| any other `function_call` (MCP tools appear as bare names, e.g. `capy_search`) | as given | bare name (generic input rendering stays deferred per ADR-025) |

`Launch.Label` prefers `task_name`, then `agent_type`, then the parent-side `SubAgentActivity.agent_path`, then the (possibly encrypted) `message`. `Launch.ChildUUID` is resolved from a later `event_msg` / `collab_agent_spawn_end` (`call_id` → `new_thread_id`, legacy) or `item_completed` with `item.type == "SubAgentActivity"` (`item.id == call_id` → `agent_thread_id`, paginated). Unresolved launches keep an empty `ChildUUID` and stay visible but non-openable. Corpus check: all 11 parent-resolved child ids match a discovered child rollout's `session_meta.id` (2 further children have no local parent).

### Tool results

`function_call_output` and `custom_tool_call_output` each become a ToolResult entry at their own line, correlated by `call_id`. Body normalization:

- `function_call_output.output` is a string or a content array (`input_text` parts joined; image/audio parts skipped).
- The `exec_command` wrapper header (`Chunk ID: …`, `Wall time: …`, `Original token count: …`, `Output:`) is stripped; the `Process exited with code N` line is **kept** as the first body line because an exit code is search signal; the text after `Output:` follows. *Implemented as:* any `Process …` / `Script …` status line is kept (the corpus also writes `Process running with session ID N` for backgrounded commands and `Script completed` for the hosted `exec` tool); a body whose leading lines do not follow this grammar is left untouched.
- `custom_tool_call_output.output` is a JSON string decoded once more; `.output` is the body. Its `metadata.exit_code`, when present, is prepended as `Process exited with code N` for parity with `exec_command`. When the second decode fails (the string is not a JSON object, or has no string `.output`), the raw string is the body verbatim, with no exit-code line and no `Diff` — the content is preserved, only the structure is not trusted. **Implementation finding (Task 6 canary):** for the hosted `exec` tool `output` is usually a content **array** (539 of 598 local results), not a string; it is then treated exactly like a `function_call_output` array — text parts joined, `exec_command` header stripped — with no inner JSON, no exit-code line and never a `Diff`. `apply_patch` results are always the string form.
- An `apply_patch` result gets `Diff` built from its call's patch text by a new helper beside `diff.go` that converts Codex's `*** Begin Patch` format to unified-diff text with add/remove counts — **only when the result succeeded** (`metadata.exit_code == 0`, or the body starts with `Success.` when no exit code is present). A failed or malformed patch keeps its plain body and no `Diff`, so the viewer never shows a diff that was not applied. (All 21 local `apply_patch` results succeeded; the failure path is fixture-tested.)

### Skipped

`reasoning`, `compacted` (its `replacement_history` duplicates earlier lines), `turn_context`, `world_state`, `token_usage_record`, `token_count`, `task_started`, `task_complete`, `turn_aborted`, `thread_settings_applied`, `inter_agent_communication*`, `security_risk_score`, `retained_context`, `realtime_item`, `tool_search_*`, `agent_message` response items, and any unknown type (ADR-021). Unknown envelope and payload types are collected per file and logged once at debug level as the format-drift fingerprint.

## Discovery and Import

### Discoverer

A `Discoverer` interface (`Discover(root) ([]SessionFile, error)` — *implemented as* `Discover(ctx, root) ([]SessionFile, DiscoveryReport, error)`: the report was added in Task 7.2, and `ctx` in the Task 14 review so the server sweep's 30 s budget bounds the walk, not only the import; a cancelled walk returns what it found with `ctx.Err()`) with two implementations:

- **Claude** wraps today's `DiscoverSessions` / `detectProjectDirs` / `discoverProject` logic unchanged.
- **Codex** walks `$CODEX_HOME/sessions` and `$CODEX_HOME/archived_sessions` (new `config.CodexHome()`, honoring `CODEX_HOME`, default `~/.codex`) for regular files (symlinks skipped, as today) whose name parses as `rollout-<YYYY-MM-DDThh-mm-ss>-<uuid>[_<rollout_id>].jsonl[.zst]`. A filename that does not parse is skipped with a warning. A file **with** a `_<rollout_id>` suffix (a `thread/revert` variant) is recognized but **skipped with a warning** naming the thread and path — v1 does not archive revert variants (see [Import](#import)); skipped paths are returned in a `DiscoveryReport` alongside the session list so `import` and the sweep can report the count. **Root order is fixed:** `sessions/` is walked before `archived_sessions/` and results are sorted within each root, never across roots, so an active copy of a thread is always seen before an archived copy of the same thread (the import reconciliation below relies on this).

`SessionFile` grows: `Platform`; `RelativePath` (Codex: the rollout path relative to `$CODEX_HOME`, normalized to its plain `.jsonl` name — the only way to restore a **local-time** filename faithfully, since the UTC metadata cannot rebuild it); `ProjectPath` hint (Codex: `cwd` from a bounded first-line read, so project filtering needs no full scan); `Compressed`; `OnDiskSize`. For Claude, `ProjectDir` keeps its mangled-dir meaning and `RelativePath` is unused.

**The first-line read is the only per-file cost discovery incurs, so the caller controls it.** The Codex discoverer takes a `Skip func(relPath string, onDiskSize int64, compressed bool) bool` predicate evaluated from directory metadata **before** any file is opened; a file it accepts is dropped from the result entirely (nil predicate → nothing skipped). The server sweep passes the archived-rows predicate described under [Server startup sweep](#server-startup-sweep); manual `capy vault import` passes nil and reads every first line. The first-line reader is bounded to `maxScanLineBytes` and, for a `.zst` rollout, uses a **streaming** zstd reader over the file (not `codec.go`'s whole-frame `DecodeAll`), so a compressed rollout costs one frame block, not a full decompression.

**Reporting surface.** `DiscoverSessions(root)` keeps its signature (the sweep tests and callers depend on it) and becomes a thin wrapper over `DiscoverSessionsReport(root) ([]SessionFile, DiscoveryReport, error)`, which the `import` command and `DiscoverAll` use, so a `--source <codex dir>` run reports skipped revert variants exactly like the default run.

### Import

`Import` reads a `.zst` rollout through the existing zstd decoder (`codec.go`) before anything else, so `raw_jsonl`, `content_hash` and `size_bytes` are always the plain JSONL bytes.

**Same-run reconciliation.** Today's decision consults only committed DB state (`SessionDigest`) and queues writes into one transaction, so two files for one uuid in the same run would both queue as inserts and fail on the primary key. Import therefore keeps an **in-run map** `uuid → {hash, size, decision, batch position}` recording **every** uuid the run has seen — including ones skipped as unchanged — and consults it before `SessionDigest`: a later file for an already-seen uuid is `skipped` when its hash matches or it is smaller, and replaces the pending record (or becomes a replace against the DB if already flushed) when it is larger and divergent. A same-hash later copy never touches the location hint. Dry run applies the same map, so it reports one `new` and one `skipped` for an active/archived pair, never two `new`.

**Location policy (Codex only).** Codex archive/unarchive is a *move*, so the same bytes can reappear at a different relative path. When the run's **first** sighting of a **Codex** uuid is a same-hash file whose `RelativePath` differs from the stored `claude_project_dir`, import performs a **metadata-only update** of the location hint and reports `updated`. Combined with the fixed discovery order, this yields: only one copy on disk at a new path (a genuine move) → hint follows it; both copies on disk → the `sessions/` copy is seen first and wins, the archived copy is `skipped`. Restore therefore follows where Codex last kept the file, not first-import order. *Implemented as (Task 14 review):* the "both copies" case cannot rely on discovery order alone — the server sweep's skip predicate drops the copy at the stored hint **unopened**, so the other copy reaches import as the run's first sighting and would flip the hint on every start. Before moving the hint, import therefore checks that no rollout (plain or `.zst`) still exists at the stored hint under the discovered root (`codexRolloutPresent`, `SessionFile.Root`); if one does, the file in hand is a lingering duplicate and is `skipped`. The hint follows the file only once the old location is empty. Mechanics:

- The policy is gated on `sf.Platform == PlatformCodex`. For Claude rows the hint is the mangled project dir (or, on a loose `--source <dir>` import, the containing directory's basename), `RelativePath` is unused, and Claude Code never moves a session file — so a Claude row's hint is never touched by this rule. A test pins that a loose-source re-import of a Claude session leaves the hint unchanged.
- The decision needs the stored hint, so `SessionDigest` gains a `claudeProjectDir` return value (one extra column on the same single-row query).
- The update is a single `UPDATE vault_sessions SET claude_project_dir = ? WHERE uuid = ?` (`UpdateLocationHint`) in its own immediate transaction, executed at decision time rather than queued in the write batch: it is rare (one row per moved file), touches no blob, hash, FTS or `archived_at`, and keeping it out of `WriteBatch` avoids a third write shape in the batch.
- A same-hash, **version-stale** (`ftsOnly`), **moved** file does both: the FTS rebuild is batched as today and the hint updates immediately; it reports `updated` once.
- Dry run reports `updated` for a move without calling `UpdateLocationHint`.

**Revert variants.** `thread/revert` writes a new immutable file for the same thread and Codex appends to the new one; the two share a prefix then diverge, so neither size nor hash establishes containment and larger-wins could silently discard unique bytes. With zero local samples, v1 **skips** `_<rollout_id>` files loudly (discovery warning; the `DiscoveryReport` count is printed by the import summary and the sweep log) and archives only the base file. Full support is a recorded follow-up ([implementation.md § Deferred](./implementation.md#deferred-work)); the parser already recognizes the suffix so the fixture and the warning are testable now.

`buildRecord` selects the decoder by `sf.Platform` and fills `Session.Platform` and `Session.ParentUUID` from `ScanOutput`, `Session.ClaudeProjectDir` (Claude: mangled dir; Codex: `RelativePath`) and `Session.ProjectPath` (`Meta.CWD`, else the discovery hint, else today's unmangle fallback). Codex sessions have no sidecars; `vault_files` stays empty for them. The zero-message exclusion is unchanged: a rollout with no human turn and no assistant entry reports `excluded` and is not archived (23 of 168 local rollouts — sessions aborted before the model answered). A Codex child rollout is archived because its assistant rows make `MessageCount ≥ 1` — **not** because of a spawn prompt: children from CLI ≥ 0.147 record no human turn at all.

**Row identity is the filename uuid.** `SessionFile.UUID` comes from `parseRolloutFilename`, before any byte of the file is read, and it is what Codex's own DB-less resume fallback scans for. `session_meta.id` (`Meta.PlatformID`) is informational: when it is non-empty and differs from the filename uuid, import logs a **warning** naming both and keeps the filename uuid as the row key. Locally the two agree in 168 of 168 rollouts (canary, Assumption 1).

`capy vault import` defaults to **every platform root that exists on disk**. `--source <dir>` autodetects layout: a `projects/` child or `*.jsonl` files means Claude (today's rules); a `sessions/` or `archived_sessions/` child holding `rollout-*` files means Codex. `--project` matches either the mangled dir or the `ProjectPath` hint (substring, as today). A new `--platform claude-code|codex` restricts the run.

### Server startup sweep

`vaultSweep` discovers **each platform independently** — a per-platform discovery error or empty result is a debug line and never aborts the other platform (today's early returns after Claude discovery would otherwise skip Codex entirely for a project with no Claude sessions). It imports Claude first, then Codex rollouts whose first-line `cwd` equals the server's project dir (both `filepath.Abs` + `EvalSymlinks` + `Clean`); `CAPY_VAULT_SWEEP_ALL` widens both.

The sweep runs under a 30-second budget and Codex has no auto-cleanup, so its per-startup cost must be bounded independently of how much a user has already archived. Before Codex discovery runs, the sweep loads one query of `(claude_project_dir, size_bytes)` for `platform = 'codex'` rows into a map and hands the discoverer a `Skip` predicate (see [Discoverer](#discoverer)) that drops a plain `.jsonl` rollout whose relative path **and** on-disk size both match (rollouts are append-only: unchanged size means unchanged content for sweep purposes), and a `.jsonl.zst` rollout whose path matches (Codex compresses only rollouts it no longer appends to). The predicate is evaluated from directory metadata, so a skipped file is never opened.

**The real bound.** The map holds only rows this vault has archived. A rollout that this sweep does *not* import — every rollout whose `cwd` is another project, unless `CAPY_VAULT_SWEEP_ALL` is set or the user ran `capy vault import` — is therefore not in the map and gets its first line read on **every** server start. The cost per startup is one bounded first-line read (streaming for `.zst`) for each rollout not already archived at its current `(path, size)`, which is O(corpus) in the worst case and O(changed) only once the corpus is archived. This is accepted for v1: a first-line read is a single `bufio` read of a few KB, the local corpus of 168 files costs well under a second, and the 30-second budget defers the rest to the next start or a manual import. The canary times a cold sweep over the local corpus (Assumption 12). A negative cache of `(path, size) → cwd` for non-matching rollouts is the next step if the timing ever matters; it is deliberately not built now (it is state the vault would have to persist and invalidate). Manual `capy vault import` passes no predicate and hashes everything. A missing `$CODEX_HOME` is a debug line, not a warning.

## Storage Model

Migration `0006_platform`, applied through the existing name-keyed runner with the read-only fast path:

| Change | Where | Contract |
|---|---|---|
| `vault_sessions.platform TEXT NOT NULL DEFAULT 'claude-code'` | `schemaSQL` `CREATE TABLE` (fresh) + `ALTER TABLE ADD COLUMN` in 0006 (legacy) | Validated in Go against the closed constant set. Existing rows read as Claude. |
| `vault_sessions.parent_uuid TEXT` | same | Nullable. **No foreign key**: a child can be imported before its parent, and deleting a parent must not cascade or fail. Empty string in Go == NULL. |
| `CREATE INDEX IF NOT EXISTS idx_sessions_parent ON vault_sessions(parent_uuid)` | **migration 0006 only**, after the column exists | Child lookups (`Children(parentUUID)`) and the `parent_uuid IS NULL` list default. It must **not** appear in `schemaSQL`: `openDB` runs `schemaSQL` before `migrateVault`, and on a legacy vault the index would reference a column that does not exist yet, failing every open. |

`claude_project_dir` keeps its name and `NOT NULL` and gains a documented generalized meaning: **the platform's location hint** — the mangled project dir for Claude, the relative rollout path under `$CODEX_HOME` for Codex. `sessionMetaColumns`, the insert and update statements, `GetSession`, `ListSessions`, `Search`, `SearchChunks` and the merge reader carry the two new columns. The schema and query plan require normal human review before the implementation merges (Go profile, database checklist).

### Reader version

Every older code path that touches a session row is platform-blind: `defaultRestoreRoot` would restore a Codex row into `~/.claude/projects/<relative rollout path>/<uuid>.jsonl`, `resume` would launch `claude --resume` on a Codex UUID, and an older binary's `merge` would rescan a Codex blob with the Claude scanner and write it into the destination as `platform = 'claude-code'` (column default) with empty FTS — a **permanent** mislabeling, because a later disk re-import is same-hash and skipped, and reindex trusts the stored platform. "Renders empty, corrupts nothing" was therefore false.

Decision: the first Codex row written stamps `vault_meta.min_reader_version = 3` and `supportedReaderVersion` becomes 3. `markMinReaderVersion` changes from `INSERT OR IGNORE` (which can create the marker but never raise it) to a monotonic upsert that only increases the numeric value, and takes the version to stamp as a parameter. The two versions get named constants — `readerVersionZstd = 2`, `readerVersionPlatform = 3` — and **every stamping site passes the version its own write requires**: the batch writer stamps the highest version any record in the transaction needs (2 for a zstd-encoded blob, 3 for a `platform = 'codex'` row); `capy vault compact` (`compact.go` `markCompressed`), which today also calls `markMinReaderVersion` after recompressing legacy blobs, passes `readerVersionZstd`, so compacting a Claude-only vault leaves the marker at 2. An older binary then refuses to open a Codex-bearing vault, and its `MergeFrom` refuses a source whose marker exceeds the version it supports (`checkReaderVersion` already runs on the source), exactly as the v2 zstd bump did. A vault that never receives a Codex row stays at marker 2 and remains openable by older binaries, including after `compact`. `currentIndexVersion` is still **not** bumped: Claude extraction is byte-identical.

**Generalized rule (ADR-031):** any future platform constant is likewise a reader-version bump. Every read path that dispatches on `platform` is exhaustive over the constants it knows, so a binary that lacks a constant must not open a vault that uses it. This is what makes the "unrecognized value ⇒ corruption" reasoning in [Format Identification](#format-identification) sound.

## Sub-agent Model

Codex writes each spawned agent as its own top-level rollout (13 of 168 local files). The vault mirrors Codex's own model on the backend and its own presentation on the surface:

- **Backend:** each child is its own `vault_sessions` row, byte-verbatim, with `parent_uuid` set from its `session_meta`. It is discovered, hashed, indexed and merged like any session. From CLI 0.147 the child records no spawn prompt; its title falls back to `agent_nickname · agent_role` (see [Meta](#meta)).
- **Presentation parity with Codex:** Codex's agents overview and resume picker hide threads that have a `parent_thread_id` or a `SubAgent(ThreadSpawn)` source and reach them from the parent via the `/subagents` picker in spawn order (verified in `codex-rs/tui/src/app/agents_overview_threads.rs` and `codex-rs/rollout/src/lib.rs` `INTERACTIVE_SESSION_SOURCES`). The vault therefore hides children from `list` and the TUI list by default (`ListOptions.IncludeChildren`, `--include-children`), shows a parent's children in `show` and opens them from the parent's launch markers in the TUI in spawn order.
- **Search:** Codex has no transcript search, so no parity constraint applies. Children are searchable as their own hits; `SearchResult` carries `Platform` and `ParentUUID` so a hit can be labeled as a child.
- **Delete:** deletes only the addressed session and warns when children exist. Codex's `/delete` cascades to descendants; capy does not (see [Not Doing](#not-doing)).
- **Other sub-agent sources:** `SubAgentSource` also has unit variants `Review`, `Compact` and `MemoryConsolidation` that carry no parent id; only a lifted `parent_thread_id` could link them. None occur locally (13/13 are `thread_spawn`). Without a parent they list as top-level sessions; hiding them by `Source` is recorded in [Deferred](#deferred-recorded-follow-ups).

## Read Surfaces

### CLI

- `vault list`: platform column; `--platform`; `--include-children` (children show their parent's short id); JSON adds `platform`, `parent_uuid`.
- `vault show`: header prints the platform; a child names its parent; a parent lists its children with short ids. Assistant heading `Codex` / `🤖 Codex` for Codex rows.
- `vault search`: each hit is tagged with its platform; child hits are marked.
- `vault stats`: per-platform breakdown (`VaultStats.ByPlatform`) and a child count.
- `import` and `merge` summaries report per-platform counts; `import` (and the sweep log) also report the number of skipped revert variants from the `DiscoveryReport`.
- `vault restore`: Codex default root is `$CODEX_HOME`; the main file is written at `claude_project_dir` (the relative path), always as plain `.jsonl`. `--output` overrides the root as today. The existing path-safety validation (`validateSidecarRel`, `safeChildPath`, symlink re-check) applies to the relative path. An existing plain file at the target goes through today's overwrite prompt. A `.jsonl.zst` **twin** at the same relative path is never touched: restore writes the plain file beside it and prints a note naming the twin (Codex itself materializes the plain `.jsonl` next to a compressed rollout when it appends, so the pair is a state Codex produces; whether Codex's picker then lists the thread once or twice is unverified and recorded under [Open Questions](#open-questions)).
- `vault resume`: a Codex session **fails loudly** with an actionable message naming `capy vault restore` and `codex resume <uuid>`; it must never launch `claude --resume` on a Codex UUID. Real integration is deferred.
- `vault delete`: warns about children, no cascade.

### Identity and display

`minUUIDPrefix` stays 8 (an ambiguous prefix is already an error path with candidates). Short-id **display** becomes platform-aware in the CLI: `cmd/capy/vault.go`'s `shortUUID` shows 12 characters for Codex rows in `list`, `show`, search output, ambiguous-candidate listings and the import table, because UUIDv7 prefixes collide at 8 (22 of the 164 rollouts measured at research time) and not at 12. The TUI needs **no change**: its `shortID` helper (`tui/render.go`) already truncates every id at 12.

### TUI

- List: hides children by default; a toggle key, chosen at implementation time to avoid `f`, `e`, `c`, `r`, `R`, `/`, `]`, `[`, `n`, includes them.
- Viewer: role label and header are platform-aware (`Codex · child of <parent short id>`). Opening a child is a **root-routed action**: `viewerModel` owns no store handle (by design — the root `Model` in `tui/app.go` owns `dataStore`, the context and `openSession`), so the viewer emits `viewerOpenChild{uuid}`; the root model loads the child through `dataStore`, pushes a **viewer frame** (session, files, scroll and focus state) onto a stack, and `esc` / `q` pops back to the parent frame — or, for a direct open from list or search, to the originating mode as today. A child that is not archived surfaces as a transient root status message, not an error dialog. Claude markers are unchanged; the existing `openSubagent` and `openInlineContent` targets stay viewer-local because they need no store access.

### MCP

- `capy_search` / `capy_vault_search` result metadata adds `platform` (and `parent_uuid` when set) to the formatted meta line of **every** vault hit, Claude included; the `session:<uuid>` tag is unchanged (`formatVaultHit`).
- `capy_stats`: the vault section adds per-platform counts.
- `capy_doctor` / `capy doctor`: a new vault check reports which platform roots exist on disk and how many sessions of each are archived, so a dual-tool user can see that Codex is being swept.

## Reindex, Merge, Restore

- **Reindex** passes each row's stored platform to `scanSessionAndSubagents`. A present but unrecognized value (corruption — see [Format Identification](#format-identification)) falls back to `DetectFormat` on the blob's first line and logs a warning with the uuid and the bad value; the stored `platform` column is **not** rewritten (reindex is FTS-only). If the sniff also fails, the session is recorded as an error, never scanned as Claude by default.
- **Merge** feature-detects `platform` and `parent_uuid` on the source. **Absent** (pre-0006 source) → Claude, no parent, **no sniff** (sniffing would drop the 8.8 % of legacy sessions whose first line is a `file-history-snapshot`). Present and recognized → carried verbatim. Present but unrecognized → `DetectFormat` with a warning, and the **resolved** value is what the destination row stores; error on failure. FTS is rebuilt with the destination's decoder for that platform. Its `--project` filter matches `claude_project_dir` **or** `project_path`. A source carrying `min_reader_version = 3` is refused by an older binary before any of this runs.
- **Restore** branches on platform (above). Claude is unchanged.

## Observability

- Every import, reindex and merge log line gains a `platform` attribute; per-platform counts and skipped revert variants appear in the CLI summaries and the sweep's info line.
- The Codex decoder emits one **debug** line per file listing unseen envelope/payload types (format-drift fingerprint), and one **warning** with `cli_version` and `history_mode` when a file is **not** subagent-sourced, yielded **at least one Assistant entry**, and yielded **zero** Human entries — the Codex analog of ADR-021's zero-turns signal: the model answered, so a prompt must have existed and the decoder failed to find it. Two file classes are deliberately outside the trigger. Subagent-sourced files are exempt because CLI ≥ 0.147 children legitimately carry no human turn. Files with **no** assistant entry either (a session aborted before the model produced anything — `task_started` then `turn_aborted`, nothing else; 23 of 168 local rollouts, all `source = cli`) are ordinary empty shells: they exclude as zero-message sessions at **debug** level, exactly like an empty Claude session. Keying the warning on `task_started` alone, as an earlier revision did, would have fired on all 23.
- A missing `$CODEX_HOME` is debug; an unparseable filename or first line is a warning plus skip; a skipped revert variant is a warning naming thread and path.
- Every decoder skip or recovery warning (malformed line, malformed payload, oversize line, zero-human) carries a `file` attribute naming its input: the rollout's on-disk path on import, `vault:<uuid>` on reindex and merge (the bytes come from the BLOB, not a file), and `<path> sidecar <relative>` for a Claude sidecar. The label rides on the reader (`withSource`, read back by `decoderLogger`) because `Decoder.Decode` takes a bare `io.Reader` shared by import, reindex and display; display consumers decode unlabelled. Added 2026-09-17 after a real torn-write rollout (line 182 cut mid-record, ordinal re-written on the next line) produced a warning no one could trace to a file. A same-day Claude sample exposed a second shape: a torn assistant object immediately followed by a complete `{"parentUuid":...}` user record without a newline. The Claude decoder now recovers that fully valid suffix, logs the discarded-byte count, and keeps the physical line anchor; all other malformed lines are still skipped.
- Errors are logged **or** returned, never both (Go profile, observability checklist). No transcript text is logged.

## Failure, Concurrency, Security

All new reads and writes use context-aware, parameterized `database/sql` calls; multi-statement writes stay inside the existing `BeginImmediateContext` discipline; NULL columns map to `sql.NullString`. The decoder never panics on malformed input: a malformed line is skipped (logged), except for the bounded Claude suffix recovery described above; an oversize line advances `LineIndex` without content (existing `scanLines` contract). Restore path safety is unchanged and now also covers the Codex relative path. Secret stripping applies to Codex FTS rows and titles exactly as to Claude. The sweep's concurrency posture (busy-timeout absorption, cooperative cancellation) is unchanged.

## Verification Strategy

Full detail in [implementation.md § Verification](./implementation.md#verification-and-gates). In summary:

1. **Refactor gate.** A written inventory of every behavioral divergence between the three readers, mapped to a model field or a documented log-only difference; committed golden outputs for every existing fixture across the four public readers; an env-gated real-corpus digest baseline over `~/.claude/projects` (run on `master`, compare on the branch). The refactor slice ships only when both are unchanged.
2. **Codex fixtures** for both history modes, a parent/child pair (one ≤ 0.137 child with a spawn prompt, one ≥ 0.147 child without), `apply_patch` success and failure, the `exec_command` header, `.jsonl.zst`, the filename parser with a `_<rollout_id>` variant that is skipped, identical content under `sessions/` and `archived_sessions/` in one run (one `new`, one `skipped`, both in dry run), and a same-hash path move (metadata-only `updated`).
3. **Real-data canary** over `~/.codex/sessions` (skipped when absent) asserting [Assumptions](#assumptions) 1 (including `session_meta.id == filename uuid`), 2 (by **per-file count reconciliation** of events vs noise-filtered response items), 3 and 11; that the zero-human warning never fires on the corpus; and that every rollout with zero Human and zero Assistant entries is the aborted-shell shape (no `task_complete`), reporting the count.
4. **Migration and merge matrix**: pre-0006 vault open (index created by the migration, not `schemaSQL`); merge from a pre-0006 source with a `file-history-snapshot` first line (must merge, not error); merge of Codex rows; unrecognized platform value → sniffed with a warning (a JSON blob merges as Claude or Codex and the destination stores the resolved value, a non-JSON blob is an error); dry run; older-binary refusal via the reader marker; `compact` on a Claude-only vault leaves the marker at 2.
5. **End to end**: import → search (per-line, chunk, federated) → show → restore with **byte equality** against the decompressed input; cwd-scoped sweep with no Claude root and with the skip predicate (a skipped file is never opened — assert via a counting first-line reader).
6. `make test-race`; `make bench-quality` and `make bench-compare BASE=master` (search result formatting and selected columns change).

## Assumptions

Each is a testable bet the design depends on.

1. `session_meta` is always line 0 of a Codex rollout, so `DetectFormat` reads one line, and its `payload.id` equals the filename uuid (168/168 locally), so the filename is a safe row key. *Test:* canary asserts both over the local corpus.
2. For every **non-subagent** Codex session, the event stream carries every human prompt: the count of `user_message` events (legacy) or `UserMessage` items (paginated) equals the count of noise-filtered user `response_item`s, and each legacy event text appears among them. *Test:* per-file canary reconciliation (currently 168/168, including the 23 aborted shells where both counts are 0). Subagent-sourced rollouts are exempt: CLI ≥ 0.147 children carry no human turn.
3. Every `function_call_output.call_id` / `custom_tool_call_output.call_id` matches an earlier call in the same file. *Test:* canary counts unmatched ids and asserts zero.
4. The shared-model refactor is byte-identical for Claude across `ScanSession`, `RenderText`, `RenderMarkdown` and `ParseTranscript`. *Test:* goldens plus the real-corpus digest baseline.
5. Revert rollouts (`_<rollout_id>`) can be recognized by filename alone and are safe to **skip** in v1 without affecting the base file. *Zero local samples:* the parser, the warning and the import summary count are fixture-tested; archival of variants is a recorded follow-up.
6. `.jsonl.zst` rollouts are plain zstd frames readable by `klauspost/compress/zstd`. *Test:* a fixture compressed with the vault's own encoder round-trips through discovery and import.
7. Hashing decompressed bytes makes an archived or compressed copy of the same thread hash-equal, and the in-run reconciliation map turns a same-run pair into one `new` and one `skipped`. *Test:* fixture with identical content under both roots, real run and dry run.
8. Storing the original relative path makes restore path-preserving with no timezone arithmetic. *Test:* restored path equals the discovered path minus `.zst`; restored bytes are byte-identical to the decompressed input (equivalently, `computeContentHash` over `{"<uuid>.jsonl": restored}` equals the stored `content_hash` — the hash is a framed digest, never a plain SHA-256 of the file).
9. Migration 0006 is additive: pre-existing rows read `platform = 'claude-code'`, `parent_uuid = NULL`, the index is created after the columns, and every existing test passes against a migrated legacy vault. *Test:* migration test on a pre-0006 fixture vault.
10. Comparing the server's project dir to `session_meta.cwd` by cleaned, symlink-resolved equality scopes the sweep correctly for the common case. *Test:* sweep integration test with a symlinked project dir.
11. A parent-resolved `Launch.ChildUUID` equals the child rollout's `session_meta.id` (and filename uuid), so `GetSession(ChildUUID)` resolves. *Test:* canary compares parent-side ids with discovered child ids (currently 11/11).
12. The sweep's `(relative path, on-disk size)` skip predicate is a safe skip because Codex rollouts are append-only and compressed rollouts are immutable, and the residual per-startup cost — one bounded first-line read per rollout not archived at its current `(path, size)`, including every other project's rollouts — fits the 30-second budget for a realistic corpus. *Test:* sweep test appends to a rollout and asserts it is re-read, and asserts a skipped file is never opened; canary times a cold Codex discovery (all first lines read) over the local corpus and fails above 5 seconds.
13. Adding a platform constant is a `min_reader_version` bump, so a vault this binary opens holds only platforms it knows and an unrecognized `platform` value is corruption. *Test:* not testable in code — a policy recorded in ADR-031 and enforced at review; the `ParsePlatform` table test fails on any constant not paired with a version bump in `codec.go` (a comment-linked constant list).

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
- **A Read-analog FTS exclusion for Codex** (`exec_command` file dumps): no tool name distinguishes a read from any other shell command; the 16 KB head/tail bound already caps the cost (see [Consumer contracts](#consumer-contracts)).
- **An opt-out env var for the Codex sweep / reader bump**: violates the confirmed no-new-env-var constraint; the v2 precedent (upgrade the older binary before merging) applies (see [Constraints](#constraints)).
- **A negative `(path, size) → cwd` cache for non-matching rollouts** in the sweep: persisted state to invalidate for a cost that is one bounded read per file (see [Server startup sweep](#server-startup-sweep)).

## Deferred (recorded follow-ups)

- **`capy vault resume` for Codex** — launch `codex resume <uuid>` with `-C <dir>` following the existing cwd fallback chain, after manually verifying Codex's DB-less filesystem fallback resumes a restored rollout. Tracked in [implementation.md § Deferred](./implementation.md#deferred-work).
- **Archiving revert rollouts** (`_<rollout_id>` variants) — skipped loudly in v1; needs a real sample to choose between file-identity keying and a containment rule.
- **Unifying the `SearchOnly` attachment asymmetry** across the three surfaces — a deliberate behavior change to decide separately once the refactor has landed.
- **Hiding non-interactive and non-`thread_spawn` sub-agent sources** (`exec`, `mcp`, `Review`, `Compact`) from `list`, mirroring Codex's picker — requires persisting `Meta.Source`.

## Rejected Alternatives

- **Parallel readers (Codex scanner/render/transcript trio behind sniff-and-dispatch).** Fastest to ship and zero Claude edits, but it institutionalizes the recorded parser-drift bug at twice the surface: six readers to keep in sync, every future Codex format change fixed in three places.
- **Translate Codex to Claude-shaped lines on read.** Zero reader changes, but translated lines no longer map to physical `line_index` anchors (breaking search-to-view jumps), and Codex semantics with no Claude equivalent (tool outputs as separate lines, `apply_patch` diff in the call input, explicit turn ids) are lost behind a leaky shim.
- **Fold Codex children into the parent's `vault_files` as `subagents/agent-<child>.jsonl`.** Reuses the sidecar open path and chunk tagging, but the same bytes are also a standalone discovery hit (dedup), a child can arrive before its parent (ordering), and it diverges from Codex's own first-class thread model.
- **Sniff-only format detection, no `platform` column.** Zero schema change, but every dispatch (list rendering, reindex, merge, restore) would need a blob read, and `list --platform` would be impossible without a scan.
- **Claude-shaped-key sniffing, and sniffing absent-column rows.** Rejected on corpus evidence: 41/467 real Claude sessions open with a `file-history-snapshot` line carrying none of the keys, so legacy merges would drop 8.8 % of sessions.
- **Fail loud (no sniff) on an unrecognized `platform` value.** Simpler, but a single corrupted column would make a session unviewable, unreindexable and unmergeable when its blob is intact and the Codex-positive test resolves it exactly; the reader-version rule already excludes the only case where sniffing could mislabel (a third platform). Kept as a warning-plus-sniff.
- **Keying the zero-human warning on `task_started`.** Fires on the 23 local aborted-at-startup shells that never got an assistant reply; keyed on "assistant entries present, human entries absent" instead.
- **Rename `claude_project_dir`.** Cleaner name, but a wide refactor across store, merge, restore and tests for no behavior gain; violates the additive-schema constraint.
- **`ScanSession` derives the title fallback itself.** Simpler contract, but today's rule (plain-string user content only, not text blocks) is Claude-specific; moving it into the decoder as `Meta.TitleFallback` is what keeps the refactor byte-identical.
- **`Assistant{Text, []ToolCall}` instead of ordered parts.** Simpler struct, but it discards block order, which today's assistant text and arrow rendering preserve.
- **Keep `min_reader_version` at 2 and document the downgrade gap.** Rejected: the platform-blind restore, resume and merge paths of an older binary would pollute Claude's projects tree and permanently mislabel Codex rows.
- **Key rows by rollout-file identity to archive revert variants in v1.** Preserves every byte, but changes identity semantics (ambiguous prefixes, parent links, names, restore) for a case with zero samples; deferred behind a loud skip.
- **Larger-wins across revert variants.** Rejected: divergent files are not orderable by size; the rule would silently discard unique bytes and mis-point the location hint.

## Open Questions

Carried from [research.md § 6](./research.md#6-open-questions--unverified-assumptions), narrowed to what still matters:

1. Does `codex resume <uuid>` find a rollout restored into `sessions/YYYY/MM/DD/` when `state_5.sqlite` has no row for it? Gates the deferred resume follow-up only.
2. Revert rollout shape and semantics — zero local samples; v1 skips them (Assumption 5).
3. Whether the `SearchOnly` attachment asymmetry should be unified (display attachment text) — decide after the refactor lands.
4. Windows `CODEX_HOME` path handling — the relative-path design avoids timezone math; path separators are normalized to slashes in storage as `vault_files.relative_path` already is.
5. Whether Codex's resume/list pickers show a thread once or twice when both `rollout-….jsonl` and `rollout-….jsonl.zst` exist at the same relative path (the state `capy vault restore` produces beside an untouched compressed twin). Gates only the wording of restore's note; no local `.zst` sample yet.

## References

- [research.md](./research.md) — evidence base, counts, redacted sample lines (fixture seeds), reproduction commands
- [design-triage.md](./design-triage.md) — corroboration of the four design reviews that produced this revision
- [Vault architecture](../../../architecture.md#vault)
- [Original vault design](../../done/vault/design.md) — HMW, target user, schema; its *Not Doing* entry for Codex is superseded by this document
- [ADR-021](../../../adr/021-session-jsonl-format-resilience.md) — silent degradation with observability
- [ADR-025](../../../adr/025-vault-index-version-and-reindex.md) — `index_version`, DB-driven reindex, deferred generic input rendering
- [ADR-027](../../../adr/027-vault-is-sole-session-store.md), [ADR-028](../../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md) — vault as sole session store; federation
- [ADR-030](../../../adr/030-vault-session-names-and-latest-wins-merge.md) — names table; platform rename records not promoted
- Codex sources (`openai/codex` `main`): `codex-rs/rollout/src/{list,lib,policy,recorder,rollout_file_name,compression}.rs`, `codex-rs/protocol/src/{protocol,models,items}.rs`, `codex-rs/tui/src/app/{agents_overview_threads,agent_navigation}.rs`
- [Issue #77](https://github.com/serpro69/capy/issues/77)
