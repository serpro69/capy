# Codex Sessions in the Vault — Implementation Plan

**Design:** [design.md](./design.md) · **Tasks:** [tasks.md](./tasks.md) · **Research:** [research.md](./research.md)

**Status:** Draft

This plan is written for an experienced Go developer with no prior exposure to capy or to agent-CLI session formats. It is sliced Risk-First: the refactor that touches every Claude display path lands first behind a mechanical parity gate, then Codex support arrives as vertical slices that each end in a user-visible, testable path.

## Orientation

### Build and test

Everything in `internal/vault` and `cmd/capy` needs `-tags fts5` and both keys:

```bash
export CAPY_DB_KEY=test-key-for-development
export CAPY_VAULT_KEY=test-key
go test -tags fts5 -count=1 ./internal/vault/... ./cmd/capy/... ./internal/server/...
make test-race
make bench-quality && make bench-compare BASE=master TARGET=<branch>   # required by Task 14
```

### Files you will touch, and why

| File | Role today | Change |
|---|---|---|
| `internal/vault/scanner_types.go` | Claude wire types (`jsonlLine`, `jsonlMessage`, `contentBlock`) | Unchanged; consumed by the Claude decoder only |
| `internal/vault/scanner.go` | FTS extraction: pass 1 (parse lines) + pass 2 (emit `ScanResult`) | Pass 1 moves into the Claude decoder; pass 2 becomes `ScanTranscript` over entries |
| `internal/vault/render.go` | `show` renderer with its own pass 1 | Pass 1 removed; consumes entries |
| `internal/vault/transcript.go` | TUI parser with its own pass 1 | Pass 1 removed; consumes entries; child-session markers |
| `internal/vault/diff.go` | `structuredPatch` → unified diff | Called by the Claude decoder to fill `Diff` |
| `internal/vault/chunker.go` | `ScanResult` → chunks | **Untouched** |
| `internal/vault/discovery.go` | Claude directory walk | `Discoverer` interface; Codex walker; filename parser |
| `internal/vault/import.go` | disk → `SessionRecord` | zstd read, decoder dispatch, new `Session` fields |
| `internal/vault/store.go` | schema, statements, list/search | migration 0006 columns everywhere they are selected or written |
| `internal/vault/migrations.go` | name-keyed migration runner | `migrate0006AddPlatform` |
| `internal/vault/merge.go` | cross-vault union | feature-detect columns, carry, dispatch |
| `internal/vault/reindex.go` | DB-driven FTS rebuild | dispatch on stored platform |
| `internal/vault/restore.go` | write session back to disk | relative-path variant for Codex |
| `internal/vault/tui/{list,viewer,render}.go` | bubbletea UI | children toggle, platform labels, child open target |
| `cmd/capy/vault.go` | cobra commands | flags, columns, headers, resume guard, delete warning |
| `internal/server/{server,tool_vault_search,tool_search,tool_stats,tool_doctor}.go` | sweep, MCP tools | Codex sweep, `platform` metadata, per-platform stats, doctor check |
| `internal/platform/{doctor,routing}.go` | doctor checks, routing text | platform-roots check, wording |
| `internal/config/paths.go` | `ClaudeProjectsDir`, unmangle | `CodexHome()` |

New files: `internal/vault/platform.go`, `transcript_model.go`, `claude_decoder.go`, `codex_types.go`, `codex_decoder.go`, `codex_patch.go`, and their tests; `internal/vault/golden_test.go`, `parity_canary_test.go`, `codex_fixtures_test.go`, `codex_canary_test.go`; `internal/vault/testdata/golden/`; `docs/adr/031-*.md`.

### Invariants that bite

- **Verbatim bytes.** `raw_jsonl` is the plain (decompressed) file; `content_hash` and `size_bytes` are computed on those bytes (`computeContentHash`). Never hash or store `.zst` bytes.
- **Do not bump `currentIndexVersion`.** Claude extraction is byte-identical after this work; the version exists to detect stale Claude rows, and nothing here makes them stale (ADR-025). Codex rows stamp the current value.
- **Migration pattern.** Copy `migrate0005AddSessionNames`: read-only fast path, `BeginImmediateContext`, re-check inside the transaction, `columnExists` guards, record by name. Fresh vaults get the same DDL through `schemaSQL`.
- **Merge sources are not migrated.** `MergeFrom` opens the source read-only without running migrations, so every new column must be feature-detected (`columnExists`) and substituted with a literal when absent, exactly as `encoding` is today.
- **Consumers own policy, decoders own format.** Secret stripping, truncation, `ftsExcludedResult`, collapse thresholds and labels live in scanner/render/transcript. Decoders never sanitize.
- **One rendered TUI row is one viewport line.** Any new marker row must pass through `singleLine` (see `docs/architecture.md` § Tool-result display).

