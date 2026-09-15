# Tasks: Codex Sessions in the Vault

> Design: [./design.md](./design.md)
> Implementation: [./implementation.md](./implementation.md)
> Research: [./research.md](./research.md)
> Review triage: [./design-triage.md](./design-triage.md)
> Status: pending
> Created: 2026-09-13 (revised 2026-09-13 after two design-review rounds)
> Not Doing: Codex `/rename` name promotion, reading Codex state DBs, indexing reasoning/world_state/token/turn_context/compacted history, per-platform index_version, cascading child delete, generic MCP input rendering, TUI `/subagents`-style picker, PreCompact for Codex, SQL CHECK on platform, Read-analog FTS exclusion for `exec_command`, sweep opt-out env var, negative sweep cache
> Deferred (see implementation.md § Deferred work): Codex `resume`, archiving revert rollouts, SearchOnly attachment unification, hiding non-interactive / non-thread_spawn sub-agent sources

## Task 1: Golden and parity harness
- **Status:** done (2026-09-15; isolated review: no P0–P2, six P3 observations — five applied, the mutable line-cap test hook accepted as designed and documented at the vars)
- **Depends on:** —
- **Size:** M (a divergence inventory over three readers, a golden table with one case per switch arm, and a real-corpus baseline is not an afternoon)
- **Can run in parallel with:** Task 2
- **Slicing:** Risk-First (freezes behavior before the riskiest change)
- **Docs:** [implementation.md#slice-1-golden-and-parity-harness](./implementation.md#slice-1-golden-and-parity-harness)

### Subtasks
- [x] 1.1 Write `internal/vault/testdata/golden/DIVERGENCES.md`: every behavioral divergence between `scanner.go`, `render.go`, `transcript.go` (plain-string-only title fallback, scanner-only `attachmentText`, scanner-only warn logs, collapse/diff policy differences, …), each mapped to a transcript-model field or marked "consumer policy / log-only"
- [x] 1.2 Introduce `var scanLineCap = maxScanLineBytes` / `var renderLineCap = renderMaxLineBytes` and pass them to `scanLines` from the public readers so tests can lower the oversize threshold
- [x] 1.3 Create `internal/vault/golden_test.go` with a named fixture table covering every `case` arm of the three `switch line.Type` blocks and every inventory row (plain-string user, block-array user with Read/Edit+structuredPatch/Bash/unknown tool results, progressive snapshots, text → tool_use → text in one assistant message, ai-title, pr-link, away_summary, queued_command attachment, attachment with message content, oversize line, malformed line, subagent sidecar), reusing `fixtures_test.go` builders
- [x] 1.4 Run `ScanSession`, `ScanSubagent`, `RenderText`, `RenderMarkdown`, `ParseTranscript` per case; serialize deterministically; compare against `internal/vault/testdata/golden/<case>.<reader>.*`; support `-update`; commit the golden files generated on `master` (25 cases × 4 reader files generated on `master` HEAD `c189cf2`; `TestGolden_CoversEveryLineType` enforces the one-case-per-arm rule mechanically)
- [x] 1.5 Create `internal/vault/parity_canary_test.go` gated on `CAPY_VAULT_PARITY_BASELINE`: walk `config.ClaudeProjectsDir()` sessions and subagent sidecars, one SHA-256 per file over the four readers' outputs; write the baseline when the file is missing, compare otherwise; `t.Skip` when unset or no sessions. **Deviation from plan:** each baseline row also records a digest of the reader *input* (raw bytes + sidecars) so a live session that grows between runs is reported as "input changed" and skipped, not failed — without it the compare run fails on the current session's own transcript every time. The env var must be an absolute path (`go test` runs with the package dir as cwd).
- [x] 1.6 Produce the baseline on `master` and re-run unchanged: 0 mismatches (700 files baselined at `$REPO/bench-results/vault-parity-baseline.tsv`, gitignored; compare run: 699 compared, 0 mismatches, 1 skipped as a live session)

## Task 2: Transcript model and Claude decoder
- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Slicing:** Contract-First (new internal boundary; adds files only, reader loops untouched so Tasks 3–6 build in parallel)
- **Docs:** [implementation.md#slice-2-transcript-model-and-claude-decoder-contract-first](./implementation.md#slice-2-transcript-model-and-claude-decoder-contract-first)

### Subtasks
- [ ] 2.1 Create `internal/vault/platform.go`: `Platform` type, `PlatformClaudeCode`/`PlatformCodex`, `ParsePlatform`, `DisplayName`, Codex-positive `DetectFormat(firstLine)` (Codex iff `type == "session_meta"` with object payload; Claude for any other JSON object; error only for non-JSON); table test incl. a Claude `file-history-snapshot` first line → Claude
- [ ] 2.1a `platform_test.go` pairs every `Platform` constant with the `readerVersion*` it requires and fails on an unpaired constant (the "new platform = reader bump" rule, design § Reader version)
- [ ] 2.2 Create `internal/vault/transcript_model.go`: `Transcript`, `Meta` (incl. `PlatformID`, `TitleFallback`, `ParentUUID`, `Source`), `Entry`/`EntryKind`, ordered `Part` (text | `ToolCall`), `ToolCall`, `Launch`, `Diff`, `Entry.SearchOnly`, `Decoder` interface, `DecoderFor(Platform)` (Codex stub errors until Task 6); doc comments state the pre-policy contract and that part order is load-bearing
- [ ] 2.3 Create `internal/vault/claude_decoder.go` **without touching the three reader loops**: reuse/relocate the shared helpers (`cleanText`, `queuedCommandPrompt`, `prLinkText`, `attachmentText`, `collectToolUseSummaries`, `toolUseSummary`, `subagentLaunchLabel`, `toolResultText`, `diffBodyFromToolResult`, unified `mergeBlocks`/`dedupBlocks`); type inference, snapshot merge, `queued_command`→Human(Queued), `pr-link`/`away_summary`→System, `attachmentText`→System(SearchOnly), assistant blocks → ordered `Parts`, `Task`/`Agent` launches, `toolResultText`→Body, `diffBodyFromToolResult`→Diff, `Meta` incl. plain-string-only `TitleFallback`
- [ ] 2.4 `claude_decoder_test.go`: entries and `Meta` for each golden case; merged snapshot `LineIndex` is the first snapshot's line; text → call → text keeps its order

## Task 3: Scanner onto the model
- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** Task 4, Task 6 (Task 5 waits for this task)
- **Slicing:** Risk-First ("Claude scanner output byte-identical" is the testable path; no user-visible change by design)
- **Docs:** [implementation.md#slice-3-scanner-onto-the-model](./implementation.md#slice-3-scanner-onto-the-model)

### Subtasks
- [ ] 3.1 Rewrite `scanner.go`: `ScanSession(p Platform, r)` = decode + `ScanTranscript(*Transcript)`; assistant row text = `Parts` in order (text, then `ToolCall.Summary`, newline-joined — today's `extractAssistantText`); `ScanOutput` gains `Platform`, `PlatformID`, `ParentUUID`, `Source` copied from `Meta`; `ScanSubagent` via the Claude decoder; keep all policy constants/helpers in `scanner.go`; delete the scanner's pass-1 loop and the orphans that deletion creates (render/transcript loops stay until Task 4)
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
- **Depends on:** Task 2, Task 3 (5.5 wires `ScanOutput.ParentUUID` and the platform parameter of `scanSessionAndSubagents`, both introduced by Task 3; every `sf.Platform` reference stays the `PlatformClaudeCode` constant until Task 7)
- **Size:** M
- **Can run in parallel with:** Task 4, Task 6
- **Slicing:** Contract-First (schema and store surface for later slices; Claude behavior unchanged)
- **Docs:** [implementation.md#slice-5-migration-0006-platform-plumbing-and-reader-version-claude-only](./implementation.md#slice-5-migration-0006-platform-plumbing-and-reader-version-claude-only)

### Subtasks
- [ ] 5.1 `migrations.go`: `migrate0006AddPlatform` (0005 pattern; `platform TEXT NOT NULL DEFAULT 'claude-code'`, `parent_uuid TEXT`, guarded by `columnExists`, **then** `CREATE INDEX IF NOT EXISTS idx_sessions_parent`); register in `migrateVault`; add the two **columns only** to `schemaSQL`'s `CREATE TABLE` — the index must not appear in `schemaSQL` (it runs before migrations)
- [ ] 5.2 `migrations_test.go`: hand-built pre-0006 vault opens with defaults, index present, migration recorded; fresh vault records it without ALTER; `grep idx_sessions_parent store.go` is empty
- [ ] 5.3 `store.go`: `Session.Platform`/`ParentUUID`; extend `sessionMetaColumns`, scan helpers, insert/update statements, `GetSession`, `ListSessions`; `ListOptions.IncludeChildren` (default hides `parent_uuid IS NOT NULL`) and `ListOptions.Platform`; `Children(ctx, parentUUID)`; `SearchResult.Platform`/`ParentUUID` in `Search` and `SearchChunks`; `VaultStats.ByPlatform`/`Children`; `CodexLocationSizes(ctx)`; `SessionDigest` additionally returns `claude_project_dir` (the location policy's comparison input); metadata-only `UpdateLocationHint(ctx, uuid, hint)` in its own immediate transaction
- [ ] 5.4 `codec.go`: `supportedReaderVersion = 3`; named `readerVersionZstd = 2` / `readerVersionPlatform = 3`; `markMinReaderVersion(ctx, tx, version)` monotonic upsert, called once per write transaction with the highest version any record in it requires; **`compact.go` `markCompressed` passes `readerVersionZstd`**; `codec_test.go`: absent → 3 on Codex write, 2 → 3 raised, 3 not lowered, marker 4 refused on open, Claude-only vault stays at 2; `compact_test.go`: compact on a Claude-only vault leaves the marker at 2
- [ ] 5.5 `import.go` `buildRecord` sets `Platform: PlatformClaudeCode` (constant until Task 7) and `ParentUUID` from `ScanOutput.ParentUUID` (Task 3); `merge.go` feature-detects `platform`/`parent_uuid` (`columnExists`, literal substitution when absent, **no sniff**) and carries them verbatim
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
- [ ] 6.2 Create `internal/vault/codex_fixtures_test.go` builders (legacy and paginated variants; session meta incl. ≤ 0.137 child **with** spawn prompt and ≥ 0.147 child **without**, and a `payload.timestamp` earlier than line 0's envelope timestamp; user event/item; assistant; function call/output pair with wrapper header; `codexApplyPatchPair(success)`; a `custom_tool_call_output` whose `output` is not JSON; spawn pair for both event shapes with an **encrypted** `message`; an aborted-at-startup shell (`session_meta`, `turn_context`, developer + injected user items, `task_started`, `turn_aborted` — 23 such files exist locally); noise incl. `<recommended_plugins>`, reasoning, compacted, token_count, world_state; `writeCodexRollout(t, home, relPath, lines, compressed)`)
- [ ] 6.3 Create `internal/vault/codex_decoder.go` implementing `Decoder`: `Meta.PlatformID` from `session_meta.id`; `StartTime` from `payload.timestamp` (else line 0's envelope time), `EndTime` from the last envelope time; events-first human turns; fallback to `response_item` user messages only when events yield none, dropping `developer` role and any text starting with `<` or `# AGENTS.md instructions`; assistant entries with ordered `Parts`; tool call summaries per design table (`spawn_agent` label from `task_name`/`agent_type`, then parent-side `agent_path`, then `message`); results by `call_id` with `stripExecHeader` (keep exit-code line) and second JSON decode for custom outputs (raw string as body, no exit-code line, no `Diff` when that decode fails); `Launch.ChildUUID` from `collab_agent_spawn_end` or `SubAgentActivity`; subagent `TitleFallback` = `agent_nickname · agent_role` else `agent_path`; skip list; per-file debug drift fingerprint; zero-human **warning** with `cli_version`/`history_mode` only for a non-subagent file with ≥ 1 Assistant and 0 Human entries; a 0-Assistant/0-Human shell logs at debug
- [ ] 6.4 Create `internal/vault/codex_patch.go`: `*** Begin Patch` → unified diff + add/remove counts; `ok=false` on malformed; wire into `apply_patch` results **only on success** (`metadata.exit_code == 0`, else body starts with `Success.`); `codex_patch_test.go` (add, update two hunks, delete, move, malformed) and decoder assertions: Diff on success, none on failure, none on malformed
- [ ] 6.5 `codex_decoder_test.go`: human/assistant/tool entries and `Parts` order, summaries (never the encrypted message), header stripping, `Meta` incl. `PlatformID` and payload-derived `StartTime`, `ParentUUID` on both child variants, ≥ 0.147 child → zero Human, non-empty agent `TitleFallback`, no warning; aborted shell → zero entries, **no warning**; assistant-but-no-human file → warning; malformed custom output → verbatim body; `<recommended_plugins>` never a Human entry; `ChildUUID` from both shapes; developer-role and injected-context produce no Human; unknown types skipped. (Consumer assertions on Codex bytes moved to Task 7.6 — they need Tasks 3 and 4.)
- [ ] 6.6 Create `internal/vault/codex_canary_test.go` over `$CODEX_HOME/{sessions,archived_sessions}` (skip when absent), decoder-level: Assumption 1 (line 0 is `session_meta` **and** `payload.id == filename uuid`), 2 (per-file event-vs-filtered-response-item count reconciliation, subagent files exempt), 3, 11 (parent-resolved child ids match discovered children); zero-human warning never fires on the corpus (captured `slog` handler); every 0-human/0-assistant file has no `task_complete` and their count is reported; no panic/error; passes over the 168 local rollouts

## Task 7: Codex discovery, import and restore round-trip
- **Status:** pending
- **Depends on:** Task 3, Task 4, Task 5, Task 6
- **Size:** L (six files across config, discovery, import reconciliation, restore, CLI and integration). Land it as two PRs without renumbering: **7a** = 7.1, 7.2, 7.4 (config, discovery, restore primitive — pure additions, no behavior change for Claude), then **7b** = 7.3, 7.5, 7.6, 7.7 (import reconciliation, CLI, consumers, round-trip)
- **Can run in parallel with:** —
- **Docs:** [implementation.md#slice-7-codex-discovery-import-and-restore-round-trip](./implementation.md#slice-7-codex-discovery-import-and-restore-round-trip)

### Subtasks
- [ ] 7.1 `internal/config/paths.go`: `CodexHome()` (honors `CODEX_HOME`, default `~/.codex`) with tests
- [ ] 7.2 `discovery.go`: `Discoverer` interface, `claudeDiscoverer` (existing logic), `codexDiscoverer` over `sessions/` and `archived_sessions/` taking `CodexDiscoverOptions{Skip func(relPath, onDiskSize, compressed) bool}` evaluated from `DirEntry` metadata **before** any open (accepted files dropped from the result); `parseRolloutFilename`; `SessionFile.{Platform, RelativePath (slash-separated, .zst stripped), ProjectPath, Compressed, OnDiskSize}`; bounded `readFirstLine` using a **streaming** zstd reader for `.zst` (never `DecodeAll`); `_<rollout_id>` variants **skipped with a warning and reported** in `DiscoveryReport.SkippedRevertVariants`; Codex walker visits `sessions/` **before** `archived_sessions/`, sorted within each root only; `DiscoverSessionsReport(root)` is the primary API (autodetects the Codex layout) with `DiscoverSessions(root)` kept as a report-dropping wrapper; `DiscoverAll(codexOpts) ([]SessionFile, DiscoveryReport, error)`; warn+skip on unparseable names; tests over a temp `CODEX_HOME` (legacy, paginated, archived, zst, revert variant skipped, misnamed, same thread under both roots → `sessions/` first, skip predicate leaves the file unopened via a counting hook, `.zst` first-line read stays far below file size, `--source` path returns the same report)
- [ ] 7.3 `import.go`: decompress before hashing; **in-run reconciliation map** recording every uuid seen, consulted before `SessionDigest` (already-seen uuid: same hash or smaller → skipped with hint untouched, larger divergent → replace pending/DB record; identical in dry run); **location policy gated on `sf.Platform == PlatformCodex`** (first sighting only: same-hash file whose `RelativePath` differs from the `claudeProjectDir` returned by `SessionDigest` → `UpdateLocationHint` in its own tx at decision time, reports `updated`; `ftsOnly` + moved → FTS rebuild batched and hint updated, one `updated`; dry run reports `updated` without writing; Claude rows never enter the branch); `PlatformID` ≠ filename uuid → warning, filename wins; decoder dispatch via `scanSessionAndSubagents(uuid, platform, main, files)`; `ClaudeProjectDir` = mangled dir or `RelativePath`; `ProjectPath` = `ScanOutput.CWD` → hint → `resolveProjectPath`; `--project` matches `ProjectDir` or `ProjectPath`; `ImportOptions.Platform`; tests: distinct threads `new`; active + archived pair → one `new` (active path stored) + one `skipped` (real and dry run, no `StatusError`); second run with both copies → all `skipped`, hint unchanged; only the archived copy left → `updated` hint with unchanged hash, and dry run reports `updated` with the DB hint unchanged; moved + version-stale → one `updated`, FTS rebuilt, hint updated; Claude loose-`--source` re-import → `skipped`, hint untouched; mismatched `session_meta.id` → imported under the filename uuid with a warning; aborted shell → `excluded`; both child variants imported; `Platform`, `ClaudeProjectDir`, `ParentUUID`, payload-derived `StartTime`; first Codex write raises `min_reader_version` to 3
- [ ] 7.4 `restore.go`: `RestoreSessionAt(uuid, mainRel, raw, files, root, overwrite)` with path-safety validation; `RestoreSession` delegates; a `<mainRel>.zst` twin is left untouched and reported in `RestoreResult.Notes`; tests: path equals `mainRel`, written bytes **byte-identical** to the input (equivalently `computeContentHash` over `{"<uuid>.jsonl": written}` equals `content_hash` — never `sha256(written)`), `..` rejected, `.zst` twin survives and is noted
- [ ] 7.5 `cmd/capy/vault.go`: `import --platform`, default `DiscoverAll(nil)`, `--source` via `DiscoverSessionsReport`, `--source` help; `defaultRestoreRoot` → `$CODEX_HOME` for Codex; `restoreVaultSession` passes the relative path and prints `Notes`; `printImportResult` per-platform counts + skipped revert variants; 12-char `shortUUID` for Codex rows in the import table
- [ ] 7.6 Consumers on Codex bytes: add `apply_patch` to `diffResultTools` (`scanner.go`); `codex_consumers_test.go`: `ScanSession(PlatformCodex, …)` roles/title/`MessageCount > 0` for both children and no `tool` row for an `apply_patch` body; `RenderText` `Codex` heading with the verbatim `apply_patch` body; `ParseTranscript` openable child marker and `Diff` marker for a successful patch; extend the canary with a consumer pass (every non-shell rollout scans to `MessageCount ≥ 1`; every rollout with a human turn has a `role=user` row)
- [ ] 7.7 Integration test: import → `Search` human phrase `Role == user` → `RenderText` shows `[Codex]` → `RestoreSessionAt` round-trips byte-for-byte; manual: `./capy vault import --dry-run --platform codex` lists 168 candidates with no errors (145 `new`, 23 `excluded` today); restore reproduces a file that `cmp` reports identical to the source

## Task 8: Sweep, merge and reindex dispatch
- **Status:** pending
- **Depends on:** Task 7
- **Size:** M
- **Can run in parallel with:** Task 9, Task 10
- **Docs:** [implementation.md#slice-8-sweep-merge-and-reindex-dispatch](./implementation.md#slice-8-sweep-merge-and-reindex-dispatch)

### Subtasks
- [ ] 8.1 `internal/server/server.go` `vaultSweep`: **independent per-platform discovery** (no early return on Claude failure/empty); Claude first, then Codex: load `CodexLocationSizes` once, build the `Skip` predicate (path + on-disk size for plain, path for `.zst`) and pass it **into** the Codex discoverer so skipped files are never opened; filter survivors by cleaned, symlink-resolved cwd equality (all under `CAPY_VAULT_SWEEP_ALL`); missing `CODEX_HOME` is debug; info log carries per-platform counts, skipped-by-predicate and first-line-read counts, and skipped revert variants; function comment states the accepted bound (one first-line read per rollout not archived at its current `(path, size)`, other projects included, every start); `countProjects` platform-aware; tests: symlinked project dir, **no Claude root**, second sweep opens zero Codex files, an appended rollout is re-read, an other-project rollout is re-read every sweep (pinned)
- [ ] 8.2 `merge.go`: dispatch FTS rebuild on carried platform; absent column → Claude with **no sniff**; present unrecognized value → `DetectFormat` with a warning and the **resolved** platform stored in the destination row, `StatusError` only for non-JSON; `--project` filter `claude_project_dir LIKE ? OR project_path LIKE ?` (update flag help); merge matrix tests (Codex rows; pre-0006 source with `file-history-snapshot` first lines merges fully; bogus platform sniffed to Claude when the blob is JSON and stored as `claude-code`, error when not; source marker 4 refused; dry run)
- [ ] 8.3 `reindex.go` `rebuildSessionFTS`: pass `sess.Platform` with the same unrecognized-value fallback and warning; the stored `platform` value is **not** rewritten (FTS-only path); test over a mixed vault incl. a `bogus` row that rebuilds correctly and still reads `bogus`

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
- [ ] 9.7 `shortUUID(u, p)`: 12 chars for Codex; apply in table, search output, import table, `handleLookupError` candidates, show header; test with colliding UUIDv7 prefixes. The TUI's `shortID` already truncates at 12 — no TUI change

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
- [ ] 12.1 `internal/platform/doctor.go` `CheckVaultPlatforms(roots []VaultPlatformRoot)` with a strings-only `VaultPlatformRoot{Name, Root string; RootExists bool; Archived int}` — `internal/platform` must not import `internal/vault` (verify with `go list -deps`); callers build the slice from `VaultStats.ByPlatform` + `config.CodexHome()`/`ClaudeProjectsDir()`; wire into `internal/server/tool_doctor.go` and `cmd/capy/doctor.go` beside `CheckVault`; tests for both roots, Codex root absent, vault disabled
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
- [ ] 13.4 Write `docs/adr/031-transcript-model-seam-and-multi-platform-vault.md` (context, decision incl. ordered parts, Codex-positive detection for corrupted values only, filename uuid as row identity, reader bump to 3 **and the standing rule "new platform constant ⇒ reader bump"**, `compact` stamps only 2, revert skip, no sweep opt-out; consequences; alternatives per design § Rejected Alternatives); link from `docs/architecture.md`
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
Task 1 ──┬─→ Task 3 ──┬─→ Task 5 ──┐
Task 2 ──┤            │            │
         ├─→ Task 4 ──┼────────────┼─→ Task 7 ──┬─→ Task 8  ──┐
         └─→ Task 6 ──┴────────────┘  (7a, 7b)  ├─→ Task 9  ──┤
                                                ├─→ Task 10 ──┼─→ Task 13 ─→ Task 14
                                   Task 5 ──────┼─→ Task 11 ──┤
                                                └─→ Task 12 ──┘
```

Tasks 1 and 2 start together (Task 2 adds files only, so the tree keeps building). Tasks 3, 4 and 6 run in parallel once 1 and 2 land (6 needs only 2). Task 5 needs Task 3 as well (it wires `ScanOutput.ParentUUID` and the platform parameter into `buildRecord`) and runs in parallel with 4 and 6. Task 7 is the integration point, landed as 7a then 7b. Tasks 8–12 fan out from 7 and converge on 13, then 14.
