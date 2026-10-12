# Implementation: Upstream sync through context-mode 0dfbe8d

> Status: done — all tasks complete; D1–D5 remain deferred
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

Task 3 implementation notes (2026-10-10): `sessionIDComponent` supplies the shared
mapping in `guidance.go`. Both entry points leave empty IDs/project directories
without filesystem effects. Existing compatible filenames and the guidance JSON
format remain unchanged; unsafe IDs start using their digest filename without
reading, migrating or deleting a legacy raw-ID path. Tests exercise JSON hook
input with a fresh adapter on each call, guidance-type/session isolation, reset,
the 128/129-byte boundary, and traversal/NUL/Unicode/1 MiB identifiers. Sentinel
files outside `.capy` remain intact. Task 5a's observation store remains pending.

## 4. Explicit main-agent redirects

**Owners:** `internal/hook/pretooluse.go`, hook routing tests; adapter tests only if formatter coverage needs strengthening.

Replace echo-command rewrites for curl/wget and inline HTTP with direct `FormatBlock` calls. Preserve safe-download exceptions, actual security asks/denies and Agent input modifications → verify: inspect the returned JSON decision and reason, and prove the original command is not approved as part of the redirect. Run `go test -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...`.

The rationale is reliable expression of a block, not an unverified assertion that every current Claude Code build ignores `updatedInput`.

Task 4 implementation notes (2026-10-10): both Bash redirect branches now return
`FormatBlock` with plain guidance; `routeAgent` retains `FormatModify`. Existing
detectors, silent/quiet download admission, WebFetch comprehension guidance and
security evaluation order are unchanged. Real-adapter tests assert deny JSON
without `updatedInput`, repeated denials after a guidance nudge, matched
deny/ask precedence, and preserved Agent/Task fields. New stateful fixtures pin
`CLAUDE_PROJECT_DIR` to their temporary project. Child identity/capability handling
remains Tasks 5 and 5a; this task does not claim those distinctions already exist.

## 5. Subagent-aware routing and discovery

**Owners:** adapter event parsing, `cmd/capy/hook.go` and hook context selection, an anchored detection helper in `internal/config/paths.go`, pre-tool routing; generated routing owner/copy where wording changes.

1. Preserve `agent_id`/`agent_type`/`cwd` and resolve project identity before policy loading using design §3.5 precedence → verify: actual child, type-only main agent, environment missing, explicit project override, payload cwd outside process cwd and nested project directories. Prepared rule origin/state storage must use the selected project consistently.
   The existing `DetectProjectRoot()` is process-cwd-based. Add a start-directory helper rather than temporarily calling `os.Chdir` or changing environment variables; pin any Git probe's directory and clear repository-routing overrides. Invalid/nonabsolute payload cwd is ignored with a diagnostic and uses the stated fallback; an invalid explicit selected directory is an error, not an alternative-project fallback.
2. Treat child availability as unknown unless demonstrated by Task 5a; keep advisory discovery and native-tool fallback → verify: unknown/fixed-tool children do not get trapped, while explicit denies and matched asks still run first. Do not label every child unavailable or use two local frontmatter files as the effective host tool pool.
3. Add one-attempt deferred-tool discovery guidance with native-tool fallback; preserve original Agent/Task fields → verify: no recursive bootstrap, no bootstrap mandate without discovery support, and Bash-to-general-purpose behavior remains covered.
4. Align generated routing text and its committed copy → verify: `go test -tags fts5 ./internal/platform -run 'TestGeneratedWholeFileArtifacts|TestMergedArtifactsAreIdempotent'` plus hook/adapter tests.

A parent MCP server's existence is not evidence that a fixed-tool child can call it. Do not introduce a new ready sentinel or Codex hook adapter.

Task 5 implementation notes (2026-10-10): `hook.Run` now owns input parsing and
project selection before policy loading. A nil explicit-directory pointer means
omission; an explicitly empty flag is invalid. `resolveHookContext` captures one
project and an independently validated rule cwd. The adapter no longer injects
an environment project into the event. `DetectProjectRootFrom` pins Git's child
directory and removes inherited `GIT_*` variables, then retains marker/fallback
discovery. Neither helper changes global cwd or environment.

Directory validation resolves symlinks before `..`. Hook Read rules use payload
cwd while settings and state use the selected project; relative capy file
parameters are separately anchored to that project without pre-cleaning away
symlink/parent components. Missing or malformed payload cwd falls back to the
selected project for rules. Selection and affected Read-preparation failures
produce structured PreToolUse blocks; other events report selection errors.

Children identified by `agent_id` get unverified-tool advice for HTTP/WebFetch
routing, while security enforcement and main-agent denials stay intact. No local
frontmatter is used as proof of availability. Both injected routing and the
generator/committed `.capy/AGENTS.md` describe conditional one-attempt discovery,
native fallback and the limits of the Bash type upgrade. Task 5a below adds
observation-backed redirects. D5's authoritative first-call discovery
remains deferred. The old knowledge note recommending a two-file lookup is
superseded by this feature's reconciled design.

## 5a. Re-enforce redirects after observed child capabilities

**Owners:** new bounded observation-state helper in `internal/hook`, post/pre-tool routing, session-end cleanup and dispatch wiring; reuse the ID helper from `guidance.go`.

Implement the one-use, per-tool 60-second evidence in design §3.5, stored in one bounded project file behind a separate stable lock → verify: exact eligible tools, no renewal on failures or another tool's success, 64 KiB/128-entry eviction, missing stable identity, sibling isolation, concurrent update/consume, lock timeout and corrupt/future state.

Consume all of that child's evidence before one redirect and follow the explicit alternative table → verify: failed capy retry permits the next native attempt, arbitrary HTTP is never turned into a fetch-only operation, and security decisions still dominate without consuming evidence. Parse stable identity for SessionEnd and remove only its entries, without DB access → verify: cleanup, missed SessionEnd expiry, live sibling preservation and no unbounded per-session file creation. Update routing generator/copy together. D5 remains the unknown first-call limitation.

Task 5a implementation notes (2026-10-10): `observationStore` owns the bounded
JSON file, its permanent `flock` lock and fixed staging filename. Only a successful
atomic replacement publishes consumed evidence. State read/validation/write or
20 ms lock-wait failures produce diagnostics and advisory fallback. Both metadata
and actual descriptor reads enforce the 64 KiB limit; regular-file/no-follow
checks reject FIFO and symlink state/lock entries. A fixed staging name also
bounds leftovers after a killed writer without a directory sweep.

The adapter records whether its session ID came from transcript, payload or
environment rather than the PID fallback. PostToolUse filters exact execute/fetch
names and host/MCP error/interruption flags, including serialized results; no
tool content is retained. Success renews only that tool. SessionEnd removes only
the matching stable session and neither sweeps sibling entries nor opens a DB.

The Git comprehension exception applies to WebFetch; Bash HTTP still requires
execute evidence, since execute accepts arbitrary methods/headers/body and does
not refuse Git URLs. WebFetch requires an absolute HTTP(S) URL and can use fetch
or execute. Consumption removes all alternatives only when a suitable one exists.
Native security denies and matched asks in the existing policy path run first.
The generator and committed routing instructions describe the same limits.

Success means host/MCP protocol success. Some pre-existing server responses
encode partial failures as successful results and can therefore mint evidence.
If semantic failures must invalidate observations, normalize those server status
flags with handler fixtures before changing hook classification; do not match
arbitrary error words in returned content. D5 remains deferred: one-use evidence
does not establish authoritative pre-call or permanent tool availability.

