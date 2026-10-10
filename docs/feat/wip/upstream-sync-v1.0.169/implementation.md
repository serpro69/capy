# Implementation: Upstream sync through context-mode 0dfbe8d

> Status: pending
> Design: [design.md](design.md)
> Provenance and exclusions: [upstream-audit.md](upstream-audit.md)
> Execution checklist: [tasks.md](tasks.md)
> Review reconciliation: [.reviews/reconciliation-2026-10-10.md](.reviews/reconciliation-2026-10-10.md)

## Contributor orientation

Read repository `AGENTS.md`, [architecture](../../../architecture.md), the [previous sync](../../done/upstream-sync-v1.0.136/design.md), and the current feature design first. The former sync's references to retrieval inside `internal/store/search.go` are historical: ranking now lives in `internal/retrieval`. Vault data must not be inserted into knowledge tables.

`internal/server/tools.go` owns descriptors, not `server.go`. The server is long-lived and shared across requests; hooks are short-lived processes. Do not introduce global cwd, mutable per-request policy or a process-local-only hook throttle. `internal/platform/setup.go` and `routing.go` generate committed artifacts, so changes there need their matching generated copies.

All test commands below require `CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`, with `CGO_ENABLED=1` and `-tags fts5`. CLI and hook tests must use temporary homes/settings, not the developer's real configuration. New test names below describe intended coverage; they are not claims that those tests already exist.

No dependency upgrade is planned. Git ignore calls use the existing executable; the API was verified against the installed Git 2.34.1 manual, as recorded in the audit. If implementation needs a new parser/library instead, run `kk:dependency-handling` and revise the decision before writing calls.

## 1. Shell command policy

**Owners:** `internal/security/split.go`, `eval.go`, their tests; handler regression cases in `internal/hook/hook_test.go` and server execute/batch tests where those files already own coverage.

1. Introduce a shared bounded scanner with the input/frame/element/visit limits from design §3.1 → verify: newline/background chains, double-quoted substitutions, quoted heredoc literals, substitutions within unquoted heredoc data, escaped pipes, even/odd backslashes, nested substitutions and arithmetic.
2. Apply element-wise deny and full-policy evaluation without changing settings precedence or deny-only defaults → verify: an allowed prefix cannot authorize a denied/unknown later executable element; literal data containing a denied command remains allowed.
3. Exercise the same scanner through shell execute, batch and non-shell extracted command checks → verify: `go test -tags fts5 -count=1 ./internal/security/... ./internal/hook/... ./internal/server/...`.

Do not add a full shell interpreter. Propagate a typed limit/evaluation error through both evaluators, hook routing and non-shell extraction checks; every affected handler blocks before execution rather than using a partial list. Test each limit immediately below/above its boundary in the hook, MCP execute and batch paths. Preserve unmatched-ask hook behavior described in design §3.1.

## 2a. Prepared Read policy on existing paths

**Owners:** `internal/security/settings.go`, `eval.go`, new `file_policy.go`; existing server/hook policy wiring and stale-read callback.

Retain rule origin and explicit anchor/cwd/home context when loading local/shared/user settings. Prepare the supported grammar and bare-tool rule; preserve a legacy absolute interpretation for single-slash **denies only**, with a diagnostic. New allows use host anchors only → verify: `//`, `~/`, source-relative `/`, cwd-relative rules, escaped literals, unsupported syntax and differing user/project settings origins.

Use one prepared object in existing MCP direct reads and the store's deny callback before adding new ingestion/CLI paths → verify: the same relative deny blocks raw-relative, absolute and physical paths, including stale refresh, and no local symlink alias grants an external target. Invalid policy preparation blocks file admission visibly. Test this end-to-end, not only the legacy exported matcher. Keep settings origin distinct from execution cwd. Run security/server/stale tests; coordinate shared files with Task 1 without making its shell parser a dependency.

## 2. Execute-file path admission

**Owners:** `internal/security/eval.go`, `settings.go`, `internal/server/security_check.go`, `server.go`, `tool_execute_file.go`; corresponding tests.

1. Consume Task 2a's prepared policy rather than raw glob arrays → verify: native absolute/home grants and legacy deny compatibility on local/shared/user fixtures.
2. Add a checked absolute-path resolver with lexical and physical containment and explicit external grants covering requested and real paths → verify: absolute outside paths, traversal, sibling-prefix paths, direct symlinks, symlink-then-`..`, denied-but-allowed targets, and valid external exceptions.
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

