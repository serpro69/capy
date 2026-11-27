# Vault session brief tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: in-progress
>
> Created: 2026-11-23
>
> Not Doing: physical-line reconstruction, prompt injection, inferred decisions, verbatim export

## Task 1. Read the latest exchange in the terminal

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Terminal brief](implementation.md#terminal-brief) · [Renderer](implementation.md#render-the-brief) · [Workload](implementation.md#verification)

### Execution context

- 2026-11-27 · observed · base `ff60647` (master, clean). Task 1 targets `internal/vault` (new reader + renderer) and `cmd/capy/vault.go`; no MCP, search or doc changes (Tasks 2–3).
- 2026-11-27 · observed · Read-only precedent: `internal/runstore/store.go` opens encrypted stores with `sqliteutil.EncryptedDSN(...)+"&mode=ro&_busy_timeout=100"` through `OpenWithCanary`. The normal vault open (`store.go` `getDB`/`openDB`) runs schema DDL, migrations and corruption backup, so the brief must not use it.
- 2026-11-27 · observed · Scanner contract (`scanner.go` `ScanTranscript`): only a `user` row opens a new turn; `assistant` rows (including tool-only) share the turn; sidecar rows carry a non-empty `subagent_id`. FTS text is already sanitized at write time, but `project_path` and `title` metadata are not FTS-sanitized, so the renderer redacts every field.
- 2026-11-27 · observed · The driver (`jgiannuzzi/go-sqlite3`) ignores `TxOptions`; a deferred transaction's snapshot starts at its first read, so the reader's first statement inside the transaction must be a read.
- 2026-11-27 · inferred · Required layout for the brief: `vault_sessions` (incl. `index_version` from 0003, `platform` from 0006), `vault_fts` position/role columns, `vault_meta`, `vault_session_names` (0005), `vault_session_projects` (0007). A missing migrated structure whose migration is unrecorded is a supported predecessor; a recorded migration with the structure missing is an inconsistent layout; a missing base table is not a recognized vault.
- 2026-11-27 · proposed (within design latitude) · An unrecognized stored platform is returned as an incompatible-archive error rather than rendered, since the design lists layout incompatibility as an error outcome.
- 2026-11-27 · observed · 1.1 workload (`CAPY_VAULT_BRIEF_WORKLOAD=1 go test -tags fts5 -run TestBriefWorkload -v ./internal/vault/`, `internal/vault/brief_workload_test.go`, this Mac, darwin/arm64, OS cache warm and not purged). 1,000 sessions × 100 rows × 256 B = 100,000 rows / 25,600,000 indexed text bytes; DB 44,105,728 B. Plan: `SCAN vault_fts VIRTUAL TABLE INDEX 0: | SCALAR SUBQUERY 1 | SEARCH vault_sessions USING INTEGER PRIMARY KEY (rowid=?) | USE TEMP B-TREE FOR ORDER BY`. Complete `ReadBrief`+`RenderBrief` for the oldest session (open, selection, fetch, render, close): live WAL with writer open (WAL 6,431,352 B) 241 ms; first reader after writer close (WAL 0 B) 239 ms; three fresh readers 235/238/238 ms. Shorter caller deadlines of ~119 ms and ~121 ms expired mid-read and returned `BriefTimeoutError` with no brief. Target (< 2 s) met with ~8× headroom; the scan-once access path is kept. Limits: warm cache only; one machine.
- 2026-11-27 · observed · Verification (captured via capy MCP, synthetic `CAPY_VAULT_KEY`/`CAPY_DB_KEY`, `-tags fts5`): `TestReadBrief*`, `TestRenderBrief*` pass, also under `-race`; `TestVaultBrief_*` (CLI subprocess, Claude + Codex import fixtures) pass; `go vet` clean. Full `./internal/vault/... ./cmd/capy/...` on the candidate fails exactly the same five tests as a detached `ff60647` baseline run in the same environment: `TestRestoreSession_OverwritePolicy`, `TestRestoreSession_SymlinkRootResolved`, `TestGeneratedLaunchVaultMigration`, `TestVaultCodex_RestoreDefaultRootIsCodexHome`, `TestVaultProject_RestoreResumeKeepPhysicalPaths` (observed `/var` vs `/private/var` temp-dir resolution under captured execution). Pre-existing and unrelated to this task; not investigated further here (owner: unassigned; next action: rerun outside captured execution / with a resolved `TMPDIR` to confirm they are environment-only).
- 2026-11-27 · observed · Isolated review (`/kk:review-code:isolated`; internal `kk:code-reviewer` + external clink `codex`, provider OpenAI assumed from PAL's shipped manifest; `claude` excluded as authoring provider; `agy` missing; `gemini` retired). Round 1: corroborated timeout-hint shell-safety issue (internal P3, codex P2) plus four internal-only P3s (marker read errors reported as incompatible; migration hint lacked vault selection; workload EXPLAIN duplicated SQL; after-resolution timeout not asserted). All fixed: `briefHintID` gates the `vault show` hint on intact + `^[A-Za-z0-9][A-Za-z0-9._-]*$`; `checkReaderVersion` returns unexported `*readerVersionError` (same message; `openDB`/`mergeSnapshot` behavior unchanged) and only that becomes `IncompatibleVaultError`; hint names `--path`/`CAPY_VAULT_PATH`; `briefPositionsSQL` shared; workload requires `BriefTimeoutError` naming the target; cancellation worded separately. Round 2: both reviewers APPROVE (Task 1 code scope); remaining doc-comment nit fixed. No P0/P1; nothing indexed as review findings.
- 2026-11-27 · observed · 1.4 workload rerun with the finished reader and renderer (same command and machine, warm cache): live WAL 227 ms; first after close 228 ms; fresh readers 227/230/227 ms; deadlines of ~113.5 ms both returned `BriefTimeoutError` naming `00000001-0000-4000-8000-000000000001`. Supersedes the timings above for the final code.
- 2026-11-27 · observed · A brief on a cleanly closed vault leaves `vault.db-wal` (0 B) and `vault.db-shm` (32 KiB); the main file's SHA-1 is unchanged and the next ordinary open removes both (throwaway `go build` + `vault import`/`brief`/`stats`). Allowed by the design's "SQLite may use auxiliary WAL/SHM files". Deferred: owner unassigned · reason: documentation belongs to Task 3.2 · next action: mention it with the command docs · verify: docs/reference/vault.md states it.
- 2026-11-27 · deferred to Task 2 · `BriefScope{}` means the whole vault, while the server's `vaultProjectScope` returns `("", "")` both for `all_projects` and when the server has no project directory. Owner: Task 2 implementer · reason: MCP wiring is Task 2 · next action: fail a default-scope request without a project directory before calling `ReadBrief` (implementation.md §MCP brief) · verify: a Task 2.2 test with no project dir expects an error, not an all-project hit.
- 2026-11-27 · deferred · `BriefField.Bytes` is read only by tests today. Owner: Task 2 implementer · next action: keep if MCP reports omitted sizes, otherwise remove · verify: no unused exported field after Task 2.
- 2026-11-27 · deferred to Task 3.2 · Brief latency grows linearly with total indexed `vault_fts` content (scan plan above; ~230 ms at 25.6 MB here). Next action: state this scale limit next to the "latest = indexed" caveat in the docs · verify: reference doc mentions it. Cold-cache, Linux and larger-vault timings remain unmeasured (unknown).

- [x] 1.1 Measure the encrypted read path on the 100,000-row fixture before settling the query. Record total indexed bytes and DB/WAL sizes, plan, new connection and repeated timings. Verify cancellation under a shorter caller deadline. Revisit the access path if it misses the two-second target reproducibly.
- [x] 1.2 Add the existing-file reader under `internal/vault`. Check key, reader version and layout. Resolve the literal prefix inside scope and select main-transcript positions and bodies in one snapshot. Cover absent and empty vaults, accepted old index, incompatible schema/marker, wrong key or damage, and a committed row still in WAL. Check that failures preserve archive contents and a missing path stays missing.
- [x] 1.3 Wire `vault brief <session-id>` under `newVaultCmd` and use the shared renderer. Verify actual stdout and errors for Claude and Codex, including opening/latest deduplication, a pending and a tool-only assistant, no indexed user and a sidecar with overlapping lines. Reject `--tui` and `--through-line`.
- [x] 1.4 Exercise a synthetic credential crossing a display cut in a raw imported project path, multiline text resembling a citation, escaped controls, multibyte text and a field above the 64 KiB read limit. Verify the 4,096-byte ceiling, including the final newline, and repeat the workload with the finished reader and renderer.

## Task 2. Request a scoped brief from MCP and from search

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [MCP brief](implementation.md#mcp-brief)

- [ ] 2.1 Register `capy_vault_brief` with its schema, read-only annotation and handler. Check argument types, required session, literal prefix handling, unknown `through_line`, disabled vault, deadline, output accounting and success/error flags. Compare the decoded text with CLI stdout for the same archive.
- [ ] 2.2 Route lookup through the existing vault project selectors. Test a unique in-scope prefix shared with out-of-scope sessions, a local three-way ambiguity, an out-of-scope miss, literal wildcard characters, rejected NUL selectors, a renamed project, all-project access and an independently archived child.
- [ ] 2.3 Update both keyword result paths through their common vault formatter. Follow the displayed latest-exchange instruction for a hit whose term occurs in a different turn, with default and explicit cross-project scope. Check title-only and split-window hits, a sidecar with the same line as the main transcript, an unusable ID preview and a knowledge control. With compact replies present, check the action fits in its full hit and is reachable via a repeat reference.

## Task 3. Verify archive changes and publish the interface documentation

- **Status:** pending
- **Depends on:** Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification](implementation.md#verification)

- [ ] 3.1 Hold a reader snapshot across a writer commit and check metadata and excerpts come from the same revision. A fresh call after a divergent replacement returns the new exchange. Repeat after rename, reassignment and remembered deletion. Verify the registered tool and focused CLI/MCP tests with FTS5 and synthetic keys. Run the applicable retrieval checks for the search-output change.
- [ ] 3.2 Document the command, tool, cutoff limitation, scope, stale-index hint and distinction from `vault show` and `vault resume`. Add the tool to the actual installed inventory and stdio registration checks. Update the architecture, `CapyToolNames` and `GenerateRoutingInstructions` in `internal/platform/routing.go` and its generated `.capy/AGENTS.md` together. Check the existing platform artifact-drift tests.
- [ ] 3.3 Review the implementation against the design and record the encrypted workload results and any remaining release issue before closing the feature.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3
```