## 6. Batch heredocs and stderr

**Owners:** `internal/server/tool_batch.go`, `tool_batch_test.go`.

Remove suffix redirection from both worker paths and share deterministic captured-stream formatting → verify: terminal heredocs with/without trailing newline, quoted delimiters, stdout-only, stderr-only, combined output and empty streams work at concurrency 1 and greater than 1. Preserve serial skipped commands and parallel timeout/error isolation. Run the server batch subset with `-race`; inspect indexed content, not just a successful exit.

Task 6 implementation notes (2026-10-10): both workers pass `cmd.Command`
unchanged and use `batchCapturedOutput` to present stdout followed by stderr.
The helper inserts a newline only when both streams are nonempty and neither
side already supplies a newline at their boundary. Existing bytes are retained;
`(no output)` is used only when both streams are empty. Output capture and
truncation remain owned by the executor. Indexed-section fixtures cover terminal
and quoted heredocs, explicit stderr redirects, multiline/nonzero-exit commands,
and partial output on timeout. Serial skips and parallel timeout annotations
retain their existing behavior. This presentation intentionally does not recover
stream interleaving. Task 18 still owns batch provenance, response budgets and
raw-byte accounting changes.

## 6a. Consistent boolean request validation

**Owners:** `internal/server/coerce.go` and the boolean-consuming tool boundaries in execute/fetch/cleanup/search/vault-search.

Implement the presence-aware literal parser once; preserve each omitted default and return explicit errors for unsupported supplied types. Wire background and force, then every cleanup boolean and both all-projects selectors → verify: native/string true and false, whitespace/case, null/numeric/collection/invalid values, default dry run, no child spawned on invalid background, no purge/optimization on invalid cleanup input, and no accidental cross-project search.

The SDK's current `GetBool` already accepts string booleans and numeric truthiness, unlike capy's direct assertions. Replacing it deliberately narrows numeric all-projects inputs; do not write a test claiming its old string-true behavior was false. Validate real stdio calls as well as handlers. Task 13 reuses the helper for its new directory booleans; mechanical boundary forwarding does not create a second parser.

Task 6a implementation notes (2026-10-10): `boolArg(args, name, defaultValue)`
returns a value or a parameter-specific error without echoing the supplied data.
The cleanup-local permissive helper is removed; every affected handler uses the
shared parser. `vaultProjectScope` returns validation errors to both search
handlers before vault access or search-throttle accounting. Even a knowledge-only
request or `project: "*"` cannot hide an invalid supplied `all_projects` value.

The advertised MCP types remain boolean; string support is boundary robustness,
verified through actual JSON-RPC stdio calls. Handler tests prove no process,
HTTP request, store/vault initialization, purge or stats reset on invalid input.
Stdio fixtures verify explicit string-false eviction, background detachment,
single/batch fetch-cache bypass, and cross-project vault scoping. The pinned
`mcp-go v0.46.0` source confirms its former `GetBool` accepted numeric truthiness
and `strconv.ParseBool` shortcuts as well as string true/false. Those shortcuts
are now rejected; trimmed mixed-case literals are accepted. Task 13 will reuse
the parser for directory options. CLI flags and dependency versions are unchanged.

## 7. Project cwd for every runtime

**Owners:** `internal/executor/executor.go`, `wrap.go`, executor tests.

1. Run scripts from the executor's project directory while keeping absolute script paths in per-call temporary directories → verify: relative fixture reads/writes land in the project for shell and available non-shell runtimes.
2. Run compiled Rust from the project but compile in its temporary workspace; keep output binary paths absolute → verify: a compiled fixture finds a project-relative marker.
3. Resolve direct executor `ExecuteFile` relative paths against its project rather than process cwd → verify: use different process/project directories without calling `os.Chdir` from concurrent tests.

Run executor tests and the final benchmarks. Add isolated Go fixtures for local imports, `replace`, module/toolchain directives and automatic vendor selection; preserve `GOFLAGS` removal by `BuildSafeEnv`. Check Elixir project detection; do not patch failures by reverting non-shell cwd piecemeal.

Task 7 implementation notes (2026-10-11): ordinary execution and Rust's run phase
now use `e.projectDir`; Rust compilation and script/TMPDIR allocation stay in the
temporary workspace. Direct `ExecuteFile` calls anchor relative paths to the
executor project before making them absolute. The server's admitted absolute
path handoff remains unchanged. Elixir wrapping already receives `e.projectDir`,
so aligning process cwd also aligns its relative Mix build-path lookup without
changing `wrap.go`.

The temporary directory is normalized to an absolute path before deriving child
script, binary and environment paths. A relative inherited `TMPDIR` otherwise
resolves differently after the cwd change; a deep-project fixture reproduces
missing scripts/binaries and verifies the fix for shell, Python and Rust.

The runtime fixture covers relative reads/writes and relative/absolute file
inputs without changing process cwd. Go fixtures isolate workspace, module
cache, proxy and toolchain settings, and verify local imports, relative replace,
automatic vendor mode, go.work discovery and module/toolchain requirements.
Hostile inherited `GOFLAGS` must be stripped for them to succeed. The compatible
toolchain fixture uses a version above its `go` directive; an equal redundant
directive triggered Go's tidy requirement and was corrected in the fixture.
CLI/CI configuration changes are not applicable to this slice. Per-call cwd
overrides and the combined cwd/project-identity ADR remain Tasks 8 and 20.

## 8. Per-call cwd overrides

**Owners:** executor request/cwd resolution, `internal/server/tool_execute.go`, shared server argument resolution; mechanical forwarding in `tool_execute_file.go`, `tool_batch.go`, and schemas in `tools.go`.

Add one optional cwd field to the execution request and one server-side normalization path. Forward the effective directory into ordinary execution, execute-file execution, both batch workers and language wrappers. Keep execute-file's input path anchored independently to the server project → verify: absolute and relative cwd, missing/non-directory values, two concurrent requests targeting different directories, and no cross-request mutation. Confirm index/search and credential selection still use the original project. Run focused executor/server tests under `-race`.

The implementation slice's substantive work is request normalization and executor semantics; parameter/schema forwarding is mechanical. If runtime-specific work makes it large, split by complete runtime paths before implementation rather than scheduling an L-sized task.

Task 8 implementation notes (2026-10-11): `executor.ResolveCwd` owns directory
resolution for both server preflight and direct executor calls. It preserves
symlink-before-`..` semantics by validating the uncleaned path before resolving
it. The server's shared `executionCwd` boundary accepts only nonempty strings
when the parameter is present; omission selects the project. There is no tilde,
environment or shell expansion. Absolute external execution directories remain
allowed. Invalid requests fail before temporary files, child processes or batch
indexing.

The resolved path travels in `ExecRequest.Cwd` through all three handlers and
both batch workers. Each executor call uses its local request value for process
cwd, Rust's run phase and Elixir wrapping; compilation and temporary scripts
keep their own workspace. A batch resolves its supplied cwd once and forwards
the same absolute path to every worker; executor validation still checks each
call's directory. No server/executor project field, parent cwd or environment is
mutated. Execute-file input resolution, captured credentials, Read rules and
knowledge/vault ownership continue to use the selected project.

