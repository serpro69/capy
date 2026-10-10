# Upstream sync verification

## Task 1: Shell policy evaluation

Implemented 2026-10-10. Scope: the shared security scanner, both policy evaluators,
hook checks, and MCP preflight. Other sync tasks remain pending.

### Behavior and coverage

- Newline/CR, background and chain separators; nested command/backtick
  substitutions; arithmetic/parameter expansion boundaries; ANSI-C string quotes;
  escape parity and comment token boundaries.
- Literal single quotes and quoted heredocs; unquoted heredoc substitutions,
  multiple/tab-stripped delimiters, continuation joins and inherited expansion
  quote context.
- Deny precedence, per-policy all-element allows, ordered ask/allow precedence,
  unchanged unmatched-ask passthrough, and checking the whole batch/extracted
  command collection before returning a matched ask.
- Input/frame/element boundaries through unit tests, hooks and MCP checks;
  exact visit accounting boundary plus adversarial aggregate-work inputs.
  Hook/MCP shell, execute-file, batch (serial/parallel), and extracted JavaScript
  checks reject over-limit inputs before spawning. Filesystem markers prove that
  neither earlier batch entries nor rejected scripts execute.
- Hook batch normalization includes serialized arrays and plain command strings;
  malformed command types block visibly.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development`, and `CAPY_VAULT_KEY=test-key`.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/security/... ./internal/hook/... ./internal/server/...` | Passed after all review fixes: security 0.016s, hook 0.399s, server 106.715s. |
| `go test -race -tags fts5 -count=1 ./internal/security/... ./internal/hook/... ./internal/server/... -run 'Test(Shell\|Split\|Evaluate\|Execute_\|BatchExecute_)'` | Passed after review fixes (security 1.111s, hook 6.052s, server 7.843s). |
| `go test -tags fts5 ./internal/security -run '^$' -fuzz FuzzSplitChainedCommands -fuzztime=10s -parallel=2` | Passed after fixes: 525,253 executions; no crash or partial-error results. |
| `go test -tags fts5 ./...` | All packages passed except CLI vault fixtures influenced by the personal 192 KiB minimum-session setting. The isolated full CLI package rerun below passed. |
| `XDG_CONFIG_HOME=/tmp/capy-task1-config go test -tags fts5 ./cmd/capy/...` | Passed: complete CLI package, 216.776s. |
| `git diff --check` | Passed after final documentation updates. |

The first attempt could not write the sandbox's default Go cache; the writable
temporary cache resolved it. The server's local HTTP fixtures required execution
with loopback socket permission; the rerun passed. No assertion was weakened.

The broad run exposed existing configuration leakage in several CLI vault tests:
the personal `~/.config/capy/config.toml` sets `min_session_bytes=196608`, excluding
their 291/741-byte fixtures. The rerun sets `XDG_CONFIG_HOME=/tmp/capy-task1-config`
to an empty directory. Deferred test-harness follow-up: align
`setupCodexVaultEnv` and the manually configured vault CLI fixtures with
`setupVaultEnv`'s temporary XDG isolation. This task does not change unrelated
vault fixtures; the concrete next step is to add `t.Setenv("XDG_CONFIG_HOME",
t.TempDir())` at those setup sites and rerun them under a nonzero personal policy.

### Review and compatibility

`kk:review-code:isolated` used an independent code-reviewer and PAL
`gemini-3.1-pro-preview`. The [review record](.reviews/task-1-code-review-2026-10-10.md)
contains all findings, resolutions and the independent reviewer's approval.
Harmless Bash probes confirmed the review's quote/comment/heredoc cases really
execute substitutions; regression tests then verify the policy blocks them.

New limits and malformed unterminated quotes/substitutions block even with no
configured deny policy. ANSI-C/localized heredoc delimiter forms fail closed;
supporting their escape/locale decoding is explicitly deferred in `split.go` and
`implementation.md`. These remain static textual policy checks: aliases, dynamic
code and the pre-existing heuristic non-shell extraction are not complete shell
interpretation or an OS security boundary.

No dependency, executor, search, indexing, storage, key, or generated artifact
changed. Retrieval-quality benchmarks are not applicable to this security slice;
the feature's broader quality/performance verification remains Task 20.

## Task 2a: Prepared Read policies

Implemented 2026-10-10. Scope: origin-aware policy preparation in security,
existing MCP direct-file inputs, hook preflight and the stale-refresh callback.
Execute-file containment/checked-path forwarding (Task 2), validated hook cwd
selection (Task 5), and future ingestion/CLI paths remain pending.

### Behavior and coverage

- Local/shared/user origins; filesystem-root, home, source and cwd anchors;
  relative/absolute inputs; recursive basenames versus explicitly scoped paths;
  `*`, `**`, `?`, escaped literals, Unicode and newline filenames.
