# Tasks: Codex Sessions in the Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Research: [./research.md](./research.md)
> Status: pending
> Created: 2026-09-13
> Not Doing: Codex `/rename` name promotion, reading Codex state DBs, indexing reasoning/world_state/token/turn_context/compacted history, per-platform index_version, cascading child delete, generic MCP input rendering, TUI `/subagents`-style picker, PreCompact for Codex, SQL CHECK on platform
> Deferred (see implementation.md § Deferred work): Codex `resume`, SearchOnly attachment unification, revert-rollout verification, hiding non-interactive Codex sources

## Task 1: Golden and parity harness
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 2
- **Slicing:** Risk-First (freezes behavior before the riskiest change)
- **Docs:** [implementation.md#slice-1-golden-and-parity-harness](./implementation.md#slice-1-golden-and-parity-harness)

### Subtasks
- [ ] 1.1 Create `internal/vault/golden_test.go` with a named fixture table covering every `case` arm of the three `switch line.Type` blocks (plain-string user, block-array user with Read/Edit+structuredPatch/Bash/unknown tool results, progressive snapshots, ai-title, pr-link, away_summary, queued_command attachment, attachment with message content, oversize line, malformed line, subagent sidecar), reusing `fixtures_test.go` builders
- [ ] 1.2 Run `ScanSession`, `ScanSubagent`, `RenderText`, `RenderMarkdown`, `ParseTranscript` per case; serialize deterministically; compare against `internal/vault/testdata/golden/<case>.<reader>.*`; support `-update`; commit the golden files generated on `master`
- [ ] 1.3 Create `internal/vault/parity_canary_test.go` gated on `CAPY_VAULT_PARITY_BASELINE`: walk `config.ClaudeProjectsDir()` sessions and subagent sidecars, one SHA-256 per file over the four readers' outputs; write the baseline when the file is missing, compare otherwise; `t.Skip` when unset or no sessions
- [ ] 1.4 Produce the baseline on `master` and re-run unchanged: 0 mismatches

## Task 2: Transcript model and Claude decoder
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Slicing:** Contract-First (new internal boundary)
- **Docs:** [implementation.md#slice-2-transcript-model-and-claude-decoder-contract-first](./implementation.md#slice-2-transcript-model-and-claude-decoder-contract-first)

### Subtasks
- [ ] 2.1 Create `internal/vault/platform.go`: `Platform` type, `PlatformClaudeCode`/`PlatformCodex`, `ParsePlatform`, `DisplayName`, `DetectFormat(firstLine)`; table test for Codex/Claude/garbage/empty first lines
- [ ] 2.2 Create `internal/vault/transcript_model.go`: `Transcript`, `Meta` (incl. `TitleFallback`, `ParentUUID`, `Source`), `Entry`/`EntryKind`, `ToolCall`, `Launch`, `Diff`, `Entry.SearchOnly`, `Decoder` interface, `DecoderFor(Platform)` (Codex stub errors until Task 6); doc comments state the pre-policy contract
- [ ] 2.3 Create `internal/vault/claude_decoder.go` by moving pass-1 logic out of `scanner.go`/`render.go`/`transcript.go`: type inference, snapshot merge (unify `mergeBlocks`/`dedupBlocks`), `cleanText`, `queuedCommandPrompt`→Human(Queued), `prLinkText`/`away_summary`→System, `attachmentText`→System(SearchOnly), tool call summaries and `Task`/`Agent` launches, `toolResultText`→Body, `diffBodyFromToolResult`→Diff, `Meta` incl. plain-string-only `TitleFallback`
- [ ] 2.4 `claude_decoder_test.go`: entries and `Meta` for each golden case; merged snapshot `LineIndex` is the first snapshot's line

## Task 3: Scanner onto the model
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** Task 4, Task 5, Task 6
- **Docs:** [implementation.md#slice-3-scanner-onto-the-model](./implementation.md#slice-3-scanner-onto-the-model)

### Subtasks
- [ ] 3.1 Rewrite `scanner.go`: `ScanSession(p Platform, r)` = decode + `ScanTranscript(*Transcript)`; `ScanSubagent` via the Claude decoder; keep all policy constants/helpers in `scanner.go`; delete duplicated pass-1 code (only orphans your change created)
- [ ] 3.2 Update `import.go` `scanSessionAndSubagents` to pass `PlatformClaudeCode` for now
- [ ] 3.3 Verify: `TestGolden` scanner outputs unchanged, parity canary 0 mismatches, vault suite green

## Task 4: Render and transcript onto the model
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** Task 3, Task 5, Task 6
- **Docs:** [implementation.md#slice-4-render-and-transcript-onto-the-model](./implementation.md#slice-4-render-and-transcript-onto-the-model)

### Subtasks
- [ ] 4.1 Rewrite `render.go`: `RenderText(p, raw)`/`RenderMarkdown(p, raw)`; entry-based `renderUserContent`/`collapsedToolResult`; assistant label from `p.DisplayName()`
- [ ] 4.2 Rewrite `transcript.go`: `ParseTranscript(p, raw, subagentIDs)`; entry-based split/collapse/diff; launch markers from `ToolCall.Launch`; add `TranscriptMessage.ChildUUID` (Openable when set; Claude keeps count-based `AgentID` mapping)
- [ ] 4.3 Thread the platform parameter through `cmd/capy/vault.go` `renderShow`, the TUI viewer parse call and search-hit rendering (constant `PlatformClaudeCode` until Task 5)
- [ ] 4.4 Verify: `TestGolden` render/transcript outputs unchanged, parity canary 0 mismatches, `internal/vault/tui` and `cmd/capy` suites green

## Task 5: Migration 0006 and platform plumbing
- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 3, Task 4, Task 6
- **Docs:** [implementation.md#slice-5-migration-0006-and-platform-plumbing-claude-only](./implementation.md#slice-5-migration-0006-and-platform-plumbing-claude-only)

### Subtasks
- [ ] 5.1 `migrations.go`: `migrate0006AddPlatform` (0005 pattern; `platform TEXT NOT NULL DEFAULT 'claude-code'`, `parent_uuid TEXT`, `idx_sessions_parent`, guarded by `columnExists`/IF NOT EXISTS); register in `migrateVault`; mirror columns and index in `schemaSQL` with the index DDL shared as a constant
- [ ] 5.2 `migrations_test.go`: hand-built pre-0006 vault opens with defaults and records the migration; fresh vault records it without ALTER
- [ ] 5.3 `store.go`: `Session.Platform`/`ParentUUID`; extend `sessionMetaColumns`, scan helpers, insert/update statements, `GetSession`, `ListSessions`; `ListOptions.IncludeChildren` (default hides `parent_uuid IS NOT NULL`) and `ListOptions.Platform`; `Children(ctx, parentUUID)`; `SearchResult.Platform`/`ParentUUID` in `Search` and `SearchChunks`; `VaultStats.ByPlatform`/`Children`
- [ ] 5.4 `import.go` `buildRecord` sets `Platform` (Claude for now) and `ParentUUID`; `merge.go` feature-detects `platform`/`parent_uuid` (`columnExists`, literal substitution when absent) and carries them verbatim
- [ ] 5.5 `store_test.go`/`merge_test.go`: list default vs `IncludeChildren`, platform filter, `Children`, stats breakdown, merge from pre-0006 and 0006 sources; `capy vault list --json` shows `"platform": "claude-code"`

## Task 6: Codex decoder
- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 3, Task 4, Task 5
- **Docs:** [implementation.md#slice-6-codex-decoder](./implementation.md#slice-6-codex-decoder)

### Subtasks
- [ ] 6.1 Create `internal/vault/codex_types.go`: envelope and typed payloads (`session_meta` with raw `source` and lifted `parent_thread_id`, `message`, `function_call`, `function_call_output` with raw `output`, `custom_tool_call(_output)`, `web_search_call`, `event_msg` variants `user_message`/`item_completed`/`collab_agent_spawn_end`/`task_started`); unmarshal tests over research Appendix A lines
- [ ] 6.2 Create `internal/vault/codex_fixtures_test.go` builders (legacy and paginated variants; session meta incl. child; user event/item; assistant; function call/output pair with wrapper header; apply_patch pair; spawn pair for both event shapes; MCP call; reasoning/compacted/token_count/world_state noise; `writeCodexRollout(t, home, relPath, lines, compressed)`)
- [ ] 6.3 Create `internal/vault/codex_decoder.go` implementing `Decoder`: events-first human turns with noise-filtered `response_item` fallback only when events yield none; assistant entries; tool calls with summaries per design table; results by `call_id` with `stripExecHeader` (keep exit-code line) and second JSON decode for custom outputs; `Launch.ChildUUID` from `collab_agent_spawn_end` or `SubAgentActivity`; skip list; per-file debug drift fingerprint; zero-human warning with `cli_version`/`history_mode`
- [ ] 6.4 Create `internal/vault/codex_patch.go`: `*** Begin Patch` → unified diff + add/remove counts; `ok=false` on malformed; wire into `apply_patch` results; `codex_patch_test.go` (add, update two hunks, delete, move, malformed)
- [ ] 6.5 `codex_decoder_test.go`: human/assistant/tool entries, summaries, header stripping, `Meta`, `ParentUUID`, `ChildUUID` from both shapes, developer-role and injected-context produce no Human, unknown types skipped; consumers on Codex bytes: `ScanSession` roles/title/`MessageCount`, `RenderText` `Codex` heading, `ParseTranscript` openable child marker
- [ ] 6.6 Create `internal/vault/codex_canary_test.go` over `$CODEX_HOME/{sessions,archived_sessions}` (skip when absent): design Assumptions 1–3, zero-human warning never fires, no panic/error; passes over the 164 local rollouts

## Task 7: Codex discovery, import and restore round-trip
- **Status:** pending
- **Depends on:** Task 3, Task 4, Task 5, Task 6
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#slice-7-codex-discovery-import-and-restore-round-trip](./implementation.md#slice-7-codex-discovery-import-and-restore-round-trip)

### Subtasks
- [ ] 7.1 `internal/config/paths.go`: `CodexHome()` (honors `CODEX_HOME`, default `~/.codex`) with tests
- [ ] 7.2 `discovery.go`: `Discoverer` interface, `claudeDiscoverer` (existing logic), `codexDiscoverer` over `sessions/` and `archived_sessions/`; `parseRolloutFilename`; `SessionFile.{Platform, RelativePath (slash-separated, .zst stripped), ProjectPath, Compressed}`; bounded `readFirstLine` with zstd decode; `DiscoverAll()`; `DiscoverSessions(root)` autodetects Codex layout; warn+skip on unparseable names; tests over a temp `CODEX_HOME` (legacy, paginated, archived, zst, `_<rollout_id>`, misnamed)
- [ ] 7.3 `import.go`: decompress before hashing; `scanSessionAndSubagents(uuid, platform, main, files)` dispatch; `ClaudeProjectDir` = mangled dir or `RelativePath`; `ProjectPath` = `Meta.CWD` → hint → `resolveProjectPath`; `--project` matches `ProjectDir` or `ProjectPath`; `ImportOptions.Platform`; tests: statuses (new, skipped archived duplicate, skipped second run, child imported), `Platform`, `ClaudeProjectDir`, `ParentUUID`
- [ ] 7.4 `restore.go`: `RestoreSessionAt(uuid, mainRel, raw, files, root, overwrite)` with path-safety validation; `RestoreSession` delegates; tests: path equals `mainRel`, SHA-256 equals plain bytes, `..` rejected
- [ ] 7.5 `cmd/capy/vault.go`: `import --platform`, default `DiscoverAll()`, `--source` help; `defaultRestoreRoot` → `$CODEX_HOME` for Codex; `restoreVaultSession` passes the relative path; `printImportResult` per-platform counts
- [ ] 7.6 Integration test: import → `Search` human phrase `Role == user` → `RenderText` shows `[Codex]` → `RestoreSessionAt` round-trips; manual: `./capy vault import --dry-run --platform codex` lists the real corpus, restore reproduces a file with equal SHA-256

## Task 8: Sweep, merge and reindex dispatch
- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** Task 9, Task 10
- **Docs:** [implementation.md#slice-8-sweep-merge-and-reindex-dispatch](./implementation.md#slice-8-sweep-merge-and-reindex-dispatch)

### Subtasks
- [ ] 8.1 `internal/server/server.go` `vaultSweep`: Codex discovery filtered by cleaned, symlink-resolved cwd equality (all under `CAPY_VAULT_SWEEP_ALL`); missing `CODEX_HOME` is debug; per-platform counts in the info log; `countProjects` platform-aware; sweep test with a symlinked project dir
- [ ] 8.2 `merge.go`: dispatch FTS rebuild on carried platform; `DetectFormat` fallback when absent/unknown, `StatusError` on failure; `--project` filter `claude_project_dir LIKE ? OR project_path LIKE ?` (update flag help); merge matrix tests (Codex rows, pre-0006 source, bogus platform → error, dry run)
- [ ] 8.3 `reindex.go` `rebuildSessionFTS`: pass `sess.Platform` with the same fallback posture; test over a mixed vault

## Task 9: CLI surfaces
- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** Task 8, Task 10
- **Docs:** [implementation.md#slice-9-cli-surfaces](./implementation.md#slice-9-cli-surfaces)

### Subtasks
- [ ] 9.1 `list`: platform column, `--platform`, `--include-children` (children show `↳ <parent short id>`), JSON `platform`/`parent_uuid`; tests
- [ ] 9.2 `show`: `writeShowHeader` prints platform, parent for a child, children (via `Children`) for a parent; tests for parent/child/Claude
- [ ] 9.3 `search`: platform tag and child marker in `printSearchResults`/`resultsToJSON`; test
- [ ] 9.4 `stats`: `ByPlatform` and `Children` in `printStats`/`statsToJSON`; test
- [ ] 9.5 `resume`: load session before `exec.LookPath("claude")`; Codex → actionable error naming `capy vault restore` and `codex resume <uuid>`; TUI `R` reaches the same path; test asserts no process launched
- [ ] 9.6 `delete`: warn when `Children` non-empty; no cascade; test
- [ ] 9.7 `shortUUID(u, p)`: 12 chars for Codex; apply in table, search output, `handleLookupError` candidates, show header; test with colliding UUIDv7 prefixes

## Task 10: TUI
- **Status:** pending
- **Depends on:** Task 4, Task 7
- **Size:** M
- **Can run in parallel with:** Task 8, Task 9
- **Docs:** [implementation.md#slice-10-tui](./implementation.md#slice-10-tui)

### Subtasks
- [ ] 10.1 `tui/list.go`: `IncludeChildren` toggle on a key absent from the existing map (verify via `keybindings_test.go`); platform column; indented children with parent short id; help text
- [ ] 10.2 `tui/render.go`: `roleLabel(role, platform)`; child markers via `markerRowFor` + `singleLine`; label and one-row-one-line tests
- [ ] 10.3 `tui/viewer.go`: header `Codex · child of <short id>`; `openFocusedMarker` child-session branch (`GetSession(ChildUUID)` + `GetFiles`, nested viewer, `esc`/`q` returns); unarchived child → transient status; `viewer_test.go` covers open/return and the unarchived case

## Task 11: MCP result metadata and stats
- **Status:** pending
- **Depends on:** Task 5, Task 7
- **Size:** S
- **Can run in parallel with:** Task 8, Task 9, Task 10, Task 12
- **Docs:** [implementation.md#slice-11-mcp-result-metadata-and-stats](./implementation.md#slice-11-mcp-result-metadata-and-stats)

### Subtasks
- [ ] 11.1 `internal/server/tool_vault_search.go` `formatVaultHit` and the federated formatting in `tool_search.go`: platform in the meta line, `child of <short id>` marker, `session:<uuid>` tag unchanged; tests for a Codex hit and an unchanged Claude hit
- [ ] 11.2 `internal/server/tool_stats.go`: per-platform vault rows and children count; test

## Task 12: Doctor and routing text
- **Status:** pending
- **Depends on:** Task 7
- **Size:** S
- **Can run in parallel with:** Task 8, Task 9, Task 10, Task 11
- **Docs:** [implementation.md#slice-12-doctor-and-routing-text](./implementation.md#slice-12-doctor-and-routing-text)

### Subtasks
- [ ] 12.1 `internal/platform/doctor.go` `CheckVaultPlatforms` (root present? archived count per platform); wire into `internal/server/tool_doctor.go` and `cmd/capy/doctor.go` beside `CheckVault`; tests for both roots, Codex root absent, vault disabled
- [ ] 12.2 `internal/platform/routing.go` and `.capy/AGENTS.md`: `session` kind row reads "archived transcripts from Claude Code and Codex"; regenerate via `capy setup --local` and confirm the diff is wording only

## Task 13: Documentation and ADR
- **Status:** pending
- **Depends on:** Task 8, Task 9, Task 10, Task 11, Task 12
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#slice-13-documentation-and-adr](./implementation.md#slice-13-documentation-and-adr)

### Subtasks
- [ ] 13.1 `docs/architecture.md` § Vault: transcript-model seam and decoders (replace the "three independent readers" framing), `platform`/`parent_uuid`, Codex discovery and restore paths, 12-char short id, CLI table updates; grep every named symbol
- [ ] 13.2 `README.md` § Session Vault: Codex paragraph (discovery roots, restore root, `--platform`, children hidden by default, resume not yet supported)
- [ ] 13.3 `docs/feat/done/vault/design.md` *Not Doing*: one-line pointer to this feature
- [ ] 13.4 Write `docs/adr/031-transcript-model-seam-and-multi-platform-vault.md` (context, decision, consequences, alternatives per design § Rejected Alternatives); link from `docs/architecture.md`
- [ ] 13.5 Update the `research.md` banner to "design landed"; index a `kk:project-conventions` note that the three-parser convention is superseded by the transcript model (decoders own format, consumers own policy)

## Task 14: Final verification
- **Status:** pending
- **Depends on:** Task 1, Task 2, Task 3, Task 4, Task 5, Task 6, Task 7, Task 8, Task 9, Task 10, Task 11, Task 12, Task 13
- **Size:** S
- **Can run in parallel with:** —

### Subtasks
- [ ] 14.1 Run `/kk:test` skill to verify all tasks — full suite with both keys and `-tags fts5`, `make test-race`, parity canary against the Task 1 baseline, Codex canary over the local corpus
- [ ] 14.2 Run `make bench-quality` on `master` and the branch, then `make bench-compare BASE=master TARGET=<branch>`; no `!` regression markers
- [ ] 14.3 Run `/kk:document` skill to update any relevant docs
- [ ] 14.4 Run `/kk:review-code` skill with the Go profile to review the implementation
- [ ] 14.5 Run `/kk:review-spec` skill to verify the implementation matches design and implementation docs

## Dependency Graph

```
Task 1 ──┬─→ Task 3 ──┐
Task 2 ──┤            │
         ├─→ Task 4 ──┤
         ├─→ Task 5 ──┼─→ Task 7 ──┬─→ Task 8  ──┐
         └─→ Task 6 ──┘            ├─→ Task 9  ──┤
                                   ├─→ Task 10 ──┼─→ Task 13 ─→ Task 14
                        Task 5 ────┼─→ Task 11 ──┤
                                   └─→ Task 12 ──┘
```

Tasks 1 and 2 start together. Tasks 3, 4, 5, 6 run in parallel once 1 and 2 land (5 and 6 need only 2). Task 7 is the integration point. Tasks 8–12 fan out from 7 and converge on 13, then 14.