The expanded runtime matrix tests default, relative and absolute execution cwd
with both relative and absolute file inputs. New tests cover invalid inputs
before side effects, overlapping requests and parallel batches, conflicting
project/override settings and credentials, encrypted reopen, unchanged vault
scope, Go module selection and Elixir wrapper selection. Real stdio checks
verify optional string schemas, error results and cwd handling. Tests use short
distinct batch labels to avoid D3's intentionally deferred identity collision.
No dependency, CLI, CI or generated-artifact change was needed. The combined
cwd/project-identity ADR remains Task 20.

## 9. Bounded background output

**Owners:** `internal/executor/safebuf.go`, `executor.go`, `wait_linux.go`,
`wait_darwin.go`, related tests.

1. Add a synchronized snapshot-and-discard transition to the capture buffer; later writes return success without appending → verify: concurrent writers cannot retain bytes after the transition and the snapshot remains stable.
2. Use it when returning a background result while keeping the pipe readers alive → verify: a child continues writing/progressing after timeout, no pipe failure occurs, and retained bytes stay bounded.
3. Transfer temporary cleanup to the eventual waiter and untrack the PID exactly once → verify: normal detached exit and `CleanupBackgrounded` termination remove artifacts, without killing a reused PID or waiting twice.
4. Record the replacement of ADR-005's accepted accumulation with a discard-capable writer, retaining the original rationale as history → verify: the ADR and new executor behavior agree without claiming pipe descriptors were replaced.

Run `go test -race -tags fts5 -count=1 ./internal/executor/...`. Foreground hard-cap/timeout tests remain required. Use synchronization/files/pipes for assertions rather than long sleeps.

Task 9 implementation notes (2026-10-11): `safeBuffer.SnapshotAndDiscard`
serializes the snapshot, release and permanent discard transition with writers.
Its byte counter now denotes retained bytes and resets under the same mutex;
late writes cannot restore it. Both streams remain attached to the same readers
after detachment. The call's timeout/hard-cap select stops at return, and one
waiter owns `cmd.Wait`, detached cleanup and registry retirement. Completion and
detachment are serialized. The foreground caller still removes its own temporary
directory.

Independent review exposed a gap in the original pipe-ownership assumption:
`cmd.Wait` reaps the leader before its internal readers finish. A leader-liveness
check then prevents terminating surviving descendants and still races with PID
reuse. The executor now owns two drain goroutines and observes exit without
reaping (`wait_linux.go` / `wait_darwin.go`). It drains both streams before calling
`cmd.Wait` under the same mutex as every group signal and registry change. The
unreaped leader reserves the PID until the final reap. This necessary lifecycle
work expands the task from S to M and is recorded in design §4.3 and ADR-005.
The existing pinned `x/sys v0.43.0` supplies the observation APIs; no version bump
or new module is needed.

Exit-observation and pipe-drain failures remain separate from ordinary command
exit status: attached calls return the infrastructure cause, and detached
waiters log it before cleanup. An instance-local observer seam verifies both
paths with an injected permission failure, without changing the host's syscall
policy or hiding the error behind an exit code.

Regression fixtures write 256 MiB through a discarded buffer with zero retained
capacity or write allocations, and release a detached child to write 4 MiB to
each stream before checking progress and temporary-script availability. Natural
exit and shutdown remove the temporary directory and registry entry. Separate
concurrency and exit/cancellation fixtures run under `-race`; a foreground
101 MiB fixture retains the hard-cap kill behavior. ADR-005 preserves its original
decision as history and supersedes output accumulation with drain/discard.
No dependency versions, schemas, CLI flags, CI or generated artifacts change.

## 10. Literal source filters

**Owners:** `internal/store/search.go`, `search_test.go`.

Bind a literal LIKE-escaped substring and declare the SQL escape character in `knowledgeFilterClauses` → verify: `%`, `_`, backslashes and quoted labels match themselves, normal partial labels still match, exact mode is unchanged, and both porter/trigram/fuzzy paths obey the filter. Include explicit ephemeral-source queries to protect kind bypass. Run store and retrieval tests; do not change vault project filters or the shared ranking engine.

Task 10 implementation notes (2026-10-11): the knowledge adapter escapes
backslashes before percent signs and underscores, then binds the complete
substring pattern with an explicit SQL escape character. Equality mode keeps
the original label. The existing prepared corpus carries the same filter to
porter, trigram, OR fallback and fuzzy correction; source-kind bypass remains
unchanged. Regression fixtures distinguish literal labels from wildcard decoys
and assert each retrieval path, including ephemeral matches with a durable-only
kind request. Quotes, SQL-looking text, trailing backslashes and combined escape
characters remain bound data. No dependency, schema or ranking changes are
needed. README documents the compatibility change for wildcard-based selectors.

## 11. Plaintext chunk cap

**Owners:** `internal/store/chunk.go`, the plaintext fallback in `index.go`, chunk/index tests and benchmark fixtures where a missing case warrants one.

Add a reusable plaintext byte-bound splitter and apply it after each existing chunk strategy → verify: 4,096/4,097-byte boundaries, 4,097–5,000-byte blank sections, fewer-than-20 huge lines, long single lines, multibyte characters at boundaries, long titles, empty/whitespace-only input, and fallback from malformed JSON. Ensure the final empty-chunk fallback cannot resurrect an oversized plaintext chunk. Assert maximum persisted content bytes in both FTS tables and retrieval of markers near the end of oversized input.

Keep the store's source-size bound and sanitization-before-hash flow. Test overlap/content retention, not just chunk count. Existing same-hash data is intentionally not re-chunked automatically; document explicit remove/reindex instructions. Run `go test -tags fts5 ./internal/store/... ./internal/retrieval/...` and quality/performance comparisons.

Task 11 implementation notes (2026-10-11): each plaintext strategy uses the same
byte-bound splitter after choosing its existing logical sections. Splits prefer
newlines, then Unicode whitespace, then a UTF-8 boundary, retaining all
non-whitespace content. Split parts retain their logical section's title; title
truncation also preserves UTF-8. Fixed-line groups retain ordinary two-line
overlap but advance past oversized overlap lines so those bytes are emitted
only once. The final empty-chunk fallback uses the bounded splitter, including
whitespace-only malformed JSON, and still stores one empty chunk.

Boundary/content/overlap tests, encrypted persistence checks against both FTS
tables, tail-marker retrieval and a fuzz target cover the cap. A legacy fixture
survives close/reopen and same-hash indexing unchanged, then receives bounded
chunks after explicit eviction/reindex. README records that repair procedure
and D4's remaining markdown/structured-JSON limitation. Source-size admission,
sanitization, hashing and schemas are unchanged. New imports are Go standard
library Unicode helpers; no module changes are needed. CLI and CI rubric topics
are N/A for this slice because it changes neither commands nor workflows.

## 12. Shared, bounded file ingestion

**Owners:** new `internal/knowledge/index.go`, `internal/server/tool_index.go`, `internal/store/search.go:fileChangedSince`; optional dependency-free bounded-reader helper.

Extract the single-file operation behind a filesystem-ingestion API that receives project, policy and store dependencies. Preserve inline-content handling in the server. Keep canonical labels, durable kind, file-backed stale refresh and deny-before-content-read behavior → verify: the existing MCP file suite remains valid through the new owner.

