# Design: Upstream sync through context-mode 0dfbe8d

> Status: implementation in progress; Task 1 complete, remaining tasks proposed
> Created: 2026-10-10
> Feature: `upstream-sync-v1.0.169`
> Upstream range: `f8d46390613f068f232eb14ad91804841c64bdfa..0dfbe8de71abcb637a07dd6444bee5823c3186fc`
> Capy inspected at: `c0bfff2800614b47a163949eef34645f854fff3a`
> Reconciled against: `d182825c824cfd154926d9f59ba7af6d8821f225`; runtime unchanged from the baseline
> Review resolution: [combined findings and evidence](.reviews/reconciliation-2026-10-10.md)
> Readiness check: [final author re-read](.reviews/readiness-2026-10-10.md)
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

Preserve mandatory encryption, FTS5 build tags, pool-close-before-checkpoint ordering, source-kind checks, strict fetch SSRF behavior, sanitized content hashing, source-size limits, per-source diversification and the existing RRF/rerank formulas. Two additive knowledge-store migrations are now required: a fetch-validation timestamp and a persistent stale-check scheduling field/index (§5.5, §7.1). Neither changes vault format, existing timestamp representations or source kinds. No module upgrade is required.

## 3. Security and hook behavior

### 3.1 Shell policy evaluation — S1

Use one command-element scanner for both evaluators. Recognize executable segments separated by newline/CR, `;`, pipes, `&&`, `||` and background `&`, plus nested `$()` and backtick substitutions. Track escape parity and shell quote contexts rather than inspecting only the preceding byte. A separator that is escaped or quoted is data; redirection such as `2>&1` is not a background-command boundary.

Do not treat literal single-quoted or quoted-heredoc text as commands. Double-quoted substitutions still execute. Unquoted heredoc bodies are data except for their shell expansions: inspect executable substitutions there, but do not interpret ordinary body lines as commands. Arithmetic syntax is not itself a command, but nested substitutions inside it are. Cover these distinctions before changing policy decisions; the TS implementation is a case source, not an authoritative shell parser.

Any denied element denies the whole request. Preserve ordered settings precedence for ask/allow; within a policy, an explicit ask wins, and allow requires every executable element to match. Otherwise the full evaluator returns ask; the deny-only evaluator still allows non-denied commands. Capy's existing hook only surfaces asks backed by a matched pattern; do not silently convert its unmatched default into a new mandatory approval mechanism.

Retain capy's existing case-sensitive policy matching. Do not port upstream's blanket macOS case-folding assumption into command matching or filesystem admission.

Scanner limits are 1 MiB of input, 64 nested substitution/quote/heredoc frames, 4,096 emitted executable elements, and an aggregate visit budget of eight times the input byte length plus 4,096 steps. Bound recursive extraction by that aggregate budget rather than rescanning without accounting. Exceeding any limit produces a typed evaluation error, not a partial list. The MCP shell, batch, and extracted non-shell paths return an error result before spawning anything; the hook emits `FormatBlock` with the exceeded limit as reason, regardless of whether a deny pattern matched. This is distinct from the existing unmatched-ask outcome. This remains static policy matching, not a complete shell interpreter or a security boundary for arbitrary code in other languages.

### 3.2 Prepared Read rules — prerequisite for S2/I1/C1

