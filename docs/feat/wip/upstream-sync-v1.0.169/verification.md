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

## Task 4: Direct routing denials

Implemented 2026-10-10. Scope: the two Bash HTTP routing rejection paths in
`internal/hook/pretooluse.go`, their hook/adapter integration coverage, and usage
documentation.

### Behavior and coverage

- Blocked curl/wget and recognized inline HTTP now use `FormatBlock`. The real
  Claude adapter emits `permissionDecision: "deny"` with actionable capy guidance
  in `permissionDecisionReason`, without an approved replacement command or
  `updatedInput`.
- Tests cover curl, wget, Python requests, JavaScript fetch/http calls, tool
  aliases, mixed safe/unsafe chains, stdout aliases, verbose output, and repeated
  denials after a Bash guidance nudge has been persisted.
- Existing silent/quiet file-download exceptions and quoted-text behavior remain
  covered. Matching security denies and asks take precedence over routing,
  including for otherwise allowed downloads and competing allow rules.
- Agent and Task calls still receive prompt edits and retain their description,
  model, background, resume and subagent-type fields. Existing Bash-subagent
  upgrade and WebFetch comprehension-guidance tests pass.
- New stateful real-adapter fixtures pin `CLAUDE_PROJECT_DIR` to a temporary
  project; the security-precedence fixture clears it.

### Verification commands

Commands used `CLAUDE_PROJECT_DIR=`, `GOCACHE=/tmp/capy-go-build`,
`CGO_ENABLED=1`, `CAPY_DB_KEY=test-key-for-development`, and
`CAPY_VAULT_KEY=test-key`. The full suite also used an empty temporary
`XDG_CONFIG_HOME` and the Unix/loopback socket permission required by its local
config/server fixtures.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...` | Passed: hook 0.341s, adapter 0.002s. |
| `go test -race -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...` | Passed: hook 5.385s, adapter 1.009s. |
| `go test -tags fts5 ./...` | All packages passed; CLI 224.455s, hook 0.424s, server 113.580s, vault 257.506s. |
| `git diff --check` | Passed after documentation updates. |

An initial Node fixture used `require('http').get(...)`, outside the unchanged
detector's supported `http.get(...)` spelling. The fixture now declares `http`
and calls that recognized form; no runtime detector or assertion was weakened.
Direct `require('http').get(...)` remains an existing detection gap, outside this
response-formatting task. A future detector change should add positive/negative
fixtures for direct module-member calls and aliases before expanding matching.

### Review and compatibility

The [isolated reviewer](.reviews/task-4-code-review-2026-10-10.md) approved the
change with no P0–P3 findings. PAL was unavailable; review used the independent
code-reviewer. No findings or new conventions require knowledge indexing.

Hosts now receive a direct rejection where they previously received approval to
run an echo replacement. Live host behavior was not tested; the JSON contract is
covered without claiming that every host ignores argument modifications.
Detection rules and policy order are unchanged. Child identity/capability
handling remains Tasks 5/5a. No dependencies, generated artifacts, database
behavior, search/indexing/chunking or executor code changed; benchmarks are not
applicable. README and architecture now describe the direct-denial behavior.

## Task 5: Subagent routing and hook context

Implemented 2026-10-10. Scope: adapter metadata, hook project/rule-cwd selection,
anchored configuration discovery, unknown-child advisory routing, and injected
and generated routing instructions.

### Behavior and coverage

- Adapter fixtures retain `agent_id`, `agent_type` and payload `cwd`. Invalid cwd
  types, null and empty strings preserve tool/security input and trigger fallback
  diagnostics. Identity semantics were checked against the official
  [hook input reference](https://code.claude.com/docs/en/hooks#common-input-fields):
  agent type can exist on a main-agent session, so only nonempty ID selects the
  child path. Tool scope/discovery was also checked through official Context7
  documentation; live Claude sessions were not exercised.
- Context fixtures cover explicit/environment/payload precedence, relative
  selections, missing/nonabsolute/non-directory/NUL payload paths, empty/invalid
  explicit paths, invalid environment selections, and symlink-before-parent
  resolution. Checks confirm the production helpers leave cwd and environment
  unchanged. Invalid PreToolUse selections become structured denies; other
  events error without guidance-state mutation.
- A real CLI subprocess runs from a different project's nested directory and
  verifies policy selection, payload/process discovery, invalid cwd diagnostics,
  invalid selections, and exact guidance-state ownership. Synthetic home/config
  directories isolate settings; the CLI fixtures unset both database keys.
- Prepared Read checks keep settings at the selected project and rule cwd at the
  payload directory. Relative capy file inputs remain project-relative; absolute
  cwd-denied paths and malformed selected policies still block child calls.
- Child cases include fixed-tool/inherit-all local definitions, unknown plugin
  types, and missing types. Their HTTP/WebFetch results contain advice without
  an explicit permission grant/deny. Type-only main sessions still get direct
  denials. Bash and capy shell security denies/asks retain precedence, including
  when cwd has an invalid type. Git-page advice favors native comprehension tools.
- Anchored Git discovery ignores inherited `GIT_DIR`, `GIT_WORK_TREE`,
  `GIT_COMMON_DIR`, ceiling and command-config overrides. Tests retain Git's
  precedence over nested capy markers and exercise marker/no-marker fallback.
  Generated whole-file and merge-idempotence checks pass with the generator and
  committed `.capy/AGENTS.md` synchronized.

### Verification commands

Commands use `CLAUDE_PROJECT_DIR=`, `GOCACHE=/tmp/capy-go-build`,
`CGO_ENABLED=1`, `CAPY_DB_KEY=test-key-for-development`, and
`CAPY_VAULT_KEY=test-key`. The broad suite uses an empty temporary
`XDG_CONFIG_HOME` and local socket permission for config/server fixtures.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/... ./internal/platform/...` | Existing suites passed after initial implementation: hook 0.374s, adapter 0.002s, platform 0.194s. |
| `go test -race -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/... ./internal/platform/... ./internal/config/... ./cmd/capy/... -run 'Test(Hook\|Child\|ResolveHook\|ParsePreToolUse\|DetectProjectRootFrom\|Routing\|GeneratedWholeFileArtifacts\|MergedArtifactsAreIdempotent\|PreparedReadPolicyHook)'` | Passed: hook 1.088s, adapter 1.016s, platform 1.017s, config 1.027s, CLI 3.048s. |
| `go test -tags fts5 -count=1 -v ./internal/config -run '^TestDetectProjectRootFromMarkersAndFallback$'` outside the sandbox | All four marker/no-marker cases passed, 0.003s. |
| `go test -tags fts5 ./...` | All packages passed: CLI 225.615s, hook 0.517s, adapter 0.003s, config 0.077s, platform 0.238s, server 113.171s, vault 256.099s. |
| `git diff --check` | Passed after documentation updates. |