Bound the actual descriptor read as well as stat size in **both initial ingestion and stale refresh**, reject non-regular files before reading, and expose a per-file outcome for aggregation. Reuse Task 2a's prepared policy → verify: deterministic file growth after stat via a read seam, FIFOs/devices, relative denies passed absolute paths, cached content retained with a logged skip, same-content dedup and read failures. Reuse existing `IndexResult.AlreadyIndexed` rather than inventing a second dedup indicator. Run knowledge/store/server tests.

This slice delivers the existing file path with a stricter read bound; directory support is the next complete path.

Task 12 implementation notes (2026-10-11): `knowledge.NewIndexer` receives the
selected project, effective source limit, immutable prepared policy and existing
store. `IndexFile` owns Read admission, lexical absolute path labels, nonblocking
regular-file open/stat, bounded content reading and `IndexWithFilePath` with
durable kind. Relative traversal containment and explicit external absolute-file
admission retain the existing contract. Inline content (including requests that
also supply an admitted path) remains in the MCP handler and does not gain a
backing file. Credential and store construction remain caller-owned.

`fileio.ReadAll` is shared by ingestion and `fileChangedSince`, avoiding a
store-to-knowledge cycle. It reads at most the configured limit plus one byte,
returns a typed oversize error and preserves partial byte counts on errors.
Invalid limits that cannot accommodate a detection byte fail before reading.
The existing descriptor stat gate avoids reads of already-oversized files;
growth after stat is rejected before sanitization/hashing. Failed stale reads
log a skip and retain the cached source. `FileResult.BytesRead` counts actual
bytes including probes; `BytesIndexed` counts raw bytes offered to the store,
preserving the MCP counter's unchanged-input and store-error behavior. Store
`IndexResult.AlreadyIndexed` remains the sole deduplication indicator.

Instance-local read seams make growth and I/O-failure regressions deterministic.
The stale-growth fixture uses content that sanitizes below the configured limit,
proving that post-sanitization store admission cannot substitute for a raw-read
bound. Read errors, limits, cancellation, regular-file admission, policies,
encrypted reopen, deduplication and automatic refresh are covered. D2's atomic
policy/open handoff remains documented at both readers; Task 12a still owns the
bounded refresh scheduler. No schema, dependency, generated artifact or ranking
changes are introduced. CLI and CI documentation topics are N/A: this slice adds
neither commands/flags nor workflows; existing build/test conventions apply.

## 12a. Persistently schedule bounded stale refresh

**Owners:** store source schema/migration/statements, `search.go` refresh loop and scheduling state transfer in `index.go`.

Add `file_check_seq` plus its partial scheduling index after column admission. Page at most 32 oldest-checked sources, close metadata rows before writes, and allocate/update completed-check sequences atomically in short transactions using the indexed current maximum. Skip concurrently removed/replaced incarnations; carry state only through the refresh's own same-file replacement → verify: migration/reopen, query plans, concurrent updates, no resurrection or full-source snapshot, and new CLI processes advancing beyond page one.

Apply design §5.5's byte budget including the growth-detection byte, post-metadata admission clock and single-active-refresh guard → verify: maximum source sizes above 8 MiB still make progress, slow metadata does not suppress every first attempt, slow I/O cannot start overlapping passes in one instance, and unattempted rows stay eligible. Include 10,000-source/fairness/restart, cancellation, scheduling-write failure and bounded cross-instance duplicate cases. Record latency/allocation/read/write counts. This changes freshness timing, not ranking or retention timestamps.

Task 12a implementation notes (2026-10-11): migration 020 installs
`file_check_seq` and `idx_sources_file_check` after the column exists. Fresh
schemas contain the column but leave index creation to migration. Both oldest
selection and maximum-sequence lookup use this partial index. `stale.go` owns
the bounded 32-row selection, explicit active-pass guard, cooldown, byte
reservation/accounting and post-metadata admission clock; the cursor is closed
before any file operation. The first selected source is admitted unless canceled,
and subsequent admissions stop at the byte/time bounds.

Completed checks allocate a sequence under the same write lock as their update.
Identity guards compare source ID, hash, backing path and kind, with the label
also checked for completion. Refresh indexing uses the same guard before its
replacement transaction; checking only the later schedule update would still
allow old file content to overwrite a concurrent replacement. Missing/replaced
incarnations are skipped. Ordinary same-file replacement carries its sequence;
an explicit different backing path resets it even when the content deduplicates.
Scheduling errors stop visibly, and cancellation leaves unprocessed rows eligible.

The store adds `SearchWithFallbackContext` while preserving the existing wrapper.
MCP knowledge search, batch search and intent search forward the request context;
the later CLI task can use the same entry point. This necessary mechanical
forwarding makes cancellation executable rather than a test-only refresh option.
Context propagation across unrelated store operations is not part of this slice.

Tests include encrypted legacy migration/rollback/concurrent opening, query-plan
checks, restart fairness, 10,000 real files plus a late source, same/different-file
replacement, concurrent incarnation changes, all read-byte outcomes, a 9 MiB
configured file, clock/cancellation boundaries, two stores, single-active-pass
protection and scheduling failure/overflow. The workload benchmark measures
unchanged and 128-changed-file trees of 10,000 sources using ordinary search and
write paths. The combined freshness/scheduling ADR remains Task 20, alongside
feature-wide baseline comparisons. Dependencies, generated artifacts, vault
schema and ranking are unchanged. CLI and CI rubric topics are N/A because this
task adds no commands, flags or workflows.

## 13. Directory ingestion with selection controls

**Owners:** new `internal/knowledge/directory.go`, `patterns.go`, `gitignore.go`; MCP path dispatch in `tool_index.go` and schema in `tools.go`.

1. Implement deterministic default non-following traversal with the design's hard limits and explicit outcomes → verify: counts distinguish visited entries/admitted attempts, raw reads include unchanged/failed/growing files and probe bytes, hard/operator caps are incomplete-success while operational failures are partial errors, and no partial file is indexed at exhaustion.
2. Implement validated include/exclude/extensions, canonical root selection and colon labels → verify: direct-root symlinks versus skipped descendant links, depth 0, huge flat directories, same/different-prefix dedup and mandatory exclusions.
3. Discover the canonical root's worktree and prune descendant Git roots before invoking bounded `git check-ignore --no-index --stdin -z` calls → verify: external roots, nested repositories/submodules, negated/tracked rules, unusual filenames and inherited Git variables. Keep selected-project policies/credentials fixed. Treat exits 0/1 as normal; other Git failures are visible, never an implicit unfiltered scan.
4. Expose directory controls using Task 6a's boolean parser and Task 2a's prepared policy → verify: direct/walked/refreshed absolute paths obey the same relative denies, repeated labels deduplicate, and Task 12a bounds searches after many repeated ingestion calls.

Task 13 delivers default directory ingestion; Task 13a separately adds descendant symlink following. This keeps the Git/symlink integration risk out of an oversized single task without weakening default admission.

Task 13 implementation notes (2026-10-11): `IndexPath` validates controls before
dispatch, preserving explicit-file and inline-content behavior. Existing lexical
file targets take precedence over physical directory dispatch, retaining the
earlier symlink/parent-component file contract. Directory walks
canonicalize the selected root but retain its requested spelling for descendant
Read checks. Per-file labels and Git queries use canonical paths. Incremental
directory enumeration consumes the shared 20,000-entry budget before sorting;
exhaustion before observing EOF stops without processing that directory. Depth
limits prune further descent while siblings continue; file/entry/byte limits
stop further admission. Each outcome separates visits, file attempts, raw reads,
bytes offered to indexing, retained chunks, skips, denies and failures.

