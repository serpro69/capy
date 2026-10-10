# Tasks: Upstream sync through context-mode 0dfbe8d

> Design: [design.md](design.md)
> Implementation: [implementation.md](implementation.md)
> Audit: [upstream-audit.md](upstream-audit.md)
> Reconciliation: [.reviews/reconciliation-2026-10-10.md](.reviews/reconciliation-2026-10-10.md)
> Status: pending
> Created: 2026-10-10
> Not Doing: CI/bundle/docs-only ports, unsupported platforms, hosted analytics/pricing, event/directive replay, new Codex hooks, new runtimes, transcript-based cwd guessing, vault schema/key changes, ranking rewrites, unconditional source echoes, OS sandboxing
> Deferred follow-ups: per-agent throttle identity (D1), atomic checked-file handoff (D2), colliding concurrent batch labels (D3), structured chunk splitting (D4), authoritative child tool-pool discovery (D5); see audit.
> Review additions: lettered Tasks 2a, 5a, 6a, 12a, 16a preserve the original task numbers; dependencies, not heading order, determine execution order.
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
- [ ] 1.3 Propagate typed scanner-limit errors as hook blocks/MCP errors before spawning; test unquoted-heredoc substitutions and every numeric bound → verify: security/hook/server suites, with no partial-list fallback.