## Slices

Each step names its verification. A step whose verification you cannot state is too vague; stop and refine it.

### Slice 1: Golden and parity harness

Purpose: freeze today's Claude behavior so the refactor cannot drift silently.

1. Add `internal/vault/golden_test.go`. Build a named table of fixture blobs covering every Claude line shape the readers handle: plain-string user, block-array user with text plus `tool_result` (one each of `Read` excluded, `Edit` with `toolUseResult.structuredPatch`, `Bash`, unknown tool id), progressive assistant snapshots sharing `message.id`, `ai-title`, `pr-link`, `system` `away_summary`, `attachment` `queued_command`, `attachment` with message content, a `>16 MB` oversize line (synthesize via a repeated-byte payload; keep it under the 16 MB reader cap by lowering the cap through a test hook if runtime cost is a problem), a malformed JSON line, and a subagent sidecar. Reuse the builders in `fixtures_test.go`. → verify: the table has at least one case per `case` arm in the three `switch line.Type` blocks of `scanner.go`, `render.go`, `transcript.go`.
2. For each case, run `ScanSession`, `ScanSubagent` (sidecar cases), `RenderText`, `RenderMarkdown`, `ParseTranscript`; serialize each result deterministically (JSON with fixed struct field order, times as RFC3339Nano UTC) and compare to `testdata/golden/<case>.<reader>.{json,txt}`. Support `-update` to regenerate. → verify: `go test -tags fts5 -run TestGolden ./internal/vault/` passes on `master`; the golden files are committed.
3. Add `internal/vault/parity_canary_test.go`, gated on `CAPY_VAULT_PARITY_BASELINE=<path>`; `t.Skip` when unset or when `config.ClaudeProjectsDir()` has no sessions. Walk every `<uuid>.jsonl` and `<uuid>/subagents/*.jsonl`; compute one SHA-256 per file over the concatenated serialized outputs of the four readers. If the baseline file is missing, write `path\tdigest` lines and pass; otherwise compare and fail listing mismatching paths. → verify: run once on `master` to produce the baseline, run again unchanged: 0 mismatches.

### Slice 2: Transcript model and Claude decoder (Contract-First)

Purpose: define the seam and prove the Claude decoder against it before any consumer moves.

1. Add `internal/vault/platform.go`: `type Platform string`; constants `PlatformClaudeCode = "claude-code"`, `PlatformCodex = "codex"`; `ParsePlatform(string) (Platform, error)`; `(Platform) DisplayName()` returning `Claude` / `Codex`; `DetectFormat(firstLine []byte) (Platform, error)` per design § Format Identification. → verify: table test covering Codex first line, Claude first line, garbage, empty.
2. Add `internal/vault/transcript_model.go` with `Transcript`, `Meta`, `Entry`, `EntryKind`, `ToolCall`, `Launch`, `Diff`, the `Decoder` interface (`Decode(io.Reader) (*Transcript, error)`) and `DecoderFor(Platform) Decoder` (Codex returns a stub that errors until Slice 6). Field set per design § The Transcript Model, including `Meta.TitleFallback` and `Entry.SearchOnly`. → verify: `go vet`; doc comments state the pre-policy contract.
3. Add `internal/vault/claude_decoder.go`. Move the pass-1 logic out of the three readers: line parsing and `message.role` type inference, snapshot merging (`mergeBlocks` and `dedupBlocks` collapse into one function), `cleanText` applied to human text, `queuedCommandPrompt` → Human with `Queued`, `prLinkText` and `away_summary` → System, `attachmentText` → System with `SearchOnly`, `collectToolUseSummaries` + `toolUseSummary` → `ToolCall.Summary`, `Task`/`Agent` → `Launch{Label: subagentLaunchLabel}`, `toolResultText` → `ToolResult.Body`, `diffBodyFromToolResult` → `ToolResult.Diff` for `diffResultTools` (one `toolUseResult` per line; first diff-tool result claims it, as today). Compute `Meta` including the plain-string-only `TitleFallback` rule. → verify: `claude_decoder_test.go` asserts entries and `Meta` for each golden case; the `LineIndex` of a merged snapshot is the first snapshot's line.

