# Design: Upstream sync through context-mode 0dfbe8d

> Status: proposed; implementation has not started
> Created: 2026-10-10
> Feature: `upstream-sync-v1.0.169`
> Upstream range: `f8d46390613f068f232eb14ad91804841c64bdfa..0dfbe8de71abcb637a07dd6444bee5823c3186fc`
> Capy inspected at: `c0bfff2800614b47a163949eef34645f854fff3a`
> Related: [audit](upstream-audit.md), [implementation](implementation.md), [tasks](tasks.md), [previous sync](../../done/upstream-sync-v1.0.136/design.md)

## 1. Problem and outcome

Port the useful runtime changes accumulated after the last sync while retaining capy's own knowledge, vault, configuration and retrieval architecture. The audience is developers using capy's MCP tools through Claude Code or Codex, and developers who need the same knowledge operations from a terminal.

The user confirmed two boundaries: retain relevant changes for existing Claude Code/Codex integrations, and include both fixes and compatible additions. CI, bundles, unsupported platforms and docs-only upstream changes are excluded. The [audit](upstream-audit.md) explains each selected, covered, rejected and deferred behavior. Its exact SHA range is authoritative: the target is 167 commits beyond the v1.0.169 tag, not just that release.

Success means the selected regressions have behavioral tests, added interfaces have equivalent CLI/MCP semantics where promised, plaintext chunks stay at or below 4,096 bytes, directory work has explicit bounds, and existing retrieval quality, encryption, source lifecycle and generated-artifact checks pass. No result may silently claim a complete directory traversal or an unrestricted search when a bound or filter narrowed it.

This is a non-trivial selective port. Constraint mapping and a failure-first review favor independently testable slices over upstream subsystem replacement. Source inspection establishes the current gaps; runtime tests remain implementation acceptance criteria.

## 2. Current architecture and constraints

- `internal/server/tools.go` owns MCP schemas; per-tool files own request handling. The executor is shared and concurrent, while every execution gets its own temporary script directory.
- `internal/security` supplies Bash policy parsing, command evaluation and Read-deny matching. The hook enforces deny/ask behavior; the MCP server enforces deny rules because it cannot display host permission prompts.
- `internal/store` owns knowledge labels, source kinds, sanitization, chunking, stale-file refresh and encryption. `internal/retrieval` owns corpus-independent ranking; source filtering remains in the knowledge corpus adapter.
- `internal/vault` is the sole session store. Preserve [ADR-027](../../../adr/027-vault-is-sole-session-store.md) and [ADR-028](../../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md).
- `cmd/capy/knowledge.go` owns project/config/credential selection. New CLI commands must reuse this flow and the captured key rather than opening a database from a fresh environment lookup.
- Only `ClaudeCodeAdapter` implements hooks. Codex support consists of MCP setup/routing and vault decoding; this sync does not create a Codex hook subsystem.

Preserve mandatory encryption, FTS5 build tags, pool-close-before-checkpoint ordering, source-kind checks, strict fetch SSRF behavior, sanitized content hashing, source-size limits, per-source diversification and the existing RRF/rerank formulas. No schema migration or module upgrade is required.

## 3. Security and hook behavior

### 3.1 Shell policy evaluation — S1

Use one command-element scanner for both evaluators. Recognize executable segments separated by newline/CR, `;`, pipes, `&&`, `||` and background `&`, plus nested `$()` and backtick substitutions. Track escape parity and shell quote contexts rather than inspecting only the preceding byte. A separator that is escaped or quoted is data; redirection such as `2>&1` is not a background-command boundary.

Do not treat literal single-quoted or quoted-heredoc text as commands. Double-quoted substitutions still execute. Arithmetic syntax is not itself a command, but nested substitutions inside it are. Cover these distinctions before changing policy decisions; the TS implementation is a case source, not an authoritative shell parser.

Any denied element denies the whole request. Preserve ordered settings precedence for ask/allow; within a policy, an explicit ask wins, and allow requires every executable element to match. Otherwise the full evaluator returns ask; the deny-only evaluator still allows non-denied commands. Capy's existing hook only surfaces asks backed by a matched pattern; do not silently convert its unmatched default into a new mandatory approval mechanism.

Retain capy's existing case-sensitive policy matching. Do not port upstream's blanket macOS case-folding assumption into command matching or filesystem admission.

Bound scanner nesting/work and return a clear evaluation failure for inputs exceeding that bound. This remains static policy matching, not a complete shell interpreter or a security boundary for arbitrary code in other languages.

### 3.2 Execute-file path admission — S2