- Bare Read, deny precedence across settings, asks not granting access, supported
  home/root grants and missing-target grant rejection. Invalid JSON, permission
  container/list/value types, unsupported patterns, dangling settings links,
  invalid project directories and symlink loops fail visibly.
- Physical deny aliases, symlink-before-`..`, symlinked project roots, and grants
  requiring both lexical and physical targets. Single-slash denies retain both
  anchor meanings, including the old matcher's embedded-`**/`, backslash and raw
  traversal behavior; allows never receive that compatibility expansion.
- End-to-end server fixtures use isolated homes and project/user settings.
  Relative, absolute and symlink inputs block both indexing and execution, with
  marker checks proving no child spawn and searches proving no denied content
  reached the index. Invalid policy does not prevent inline-content indexing.
- After closing/reopening an encrypted store with newly restrictive or invalid
  settings, stale refresh logs a skipped read, preserves the original searchable
  content, and never indexes the changed backing file. Policy snapshot and
  warning-copy tests establish restart semantics and immutable diagnostics.
- Hooks return structured deny responses for invalid policy data and denied
  execute-file/index paths instead of relying on an exit code the wrapper masks.

### Verification commands

All Go commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development`, and `CAPY_VAULT_KEY=test-key`.
Broad suites also set `HOME` to a new temporary directory,
`XDG_CONFIG_HOME=$HOME/.config`, `GOPATH=/home/sergio/go`, and
`GOMODCACHE=/home/sergio/go/pkg/mod`. This isolates user settings while reusing
installed dependencies. Local HTTP fixtures run with loopback permission.

| Command | Result |
|---|---|
| `go test -tags fts5 ./internal/security ./internal/hook ./internal/server -run 'Test(ReadPolicy\|PreparedReadPolicy)' -count=1` | Passed: new policy, hook and server tests. |
| `go test -tags fts5 ./...` | All packages passed before the final legacy-matcher compatibility addition: CLI 218.039s, server 110.483s, vault 180.752s; unchanged packages used Go's test cache where eligible. |
| `go test -race -tags fts5 -count=1 ./internal/security ./internal/hook ./internal/server -run 'Test(ReadPolicy\|PreparedReadPolicy\|Index_Path\|ExecuteFile)'` | Passed after the legacy compatibility addition. |
| `go test -tags fts5 -count=1 ./internal/security/... ./internal/hook/... ./internal/server/...` | Passed after all implementation changes: security 0.052s, hook 0.358s, server 106.829s. |
| `go vet -tags fts5 ./internal/security ./internal/hook ./internal/server` | Passed. |
| `git diff --check` | Passed after documentation and review updates. |

The initial new server tests used a nonexistent local store search method; the
compile error was corrected to `SearchWithFallback` without changing assertions.
No modules, encryption/storage formats, retrieval/indexing algorithms, executor
semantics or generated artifacts changed. Retrieval benchmarks are not applicable
to this policy-wiring slice; Task 20 retains the broader performance comparisons.

### Review and compatibility

The independent [review](.reviews/task-2a-code-review-2026-10-10.md) approved the
final changes without actionable findings using `kk:review-code:isolated` with
a code-reviewer agent. PAL is unavailable in this session. The reviewer performs
static review only;
the implementation session runs the tests recorded above. The original matcher
remains available to legacy API callers, while every production file consumer
uses the prepared snapshot. Single-slash compatibility can deny more paths;
`//` removes anchor ambiguity. Invalid policy data blocks file admission until
corrected and the server restarted; skipped refreshes retain cached content.
Check/open replacement remains D2, documented at the policy helper and index
descriptor read. No new convention needs indexing beyond these repository docs.

Optional documentation follow-up: `kk:clarify-docs` can review the updated
`README.md` Security section and `docs/architecture.md` File Path Evaluation
section. This is editorial polish, not an unfinished Task 2a requirement.

## Task 2: Execute-file path admission

Implemented 2026-10-10. Scope: prepared-policy containment/external grants,
checked absolute-path forwarding, and regression tests. Runtime cwd changes,
per-call cwd overrides and hook cwd selection remain separate pending tasks.

### Behavior and coverage

- Relative paths resolve from the selected project independently of process cwd
  and the policy's rule cwd. Both spellings of a symlinked project are local.
  Sibling-prefix, traversal, external-alias and symlink-before-`..` inputs require
  a grant when either lexical or physical containment fails.
- Native absolute/home/bare Read allows work through local, shared and user
  settings. One grant must cover both candidates; separate alias-only and
  target-only grants do not combine. Denies still win on raw, lexical and physical
  paths, including conservative legacy single-slash denies.