The sandbox supplies an ambient `/tmp/.git`, so the initial no-marker fixture
correctly discovered that ancestor instead of falling back to its start
directory. The test now skips only that case when its unmarked-ancestry
precondition is false; the unsandboxed rerun above exercised the assertion and
passed. Discovery behavior and assertions were not weakened.

### Review and compatibility

The [independent reviewer](.reviews/task-5-code-review-2026-10-10.md) approved with
no P0–P3 findings. PAL was unavailable. No knowledge indexing is needed: the
decisions and limitations are recorded here and in the reconciled design.

Hook policy/state ownership now follows one validated selection. Payload cwd can
change cwd-relative rule matching without moving settings or state. Explicitly
empty/invalid directories now fail instead of selecting another project. Default
Git discovery also uses the new anchored helper, so inherited `GIT_*` overrides
no longer redirect it; `CLAUDE_PROJECT_DIR` retains precedence.

All child capabilities remain unverified in this slice, including inherit-all
types and children that may actually have capy tools. They can make native calls
with advisory guidance. Task 5a must add its bounded one-use observations before
renewed child redirects are claimed; D5 still tracks authoritative pre-call
tool-pool discovery. No vault behavior, database/key access, new hook platform,
dependency version or search/indexing/chunking/executor behavior changed.
Benchmarks are not applicable to this task.

## Task 5a: One-use child tool observations