**Owners:** adapter event parsing, `cmd/capy/hook.go` and hook context selection, `internal/hook/pretooluse.go`/`routing.go`; generated routing owner/copy where wording changes.

1. Preserve `agent_id`/`agent_type`/`cwd` and resolve project identity before policy loading using design §3.4 precedence → verify: actual child, type-only main agent, environment missing, explicit project override, payload cwd outside process cwd and nested project directories. Prepared rule origin/state storage must use the selected project consistently.
2. Treat child availability as unknown unless demonstrated by Task 5a; keep advisory discovery and native-tool fallback → verify: unknown/fixed-tool children do not get trapped, while explicit denies and matched asks still run first. Do not label every child unavailable or use two local frontmatter files as the effective host tool pool.
3. Add one-attempt deferred-tool discovery guidance with native-tool fallback; preserve original Agent/Task fields → verify: no recursive bootstrap, no bootstrap mandate without discovery support, and Bash-to-general-purpose behavior remains covered.
4. Align generated routing text and its committed copy → verify: `go test -tags fts5 ./internal/platform -run 'TestGeneratedWholeFileArtifacts|TestMergedArtifactsAreIdempotent'` plus hook/adapter tests.

A parent MCP server's existence is not evidence that a fixed-tool child can call it. Do not introduce a new ready sentinel or Codex hook adapter.

## 5a. Re-enforce redirects after observed child capabilities

**Owners:** `internal/hook/posttooluse.go`, `pretooluse.go`, `guidance.go` and dispatch wiring.

Record the exact capy tool from a child PostToolUse event in bounded session/agent-scoped state; reuse safe filename components and ensure parallel updates do not overwrite sibling capabilities → verify: one child's execute capability cannot authorize another child's redirect, and search alone is not proof of execute/fetch availability.

Select an observed suitable redirect alternative; absent/corrupt state stays unknown and advisory → verify: inherit-all child first calls capy execute, then a native flood is redirected; fixed-tool and unseen children retain fallback; security checks still dominate. Document the first-call limitation and D5. Update routing generator/copy if the selected alternative's guidance changes.

## 6. Batch heredocs and stderr

**Owners:** `internal/server/tool_batch.go`, `tool_batch_test.go`.

Remove suffix redirection from both worker paths and share deterministic captured-stream formatting → verify: terminal heredocs with/without trailing newline, quoted delimiters, stdout-only, stderr-only, combined output and empty streams work at concurrency 1 and greater than 1. Preserve serial skipped commands and parallel timeout/error isolation. Run the server batch subset with `-race`; inspect indexed content, not just a successful exit.

## 6a. Consistent boolean request validation

**Owners:** `internal/server/coerce.go` and the boolean-consuming tool boundaries in execute/fetch/cleanup/search/vault-search.

Implement the presence-aware literal parser once; preserve each omitted default and return explicit errors for unsupported supplied types. Wire background and force, then every cleanup boolean and both all-projects selectors → verify: native/string true and false, whitespace/case, null/numeric/collection/invalid values, default dry run, no child spawned on invalid background, no purge/optimization on invalid cleanup input, and no accidental cross-project search.

The SDK's current `GetBool` already accepts string booleans and numeric truthiness, unlike capy's direct assertions. Replacing it deliberately narrows numeric all-projects inputs; do not write a test claiming its old string-true behavior was false. Validate real stdio calls as well as handlers. Task 13 reuses the helper for its new directory booleans; mechanical boundary forwarding does not create a second parser.

## 7. Project cwd for every runtime

**Owners:** `internal/executor/executor.go`, `wrap.go`, executor tests.

1. Run scripts from the executor's project directory while keeping absolute script paths in per-call temporary directories → verify: relative fixture reads/writes land in the project for shell and available non-shell runtimes.
2. Run compiled Rust from the project but compile in its temporary workspace; keep output binary paths absolute → verify: a compiled fixture finds a project-relative marker.
3. Resolve direct executor `ExecuteFile` relative paths against its project rather than process cwd → verify: use different process/project directories without calling `os.Chdir` from concurrent tests.

