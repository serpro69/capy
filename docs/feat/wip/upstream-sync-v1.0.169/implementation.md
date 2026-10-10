# Implementation: Upstream sync through context-mode 0dfbe8d

> Status: pending
> Design: [design.md](design.md)
> Provenance and exclusions: [upstream-audit.md](upstream-audit.md)
> Execution checklist: [tasks.md](tasks.md)

## Contributor orientation

Read repository `AGENTS.md`, [architecture](../../../architecture.md), the [previous sync](../../done/upstream-sync-v1.0.136/design.md), and the current feature design first. The former sync's references to retrieval inside `internal/store/search.go` are historical: ranking now lives in `internal/retrieval`. Vault data must not be inserted into knowledge tables.

`internal/server/tools.go` owns descriptors, not `server.go`. The server is long-lived and shared across requests; hooks are short-lived processes. Do not introduce global cwd, mutable per-request policy or a process-local-only hook throttle. `internal/platform/setup.go` and `routing.go` generate committed artifacts, so changes there need their matching generated copies.

All test commands below require `CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`, with `CGO_ENABLED=1` and `-tags fts5`. CLI and hook tests must use temporary homes/settings, not the developer's real configuration. New test names below describe intended coverage; they are not claims that those tests already exist.

No dependency upgrade is planned. Git ignore calls use the existing executable; the API was verified against the installed Git 2.34.1 manual, as recorded in the audit. If implementation needs a new parser/library instead, run `kk:dependency-handling` and revise the decision before writing calls.

## 1. Shell command policy

**Owners:** `internal/security/split.go`, `eval.go`, their tests; handler regression cases in `internal/hook/hook_test.go` and server execute/batch tests where those files already own coverage.

1. Introduce a shared bounded scanner that emits executable elements while respecting escapes, quotes, substitutions, redirections and heredoc bodies → verify: table cases for newline/background chains, double-quoted substitutions, literal quotes, escaped pipes, even/odd backslashes, nested substitutions and arithmetic.
2. Apply element-wise deny and full-policy evaluation without changing settings precedence or deny-only defaults → verify: an allowed prefix cannot authorize a denied/unknown later executable element; literal data containing a denied command remains allowed.
3. Exercise the same scanner through shell execute, batch and non-shell extracted command checks → verify: `go test -tags fts5 -count=1 ./internal/security/... ./internal/hook/... ./internal/server/...`.

Do not add a full shell interpreter. Detect unsupported/excessive scanner state explicitly; do not quietly ignore a nesting overflow. Preserve unmatched-ask hook behavior described in design §3.1.

## 2. Execute-file path admission

**Owners:** `internal/security/eval.go`, `settings.go`, `internal/server/security_check.go`, `server.go`, `tool_execute_file.go`; corresponding tests.

1. Generalize permission-pattern reading to select Read allow or deny lists, keeping `ReadToolDenyPatterns` as a compatible entry point → verify: local/shared/global settings fixtures and literal false/missing patterns preserve existing deny behavior.
2. Add a checked absolute-path resolver with lexical and physical containment and explicit external-target allows; wire cached allows alongside server denies → verify: absolute outside paths, traversal, sibling-prefix paths, direct symlinks, symlink-then-`..`, denied-but-allowed targets, and valid external exceptions.
3. Pass the admitted absolute path into `ExecuteFile` → verify: a server project different from process cwd reads the expected fixture and rejects a denied path before any child is spawned.
4. Document the remaining check/open race at the helper and link [D2](upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads) → verify: documentation and error text do not claim an OS sandbox.

Run `go test -tags fts5 -count=1 ./internal/security/... ./internal/server/...`. Existing explicit external-path tests should use intentional allow fixtures; do not just remove them or weaken the assertions.

## 3. Safe guidance filenames

**Owners:** `internal/hook/guidance.go`, guidance tests.

