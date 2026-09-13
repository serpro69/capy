# Tasks: Codex Sessions in the Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Research: [./research.md](./research.md)
> Review triage: [./design-triage.md](./design-triage.md)
> Status: pending
> Created: 2026-09-13 (revised 2026-09-13 after design review)
> Not Doing: Codex `/rename` name promotion, reading Codex state DBs, indexing reasoning/world_state/token/turn_context/compacted history, per-platform index_version, cascading child delete, generic MCP input rendering, TUI `/subagents`-style picker, PreCompact for Codex, SQL CHECK on platform
> Deferred (see implementation.md § Deferred work): Codex `resume`, archiving revert rollouts, SearchOnly attachment unification, hiding non-interactive / non-thread_spawn sub-agent sources

## Task 1: Golden and parity harness
- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Task 2
- **Slicing:** Risk-First (freezes behavior before the riskiest change)
- **Docs:** [implementation.md#slice-1-golden-and-parity-harness](./implementation.md#slice-1-golden-and-parity-harness)

### Subtasks
- [ ] 1.1 Write `internal/vault/testdata/golden/DIVERGENCES.md`: every behavioral divergence between `scanner.go`, `render.go`, `transcript.go` (plain-string-only title fallback, scanner-only `attachmentText`, scanner-only warn logs, collapse/diff policy differences, …), each mapped to a transcript-model field or marked "consumer policy / log-only"
- [ ] 1.2 Introduce `var scanLineCap = maxScanLineBytes` / `var renderLineCap = renderMaxLineBytes` and pass them to `scanLines` from the public readers so tests can lower the oversize threshold
- [ ] 1.3 Create `internal/vault/golden_test.go` with a named fixture table covering every `case` arm of the three `switch line.Type` blocks and every inventory row (plain-string user, block-array user with Read/Edit+structuredPatch/Bash/unknown tool results, progressive snapshots, text → tool_use → text in one assistant message, ai-title, pr-link, away_summary, queued_command attachment, attachment with message content, oversize line, malformed line, subagent sidecar), reusing `fixtures_test.go` builders
- [ ] 1.4 Run `ScanSession`, `ScanSubagent`, `RenderText`, `RenderMarkdown`, `ParseTranscript` per case; serialize deterministically; compare against `internal/vault/testdata/golden/<case>.<reader>.*`; support `-update`; commit the golden files generated on `master`
- [ ] 1.5 Create `internal/vault/parity_canary_test.go` gated on `CAPY_VAULT_PARITY_BASELINE`: walk `config.ClaudeProjectsDir()` sessions and subagent sidecars, one SHA-256 per file over the four readers' outputs; write the baseline when the file is missing, compare otherwise; `t.Skip` when unset or no sessions
- [ ] 1.6 Produce the baseline on `master` and re-run unchanged: 0 mismatches

## Task 2: Transcript model and Claude decoder
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Slicing:** Contract-First (new internal boundary; adds files only, reader loops untouched so Tasks 3–6 build in parallel)
- **Docs:** [implementation.md#slice-2-transcript-model-and-claude-decoder-contract-first](./implementation.md#slice-2-transcript-model-and-claude-decoder-contract-first)

### Subtasks
- [ ] 2.1 Create `internal/vault/platform.go`: `Platform` type, `PlatformClaudeCode`/`PlatformCodex`, `ParsePlatform`, `DisplayName`, Codex-positive `DetectFormat(firstLine)` (Codex iff `type == "session_meta"` with object payload; Claude for any other JSON object; error only for non-JSON); table test incl. a Claude `file-history-snapshot` first line → Claude
- [ ] 2.2 Create `internal/vault/transcript_model.go`: `Transcript`, `Meta` (incl. `TitleFallback`, `ParentUUID`, `Source`), `Entry`/`EntryKind`, ordered `Part` (text | `ToolCall`), `ToolCall`, `Launch`, `Diff`, `Entry.SearchOnly`, `Decoder` interface, `DecoderFor(Platform)` (Codex stub errors until Task 6); doc comments state the pre-policy contract and that part order is load-bearing
- [ ] 2.3 Create `internal/vault/claude_decoder.go` **without touching the three reader loops**: reuse/relocate the shared helpers (`cleanText`, `queuedCommandPrompt`, `prLinkText`, `attachmentText`, `collectToolUseSummaries`, `toolUseSummary`, `subagentLaunchLabel`, `toolResultText`, `diffBodyFromToolResult`, unified `mergeBlocks`/`dedupBlocks`); type inference, snapshot merge, `queued_command`→Human(Queued), `pr-link`/`away_summary`→System, `attachmentText`→System(SearchOnly), assistant blocks → ordered `Parts`, `Task`/`Agent` launches, `toolResultText`→Body, `diffBodyFromToolResult`→Diff, `Meta` incl. plain-string-only `TitleFallback`
- [ ] 2.4 `claude_decoder_test.go`: entries and `Meta` for each golden case; merged snapshot `LineIndex` is the first snapshot's line; text → call → text keeps its order

## Task 3: Scanner onto the model
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** Task 4, Task 5, Task 6
- **Slicing:** Risk-First ("Claude scanner output byte-identical" is the testable path; no user-visible change by design)
- **Docs:** [implementation.md#slice-3-scanner-onto-the-model](./implementation.md#slice-3-scanner-onto-the-model)

### Subtasks
- [ ] 3.1 Rewrite `scanner.go`: `ScanSession(p Platform, r)` = decode + `ScanTranscript(*Transcript)`; assistant row text = `Parts` in order (text, then `ToolCall.Summary`, newline-joined — today's `extractAssistantText`); `ScanOutput` gains `Platform`, `ParentUUID`, `Source` copied from `Meta`; `ScanSubagent` via the Claude decoder; keep all policy constants/helpers in `scanner.go`; delete the scanner's pass-1 loop and the orphans that deletion creates (render/transcript loops stay until Task 4)
- [ ] 3.2 `import.go` `scanSessionAndSubagents(uuid, platform, main, files)`; callers pass `PlatformClaudeCode` for now
- [ ] 3.3 Verify: `TestGolden` scanner outputs unchanged, parity canary 0 mismatches, vault suite green

## Task 4: Render and transcript onto the model
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** M
- **Can run in parallel with:** Task 3, Task 5, Task 6
- **Slicing:** Risk-First ("Claude show/TUI output byte-identical" is the testable path)
- **Docs:** [implementation.md#slice-4-render-and-transcript-onto-the-model](./implementation.md#slice-4-render-and-transcript-onto-the-model)

### Subtasks
- [ ] 4.1 Rewrite `render.go`: `RenderText(p, raw)`/`RenderMarkdown(p, raw)`; entry-based `renderUserContent`/`collapsedToolResult`; assistant bodies from `Parts` in order; assistant label from `p.DisplayName()`; delete the render pass-1 loop and its orphans
- [ ] 4.2 Rewrite `transcript.go`: `ParseTranscript(p, raw, subagentIDs)`; entry-based split/collapse/diff; launch markers from `ToolCall.Launch`; add `TranscriptMessage.ChildUUID` (Openable when set; Claude keeps count-based `AgentID` mapping); delete the transcript pass-1 loop and its orphans
- [ ] 4.3 Thread the platform parameter through `cmd/capy/vault.go` `renderShow`, the TUI viewer parse call and search-hit rendering (constant `PlatformClaudeCode` until Task 5)
- [ ] 4.4 Verify: `TestGolden` render/transcript outputs unchanged, parity canary 0 mismatches, `internal/vault/tui` and `cmd/capy` suites green

## Task 5: Migration 0006, platform plumbing and reader version
- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 3, Task 4, Task 6
- **Slicing:** Contract-First (schema and store surface for later slices; Claude behavior unchanged)
- **Docs:** [implementation.md#slice-5-migration-0006-platform-plumbing-and-reader-version-claude-only](./implementation.md#slice-5-migration-0006-platform-plumbing-and-reader-version-claude-only)

### Subtasks
- [ ] 5.1 `migrations.go`: `migrate0006AddPlatform` (0005 pattern; `platform TEXT NOT NULL DEFAULT 'claude-code'`, `parent_uuid TEXT`, guarded by `columnExists`, **then** `CREATE INDEX IF NOT EXISTS idx_sessions_parent`); register in `migrateVault`; add the two **columns only** to `schemaSQL`'s `CREATE TABLE` — the index must not appear in `schemaSQL` (it runs before migrations)
- [ ] 5.2 `migrations_test.go`: hand-built pre-0006 vault opens with defaults, index present, migration recorded; fresh vault records it without ALTER; `grep idx_sessions_parent store.go` is empty
- [ ] 5.3 `store.go`: `Session.Platform`/`ParentUUID`; extend `sessionMetaColumns`, scan helpers, insert/update statements, `GetSession`, `ListSessions`; `ListOptions.IncludeChildren` (default hides `parent_uuid IS NOT NULL`) and `ListOptions.Platform`; `Children(ctx, parentUUID)`; `SearchResult.Platform`/`ParentUUID` in `Search` and `SearchChunks`; `VaultStats.ByPlatform`/`Children`; `CodexLocationSizes(ctx)`; metadata-only `UpdateLocationHint(ctx, uuid, hint)`
- [ ] 5.4 `codec.go`: `supportedReaderVersion = 3`; `markMinReaderVersion(ctx, tx, version)` monotonic upsert, called once per write transaction with the highest version any record in it requires (2 for zstd blobs as today, 3 for `Platform == codex`); `codec_test.go`: absent → 3 on Codex write, 2 → 3 raised, 3 not lowered, marker 4 refused on open, Claude-only vault stays at 2
- [ ] 5.5 `import.go` `buildRecord` sets `Platform` (Claude for now) and `ParentUUID` from `ScanOutput`; `merge.go` feature-detects `platform`/`parent_uuid` (`columnExists`, literal substitution when absent, **no sniff**) and carries them verbatim
- [ ] 5.6 `store_test.go`/`merge_test.go`: list default vs `IncludeChildren`, platform filter, `Children`, stats breakdown, location-size map; merge from a pre-0006 source with `file-history-snapshot` first lines (merges fully) and from a 0006 source; `capy vault list --json` shows `"platform": "claude-code"`

## Task 6: Codex decoder
- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 3, Task 4, Task 5
- **Slicing:** Contract-First (decoder proven against fixtures and the real corpus before any surface uses it)
- **Docs:** [implementation.md#slice-6-codex-decoder](./implementation.md#slice-6-codex-decoder)

### Subtasks
- [ ] 6.1 Create `internal/vault/codex_types.go`: envelope and typed payloads (`session_meta` with raw `source`, lifted `parent_thread_id`, `agent_nickname`/`agent_role`/`agent_path`/`thread_source`; `message`; `function_call`; `function_call_output` with raw `output`; `custom_tool_call(_output)`; `web_search_call`; `event_msg` variants `user_message`/`item_completed`/`collab_agent_spawn_end`/`task_started`); unmarshal tests over research Appendix A lines
- [ ] 6.2 Create `internal/vault/codex_fixtures_test.go` builders (legacy and paginated variants; session meta incl. ≤ 0.137 child **with** spawn prompt and ≥ 0.147 child **without**; user event/item; assistant; function call/output pair with wrapper header; `codexApplyPatchPair(success)`; spawn pair for both event shapes with an **encrypted** `message`; noise incl. `<recommended_plugins>`, reasoning, compacted, token_count, world_state; `writeCodexRollout(t, home, relPath, lines, compressed)`)
- [ ] 6.3 Create `internal/vault/codex_decoder.go` implementing `Decoder`: events-first human turns; fallback to `response_item` user messages only when events yield none, dropping `developer` role and any text starting with `<` or `# AGENTS.md instructions`; assistant entries with ordered `Parts`; tool call summaries per design table (`spawn_agent` label from `task_name`/`agent_type`, then parent-side `agent_path`, then `message`); results by `call_id` with `stripExecHeader` (keep exit-code line) and second JSON decode for custom outputs; `Launch.ChildUUID` from `collab_agent_spawn_end` or `SubAgentActivity`; subagent `TitleFallback` = `agent_nickname · agent_role` else `agent_path`; skip list; per-file debug drift fingerprint; zero-human warning with `cli_version`/`history_mode` **only for non-subagent files**
- [ ] 6.4 Create `internal/vault/codex_patch.go`: `*** Begin Patch` → unified diff + add/remove counts; `ok=false` on malformed; wire into `apply_patch` results **only on success** (`metadata.exit_code == 0`, else body starts with `Success.`); `codex_patch_test.go` (add, update two hunks, delete, move, malformed) and decoder assertions: Diff on success, none on failure, none on malformed
- [ ] 6.5 `codex_decoder_test.go`: human/assistant/tool entries and `Parts` order, summaries (never the encrypted message), header stripping, `Meta`, `ParentUUID` on both child variants, ≥ 0.147 child → zero Human, non-empty agent `TitleFallback`, no warning; `<recommended_plugins>` never a Human entry; `ChildUUID` from both shapes; developer-role and injected-context produce no Human; unknown types skipped; consumers on Codex bytes: `ScanSession` roles/title/`MessageCount > 0` for both children, `RenderText` `Codex` heading, `ParseTranscript` openable child marker
- [ ] 6.6 Create `internal/vault/codex_canary_test.go` over `$CODEX_HOME/{sessions,archived_sessions}` (skip when absent): Assumptions 1, 2 (per-file event-vs-filtered-response-item count reconciliation, subagent files exempt), 3, 11 (parent-resolved child ids match discovered children); zero-human warning never fires for a non-subagent file; no panic/error; passes over the 168 local rollouts

## Task 7: Codex discovery, import and restore round-trip
- **Status:** pending
- **Depends on:** Task 3, Task 4, Task 5, Task 6
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [implementation.md#slice-7-codex-discovery-import-and-restore-round-trip](./implementation.md#slice-7-codex-discovery-import-and-restore-round-trip)

### Subtasks
- [ ] 7.1 `internal/config/paths.go`: `CodexHome()` (honors `CODEX_HOME`, default `~/.codex`) with tests
- [ ] 7.2 `discovery.go`: `Discoverer` interface, `claudeDiscoverer` (existing logic), `codexDiscoverer` over `sessions/` and `archived_sessions/`; `parseRolloutFilename`; `SessionFile.{Platform, RelativePath (slash-separated, .zst stripped), ProjectPath, Compressed, OnDiskSize}`; bounded `readFirstLine` with zstd decode; `_<rollout_id>` variants **skipped with a warning and reported** in `DiscoveryReport.SkippedRevertVariants`; Codex walker visits `sessions/` **before** `archived_sessions/`, sorted within each root only; `DiscoverAll() ([]SessionFile, DiscoveryReport, error)`; `DiscoverSessions(root)` autodetects Codex layout; warn+skip on unparseable names; tests over a temp `CODEX_HOME` (legacy, paginated, archived, zst, revert variant skipped, misnamed, same thread under both roots → `sessions/` first)
- [ ] 7.3 `import.go`: decompress before hashing; **in-run reconciliation map** recording every uuid seen, consulted before `SessionDigest` (already-seen uuid: same hash or smaller → skipped with hint untouched, larger divergent → replace pending/DB record; identical in dry run); **location policy** (first sighting only: same-hash file at a path other than the stored hint → metadata-only `UpdateLocationHint`, reports `updated`; with `sessions/` discovered first, a thread under both roots keeps its active path and the archived copy is skipped); decoder dispatch via `scanSessionAndSubagents(uuid, platform, main, files)`; `ClaudeProjectDir` = mangled dir or `RelativePath`; `ProjectPath` = `ScanOutput.CWD` → hint → `resolveProjectPath`; `--project` matches `ProjectDir` or `ProjectPath`; `ImportOptions.Platform`; tests: distinct threads `new`; active + archived pair → one `new` (active path stored) + one `skipped` (real and dry run, no `StatusError`); second run with both copies → all `skipped`, hint unchanged; only the archived copy left → `updated` hint with unchanged hash; both child variants imported; `Platform`, `ClaudeProjectDir`, `ParentUUID`; first Codex write raises `min_reader_version` to 3
- [ ] 7.4 `restore.go`: `RestoreSessionAt(uuid, mainRel, raw, files, root, overwrite)` with path-safety validation; `RestoreSession` delegates; tests: path equals `mainRel`, SHA-256 equals `content_hash`, `..` rejected
- [ ] 7.5 `cmd/capy/vault.go`: `import --platform`, default `DiscoverAll()`, `--source` help; `defaultRestoreRoot` → `$CODEX_HOME` for Codex; `restoreVaultSession` passes the relative path; `printImportResult` per-platform counts + skipped revert variants
- [ ] 7.6 Integration test: import → `Search` human phrase `Role == user` → `RenderText` shows `[Codex]` → `RestoreSessionAt` round-trips; manual: `./capy vault import --dry-run --platform codex` lists 168 candidates with no errors; restore reproduces a file with SHA-256 equal to `content_hash`

## Task 8: Sweep, merge and reindex dispatch
- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** Task 9, Task 10
- **Docs:** [implementation.md#slice-8-sweep-merge-and-reindex-dispatch](./implementation.md#slice-8-sweep-merge-and-reindex-dispatch)

### Subtasks
- [ ] 8.1 `internal/server/server.go` `vaultSweep`: **independent per-platform discovery** (no early return on Claude failure/empty); Claude first, then Codex filtered by cleaned, symlink-resolved cwd equality (all under `CAPY_VAULT_SWEEP_ALL`); `CodexLocationSizes` pre-filter (path + on-disk size for plain, path for `.zst`) before first-line reads; missing `CODEX_HOME` is debug; per-platform counts in the info log; `countProjects` platform-aware; tests: symlinked project dir, **no Claude root**, second sweep reads zero first lines and an appended rollout is re-read
- [ ] 8.2 `merge.go`: dispatch FTS rebuild on carried platform; absent column → Claude with **no sniff**; present unrecognized value → `DetectFormat`, `StatusError` only for non-JSON; `--project` filter `claude_project_dir LIKE ? OR project_path LIKE ?` (update flag help); merge matrix tests (Codex rows; pre-0006 source with `file-history-snapshot` first lines merges fully; bogus platform sniffed to Claude when the blob is JSON, error when not; source marker 4 refused; dry run)
- [ ] 8.3 `reindex.go` `rebuildSessionFTS`: pass `sess.Platform` with the same unrecognized-value fallback; test over a mixed vault

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
- [ ] 10.3 `tui/viewer.go`: `openFocusedMarker` returns a `viewerOpenChild{uuid}` action for a marker with `ChildUUID` (no store access in the viewer); header `Codex · child of <short id>`; `viewer_test.go` asserts the action is returned and the viewer is not mutated
- [ ] 10.4 `tui/app.go`: root `Model` handles `viewerOpenChild` — `GetSession`/`GetFiles` via `dataStore`, push a `viewerFrame` (session, files, viewport offset, focused marker) onto `viewerStack`, load the child; `esc`/`q` pops a frame when the stack is non-empty, else returns to `prevMode` as today; a `GetSession` miss sets the root status "child session not archived"; `app_test.go` with the stub store: open child → return to parent at the same offset → return to list; two-level chain; unarchived child

## Task 11: MCP result metadata and stats
- **Status:** pending
- **Depends on:** Task 5, Task 7
- **Size:** S
- **Can run in parallel with:** Task 8, Task 9, Task 10, Task 12
- **Docs:** [implementation.md#slice-11-mcp-result-metadata-and-stats](./implementation.md#slice-11-mcp-result-metadata-and-stats)

### Subtasks
- [ ] 11.1 `internal/server/tool_vault_search.go` `formatVaultHit` and the federated formatting in `tool_search.go`: platform in the meta line of every vault hit, `child of <short id>` marker, `session:<uuid>` tag unchanged; tests for a Codex hit and for a Claude hit (platform label added, everything else byte-identical)
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
- [ ] 13.1 `docs/architecture.md` § Vault: transcript-model seam and decoders (replace the "three independent readers" framing), `platform`/`parent_uuid`, reader version 3 and older-binary behavior, Codex discovery and restore paths, revert-variant skip, 12-char short id, CLI table updates; grep every named symbol
- [ ] 13.2 `README.md` § Session Vault: Codex paragraph (discovery roots, restore root, `--platform`, children hidden by default, revert variants skipped, resume not yet supported, older binaries refuse a Codex-bearing vault)
- [ ] 13.3 `docs/feat/done/vault/design.md` *Not Doing*: one-line pointer to this feature
- [ ] 13.4 Write `docs/adr/031-transcript-model-seam-and-multi-platform-vault.md` (context, decision incl. ordered parts, Codex-positive detection, reader bump to 3, revert skip; consequences; alternatives per design § Rejected Alternatives); link from `docs/architecture.md`
- [ ] 13.5 Index a `kk:project-conventions` note that the three-parser convention is superseded by the transcript model (decoders own format, consumers own policy) — only now that the code reflects it; update the existing `kk:arch-decisions` Codex notes to say the reader version **is** bumped to 3

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
- [ ] 14.5 Run `/kk:review-spec` skill to verify implementation matches design and implementation docs

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

Tasks 1 and 2 start together (Task 2 adds files only, so the tree keeps building). Tasks 3, 4, 5, 6 run in parallel once 1 and 2 land (5 and 6 need only 2). Task 7 is the integration point. Tasks 8–12 fan out from 7 and converge on 13, then 14.