- Empty/NUL paths, missing targets, dangling/looping links, unavailable policies
  and disappeared/non-directory projects return errors. Server marker tests
  establish that rejected admissions do not spawn submitted code.
- A real shell fixture proves `FILE_CONTENT` uses the selected project even when
  a same-named file exists in process cwd. Another proves symlink-before-`..`
  reads the physical target rather than the different lexical file. Existing
  external-success coverage now uses an intentional grant; command-deny/limit
  fixtures use existing local inputs and retain their original assertions.
- The review-driven handoff regression covers runtime interpolation, quotes,
  backslashes, newlines, control characters and Unicode in physical filenames
  behind safe aliases, with denied alternative filenames. JavaScript, Python,
  shell, Go, Rust and Perl passed; capy's detector found no TypeScript, Ruby, PHP,
  R or Elixir runtime, so those cases skip explicitly. Node v24.21.0 is installed
  and a direct TypeScript probe passed, but the TypeScript candidate list only
  includes bun, tsx and ts-node. Adding Node detection is outside Task 2.
  The parent environment stays intact
  and cannot override the per-child admitted path. Rustup's toolchain location
  is preserved while isolating home/settings.
- Explicit external `capy_index(path)` still succeeds under an empty deny policy.
  No retrieval, indexing algorithm, module dependency, storage format, key
  resolution, CLI interface, CI configuration or generated artifact changed.

### Verification commands

Go commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development`, and `CAPY_VAULT_KEY=test-key`. The broad
suite additionally uses a temporary home/config directory with the installed
`GOPATH=/home/sergio/go` and `GOMODCACHE=/home/sergio/go/pkg/mod`. Final runtime
checks also set `RUSTUP_HOME=/home/sergio/.rustup` to retain the installed compiler.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/security/... ./internal/server/... -run 'TestResolveExecuteFile\|TestExecuteFile_\|TestPreparedReadPolicy\|TestShellPolicyLimitsBeforeSpawn'` | Passed: security 0.020s, server 3.116s. |
| `go test -race -tags fts5 -count=1 ./internal/security ./internal/server -run 'Test(ReadPolicy\|ResolveExecuteFile\|PreparedReadPolicy\|ExecuteFile\|ShellPolicyLimitsBeforeSpawn)'` | Passed including the final lexical-deny case: security 1.061s, server 5.085s. |
| `go vet -tags fts5 ./internal/security ./internal/server` | Passed. |
| `go test -tags fts5 ./...` | Before the review-driven executor fix: CLI, executor, hook, platform, security, vault and TUI passed; remaining unchanged packages used cache. Config/server needed the socket-enabled rerun below. |
| `go test -tags fts5 -count=1 ./internal/config ./internal/security ./internal/server` | Socket-enabled rerun passed: config 0.060s, security 0.029s, server 105.892s. |
| `go test -tags fts5 -count=1 ./internal/server -run TestExecuteFile_LiteralPhysicalPath -v` | Passed after the handoff fix: six installed runtimes; five explicit skips. |
| `go test -tags fts5 -count=1 ./internal/security/... ./internal/server/... ./internal/executor/...` | Final code, isolated settings and socket access: security 0.036s, server 107.439s, executor 2.226s; all passed. |
| `go test -race -tags fts5 -count=1 ./internal/security ./internal/server ./internal/executor -run 'Test(ReadPolicy\|ResolveExecuteFile\|PreparedReadPolicy\|ExecuteFile\|ShellPolicyLimitsBeforeSpawn\|InjectFileContent)'` | Final code passed: security 1.058s, server 5.218s, executor 1.015s. |
| `go vet -tags fts5 ./internal/security ./internal/server ./internal/executor` | Passed after the handoff fix. |
| `go test -race -tags fts5 -count=1 ./internal/server ./internal/executor -run 'Test(ExecuteFile\|InjectFileContent)'` | After the final JS/TS shadowing fix: server 1.773s, executor 1.017s; passed. Vet also passed again. |
| `make bench-quality` | Passed; report: `bench-results/feat-upstream_sync.json`. |
| `git diff --check` | Passed. |

The first test attempt could not write the sandbox's default Go cache; using the
temporary cache resolved that setup failure. The initial broad suite passed all
packages except config/server fixtures requiring local Unix/TCP sockets; these
passed when rerun with socket permission. The first literal-filename runtime
probe exposed the temporary home's missing rustup configuration, fixed by
retaining its installed toolchain location without weakening the assertions.