Use a single bounded filename-component function from both `guidanceOnce` and `ResetGuidanceFile`. Preserve safe IDs; digest unsafe/oversized ones without exposing their contents → verify: repeated invocations share one file, two different unsafe IDs remain separate, reset removes the same file, and traversal/NUL/long IDs create nothing outside the temporary `.capy` directory. Run the hook package tests.

## 4. Explicit main-agent redirects

**Owners:** `internal/hook/pretooluse.go`, hook routing tests; adapter tests only if formatter coverage needs strengthening.

Replace echo-command rewrites for curl/wget and inline HTTP with direct `FormatBlock` calls. Preserve safe-download exceptions, actual security asks/denies and Agent input modifications → verify: inspect the returned JSON decision and reason, and prove the original command is not approved as part of the redirect. Run `go test -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...`.

The rationale is reliable expression of a block, not an unverified assertion that every current Claude Code build ignores `updatedInput`.

## 5. Subagent-aware routing and discovery

**Owners:** `internal/adapter/adapter.go`, `claudecode.go`, `internal/hook/pretooluse.go`, `routing.go`; `internal/platform/routing.go` and `.capy/AGENTS.md` where shared instructions need the same availability rule.

1. Preserve `agent_id`/`agent_type` in parsed hook context → verify: actual child ID, missing fields and type-only main-agent fixtures.
2. Thread child context into routing, bypassing only capy redirects/nudges when tool availability is unverified → verify: child native WebFetch/HTTP can proceed, while an explicit denied shell command remains denied and a matched ask remains an ask.
3. Add one-attempt deferred-tool discovery guidance with native-tool fallback; preserve original Agent/Task fields → verify: no recursive bootstrap, no bootstrap mandate without discovery support, and Bash-to-general-purpose behavior remains covered.
4. Align generated routing text and its committed copy → verify: `go test -tags fts5 ./internal/platform -run 'TestGeneratedWholeFileArtifacts|TestMergedArtifactsAreIdempotent'` plus hook/adapter tests.

A parent MCP server's existence is not evidence that a fixed-tool child can call it. Do not introduce a new ready sentinel or Codex hook adapter.

## 6. Batch heredocs and stderr

**Owners:** `internal/server/tool_batch.go`, `tool_batch_test.go`.

Remove suffix redirection from both worker paths and share deterministic captured-stream formatting → verify: terminal heredocs with/without trailing newline, quoted delimiters, stdout-only, stderr-only, combined output and empty streams work at concurrency 1 and greater than 1. Preserve serial skipped commands and parallel timeout/error isolation. Run the server batch subset with `-race`; inspect indexed content, not just a successful exit.

## 7. Project cwd for every runtime

**Owners:** `internal/executor/executor.go`, `wrap.go`, executor tests.

1. Run scripts from the executor's project directory while keeping absolute script paths in per-call temporary directories → verify: relative fixture reads/writes land in the project for shell and available non-shell runtimes.
2. Run compiled Rust from the project but compile in its temporary workspace; keep output binary paths absolute → verify: a compiled fixture finds a project-relative marker.
3. Resolve direct executor `ExecuteFile` relative paths against its project rather than process cwd → verify: use different process/project directories without calling `os.Chdir` from concurrent tests.

Run executor tests and `make bench-quality` as part of the final comparison. Check Go module behavior and Elixir project detection; do not patch failures by reverting non-shell cwd piecemeal.

## 8. Per-call cwd overrides

**Owners:** executor request/cwd resolution, `internal/server/tool_execute.go`, shared server argument resolution; mechanical forwarding in `tool_execute_file.go`, `tool_batch.go`, and schemas in `tools.go`.

Add one optional cwd field to the execution request and one server-side normalization path. Forward the effective directory into ordinary execution, execute-file execution, both batch workers and language wrappers. Keep execute-file's input path anchored independently to the server project → verify: absolute and relative cwd, missing/non-directory values, two concurrent requests targeting different directories, and no cross-request mutation. Confirm index/search and credential selection still use the original project. Run focused executor/server tests under `-race`.