Run executor tests and the final benchmarks. Add isolated Go fixtures for local imports, `replace`, module/toolchain directives and automatic vendor selection; preserve `GOFLAGS` removal by `BuildSafeEnv`. Check Elixir project detection; do not patch failures by reverting non-shell cwd piecemeal.

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

**Owners:** new `internal/knowledge/index.go`, `internal/server/tool_index.go`, `internal/store/search.go:fileChangedSince`; optional dependency-free bounded-reader helper.

Extract the single-file operation behind a filesystem-ingestion API that receives project, policy and store dependencies. Preserve inline-content handling in the server. Keep canonical labels, durable kind, file-backed stale refresh and deny-before-content-read behavior → verify: the existing MCP file suite remains valid through the new owner.

Bound the actual descriptor read as well as stat size in **both initial ingestion and stale refresh**, reject non-regular files before reading, and expose a per-file outcome for aggregation. Reuse Task 2a's prepared policy → verify: deterministic file growth after stat via a read seam, FIFOs/devices, relative denies passed absolute paths, cached content retained with a logged skip, same-content dedup and read failures. Reuse existing `IndexResult.AlreadyIndexed` rather than inventing a second dedup indicator. Run knowledge/store/server tests.

This slice delivers the existing file path with a stricter read bound; directory support is the next complete path.

## 12a. Persistently schedule bounded stale refresh

**Owners:** store source schema/migration/statements, `search.go` refresh loop and scheduling state transfer in `index.go`.

Add `file_check_seq` plus its partial scheduling index after column admission. Page at most 32 oldest-checked sources, close metadata rows before writes, and advance only attempted rows with a logical sequence derived from the indexed current maximum. Ensure replacement rows retain the completed check's position → verify: migration/reopen, query plans using the scheduling index, no full-source snapshot, and repeated CLI processes advancing beyond the first page.

Apply the per-pass byte/admission-time limits from design §5.5 using Task 12's bounded reads → verify: 10,000 sources, changed/unchanged/missing/denied files, budget exhaustion leaving unprocessed rows eligible, cancellation, concurrent bounded duplicate checks, and eventual progress in a quiescent corpus. Record raw latency/allocation/read/write counts for final baseline comparison. This changes freshness timing, not retrieval ranking or retention timestamps.

## 13. Directory ingestion with selection controls

**Owners:** new `internal/knowledge/directory.go`, `patterns.go`, `gitignore.go`; MCP path dispatch in `tool_index.go` and schema in `tools.go`.

1. Implement deterministic bounded traversal with the design's default/hard limits, incremental directory reads, exclusions, colon-separated source prefixes and per-file outcomes → verify: depth 0, file/entry/byte ceilings, an oversized flat directory, cancellation, same/different-prefix dedup and bounded failure summaries.
2. Implement and document include/exclude/extension selection and optional root-contained symlink following → verify: cycles, aliases, dangling/escaping links, root symlink denial, hidden metadata and direct-file versus recursive admission.
3. Discover the worktree containing the canonical ingestion root and use `git check-ignore --no-index --stdin -z` with bounded input/output and request cancellation, without inherited Git directory/index/worktree overrides → verify: external root in another repo with conflicting ignores, nested/negated/tracked rules, unusual filenames and wrong inherited Git variables. Keep selected-project policies/credentials unchanged. Treat exits 0/1 as normal; surface other failures. Outside Git report no Git ignores; missing/broken Git in a repo fails visibly.
4. Expose directory controls using Task 6a's boolean parser and Task 2a's prepared policy → verify: direct/walked/refreshed absolute paths obey the same relative denies, repeated labels deduplicate, and Task 12a bounds searches after many repeated ingestion calls.

The directory API is one bounded ingestion path, not a repository crawler/index-all feature. Keep the implementation reviewable; split selection/ignore additions into another complete user-facing slice if the five substantive files exceed M scope. Do not weaken defaults temporarily to make a partial task appear complete.

## 14. CLI indexing

**Owners:** new `cmd/capy/index.go`, command registration in `main.go`, CLI integration tests; reuse `internal/knowledge`.

Add the file/directory command and translate CLI flags into the same ingestion options. Reuse `--project-dir`, `loadKnowledgeTarget`, key resolution and `newKnowledgeStore` → verify: CLI-indexed markers are returned by an MCP server using the same target, and an explicit external input does not choose another database.

