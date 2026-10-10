# Implementation: Upstream sync through context-mode 0dfbe8d

> Status: in-progress (Tasks 1, 2a and 2 complete; remaining tasks pending)
> Design: [design.md](design.md)
> Provenance and exclusions: [upstream-audit.md](upstream-audit.md)
> Execution checklist: [tasks.md](tasks.md)
> Review reconciliation: [.reviews/reconciliation-2026-10-10.md](.reviews/reconciliation-2026-10-10.md)
> Latest readiness check: [.reviews/readiness-2026-10-10.md](.reviews/readiness-2026-10-10.md)

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

Task 1 implementation notes (2026-10-10): the scanner also tracks comments,
ANSI-C string quotes and parameter-expansion boundaries because each can hide
substitutions if treated as ordinary characters. Hook batches normalize the
server's supported JSON-string/plain-string command forms before scanning, and
defer matched asks until all commands have cleared deny/error checks. Unsupported
ANSI-C/localized **heredoc delimiter** quoting fails closed with an actionable
message. Full delimiter escape/locale decoding is deferred to avoid approximating
shell semantics; add differential Bash fixtures and bounded decoding before
admitting those forms. Ordinary quoted delimiters remain supported. See
[Task 1 verification](verification.md#task-1-shell-policy-evaluation).

## 2a. Prepared Read policy on existing paths

**Owners:** `internal/security/settings.go`, `eval.go`, new `file_policy.go`; existing server/hook policy wiring and stale-read callback.

Retain rule origin and explicit anchor/cwd/home context when loading local/shared/user settings. Prepare the supported grammar and bare-tool rule; preserve a legacy absolute interpretation for single-slash **denies only**, with a diagnostic. New allows use host anchors only → verify: `//`, `~/`, source-relative `/`, cwd-relative rules, escaped literals, unsupported syntax and differing user/project settings origins.

Use one prepared object in existing MCP direct reads and the store's deny callback before adding new ingestion/CLI paths → verify: the same relative deny blocks raw-relative, absolute and physical paths, including stale refresh, and no local symlink alias grants an external target. Invalid policy preparation blocks file admission visibly. Test this end-to-end, not only the legacy exported matcher. Keep settings origin distinct from execution cwd. Run security/server/stale tests; coordinate shared files with Task 1 without making its shell parser a dependency.

Task 2a implementation notes (2026-10-10): `LoadReadPolicy(FilePolicyContext)`
captures rule origin/action/anchor and returns a prepared `FilePolicy` or an
error. Server construction retains either outcome; all direct reads and the
stale checker enforce it. Hooks return `FormatBlock` on errors for execute-file
and file-index calls. Missing settings are normal; malformed JSON, invalid
permission arrays and unsupported Read patterns are not empty policies.
Diagnostics quote rule/source data and compatibility warnings are deduplicated
per rule/source in the snapshot. Server policy changes require restart.

Literal deny prefixes resolve physically as well as lexically; allows resolve
the anchor only and must cover both normalized requested and physical targets.
This avoids turning an allowed alias into an external grant. The API's `Allows`
method prepares Task 2's grant check; current direct reads enforce denies only.
The legacy exported loader/matcher are retained without production callers.
Task 5 still owns validated hook payload-cwd/project selection. D2 still owns
atomic checked-file handoff; neither this task nor descriptor-bound stat/read
eliminates replacement between policy evaluation and open. Named ADR creation
remains Task 20. No CLI interface or CI configuration changes in this slice.

## 2. Execute-file path admission

**Owners:** `internal/security/eval.go`, `settings.go`, `internal/server/security_check.go`, `server.go`, `tool_execute_file.go`; corresponding tests.

1. Consume Task 2a's prepared policy rather than raw glob arrays → verify: native absolute/home grants and legacy deny compatibility on local/shared/user fixtures.
2. Add a checked absolute-path resolver with lexical and physical containment and explicit external grants covering requested and real paths → verify: absolute outside paths, traversal, sibling-prefix paths, direct symlinks, symlink-then-`..`, denied-but-allowed targets, and valid external exceptions.
3. Pass the admitted absolute path into `ExecuteFile` → verify: a server project different from process cwd reads the expected fixture and rejects a denied path before any child is spawned.
4. Document the remaining check/open race at the helper and link [D2](upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads) → verify: documentation and error text do not claim an OS sandbox.

Run `go test -tags fts5 -count=1 ./internal/security/... ./internal/server/...`. Existing explicit external-path tests should use intentional allow fixtures; do not just remove them or weaken the assertions.

Task 2 implementation notes (2026-10-10): `FilePolicy.ResolveExecuteFile`
reuses the prepared deny/grant matchers and candidate resolver in
`internal/security/file_policy.go`. It resolves relative inputs from the selected
project, even when the policy's rule cwd differs, and checks both lexical and
physical containment using path components. Symlinked project roots accept their
lexical and canonical spellings. One allow must cover both candidates for an
external read; unrelated partial allows do not combine. Missing/unresolvable
targets and invalid projects fail before execution. The server forwards the
returned physical absolute path to the executor while keeping existing response
source labels. No second policy load is needed.

Isolated review found that embedding that physical path in generated Ruby,
Elixir, PHP or Perl source could interpolate a filename into a different, denied
target. The executor now transfers it in the request-local child environment as
`CAPY_FILE_CONTENT_PATH`; each runtime reads that value into its existing path
variable. This also avoids incompatible control/Unicode escapes across runtimes.
The parent environment is never mutated, and inherited values cannot override
the request. Rust receives it when running the compiled binary. This necessary
handoff fix touches `executor.go` and `wrap.go`; Task 7's cwd changes stay pending.
JavaScript/TypeScript obtain the environment through `require("process")` so a
snippet's own `process` declaration does not shadow the preamble.

The original external-success fixture now declares an intentional absolute Read
allow. Shell-policy fixtures use existing project files so they still exercise
command rejection. Tests cover differing process/project cwd and symlink-before-
`..` handoff with distinct file contents. `capy_index` keeps deny-only explicit
file admission. D2 remains documented at the helper: an atomic checked-file
handoff into the child runtime is required to close concurrent replacement.
The named admission ADR remains part of Task 20.

## 3. Safe guidance filenames

**Owners:** `internal/hook/guidance.go`, guidance tests.

Use design §3.4's exact ID alphabet/128-byte bound and digest prefix from both `guidanceOnce` and `ResetGuidanceFile`; empty IDs stay non-persisting → verify: repeated invocations share one file, safe IDs remain compatible, unsafe IDs stay separate, reset targets the same file, and traversal/NUL/long IDs create nothing outside temporary `.capy`. New observations have a separate format and reuse only the component helper. Run hook tests.

## 4. Explicit main-agent redirects

**Owners:** `internal/hook/pretooluse.go`, hook routing tests; adapter tests only if formatter coverage needs strengthening.

Replace echo-command rewrites for curl/wget and inline HTTP with direct `FormatBlock` calls. Preserve safe-download exceptions, actual security asks/denies and Agent input modifications → verify: inspect the returned JSON decision and reason, and prove the original command is not approved as part of the redirect. Run `go test -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...`.

The rationale is reliable expression of a block, not an unverified assertion that every current Claude Code build ignores `updatedInput`.

## 5. Subagent-aware routing and discovery

**Owners:** adapter event parsing, `cmd/capy/hook.go` and hook context selection, an anchored detection helper in `internal/config/paths.go`, pre-tool routing; generated routing owner/copy where wording changes.

1. Preserve `agent_id`/`agent_type`/`cwd` and resolve project identity before policy loading using design §3.5 precedence → verify: actual child, type-only main agent, environment missing, explicit project override, payload cwd outside process cwd and nested project directories. Prepared rule origin/state storage must use the selected project consistently.
   The existing `DetectProjectRoot()` is process-cwd-based. Add a start-directory helper rather than temporarily calling `os.Chdir` or changing environment variables; pin any Git probe's directory and clear repository-routing overrides. Invalid/nonabsolute payload cwd is ignored with a diagnostic and uses the stated fallback; an invalid explicit selected directory is an error, not an alternative-project fallback.
2. Treat child availability as unknown unless demonstrated by Task 5a; keep advisory discovery and native-tool fallback → verify: unknown/fixed-tool children do not get trapped, while explicit denies and matched asks still run first. Do not label every child unavailable or use two local frontmatter files as the effective host tool pool.
3. Add one-attempt deferred-tool discovery guidance with native-tool fallback; preserve original Agent/Task fields → verify: no recursive bootstrap, no bootstrap mandate without discovery support, and Bash-to-general-purpose behavior remains covered.
4. Align generated routing text and its committed copy → verify: `go test -tags fts5 ./internal/platform -run 'TestGeneratedWholeFileArtifacts|TestMergedArtifactsAreIdempotent'` plus hook/adapter tests.

A parent MCP server's existence is not evidence that a fixed-tool child can call it. Do not introduce a new ready sentinel or Codex hook adapter.

## 5a. Re-enforce redirects after observed child capabilities

**Owners:** new bounded observation-state helper in `internal/hook`, post/pre-tool routing, session-end cleanup and dispatch wiring; reuse the ID helper from `guidance.go`.

Implement the one-use, per-tool 60-second evidence in design §3.5, stored in one bounded project file behind a separate stable lock → verify: exact eligible tools, no renewal on failures or another tool's success, 64 KiB/128-entry eviction, missing stable identity, sibling isolation, concurrent update/consume, lock timeout and corrupt/future state.

Consume all of that child's evidence before one redirect and follow the explicit alternative table → verify: failed capy retry permits the next native attempt, arbitrary HTTP is never turned into a fetch-only operation, and security decisions still dominate without consuming evidence. Parse stable identity for SessionEnd and remove only its entries, without DB access → verify: cleanup, missed SessionEnd expiry, live sibling preservation and no unbounded per-session file creation. Update routing generator/copy together. D5 remains the unknown first-call limitation.

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

Add `file_check_seq` plus its partial scheduling index after column admission. Page at most 32 oldest-checked sources, close metadata rows before writes, and allocate/update completed-check sequences atomically in short transactions using the indexed current maximum. Skip concurrently removed/replaced incarnations; carry state only through the refresh's own same-file replacement → verify: migration/reopen, query plans, concurrent updates, no resurrection or full-source snapshot, and new CLI processes advancing beyond page one.

Apply design §5.5's byte budget including the growth-detection byte, post-metadata admission clock and single-active-refresh guard → verify: maximum source sizes above 8 MiB still make progress, slow metadata does not suppress every first attempt, slow I/O cannot start overlapping passes in one instance, and unattempted rows stay eligible. Include 10,000-source/fairness/restart, cancellation, scheduling-write failure and bounded cross-instance duplicate cases. Record latency/allocation/read/write counts. This changes freshness timing, not ranking or retention timestamps.

## 13. Directory ingestion with selection controls

**Owners:** new `internal/knowledge/directory.go`, `patterns.go`, `gitignore.go`; MCP path dispatch in `tool_index.go` and schema in `tools.go`.

1. Implement deterministic default non-following traversal with the design's hard limits and explicit outcomes → verify: counts distinguish visited entries/admitted attempts, raw reads include unchanged/failed/growing files and probe bytes, hard/operator caps are incomplete-success while operational failures are partial errors, and no partial file is indexed at exhaustion.
2. Implement validated include/exclude/extensions, canonical root selection and colon labels → verify: direct-root symlinks versus skipped descendant links, depth 0, huge flat directories, same/different-prefix dedup and mandatory exclusions.
3. Discover the canonical root's worktree and prune descendant Git roots before invoking bounded `git check-ignore --no-index --stdin -z` calls → verify: external roots, nested repositories/submodules, negated/tracked rules, unusual filenames and inherited Git variables. Keep selected-project policies/credentials fixed. Treat exits 0/1 as normal; other Git failures are visible, never an implicit unfiltered scan.
4. Expose directory controls using Task 6a's boolean parser and Task 2a's prepared policy → verify: direct/walked/refreshed absolute paths obey the same relative denies, repeated labels deduplicate, and Task 12a bounds searches after many repeated ingestion calls.

Task 13 delivers default directory ingestion; Task 13a separately adds descendant symlink following. This keeps the Git/symlink integration risk out of an oversized single task without weakening default admission.

## 13a. Follow admitted symlinks during directory ingestion

**Owners:** `internal/knowledge/directory.go`, `gitignore.go`; `follow_symlinks` schema/CLI option wiring and tests.

Add the opt-in path with canonical target selection, cycle/alias dedup and both alias/target mandatory-exclusion and prepared-policy checks → verify: innocent aliases to ignored/denied/credential targets remain excluded, and links stay in the canonical root and its selected worktree.

Git checks a symlink entry itself and canonical target paths, never a path descending lexically through a directory symlink → verify: reproduce Git's `beyond a symbolic link` failure with a raw alias spelling, then prove ingestion uses the canonical path and honors its ignores. Check prefixed labels, overlapping aliases, nested Git targets, dangling links and all inherited budgets. A broken opt-in must not silently downgrade to unfiltered following.

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

Add nullable millisecond metadata and an atomic fetched-index operation. Carry each URL's timestamp from completed/admitted HTTP body read through conversion and serial indexing; generic indexing never renews it → verify: queue/lock delays do not restart freshness, out-of-order commits pair marker with their actual content, and expired/forced unchanged revalidation re-arms caching. Include kind transitions, failed fetch/index, no access renewal and accurate stored chunk counts.

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

**Owners:** `internal/server/tool_batch.go`, `tools.go`, the store's bounded section-metadata query, batch tests.

1. Make queries optional and validate supplied queries and `query_scope` before command execution → verify: no-query calls still index; malformed arrays/scopes produce no child side effects rather than becoming indexing-only calls.
2. Default to exact batch source; make global scope explicitly durable+ephemeral knowledge-only and label every hit with its own source → verify: two equal-titled sources remain distinguishable, and vault sessions are not queried.
3. Persist bounded sanitized command previews in indexed sections as well as returning a capped command inventory → verify: later source-filtered provenance retrieval without vault, secret/heredoc/Markdown/UTF-8 cases, and unchanged complete execution/raw-output accounting.
4. Enforce design §4.4's whole serialized-result budget and bounded section metadata query → verify: 8,000 headings, indexing-only/query modes, long labels/queries/terms, escaping, inventory omissions and final serialized size at or below 81,920 bytes. Do not truncate stored content to meet a response budget.
5. Handle `AlreadyIndexed` as an unchanged outcome and read its stored total before summary/omission arithmetic → verify: repeated identical batches report retained sections rather than zero, and omitted inventory counts reflect the stored source.

Preserve serial/parallel timeout semantics from Task 6. Existing label-based batch dedup remains; do not claim isolation for simultaneous batches using the same truncated label (follow-up in design risk notes). Run batch and federation suites, then the quality benchmarks.

## 19. Search throttle visibility

**Owners:** `internal/config/config.go`, `loader.go`; server throttle policy and response formatting, schemas/docs and tests.

Add the three `[search]` settings with omission-aware merging, explicit-zero/type/range validation and post-merge taper/block validation. Capture the resulting policy per server and return a coherent snapshot from atomic advance → verify: global/project precedence, defaults, invalid settings, changed thresholds, exact window boundary, empty/partial-result paths, and concurrent calls. Use a clock seam rather than minute-long sleeps; invalid requests remain outside accounting.

The notice states the actual effective limit and shared-server scope. Add an actionable code comment referencing [D1](upstream-audit.md#d1-per-agent-search-budgets) beside `searchThrottle`; do not add a per-agent map without verified request identity. Run server search/federation/vault-search tests under `-race` and retain current source-kind behavior.

## 20. Final verification and documentation

Depends on every implementation slice, including Tasks 2a, 5a, 6a, 12a, 13a and 16a. Shared files require coordination; dependencies elsewhere represent behavior prerequisites rather than schema-file serialization.

1. Run `kk:test`: `make test`, `make test-race`, `make build`, and `go test -tags fts5,glamour ./internal/vault/tui/...` to protect the shared retrieval consumers → verify: save commands, outcomes and any environmental limitations in `verification.md`.
2. Run the real CLI/MCP round trips using `cmd/capy/mcp_stdio_helpers_test.go` conventions → verify: fresh and existing encrypted stores, same target across interfaces, correct project selection, shutdown checkpoint and unchanged vault ownership.
3. Run `make bench` against `c0bfff2` and the implementation branch, plus the 10,000-file stale-refresh workload; compare quality/performance and record bounded work, latency, allocation and DB-write measurements → verify: no hidden full-source scan or CLI starvation and no weakened correctness assertions. Rename detached `HEAD.json`/`HEAD.txt` to the baseline label before `make bench-compare`.
4. Run `kk:document` for README/tool/CLI usage, architecture and the complete design §10 compatibility list. Create the three named proposed ADRs there and amend ADR-005/008/012/013/022 where needed → verify: origin-aware grants, cwd/project separation, additive metadata, independent retention and bounded work are recorded with rationale/provenance. Test actual old-reader compatibility before claiming it.
5. Run generated artifact checks and `git diff --check` → verify: each changed generator matches its committed counterpart and no unrelated generated file is included.
6. Run `kk:review-code` with Go input, then `kk:review-spec` against all feature artifacts → verify: fix actionable findings or record each unresolved item durably with a reason and next step. Mark implementation tasks done only with evidence.

Do not remove deferred D1–D5 merely because the suite passes. Both supplied independent reviews have been reconciled; the reconciliation is an author response, not a new independent approval.

## Verification status at design time

The reconciliation reran the existing synthetic encrypted-store probe and focused current security/store tests; results are in its evidence ledger. No feature runtime implementation was changed. All future-behavior tests above remain acceptance criteria, not completed implementation evidence.