Resolve an explicit `path` relative to the server's selected project directory once; absolute inputs remain absolute. Run Read denies against raw, lexical and physical candidates, retaining symlink-before-`..` handling. Check ordinary containment using path-component relationships, not string-prefix similarity.

In-project paths are admitted; an external path requires a matching existing `Read(...)` allow for its external target. A deny always wins. Matching a project-local symlink alias is insufficient to authorize an external real target. Failure to resolve the configured project or an existing target is an error, not a reason to silently disable containment. Missing paths produce normal file errors. Return a concise reason and the existing settings mechanism for a legitimate exception.

Pass the admitted absolute path to the executor so the checked target and `FILE_CONTENT` refer to the same path. This boundary applies to the **execute-file path parameter**. Preserve `capy_index`'s current explicit absolute-file admission contract; directory ingestion applies its own traversal-root containment. Neither rule restricts all filesystem accesses made by arbitrary submitted code. The concurrent replacement limitation is recorded as [D2](upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads).

### 3.3 Guidance state filenames — S3

Use one session-to-filename helper for guidance creation and reset. Keep ordinary bounded ASCII IDs compatible; map invalid, empty or oversized identifiers to a stable SHA-256-derived component. Never interpolate slashes, traversal segments, NULs or unbounded text into a path. Use a stable mapping across short-lived hook processes, not their changing parent PID. An empty ID may retain the current non-persisting guidance fallback.

### 3.4 Explicit redirects and subagent availability — H1/H2

Use `FormatBlock` for the main agent's curl/wget and inline-HTTP redirects, carrying guidance directly as the denial reason. Preserve safe file-download exceptions and comprehension-aware WebFetch guidance. Keep `FormatModify` for actual Agent/Task prompt updates, copying the original input fields.

Extend parsed hook events with `agent_id` and `agent_type`. An actual nonempty agent ID identifies a child context; a type alone does not establish one. In a child context with unverified capy tool availability, suppress capy-specific blocking redirects and unavailable-tool nudges. Explicit user security denies and matched asks still run first and remain enforceable. Main-agent behavior is unchanged apart from the explicit-deny transport above.

Injected subagent guidance should say to discover deferred capy schemas once if the host offers tool discovery, then use them. If discovery is unavailable or reports that the tools are absent, use native tools following the comprehension/extraction principle; do not prescribe retry loops. Keep Bash-to-general-purpose upgrades, but do not treat that type change as proof of MCP availability.

## 4. Execution and batch behavior

### 4.1 Preserve submitted commands — E1

Both batch paths send the original command unchanged to the executor. Combine captured stdout followed by stderr, adding a separating newline only when necessary; this is deterministic presentation, not a claim to reconstruct stream interleaving. Preserve partial output, serial cascading timeout skips, parallel per-command timeouts and command-order responses.

### 4.2 Working directories — E2

Separate the script location from the process working directory. Default execution cwd is the selected project for every runtime. Rust compiles in its temporary workspace and runs the resulting binary in the selected cwd. Runtime helpers that inspect project files, such as Elixir Mix detection, receive the effective execution cwd.

Add optional `cwd` to execute, execute-file and batch MCP requests. Resolve relative values against the selected project, require an existing directory, and pass the result through the execution request; never call process-wide `os.Chdir`. The override applies to every runtime, and once to the entire batch. It changes execution location only: database target, key ownership, policy origin, vault scope, and execute-file `path` resolution remain tied to the server's selected project. Absolute external cwd values remain possible because execute already runs arbitrary code; they are not a way to bypass execute-file path admission.

Do not infer cwd from the newest Claude/Codex transcript or plugin cache. Document the change for scripts that previously wrote relative outputs into a temporary directory; temporary script files and safe temporary environment variables remain isolated.

### 4.3 Background output — E3

At background return, atomically snapshot the retained stdout/stderr and switch both buffers to drain-and-discard mode. Keep the read ends open and consume future writes without retaining bytes. This avoids both child pipe failure and unbounded Go heap growth. Foreground hard-cap behavior stays unchanged.

The process waiter owns eventual temporary-directory cleanup and PID untracking after a detached child exits, including shutdown-triggered termination. Handle the exit/timeout race without double waits or unsafe PID reuse. Test bounded retained memory, continued child progress and eventual cleanup under the race detector.

This proposes superseding the accepted memory-accumulation decision in [ADR-005](../../../adr/005-background-mode-output-streams.md). A discard-capable writer keeps Go's existing pipe ownership intact; it does not require manually replacing pipe descriptors. Task 9 must record that changed rationale while preserving the historical decision.