The implementation slice's substantive work is request normalization and executor semantics; parameter/schema forwarding is mechanical. If runtime-specific work makes it large, split by complete runtime paths before implementation rather than scheduling an L-sized task.

## 9. Bounded background output

**Owners:** `internal/executor/safebuf.go`, `executor.go`, related tests.

1. Add a synchronized snapshot-and-discard transition to the capture buffer; later writes return success without appending → verify: concurrent writers cannot retain bytes after the transition and the snapshot remains stable.
2. Use it when returning a background result while keeping `cmd.Wait` pipe readers alive → verify: a child continues writing/progressing after timeout, no pipe failure occurs, and retained bytes stay bounded.
3. Transfer temporary cleanup to the eventual waiter and untrack the PID exactly once → verify: normal detached exit and `CleanupBackgrounded` termination remove artifacts, without killing a reused PID or waiting twice.
4. Record the replacement of ADR-005's accepted accumulation with a discard-capable writer, retaining the original rationale as history → verify: the ADR and new executor behavior agree without claiming pipe descriptors were replaced.

Run `go test -race -tags fts5 -count=1 ./internal/executor/...`. Foreground hard-cap/timeout tests remain required. Use synchronization/files/pipes for assertions rather than long sleeps.

## 10. Literal source filters

**Owners:** `internal/store/search.go`, `search_test.go`.

Bind a literal LIKE-escaped substring and declare the SQL escape character in `knowledgeFilterClauses` → verify: `%`, `_`, backslashes and quoted labels match themselves, normal partial labels still match, exact mode is unchanged, and both porter/trigram/fuzzy paths obey the filter. Include explicit ephemeral-source queries to protect kind bypass. Run store and retrieval tests; do not change vault project filters or the shared ranking engine.

## 11. Plaintext chunk cap

**Owners:** `internal/store/chunk.go`, the plaintext fallback in `index.go`, chunk/index tests and benchmark fixtures where a missing case warrants one.

Add a reusable plaintext byte-bound splitter and apply it after each existing chunk strategy → verify: 4,096/4,097-byte boundaries, 4,097–5,000-byte blank sections, fewer-than-20 huge lines, long single lines, multibyte characters at boundaries, long titles, empty/whitespace-only input, and fallback from malformed JSON. Ensure the final empty-chunk fallback cannot resurrect an oversized plaintext chunk. Assert maximum persisted content bytes in both FTS tables and retrieval of markers near the end of oversized input.

Keep the store's source-size bound and sanitization-before-hash flow. Test overlap/content retention, not just chunk count. Existing same-hash data is intentionally not re-chunked automatically; document explicit remove/reindex instructions. Run `go test -tags fts5 ./internal/store/... ./internal/retrieval/...` and quality/performance comparisons.

## 12. Shared, bounded file ingestion

**Owners:** new `internal/knowledge/index.go`, `internal/server/tool_index.go`, ingestion/server tests.

Extract the single-file operation behind a filesystem-ingestion API that receives project, policy and store dependencies. Preserve inline-content handling in the server. Keep canonical labels, durable kind, file-backed stale refresh and deny-before-content-read behavior → verify: the existing MCP file suite remains valid through the new owner.

Bound the actual descriptor read as well as checking stat size, reject non-regular files before reading, and expose an explicit per-file outcome suitable for directory aggregation → verify: growing files, FIFOs/devices, denied files, same-content dedup and failed reads. If a dedup indicator is added to `IndexResult`, it is response metadata only, not a schema change. Run knowledge/store/server tests.

This slice delivers the existing file path with a stricter read bound; directory support is the next complete path.

## 13. Directory ingestion with selection controls

**Owners:** new `internal/knowledge/directory.go`, `patterns.go`, `gitignore.go`; MCP path dispatch in `tool_index.go` and schema in `tools.go`.