Report partial/failure/cap outcomes with the documented exit semantics. Always close the store and surface its close error where command conventions permit → verify: immediate encrypted reopen and retained content, missing/invalid selected keys without fallback or unintended files, and crossed environment/project credentials. Run the `cmd/capy` integration subset with synthetic keys and isolated homes.

## 15. CLI knowledge search

**Owners:** new `cmd/capy/search.go`, `main.go`, CLI tests; reuse `knowledge.go`.

Expose knowledge query, source, type, bounded limit and kind flags; wire Task 2a's prepared policy into the stale-refresh callback. Output bounded snippets with source/title and no secrets → verify: MCP-indexed data is found by CLI, literal wildcard labels stay scoped, relative/native-anchor denies protect absolute backing paths, repeated CLI processes progress through Task 12a's scheduler, ephemeral content is opt-in except explicit source bypass, and vault-only content is not silently included.

Validate arguments before store access; empty results exit successfully and key/DB failures do not → verify: actual command exit codes and help output. Run CLI and store tests. The task depends on the source-filter fix, not on introducing a server instance into the command.

Amend ADR-012 to name the terminal filtering use case while keeping the MCP content-type field unexposed → verify: CLI help and actual MCP descriptors match that distinction.

## 16a. Renew persisted fetch freshness

**Owners:** source schema/migration/metadata, new store fetched-index orchestration over `indexPreparedChunks`, `internal/server/tool_fetch.go` integration.

Add nullable millisecond validation metadata and an atomic fetched-index operation; generic indexing does not renew the marker. Deliver it through both fetch modes using today's configured TTL → verify: expired → unchanged fetch → immediate hit, force → hit, unchanged kind transition, changed/new content, failed fetch/index not renewing, no search/access renewal and retained stored chunk counts in the unchanged response.

Keep `indexed_at` and retention semantics unchanged for identical content; do not reuse the existing stale-file timestamp updater. Old rows miss conservatively until revalidated → verify: old encrypted schema, repeated migrations, reopen, pre-change binary coexistence/downgrade and source replacement clearing an old marker. Extend ADR-013's freshness rationale. This is a complete default-cache correction before Task 16 adds per-call controls.

## 16. Fetch TTL and argument normalization

**Owners:** `internal/server/coerce.go`, `tool_fetch.go`, `tools.go`, fetch/coercion tests.

1. Parse TTL with the accepted forms/overflow checks and consume Task 6a's force parser → verify: omitted/zero/integer-string/fractional/negative/overflow/null cases before I/O.
2. Compare against Task 16a's persisted millisecond marker in both fetch modes → verify: fractional-second 500 ms boundaries, strict expiry, legacy NULL/future markers, configured fallback, force, kind mismatch, URL keys, unchanged revalidation and reopen.
3. Display effective freshness separately from retention and return accurate unchanged/new outcomes → verify: an old ephemeral source can still expire through retention despite fresh validation.
4. Classify typed fetch failures before formatting and append the bounded one-retry hint only to the selected transient categories → verify: single and batch temporary-DNS/timeout/unreachable failures, and negative cancellation/SSRF/NXDOMAIN/TLS/permission/HTTP/body cases. No automatic retries.

Run real stdio and fetch tests with `-race`; no per-request TTL value is persisted. The validation timestamp migration belongs to Task 16a, not a change to old timestamp formats.

## 17. Cache outcome statistics

**Owners:** `internal/server/stats.go`, `tool_fetch.go`, `tool_stats.go`, stats/fetch tests.

Count typed terminal outcomes once per URL, including bypasses and failed network attempts → verify: concurrent batches, zero-count Git-platform redirects/SSRF/syntax rejection, attempted post-fetch failures, and purge-all reset preserving uptime.

Render hit/miss counts and a zero-safe rate even when all attempts missed. Replace uptime-based TTL remaining with configured default freshness; use one shared one-decimal savings formatter with the nonzero-return clamp at both display sites → verify: long uptime plus mixed TTLs, 99.888%, extremely-near-100%, true zero-return and zero-processed fixtures. Preserve existing savings formula and source/chunk statistics. Update ADR-013's obsolete stats consequence. Run focused stats/fetch tests under `-race`.