Because review required an executor handoff change, quality benchmarks were run
as required by repository guidance. A clean `git archive` of pre-task commit
`771bcca` was benchmarked using `CAPY_BENCH_RESULTS=/tmp/capy-task2-baseline.json`
and `go test -tags fts5 -run '^TestBench' -p 1 ./internal/store ./internal/server
./internal/vault`. The dataset hash matches. All quality metrics (156 knowledge
cases plus vault cases), post-processing deltas, failure records and threshold
output/compression decisions match exactly. Only run metadata and latency differ:
threshold baseline/current milliseconds were 1.966/1.943, 312.383/309.177,
6.353/6.644 and 21.807/22.324. These single-run timings do not establish statistical
performance significance. Task 20 retains final feature comparisons.

### Compatibility and remaining boundary

External execute-file inputs now require an explicit Read allow. The request's
existing response/source labels are preserved, while the executor gets the
admitted physical absolute path. Missing paths fail before execution instead of
depending on the runtime's file-read behavior. D2 remains: concurrent replacement
after admission is possible until the checked file can be handed atomically into
the runtime. The helper and user-facing documentation state that limitation.
These decisions are already recorded in repository docs, so no duplicate
knowledge note is needed. CLI and CI documentation changes are N/A for this task.

The [isolated reviewer](.reviews/task-2-code-review-2026-10-10.md) approved the
final changes with no remaining actionable findings. The original P1 runtime
interpolation and follow-up P2 JS/TS `process` shadowing were fixed and retested.
PAL was unavailable; the independent code-reviewer supplied static review.

Optional documentation follow-up: `kk:clarify-docs` can review the new execute-file
paragraph in `README.md` and File Path Evaluation in `docs/architecture.md`.

## Task 3: Guidance state filenames

Implemented 2026-10-10. Scope: the shared session filename component in
`internal/hook/guidance.go`, creation/reset callers, and regression tests.

### Behavior and coverage

- Preserve 1–128-byte IDs containing only ASCII letters, digits, `.`, `_` or `-`.
  Other nonempty IDs become `sha256-` plus the full lowercase SHA-256 digest.
  Empty IDs retain the non-persisting guidance fallback; reset is a no-op.
- Golden mapping cases cover the full allowed alphabet, UUIDs, dots, 128/129-byte
  boundaries, an invalid final byte, slashes, backslashes, NUL, Unicode and spaces.
- Hook JSON calls with fresh test adapters verify persisted Read/Grep throttling,
  reset, separate sessions, and traversal/Unicode/NUL/1 MiB IDs. Files outside
  `.capy` remain byte-identical, with no unexpected files or directories.
- The existing environment-independent test adapter prevents inherited
  `CLAUDE_PROJECT_DIR` from redirecting filesystem writes outside the fixture.

### Verification commands

Commands used `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development`, and `CAPY_VAULT_KEY=test-key`. Broad tests
also used an empty temporary `XDG_CONFIG_HOME` to avoid personal config settings.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/hook/...` | Passed, 0.370s. |
| `go test -race -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...` | Passed: hook 5.248s, adapter 1.010s. |
| `go test -race -tags fts5 -count=1 ./internal/hook/... -run 'Test(SessionIDComponent\|GuidanceSession\|GuidanceEmpty)'` with an external temporary `CLAUDE_PROJECT_DIR` | Passed after the review fix, 1.329s; the external directory remained empty. |
| `go test -tags fts5 ./...` | All packages passed except sandbox-blocked Unix/TCP socket fixtures in config/server. CLI passed in 223.958s, vault in 254.548s. |
| `go test -tags fts5 ./internal/config/... ./internal/server/...` with socket permission | Both previously blocked packages passed: config 0.047s, server 106.733s. |
| `git diff --check` | Passed after documentation updates. |

The broad attempt began before the test-adapter review fix; the focused race
rerun above verifies the final tests under the environment that exposed the
issue. Production code was unchanged throughout verification. All packages are
covered by the broad run plus the socket-enabled rerun; no assertions were
weakened. No search, indexing, chunking or executor code changed, so retrieval
benchmarks are not applicable to this task.

### Review and compatibility

The [isolated review](.reviews/task-3-code-review-2026-10-10.md) approved the final
change after one P2 test-isolation fix. PAL was unavailable; the independent
code-reviewer supplied static review. No systemic P0/P1 findings or new project
conventions require knowledge indexing; the mapping is already specified in
the feature design and documented in the architecture guide.

Compatible filenames and guidance JSON remain unchanged. Unsafe legacy filenames
are neither read nor migrated or deleted; those IDs begin using the safe digest
filename and may receive guidance again once after upgrade. This change contains
the session ID's path syntax; it does not add protection against a separately
replaced/symlinked project directory or state file. Task 5a's observation-state
format and lifecycle remain pending. No CLI option, dependency version, database,
generated artifact or CI configuration changed.