1. Implement deterministic bounded traversal with the design's default/hard limits, incremental directory reads, exclusions, source naming and per-file outcomes → verify: depth 0, file/entry/byte ceilings, an oversized flat directory, cancellation and bounded failure summaries.
2. Implement and document include/exclude/extension selection and optional root-contained symlink following → verify: cycles, aliases, dangling/escaping links, root symlink denial, hidden metadata and direct-file versus recursive admission.
3. In a Git worktree, use `git check-ignore --no-index --stdin -z` with bounded input/output and request cancellation; run with the intended working directory and without inherited Git directory/index/worktree overrides → verify: nested ignores, negation, tracked-but-ignored files, spaces/newlines in names, and wrong inherited repository variables. Treat exits 0/1 as normal; surface other failures. Outside Git, visibly report that ignore evaluation was unavailable by context; in a Git tree with missing/broken Git, fail instead of proceeding unfiltered.
4. Expose directory controls and aggregate results through `capy_index` → verify: repeated ingestion deduplicates, every file remains searchable by label and auto-refreshable, and failures do not erase prior sources.

The directory API is one bounded ingestion path, not a repository crawler/index-all feature. Keep the implementation reviewable; split selection/ignore additions into another complete user-facing slice if the five substantive files exceed M scope. Do not weaken defaults temporarily to make a partial task appear complete.

## 14. CLI indexing

**Owners:** new `cmd/capy/index.go`, command registration in `main.go`, CLI integration tests; reuse `internal/knowledge`.

Add the file/directory command and translate CLI flags into the same ingestion options. Reuse `--project-dir`, `loadKnowledgeTarget`, key resolution and `newKnowledgeStore` → verify: CLI-indexed markers are returned by an MCP server using the same target, and an explicit external input does not choose another database.

Report partial/failure/cap outcomes with the documented exit semantics. Always close the store and surface its close error where command conventions permit → verify: immediate encrypted reopen and retained content, missing/invalid selected keys without fallback or unintended files, and crossed environment/project credentials. Run the `cmd/capy` integration subset with synthetic keys and isolated homes.

## 15. CLI knowledge search

**Owners:** new `cmd/capy/search.go`, `main.go`, CLI tests; reuse `knowledge.go`.

Expose knowledge query, source, type, bounded limit and kind flags; wire the same Read-deny stale-refresh callback used by MCP. Output bounded snippets with source/title and no secrets → verify: MCP-indexed data is found by CLI, literal wildcard labels stay scoped, denied backing files are not reread, ephemeral content is opt-in except explicit source bypass, and vault-only content is not silently included.

Validate arguments before store access; empty results exit successfully and key/DB failures do not → verify: actual command exit codes and help output. Run CLI and store tests. The task depends on the source-filter fix, not on introducing a server instance into the command.

Amend ADR-012 to name the terminal filtering use case while keeping the MCP content-type field unexposed → verify: CLI help and actual MCP descriptors match that distinction.

## 16. Fetch TTL and argument normalization

**Owners:** `internal/server/coerce.go`, `tool_fetch.go`, `tools.go`, fetch/coercion tests.

1. Parse optional TTL and force using the design's accepted forms and overflow checks → verify: omitted versus zero TTL, integer strings, fractional/negative/overflow/null TTL, and literal `false` versus malformed booleans before network activity.
2. Share freshness policy across single and batch cache paths → verify: configured fallback, short TTL refresh, long TTL hit, force precedence, requested-kind mismatch, composite URL keys and mixed batch cache outcomes using a fake clock or controlled timestamps.
3. Display effective freshness separately from lifecycle TTL → verify: no message promises a durable cache merely because a large TTL was supplied, and SSRF/size/concurrency tests still pass.

Run the server fetch subset with `-race`; do not add new config precedence or per-request persistent TTL columns.

## 17. Cache outcome statistics

**Owners:** `internal/server/stats.go`, `tool_fetch.go`, `tool_stats.go`, stats/fetch tests.

Count one hit or attempted miss per eligible URL, including bypasses and failed network attempts; update snapshot and reset under the existing mutex → verify: mixed concurrent batches produce exact counts, rejected URLs do not inflate attempts, and purge-all resets the new counters while preserving uptime.

