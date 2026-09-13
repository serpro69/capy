# Codex sessions in the vault — research notes (issue #77)

> **Status:** Research input — **the design has landed.** See [design.md](./design.md)
> (decisions for every item in §4; §5's suggested shape was revised: a shared *transcript
> model* with per-platform decoders replaces the per-format scanner trio),
> [implementation.md](./implementation.md) (Risk-First slices with verification gates) and
> [tasks.md](./tasks.md). This file remains the evidence base: format facts, verified counts,
> redacted fixture seeds (Appendix A) and reproduction commands (Appendix B). §6's open questions
> are carried forward in design.md § Open Questions and implementation.md § Deferred work.
> **Date:** 2026-09-12 (research) · 2026-09-13 (design landed)
> **Evidence base:** capy `master` @ `c68b855`; 164 real Codex rollouts under `~/.codex/sessions`
> (cli_version 0.124.0 → 0.154.0, Apr–Sep 2026, 96 MB); Codex CLI 0.147.0 installed locally;
> `openai/codex` `main` sources (`codex-rs/rollout/`, `codex-rs/history/`, `codex-rs/protocol/`);
> the three docs linked from the issue plus the `.md` variants of the official CLI/config/env pages.
> Counts below are indicative, not contractual (same caveat as vault design.md §JSONL Line Types).

## 1. What the vault assumes today (Claude Code coupling)

The vault design explicitly lists "Codex session support — different format and discovery paths;
future work" under *Not Doing* (`docs/feat/done/vault/design.md`). Every layer bakes in Claude Code's
layout. The coupling points, by file:

| Layer | Where | Claude Code assumption |
|---|---|---|
| Wire types | `internal/vault/scanner_types.go` | `jsonlLine{type, subtype, uuid, timestamp, sessionId, cwd, gitBranch, aiTitle, prUrl…, message{id, role, content}, attachment, toolUseResult}`; `contentBlock{type: text\|tool_use\|tool_result\|thinking, tool_use_id, input}` |
| FTS scanner | `internal/vault/scanner.go` `scan()` | `switch line.Type` on `user` / `assistant` / `ai-title` / `pr-link` / `attachment` / `system:away_summary`; tool_result lives in **user** entries; progressive assistant snapshots merged by `message.id`; `<system-reminder>` + `<command-*>` noise regexes; `excludedResultTools`/`diffResultTools` keyed by Claude tool names (`Read`, `Edit`, `Write`, `Bash`, `Agent`/`Task`) |
| `show` renderer | `internal/vault/render.go` `collectDisplay()` | same switch, same block model |
| TUI transcript | `internal/vault/transcript.go` `ParseTranscript()` | same switch; `Task`/`Agent` tool_use → subagent launch markers; `toolUseResult.structuredPatch` → diff view |
| Chunker | `internal/vault/chunker.go` | **format-agnostic** (consumes `ScanResult` only) — reusable as-is |
| Discovery | `internal/vault/discovery.go` | `config.ClaudeProjectsDir()` → `<mangled-project-dir>/<uuid>.jsonl` + `<uuid>/` sidecar dir (`subagents/agent-<id>.jsonl`, `tool-results/…`); `ProjectSessionDir()` mangles `/`,`.`→`-` |
| Import | `internal/vault/import.go` | `SessionFile.ProjectDir` = mangled dir; `ImportOptions.Project` substring-matches the **mangled dir name**; `resolveProjectPath` falls back to `config.UnmanglePath`; `subagentID()` parses `subagents/agent-<id>.jsonl`; `MessageCount == 0` → `StatusExcluded` |
| Schema | `internal/vault/store.go` | `vault_sessions.claude_project_dir TEXT NOT NULL`; no platform/format column; `minUUIDPrefix = 8`; `vault_files` = sidecars under `<uuid>/` |
| Reindex / merge | `internal/vault/reindex.go:126`, `merge.go:224` | re-run `scanSessionAndSubagents(uuid, raw_jsonl, files)` from **DB bytes only** — the parser must be selectable per row without disk context; `merge.go:328` filters on `claude_project_dir LIKE` |
| Restore | `internal/vault/restore.go`, `cmd/capy/vault.go:1265` | writes `<root>/<uuid>.jsonl` + `<root>/<uuid>/<rel>`; default root = `ClaudeProjectsDir()/<claude_project_dir>` |
| Resume | `cmd/capy/vault.go:884-1000` | `exec.LookPath("claude")`, `claude --resume <uuid>`, cwd fallback chain |
| Server sweep | `internal/server/server.go:213-275` | `vault.ProjectSessionDir(s.projectDir)` or `ClaudeProjectsDir()` (`CAPY_VAULT_SWEEP_ALL`) |
| TUI labels | `internal/vault/tui/render.go:258` | assistant header literally `"Claude"` |
| Tests | `internal/vault/fixtures_test.go` | Claude-shaped line builders (`userLine`, `assistantLine`, `aiTitleLine`, `userToolResultLine`, `writeSession`) |