The shared descriptor reader accepts a directory allowance including its probe
byte, distinguishes a request byte cap from a source-size failure, and never
indexes incomplete content. Unchanged results retrieve stored chunk counts.
Git queries use batches of at most 128 paths, no shell, five-second child
deadlines and bounded stdout/stderr. Root worktree discovery ignores empty
`.git` directories as Git does; candidate gitfile/HEAD errors stay visible.
Nested-root pruning treats any `.git` marker conservatively, including when
ignores are disabled. No module was added; the Git interface was checked against
the installed 2.34.1 manual after Context7 had no version-specific coverage.

Tests cover root aliases and alias-relative child denies, external-root ignores,
tracked/negated rules, linked worktrees/submodule layouts, raw byte accounting,
limit/error distinctions, ordered prefixes, invalid arguments without storage
effects and an encrypted MCP stdio reopen. README and architecture describe the
new behavior. CLI and CI rubric topics are N/A: no command, flag or workflow was
added; tests use the existing `serve` entry point and FTS5 build conventions.
D2, opt-in following and terminal indexing remain separate tasks. The combined
admission/freshness ADR and feature-wide performance comparison remain Task 20.

## 13a. Follow admitted symlinks during directory ingestion

**Owners:** `internal/knowledge/directory.go`, `gitignore.go`; `follow_symlinks` schema/CLI option wiring and tests.

Add the opt-in path with canonical target selection, cycle/alias dedup and both alias/target mandatory-exclusion and prepared-policy checks → verify: innocent aliases to ignored/denied/credential targets remain excluded, and links stay in the canonical root and its selected worktree.

Git checks a symlink entry itself and canonical target paths, never a path descending lexically through a directory symlink → verify: reproduce Git's `beyond a symbolic link` failure with a raw alias spelling, then prove ingestion uses the canonical path and honors its ignores. Check prefixed labels, overlapping aliases, nested Git targets, dangling links and all inherited budgets. A broken opt-in must not silently downgrade to unfiltered following.

Task 13a implementation notes (2026-10-11): `DirectoryOptions.FollowSymlinks`
and the MCP `follow_symlinks` boolean enable descendant following; omission
preserves skipping. The walker carries canonical targets separately from alias
spellings, including aliases under both the requested and canonical root.
Canonical ancestors retain mandatory/user exclusions, Read checks and nested
Git boundaries even when a link jumps directly to a deep file. Both canonical
and traversal depth obey the existing cap. Git receives physical-parent link
entries and canonical targets, never directory-link descendant spellings.

Request-local path maps deduplicate file attempts and directory enumeration.
A directory with alias-specific denies or operational failures remains eligible
through another path; all repeated enumeration still consumes the shared entry
budget. A later shallower visit may traverse previously depth-limited descendants.
Target labels and stored backing paths remain canonical, so unchanged reruns and
stale refresh keep their existing identity. Broken links are operational partial
errors; external/nested targets are intentional skips. D2 remains deferred.

Tests reproduce the real Git alias-descendant error and verify ignored/tracked
targets, canonical selection, root aliases, denied ancestors, cycles, dedup,
limits, cancellation and strict booleans through real stdio. CLI and CI rubric
topics are N/A: no commands, flags or workflows change. Task 14 will expose the
shared option in terminal indexing; build/test conventions remain in `AGENTS.md`.
No dependency upgrade, migration or generated-artifact change is required.

## 14. CLI indexing

**Owners:** new `cmd/capy/index.go`, command registration in `main.go`, CLI integration tests; reuse `internal/knowledge`.

Add the file/directory command and translate CLI flags into the same ingestion options. Reuse `--project-dir`, `loadKnowledgeTarget`, key resolution and `newKnowledgeStore` → verify: CLI-indexed markers are returned by an MCP server using the same target, and an explicit external input does not choose another database.

Report partial/failure/cap outcomes with the documented exit semantics. Always close the store and surface its close error where command conventions permit → verify: immediate encrypted reopen and retained content, missing/invalid selected keys without fallback or unintended files, and crossed environment/project credentials. Run the `cmd/capy` integration subset with synthetic keys and isolated homes.

Task 14 implementation notes (2026-10-11): `newIndexCmd` registers `index <path>`
and maps local flags to `knowledge.DirectoryOptions`; exact argument count and
option validation precede target/credential loading. Repeatable include/exclude
flags preserve commas, while extensions accept comma-separated/repeated values.
The shared ingester owns all path, policy, Git, limit and label behavior. The CLI
uses the existing knowledge-target helpers and an immutable Read snapshot,
never an MCP server instance or input-directory-derived storage selection.

The command prints directory counters/limits/bounded failure details and reports
stored chunk counts for unchanged files. It returns exit 0 for complete or
limit-stopped scans, including empty/all-filtered/all-denied scans; invalid
arguments, operational failures and cancellation return the existing generic
exit 1. SIGINT/SIGTERM cancel its context. Deferred `Close` flushes through the
existing pool-close/checkpoint path, joining any close failure with the primary
error. No global signal/cwd/config changes affect other commands.

Review required checking the command context after ingestion and after cleanup:
the store's transaction/checkpoint path does not use that context, so a signal
during the final write must still produce an error after the commit finishes.
Regression tests cancel after the final file/directory commit and verify both
the canceled result and readable, checkpointed content. Output-write failures
also preserve committed data and run store cleanup.