## Task 2a: Prepare origin-aware Read policies on current file paths

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 3, 6, 7, 10, 11
- **Docs:** [Prepared policy](implementation.md#2a-prepared-read-policy-on-existing-paths)

### Subtasks

- [ ] 2a.1 Retain rule source/anchors and prepare supported path grammar in `internal/security` → verify: root/home/settings/cwd anchors, escaped literals, bare Read and unsupported syntax diagnostics.
- [ ] 2a.2 Preserve single-slash legacy denies conservatively; never use legacy interpretation for new allows → verify: no weakened deny or broadened grant through symlink aliases.
- [ ] 2a.3 Wire the prepared policy into existing direct-file and stale-refresh consumers → verify: relative, absolute and physical inputs make the same decision using isolated project/user settings.

## Task 2: Admit execute-file paths consistently

- **Status:** pending
- **Depends on:** Task 2a
- **Size:** M
- **Can run in parallel with:** Tasks 3, 6, 10, 11, 12
- **Docs:** [Path admission](implementation.md#2-execute-file-path-admission)

### Subtasks

- [ ] 2.1 Use Task 2a's prepared Read rules for canonical containment and external grants → verify: native absolute/home rules, traversal, symlink aliases, deny-wins and explicit exceptions.
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
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 2, 3, 6, 7, 10, 11, 12, 16
- **Docs:** [Explicit redirects](implementation.md#4-explicit-main-agent-redirects)

### Subtasks

- [ ] 4.1 Use `FormatBlock` in `routeBash` for capy redirects → verify: deny JSON contains actionable guidance and does not approve an echo replacement.
- [ ] 4.2 Preserve safe downloads, security asks and Agent input edits → verify: hook and adapter tests.

## Task 5: Route subagents according to available tools

- **Status:** pending
- **Depends on:** Tasks 2a, 3, 4
- **Size:** M
- **Can run in parallel with:** Tasks 2, 6, 7, 10, 11, 12, 16
- **Docs:** [Subagent routing](implementation.md#5-subagent-aware-routing-and-discovery)

### Subtasks

- [ ] 5.1 Preserve child identity/payload cwd; select project before loading rules in the hook command/context path → verify: explicit/environment/payload/process precedence and no mismatched policy directory.
- [ ] 5.2 Treat unverified child tools as unknown, with advisory fallback and security checks intact → verify: unknown/fixed-tool children avoid unavailable redirects, and type alone does not classify a child or its tool pool.
- [ ] 5.3 Add bounded deferred-tool discovery/fallback wording to injected and generated routing → verify: Agent inputs survive, generated `.capy/AGENTS.md` matches its generator, and artifact tests pass.

## Task 5a: Restore child redirects for observed available tools

- **Status:** pending
- **Depends on:** Tasks 3, 5
- **Size:** M
- **Can run in parallel with:** Tasks 6, 7, 10, 11, 16
- **Docs:** [Observed capabilities](implementation.md#5a-re-enforce-redirects-after-observed-child-capabilities)

### Subtasks

- [ ] 5a.1 Record finite per-tool capabilities from child PostToolUse events with safe session/agent-scoped state → verify: concurrent updates, sibling isolation and corrupt/missing state.
- [ ] 5a.2 Require only an observed suitable alternative in a child redirect → verify: execute-capable child is guarded, search-only child is not forced into execute, and security decisions still dominate.
- [ ] 5a.3 Document the first-call unknown limitation and D5; synchronize any generated wording → verify: no claim that two definition files reveal the effective tool pool.

## Task 6: Preserve batch heredocs and captured stderr

- **Status:** pending
- **Depends on:** —
- **Size:** S
- **Can run in parallel with:** Tasks 1, 2, 3, 4, 5, 7, 10, 11, 12
- **Docs:** [Batch streams](implementation.md#6-batch-heredocs-and-stderr)

### Subtasks

- [ ] 6.1 Execute original commands in both batch worker paths and combine captured streams afterward → verify: heredoc terminators and multiline commands run intact.
- [ ] 6.2 Cover partial/empty/stderr-only output and serial/parallel timeout semantics → verify: batch tests under `-race` and assertions against indexed content.

## Task 6a: Validate boolean inputs consistently

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 1, 3, 7, 10, 11, 12a
- **Docs:** [Boolean contract](implementation.md#6a-consistent-boolean-request-validation)

### Subtasks

- [ ] 6a.1 Add the presence-aware literal parser in `coerce.go` and wire execute/fetch flags → verify: string booleans preserve intent; invalid background spawns nothing.
- [ ] 6a.2 Apply it to cleanup and both all-projects selectors, preserving omitted defaults → verify: invalid/null/numeric values cause no purge/optimization/widening, and explicit literal `dry_run: "false"` is intentionally honored.
- [ ] 6a.3 Exercise actual stdio requests and record compatibility changes → verify: no handler-only coercion claim or false assertion about the SDK's previous string handling.

## Task 7: Use project cwd for every runtime

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 1, 3, 4, 5, 6, 10, 11, 12
- **Docs:** [Runtime cwd](implementation.md#7-project-cwd-for-every-runtime)

### Subtasks

- [ ] 7.1 Separate temporary scripts from execution cwd in `executor.go` → verify: project-relative fixture operations across installed runtimes.
- [ ] 7.2 Keep Rust compilation temporary and run its binary in the project; align direct execute-file path resolution → verify: compiled and file-processing fixtures when process cwd differs.
- [ ] 7.3 Test Go module/replace/vendor/toolchain behavior and Elixir project detection; retain safe-env stripping of `GOFLAGS` → verify: executor fixtures and documented relative-write/module changes.

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
- **Depends on:** —
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
- **Depends on:** Task 2a
- **Size:** M
- **Can run in parallel with:** Tasks 1, 6, 10, 11, 16
- **Docs:** [File ingestion](implementation.md#12-shared-bounded-file-ingestion)

### Subtasks

- [ ] 12.1 Extract filesystem ingestion into `internal/knowledge/index.go` and route MCP file indexing through it → verify: existing canonical labels, dedup, durable kind and stale refresh remain valid.
- [ ] 12.2 Bound actual reads in ingestion and `store.fileChangedSince`, using prepared rules → verify: deterministic growth-after-stat, FIFO, absolute input matching a relative deny, cached-content retention and logged skip.

## Task 12a: Bound stale-refresh work across server and CLI searches

- **Status:** pending
- **Depends on:** Task 12
- **Size:** M
- **Can run in parallel with:** Tasks 3, 6, 6a, 7, 11, 19
- **Docs:** [Stale scheduling](implementation.md#12a-persistently-schedule-bounded-stale-refresh)

### Subtasks

- [ ] 12a.1 Add the scheduling column/index through safe additive migration and bounded metadata selection → verify: old encrypted schema, query plans, migration idempotence and 32-row limit.
- [ ] 12a.2 Persist attempted-check order, transfer it through source replacement and enforce read/time-admission budgets → verify: denied/missing/changed files, unprocessed rows stay eligible, and repeated new CLI instances progress beyond page one.
- [ ] 12a.3 Add a 10,000-source measurement fixture → verify: bounded work/memory, eventual progress and recorded latency/write overhead for Task 20.

## Task 13: Index directories with explicit traversal limits

- **Status:** pending
- **Depends on:** Tasks 2a, 6a, 11, 12, 12a
- **Size:** M
- **Can run in parallel with:** Tasks 1, 3, 6, 7, 9, 10, 15
- **Docs:** [Directory ingestion](implementation.md#13-directory-ingestion-with-selection-controls)

### Subtasks

- [ ] 13.1 Implement deterministic traversal, per-file labels/outcomes and all hard bounds in `internal/knowledge` → verify: partial/capped/canceled scans report their actual extent.
- [ ] 13.2 Implement validated pattern/extension controls, canonical symlink containment and cycle detection → verify: deny rules and secret/VCS exclusions survive all options.
- [ ] 13.3 Apply the ignore evaluator from the canonical root's containing worktree → verify: external root uses its own ignores while capy project policies/keys stay fixed, plus nested/negated/tracked rules and visible failures.
- [ ] 13.4 Add dispatch/schema fields using shared boolean validation and prepared rules → verify: colon-label identity, same/different-prefix reruns, bounded stale checks and no implicit deletion.

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
- **Depends on:** Tasks 2a, 10, 12a
- **Size:** S
- **Can run in parallel with:** Tasks 1, 6, 8, 9, 11, 13, 16, 17, 18, 19
- **Docs:** [CLI search](implementation.md#15-cli-knowledge-search)

### Subtasks

- [ ] 15.1 Add `cmd/capy/search.go` with bounded limit/source/type/kind flags and existing key resolution → verify: MCP→CLI retrieval and meaningful no-result/error exit codes.
- [ ] 15.2 Install stale-read denies and retain knowledge-only semantics → verify: denied backing files, ephemeral filtering, literal sources and no accidental vault federation.
- [ ] 15.3 Amend ADR-012 for CLI-only content-type selection → verify: the MCP schema still omits that filter.

## Task 16a: Renew fetch freshness atomically on successful validation

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 1, 3, 4, 7, 9, 11, 19; coordinate store migrations with Task 12a
- **Docs:** [Freshness renewal](implementation.md#16a-renew-persisted-fetch-freshness)

### Subtasks

- [ ] 16a.1 Add nullable millisecond validation metadata and fetched-index transaction semantics → verify: migration/reopen, generic replacements clear markers, and old readers remain compatible.
- [ ] 16a.2 Use it in both fetch modes under the configured TTL, including identical-content/kind-transition outcomes → verify: expire/force → unchanged revalidation → hit with correct stored chunk counts.
- [ ] 16a.3 Preserve indexed-at retention and prevent renewal on fetch/index failures or ordinary search → verify: durable/ephemeral lifecycle fixtures and ADR-013 amendment.

## Task 16: Add per-call fetch freshness

- **Status:** pending
- **Depends on:** Tasks 6a, 16a
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 9, 10, 11, 12, 14, 15
- **Docs:** [Fetch TTL](implementation.md#16-fetch-ttl-and-argument-normalization)

### Subtasks

- [ ] 16.1 Parse strict TTL and reuse shared force normalization → verify: omitted/zero/invalid/overflow cases before I/O.
- [ ] 16.2 Compare persisted millisecond markers and expose `ttl` → verify: 500 ms boundaries, NULL/future markers, unchanged revalidation, cache-key/kind behavior, force and reopen.
- [ ] 16.3 Display effective freshness separately from retention → verify: fetch/SSRF/size-limit tests under `-race`.
- [ ] 16.4 Add typed transient-error retry guidance to single and batch fetch → verify: temporary DNS/timeout/unreachable hints and negative cancellation/policy/NXDOMAIN/TLS/permission cases, with no automatic retry.

## Task 17: Report cache hits and attempted misses

- **Status:** pending
- **Depends on:** Task 16
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 9, 10, 11, 12, 14, 15, 19
- **Docs:** [Cache statistics](implementation.md#17-cache-outcome-statistics)

### Subtasks

- [ ] 17.1 Count per-URL outcomes consistently in fetch/stats paths and include them in snapshot/reset → verify: concurrent batches and purge-all counters.
- [ ] 17.2 Replace uptime-based TTL remaining with configured default freshness and show typed hit/miss outcomes → verify: long uptime, mixed TTLs, failed attempts, and zero-count Git redirects/SSRF blocks.
- [ ] 17.3 Format both savings percentages with one decimal and nonzero-return clamp → verify: 99.888%, near-100%, zero-return/zero-processed cases and amended ADR-013 wording.

## Task 18: Add batch query scope and bounded provenance

- **Status:** pending
- **Depends on:** Task 6
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 9, 12, 14, 15, 17
- **Docs:** [Batch controls](implementation.md#18-batch-search-controls-and-provenance)

### Subtasks

- [ ] 18.1 Make queries optional and implement validated batch/global scope → verify: no-query indexing and durable+ephemeral knowledge-only global results.
- [ ] 18.2 Persist sanitized bounded per-command provenance and label every global hit by source → verify: later knowledge-only retrieval and equal-titled sources remain attributable.
- [ ] 18.3 Bound the entire serialized result, section metadata and inventories → verify: 8,000 sections, long headings/queries, escaping and Unicode stay within 81,920 bytes without losing indexed content.
- [ ] 18.4 Preserve raw/output accounting and execution semantics; record D3 at the existing label builder → verify: batch/federation suites and final benchmark comparison.

## Task 19: Configure and report the shared search budget

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Tasks 3, 5, 9, 12, 14, 15, 17
- **Docs:** [Throttle visibility](implementation.md#19-search-throttle-visibility)

### Subtasks

- [ ] 19.1 Add `[search]` settings/defaults with presence-aware merge and validation → verify: explicit-zero/type/range/relationship errors and global/project precedence.
- [ ] 19.2 Use effective values in throttle decisions/notices for every valid path → verify: default and overridden boundaries, exact reset, empty/partial results and concurrent calls using a clock seam.
- [ ] 19.3 Record shared-server scope/D1 and update ADR-008 → verify: tunability is not claimed as per-agent isolation and invalid calls consume no budget.

## Task 20: Verify the complete sync and document compatibility

- **Status:** pending
- **Depends on:** Tasks 1–19, 2a, 5a, 6a, 12a, 16a
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Final verification](implementation.md#20-final-verification-and-documentation)

### Subtasks

- [ ] 20.1 Run `kk:test`, full FTS5/race suites, default build and glamour TUI subset → verify: record commands/results in `verification.md`.
- [ ] 20.2 Run encrypted CLI/MCP round trips, additive migration/old-reader cases and artifact checks → verify: same project/key/content and close/reopen behavior.
- [ ] 20.3 Run quality/performance plus 10,000-file stale-work benchmarks against `c0bfff2` → verify: bounded work/progress, explain regressions and keep assertions intact.
- [ ] 20.4 Run `kk:document`, record the named cwd/admission/freshness ADRs and amend existing decisions; publish all compatibility changes from design §10 → verify: generators/copies and contract descriptions agree.
- [ ] 20.5 Run `kk:review-code` with Go input, then `kk:review-spec` → verify: resolve findings or record actionable deferrals; retain D1–D5 until actually addressed.

## Dependency Graph

```text
Task 2a                       --> Task 2
Tasks 2a, 3, 4               --> Task 5 --> Task 5a
Tasks 2, 6, 7                --> Task 8
Task 2a                       --> Task 12 --> Task 12a
Tasks 2a, 6a, 11, 12, 12a    --> Task 13 --> Task 14
Tasks 2a, 10, 12a            --> Task 15
Tasks 6a, 16a                --> Task 16 --> Task 17
Task 6                        --> Task 18
Tasks 1, 2a, 3, 4, 6, 6a, 7, 9, 10, 11, 16a, 19 have no prerequisites.
Tasks 1-19, 2a, 5a, 6a, 12a, 16a --> Task 20
```

Parallel markers indicate possible independent work after dependencies are met, not permission to overwrite another contributor's shared file. Coordinate mechanically shared schema/registration edits.