Governing decisions that constrain the design: ADR-021 (silent-degradation parser resilience),
ADR-025 (`index_version` bump only across a released boundary; reindex is DB-driven),
ADR-027 (vault is the sole session store), ADR-028 (chunk corpus feeds `capy_search` federation),
ADR-030 (user names live in `vault_session_names`; Claude's `custom-title` is deliberately **not**
promoted into a vault name — the same question recurs for Codex's `/rename`).

## 2. Codex session storage model

Sources: `codex-rs/rollout/src/{recorder,list,compression,policy,session_index,rollout_file_name}.rs`,
`codex-rs/history/src/lib.rs`, official docs (config-advanced "Config and state locations", CLI
reference), the archiving blog post, and local `~/.codex`.

### 2.1 Locations & naming

- Root: `$CODEX_HOME` (default `~/.codex`) — analog of `CLAUDE_CONFIG_DIR`.
- Active rollouts: `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-YYYY-MM-DDThh-mm-ss-<thread_uuid>.jsonl`
  (`SESSIONS_SUBDIR = "sessions"`). **Date dir and filename timestamp are local time**
  (`OffsetDateTime::now_local()` in `recorder.rs precompute_new_rollout_path`; format
  `[year]-[month]-[day]T[hour]-[minute]-[second]` in `rollout_file_name.rs`). The
  `session_meta.payload.timestamp` inside the file is UTC. Verified locally: file
  `rollout-2026-09-02T14-43-05-…` ↔ meta `2026-09-02T12:43:05.050Z` (Europe/Oslo, +02:00).
  ⇒ the on-disk path is **not reconstructible from UTC metadata alone**; restore must keep the
  original relative path.
- Revert variant: `thread/revert` writes a **new immutable file** for the same thread id:
  `rollout-<ts>-<thread_id>_<rollout_id>.jsonl` (`recorder.rs:95-105`). Not observed locally
  (0/164), but it means one thread id can map to several rollout files.
- Archived: `$CODEX_HOME/archived_sessions/YYYY/MM/DD/…` (`ARCHIVED_SESSIONS_SUBDIR`); archive is a
  **move**, file unchanged. `codex archive|unarchive|delete <SESSION>`, TUI `/archive`, `/delete`
  (permanent; also deletes spawned descendant sessions). Archived sessions are hidden from the
  `resume`/`fork` pickers.
- Compression (landed ~0.152/0.153, PRs #41357, #42039): a best-effort background worker
  compresses rollouts **older than 7 days** to `<name>.jsonl.zst` (zstd level 3, run marker
  `rollout-compression.lock`); readers accept both, and append paths "materialize" the plain
  `.jsonl` back. Not yet observed locally (installed CLI is 0.147). Discovery must accept
  `*.jsonl.zst`; capy already depends on `klauspost/compress/zstd` (vault `codec.go`).
- File mode: rollouts are created **0644** (world-readable, noted in the blog and Codex issue
  tracker). Vault restore writes 0600 — fine.
- Sizes (local): 164 files, 96.2 MB total, median 403 KB, max 4.2 MB; 9–806 lines/file
  (median 127). Comparable to Claude sessions (design.md: avg 536 KB).

### 2.2 Auxiliary state (outside the rollout)

| File | Content | Relevance |
|---|---|---|
| `session_index.jsonl` | append-only `{id, thread_name, updated_at}`; written by `/rename` (`session_index.rs append_thread_name`); last entry per id wins | the only place a **user-chosen thread name** lives (7/164 threads named locally). Analog of Claude's `custom-title`. |
| `history.jsonl` | `{session_id, ts, text}` prompt history; `[history].persistence = save-all\|none` | not needed (prompts are in the rollout) |
| `state_5.sqlite` `threads` | `id, rollout_path, cwd, title (= first user message), name, archived, archived_at, git_branch, cli_version, history_mode, source, thread_source, first_user_message, preview…`; `thread_spawn_edges(parent_thread_id, child_thread_id, status)` | Codex's index; **derived** from rollouts (`backfill_state`, `thread_history_projection_state.next_rollout_byte_offset`). Resume lookup: DB first, then filesystem scan of `sessions/` and `archived_sessions/` by UUID in filename (`list.rs find_thread_path_by_id_str_in_subdir`). Do **not** depend on it (live DB, schema versioned `_5`). |
| `thread_history_1.sqlite` | projection of rollout items (`item_type`: reasoning, commandExecution, agentMessage, mcpToolCall, userMessage, fileChange, …) | confirms rollouts are the source of truth |

### 2.3 Identity

- Thread ids are **UUIDv7** (time-ordered). Filename uuid == `session_meta.payload.id`.
- Consequence for capy's git-style 8-char prefix (`minUUIDPrefix = 8`, `shortUUID` display):
  the first 8 hex chars encode the top 32 bits of a ms timestamp (≈65 s granularity), so sessions
  started close together collide. Local corpus: **22 colliding 8-char prefixes** among 164 files
  (5 files share `019dca5d`); **0 collisions at 12 chars**. `AmbiguousUUIDError` already handles
  this correctly but the UX (list shows 8 chars) degrades for Codex.
- `session_meta.session_id` = root thread id (equals `id` except for sub-threads); `forked_from_id`
  set on `codex fork` results (new uuid — same property as Claude `--fork-session`).

### 2.4 Sub-agents, forks, reviews

- A spawned sub-agent is a **separate rollout file in the same tree**, not a sidecar. Its
  `session_meta.payload.source = {"subagent":{"thread_spawn":{parent_thread_id, depth, agent_path,
  agent_nickname, agent_role}}}` (newer versions also lift `parent_thread_id`, `agent_nickname`,
  `agent_role`, `agent_path`, `multi_agent_version` to the top of the payload). `SubAgentSource`
  also has `Review` and `Compact` variants (review / compaction sub-threads).
- Parent side: legacy `event_msg/collab_agent_spawn_end{new_thread_id, new_agent_nickname,
  new_agent_role, prompt, model}`; paginated `item_completed` with
  `item.type == "SubAgentActivity"{agent_thread_id, agent_path, kind}`; plus `function_call`
  `spawn_agent` / `wait_agent` / `send_input` / `close_agent` (analog of Claude `Task`/`Agent`).
- Local: 13 sub-agent rollouts (`thread_spawn_edges`: 11 open, 2 closed). Each child has exactly
  **1 human turn** (the spawn prompt arrives as a `user_message`), so the `MessageCount == 0`
  exclusion would *not* drop them — they would import as ordinary sessions.

### 2.5 Lifecycle differences that matter to a vault

| Concern | Claude Code | Codex |
|---|---|---|
| Compaction | `/compact` **rewrites** the JSONL (pre-compaction content lost → vault's known v1 gap) | **append-only**: a `compacted` line with `replacement_history` is appended; prior lines stay. No pre-compaction loss. |
| Auto-cleanup | 30-day retention | none found; explicit `codex delete` / `/delete`; 7-day **compression** instead |
| Archive | n/a | move to `archived_sessions/` (same uuid, same bytes) |
| Rewrite of same uuid | compaction | `thread/revert` → new file with `_<rollout_id>` suffix |
| Resume | `claude --resume <uuid>` | `codex resume <uuid\|name>` (`-C/--cd <DIR>` global; prompts on cwd mismatch, `tui.resume_cwd`) — falls back to a filesystem scan by uuid when the state DB has no row ⇒ a restored file *should* be resumable (**unverified**, see §6) |

## 3. Codex rollout JSONL format

### 3.1 Envelope

`RolloutLine { timestamp: String, ordinal: Option<u64>, #[serde(flatten)] item: RolloutItem }`
(`codex-rs/history/src/lib.rs:262`), wire-tagged `type` + `payload` (`rollout_payload.rs`,
`#[serde(tag = "type", rename_all = "snake_case")]`):

```json
{"timestamp":"2026-09-02T12:43:08.280Z","ordinal":0,"type":"session_meta","payload":{...}}
```

- `timestamp` on **every** line (RFC3339, ms, UTC) — richer than Claude (`ai-title` lines have none).
- `ordinal` present from cli 0.147 (`history_mode: "paginated"`); equals the 0-based line index in
  the sampled paginated file (806/806) but is a **Codex history position**, not guaranteed to be a
  line index — capy should keep its own `line_index` anchor.
- `RolloutItem` variants → wire `type`: `session_meta`, `response_item`, `compacted`,
  `turn_context`, `event_msg`, `token_usage_record`, `world_state`,
  `inter_agent_communication`, `inter_agent_communication_metadata`, `security_risk_score`,
  `retained_context`, `realtime_item`. Observed locally (24,020 lines):

  | type | count | handling sketch |
  |---|---|---|
  | `response_item` | 16,946 | **the transcript** (see 3.3) |
  | `event_msg` | 7,582 | human turns + turn boundaries (see 3.4) |
  | `turn_context` | 269 | per-turn `cwd`, model, sandbox/approval, timezone — metadata only |
  | `session_meta` | 164 | first line; session metadata (see 3.2) |
  | `world_state` | 49 | `{full, state}` snapshots (up to ~hundreds of KB) — skip |
  | `compacted` | 5 | `{message, replacement_history:[ResponseItem…], …}` — skip for FTS (content duplicates earlier lines); mark in display |
  | `inter_agent_communication_metadata` | 4 | `{trigger_turn}` — skip |
  | `token_usage_record` | 1 | usage — skip |

### 3.2 `session_meta` (first line)

`SessionMeta` (`codex-rs/protocol/src/protocol.rs:3062`): `session_id`, `id`, `forked_from_id?`,
`parent_thread_id?`, `timestamp`, `cwd`, `originator` (`codex-tui`, `codex_exec`),
`cli_version`, `source` (`cli` | `exec` | `vscode` | `mcp` | `{subagent:{…}}` | …), `thread_source?`,
`agent_nickname?`, `agent_role?`, `agent_path?`, `model_provider`, `base_instructions` (full system
prompt text — large, skip), `git?: {commit_hash, branch, repository_url}`, `history_mode?`
(`legacy` | `paginated`), `context_window?`, `runtime_workspace_roots?`.

Vault field mapping: `uuid` ← `payload.id`; `project_path` ← `payload.cwd` (**no unmangling
needed**; also on every `turn_context`); `git_branch` ← `payload.git.branch` (absent for non-git
cwd — 2/164 + a few); `start_time` ← first line `timestamp`; `end_time` ← last line `timestamp`.
Nine distinct key sets across 0.124→0.154 — the payload is **version-volatile** (`session_id`,
`history_mode`, `context_window`, `parent_thread_id` appear only in newer versions).

### 3.3 `response_item` payloads (`ResponseItem`, `#[serde(tag="type", rename_all="snake_case")]`)

Persisted variants (`policy.rs should_persist_response_item`): `message`, `agent_message`,
`reasoning`, `local_shell_call`, `function_call`, `function_call_output`, `custom_tool_call`,
`custom_tool_call_output`, `tool_search_call`, `tool_search_output`, `web_search_call`,
`image_generation_call`, `compaction`, `configuration_update`, `context_compaction`.
Observed: `function_call` 5,533 / `function_call_output` 5,533 / `reasoning` 2,795 / `message`
2,001 / `custom_tool_call(_output)` 470 each / `web_search_call` 130 / `tool_search_*` 5 / `agent_message` 4.

| payload.type | shape (observed) | vault role / notes |
|---|---|---|
| `message` | `{role: user\|assistant\|developer, content:[{type: input_text\|output_text\|input_image\|input_audio, text}], id?, phase?: commentary\|final_answer}` | **assistant** → `role=assistant` row (canonical assistant text; 1,087 local). **developer** → skip (system prompt fragments: `<permissions instructions>`, `<skills_instructions>`, `<collaboration_mode>`…). **user** → mostly *injected context*, NOT the human prompt — see 3.5. No progressive snapshots; `id` present on only ~20% of messages (newer versions), so dedup-by-id is not needed/available. |
| `function_call` | `{name, arguments: <JSON **string**>, call_id, id?}` | tool_use analog. Names: `exec_command` (4,610; args `{cmd, workdir, yield_time_ms, max_output_tokens}`), `write_stdin`, `wait`, `update_plan`, MCP tools appear as **bare tool names** (`capy_search` 210, `capy_index` 137, `capy_batch_execute` 114, …, no `mcp__` prefix; server name only in paginated `McpToolCall` items), `spawn_agent`/`wait_agent`/`close_agent`/`send_input` (sub-agents), `search_openai_docs`… |
| `function_call_output` | `{call_id, output: string \| [{type: input_text\|input_image\|input_audio\|encrypted_content, text}]}` (5,500 string / 33 array) | tool_result analog → `role=tool` row, correlated by `call_id` (same idea as `tool_use_id`). `exec_command` output carries a wrapper header: `Chunk ID: …\nWall time: …\nProcess exited with code N\nOriginal token count: N\nOutput:\n…` (4,655/5,500) — decide whether to strip the header before FTS (keep "Process exited with code" — it is searchable signal). |
| `custom_tool_call` | `{name: exec \| apply_patch, input: string, call_id, status}` | `apply_patch` (24): input is Codex's `*** Begin Patch / *** Add File: / *** Update File: … *** End Patch` text — the **Edit/Write + structuredPatch analog** (diff is in the *call*, not a sibling field). `exec` (446): JS code run in a hosted tool sandbox (`tools.web__run(…)`) — treat like Bash. |
| `custom_tool_call_output` | `{call_id, output: <JSON string {output, metadata{exit_code, duration_seconds}}>}` | `role=tool` row; needs a second JSON decode of `output`. |
| `reasoning` | `{summary:[{type: summary_text, text}], content: null\|[…], encrypted_content}` | thinking analog → skip (summary is usually `[]`; if indexing summaries is ever wanted it is a scanner change → ADR-025 bump rules). |
| `web_search_call` | `{status, action:{type: search, query, queries[]}}` | index the query strings on the assistant row (like a tool summary). |
| `agent_message` | `{author, recipient, content:[input_text], …}` | inter-agent messages (4) → `role=system` or skip. |
| `tool_search_call/output` | tool discovery (namespaces + schemas — large) | skip. |
| `local_shell_call`, `image_generation_call`, `compaction`, `configuration_update`, `context_compaction` | not observed | skip by default (ADR-021: unknown → skip, raw blob preserves). |

### 3.4 `event_msg` payloads — and the legacy / paginated split

`session_meta.history_mode` (absent ⇒ `legacy`; `"paginated"` from cli 0.147, also `"legacy"`
explicitly in 0.146) changes **which events are persisted** (`policy.rs should_persist_event_msg`):

| event | legacy (<0.147, 131 files) | paginated (≥0.147, 43 files) |
|---|---|---|
| `user_message` `{message, images, local_images, text_elements}` | ✅ 168 — **the human prompt** | ✗ (never written) |
| `agent_message` `{message, phase}` | ✅ 926 — verbatim duplicate of `response_item` assistant text (verified byte-equal) | ✗ |
| `item_completed` `{thread_id, turn_id, item: TurnItem}` | only `FunctionCallOutput`/`Plan`/`Extension(Sleep)`/completed `SubAgentActivity` | ✅ 1,585 — carries **`UserMessage`**, `AgentMessage`, `Reasoning`, `CommandExecution` (`command[]`, `cwd`, `exit_code`, `aggregated_output`), `McpToolCall` (`server`, `tool`, `arguments`, `result`), `FileChange` (`changes{path:{type,content}}`), `WebSearch`, `ImageView`, `SubAgentActivity`, `ContextCompaction`, `Extension`, … |
| `task_started` / `task_complete` | ✅ (`turn_id`, `last_agent_message`) | ✅ — natural **turn boundaries** |
| `token_count`, `turn_aborted`, `thread_settings_applied` | ✅ | ✅ |
| `exec_command_end`, `mcp_tool_call_end`, `web_search_end`, `patch_apply_end`, `context_compacted`, `collab_*` | ✅ | ✗ |

`TurnItem` (`codex-rs/protocol/src/items.rs:45`) variant tags are **PascalCase** in the wire
(`"UserMessage"`, `"CommandExecution"`, …), unlike the snake_case `ResponseItem`.

### 3.5 Where the human prompt actually is

`response_item` `message role=user` lines are dominated by context Codex injects as user-role
input: `<environment_context>` (165), `# AGENTS.md instructions for …` (86+), `<skill>…` (67),
`<turn_aborted>` (25), `<subagent_notification>` (10), the `<INSTRUCTIONS>`-wrapped AGENTS.md body
(first line blank — a prefix filter misses it), plus `developer`-role blocks. Only the newest
versions tag these via `internal_chat_message_metadata_passthrough.content_item_kinds`
(`user.text` vs `agents_md.instructions`, `environments.environment_context`) — 49/511 locally.

The reliable human-turn source is the **event stream**:
- legacy: `event_msg/user_message.message` — verified equal to the non-noise `response_item` user
  text, and it is written one line **after** its `response_item` twin;
- paginated: `event_msg/item_completed` with `item.type == "UserMessage"` →
  `item.content[{type: "text", text}]` (30 items vs 31 `task_started` in the largest local file).

Both exist across the whole 0.124→0.154 range. ⇒ **Design rule candidate:** human turns come from
events; assistant/tool rows come from `response_item`; `response_item` user messages are used only
as a fallback (with a noise filter) when a session has no user events.

### 3.6 Claude ↔ Codex concept map

| Vault concept | Claude Code | Codex |
|---|---|---|
| Session file | `<projects>/<mangled-cwd>/<uuid>.jsonl` | `sessions/YYYY/MM/DD/rollout-<local-ts>-<uuid>[_<rollout_id>].jsonl[.zst]` (+ `archived_sessions/`) |
| Line envelope | `{type, uuid, timestamp?, sessionId, cwd, gitBranch, message{…}, …}` | `{timestamp, ordinal?, type, payload{…}}` |
| Session uuid | UUIDv4, filename | UUIDv7, filename + `session_meta.payload.id` |
| Project path | `cwd` on user lines, else unmangle dir | `session_meta.payload.cwd` (+ `turn_context.cwd`) |
| Git branch | `gitBranch` on user lines | `session_meta.payload.git.branch` (+ `commit_hash`, `repository_url`) |
| Title | last `ai-title`, else first significant user string | **no title record** → first human turn (Codex's own `threads.title` is the same); `/rename` name in `session_index.jsonl` (outside the file) |
| Human turn | `user` line, string or `text` blocks | `event_msg/user_message` (legacy) / `item_completed.UserMessage` (paginated) |
| Assistant turn | `assistant` lines, progressive snapshots merged by `message.id` | `response_item/message role=assistant` (single, final) |
| Thinking | `thinking` block → skipped | `response_item/reasoning` → skip |
| Tool call | `tool_use{id, name, input}` in assistant content | `function_call{call_id, name, arguments-as-string}` / `custom_tool_call{call_id, name, input}` |
| Tool result | `tool_result{tool_use_id, content}` inside **user** lines | `function_call_output{call_id, output}` / `custom_tool_call_output{call_id, output-json-string}` as **their own lines** |
| Shell | `Bash{command}` | `exec_command{cmd, workdir}` (wrapper header in output); `exec` custom tool |
| File read dump (FTS-excluded) | `Read`, `NotebookRead` | none — reads are `exec_command` `sed -n`/`cat` (cannot exclude by tool name; only the size cap applies) |
| Edit diff | `Edit`/`Write` + `toolUseResult.structuredPatch` | `apply_patch` custom tool: patch text is the **call input**; output `Success. Updated the following files:` |
| Sub-agent | sidecar `<uuid>/subagents/agent-<id>.jsonl` (+ `.meta.json`), `Task`/`Agent` tool_use | **separate top-level rollout**; child `session_meta.source.subagent.thread_spawn.parent_thread_id`; parent `collab_agent_spawn_end.new_thread_id` / `SubAgentActivity.agent_thread_id`; `spawn_agent` function_call |
| Queued prompt | `attachment{queued_command}` | not observed (steer/queue events not persisted) |
| PR link | `pr-link` | none observed |
| Compaction | file rewritten | `compacted` line appended (`replacement_history`) |
| System noise | `<system-reminder>`, `<command-*>` | `developer` role, `<environment_context>`, `# AGENTS.md instructions`, `<skill>`, `<turn_aborted>`, `<subagent_notification>` |
| Turn boundary | heuristic: human text starts a turn | explicit `task_started{turn_id}` / `task_complete` (also `turn_context`) |
| Version marker | `version` on most lines | `session_meta.cli_version` (+ `history_mode`) |

## 4. Design decisions the design phase must settle

1. **Format identification.** Reindex and merge re-scan from DB bytes (`reindex.go:126`,
   `merge.go:224`), and `show`/restore/resume need the platform too. Options: (a) sniff the first
   line (`"type":"session_meta"` ⇒ Codex; `session_meta` is *always* the first line) — zero schema
   change, works for legacy rows; (b) persist a `platform` column (migration `0006`) — cheap
   dispatch, filterable (`vault list --platform`), explicit in `list`/`stats`. Recommendation:
   sniff at import to *detect*, persist to *dispatch and display*; keep the sniffer as the fallback
   for rows written before the column existed (all Claude).
2. **`claude_project_dir` semantics for Codex.** The column is `NOT NULL` and restore/merge key
   on it. Codex has no mangled dir; the useful restore hint is the **original relative path**
   (`sessions/2026/09/02/rollout-2026-09-02T14-43-05-<uuid>.jsonl`), because the local-time
   filename cannot be rebuilt from UTC metadata (§2.1). Either generalize the column's meaning
   ("platform location hint") and store the relative rollout path there, or add a dedicated
   column. Changing the column *name* is a wide refactor for little value.
3. **Sub-agent modeling.** Children are first-class rollouts that discovery will find on their
   own. Options: (A) archive each child as its own `vault_sessions` row plus a `parent_uuid` link
   (new column; TUI marker opens by uuid; `list` needs a "hide children" default); (B) fold
   children into the parent's `vault_files` as `subagents/agent-<child-uuid>.jsonl` to reuse the
   existing `subagent_id` anchor, TUI open path and chunk tagging — but then the same bytes are
   also a standalone discovery hit (must dedupe/exclude), a child can be imported before its parent
   exists, and Codex `/delete` semantics (parent deletes descendants) differ. Hybrid: (A) for
   storage/identity, plus deriving launch markers from `spawn_agent`/`SubAgentActivity` so the TUI
   can open the child session. Must also decide how `MessageCount`/`list` treat children.
4. **UUIDv7 prefixes.** Keep `minUUIDPrefix = 8` (ambiguity is already an error path) but the
   8-char `shortUUID`/`shortID` display is misleading for Codex (22/164 collide locally; 12 chars
   → 0). Options: 12-char display for Codex rows, or a suffix-based short id.
5. **Human-turn extraction policy** (§3.5): events first; `response_item` user only as fallback;
   `developer` role skipped; noise list (`<environment_context>`, `# AGENTS.md instructions`,
   `<skill>`, `<turn_aborted>`, `<subagent_notification>`, `<INSTRUCTIONS>`); `phase` ignored.
   Legacy `agent_message` events skipped (duplicate).
6. **Tool mapping** (§3.3): `function_call` + `custom_tool_call` → assistant-row tool summaries
   (`exec_command` → `"Bash <cmd>"`-style label from the `cmd` field of the *stringified*
   `arguments`; `apply_patch` → `Edit`-style label, file names parsed from `*** Update File:` lines;
   MCP tools → bare name, per the deferred generic-input rule in ADR-025); outputs → `role=tool`
   rows correlated by `call_id`; decide on stripping the `exec_command` wrapper header; no
   Read-style exclusion exists (size cap only); `apply_patch` display can reuse the TUI diff view by
   converting the patch text (a `diff.go` sibling), `show` keeps the verbatim body.
7. **Turn/line indices.** Use `task_started`/`turn_id` for `TurnIndex` (exact) or keep the
   "human text starts a turn" heuristic (shared with Claude, simpler). `LineIndex` stays the
   physical line index (viewer anchor); do not reuse `ordinal`.
8. **Title.** First human turn (existing fallback path, sanitized, 120 runes). Promoting
   `session_index.jsonl` thread names into `vault_session_names` would contradict ADR-030's
   Claude decision — recommend *not* in v1, document as a follow-up.
9. **Discovery & project scoping.** Walk `$CODEX_HOME/{sessions,archived_sessions}/**/rollout-*.jsonl{,.zst}`;
   parse `(local-ts, thread_id, rollout_id?)` from the filename; project filtering requires reading
   the first line (`session_meta.cwd`) — cheap (`bufio` first line), no directory-name proxy.
   Server sweep (`server.go vaultSweep`) currently maps `s.projectDir` to a Claude dir; for Codex
   it must filter by `cwd == projectDir`. How the MCP server knows it runs under Codex is
   **unknown** (no `CAPY_PLATFORM`; `capy setup --platform codex` writes `.codex/config.toml`
   but sets no env) — simplest: sweep whichever platform roots exist on disk.
10. **Content identity across compression/archive.** Hash and store the **plain JSONL bytes**
    (decompress `.jsonl.zst` before hashing) so a later compressed or archived copy of the same
    thread hashes identical → `StatusSkipped`. Same rule the vault already applies to its own
    `encoding` column.
11. **Revert files** (`_<rollout_id>`): one thread id, several files. Either key `vault_sessions.uuid`
    by rollout id (filename) and store the thread id separately, or treat later rollouts as
    replacements (larger-wins). Unobserved locally — flag as an assumption to verify.
12. **Restore / resume.** Restore to `$CODEX_HOME/<relative path>` (needs decision 2); resume via
    `codex resume <uuid>` with `-C <dir>` for the cwd fallback chain; `capy vault resume` branches
    on platform; `exec.LookPath("codex")`. Verify Codex's DB-less filesystem fallback actually
    resumes a restored file (§6).
13. **`index_version`.** Adding a Codex scanner does not change Claude extraction ⇒ no bump for
    existing rows (ADR-025 rule). Codex rows stamp `currentIndexVersion`. A later Codex-only
    scanner change would still force a vault-wide reindex; a per-platform version is probably
    over-engineering — note and accept.
14. **Merge compatibility.** `merge.go` feature-detects source columns already (v1 `encoding`,
    pre-0005 names) — a new `platform` column needs the same treatment; `--project` filter on
    `claude_project_dir LIKE` must switch to `project_path` (or both).
15. **Display.** TUI header `"Claude"` → platform-aware label; `list`/`stats` show platform;
    `capy_vault_search`/`capy_search` result tagging (`session:<uuid>`) unchanged.
16. **Tests.** Codex fixture builders for *both* history modes (legacy `user_message` events and
    paginated `item_completed.UserMessage`), sub-agent child+parent pair, `apply_patch`,
    `.jsonl.zst` discovery, filename parser incl. `_<rollout_id>`; a real-data canary over
    `~/.codex/sessions` that `t.Skip`s when absent (mirrors ADR-021's canary idea).
17. **Docs.** `docs/architecture.md` Vault section, README vault commands, `.capy/AGENTS.md`
    vault wording ("archived transcripts"), `design.md` *Not Doing* entry superseded, an ADR for
    the multi-platform scanner seam, `capy doctor` vault check mentions.

## 5. Recommended shape (for the design doc to confirm)

- Introduce a **`SessionFormat`/platform seam** in `internal/vault`: `DetectFormat(firstLine)`,
  and a per-format scanner producing the *same* `ScanOutput`/`ScanResult` so `chunker.go`,
  `vault_fts`, `vault_chunks`, RRF federation, reindex batching and merge stay untouched. Codex
  needs its own render/transcript readers too (or a shared intermediate "display model" — the three
  Claude readers already duplicate the block model, so a shared model is the cheaper long-term
  path but a larger refactor).
- Discovery becomes a set of `Discoverer`s (`claude`, `codex`) returning a common `SessionFile`
  that carries platform + relative path; import/restore/resume dispatch on it.
- Migration `0006`: `platform TEXT NOT NULL DEFAULT 'claude-code'` (+ optional `parent_uuid`),
  DDL shared between `schemaSQL` and the migration as `0005` does.

## 6. Open questions / unverified assumptions

1. Does `codex resume <uuid>` find a rollout **restored** into `sessions/YYYY/MM/DD/` when
   `state_5.sqlite` has no row for it? Source suggests yes (filesystem fallback in `list.rs`), and
   the state DB is backfilled from rollouts — needs a manual test with the installed CLI.
2. Revert rollouts (`_<rollout_id>` suffix) — 0 local samples; shape and resume semantics unverified.
3. Compressed `.jsonl.zst` rollouts — 0 local samples (CLI 0.147 predates the worker); standard
   zstd frames are assumed.
4. How the capy MCP server can tell it is hosted by Codex (for the startup sweep scope).
5. `world_state`, `security_risk_score`, `retained_context`, `realtime_item`, `local_shell_call`,
   `image_generation_call` payloads — unobserved; skip-by-default per ADR-021.
6. Whether sub-agent rollouts should be searchable/listable on their own or only through their parent.
7. Windows path handling of `CODEX_HOME` and the local-time filename (time zone at creation vs at restore).

## 7. Reference pointers

- Issue: https://github.com/serpro69/capy/issues/77
- Codex sources (`openai/codex` `main`): `codex-rs/history/src/lib.rs` (`RolloutItem`, `RolloutLine`, `CompactedItem`),
  `codex-rs/history/src/rollout_payload.rs` (wire tags), `codex-rs/rollout/src/policy.rs` (what is persisted),
  `codex-rs/rollout/src/recorder.rs` (`precompute_new_rollout_path`), `codex-rs/rollout/src/rollout_file_name.rs`,
  `codex-rs/rollout/src/compression.rs` (`.zst`, 7-day age), `codex-rs/rollout/src/list.rs` (resume lookup),
  `codex-rs/rollout/src/session_index.rs`, `codex-rs/protocol/src/protocol.rs` (`SessionMeta`, `SessionSource`,
  `SubAgentSource`), `codex-rs/protocol/src/models.rs` (`ResponseItem`, `ContentItem`),
  `codex-rs/protocol/src/items.rs` (`TurnItem`, `UserMessageItem`).
- Docs: https://developers.openai.com/codex/cli/features , https://developers.openai.com/codex/cli/reference
  (`codex resume|fork|archive|unarchive|delete`, `/rename`, `/archive`, `/delete`),
  https://developers.openai.com/codex/config-file/config-advanced ("Config and state locations", hooks incl. `SessionEnd`),
  https://developers.openai.com/codex/config-file/environment-variables (`CODEX_HOME`),
  https://developers.openai.com/codex/changelog (rollout compression PRs #41357/#42039; archive/fork PRs #37367-#37371),
  https://codex.danielvaughan.com/2026/06/02/codex-cli-session-archiving-lifecycle-management-v0136/
  (archive lifecycle; note its `codex archive 2026-06-01T14-30-00Z-abc123` example id format does not match the real CLI, which takes a UUID or session name).
- Local corpus used for counts: `~/.codex/sessions` (164 rollouts), `~/.codex/session_index.jsonl`, `~/.codex/state_5.sqlite` (read-only).

## Appendix A — Redacted sample lines (fixture seeds)

One real line per record type, copied from the local corpus and then redacted: paths →
`/home/user/Projects/proj` or `/tmp/proj`, repo URL → `org/repo`, long strings cut to ~90 chars
(`…`), prompt/response text replaced by generic wording. UUIDs are real UUIDv7 values (note the
`7` version nibble) and are safe to keep. Timestamps are UTC. **`ordinal` and
`internal_chat_message_metadata_passthrough` appear only in paginated (≥0.147) files.** Use these
as the shapes for `fixtures_test.go`-style builders; keep both a legacy and a paginated variant.

### A.1 Session metadata

```jsonl
// legacy (0.130): no ordinal, no history_mode, no session_id
{"timestamp":"2026-05-13T19:11:47.906Z","type":"session_meta","payload":{"id":"019e22ba-4c15-7ae0-a903-255537a6a1b3","timestamp":"2026-05-13T19:04:55.061Z","cwd":"/home/user/Projects/proj","originator":"codex-tui","cli_version":"0.130.0","source":"cli","thread_source":"user","model_provider":"openai","base_instructions":{"text":"You are Codex, a coding agent based on G…"},"git":{"commit_hash":"17985d49e752c3f3c5aeaedd55364f12cc1a43bc","branch":"master","repository_url":"git@github.com:org/repo.git"}}}
// paginated (0.152): ordinal, session_id, history_mode, context_window; git absent for a non-git cwd
{"timestamp":"2026-09-02T12:43:08.280Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a06224-f7d9-71d3-b6bb-4ea07576fdb3","id":"01a06224-f7d9-71d3-b6bb-4ea07576fdb3","timestamp":"2026-09-02T12:43:05.050Z","cwd":"/tmp/proj","originator":"codex-tui","cli_version":"0.152.1","source":"cli","thread_source":"user","model_provider":"openai","base_instructions":{"text":"You are Codex, an agent based on GPT-5. …","provenance":{"type":"model","model":"gpt-5.6-sol"}},"history_mode":"paginated","context_window":{"window_id":"01a06224-f7d9-71d3-b6bb-4eb9b471a48a"}}}
// sub-agent child (0.125): source is an object; newer versions also lift parent_thread_id/agent_* to the payload top level
{"timestamp":"2026-04-25T19:05:08.569Z","type":"session_meta","payload":{"id":"019dc608-0061-7d90-a4e9-fca142504625","timestamp":"2026-04-25T19:05:06.466Z","cwd":"/home/user/Projects/proj","originator":"codex-tui","cli_version":"0.125.0","source":{"subagent":{"thread_spawn":{"parent_thread_id":"019dc606-f552-7f93-b9a1-c5620b23b8dd","depth":1,"agent_path":null,"agent_nickname":"Boole","agent_role":"code-reviewer"}}},"agent_nickname":"Boole","agent_role":"code-reviewer","model_provider":"openai","base_instructions":{"text":"…"},"git":{"commit_hash":"cb1ad9a7c33419f424fad1f5c6c2a499edc7ced4","branch":"feat/x","repository_url":"git@github.com:org/repo.git"}}}
// turn_context (one per turn; metadata only)
{"timestamp":"2026-09-02T12:43:08.710Z","ordinal":7,"type":"turn_context","payload":{"turn_id":"01a06225-0462-7b61-807f-4c5175c52572","cwd":"/tmp/proj","workspace_roots":["/tmp/proj"],"current_date":"2026-09-02","timezone":"Europe/Oslo","approval_policy":"on-request","sandbox_policy":{"type":"workspace-write","network_access":false},"permission_profile":"…","model":"gpt-5.6-sol","personality":"pragmatic","collaboration_mode":{"mode":"default","settings":{"model":"gpt-5.6-sol","reasoning_effort":"xhigh","developer_instructions":"…"}},"multi_agent_version":"v2","effort":"xhigh","summary":"auto"}}
```

### A.2 Messages (`response_item` / `message`)

```jsonl
// developer role: system-prompt fragments → skip
{"timestamp":"2026-09-02T12:43:08.704Z","ordinal":2,"type":"response_item","payload":{"type":"message","id":"msg_01a06225-0620-75b2-a7a4-9667890893d7","role":"developer","content":[{"type":"input_text","text":"<skills_instructions>\n## Skills\nA skill is a set of local instructions…"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a06225-0462-7b61-807f-4c5175c52572","create_time":1788352988.7040403,"content_item_kinds":["host_skills.instructions","permissions.instructions"]}}}
// user role, injected context (NOT a human turn) — the dominant case
{"timestamp":"2026-09-02T12:43:08.704Z","ordinal":5,"type":"response_item","payload":{"type":"message","id":"msg_01a06225-0620-75b2-a7a4-96948e8943db","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/tmp/proj</cwd>\n  <shell>zsh</shell>\n  <current_date>…"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a06225-0462-7b61-807f-4c5175c52572","create_time":1788352988.7040434,"content_item_kinds":["environments.environment_context"]}}}
// user role, human prompt (legacy file; no id, no passthrough) — its event twin is the NEXT line (A.3)
{"timestamp":"2026-05-13T19:11:48.041Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Please review the design documents under docs/ and report inconsistencies."}]}}
// assistant (canonical assistant text; phase = commentary | final_answer)
{"timestamp":"2026-05-13T19:12:00.963Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I'll review the three documents and trace each claim against the code."}],"phase":"commentary"}}
```

### A.3 Human turns and turn boundaries (`event_msg`)

```jsonl
// legacy human turn (0.124–0.146) — byte-equal to the response_item user text on the previous line
{"timestamp":"2026-05-13T19:11:48.042Z","type":"event_msg","payload":{"type":"user_message","message":"Please review the design documents under docs/ and report inconsistencies.","images":[],"local_images":[],"text_elements":[]}}
// legacy assistant duplicate (verbatim copy of the assistant response_item) → skip
{"timestamp":"2026-05-13T19:12:00.963Z","type":"event_msg","payload":{"type":"agent_message","message":"I'll review the three documents and trace each claim against the code.","phase":"commentary","memory_citation":null}}
// paginated human turn (≥0.147) — item.type is PascalCase; content[].type is "text"
{"timestamp":"2026-09-05T11:23:32.138Z","ordinal":10,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-cbc4-7d23-9f9d-02d710c07ed7","turn_id":"01a0714f-3474-7ef1-b960-bdad01a46df8","item":{"type":"UserMessage","id":"01a0714f-37aa-7f92-869a-f8c1130b8fdb","content":[{"type":"text","text":"Let's work on issue #81. Use kk:design to discuss and design the feature.","text_elements":[]}]},"started_at_ms":1788607412138,"completed_at_ms":1788607412138}}
// paginated assistant item (duplicate of the assistant response_item; note content[].type "Text")
{"timestamp":"2026-09-05T11:23:36.321Z","ordinal":13,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-cbc4-7d23-9f9d-02d710c07ed7","turn_id":"01a0714f-3474-7ef1-b960-bdad01a46df8","item":{"type":"AgentMessage","id":"msg_085a52…","content":[{"type":"Text","text":"I'm turning issue #81 into a reviewed design direction."}],"phase":"commentary"},"started_at_ms":1788607415164,"completed_at_ms":1788607416321}}
// turn boundaries (both modes)
{"timestamp":"2026-09-02T12:43:08.280Z","ordinal":1,"type":"event_msg","payload":{"type":"task_started","turn_id":"01a06225-0462-7b61-807f-4c5175c52572","started_at":1788352988,"model_context_window":258400,"collaboration_mode_kind":"default"}}
{"timestamp":"2026-09-02T12:43:10.581Z","ordinal":13,"type":"event_msg","payload":{"type":"task_complete","turn_id":"01a06225-0462-7b61-807f-4c5175c52572","last_agent_message":"Hi! What would you like to work on?","started_at":1788352988,"completed_at":1788352990,"duration_ms":2318,"time_to_first_token_ms":1925}}
{"timestamp":"2026-04-24T12:51:20.306Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"019dbf8b-6f5b-70d1-af82-c61c535777eb","reason":"interrupted","completed_at":1777035080,"duration_ms":789}}
// noise (both modes): token accounting — skip
{"timestamp":"2026-09-02T12:43:10.577Z","ordinal":12,"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":"…","model_context_window":258400,"total_token_usage":"…"},"rate_limits":"…"}}
```

### A.4 Tool calls and results

```jsonl
// shell: function_call with arguments as a JSON *string*; correlate by call_id
{"timestamp":"2026-05-13T19:12:06.002Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n '1,240p' /home/user/Projects/proj/README.md\",\"workdir\":\"/home/user/Projects/proj\",\"yield_time_ms\":1000,\"max_output_tokens\":4000}","call_id":"call_2NEhPnK4XuycKlDAmpVx6QWd"}}
// shell result: string output with the exec_command wrapper header (4,655/5,500 outputs start with "Chunk ID:")
{"timestamp":"2026-05-13T19:12:06.140Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_2NEhPnK4XuycKlDAmpVx6QWd","output":"Chunk ID: 5e6cdb\nWall time: 0.1310 seconds\nProcess exited with code 0\nOriginal token count: 5\nOutput:\n# proj\n\nREADME body…"}}
// MCP tool: bare tool name (no server prefix); arguments still a JSON string
{"timestamp":"2026-09-05T11:31:29.960Z","ordinal":241,"type":"response_item","payload":{"type":"function_call","name":"capy_search","arguments":"{\"queries\":[\"vault scanner conventions\"],\"source\":\"kk:project-conventions\",\"limit\":3}","call_id":"call_XXXXXXXXXXXXXXXXXXXXXXXX"}}
// result as content array (33/5,533) — newer files also carry id + passthrough
{"timestamp":"2026-08-28T13:11:09.248Z","ordinal":178,"type":"response_item","payload":{"type":"function_call_output","id":"fco_01a0487e-dec0-7643-9647-4a03ca597336","call_id":"call_6uqdeRXUesJmNplXDPt8wcfK","output":[{"type":"input_text","text":"Script completed\nWall time 0.0 seconds\nOutput:\n"}],"internal_chat_message_metadata_passthrough":{"turn_id":"01a04876-de72-7420-8734-9068acba0efe","create_time":1787922669.2481296}}}
// sub-agent launch (Task/Agent analog)
{"timestamp":"2026-04-25T19:05:00.552Z","type":"response_item","payload":{"type":"function_call","name":"spawn_agent","arguments":"{\"agent_type\":\"code-reviewer\",\"fork_context\":true,\"message\":\"Review the latest commit on the current branch…\"}","call_id":"call_6SXk8LhIEcye4gXl5reZ8gb9"}}
// legacy parent-side spawn event: links call_id → new_thread_id (child rollout uuid)
{"timestamp":"2026-04-25T19:05:06.724Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","call_id":"call_YOG7fKaiPd6iRKbLKV5JJxf8","sender_thread_id":"019dc606-f552-7f93-b9a1-c5620b23b8dd","new_thread_id":"019dc608-0061-7d90-a4e9-fca142504625","new_agent_nickname":"Boole","new_agent_role":"code-reviewer","prompt":"Review the latest commit on the current branch…","model":"gpt-5.5","reasoning_effort":"high","status":"pending_init"}}
// paginated parent-side spawn event
{"timestamp":"2026-09-05T11:24:21.149Z","ordinal":57,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-cbc4-7d23-9f9d-02d710c07ed7","turn_id":"01a0714f-3474-7ef1-b960-bdad01a46df8","item":{"type":"SubAgentActivity","id":"call_Ttit3KpsQjMgX6QwLug6dzXa","kind":"started","agent_thread_id":"01a0714f-f6ea-7bb1-bdeb-dc80fab2f1bf","agent_path":"/root/detect_profiles"},"started_at_ms":1788607461149,"completed_at_ms":1788607461149}}
// file edit (Edit/Write + structuredPatch analog): the patch text IS the call input
{"timestamp":"2026-04-24T13:04:38.070Z","type":"response_item","payload":{"type":"custom_tool_call","status":"completed","call_id":"call_8DaKfXFwQ5Gij3AqBTbNnodD","name":"apply_patch","input":"*** Begin Patch\n*** Add File: /tmp/proj/script.py\n+#!/usr/bin/env python3\n+import json\n*** End Patch\n"}}
// its result: output is a JSON *string* that must be decoded again
{"timestamp":"2026-04-24T13:04:38.117Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_8DaKfXFwQ5Gij3AqBTbNnodD","output":"{\"output\":\"Success. Updated the following files:\\nA /tmp/proj/script.py\\n\",\"metadata\":{\"exit_code\":0,\"duration_seconds\":0.1}}"}}
// thinking analog → skip
{"timestamp":"2026-09-02T12:43:42.011Z","ordinal":20,"type":"response_item","payload":{"type":"reasoning","id":"rs_018ab236…","summary":[],"encrypted_content":"gAAAAABqmBn9…","internal_chat_message_metadata_passthrough":{"turn_id":"01a06225-7cd9-7362-8cf1-84fc4391b3d1"}}}
// hosted web search
{"timestamp":"2026-04-24T13:00:03.106Z","type":"response_item","payload":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"site:developers.openai.com/codex hooks","queries":["site:developers.openai.com/codex hooks"]}}}
// paginated structured tool items (duplicates of the response_item pair, richer metadata)
{"timestamp":"2026-09-05T11:23:37.973Z","ordinal":16,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a0714f-…","item":{"type":"CommandExecution","id":"exec-e21167b2-4f0f-44b1-a6f9-5eb82ddec091","process_id":"45532","command":["/usr/bin/zsh","-lc","sed -n '1,260p' README.md"],"cwd":"file:///home/user/Projects/proj","parsed_cmd":[{"type":"read","cmd":"sed -n '1,260p' README.md","name":"README.md","path":"/home/user/Projects/proj/README.md"}],"source":"unified_exec_startup","status":"completed","stdout":"# proj…","stderr":"","aggregated_output":"# proj…","exit_code":0,"duration":{"secs":0,"nanos":109277969},"formatted_output":"# proj…"},"started_at_ms":1788607417863,"completed_at_ms":1788607417972}}
{"timestamp":"2026-09-05T11:31:30.267Z","ordinal":242,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a07153-…","item":{"type":"McpToolCall","id":"exec-1377d957-…","server":"capy","tool":"capy_search","arguments":{"queries":["vault scanner conventions"],"source":"kk:project-conventions","limit":3},"readOnlyHint":true,"status":"completed","result":{"content":[{"type":"text","text":"## vault scanner conventions\n…"}]},"duration":{"secs":0,"nanos":304582142}},"started_at_ms":1788607889962,"completed_at_ms":1788607890266}}
{"timestamp":"2026-09-05T12:42:04.387Z","ordinal":649,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a0718e-…","item":{"type":"FileChange","id":"exec-0db07141-…","changes":{"/home/user/Projects/proj/docs/design.md":{"type":"add","content":"# Design\n…"}},"status":"failed","stdout":"","stderr":"Failed to create parent directories for /home/user/Projects/proj/docs/…"},"started_at_ms":1788611847513,"completed_at_ms":1788612124387}}
```

### A.5 Compaction and bookkeeping

```jsonl
// compaction is APPENDED; replacement_history re-lists the retained ResponseItems and ends with a {"type":"compaction","encrypted_content":…} item
{"timestamp":"2026-04-26T15:30:21.505Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>…"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /home/user/Projects/proj\n\n<INSTRUCTIONS>…"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"What about user-facing docs?"}]},{"type":"compaction","encrypted_content":"gAAAAABp74mL…"}]}}
{"timestamp":"2026-09-05T13:01:56.074Z","ordinal":780,"type":"event_msg","payload":{"type":"item_completed","thread_id":"01a0714e-…","turn_id":"01a071a8-…","item":{"type":"ContextCompaction","id":"01a071a8-e0fc-7c82-b6ac-09b16bf7ed35"},"started_at_ms":1788613288188,"completed_at_ms":1788613316074}}
{"timestamp":"2026-09-05T11:25:34.037Z","ordinal":72,"type":"inter_agent_communication_metadata","payload":{"trigger_turn":false}}
{"timestamp":"2026-09-10T14:45:02.192Z","ordinal":12,"type":"token_usage_record","payload":{"thread_id":"01a08bc7-4014-7980-9b35-951772b7ddfd","turn_id":"01a08bc7-5f3e-7d12-9009-5f42a37d292b","session_id":"01a08bc7-4014-7980-9b35-951772b7ddfd","root_turn_id":"01a08bc7-5f3e-…","response_id":"resp_0171…","usage":{"input_tokens":15460,"cached_input_tokens":10880,"cache_write_input_tokens":0,"output_tokens":13,"reasoning_output_tokens":0,"total_tokens":15473},"turn_token_usage":"…","thread_token_usage":"…"}}
{"timestamp":"2026-08-04T10:04:52.370Z","type":"world_state","payload":{"full":true,"state":"{\"agents_md\":{},…}"}}
```

### A.6 Auxiliary files

```jsonl
// ~/.codex/session_index.jsonl — append-only, last entry per id wins; the first entry is Codex's auto-name, the second a /rename
{"id":"01a04876-0d7d-7fa1-a645-e63187ab1fea","thread_name":"You are being given a Skill to execu","updated_at":"2026-08-28T13:01:34.425794375Z"}
{"id":"01a04876-0d7d-7fa1-a645-e63187ab1fea","thread_name":"Execute archify skill","updated_at":"2026-08-28T13:01:39.049054014Z"}
// ~/.codex/history.jsonl — prompt history only
{"session_id":"019dbf88-35de-7091-b38c-5fd90e343f82","ts":1777034882,"text":"/model gpt-5.5"}
```

Filename for the paginated sample above: `sessions/2026/09/02/rollout-2026-09-02T14-43-05-01a06224-f7d9-71d3-b6bb-4ea07576fdb3.jsonl`
(local time 14:43:05 CEST ↔ payload `12:43:05Z`).

## Appendix B — Reproducing the counts

All commands run from `~/.codex/sessions` (`cd "${CODEX_HOME:-$HOME/.codex}/sessions"`) with `jq`.
They read only. Re-run after a Codex upgrade to catch new record types before touching the
scanner (the ADR-021 "format fingerprint" idea, done by hand). Once compressed rollouts appear,
replace `cat */*/*/*.jsonl` with `find . -name 'rollout-*.jsonl*' | while read f; do case $f in *.zst) zstd -dc "$f";; *) cat "$f";; esac; done`.

```bash
# inventory
find . -name 'rollout-*.jsonl' | wc -l
find . -name '*.jsonl.zst' | wc -l                       # compressed rollouts (0 as of 0.147)
find . -name 'rollout-*_*.jsonl' | wc -l                 # revert variants (0 observed)
find . -name '*.jsonl' -printf '%s\n' | sort -n | awk '{a[NR]=$1;s+=$1} END {print "n="NR,"total="s,"median="a[int(NR/2)],"max="a[NR]}'

# envelope: top-level type / (type, payload.type) / key sets
cat */*/*/*.jsonl | jq -r '.type' | sort | uniq -c | sort -rn
cat */*/*/*.jsonl | jq -r '[.type, (.payload.type // "-")] | join(" / ")' | sort | uniq -c | sort -rn
cat */*/*/*.jsonl | jq -r 'keys | join(",")' | sort | uniq -c

# session_meta: versions × history_mode × ordinal presence; key-set drift
for f in */*/*/*.jsonl; do head -1 "$f" | jq -r '"\(.payload.cli_version) \(.payload.history_mode // "legacy") ordinal=\(has("ordinal"))"'; done | sort | uniq -c
cat */*/*/*.jsonl | jq -c 'select(.type=="session_meta") | .payload | keys' | sort | uniq -c | sort -rn

# legacy vs paginated event sets
for f in */*/*/*.jsonl; do m=$(head -1 "$f" | jq -r '.payload.history_mode // "legacy"'); jq -r --arg m "$m" 'select(.type=="event_msg") | "\($m) \(.payload.type)"' "$f"; done | sort | uniq -c | sort -k2,2 -k1,1rn
cat */*/*/*.jsonl | jq -r 'select(.type=="event_msg" and .payload.type=="item_completed") | .payload.item.type' | sort | uniq -c | sort -rn

# messages: roles, content block types, noise taxonomy of user-role response_items
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="message") | .payload.role as $r | .payload.content[]? | "\($r) / \(.type)"' | sort | uniq -c
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="message" and .payload.role=="user") | .payload.content[]? | select(.type=="input_text") | .text | gsub("^\\s+";"") | .[:30]' | sed -E 's/[0-9]+/N/g' | sort | uniq -c | sort -rn | head -15
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="message" and .payload.role=="user") | (.payload.internal_chat_message_metadata_passthrough.content_item_kinds // ["<none>"]) | join(",")' | sort | uniq -c | sort -rn

# human-turn source check on one LEGACY file F: event text == non-noise response_item user text
jq -r 'select(.type=="event_msg" and .payload.type=="user_message") | .payload.message' "$F" | md5sum
jq -r 'select(.type=="response_item" and .payload.type=="message" and .payload.role=="user") | .payload.content[] | select(.text|startswith("<")|not) | select(.text|startswith("# AGENTS")|not) | .text' "$F" | md5sum
# same for assistant duplicates
jq -r 'select(.type=="event_msg" and .payload.type=="agent_message") | .payload.message' "$F" | md5sum
jq -r 'select(.type=="response_item" and .payload.type=="message" and .payload.role=="assistant") | .payload.content[] | .text' "$F" | md5sum
# paginated file P: UserMessage items vs turns
echo "UserMessage=$(jq -c 'select(.payload.type=="item_completed" and .payload.item.type=="UserMessage")' "$P" | wc -l) task_started=$(jq -c 'select(.payload.type=="task_started")' "$P" | wc -l)"
# ordinal == line index?
jq -r '.ordinal' "$P" | awk 'NR-1!=$1 {bad++} END {print "lines="NR, "mismatches="bad+0}'

# tools
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="function_call") | .payload.name' | sort | uniq -c | sort -rn
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="custom_tool_call") | .payload.name' | sort | uniq -c
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="function_call_output") | .payload.output | type' | sort | uniq -c
cat */*/*/*.jsonl | jq -r 'select(.type=="response_item" and .payload.type=="function_call_output" and (.payload.output|type)=="string") | .payload.output | split("\n")[0] | .[:30]' | sed -E 's/[0-9a-f]{6}$/X/; s/[0-9]+/N/g' | sort | uniq -c | sort -rn | head

# sub-agents: children (source is an object) and their parents
for f in */*/*/*.jsonl; do head -1 "$f" | jq -r --arg f "$f" 'select(.payload.source|type=="object") | "\($f) parent=\(.payload.source.subagent.thread_spawn.parent_thread_id) role=\(.payload.agent_role)"'; done
cat */*/*/*.jsonl | jq -c 'select(.type=="event_msg" and (.payload.type=="collab_agent_spawn_end" or (.payload.type=="item_completed" and .payload.item.type=="SubAgentActivity"))) | .payload | {call_id, new_thread_id, agent_thread_id: .item.agent_thread_id}'

# UUIDv7 prefix collisions at 8 vs 12 hex chars
ls */*/*/*.jsonl | sed -E 's/.*rollout-[0-9T-]+-([0-9a-f]{8}).*/\1/' | sort | uniq -d | wc -l
ls */*/*/*.jsonl | sed -E 's/.*rollout-[0-9T-]+-([0-9a-f]{8}-[0-9a-f]{4}).*/\1/' | sort | uniq -d | wc -l

# filename (local time) vs payload timestamp (UTC) — pick any file
f=$(ls */*/*/*.jsonl | head -1); echo "$f"; head -1 "$f" | jq -r '.payload.timestamp'

# Codex's own index (read-only!)
sqlite3 -readonly "${CODEX_HOME:-$HOME/.codex}/state_5.sqlite" "select count(*), sum(archived), sum(name is not null), sum(title<>'') from threads;"
sqlite3 -readonly "${CODEX_HOME:-$HOME/.codex}/state_5.sqlite" "select status, count(*) from thread_spawn_edges group by status;"
```

Source-of-truth checks against `openai/codex` (no clone needed):

```bash
gh api repos/openai/codex/contents/codex-rs/rollout/src/policy.rs --jq .content | base64 -d      # what is persisted, per history mode
gh api repos/openai/codex/contents/codex-rs/history/src/lib.rs   --jq .content | base64 -d | grep -n -A20 'pub enum RolloutItem'
gh api repos/openai/codex/contents/codex-rs/rollout/src/compression.rs --jq .content | base64 -d | grep -n 'COMPRESSED_SUFFIX\|MIN_ROLLOUT_AGE'
gh api repos/openai/codex/contents/codex-rs/rollout/src/rollout_file_name.rs --jq .content | base64 -d | grep -n -A12 'fn render'
gh api repos/openai/codex/contents/codex-rs/protocol/src/protocol.rs --jq .content | base64 -d | grep -n -A40 'pub struct SessionMeta {'
```
