# Tasks: Upstream sync through context-mode 0dfbe8d

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Audit: [upstream-audit.md](upstream-audit.md)
> Status: pending
> Created: 2026-10-10
> Not Doing: CI/bundle/docs-only ports, unsupported platforms, hosted analytics/pricing, event/directive replay, new Codex hooks, new runtimes, transcript-based cwd guessing, schema/key changes, ranking rewrites, unconditional source echoes, OS sandboxing
> Deferred follow-ups: per-agent throttle identity (D1), atomic checked-file handoff (D2), colliding concurrent batch labels (D3); see audit.
> Size convention: tests/fixtures and mechanical schema/registration/generated-copy updates do not inflate the substantive-file size. Split any slice that grows to L before coding.

## Task 1: Enforce shell policies on every executable element

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 3, 6, 7, 10, 11, 12
- **Docs:** [Shell policy](implementation.md#1-shell-command-policy)

### Subtasks

- [ ] 1.1 Extend the scanner in `internal/security/split.go` for nested substitutions, real command separators and quote/escape/heredoc contexts → verify: table-driven positive and literal-data negative cases.
- [ ] 1.2 Use executable elements in both evaluators in `eval.go`, preserving deny precedence and existing ask behavior → verify: allowed prefixes cannot authorize later commands.
- [ ] 1.3 Add handler regressions for shell, batch and extracted non-shell commands → verify: security/hook/server package suites.

## Task 2: Admit execute-file paths consistently

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** Tasks 3, 6, 10, 11, 12
- **Docs:** [Path admission](implementation.md#2-execute-file-path-admission)

### Subtasks

- [ ] 2.1 Extend Read permission lookup and add canonical project-boundary/external-allow evaluation in `internal/security` → verify: traversal, symlink, deny-wins and explicit-exception fixtures.
- [ ] 2.2 Resolve the path once in server security handling and pass it to `handleExecuteFile`'s executor request → verify: process cwd differs from project without reading the wrong file.
- [ ] 2.3 Record D2 at the helper and test failure paths before child spawn → verify: security/server suites and accurate boundary documentation.

## Task 3: Contain guidance state filenames

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 1, 2, 6, 7, 10, 11, 12, 16
- **Docs:** [Guidance filenames](implementation.md#3-safe-guidance-filenames)

### Subtasks

- [ ] 3.1 Share bounded session-component mapping between guidance creation and reset in `internal/hook/guidance.go` → verify: normal IDs stay compatible; unsafe IDs remain stable and separate.
- [ ] 3.2 Add traversal/NUL/oversized-ID tests with a temporary project → verify: no filesystem effects outside `.capy` and reset targets the same state file.

## Task 4: Emit direct main-agent routing denials

- **Status:** pending
- **Depends on:** Task 1
- **Size:** S
- **Can run in parallel with:** Tasks 2, 3, 6, 7, 10, 11, 12, 16
- **Docs:** [Explicit redirects](implementation.md#4-explicit-main-agent-redirects)

### Subtasks

- [ ] 4.1 Use `FormatBlock` in `routeBash` for capy redirects → verify: deny JSON contains actionable guidance and does not approve an echo replacement.
- [ ] 4.2 Preserve safe downloads, security asks and Agent input edits → verify: hook and adapter tests.

## Task 5: Route subagents according to available tools

- **Status:** pending
- **Depends on:** Tasks 3, 4
- **Size:** M
- **Can run in parallel with:** Tasks 2, 6, 7, 10, 11, 12, 16
- **Docs:** [Subagent routing](implementation.md#5-subagent-aware-routing-and-discovery)

### Subtasks

- [ ] 5.1 Preserve child identity in adapter events and thread it through routing → verify: actual child, missing-ID and type-only main-agent cases.
- [ ] 5.2 Suppress only unavailable capy redirects; retain user security checks → verify: native child calls proceed while denied commands remain blocked.
- [ ] 5.3 Add bounded deferred-tool discovery/fallback wording to injected and generated routing → verify: Agent inputs survive, generated `.capy/AGENTS.md` matches its generator, and artifact tests pass.

## Task 6: Preserve batch heredocs and captured stderr

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 1, 2, 3, 4, 5, 7, 10, 11, 12
- **Docs:** [Batch streams](implementation.md#6-batch-heredocs-and-stderr)

### Subtasks

- [ ] 6.1 Execute original commands in both batch worker paths and combine captured streams afterward → verify: heredoc terminators and multiline commands run intact.
- [ ] 6.2 Cover partial/empty/stderr-only output and serial/parallel timeout semantics → verify: batch tests under `-race` and assertions against indexed content.

## Task 7: Use project cwd for every runtime

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 1, 3, 4, 5, 6, 10, 11, 12
- **Docs:** [Runtime cwd](implementation.md#7-project-cwd-for-every-runtime)

### Subtasks

- [ ] 7.1 Separate temporary scripts from execution cwd in `executor.go` → verify: project-relative fixture operations across installed runtimes.
- [ ] 7.2 Keep Rust compilation temporary and run its binary in the project; align direct execute-file path resolution → verify: compiled and file-processing fixtures when process cwd differs.
- [ ] 7.3 Check Go/Elixir project-dependent wrappers and document changed relative-write behavior → verify: executor package tests.

## Task 8: Expose per-call execution cwd

- **Status:** pending
- **Depends on:** Tasks 2, 6, 7
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 10, 11, 12, 15
- **Docs:** [Cwd overrides](implementation.md#8-per-call-cwd-overrides)

### Subtasks

- [ ] 8.1 Add request-local cwd normalization and executor handling → verify: relative/absolute/missing/non-directory cases without global `Chdir`.
- [ ] 8.2 Wire execute, execute-file and batch schema/handler inputs → verify: simultaneous different cwd requests remain isolated, including parallel batch workers.
- [ ] 8.3 Preserve selected DB/key/policy/vault identity and execute-file path anchoring → verify: executor/server tests under `-race`.

## Task 9: Bound detached-process output retention

- **Status:** pending
- **Depends on:** Task 8
- **Size:** S
- **Can run in parallel with:** Tasks 3, 5, 10, 11, 12, 15, 16
- **Docs:** [Background output](implementation.md#9-bounded-background-output)

### Subtasks

- [ ] 9.1 Add atomic snapshot/discard semantics to `safeBuffer` and use them at detach → verify: child progress continues with bounded retained bytes.
- [ ] 9.2 Let the eventual waiter clean temporary files and untrack the PID once → verify: detached exit, shutdown and timeout/exit race cases under `-race`.
- [ ] 9.3 Supersede ADR-005's accepted output accumulation while preserving its history → verify: documented pipe ownership and discard behavior match the implementation.

## Task 10: Treat knowledge source selectors literally

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 1, 6, 9, 11, 12, 14, 16
- **Docs:** [Literal filters](implementation.md#10-literal-source-filters)

### Subtasks

- [ ] 10.1 Escape LIKE metacharacters in `knowledgeFilterClauses` → verify: literal `%`, `_`, backslash and quoted-label fixtures.
- [ ] 10.2 Cover porter/trigram/fallback, exact matching and explicit ephemeral-source bypass → verify: store/retrieval suites.

## Task 11: Cap every plaintext chunk by bytes

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 1, 6, 10, 12, 15, 16
- **Docs:** [Chunk cap](implementation.md#11-plaintext-chunk-cap)

### Subtasks

- [ ] 11.1 Bound each plaintext strategy and title truncation safely in `chunk.go` → verify: boundary-size and multibyte tests retain content.
- [ ] 11.2 Guard the final plaintext fallback in `index.go` and assert persisted size/tail retrieval for oversized sections, single lines, whitespace and JSON fallback → verify: store/retrieval tests and quality comparison in Task 20.
- [ ] 11.3 Document existing same-hash chunk behavior and explicit remove/reindex repair → verify: no implicit data migration is introduced.

## Task 12: Share bounded file ingestion

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 1, 6, 10, 11, 15, 16
- **Docs:** [File ingestion](implementation.md#12-shared-bounded-file-ingestion)

### Subtasks

- [ ] 12.1 Extract filesystem ingestion into `internal/knowledge/index.go` and route MCP file indexing through it → verify: existing canonical labels, dedup, durable kind and stale refresh remain valid.
- [ ] 12.2 Bound actual reads and preserve deny/regular-file admission → verify: file-growth, FIFO, denied-file and error-outcome fixtures.

## Task 13: Index directories with explicit traversal limits

- **Status:** pending
- **Depends on:** Tasks 11, 12
- **Size:** M
- **Can run in parallel with:** Tasks 1, 3, 6, 7, 9, 10, 15
- **Docs:** [Directory ingestion](implementation.md#13-directory-ingestion-with-selection-controls)

### Subtasks

- [ ] 13.1 Implement deterministic traversal, per-file labels/outcomes and all hard bounds in `internal/knowledge` → verify: partial/capped/canceled scans report their actual extent.
- [ ] 13.2 Implement validated pattern/extension controls, canonical symlink containment and cycle detection → verify: deny rules and secret/VCS exclusions survive all options.
- [ ] 13.3 Apply Git's actual ignore evaluator with bounded NUL-separated calls → verify: nested/negated/tracked rules, unusual filenames, inherited Git env isolation and visible failures.
- [ ] 13.4 Add MCP directory dispatch and schema fields → verify: repeat indexing, searchable per-file sources, stale refresh and no implicit deletion.

## Task 14: Add terminal knowledge indexing

- **Status:** pending
- **Depends on:** Task 13
- **Size:** S
- **Can run in parallel with:** Tasks 1, 6, 9, 10, 16, 17, 18, 19
- **Docs:** [CLI index](implementation.md#14-cli-indexing)

### Subtasks

- [ ] 14.1 Add `cmd/capy/index.go` and register the command with shared ingestion options → verify: file/directory help and input validation.
- [ ] 14.2 Reuse project/key/store selection and documented partial-result exit codes → verify: encrypted CLI→MCP round trip, crossed credentials and immediate reopen after close.

## Task 15: Add terminal knowledge search

- **Status:** pending
- **Depends on:** Task 10
- **Size:** S
- **Can run in parallel with:** Tasks 1, 6, 8, 9, 11, 12, 13, 16, 17, 18, 19
- **Docs:** [CLI search](implementation.md#15-cli-knowledge-search)

### Subtasks

- [ ] 15.1 Add `cmd/capy/search.go` with bounded limit/source/type/kind flags and existing key resolution → verify: MCP→CLI retrieval and meaningful no-result/error exit codes.
- [ ] 15.2 Install stale-read denies and retain knowledge-only semantics → verify: denied backing files, ephemeral filtering, literal sources and no accidental vault federation.
- [ ] 15.3 Amend ADR-012 for CLI-only content-type selection → verify: the MCP schema still omits that filter.

## Task 16: Add per-call fetch freshness

- **Status:** pending
- **Depends on:** Task 8
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 9, 10, 11, 12, 14, 15
- **Docs:** [Fetch TTL](implementation.md#16-fetch-ttl-and-argument-normalization)

### Subtasks

- [ ] 16.1 Parse strict TTL and literal boolean forms in `coerce.go` → verify: omitted/zero/invalid/overflow cases fail or behave as specified before I/O.
- [ ] 16.2 Share freshness policy across single/batch fetch and expose `ttl` in `tools.go` → verify: cache-key/kind behavior, force precedence and mixed batches.
- [ ] 16.3 Display effective freshness separately from retention → verify: fetch/SSRF/size-limit tests under `-race`.

## Task 17: Report cache hits and attempted misses

- **Status:** pending
- **Depends on:** Task 16
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 9, 10, 11, 12, 14, 15, 19
- **Docs:** [Cache statistics](implementation.md#17-cache-outcome-statistics)

### Subtasks

- [ ] 17.1 Count per-URL outcomes consistently in fetch/stats paths and include them in snapshot/reset → verify: concurrent batches and purge-all counters.
- [ ] 17.2 Render zero-safe hit rate and explicit misses in `tool_stats.go` → verify: all-miss, all-hit, forced, failed-network and rejected-URL fixtures.

## Task 18: Add batch query scope and bounded provenance

- **Status:** pending
- **Depends on:** Tasks 6, 8, 10, 11, 16
- **Size:** S
- **Can run in parallel with:** Tasks 3, 5, 9, 12, 14, 15, 17
- **Docs:** [Batch controls](implementation.md#18-batch-search-controls-and-provenance)

### Subtasks

- [ ] 18.1 Make queries optional and implement validated batch/global scope → verify: no-query indexing and durable+ephemeral knowledge-only global results.
- [ ] 18.2 Return scope/source plus sanitized capped command inventory → verify: huge/secret-bearing/Unicode/Markdown inputs cannot flood or corrupt the inventory.
- [ ] 18.3 Preserve raw/output accounting and execution semantics; record D3 at the existing label builder → verify: batch/federation suites and final benchmark comparison.

## Task 19: Make the shared search budget visible

- **Status:** pending
- **Depends on:** Task 18
- **Size:** S
- **Can run in parallel with:** Tasks 3, 5, 9, 12, 14, 15, 17
- **Docs:** [Throttle visibility](implementation.md#19-search-throttle-visibility)

### Subtasks

- [ ] 19.1 Return coherent count/limit/reset information for every valid search path → verify: first/soft/hard/reset boundaries, empty store and partial failures with a clock seam.
- [ ] 19.2 State shared-server scope and record D1 beside `searchThrottle` → verify: no claim of per-agent isolation and invalid calls do not consume budget.

## Task 20: Verify the complete sync and document compatibility

- **Status:** pending
- **Depends on:** Tasks 1–19
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Final verification](implementation.md#20-final-verification-and-documentation)

### Subtasks

- [ ] 20.1 Run `kk:test`, full FTS5/race suites, default build and glamour TUI subset → verify: record commands/results in `verification.md`.
- [ ] 20.2 Run real encrypted CLI/MCP round trips and generated whole-file/merged-artifact checks → verify: same project/key/content and close/reopen behavior.
- [ ] 20.3 Run quality/performance benchmarks against `c0bfff2` and compare results → verify: explain regressions rather than weakening assertions.
- [ ] 20.4 Run `kk:document` and update behavior/parameter/CLI documentation → verify: generators and committed copies stay synchronized.
- [ ] 20.5 Run `kk:review-code` with Go input, then `kk:review-spec` → verify: resolve findings or record actionable deferrals; retain D1/D2/D3 until actually addressed.

## Dependency Graph

```text
Task 1                  --> Task 2
Task 1                  --> Task 4
Tasks 3, 4              --> Task 5
Tasks 2, 6, 7           --> Task 8 --> Task 9
Task 8                  --> Task 16 --> Task 17
Tasks 11, 12            --> Task 13 --> Task 14
Task 10                 --> Task 15
Tasks 6, 8, 10, 11, 16  --> Task 18 --> Task 19
Tasks 1-19              --> Task 20
```

Parallel markers indicate possible independent work after dependencies are met, not permission to overwrite another contributor's shared file. Coordinate mechanically shared schema/registration edits.