## 18. Batch search controls and provenance

**Owners:** `internal/server/tool_batch.go`, `tools.go`, batch tests.

1. Make queries optional and validate supplied queries and `query_scope` before command execution → verify: no-query calls still index; malformed arrays/scopes produce no child side effects rather than becoming indexing-only calls.
2. Default to exact batch source; make global scope explicitly durable+ephemeral knowledge-only and label every hit with its own source → verify: two equal-titled sources remain distinguishable, and vault sessions are not queried.
3. Persist bounded sanitized command previews in indexed sections as well as returning a capped command inventory → verify: later source-filtered provenance retrieval without vault, secret/heredoc/Markdown/UTF-8 cases, and unchanged complete execution/raw-output accounting.
4. Enforce design §4.4's whole serialized-result budget and bounded section metadata query → verify: 8,000 headings, indexing-only/query modes, long labels/queries/terms, escaping, inventory omissions and final serialized size at or below 81,920 bytes. Do not truncate stored content to meet a response budget.

Preserve serial/parallel timeout semantics from Task 6. Existing label-based batch dedup remains; do not claim isolation for simultaneous batches using the same truncated label (follow-up in design risk notes). Run batch and federation suites, then the quality benchmarks.

## 19. Search throttle visibility

**Owners:** `internal/config/config.go`, `loader.go`; server throttle policy and response formatting, schemas/docs and tests.

Add the three `[search]` settings with omission-aware merging, explicit-zero/type/range validation and post-merge taper/block validation. Capture the resulting policy per server and return a coherent snapshot from atomic advance → verify: global/project precedence, defaults, invalid settings, changed thresholds, exact window boundary, empty/partial-result paths, and concurrent calls. Use a clock seam rather than minute-long sleeps; invalid requests remain outside accounting.

The notice states the actual effective limit and shared-server scope. Add an actionable code comment referencing [D1](upstream-audit.md#d1-per-agent-search-budgets) beside `searchThrottle`; do not add a per-agent map without verified request identity. Run server search/federation/vault-search tests under `-race` and retain current source-kind behavior.

## 20. Final verification and documentation

Depends on every implementation slice, including Tasks 2a, 5a, 6a, 12a and 16a. Shared files require coordination; dependencies elsewhere represent behavior prerequisites rather than schema-file serialization.

1. Run `kk:test`: `make test`, `make test-race`, `make build`, and `go test -tags fts5,glamour ./internal/vault/tui/...` to protect the shared retrieval consumers → verify: save commands, outcomes and any environmental limitations in `verification.md`.
2. Run the real CLI/MCP round trips using `cmd/capy/mcp_stdio_helpers_test.go` conventions → verify: fresh and existing encrypted stores, same target across interfaces, correct project selection, shutdown checkpoint and unchanged vault ownership.
3. Run `make bench` against `c0bfff2` and the implementation branch, plus the 10,000-file stale-refresh workload; compare quality/performance and record bounded work, latency, allocation and DB-write measurements → verify: no hidden full-source scan or CLI starvation and no weakened correctness assertions. Rename detached `HEAD.json`/`HEAD.txt` to the baseline label before `make bench-compare`.
4. Run `kk:document` for README/tool/CLI usage, architecture and the complete design §10 compatibility list. Create the three named proposed ADRs there and amend ADR-005/008/012/013/022 where needed → verify: origin-aware grants, cwd/project separation, additive metadata, independent retention and bounded work are recorded with rationale/provenance. Test actual old-reader compatibility before claiming it.
5. Run generated artifact checks and `git diff --check` → verify: each changed generator matches its committed counterpart and no unrelated generated file is included.
6. Run `kk:review-code` with Go input, then `kk:review-spec` against all feature artifacts → verify: fix actionable findings or record each unresolved item durably with a reason and next step. Mark implementation tasks done only with evidence.

Do not remove deferred D1–D5 merely because the suite passes. Both supplied independent reviews have been reconciled; the reconciliation is an author response, not a new independent approval.

## Verification status at design time

The reconciliation reran the existing synthetic encrypted-store probe and focused current security/store tests; results are in its evidence ledger. No feature runtime implementation was changed. All future-behavior tests above remain acceptance criteria, not completed implementation evidence.