### Slice 3: Scanner onto the model

1. Rewrite `scanner.go`: `ScanSession(p Platform, r io.Reader)` decodes via `DecoderFor(p)` then calls `ScanTranscript(*Transcript) *ScanOutput`; `ScanSubagent` uses the Claude decoder. Keep every policy constant and helper (`excludedResultTools`, `diffResultTools`, `ftsExcludedResult`, `truncateHeadTail`, `prefixToolResult`, `maxToolResultChars`, `titleMaxChars`) in `scanner.go`. Delete the now-duplicated pass-1 code. → verify: `go vet` reports no unused symbols left behind by your change.
2. Update `import.go` `scanSessionAndSubagents` to pass `PlatformClaudeCode` for now. → verify: `go build ./...`.
3. → verify: `TestGolden` scanner files unchanged; parity canary 0 mismatches; `go test -tags fts5 ./internal/vault/...` green.

### Slice 4: Render and transcript onto the model

1. Rewrite `render.go`: `RenderText(p, raw)` / `RenderMarkdown(p, raw)` decode then format; `renderUserContent` and `collapsedToolResult` become entry-based; `displayLabels` assistant label comes from `p.DisplayName()`. → verify: `TestGolden` render files unchanged for Claude.
2. Rewrite `transcript.go`: `ParseTranscript(p, raw, subagentIDs)`; `splitUserContentForViewer` and `overCollapseThreshold` operate on ToolResult entries; `Diff` comes from the entry; launch markers come from `ToolCall.Launch`; add `TranscriptMessage.ChildUUID` and set `Openable` when it is non-empty (Claude keeps the count-based `AgentID` mapping). → verify: `TestGolden` transcript files unchanged; existing `transcript_test.go` green.
3. Thread the platform through callers: `cmd/capy/vault.go` `renderShow`, the TUI viewer's parse call, and search-hit rendering. Pass `PlatformClaudeCode` until Slice 5 adds `Session.Platform`. → verify: `go test -tags fts5 ./internal/vault/tui/... ./cmd/capy/...` green; parity canary 0 mismatches.

### Slice 5: Migration 0006 and platform plumbing (Claude-only)

1. `migrations.go`: add `migrate0006AddPlatform` following the 0005 pattern: `ALTER TABLE vault_sessions ADD COLUMN platform TEXT NOT NULL DEFAULT 'claude-code'` and `ADD COLUMN parent_uuid TEXT` behind `columnExists`, `CREATE INDEX IF NOT EXISTS idx_sessions_parent ON vault_sessions(parent_uuid)`, record `0006_platform`. Register in `migrateVault`. Add the same columns and index to `schemaSQL`; share the index DDL as a constant. → verify: `migrations_test.go` builds a pre-0006 vault (create the 0005 schema by hand in a temp DB), opens it through `VaultStore`, asserts both columns exist with the defaults and the migration row is recorded; opening a fresh vault records the migration without an ALTER.
2. `store.go`: `Session.Platform Platform`, `Session.ParentUUID string` (empty == NULL via `nullString`); extend `sessionMetaColumns`, the scan helpers, `stmtInsertSession`, `stmtUpdateSession`, `GetSession`, `ListSessions`; `ListOptions.IncludeChildren bool` (false → `AND s.parent_uuid IS NULL`) and `ListOptions.Platform Platform` (`AND s.platform = ?`); add `Children(ctx, parentUUID) ([]Session, error)`; `SearchResult.Platform` / `.ParentUUID` populated by `Search` and `SearchChunks` (`chunk_search.go`); `VaultStats.ByPlatform []PlatformStat{Platform, Sessions, Bytes}` and `VaultStats.Children int`. Parameterized, context-aware, `rows.Close` + `rows.Err`. → verify: `store_test.go` covers list default hides a row with `parent_uuid`, `IncludeChildren` shows it, `Platform` filter, `Children`, stats breakdown.
3. `import.go` `buildRecord`: set `Platform` from `sf.Platform` (Claude for now) and `ParentUUID` from `Meta`. `merge.go`: feature-detect `platform` and `parent_uuid` with `columnExists`, substitute `'claude-code'` / `NULL` literals when absent, carry into `sourceSession` and `toRecord`. → verify: merge tests green against a pre-0006 source fixture and a 0006 source.
4. → verify: full vault suite green; `capy vault list --json` on a real vault shows `"platform": "claude-code"` on every row.