Render hit/miss counts and a zero-safe rate even when all attempts missed. Preserve estimated savings and existing source/chunk statistics; no last-index timestamp or analytics migration is needed for this contract → verify: focused stats/fetch tests plus `go test -race -tags fts5 ./internal/server/...`.

## 18. Batch search controls and provenance

**Owners:** `internal/server/tool_batch.go`, `tools.go`, batch tests.

1. Make queries optional and validate supplied queries and `query_scope` before command execution → verify: no-query calls still index; malformed arrays/scopes produce no child side effects rather than becoming indexing-only calls.
2. Default to exact batch source; make global scope explicitly durable+ephemeral knowledge-only → verify: unrelated durable/ephemeral fixtures appear only globally and vault sessions are not queried.
3. Add source/scope information and a sanitized command inventory within per-command and aggregate byte budgets → verify: secret-bearing commands, huge heredocs, Markdown delimiters and UTF-8; complete commands still execute, raw-output stats exclude echoed source, and final response accounting includes the inventory.

Preserve serial/parallel timeout semantics from Task 6. Existing label-based batch dedup remains; do not claim isolation for simultaneous batches using the same truncated label (follow-up in design risk notes). Run batch and federation suites, then the quality benchmarks.

## 19. Search throttle visibility

**Owners:** `internal/server/server.go`, `tool_search.go`, `tools.go` description, search tests.

Return a coherent throttle snapshot from the existing atomic advance operation and format one notice on every admitted request path. Keep invalid-input paths outside accounting → verify: calls 1, 3, 4, 8, 9 and post-reset behavior, empty store, partial vault failure and concurrent calls. Prefer a clock seam to minute-long sleeps.

The notice states the actual effective limit and shared-server scope. Add an actionable code comment referencing [D1](upstream-audit.md#d1-per-agent-search-budgets) beside `searchThrottle`; do not add a per-agent map without verified request identity. Run server search/federation/vault-search tests under `-race` and retain current source-kind behavior.

## 20. Final verification and documentation

Depends on every implementation slice.

1. Run `kk:test`: `make test`, `make test-race`, `make build`, and `go test -tags fts5,glamour ./internal/vault/tui/...` to protect the shared retrieval consumers → verify: save commands, outcomes and any environmental limitations in `verification.md`.
2. Run the real CLI/MCP round trips using `cmd/capy/mcp_stdio_helpers_test.go` conventions → verify: fresh and existing encrypted stores, same target across interfaces, correct project selection, shutdown checkpoint and unchanged vault ownership.
3. Run `make bench` (which includes `bench-quality` and `bench-perf`) against the pre-change capy baseline `c0bfff2` and the implementation branch; compare with `make bench-compare BASE=<baseline-label> TARGET=<branch-label>` → verify: explain metric deltas, inspect retrieval rank/recall failures and do not trade correctness assertions for passing numbers. A detached baseline writes `HEAD.json`/`HEAD.txt`; rename the artifacts to the baseline label before comparing. Keep baseline production isolated from ongoing work.
4. Run `kk:document` to update README/tool/CLI usage, relevant architecture sections and compatibility notes → verify: new parameters, source kinds, freshness versus retention, directory bounds and intentional behavior changes agree across docs and actual schemas.
5. Run generated artifact checks and `git diff --check` → verify: each changed generator matches its committed counterpart and no unrelated generated file is included.
6. Run `kk:review-code` with Go input, then `kk:review-spec` against all feature artifacts → verify: fix actionable findings or record each unresolved item durably with a reason and next step. Mark implementation tasks done only with evidence.

Do not remove deferred D1/D2/D3 merely because the suite passes. The design review recommended after drafting is a separate gate; author checks do not count as independent review.

## Verification status at design time

Only repository/upstream source inspection, contract lookup and documentation consistency checks are in scope for this design task. No Go runtime code has been changed or behaviorally verified. The commands above are the implementation acceptance plan, not a record of completed tests.