Implemented 2026-10-10. Scope: bounded project observation state, stable session
identity metadata, successful PostToolUse recording, suitable child redirects,
SessionEnd cleanup, and synchronized routing instructions.

### Behavior and coverage

- Stable transcript/payload/environment session IDs can record observations;
  PID fallbacks and absent child IDs cannot. Explicit pid-like IDs remain stable
  when their source is stable. Unsafe IDs use the shared component mapping.
- Only execute/fetch tool names and their MCP-qualified forms mint observations.
  Protocol error/interruption/cancellation flags, malformed flags, failure event
  names, serialized MCP errors, absent responses and other tools do not renew
  existing evidence. Returned prose is not searched for error words. The official
  [hook reference](https://code.claude.com/docs/en/hooks#posttooluse) and Context7
  describe PostToolUse as a success event with an arbitrary tool response; live
  Claude sessions were not used for verification.
- Clock-controlled tests cover 59.999-second validity, strict 60-second expiry,
  independent execute/fetch clocks, and consumption of all alternatives before
  one redirect. Fetch alone cannot redirect arbitrary Bash HTTP. WebFetch accepts
  either tool for absolute HTTP(S) URLs; Git issue/PR and invalid URL paths retain
  native advice without consuming evidence. Existing denies/asks run first.
- Tests cover 128-entry admission/oldest eviction, the 64 KiB serialized bound,
  oversize reads, malformed/future/negative state, duplicate or unsafe identities,
  unknown tools, FIFO/symlink/directory rejection and unwritable state. Failed
  consumption writes return no tool and retain the old file.
- Concurrent updates retain every committed observation, and concurrent consumes
  return evidence exactly once. A separate helper process holds the lock while
  another attempt reaches the 20 ms deadline and falls back; the same lock inode
  survives later consumption. A fixed staging name recovers a simulated killed
  writer's leftover without accumulating files or sweeping the directory.
- Separate capy CLI processes exercise first-call advice, success → redirect →
  failed retry → native fallback, and SessionEnd removal with live sibling
  sessions retained. Both database keys are unset and isolated homes/data paths
  remain free of knowledge/vault database artifacts. Unit cleanup checks also
  preserve expired sibling entries, proving SessionEnd does not sweep them.
- Generated whole-file and merge-idempotence checks pass with both routing
  representations synchronized. macOS compilation checks the new Unix APIs;
  runtime lock tests were performed on Linux only.

### Verification commands

Test runs use `CLAUDE_PROJECT_DIR=`, `GOCACHE=/tmp/capy-go-build`,
`CGO_ENABLED=1`, `CAPY_DB_KEY=test-key-for-development`, and
`CAPY_VAULT_KEY=test-key`. The broad run uses an empty temporary
`XDG_CONFIG_HOME` and local socket permission for config/server fixtures.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/...` | Passed after initial implementation: hook 0.456s, adapter 0.002s. |
| `go test -race -tags fts5 -count=1 ./internal/hook/... ./internal/adapter/... ./internal/platform/... ./cmd/capy/... -run 'Test(Observation\|Observed\|HandleSessionEnd\|SessionIDStability\|HookObservationsAcrossProcesses\|Routing\|GeneratedWholeFileArtifacts\|MergedArtifactsAreIdempotent)'` | Passed after fixed staging-file and wording updates: hook 2.417s, adapter 1.010s, platform 1.015s, CLI 2.525s. |
| `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go test -tags fts5 -c -o /tmp/capy-task5a-darwin-hook.test ./internal/hook` | Passed; compile-only verification. |
| `go test -tags fts5 ./...` | All packages passed: CLI 225.873s, hook 0.838s, adapter 0.003s, config 0.054s, platform 0.264s, server 114.407s, vault 255.128s. |
| `git diff --check` | Passed after documentation updates. |

An initial generated-wording assertion required the phrase "native fallback";
both generator and committed copy now state that behavior explicitly. No runtime
assertion was removed or relaxed. The write-permission test skips only when run
as root; the reported Linux runs used the unprivileged development account.

### Review and compatibility

The [isolated reviewer](.reviews/task-5a-code-review-2026-10-10.md) approved with no
P0–P3 findings. PAL was unavailable. No findings or new conventions require
knowledge indexing because the state invariants are recorded in the design,
architecture and repository instructions.

The first child native call can remain advisory. Observations describe recent
host/MCP success, not permanent availability or semantic success of every nested
operation. D5 remains deferred. Failed or unavailable state access stays advisory;
invalid data is not silently trusted or overwritten. The lock coordinates capy
hook processes; external replacement of the project directory is outside that
guarantee. The 20 ms bound applies to lock contention, not an OS disk-I/O deadline.

SessionEnd now removes its observation entries, but still performs no knowledge
checkpoint or database access. State is ephemeral project metadata with no tool
contents, remains covered by the existing `.capy/**` ignore rule, and requires no
database migration or key. No dependency version, indexing, chunking, retrieval
or executor code changed; retrieval benchmarks are not applicable.

## Task 6: Batch heredocs and captured stderr

Implemented 2026-10-10. Scope: unchanged command forwarding and deterministic
captured-stream presentation in both batch worker paths.

### Behavior and coverage

- Commands reach the executor without an appended `2>&1`. Terminal heredocs
  work with or without a trailing newline, including quoted delimiters whose
  body contains literal shell expansions.
- Both workers combine stdout followed by stderr, adding a newline only when
  needed at the stream boundary. Existing boundary newlines survive, and only
  two empty streams produce `(no output)`.
- Handler fixtures check exact persisted section titles/content at concurrency
  1 and 3. They cover heredocs, multiline commands, stdout-only, stderr-only,
  stderr emitted before stdout, all separator cases, empty output and partial
  output from a nonzero exit.
- Timeout fixtures check both persisted streams, serial cascading skips and
  parallel sibling completion. Existing worker-order, error-isolation,
  concurrency-clamping and sub-second timeout tests also pass under `-race`.
- Before the fix, these fixtures reproduced `EOF 2>&1` being indexed as heredoc
  body text and stderr disappearing, including before a timeout, in both paths.

### Verification commands

Tests use `CGO_ENABLED=1`, `GOCACHE=/tmp/capy-go-build`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.
The full suite additionally uses an empty temporary `XDG_CONFIG_HOME`, clears
`CLAUDE_PROJECT_DIR`, and has local socket access for HTTP fixtures.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/server -run 'TestBatchExecute_(CapturedStreams\|TimeoutCapturedStreams)$'` before the fix | Failed as expected in both paths; heredoc and stderr regressions reproduced. |
| `go test -race -tags fts5 -count=1 ./internal/server -run 'Test(Batch\|ExecuteBatch\|TruncateLabel)'` | Passed, 15.582s. |
| `go test -tags fts5 ./...` | All packages passed, with unchanged packages using cached results: CLI 227.358s, server 118.894s, hook 0.516s, platform 0.194s, vault 257.430s. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task6` | Passed: store 6.114s, server 0.603s, vault 0.550s. |
| `go run -tags fts5 ./cmd/qualstat bench-results/feat-upstream_sync.json bench-results/upstream-sync-task6.json` | Dataset verified; all retrieval and context-reduction metrics match the existing branch snapshot (`771bcca`). Current report reflects the working tree based on `9eca616`. |
| `git diff --check` | Passed after final documentation/task updates. |

The benchmark branch override preserves the existing report. This is a quality
comparison, not an executor latency/allocation claim; Task 20 still owns the full
feature's baseline performance comparison.

### Review and compatibility

The [isolated review](.reviews/task-6-code-review-2026-10-10.md) approved with no
P0–P3 findings. PAL was unavailable. No findings or conventions require knowledge
indexing because the behavior and its rationale are documented here, in the
implementation notes, README and architecture.

Batch sections now present stdout before stderr instead of relying on shell
redirection; their ordering does not represent runtime stream interleaving.
Capture/truncation limits remain owned by the executor. Existing serial timeout
budgets and parallel per-command timeouts retain their semantics. Task 18 still
owns provenance, response-budget and raw-byte-accounting changes. CLI flags,
credentials, database formats, CI configuration and generated artifacts are
unchanged.

## Task 6a: Consistent boolean request validation

Implemented 2026-10-10. Scope: shared boolean parsing in `coerce.go` and validation
at execute, fetch, cleanup and both search boundaries.

### Behavior and coverage

- Omitted arguments preserve their defaults. Native booleans and whitespace-
  trimmed, case-insensitive literal true/false strings are accepted. Explicit
  null, integer/float/JSON-number values, arrays, objects, empty/other strings,
  numeric strings and single-letter shortcuts are rejected with a parameter
  name; errors do not echo the supplied value.
- Handler tests cover all existing boolean arguments, both fetch modes,
  knowledge-only searches, star selectors and vault searches. Invalid values
  create no child marker, HTTP request or database file, initialize neither
  store nor vault, and leave usage counters and the search budget untouched.
- Invalid cleanup values also preserve an already-populated store and its stats
  when combined with real purge/optimize/vacuum requests. Omitted/string-true
  dry runs preserve sources; string-false dry runs evict them. Literal action
  strings preserve mutual exclusions and standalone reclamation behavior.
- Background string true detaches; false times out normally. Fetch string false
  uses the cache and string true issues another HTTP request, in both modes.
  Existing real-vault fixtures exercise mixed-case strings on both search tools.
- `TestMCPStdioBooleanArgs` builds and launches the real server, negotiates MCP,
  and sends JSON-RPC requests. It tests malformed values on every boolean,
  content preservation, no child marker, the search budget, destructive string
  false, background behavior and single/batch force. The force fixture uses a
  seeded cache entry and the real loopback SSRF refusal to prove bypass without
  permitting a network request. A separately imported/reindexed encrypted vault
  session proves false stays scoped and true finds another project's session.
  Invalid selectors still error when the vault is disabled or `project` is `*`.

### Verification commands

Tests use `CGO_ENABLED=1`, `GOCACHE=/tmp/capy-go-build`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.
HTTP handler fixtures have local socket access. Stdio fixtures isolate child
configuration, discovery directories and credentials. The stdio test harness
runs under `-race`; its child binary uses the existing helper's ordinary FTS5
build. The server handler tests themselves run under the race detector.

| Command | Result |
|---|---|
| `go test -race -tags fts5 -count=1 ./internal/server -run 'Test(Bool\|Search_ProjectOverrideScopes\|VaultSearch_ProjectOverrideScopes)'` | Passed, 8.705s. |
| `go test -race -tags fts5 -count=1 ./cmd/capy -run '^TestMCPStdioBooleanArgs$'` | Passed, 6.734s. |
| `go test -tags fts5 ./...` | All packages passed, with unchanged packages using cached results: CLI 226.500s, server 121.090s, hook 0.521s, platform 0.189s, vault 252.519s. Used an empty temporary `XDG_CONFIG_HOME` and cleared `CLAUDE_PROJECT_DIR`. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task6a` | Passed: store 5.707s, server 0.503s, vault 0.527s. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task6.json bench-results/upstream-sync-task6a.json` | Dataset verified; all 84 retrieval/context metrics match. Reports reflect the Task 6 working tree based on `9eca616` and this working tree based on `2f74147`. |
| `git diff --check` | Passed after final documentation/task updates. |

### Compatibility and scope

The [isolated review](.reviews/task-6a-code-review-2026-10-10.md) approved with
no P0–P3 findings. PAL was unavailable. No findings require knowledge indexing.

`dry_run: "false"` now intentionally performs requested eviction instead of
being ignored. Literal string background, force and cleanup action values now
take effect. The pinned `mcp-go v0.46.0` source confirms `GetBool` already accepted
string booleans, numeric truthiness and `strconv.ParseBool` shortcuts; numeric
`all_projects`, `"1"`, `"t"` and similar shortcuts now return validation errors.
Trimmed mixed-case true/false strings are supported uniformly. Native boolean
MCP schemas and omitted defaults are preserved.

No dependency upgrade, CLI option, database migration, generated artifact or
CI change was needed. The core is one parser; the five handler boundaries use
it before side effects. Future directory booleans remain Task 13. The plan
required no behavioral deviation, and no new project convention needs separate
knowledge indexing because the contract is documented here and in the design.