### Slice 6: Codex decoder

Fixture seeds: [research.md Appendix A](./research.md#appendix-a--redacted-sample-lines-fixture-seeds). Keep a legacy and a paginated variant of every builder.

1. Add `codex_types.go`: envelope `{timestamp, type, payload json.RawMessage}`; typed payloads for `session_meta` (including `source` as `json.RawMessage` — string or object — and the lifted `parent_thread_id`), `message`, `function_call`, `function_call_output` (`output` as `json.RawMessage`: string or array), `custom_tool_call`, `custom_tool_call_output`, `web_search_call`, `event_msg` variants (`user_message`, `item_completed` with `item{type, id, content[], agent_thread_id}`, `collab_agent_spawn_end`, `task_started`). → verify: unit tests unmarshal each Appendix A line into its type.
2. Add `codex_fixtures_test.go` with builders mirroring `fixtures_test.go` (`codexSessionMeta(opts)`, `codexUserEvent`, `codexUserItem`, `codexAssistant`, `codexFunctionCall`, `codexFunctionOutput`, `codexApplyPatchPair`, `codexSpawnPair`, `codexNoise…`, `writeCodexRollout(t, home, relPath, lines, compressed bool)`). → verify: builders produce lines that round-trip through the types.
3. Add `codex_decoder.go` implementing `Decoder` per design § The Codex Decoder: pass 1 collects entries and pending calls; pass 2 resolves `call_id` correlation and `Launch.ChildUUID`; human turns from events with the noise-filtered `response_item` fallback only when events produced none; `stripExecHeader`; second JSON decode for `custom_tool_call_output`; skip list; drift fingerprint at debug; zero-human warning with `cli_version` and `history_mode`. → verify: `codex_decoder_test.go` asserts, for legacy and paginated fixtures alike: human entries count and text, assistant text, `ToolCall.Summary` for each call kind, `ToolResult.Body` after header stripping (exit-code line kept), `Meta` fields, `ParentUUID` on a child, `Launch.ChildUUID` resolved from both event shapes, developer-role and injected-context lines produce no Human entry, unknown types are skipped without error.
4. Add `codex_patch.go`: convert `*** Begin Patch … *** End Patch` (Add / Update / Delete / Move File, `@@` hunks, `+`/`-`/context lines) into unified-diff text plus add/remove counts; return `ok=false` on malformed input. Wire it into the decoder for `apply_patch` results. → verify: `codex_patch_test.go` covers add, update with two hunks, delete, move, and malformed; the decoder test asserts `Diff` on the `apply_patch` result and none on a malformed patch.
5. Run the three consumers on Codex bytes: `ScanSession(PlatformCodex, …)` yields `role=user` for human turns, `assistant`, `tool` rows prefixed with the call summary, `MessageCount > 0`, title equals the first human turn (sanitized, ≤120 runes); `RenderText` shows the `Codex` heading; `ParseTranscript` has an `Openable` marker with `ChildUUID`. → verify: assertions in the decoder test file or a `codex_consumers_test.go`.
6. Add `codex_canary_test.go`: walk `$CODEX_HOME/sessions` and `archived_sessions` (skip when absent); assert design Assumptions 1–3 and that no file with `task_started` events yields zero human entries; assert no panic and no decode error. → verify: passes locally over the 164 rollouts; `t.Skip` in CI.

### Slice 7: Codex discovery, import and restore round-trip

1. `internal/config/paths.go`: `CodexHome()` honoring `CODEX_HOME`, default `~/.codex`. → verify: unit test with the env set and unset.
2. `discovery.go`: introduce `Discoverer` with `claudeDiscoverer` (today's code) and `codexDiscoverer`; `parseRolloutFilename(name) (localTS, threadUUID, rolloutID string, compressed, ok bool)`; `SessionFile` gains `Platform`, `RelativePath` (slash-separated, `.zst` stripped), `ProjectPath`, `Compressed`; `readFirstLine(path, compressed)` bounded to `maxScanLineBytes`, decompressing through `codec.go` when needed; `DiscoverAll()` returns sessions from every platform root that exists; `DiscoverSessions(root)` keeps its signature and autodetects the Codex layout (`sessions/` or `archived_sessions/` child with `rollout-*` files). Skip symlinks and unparseable names with a warning. → verify: discovery tests over a temp `CODEX_HOME` with legacy, paginated, archived, `.zst`, `_<rollout_id>` and a misnamed file.
3. `import.go`: read the main file, decompress when `Compressed`, then hash; `buildRecord` uses `DecoderFor(sf.Platform)` via `scanSessionAndSubagents(uuid, platform, main, files)`; `ClaudeProjectDir` = `sf.ProjectDir` (Claude) or `sf.RelativePath` (Codex); `ProjectPath` = `Meta.CWD`, else `sf.ProjectPath`, else `resolveProjectPath`; `ImportOptions.Project` matches `sf.ProjectDir` **or** `sf.ProjectPath`; `ImportOptions.Platform` restricts. → verify: import test over the temp home: statuses `new` for distinct threads, `skipped` for the archived duplicate and on a second run, `excluded` never for a child; `Session.Platform == codex`, `ClaudeProjectDir` equals the relative path, `ParentUUID` set on the child.
4. `restore.go`: add `RestoreSessionAt(uuid, mainRel, raw, files, root, overwrite)`; `RestoreSession` delegates with `uuid + ".jsonl"`. Validate `mainRel` with `validateSidecarRel` and `safeChildPath`. → verify: restore test writes to `<root>/<mainRel>`, SHA-256 equals the plain source bytes, a `..` in `mainRel` is rejected.
5. `cmd/capy/vault.go`: `import` gains `--platform`; `--source` help text mentions both layouts; default (no `--source`) calls `DiscoverAll()`; `defaultRestoreRoot` returns `$CODEX_HOME` for Codex; `restoreVaultSession` passes `sess.ClaudeProjectDir` as `mainRel` for Codex. `printImportResult` groups counts per platform. → verify: `./capy vault import --dry-run --platform codex` against the real `~/.codex` lists 164 candidates; `./capy vault import` then `./capy vault restore <codex-id> --output /tmp/x` reproduces the file at the original relative path with equal SHA-256.
6. Integration test (`cmd/capy/vault_test.go` or `internal/vault/import_test.go`): import → `Search` finds a human phrase with `Role == user` → `RenderText` contains `[Codex]` → `RestoreSessionAt` round-trips. → verify: test green.

### Slice 8: Sweep, merge and reindex dispatch

1. `internal/server/server.go` `vaultSweep`: after the Claude discovery, discover Codex rollouts and keep those whose `ProjectPath` equals the cleaned, symlink-resolved `s.projectDir` (all of them under `CAPY_VAULT_SWEEP_ALL`); a missing `CODEX_HOME` is `slog.Debug`; log per-platform counts. `countProjects` counts `ProjectPath` for Codex. → verify: sweep integration test with a temp `CODEX_HOME` and a project dir reached through a symlink: only matching-cwd rollouts are imported.
2. `merge.go`: dispatch the FTS rebuild on the carried platform; when the source lacks the column or holds an unknown value, `DetectFormat` the first line and, on failure, record `StatusError` for that session. `sourceSessionUUIDs` `--project` filter becomes `claude_project_dir LIKE ? OR project_path LIKE ?`; update the flag help in `cmd/capy/vault.go`. → verify: merge test matrix: Codex rows from a 0006 source land with platform and parent intact and searchable; a pre-0006 source still merges; a source row with `platform = 'bogus'` is reported as an error, not scanned as Claude; dry run reports the same decisions.
3. `reindex.go` `rebuildSessionFTS`: pass `sess.Platform` with the same fallback-and-error posture. → verify: reindex test over a vault holding both platforms rebuilds both; a Codex row's FTS rows have `role=user` human turns after rebuild.

### Slice 9: CLI surfaces

1. `list`: platform column in `printSessionTable`; `--platform`, `--include-children` flags mapped to `ListOptions`; children rows show `↳ <parent short id>`; `sessionsToJSON` adds `platform`, `parent_uuid`. → verify: CLI unit tests on the table and JSON output; a parent-only default listing and an `--include-children` listing differ by exactly the child rows.
2. `show`: `writeShowHeader` prints the platform, the parent for a child, and the children (via `Children`) for a parent. → verify: unit test on the header for a Codex parent, a Codex child and a Claude session.
3. `search`: `printSearchResults` / `resultsToJSON` include the platform and a child marker. → verify: unit test.
4. `stats`: `printStats` / `statsToJSON` print `ByPlatform` and `Children`. → verify: unit test.
5. `resume`: in `resumeVaultSession`, load the session **before** `exec.LookPath("claude")` and return a clear error for `PlatformCodex` naming `capy vault restore` and `codex resume <uuid>`; the TUI `R` action reaches the same function. → verify: unit test asserts the error text and that no process is launched.
6. `delete`: warn on stderr when `Children` is non-empty; delete only the addressed row. → verify: unit test.
7. `shortUUID(u string, p Platform)` returns 12 characters for Codex; use it in `printSessionTable`, `printSearchResults`, `handleLookupError` candidates and `writeShowHeader`. → verify: unit test; `handleLookupError` output for two colliding UUIDv7 prefixes is distinguishable.

### Slice 10: TUI

1. `tui/list.go`: `IncludeChildren` toggle bound to a key not in the existing map (check `keybindings_test.go`); platform column; children indented with the parent short id; help text updated. → verify: `list_test.go` toggles and asserts row sets; `keybindings_test.go` asserts no conflict.
2. `tui/render.go`: `roleLabel(role, platform)`; child markers render through `markerRowFor` and `singleLine`. → verify: `render_test.go` label assertions; the one-row-one-line invariant test covers the new marker.
3. `tui/viewer.go`: header `Codex · child of <short id>`; `openFocusedMarker` gains a child-session branch: `GetSession(ChildUUID)` + `GetFiles`, push a nested viewer, `esc`/`q` returns. Unresolved `ChildUUID` (not archived) shows a transient status, not an error dialog. → verify: `viewer_test.go` opens a child from a parent fixture and returns; an unarchived child yields the status message.

### Slice 11: MCP result metadata and stats

1. `internal/server/tool_vault_search.go` `formatVaultHit` and the federated formatting in `tool_search.go`: append the platform to the meta line and mark child hits (`child of <short id>`); keep the `session:<uuid>` tag. → verify: `tool_vault_search_test.go` and `tool_search_federation_test.go` assert the new fields for a Codex hit and unchanged output for a Claude hit.
2. `internal/server/tool_stats.go` vault section: per-platform rows from `VaultStats.ByPlatform`, plus the children count. → verify: stats test asserts the rows.

### Slice 12: Doctor and routing text

1. `internal/platform/doctor.go`: `CheckVaultPlatforms(roots []PlatformRoot, counts map[Platform]int) CheckResult` reporting, per platform, whether the root exists and how many sessions are archived; wire into `internal/server/tool_doctor.go` and `cmd/capy/doctor.go` next to `CheckVault`. → verify: doctor unit test for (both roots present), (Codex root absent), (vault disabled).
2. `internal/platform/routing.go` and `.capy/AGENTS.md`: the `session` kind row reads "archived transcripts from Claude Code and Codex". → verify: the routing template test (if present) and `capy setup --local` regenerate identical text; `git diff` shows only the wording change.

### Slice 13: Documentation and ADR

1. `docs/architecture.md` § Vault: add the transcript-model seam and decoders, replace the "three independent readers" framing, document `platform` / `parent_uuid`, Codex discovery/restore paths, the 12-character short id, and update the CLI table (`list --platform/--include-children`, Codex restore root, resume limitation). → verify: every symbol named in the section exists (`grep`).
2. `README.md` § Session Vault: a Codex paragraph (what is discovered, where it restores, `--platform`, children hidden by default, resume not yet supported). → verify: commands shown actually run.
3. `docs/feat/done/vault/design.md` *Not Doing*: append a one-line pointer that Codex support is designed in `docs/feat/wip/codex-vault-sessions/`. → verify: link resolves.
4. `docs/adr/031-transcript-model-seam-and-multi-platform-vault.md`: context (three duplicated readers, Codex), decision (pre-policy transcript model, per-platform decoders, `platform` column with sniff fallback, Go-side validation over SQL CHECK, standalone children with `parent_uuid`, relative-path location hint), consequences (byte-identical refactor gate, older-binary known gap, no `index_version` bump), alternatives (from design § Rejected Alternatives). → verify: `docs/adr/` listing shows 031; `docs/architecture.md` links it.
5. `research.md` banner: mark the design as landed. `kk:project-conventions` in the capy knowledge base: index a note that the "three parsers in sync" convention is superseded by the transcript model (decoders own format, consumers own policy). → verify: `capy_search(source: "kk:project-conventions", queries: ["vault transcript model decoder"])` returns the note.

### Slice 14: Final verification

Run `/kk:test`, `/kk:document`, `/kk:review-code` (Go), `/kk:review-spec` for this feature. Also: `make test-race`; `make bench-quality` on `master` and on the branch, then `make bench-compare BASE=master TARGET=<branch>` with no `!` regression markers; the parity canary against the baseline produced in Slice 1; the Codex canary over the local corpus.

## Verification and gates

| Gate | When | Passes when |
|---|---|---|
| Goldens | Slices 3, 4 | `TestGolden` unchanged (no `-update` in the diff) |
| Parity canary | Slices 3, 4, 14 | 0 mismatches against the `master` baseline |
| Migration test | Slice 5 | pre-0006 vault opens with defaults; fresh vault records 0006 |
| Codex fixtures | Slice 6 | both history modes, child pair, patch, header, zst, filename parser |
| Codex canary | Slices 6, 14 | Assumptions 1–3 hold over the local corpus; zero-human warning never fires |
| Round-trip | Slice 7 | restore SHA-256 equals source; path equals discovered relative path |
| Merge matrix | Slice 8 | pre-0006 source, Codex rows, bogus platform → error, dry run |
| Race | Slice 14 | `make test-race` green |
| Benchmarks | Slice 14 | `make bench-compare BASE=master` shows no `!` |

## Deferred work

Recorded here so it is not lost with the session. Each item names the what, the why, and the concrete next step.

1. **`capy vault resume` for Codex sessions.** *Why deferred:* Codex's DB-less filesystem fallback (`codex-rs/rollout/src/list.rs` `find_thread_path_by_id_str_in_subdir`) is unverified against a restored file, and the user ranked resume as nice-to-have. *Next step:* restore a Codex session with `capy vault restore`, delete its `state_5.sqlite` row is **not** allowed (sources-of-truth constraint) — instead pick a session Codex has never seen (copy a rollout from another machine), run `codex resume <uuid>` from the recorded cwd and confirm it loads. Then in `cmd/capy/vault.go` add `runCodexResume(bin, uuid, dir)` invoking `codex -C <dir> resume <uuid>` with `exec.LookPath("codex")`, branch `resumeVaultSession` on `sess.Platform`, and delete the fail-loud guard. Update README and the design's Deferred section.
2. **`SearchOnly` attachment asymmetry.** *Why deferred:* unifying it changes `show`/TUI output and would break the byte-identical gate. *Next step:* after Slice 4 lands, decide whether `attachmentText` content should render; if yes, drop the flag and regenerate goldens knowingly in a dedicated commit.
3. **Revert rollouts (`_<rollout_id>`).** *Why deferred:* zero local samples. *Next step:* when one appears, run the Slice 7 discovery and import tests against it and confirm larger-wins picks the intended file; adjust `parseRolloutFilename` if the real shape differs.
4. **Hiding non-interactive Codex sources (`exec`, `mcp`) from `list`.** *Why deferred:* Codex's picker hides them (`INTERACTIVE_SESSION_SOURCES`), but `Meta.Source` is not persisted in v1 and children are the only agreed parity point. *Next step:* if wanted, add a `source TEXT` column in a later migration and a `--source` filter.
5. **Generic MCP tool-input rendering.** Unchanged from ADR-025 § Deferred; `ToolCall.Input` now carries the raw JSON for both platforms, so the future change is consumer-side plus a `currentIndexVersion` bump.