Replace origin-free glob arrays on production read paths with a prepared file policy retaining each rule's action, source settings path, source anchor, selected project, working directory and home directory. Parse anchor syntax before path cleaning. The currently documented host anchors are `//` for filesystem root, `~/` for home, `/` for the settings source and unprefixed/`./` for cwd; project/local settings use the primary working directory, while user settings use the settings file's directory. [Host permission reference](https://code.claude.com/docs/en/permissions#read-and-edit).

For capy's MCP and CLI file operations, both the primary directory and rule cwd are the selected project; an execution `cwd` override never changes them. Hooks retain project identity for settings/state and use the validated payload cwd for cwd-relative rules. Resolve patterns to anchored candidates, so a relative deny matches a file whether the caller supplies a relative name, a walker supplies an absolute name, or stale refresh supplies its stored path. Preserve lexical and physical paths, including symlink-before-`..` resolution; denies apply to either. For an external grant, require its normalized allow to cover both the requested path and physical target. This intentionally rejects an allowed-looking alias to an ungranted target.

Support the existing `*`, `**`, `?` grammar plus escaped literals. An unprefixed pattern with no slash, such as `*.env`, matches a basename at any depth under its cwd anchor; explicitly anchored path patterns keep their path scope. Do not claim complete host policy emulation: bracket classes, leading negation and other unsupported constructs must produce a policy diagnostic and fail admission rather than becoming a permissive approximation. A bare `Read` rule means every read. Continue loading capy's existing local/shared/user settings locations; managed/CLI/session host policy discovery is not added by this sync.

**Legacy single-slash compatibility:** denies written as `/absolute/**` currently mean filesystem-root paths in capy. Prepare both the host-anchored interpretation and that legacy absolute interpretation for such denies, and warn once per rule/source that `//absolute/**` removes the ambiguity. This union may deny more, but cannot silently remove an existing deny. New external allows use only the documented anchor interpretation; no legacy alias may broaden a grant. Expose the matched rule/source safely in diagnostics. Invalid selected policy data fails file admission; it is not an empty allow/deny list.

Task 2a delivers this policy through existing direct-file and stale-refresh paths. Tasks 2, 12–15 reuse it. The old exported matcher may remain for compatibility, but production callers must not bypass the prepared policy. Tests cover local/user origins, every anchor, relative and absolute callers, physical aliases, legacy denies, unsupported syntax, CLI, walking and stale refresh.

### 3.3 Execute-file path admission — S2

Resolve an explicit `path` relative to the server's selected project directory once; absolute inputs remain absolute. Run Read denies against raw, lexical and physical candidates, retaining symlink-before-`..` handling. Check ordinary containment using path-component relationships, not string-prefix similarity.

In-project paths are admitted subject to the prepared Read policy; an external path requires its normalized Read allow (§3.2). A deny always wins. Matching a project-local symlink alias is insufficient to authorize an external real target. Failure to resolve the configured project or an existing target is an error, not a reason to silently disable containment. Missing paths produce normal file errors. Return a concise reason and the existing settings mechanism for a legitimate exception, using `Read(//absolute/path/**)` in examples.

Pass the admitted absolute path to the executor so the checked target and `FILE_CONTENT` refer to the same path. This boundary applies to the **execute-file path parameter**. Preserve `capy_index`'s current explicit absolute-file admission contract; directory ingestion applies its own traversal-root containment. Neither rule restricts all filesystem accesses made by arbitrary submitted code. The concurrent replacement limitation is recorded as [D2](upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads).

### 3.4 Guidance state filenames — S3

Use one session-to-filename helper for guidance creation and reset. Preserve IDs of 1–128 ASCII letters, digits, dots, underscores or hyphens; map any other nonempty identifier to `sha256-` plus its full SHA-256 hex digest. An empty ID keeps the current non-persisting guidance fallback. Never interpolate raw separators, NULs or unbounded text into a path. Use a stable mapping across short-lived hook processes, not their changing parent PID. New observation state uses this component helper but its own file format/lifecycle; existing guidance files are not silently repurposed.

### 3.5 Explicit redirects and subagent availability — H1/H2

Use `FormatBlock` for the main agent's curl/wget and inline-HTTP redirects, carrying guidance directly as the denial reason. Preserve safe file-download exceptions and comprehension-aware WebFetch guidance. Keep `FormatModify` for actual Agent/Task prompt updates, copying the original input fields.

Extend parsed hook events with `agent_id`, `agent_type` and `cwd`. An actual nonempty agent ID identifies a child context; a type alone does not establish one. Parse payload cwd as working-directory context, with environment/CLI directory fallback. Select the project once, before loading policies: explicit `--project-dir` wins, then `CLAUDE_PROJECT_DIR`, then project detection anchored at payload cwd, then process cwd. Do not load rules for one project and overwrite only the routing directory afterward. This closes a pre-existing capy gap; upstream's payload-cwd helper already existed at `f8d4639`.

Normalize an explicitly selected directory against process cwd and require an existing directory. Ignore a malformed/nonabsolute payload cwd with a diagnostic and use the stated fallback. An invalid explicit selection is never replaced by another project: PreToolUse emits a structured block; other hook events report the error and skip state mutation. Policy-preparation failures on an affected PreToolUse file operation also produce structured blocks, not merely a nonzero process exit that the wrapper intentionally masks. Anchored detection must use explicit path arguments/child command directories, never temporary process-wide cwd or environment changes.

Child status does **not** imply missing tools: inherited tool pools can include MCP. Conversely, reading two agent-definition files cannot establish effective availability because managed, CLI and plugin definitions also exist. [Host subagent scope and tool controls](https://code.claude.com/docs/en/sub-agents#choose-the-subagent-scope). Do not implement the proposed two-file lookup as an authority or infer availability solely from `agent_type`.

Use recent per-child observations as **one-use redirect evidence**, not permanent availability. A successful `PostToolUse` event for `capy_execute` or `capy_fetch_and_index` records that exact tool for the stable session/agent pair; other tools do not mint redirect evidence. Error/canceled calls do not renew evidence. Each tool observation expires 60 seconds after its own success, so a different tool's success cannot extend it; reject future timestamps and malformed state. Before a capy-specific child block, atomically consume all currently observed alternatives for that child. A successful retry can renew them; if it fails, the next native call falls back instead of being trapped by the same old observation. Failed reads/writes/locking also produce advisory fallback. Actual user security denies and matched asks always run first and never consume this evidence.

Only an observed `capy_execute` can redirect arbitrary curl/wget or inline HTTP, because those commands can carry methods/headers/body that fetch-and-index cannot reproduce. An indexing-eligible WebFetch can use observed `capy_fetch_and_index` or `capy_execute`. Search and execute-file are not substitutes. A Git-platform comprehension URL keeps native CLI guidance; a child is not required to call a capy tool that refuses that URL. For unknown child availability, give discovery/native-tool advice without a capy-specific block. Main-agent redirect behavior stays as specified above.

Task 5a owns one `.capy/tool-observations.json` per selected project, capped at 64 KiB serialized and 128 entries keyed by the pair of stable session/agent identifiers. Store only safe identity components, known tool names and timestamps. Missing/PID-fallback identities remain unknown and do not persist observations. Expire observations before admitting new ones; evict oldest entries until both bounds hold. Serialize bounded read/modify/write with one separate stable project-state lock file and atomic data-file replacement; cap lock waiting at 20 ms and fall back to unknown if unavailable. Never unlink a lock file while another hook might hold it. SessionEnd best-effort removes only its session's entries after parsing identity; it must not open/checkpoint the knowledge DB or disturb live siblings. If SessionEnd never arrives, expiry and the fixed file/entry bounds still hold, so no directory-wide orphan sweep is needed. The present SessionEnd handler is a no-op; cleanup and payload plumbing are new work, not an existing mechanism to assume.

This deliberately leaves a child's first native call unblocked when no usable capy capability has been observed, even for an inherit-all definition. It avoids a deadlock without claiming perfect first-call flood prevention. Establishing authoritative pre-call tool pools across all host definition sources is a separate follow-up, D5.

Injected subagent guidance should say to discover deferred capy schemas once if the host offers tool discovery, then use them. If discovery is unavailable or reports that the tools are absent, use native tools following the comprehension/extraction principle; do not prescribe retry loops. Keep Bash-to-general-purpose upgrades, but do not treat that type change as proof of MCP availability.

## 4. Execution and batch behavior

### 4.1 Preserve submitted commands — E1

Both batch paths send the original command unchanged to the executor. Combine captured stdout followed by stderr, adding a separating newline only when necessary; this is deterministic presentation, not a claim to reconstruct stream interleaving. Preserve partial output, serial cascading timeout skips, parallel per-command timeouts and command-order responses.

### 4.2 Working directories — E2

Separate the script location from the process working directory. Default execution cwd is the selected project for every runtime. Rust compiles in its temporary workspace and runs the resulting binary in the selected cwd. Runtime helpers that inspect project files, such as Elixir Mix detection, receive the effective execution cwd.

Add optional `cwd` to execute, execute-file and batch MCP requests. Resolve relative values against the selected project, require an existing directory, and pass the result through the execution request; never call process-wide `os.Chdir`. The override applies to every runtime, and once to the entire batch. It changes execution location only: database target, key ownership, policy origin, vault scope, and execute-file `path` resolution remain tied to the server's selected project. Absolute external cwd values remain possible because execute already runs arbitrary code; they are not a way to bypass execute-file path admission.

Do not infer cwd from the newest Claude/Codex transcript or plugin cache. Document the change for scripts that previously wrote relative outputs into a temporary directory; temporary script files and safe temporary environment variables remain isolated.

For Go, project cwd deliberately changes main-module/workspace discovery. Verify project-local imports, `replace`, automatic vendor mode and toolchain selection using isolated fixtures; preserve the existing safe-environment removal of `GOFLAGS` rather than treating inherited `-mod` flags as supported. The `go run <absolute temporary script>` invocation remains unchanged.

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

Always label the selected scope. `global` must explicitly include ephemeral sources: merely removing the source filter would reapply capy's defaults and hide the newly executed batch. Every hit includes its own source label before title/snippet, including global hits whose title matches a current-batch title.

The existing source label derives from truncated command labels. Simultaneous requests with the same resulting label can overwrite one another; batch scope here means that exact source label, not a new guarantee of request isolation. The source-identity change is recorded as [D3](upstream-audit.md#d3-concurrent-batch-source-label-collisions).

Persist one sanitized, whitespace-normalized command preview (at most 500 UTF-8 bytes including its truncation marker) under each indexed command section. Use safe Markdown/plain-text formatting so submitted delimiters cannot create fake provenance sections. This ports the stored-output part of `f7af3ca`, as well as the immediate inventory from `c1030ca`. A later source-filtered query can retrieve the producing command even when the vault is disabled; do not promise every arbitrary snippet repeats the preview. Skipped commands are marked as not executed. Source-size admission counts this added metadata; it cannot displace/truncate captured command output silently.

The **entire serialized MCP tool result** is capped at 81,920 bytes, excluding the JSON-RPC envelope. Include summaries, source/scope labels, both inventories, query headings/hits, errors, terms and truncation notices in this budget. Measure JSON escaping as well as UTF-8 length, reserving space for a source selector and omitted counts before adding optional sections. The command inventory has an additional 4,096-byte cap. The section inventory shows at most 40 entries with 160-byte titles, within 8,192 bytes; obtain bounded metadata rather than loading every chunk's content solely to build an inventory. Query output consumes the remaining budget, and one aggregate notice replaces arbitrarily many omitted-query notices. Cap displayed arbitrary hit labels/queries while retaining the exact bounded batch source for follow-up.

Preserve the full admitted indexed content and complete executed commands. On an unchanged batch, use stored section totals for summaries and omitted counts; `IndexResult.AlreadyIndexed` does not carry a populated `TotalChunks`. Do not prepend source-code echoes to ordinary execute/execute-file responses. Sandbox byte counters count captured stdout/stderr only; indexing counts stored payload, including provenance; returned-byte accounting includes the complete formatted result. Regression fixtures include the 8,000-heading case from the review probe, repeated unchanged batches, indexing-only calls, identical titles from different sources, later provenance retrieval, long labels/queries, escaping and Unicode.

## 5. Retrieval and ingestion

### 5.1 Literal source selection — R1

Escape backslash first, then `%` and `_`, and declare the same SQL escape character in `knowledgeFilterClauses`. Surround the bound escaped value with substring wildcards. Exact mode continues to use equality. Retain explicit-source bypass of kind filtering. The clause reaches porter, trigram and fallback searches through the shared knowledge corpus; do not put knowledge-label policy in `internal/retrieval` or change vault project matching.

### 5.2 Plaintext byte limits — R2

Every plaintext strategy must emit chunks with content at most `MaxChunkBytes` (4,096 bytes), preserving UTF-8 validity for valid input: small line counts, blank-line sections, overlapping line groups and oversized single lines. Prefer existing line boundaries, then whitespace where useful, then a UTF-8 boundary. Preserve all non-whitespace content and deterministic titles; normal line groups retain their two-line overlap without multiplying oversized-line content unnecessarily. Make truncated titles UTF-8 safe as part of this path. Cover empty/whitespace-only input and `chunkContent`'s final fallback so it cannot restore the entire oversized plaintext after splitting returns no chunks.

This is a plaintext fix, including JSON's fallback-to-plaintext path. It does not redesign markdown/JSON chunking or the vault chunk format. Existing same-hash sources are not automatically rewritten. New or changed indexing uses the cap; users can explicitly remove/reindex an old oversized source. Do not bump the schema or silently rewrite all persistent knowledge on startup.

Markdown single paragraphs/fences and structured JSON primitive leaves can still exceed 4,096 bytes. The source limit (2 MiB by default) bounds **admitted input**, not necessarily the resulting chunks: sanitization, JSON escaping/pretty-printing and overlap can expand stored content. Batch markdown and converted HTML follow those paths. This is a deliberate narrow port of upstream `6f699fa`, not a universal chunk-size or DB-size invariant. Splitting those representations while preserving fence/key-path meaning needs separate quality fixtures (D4). The complete response budget in §4.4 applies regardless of chunk type.

### 5.3 Shared file and directory ingestion — I1

Add `internal/knowledge` for filesystem ingestion orchestration shared by MCP and CLI. It owns path admission, bounded reads, traversal and aggregate outcomes. It receives the selected project, source-size limit, policy snapshot and store; `internal/store` remains responsible for sanitization, hashes, source kind, chunking and writes. Avoid constructing an MCP server just to implement a CLI command.

Refactor the existing single-file path through this helper before extending it. Preserve descriptor-bound regular-file checks and nonblocking admission, and cap the actual read at the source limit plus one byte so growth after stat cannot bypass the limit. Apply the same actual-read limit in `store.fileChangedSince`, retaining cached content and logging a skipped refresh if the bound is exceeded. A dependency-free low-level bounded-reader package may be shared by store and knowledge; never introduce a store-to-knowledge import cycle. Denied files are never opened for content. Every indexed file remains a durable, file-backed source with stale refresh support through the prepared policy.

Directory ingestion walks in deterministic lexical order and writes one file at a time. Defaults: depth 5 (root depth 0), 200 eligible file attempts, no descendant symlink following, and extensions `.md`, `.mdx`, `.txt`, `.json`, `.yaml`, `.yml`, `.toml`, `.ts`, `.tsx`, `.js`, `.jsx`, `.py`, `.rs`, `.go`, `.sh`. Hard ceilings: depth 20, 1,000 eligible file attempts, 20,000 visited entries and 32 MiB of actual file-content reads per request. Count read bytes even for unchanged files, failed indexing, or growth rejection, including any oversize-detection byte; this is not just a successful-indexing allowance. Filtered/denied entries consume traversal work but no file-attempt slot; an admitted open/read attempt consumes one slot even if it fails. Bounds are enforced during traversal/read, not after collecting the tree. Cancellation stops further work and returns a marked partial outcome.

Limit each read by both the per-source limit plus one detection byte and the remaining request allowance. If the allowance cannot establish a complete admissible file, stop before indexing that partial content and report the byte cap distinctly from a per-source size error. A complete empty scan is successful, and all-filtered/all-denied counts are explicit. Both operator limits and hard ceilings yield successful but incomplete results; operational errors/cancellation yield a marked partial tool error and nonzero CLI exit while retaining prior committed files.

Read directory entries incrementally within the remaining entry budget, then sort an admitted directory's entries. If that directory cannot be fully enumerated within the budget, stop and report the entry cap instead of loading an unbounded list or selecting a filesystem-order-dependent prefix. Count denied/skipped entries toward traversal work even though they do not consume the eligible-file allowance.

Prune directory components `.git`, `.hg`, `.svn`, `.capy`, `.aws`, `.ssh`, `node_modules`, `vendor`, `dist`, `build`, `.next`, `coverage`, `.venv`, `venv`, `__pycache__`, `target`, `.cache`, and filenames `.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa`, `id_ed25519`, `credentials`. A non-Git tree is supported with those exclusions. File roots retain their existing explicit-path behavior; recursive defaults do not silently apply to a directly named file.

Each source label defaults to its canonical absolute file path. An explicit directory `source` produces `source + ":" + relativePath`, with the path normalized to forward slashes relative to the canonical ingestion root. Deduplication is by complete label and sanitized hash, not inode: rerunning with the same prefix/path deduplicates; changing the prefix creates another source. Reusing one prefix for two roots can intentionally collide on equal relative paths, so the caller owns that namespace. Removed/excluded files do not implicitly delete previously indexed sources; cleanup remains explicit.

Return indexed/unchanged, denied, skipped, failed and byte/chunk counts; state which bound stopped the walk and whether the scan completed. Report at most ten individual failure details and an omitted count. An invalid root is a tool error; partial progress is explicitly marked, and the CLI returns nonzero for operational failures/cancellation. Intentional filters are not errors; reaching an operator-supplied bound is a successful but incomplete scan.

### 5.4 Traversal controls and ignore rules

Expose `max_depth`, `max_files`, `extensions`, `include`, `exclude`, `respect_gitignore` and `follow_symlinks`. Validate all controls before indexing: depth is an integer in 0–20 and file count in 1–1,000. Empty/omitted extensions use the default list; nonempty extensions replace that list and are normalized to lowercase leading-dot forms. Empty/omitted includes select everything admitted by the other filters; excludes add to the mandatory defaults. User include/exclude patterns match entire root-relative POSIX-style paths, supporting `*`, `?`, and `**`; reject absolute paths, `..` components and unsupported pattern syntax instead of approximately interpreting it. Default directory exclusions match path components. Includes cannot override mandatory exclusions.

With `respect_gitignore: true` (default), discover the Git worktree containing the **canonical ingestion root**, even when it differs from the selected capy project. Use its ignore evaluator on bounded, NUL-delimited batches, including ancestor/nested rules and negation. Deliberately apply ignore rules to tracked files too. Clear inherited repository-routing environment variables so that worktree determines the lookup. This affects ignores only: the selected capy project still owns credentials, storage and prepared Read rules. Do not shell-interpolate filenames. A Git lookup failure stops the scan with a visible partial/error result; it does not silently turn ignores off. Outside a Git worktree, state that Git ignores were not applied. Prune descendant repositories/worktrees/submodules identified by a `.git` marker and count them as skipped; explicitly indexing that nested root is the way to select its own worktree. Do not cross Git boundaries during one walk, even with ignores disabled.

An explicitly supplied root may be a symlink: validate its requested and real paths through Read policy, then select its canonical directory. The follow flag governs **descendants**, preserving direct-file/root admission semantics. Task 13a adds opt-in descendant following within the canonical root, cycle detection and target deduplication; Task 13 first delivers the default non-following path.

For followed entries, apply mandatory exclusions and Read denies to both the encountered alias and canonical target. Selection globs/extensions, prefixed labels and descendant Git-ignore queries use the canonical target-relative path; an alias cannot rename a denied/ignored credential into eligibility. Git may check the symlink entry itself, but never receives a descendant spelling such as `alias/file`: `git check-ignore` rejects paths beyond a directory symlink. Also require the canonical target to stay in the selected ingestion worktree, outside nested Git roots. All depth, entry, file and byte bounds still apply. Test a canonical ignored target reached by an innocent alias, a symlink to `.capy`, cycles, nested roots and direct-root symlinks.

### 5.5 Bounded automatic stale checks

Directory ingestion makes an unbounded all-file refresh on every eligible search unacceptable. Task 12a adds `sources.file_check_seq INTEGER NOT NULL DEFAULT 0` and a partial index on `(file_check_seq, id)` for file-backed rows. A pass selects at most 32 oldest-checked rows and closes its query cursor before file I/O/writes. After each attempted file (including denied/missing/unchanged files), persist a new logical sequence greater than the current maximum; unprocessed rows do not advance. Reindexing a changed row carries scheduling state to its replacement source. This survives CLI restarts and does not repeatedly favor the first 32 paths. Concurrent server instances may duplicate a bounded check; no cross-process exclusion guarantee is promised.

Keep the five-second in-process cooldown and permit at most one active refresh per store instance, even when slow I/O outlasts the cooldown. Bound raw content reads per pass to `max(8 MiB, effectiveMaxSourceBytes + 1)`, with checked arithmetic and the extra detection byte counted. Reserve the bounded read allowance before opening each file; an eligible maximum-size file must fit as the first attempt. Start the 100 ms admission clock after the bounded metadata query; attempt the first eligible file unless canceled, then stop admitting further work when the budget expires. An in-progress regular-file operation may finish, so this is not an OS I/O deadline. Unattempted files do not advance. Retain old searchable content on a denied, failed or postponed refresh. No result promises that every backing file was just validated.

Allocate each completed-check sequence under the same short SQLite write transaction as its update, using the partial index to obtain the current maximum. Do not hold a transaction across filesystem I/O. Only update the source incarnation actually checked (or the replacement produced by that refresh); a concurrently removed/replaced row is skipped, not recreated or relabeled by the scheduling update. A scheduling-write failure is reported and stops that pass, rather than silently claiming progress. Source replacement preserves the scheduling value only for the same backing path; a different backing file starts unchecked.

Validate with 10,000 file-backed sources: metadata selection stays at 32 rows, at most 32 files are attempted, reads respect the byte budget, and repeated new CLI instances eventually check late-added and high-ID files. Measure unchanged/changed-tree search latency, memory and write overhead against the baseline in Task 20. Create the scheduling index only after its column exists; `schemaSQL` runs before migrations on legacy databases.

## 6. Knowledge CLI — C1

Add `capy index <path>` and `capy search <query...>`. Both use the existing persistent `--project-dir`, `loadKnowledgeTarget`, `ResolveStoreKey` and captured-key store construction. The project is selected independently of the indexed file/directory; never choose another database merely because the input path points outside the current checkout.

`index` shares file/directory options and labels with MCP. Relative input paths are resolved against the selected project, as in MCP, and the help text states this. Close the store through the established checkpoint path and return meaningful errors without exposing credentials.

`search` searches **knowledge only**, with `--source`, `--limit`, `--type` (`code`/`prose`) and repeatable `--include-kind` for durable/ephemeral. Default is durable. Use the same retrieval engine, literal source filter and Read-deny callback during stale refresh as MCP. An explicit source retains its kind-bypass semantics. `capy vault search` remains the session CLI; do not imply terminal knowledge search federates vault results. It has no MCP context throttle, but enforces a result limit of 1–100 and bounded text snippets. Empty results are successful; invalid options or storage failures are errors.

The CLI type flag uses existing backend filtering without adding a parameter to the MCP schema. Task 15 amends [ADR-012](../../../adr/012-contenttype-internal-only.md) to record that narrower terminal use case while preserving its MCP decision.

## 7. Fetch freshness and local visibility

### 7.1 Per-call TTL — F1

Add optional `ttl` in milliseconds, deliberately matching upstream and existing MCP timeout units. Omission converts configured `store.cache.fetch_ttl_hours` to milliseconds; zero bypasses cache; `force: true` also bypasses regardless of TTL. Accept nonnegative integral JSON numbers and decimal integer strings that fit a Go duration; reject negative, fractional, nonfinite, overflow, null and unrelated types. Parse before cache/network/index work. No per-item overrides in a batch.

Use the shared boolean contract (§7.3) for `force`. Preserve already-supported serialized `requests` arrays.

Add nullable `sources.fetch_validated_at_ms INTEGER`, exposed as optional metadata. Capture the per-URL validation time when the successful response body has been completely read and admitted, before conversion or the batch's serial indexing queue. Carry it with the content into the atomic store operation that indexes/deduplicates and records that timestamp. Queue/lock delay cannot make old bytes appear newly fetched. New content, unchanged content and requested-kind transitions all renew the marker on successful fetch+index. Failed HTTP/body/transform/store work does not renew it. Cache hits and ordinary searches do not renew it. Generic index calls never stamp it; a generic replacement clears it. Keep label+URL identity, kind compatibility and SSRF behavior. When concurrent fetches commit out of order, the timestamp must correspond to the content actually committed, not the maximum timestamp from a different response.

Freshness is `0 <= now_ms - validated_at_ms < ttl_ms`. Integer Unix milliseconds preserve sub-second behavior without changing the old second-resolution `indexed_at` format or its readers. An absent/invalid/future marker is a miss. Existing rows with NULL conservatively revalidate once after upgrade; do not infer validation from a label or a second-resolution timestamp. Persist the marker across reopen. Capture the success time through a clock seam and include 500 ms boundary tests.

An unchanged success returns **revalidated, unchanged**, with the stored chunk count, rather than claiming zero newly indexed sections; changed/new content reports its actual write outcome. Updating freshness alone does not advance `indexed_at` or alter retention/decay. Existing same-hash kind/access behavior remains; changed content still replaces the source as today. Thus an old ephemeral entry can be evicted after a successful unchanged revalidation: freshness and retention are intentionally independent, and durable retention still requires `kind: "durable"`.

The migration is additive and uses existing encrypted migration/open/close paths. Old binaries ignore the new column; old replacement writes leave it NULL and force one later revalidation. Test old-schema migration, rollback/reopen and both dedup branches. Task 16a delivers renewal under the configured default TTL; Task 16 adds the override and precision boundary cases.

### 7.2 Cache and throttle visibility — O1

Record a hit or attempted miss once per eligible URL, including force/TTL-zero bypasses. Malformed URLs, SSRF policy blocks, Git-platform CLI redirects and pre-dispatch cancellation count as neither. A real fetch that fails DNS/network, HTTP, body validation or subsequent indexing counts as one attempted miss. Carry a typed outcome until accounting rather than guessing from formatted error text. Keep counters under the existing stats mutex and reset/snapshot them together. Show `hits / (hits + attempted misses)` and `n/a` when that denominator is zero; preserve estimated cache-byte savings as an estimate.

Replace the current session-uptime-derived **TTL remaining** field with **Default fetch freshness** from configuration. Individual fetch responses state their own effective TTL; no single remaining lifetime exists for a mixed cache. At both savings-percentage display sites use one decimal, clamped to 99.9% whenever returned bytes are nonzero; 100.0% is reserved for positive processed bytes and zero returned bytes. Zero processed bytes displays 0.0%. This ports the separable display fix from `49fa9d2` and avoids claiming exact exclusion near the rounding boundary. The underlying savings formula is unchanged. No hosted analytics, pricing, token-cost or tracing subsystem is introduced.

For valid `capy_search` requests, report call count, current effective result limit, calls until taper/block and remaining time until reset, including the first call, empty-store and partial-result responses. Add `[search] window_ms`, `taper_after`, `block_after` through capy's existing TOML merge/validation flow. Defaults remain 60000/3/8. Require integers, `1000 <= window_ms <= 3600000` and `1 <= taper_after < block_after <= 10000`; explicit zero/invalid values fail config loading. Taper when count exceeds `taper_after`, block when it exceeds `block_after`, and reset at or after the configured window. All notices use the effective values. Invalid requests do not consume a count. No upstream environment aliases or per-request threshold overrides are added. State that the budget belongs to the shared MCP server; tunability mitigates fan-out but does not supply [D1](upstream-audit.md#d1-per-agent-search-budgets)'s missing per-agent identity.

### 7.3 Boolean inputs

Task 6a introduces one presence-aware parser: omission uses the parameter's established default; a native boolean or trimmed case-insensitive `true`/`false` string is accepted; null, numbers, collections and other strings fail before side effects. Apply it to `background`, `force`, `dry_run`, every `purge_*`, `optimize`, `vacuum`, `all_projects` on both search tools, and new directory booleans. Preserve `dry_run=true` and `respect_gitignore=true` defaults. A malformed supplied value never becomes a different operation. This deliberately changes literal strings such as `dry_run: "false"` from ignored to honored, which must appear in release notes and destructive-operation tests.

### 7.4 Fetch failure guidance

Port the useful fetch retry hint from `9e29a94` by classifying typed Go errors before formatting: temporary/timeout DNS, uncanceled network timeouts and network-unreachable errors may suggest **one retry of the same capy call**. Keep the original cause. Cancellation, SSRF blocks, NXDOMAIN, certificate/permission errors and ordinary HTTP/body/index errors do not get that hint. Do not copy upstream's broad `EPERM`/`getaddrinfo` string heuristic, promise network access, or automatically retry. Single and batch paths share this behavior.

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
4. Host hook payloads provide `agent_id` for child contexts and post-tool events. Verify actual supported-host fixtures; observed per-tool availability does not establish an entire tool pool or guarantee the first native call is redirected.
5. Git 2.34.1-compatible ignore flags are sufficient; no Go module is added. An unavailable Git executable is only relevant when applying Git ignores in a repository.
6. No stable per-agent stdio request identity has been established. This is a limitation to measure, not an assumption that a transport session ID solves it.

## 9. Not Doing

- CI/bundle/docs-only upstream ports and unsupported operating systems/runtimes/hosts: excluded by request.
- Upstream analytics/pricing/hosted Insight or event/directive replay: different product and persistence model.
- New Codex hooks, new languages or a transcript-based cwd resolver: existing integrations do not require those subsystems.
- Vault schema/key-resolution changes, ranking-formula changes or blanket reindexing: preserve capy's established invariants. The two knowledge metadata additions described above are selected exceptions, not a storage redesign.
- Unconditional execution-source echoes or command-length output guesses: these conflict with capy's context-routing purpose.
- OS-level sandboxing of submitted code: this feature strengthens explicit tool inputs and existing policy enforcement.

Deferred work is listed separately in the audit; it is not hidden in these exclusions.

## 10. Rollout, reversibility and verification

Implement as the [small task slices](tasks.md). Release notes must enumerate shell element/limit decisions, non-shell cwd/module behavior, literal source selectors, prepared Read anchors and conservative legacy-deny handling, execute-file external grants, explicit main-agent blocks and child availability tradeoffs, stdout-then-stderr batch presentation, strict supplied-boolean handling (including rejection of previously coerced numeric all-projects values), the one-time legacy-cache miss, full-response truncation, and bounded/possibly deferred stale checks. Preserve omitted defaults unless a change is explicitly listed; do not reduce this to the former four-item list.

No destructive rollback is necessary: older binaries ignore the additive source columns/index, and chunk/cipher formats remain unchanged. They lose renewal/scheduling guarantees and may reset a marker when replacing a source. Test that compatibility rather than claiming there is no migration. Rolling back cwd/path behavior restores prior semantics, so release notes must make that tradeoff visible.

Task 20 records proposed ADRs titled **Execution working directories and project identity**, **Prepared Read rules and execute-file admission**, and **Fetch validation time and bounded stale checks**, using the next available ADR numbers at implementation time. They cover S1/S2/E2/I1/C1 tradeoffs without falsely marking unimplemented decisions as accepted. Amend ADR-005, ADR-008, ADR-012, ADR-013 and ADR-022 where their existing claims change; preserve their historical rationale.

Update generators and their committed counterparts together whenever routing/setup artifacts change: `internal/platform/routing.go` with `.capy/AGENTS.md`, and any affected wrapper/config pair listed in repository `AGENTS.md`. Run whole-file artifact and merge-idempotence checks; do not patch just the generated copy.

Verification requires behavioral tests for the selected fixes, real CLI/MCP round trips, focused race checks for concurrent execution/cache counters, the full FTS5 suite, and quality/performance benchmarks for chunking/search/executor work. Use synthetic knowledge and vault keys and temporary homes. The documentation-only audit itself does not establish runtime correctness or independent design approval.