### 4.4 Batch query scope and provenance — B1/B2

| Input | Contract |
|---|---|
| Omitted or empty `queries` | Execute and index; return command/section inventories and the exact source label without running retrieval. |
| Omitted `query_scope` or `batch` | Search only this batch's exact source label, preserving today's behavior. |
| `query_scope: "global"` | Search durable and ephemeral knowledge in the selected knowledge DB, including this batch. No vault federation and no cross-project DB discovery. |
| Invalid scope | Error before any command executes. |

A supplied malformed `queries` value is also an error before execution. Preserve existing support for serialized JSON arrays, but distinguish an omitted/empty valid array from failed coercion; failed parsing must not silently become an indexing-only call.

Always label the selected scope. `global` must explicitly include ephemeral sources: merely removing the source filter would reapply capy's defaults and hide the newly executed batch.

The existing source label derives from truncated command labels. Simultaneous requests with the same resulting label can overwrite one another; batch scope here means that exact source label, not a new guarantee of request isolation. The source-identity change is recorded as [D3](upstream-audit.md#d3-concurrent-batch-source-label-collisions).

Provide a command inventory with sanitized previews, at most 500 UTF-8 bytes per command and 4,096 bytes for the entire inventory including headings/markers. Include an omitted-entry count when capped. Use safe Markdown formatting and avoid breaking fences with user input. Do not prepend source-code echoes to ordinary execute/execute-file responses. Continue counting raw captured work as sandbox bytes and complete formatted responses as returned bytes; echoed input is not newly sandboxed output.

## 5. Retrieval and ingestion

### 5.1 Literal source selection — R1

Escape backslash first, then `%` and `_`, and declare the same SQL escape character in `knowledgeFilterClauses`. Surround the bound escaped value with substring wildcards. Exact mode continues to use equality. Retain explicit-source bypass of kind filtering. The clause reaches porter, trigram and fallback searches through the shared knowledge corpus; do not put knowledge-label policy in `internal/retrieval` or change vault project matching.

### 5.2 Plaintext byte limits — R2

Every plaintext strategy must emit chunks with content at most `MaxChunkBytes` (4,096 bytes), preserving UTF-8 validity for valid input: small line counts, blank-line sections, overlapping line groups and oversized single lines. Prefer existing line boundaries, then whitespace where useful, then a UTF-8 boundary. Preserve all non-whitespace content and deterministic titles; normal line groups retain their two-line overlap without multiplying oversized-line content unnecessarily. Make truncated titles UTF-8 safe as part of this path. Cover empty/whitespace-only input and `chunkContent`'s final fallback so it cannot restore the entire oversized plaintext after splitting returns no chunks.

This is a plaintext fix, including JSON's fallback-to-plaintext path. It does not redesign markdown/JSON chunking or the vault chunk format. Existing same-hash sources are not automatically rewritten. New or changed indexing uses the cap; users can explicitly remove/reindex an old oversized source. Do not bump the schema or silently rewrite all persistent knowledge on startup.

### 5.3 Shared file and directory ingestion — I1

Add `internal/knowledge` for filesystem ingestion orchestration shared by MCP and CLI. It owns path admission, bounded reads, traversal and aggregate outcomes. It receives the selected project, source-size limit, policy snapshot and store; `internal/store` remains responsible for sanitization, hashes, source kind, chunking and writes. Avoid constructing an MCP server just to implement a CLI command.

Refactor the existing single-file path through this helper before extending it. Preserve descriptor-bound regular-file checks and nonblocking admission, and cap the actual read at the source limit plus one byte so growth after stat cannot bypass the limit. Denied files are never opened for content. Every indexed file remains a durable, file-backed source with stale refresh support.

Directory ingestion walks in deterministic lexical order and writes one file at a time. Defaults: depth 5 (root depth 0), 200 eligible files, no symlink following, and extensions `.md`, `.mdx`, `.txt`, `.json`, `.yaml`, `.yml`, `.toml`, `.ts`, `.tsx`, `.js`, `.jsx`, `.py`, `.rs`, `.go`, `.sh`. Hard ceilings: depth 20, 1,000 files, 20,000 visited entries and 32 MiB admitted content per request. Bounds are enforced during traversal/read, not after collecting the tree. Cancellation stops further work and returns a marked partial outcome.

Read directory entries incrementally within the remaining entry budget, then sort an admitted directory's entries. If that directory cannot be fully enumerated within the budget, stop and report the entry cap instead of loading an unbounded list or selecting a filesystem-order-dependent prefix. Count denied/skipped entries toward traversal work even though they do not consume the eligible-file allowance.

Prune directory components `.git`, `.hg`, `.svn`, `.capy`, `.aws`, `.ssh`, `node_modules`, `vendor`, `dist`, `build`, `.next`, `coverage`, `.venv`, `venv`, `__pycache__`, `target`, `.cache`, and filenames `.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa`, `id_ed25519`, `credentials`. A non-Git tree is supported with those exclusions. File roots retain their existing explicit-path behavior; recursive defaults do not silently apply to a directly named file.

Each source label defaults to its canonical absolute file path. An explicit directory `source` becomes a prefix followed by the relative path, not one label overwritten by every file. Repeated ingestion deduplicates per file. Removed/excluded files do not implicitly delete previously indexed sources; cleanup remains explicit.

Return indexed/unchanged, denied, skipped, failed and byte/chunk counts; state which bound stopped the walk and whether the scan completed. Report at most ten individual failure details and an omitted count. An invalid root is a tool error; partial progress is explicitly marked, and the CLI returns nonzero for operational failures/cancellation. Intentional filters are not errors; reaching an operator-supplied bound is a successful but incomplete scan.

### 5.4 Traversal controls and ignore rules

Expose `max_depth`, `max_files`, `extensions`, `include`, `exclude`, `respect_gitignore` and `follow_symlinks`. Validate all controls before indexing: depth is an integer in 0–20 and file count in 1–1,000. Empty/omitted extensions use the default list; nonempty extensions replace that list and are normalized to lowercase leading-dot forms. Empty/omitted includes select everything admitted by the other filters; excludes add to the mandatory defaults. User include/exclude patterns match entire root-relative POSIX-style paths, supporting `*`, `?`, and `**`; reject absolute paths, `..` components and unsupported pattern syntax instead of approximately interpreting it. Default directory exclusions match path components. Includes cannot override mandatory exclusions.

With `respect_gitignore: true` (default) in a Git worktree, use the existing Git executable's ignore evaluator on bounded, NUL-delimited batches, including ancestor/nested rules and negation. Deliberately apply ignore rules to tracked files too. Clear inherited repository-routing environment variables so the requested worktree determines the lookup. Do not shell-interpolate filenames. A Git lookup failure stops the scan with a visible partial/error result; it does not silently turn ignores off. Outside a Git worktree, state that Git ignores were not applied. This avoids advertising a home-grown subset as Git semantics.

Opt-in symlink following must stay within the canonical traversal root, detect visited-target cycles and deduplicate aliases. Recheck Read denies for the canonical root and every file. The default skips both file and directory symlinks. Source-size, entry, depth and aggregate-byte limits apply equally to followed targets.

## 6. Knowledge CLI — C1

Add `capy index <path>` and `capy search <query...>`. Both use the existing persistent `--project-dir`, `loadKnowledgeTarget`, `ResolveStoreKey` and captured-key store construction. The project is selected independently of the indexed file/directory; never choose another database merely because the input path points outside the current checkout.

`index` shares file/directory options and labels with MCP. Relative input paths are resolved against the selected project, as in MCP, and the help text states this. Close the store through the established checkpoint path and return meaningful errors without exposing credentials.

`search` searches **knowledge only**, with `--source`, `--limit`, `--type` (`code`/`prose`) and repeatable `--include-kind` for durable/ephemeral. Default is durable. Use the same retrieval engine, literal source filter and Read-deny callback during stale refresh as MCP. An explicit source retains its kind-bypass semantics. `capy vault search` remains the session CLI; do not imply terminal knowledge search federates vault results. It has no MCP context throttle, but enforces a result limit of 1–100 and bounded text snippets. Empty results are successful; invalid options or storage failures are errors.

The CLI type flag uses existing backend filtering without adding a parameter to the MCP schema. Task 15 amends [ADR-012](../../../adr/012-contenttype-internal-only.md) to record that narrower terminal use case while preserving its MCP decision.

## 7. Fetch freshness and local visibility

### 7.1 Per-call TTL — F1

Add optional `ttl` in milliseconds. Omission uses configured `store.cache.fetch_ttl_hours`; zero bypasses cache; `force: true` also bypasses regardless of TTL. Accept nonnegative integral JSON numbers and decimal integer strings that fit a Go duration; reject negative, fractional, nonfinite, overflow, null and unrelated types. Parse before cache/network/index work. No per-item overrides in a batch.

Normalize `force` from a boolean or trimmed case-insensitive `true`/`false` string; malformed supplied values fail instead of silently becoming false. Preserve already-supported serialized `requests` arrays. Do not use truthiness, where the string `false` becomes true.

Single and batch cache checks use one policy. Keep label+URL identity, requested-kind compatibility and SSRF behavior. Responses show the effective freshness window. This is a read-time cache decision, not a retention change: an ephemeral source may be evicted before a long requested freshness window; `kind: "durable"` remains the way to request durable knowledge.

### 7.2 Cache and throttle visibility — O1

Record hits and fetch attempts once per eligible URL, including force/TTL-zero bypasses; malformed/rejected URLs are not attempts. A network failure remains an attempted miss. Keep counters under the existing stats mutex and reset/snapshot them together. Show hit/miss counts and a rate with a defined zero-attempt representation; preserve estimated cache-byte savings as an estimate. No hosted analytics, pricing, token-cost or tracing subsystem is introduced.

For valid `capy_search` requests, report call count, current effective result limit, calls until taper/block and remaining time until reset, including the first call, empty-store and partial-result responses. Keep the current 60-second/3/8 policy. Invalid requests do not consume a count. State that the budget belongs to the shared MCP server when no agent identity is available. Per-agent isolation is [deferred D1](upstream-audit.md#d1-per-agent-search-budgets), not delivered by this feature.

## 8. Alternatives and assumptions

### Rejected alternatives and selected direction

| Direction | Value and feasibility | Decision |
|---|---|---|
| Fixes only | Smallest review surface, but leaves useful directory/CLI/freshness capabilities behind. | Rejected after the user selected fixes plus compatible additions. |
| Selective behavioral ports | Uses existing Go seams, preserves capy's independent storage/security contracts and permits small releaseable slices. | Selected. |
| Broad TS architecture parity | Adds event/snapshot databases, platform bridges, analytics, JS lifecycle assumptions and duplicate session ownership. | Rejected: large migration cost with no matching capy requirement. |

### Assumptions

Verify these during implementation:

1. A 4,096-byte plaintext cap improves bounded retrieval without unacceptable quality loss; compare fixtures and quality benchmarks before release.
2. The proposed directory ceilings fit explicit documentation/source ingestion. Measure representative trees and report cap outcomes; do not silently increase bounds to make a test pass.
3. Changing non-shell default cwd is useful but observable. Test Go, Python/JS where installed, Rust run/compile separation and Elixir wrapping; document migration for relative writes.
4. Host hook payloads provide `agent_id` for child contexts. Verify actual supported-host fixtures; do not infer tool availability from a running server or from `agent_type` alone.
5. Git 2.34.1-compatible ignore flags are sufficient; no Go module is added. An unavailable Git executable is only relevant when applying Git ignores in a repository.
6. No stable per-agent stdio request identity has been established. This is a limitation to measure, not an assumption that a transport session ID solves it.

## 9. Not Doing

- CI/bundle/docs-only upstream ports and unsupported operating systems/runtimes/hosts: excluded by request.
- Upstream analytics/pricing/hosted Insight or event/directive replay: different product and persistence model.
- New Codex hooks, new languages or a transcript-based cwd resolver: existing integrations do not require those subsystems.
- Knowledge/vault schema changes, key-resolution changes, ranking-formula changes or blanket reindexing: preserve capy's established invariants.
- Unconditional execution-source echoes or command-length output guesses: these conflict with capy's context-routing purpose.
- OS-level sandboxing of submitted code: this feature strengthens explicit tool inputs and existing policy enforcement.

Deferred work is listed separately in the audit; it is not hidden in these exclusions.

## 10. Rollout, reversibility and verification

Implement as the [small task slices](tasks.md). Additive parameters preserve omitted-input defaults except the intentional non-shell cwd correction, literal source wildcard correction, explicit execute-file containment, and reliable main-agent redirect denial. Document those four behavior changes prominently.

No data-format rollback is necessary. New CLI commands and directory source labels use existing encrypted source rows. Already-indexed chunks remain valid for older binaries. Rolling back cwd/path behavior restores prior semantics, so release notes must make that tradeoff visible.

Update generators and their committed counterparts together whenever routing/setup artifacts change: `internal/platform/routing.go` with `.capy/AGENTS.md`, and any affected wrapper/config pair listed in repository `AGENTS.md`. Run whole-file artifact and merge-idempotence checks; do not patch just the generated copy.

Verification requires behavioral tests for the selected fixes, real CLI/MCP round trips, focused race checks for concurrent execution/cache counters, the full FTS5 suite, and quality/performance benchmarks for chunking/search/executor work. Use synthetic knowledge and vault keys and temporary homes. The documentation-only audit itself does not establish runtime correctness or independent design approval.