Dependency lookup used Context7, then pinned
[Cobra v1.10.2](https://pkg.go.dev/github.com/spf13/cobra@v1.10.2) and
[pflag v1.0.9](https://pkg.go.dev/github.com/spf13/pflag@v1.0.9) documentation/source
because Context7 did not provide version-specific coverage. No dependency
upgrade is needed. The [Go signal API](https://pkg.go.dev/os/signal#NotifyContext)
provides command-local cancellation. README/help cover syntax, flags, labels,
project/key ownership, output and exit codes. CI workflow changes are N/A;
the existing FTS5/synthetic-key test conventions from `AGENTS.md` apply.

## 15. CLI knowledge search

**Owners:** new `cmd/capy/search.go`, `main.go`, CLI tests; reuse `knowledge.go`.
Review correction: opt-in error reporting in `internal/retrieval/retrieval.go`
and `internal/store/search.go`/`types.go`, with regression tests.

Expose knowledge query, source, type, bounded limit and kind flags; wire Task 2a's prepared policy into the stale-refresh callback. Output bounded snippets with source/title and no secrets → verify: MCP-indexed data is found by CLI, literal wildcard labels stay scoped, relative/native-anchor denies protect absolute backing paths, repeated CLI processes progress through Task 12a's scheduler, ephemeral content is opt-in except explicit source bypass, and vault-only content is not silently included.

Validate arguments before store access; empty results exit successfully and key/DB failures do not → verify: actual command exit codes and help output. Run CLI and store tests. The task depends on the source-filter fix, not on introducing a server instance into the command.

Amend ADR-012 to name the terminal filtering use case while keeping the MCP content-type field unexposed → verify: CLI help and actual MCP descriptors match that distinction.

Task 15 implementation notes (2026-10-11): `newSearchCmd` joins positional query
arguments and validates nonempty queries, limits 1–100, chunk types and repeatable
durable/ephemeral kinds before target, policy, credential or storage access.
Supplied kinds replace the durable default; a nonempty literal source retains
the store's existing kind bypass. The command calls `SearchWithFallbackContext`
with a prepared Read snapshot, reusing Task 12a's persisted refresh scheduling.
It never creates a server or opens the vault. SIGINT/SIGTERM and late cancellation
propagate, and deferred close joins checkpoint failures with the command error.

Output uses bounded UTF-8 prefix snippets (1,500 bytes including an ellipsis),
512-byte source labels and 256-byte titles. Metadata is quoted; known secrets
are stripped before truncation and terminal controls removed. Empty results
exit 0; invalid options, credentials, policy, storage, output and cancellation
errors exit 1, following existing command conventions. Prefix snippets are
documented so a long chunk does not imply the match itself must be displayed.

CLI tests run real subprocesses with isolated homes and synthetic keys, checking
MCP-to-CLI retrieval, crossed credentials and linked-worktree ownership, actual
exit statuses, literal wildcard labels, kind/type filters, Read-policy anchors,
65 file-backed sources across repeated invocations and separate vault search.
ADR-012 now records terminal type selection while the real MCP descriptor stays
unchanged. No dependency upgrade, migration or generated artifact changes are
needed. [Cobra v1.10.2](https://pkg.go.dev/github.com/spf13/cobra@v1.10.2) and
[pflag v1.0.9](https://pkg.go.dev/github.com/spf13/pflag@v1.0.9) APIs were checked
against their pinned documentation after Context7 lacked matching versions. CI changes
are N/A; FTS5 and synthetic-key verification inherit repository `AGENTS.md`.

Isolated review found that the existing retrieval executor logged SQL query,
row-scan and iteration errors but returned empty/partial results with no error.
The CLI now opts into `SearchOptions.FailOnError`: the corpus reports layer
errors to a request-local, synchronized collector, and the store returns the
first error after all layer workers finish, before access tracking or successful
CLI output. Default MCP/vault behavior and ranking/fallback logic stay unchanged.
Strict CLI searches also surface generated FTS-query failures rather than
claiming success from partial layers. Denied/deferred/failed stale-file reads
remain intentionally nonfatal and retain cached content. This necessary fix
expands the task to M without changing retrieval formulas.

Regression tests cover all three layer failure stages, legacy versus strict
store behavior after dropping initialized FTS tables, and actual CLI exit 1
from an encrypted database whose matching FTS row cannot be scanned. That
fixture preserves a valid schema/open path, so it proves query-stage errors
reach the command rather than merely repeating a credential/open failure.

## 16a. Renew persisted fetch freshness

**Owners:** source schema/migration/metadata, new store fetched-index orchestration over `indexPreparedChunks`, `internal/server/tool_fetch.go` integration.

Add nullable millisecond metadata and an atomic fetched-index operation. Carry each URL's timestamp from completed/admitted HTTP body read through conversion and serial indexing; generic indexing never renews it → verify: queue/lock delays do not restart freshness, out-of-order commits pair marker with their actual content, and expired/forced unchanged revalidation re-arms caching. Include kind transitions, failed fetch/index, no access renewal and accurate stored chunk counts.

Keep `indexed_at` and retention semantics unchanged for identical content; do not reuse the existing stale-file timestamp updater. Old rows miss conservatively until revalidated → verify: old encrypted schema, repeated migrations, reopen, pre-change binary coexistence/downgrade and source replacement clearing an old marker. Extend ADR-013's freshness rationale. This is a complete default-cache correction before Task 16 adds per-call controls.

Task 16a implementation notes (2026-10-11): migration 021 adds nullable
`fetch_validated_at_ms`. `IndexFetched` uses the existing transaction and eager
write lock to pair remote content, kind and its supplied body-validation time.
New/replaced rows insert the marker; both same-hash branches renew it without
changing `indexed_at`, and retrieve retained counts/type within that transaction.
Generic dedup leaves the marker alone; generic replacement binds NULL. The
common preparation helper preserves sanitization, source limits and chunking.

The single and batch handlers capture time after body admission, before response
cleanup, conversion and indexing. Their clock seam is fixed before concurrent
use. A shared millisecond predicate rejects absent, invalid, future and expired
markers. Unchanged responses report revalidation and retained sections; batch
summaries count unchanged outcomes separately. HTML conversion failure now
returns an error, ensuring failed transforms cannot create fresh cache entries.

Tests cover expiry/force renewal, failures, kind transitions, retained metadata,
out-of-order commits, delayed cleanup/queues, old encrypted schemas, migration
rollback/concurrent opening, reopen, and actual ephemeral eviction after fresh
validation. Real stdio checks reopen persisted cache entries. The optional
`CAPY_TEST_OLD_BINARY` fixture was run against `c5a2f297`, verifying simultaneous
old/new access, old dedup/replacement and later new validation. CLI and CI changes
are N/A; tests inherit FTS5, synthetic-key and checkpoint conventions in
`AGENTS.md`. No dependency upgrade, vault or generated-artifact change is needed.
Task 16 still owns per-call TTL parsing and typed retry hints; Task 17 owns stats.

## 16. Fetch TTL and argument normalization

**Owners:** `internal/server/coerce.go`, `tool_fetch.go`, `tools.go`, fetch/coercion tests.

1. Parse TTL with the accepted forms/overflow checks and consume Task 6a's force parser → verify: omitted/zero/integer-string/fractional/negative/overflow/null cases before I/O.
2. Compare against Task 16a's persisted millisecond marker in both fetch modes → verify: fractional-second 500 ms boundaries, strict expiry, legacy NULL/future markers, configured fallback, force, kind mismatch, URL keys, unchanged revalidation and reopen.
3. Display effective freshness separately from retention and return accurate unchanged/new outcomes → verify: an old ephemeral source can still expire through retention despite fresh validation.
4. Classify typed fetch failures before formatting and append the bounded one-retry hint only to the selected transient categories → verify: single and batch temporary-DNS/timeout/unreachable failures, and negative cancellation/SSRF/NXDOMAIN/TLS/permission/HTTP/body cases. No automatic retries.

Run real stdio and fetch tests with `-race`; no per-request TTL value is persisted. The validation timestamp migration belongs to Task 16a, not a change to old timestamp formats.

Task 16 implementation notes (2026-10-11): `fetchTTLArg` preserves omission,
accepts integral numeric values and trimmed decimal digit strings, and checks
the whole-millisecond duration limit before multiplication. A supplied invalid
value fails even with force enabled. Omission selects configured hours through
checked conversion; a negative/overflowing selected configuration is an error.
The MCP schema exposes optional numeric `ttl` with bounds and `multipleOf: 1`;
real stdio also verifies accepted integer-string inputs. No default is embedded
in the descriptor because the selected project supplies it.

Single and batch paths pass one duration per request into the existing persisted
marker predicate; zero and force bypass cache reads. Responses distinguish fetch
freshness from configured ephemeral retention or durable scoring. The supplied
TTL is never written to the source or mutable server configuration. Existing
kind/URL-label identity, unchanged-result metadata and retention age are retained.

`transientFetchError` inspects typed dispatch errors before formatting. It uses
DNS flags, network timeout interfaces and wrapped unreachable errno values,
with request cancellation, permission and NXDOMAIN exclusions. It walks past
wrappers because `url.Error.Timeout` only forwards to its immediate cause.
Body/status/conversion/index failures never enter this classifier. Both modes
reuse the same fixed one-retry hint and perform no automatic retry.

Dependency checks followed `kk:dependency-handling`: Context7 lacked exact Go
1.25.2 coverage and returned current rather than pinned MCP docs. The APIs were
verified against [Go 1.25.2 net](https://github.com/golang/go/blob/go1.25.2/src/net/net.go),
[url](https://github.com/golang/go/blob/go1.25.2/src/net/url/url.go),
[strconv](https://github.com/golang/go/blob/go1.25.2/src/strconv/atoi.go),
[time](https://pkg.go.dev/time@go1.25.2#Duration), and the installed
`mcp-go v0.46.0` source (`WithNumber`, `Min`, `Max`, `MultipleOf`). No dependency
upgrade is required. CLI and CI changes are N/A; the real stdio fixtures use the
existing serve command and inherit FTS5/key conventions from `AGENTS.md`.
Task 17 still owns cache counters and the uptime-based stats display correction.

## 17. Cache outcome statistics

**Owners:** `internal/server/stats.go`, `tool_fetch.go`, `tool_stats.go`, typed transport-policy errors and URL validation in `ssrf.go`, stats/fetch tests.

Count typed terminal outcomes once per URL, including bypasses and failed network attempts → verify: concurrent batches, zero-count Git-platform redirects/SSRF/syntax rejection, attempted post-fetch failures, and purge-all reset preserving uptime.

Render hit/miss counts and a zero-safe rate even when all attempts missed. Replace uptime-based TTL remaining with configured default freshness; use one shared one-decimal savings formatter with the nonzero-return clamp at both display sites → verify: long uptime plus mixed TTLs, 99.888%, extremely-near-100%, true zero-return and zero-processed fixtures. Preserve existing savings formula and source/chunk statistics. Update ADR-013's obsolete stats consequence. Run focused stats/fetch tests under `-race`.

Task 17 implementation notes (2026-10-12): `fetchCacheOutcome` distinguishes
excluded requests, hits and attempted misses before formatting failures. The
single handler records its final outcome on return; batch workers carry outcomes
to the serial phase, which records each item once even if indexing later fails.
`CacheMisses` shares the existing mutex, snapshot and reset with hits and estimated
avoided bytes. `AddCacheHit` remains a compatible wrapper for existing callers.

The transport returns `fetchPolicyError` only when every resolved address was
blocked. If an eligible address was dialed unsuccessfully, its network error
takes precedence over later blocked addresses. Missing URL hosts and invalid
port ranges fail before dispatch. Review also required rejecting malformed
unbracketed colon-containing hostnames; IPv6 literal/zone and port-boundary
fixtures protect valid syntax. Already-canceled requests are checked before
cache lookup and immediately before HTTP dispatch; cancellation after dispatch
is an attempted miss. SSRF-blocked redirects remain excluded policy outcomes.
No security range or retry rule is relaxed.

Stats always show the cache denominator, including unused/all-miss sessions,
configured default hours, and explicitly estimated byte savings. The shared
formatter preserves the savings formula and clamps nonzero-return percentages
before rounding to one decimal. README, architecture and ADR-013 describe these
semantics. CLI and CI changes are N/A; build/test/key requirements inherit
repository `AGENTS.md`. No module, schema, generator or retrieval change is needed.
`kk:dependency-handling` found no Go 1.25.2 Context7 coverage; port parsing was
verified against pinned [strconv source](https://github.com/golang/go/blob/go1.25.2/src/strconv/atoi.go)
and [URL source](https://github.com/golang/go/blob/go1.25.2/src/net/url/url.go), plus
the installed Go 1.25.2 source. Tasks 18–20 and D1–D5 remain separate work.

## 18. Batch search controls and provenance

**Owners:** `internal/server/tool_batch.go`, `tools.go`, the store's bounded section-metadata query, batch tests.

1. Make queries optional and validate supplied queries and `query_scope` before command execution → verify: no-query calls still index; malformed arrays/scopes produce no child side effects rather than becoming indexing-only calls.
2. Default to exact batch source; make global scope explicitly durable+ephemeral knowledge-only and label every hit with its own source → verify: two equal-titled sources remain distinguishable, and vault sessions are not queried.
3. Persist bounded sanitized command previews in indexed sections as well as returning a capped command inventory → verify: later source-filtered provenance retrieval without vault, secret/heredoc/Markdown/UTF-8 cases, and unchanged complete execution/raw-output accounting.
4. Enforce design §4.4's whole serialized-result budget and bounded section metadata query → verify: 8,000 headings, indexing-only/query modes, long labels/queries/terms, escaping, inventory omissions and final serialized size at or below 81,920 bytes. Do not truncate stored content to meet a response budget.
5. Handle `AlreadyIndexed` as an unchanged outcome and read its stored total before summary/omission arithmetic → verify: repeated identical batches report retained sections rather than zero, and omitted inventory counts reflect the stored source.

Preserve serial/parallel timeout semantics from Task 6. Existing label-based batch dedup remains; do not claim isolation for simultaneous batches using the same truncated label (follow-up in design risk notes). Run batch and federation suites, then the quality benchmarks.

Task 18 implementation notes (2026-10-12): `batchSearchArgs` validates supplied
arrays and exact `batch`/`global` scopes before security checks or execution.
Serialized arrays remain supported; omission and valid empty arrays skip retrieval.
Global search explicitly selects durable+ephemeral knowledge and never opens the
vault. Result records preserve command order, serial skip/parallel timeout behavior,
and separate raw captured bytes from formatted indexing payloads.

`batch_response.go` owns sanitized, normalized and escaped previews, bounded
inventories, source-attributed hits and JSON-aware response accounting. Previews
are capped after escaping, including their ellipsis. Source labels retain the
80-byte label-derived identity, with secret stripping/whitespace normalization
before truncation. Complete commands execute, and full captured output is passed
to store admission with provenance; admission failures never truncate output to
make room. D3 remains documented beside the label builder.

`GetSectionInventory` reads stored totals, at most 40 title prefixes and byte
lengths by source ID. No chunk bodies enter Go for inventory formatting. SQL
bounds title bytes before UTF-8-safe 160-byte truncation. Unchanged batches use
the persisted total. The formatter budgets serialized JSON escaping, separately
caps both inventories and reserves one aggregate query-omission notice. A final
guard covers early errors too; rejected preflight calls remain outside call
statistics. The exact source selector uses JSON quoting, including control-bearing
labels. Returned counters keep the established complete
text-byte units; indexed counters include the payload offered on unchanged and
failed admissions, consistent with other tools.

No module, schema, vault or generated artifact changes are needed. The dependency
lookup used Context7, then pinned installed `mcp-go v0.46.0` source because its
Context7 examples were from main; Go serialization was checked against
[Go 1.25.2 encoding/json](https://pkg.go.dev/encoding/json@go1.25.2#Marshal), and
bounded metadata functions against [SQLite's scalar reference](https://www.sqlite.org/lang_corefunc.html).
CLI and CI changes are N/A; stdio tests use the existing serve command and inherit
FTS5/synthetic-key/checkpoint requirements from repository `AGENTS.md`.
Task 19's throttle policy and Task 20's feature-wide verification remain pending.

## 19. Search throttle visibility

**Owners:** `internal/config/config.go`, `loader.go`; server throttle policy and response formatting, schemas/docs and tests.

Add the three `[search]` settings with omission-aware merging, explicit-zero/type/range validation and post-merge taper/block validation. Capture the resulting policy per server and return a coherent snapshot from atomic advance → verify: global/project precedence, defaults, invalid settings, changed thresholds, exact window boundary, empty/partial-result paths, and concurrent calls. Use a clock seam rather than minute-long sleeps; invalid requests remain outside accounting.

The notice states the actual effective limit and shared-server scope. Add an actionable code comment referencing [D1](upstream-audit.md#d1-per-agent-search-budgets) beside `searchThrottle`; do not add a per-agent map without verified request identity. Run server search/federation/vault-search tests under `-race` and retain current source-kind behavior.

Task 19 implementation notes (2026-10-12): `SearchConfig` defaults to
60000/3/8. Pointer detection merges supplied fields and rejects invalid scalars
per layer; threshold ordering is checked after all layers merge. `searchThrottle`
captures policy by value, then resets/increments and computes limits/time under
one mutex. Notices use this admission snapshot on all valid response paths,
including blocked, empty and degraded results, and count toward returned bytes.
The reset comparison includes equality; displayed milliseconds round upward.

Preflight now rejects malformed query/kind arrays, blank queries, non-string
selectors and nonpositive/fractional/nonfinite limits before storage or budget
use. Serialized arrays and positive integer numeric strings remain supported.
The strict array parser is shared with batch preflight without changing batch
semantics. This closes formerly permissive validation paths required by the
invalid-calls-consume-no-budget contract. README records the compatibility change.

Clock-controlled tests cover thresholds, exact expiry, coherent concurrent calls
and construction-time capture. Real stdio checks verify configured behavior and
validation before storage. ADR-008 records tunability and the shared-server/D1
limitation. No module, schema or generated artifact changes are needed. CLI and
CI changes are N/A; stdio uses the existing serve command and tests inherit
FTS5/synthetic-key conventions from repository `AGENTS.md`. Dependency lookup
returned main-branch Go examples despite the requested 1.25.2 Context7 ID;
`strconv.ParseFloat` special-value/error semantics were verified against the
installed Go 1.25.2 source after the pinned web fallback failed.

## 20. Final verification and documentation

Depends on every implementation slice, including Tasks 2a, 5a, 6a, 12a, 13a and 16a. Shared files require coordination; dependencies elsewhere represent behavior prerequisites rather than schema-file serialization.

1. Run `kk:test`: `make test`, `make test-race`, `make build`, and `go test -tags fts5,glamour ./internal/vault/tui/...` to protect the shared retrieval consumers → verify: save commands, outcomes and any environmental limitations in `verification.md`.
2. Run the real CLI/MCP round trips using `cmd/capy/mcp_stdio_helpers_test.go` conventions → verify: fresh and existing encrypted stores, same target across interfaces, correct project selection, shutdown checkpoint and unchanged vault ownership.
3. Run `make bench` against `c0bfff2` and the implementation branch, plus the 10,000-file stale-refresh workload; compare quality/performance and record bounded work, latency, allocation and DB-write measurements → verify: no hidden full-source scan or CLI starvation and no weakened correctness assertions. Rename detached `HEAD.json`/`HEAD.txt` to the baseline label before `make bench-compare`.
4. Run `kk:document` for README/tool/CLI usage, architecture and the complete design §10 compatibility list. Create the three named proposed ADRs there and amend ADR-005/008/012/013/022 where needed → verify: origin-aware grants, cwd/project separation, additive metadata, independent retention and bounded work are recorded with rationale/provenance. Test actual old-reader compatibility before claiming it.
5. Run generated artifact checks and `git diff --check` → verify: each changed generator matches its committed counterpart and no unrelated generated file is included.
6. Run `kk:review-code` with Go input, then `kk:review-spec` against all feature artifacts → verify: fix actionable findings or record each unresolved item durably with a reason and next step. Mark implementation tasks done only with evidence.

Do not remove deferred D1–D5 merely because the suite passes. Both supplied independent reviews have been reconciled; the reconciliation is an author response, not a new independent approval.

Task 20 implementation notes (2026-10-12): the final feature review required
three bounded corrections. Python list-form subprocess extraction now preserves
direct argv provenance through both policy evaluators; bounded scanning quotes
literal data while deny/ask matching retains the original flattened spelling.
Actual shell-source arguments (`shell=True`, conservatively dynamic shell flags,
and recognized shell `-c` forms) remain separately inspected. Trailing-call
inspection accounts for nested keyword expressions, quotes and comments. This
is still static extraction, not full Python evaluation or arbitrary wrapper
resolution. Bash compound-array values likewise remain data while their command
and process substitutions are scanned under the same resource bounds.

Shared file/directory ingestion now reports cancellation after the final store
write or metadata step while retaining committed new/unchanged/chunk counters.
Deterministic tests cancel immediately after a real commit and verify partial
outcomes and encrypted reopen. CLI's existing late-cancellation handling remains.

Verification also fixes two previously recorded fixture issues: direct Node
benchmarks execute inline code with `-e`, and cleanup assertions compare public
stats fields without copying a mutex. The CLI refresh fixture explicitly sets
file mtimes newer than second-resolution indexing time, preserving its exact
positive-progress/32-file/fairness assertions. The old-binary fixture now starts
with an actual pre-sync encrypted database and uses MCP after reopen, so it can
test `c0bfff2` as well as pre-021 binaries that already have the search CLI.

Performance sampling is configurable through `BENCH_COUNT` and `BENCH_TIME`,
retaining the six-sample/one-second defaults and unchanged quality assertions.
Task 20 uses a complete one-iteration smoke run plus repeated focused comparisons
to avoid rebuilding each large search corpus six times. Baseline and current
trees receive identical fixture/sampling changes; measured setup limitations
and descriptive-versus-statistical claims are explicit in verification.
Search fixture seeding also defers periodic FTS optimization until the corpus is
complete, retaining every production admission/chunk/vocabulary write and checked
final optimization of both indexes. Both revisions use that same benchmark-only
setup; timed indexing and quality fixtures keep their existing behavior.

README consolidates every design §10 compatibility change. Proposed ADR-033,
ADR-034 and ADR-035 record cwd/project identity, prepared admission, and independent
freshness/retention/scheduling respectively. ADR-005/008/012/013 already contain
their relevant amendments; ADR-022 now qualifies historical chunk/database-size
claims and records raw-read and response-versus-storage bounds. Their proposed
status does not assert new decision acceptance. Existing CLI usage, build/key
requirements and CI are inherited from README, CONTRIBUTING and AGENTS.md; no
module, vault schema or generated artifact changes are added by Task 20.

The timestamp fixture's standard-library lookup followed `kk:dependency-handling`:
Context7 returned the pinned Go 1.25.2 `test_cache_inputs.txt` example using
`time.Now().Add` and `os.Chtimes`. No dependency upgrade is needed. Commands,
results, benchmark provenance and environment limitations are recorded in
[final verification](verification.md#task-20-final-verification-and-compatibility).

Final reflection: the sync preserves the selected architecture and all 84 quality
metrics. Final review required small runtime corrections for literal argv/array
data and late cancellation; those exposed why policy parsing and committed
progress need explicit representations. The most time-consuming verification
surprises were the real-corpus race canary and repeated FTS compaction during
benchmark setup. Their timeout/setup fixes leave correctness assertions intact.
No additional design deviation or undocumented convention needs knowledge indexing.

## Verification status at design time

The reconciliation reran the existing synthetic encrypted-store probe and focused current security/store tests; results are in its evidence ledger. No feature runtime implementation was changed. All future-behavior tests above remain acceptance criteria, not completed implementation evidence.
