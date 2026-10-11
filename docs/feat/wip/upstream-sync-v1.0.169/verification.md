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

## Task 7: Project cwd for every runtime

Implemented 2026-10-11. Scope: executor default cwd, Rust compile/run separation,
direct executor file-path anchoring and compatibility documentation.

### Behavior and coverage

- Regression-first fixtures failed on the original executor for relative file
  inputs, non-shell relative reads/writes, Rust execution cwd and all six Go
  project-module cases. The corrected executor passes the same assertions.
- Shell, JavaScript, Python, Go, Rust and Perl ran real project-relative reads
  and writes, plus `ExecuteFile` with relative and absolute input paths, while
  the test process stayed outside the selected project. Temporary scripts and
  compiler artifacts did not appear in the project.
- A recording shim invokes the real Rust compiler and proves compilation runs
  in the temporary directory, while the binary's current directory is the
  project and its executable/TMPDIR remain temporary. Foreground cleanup is
  verified after exit.
- A follow-up author check reproduced missing scripts/binaries with a relative
  inherited `TMPDIR` and a deeper project directory. Normalizing the newly
  created temporary directory to an absolute path fixes shell, Python and Rust
  execution and keeps their child `TMPDIR` absolute. The fixture also checks
  cleanup. The original cleanup defer is installed before normalization, so an
  absolute-path resolution error still removes the created directory.
- Offline Go fixtures cover project-local imports, relative `replace`, automatic
  vendor mode, `go.work` discovery, minimum Go requirements, and project toolchain
  selection. An invalid inherited `GOFLAGS` must be stripped. A compatible
  toolchain directive is tested separately from a future toolchain that cannot
  be downloaded with `GOPROXY=off`.
- Elixir's Mix marker detection and project isolation pass without a runtime.
  TypeScript, Ruby, PHP, R and Elixir execution cases are skipped because their
  runtimes are absent on this host. They remain runnable fixtures; install the
  corresponding runtime and rerun `TestExecuteProjectCwd` and, for Elixir,
  `TestExecuteElixirProjectPaths` to close that validation gap. No unsupported
  runtime is silently treated as tested.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.

| Command | Result |
|---|---|
| `go test -tags fts5 ./internal/executor -run 'TestExecute(ProjectCwd\|GoProjectModules\|RustCompileDirectory\|ElixirProjectPaths)\|TestAutoWrapElixir' -v -count=1` | Passed, 0.965s; runtime skips listed above. |
| `go test -race -tags fts5 -count=1 ./internal/executor/...` | Passed, 4.245s; final temporary-path fix and added fixture also passed, 4.398s. |
| `go test -race -tags fts5 ./internal/server -run '^TestExecute' -count=1` | Passed after the temporary-path fix, 7.353s. |
| `env -u CLAUDE_PROJECT_DIR XDG_CONFIG_HOME=/tmp/capy-task7-config make test` | All packages passed with socket access: CLI 231.314s, server 122.258s, executor 3.289s, vault 258.313s; unchanged packages used cached results. |
| `go vet -tags fts5 ./internal/executor/...` | Passed. |
| `make build` | Passed before and after the temporary-path follow-up; initial build reported a non-fatal read-only module-stat-cache warning. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task7` | Passed: store 5.703s, server 0.534s, vault 0.554s. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task6a.json bench-results/upstream-sync-task7.json` | Dataset verified; all 84 retrieval/context metrics match. |
| `gofmt -l internal/executor/executor.go internal/executor/cwd_test.go` and `git diff --check` | Passed; no formatting or whitespace errors. |

The first `make test` ran inside the restricted sandbox and failed existing
local-socket fixtures (`operation not permitted`) and CLI vault import fixtures
that inherited a 196,608-byte global minimum-size setting. The rerun uses local
socket access, clears `CLAUDE_PROJECT_DIR`, and points `XDG_CONFIG_HOME` to an
empty temporary directory. Executor tests passed in the restricted run.
Existing CLI fixture dependence on inherited configuration is outside this
executor slice; a future test-harness fix should set an isolated config directory
and clear project-selection overrides in the shared CLI launcher itself.
The full rerun started before the temporary-path follow-up; the final change was
then verified with the complete executor race suite, server execution race subset,
vet and build listed above.

Quality reports represent the Task 6a working tree based on `2f74147` and the
Task 7 working tree based on `62a2c48`. The feature-wide comparison against
`c0bfff2` remains Task 20.

Executor overhead smoke measurements used
`go test -tags fts5 ./internal/executor -run '^$' -bench '^BenchmarkExecutorOverhead$/(shell|python)/(executor|direct)$' -benchmem -count=3`
before and after the production edit. Median shell executor time was
0.954 → 1.085 ms; Python was 10.140 → 10.965 ms. Direct-process controls also
slowed (shell 0.763 → 0.811 ms; Python 9.830 → 11.253 ms). Allocations stayed
near 136/133 KB per execution. The after-run overlapped the full suite and build;
these three-sample results are a noisy smoke check, not statistically established
regressions. Task 20 must rerun the planned controlled performance comparison.

### Compatibility and scope

Relative non-shell writes now land in the selected project. Scripts needing
disposable output must explicitly use a temporary path. Go now honors the
selected project's module/workspace and toolchain requirements; inherited
`GOFLAGS` remains stripped. Rust compilation stays temporary. Elixir's wrapper
already used the selected project, so no wrapper change was necessary.

Per-call cwd overrides remain Task 8; the combined cwd/project-identity ADR
remains Task 20. No dependency, CLI, CI, database or generated-artifact change
was required. New conventions are already documented here and need no separate
knowledge entry.

The [isolated review](.reviews/task-7-code-review-2026-10-11.md) approved the
initial change and the subsequent absolute-temporary-path fix with no P0–P3
findings. PAL was unavailable. The author-sourced issue is fixed and documented
above; no findings require separate knowledge indexing.

## Task 8: Per-call execution cwd

Implemented 2026-10-11. Scope: optional `cwd` on execute, execute-file and batch,
shared directory validation, request-local executor handling, schemas and docs.

### Behavior and coverage

- Omission preserves the selected project. Supplied empty strings, nulls,
  numbers, booleans, arrays and objects fail; nonempty paths must name an
  existing directory. Relative paths anchor to the project. Absolute external
  directories are accepted. Spaces are literal, and symlink/parent paths retain
  filesystem semantics instead of being lexically collapsed first. Missing,
  file, dangling, NUL and file-before-parent inputs fail visibly.
- Invalid executor calls fail before temporary allocation or Rust compilation.
  Invalid server requests spawn no marker, initialize neither store nor vault,
  and do not update stats. Actual stdio calls verify these errors and advertise
  optional string `cwd` on all three MCP descriptors.
- The runtime matrix now covers default, relative and absolute cwd with normal
  execution and both relative/absolute execute-file inputs. A distinct input in
  the override directory proves `FILE_CONTENT` still comes from the selected
  project while relative outputs land in the requested directory.
- Six installed runtimes passed: shell, JavaScript, Python, Go, Rust and Perl.
  TypeScript, Ruby, PHP, R and Elixir remain unavailable. Their matrix entries
  are explicit skips; run `TestExecuteProjectCwd` after installing those runtimes.
  A source-capturing shim separately verifies Elixir Mix wrapping follows the
  override rather than the selected project. It does not claim real Elixir
  execution. An offline Go fixture proves module discovery follows the override
  even when the selected project requires an unavailable newer toolchain.
- Parallel subtests share an executor/server across different directories and
  both serial/parallel batch requests. Every output has the expected directory
  and marker; subsequent omitted-cwd calls return to the project. Parent cwd and
  environment remain unchanged. Stored chunks prove every batch command uses
  the requested cwd; short distinct command labels avoid deferred D3 collisions.
- Conflicting override-directory Read settings and dotenv credentials do not
  alter the selected policy or captured key. A lazy batch store opens only in
  the project and reopens with its selected key. Subsequent index/search still
  use the project; an external cwd does not grant an external execute-file read.
  A two-project archived vault fixture proves session searches remain scoped to
  the selected project after execution elsewhere.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`. The full
suite uses socket access, an empty `/tmp/capy-task8-config` as `XDG_CONFIG_HOME`,
and no inherited `CLAUDE_PROJECT_DIR`, as established in Task 7.

| Command | Result |
|---|---|
| `go test -race -tags fts5 ./internal/executor ./internal/server -run 'Test(ExecutionCwd\|ExecuteProjectCwd\|ResolveCwd\|ExecuteCwd)' -count=1` | Passed: executor 2.854s, server 3.896s. |
| `go test -race -tags fts5 ./internal/executor ./internal/server -run 'Test(Execute\|ExecutionCwd\|ResolveCwd\|BatchExecute\|Search_ProjectOverrideScopes\|VaultSearch_ProjectOverrideScopes)' -count=1` | Passed with the final Go module fixture: executor 4.587s, server 22.701s. |
| `go test -race -tags fts5 ./cmd/capy -run '^TestMCPStdioExecutionCwd$' -count=1` | Passed, 2.908s; child binary uses the stdio helper's normal FTS5 build. |
| `env -u CLAUDE_PROJECT_DIR XDG_CONFIG_HOME=/tmp/capy-task8-config make test` | All packages passed: CLI 233.909s, executor 4.840s, server 127.319s, vault 258.887s; unchanged packages used cached results. |
| `go vet -tags fts5 ./internal/executor/...` | Passed. |
| `go vet -tags fts5 ./internal/executor/... ./internal/server/...` | Reports an existing lock-copy warning in unchanged `internal/server/tool_boolean_test.go:110`; see follow-up below. |
| `make build` | Passed with a non-fatal read-only module-stat-cache warning. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task8` | Passed: store 5.954s, server 0.605s, vault 0.596s. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task7.json bench-results/upstream-sync-task8.json` | Dataset verified; all 84 retrieval/context metrics match. |
| `git diff --check` | Passed. |

Benchmark reports represent the Task 7 working tree based on `62a2c48` and this
Task 8 working tree based on `0f538b6`. The controlled feature-wide performance
comparison against `c0bfff2` remains Task 20.

### Recorded follow-up and compatibility

Server-wide vet reports a pre-existing `copylocks` warning: the boolean cleanup
fixture passes a `SessionStats` snapshot, which contains `sync.Mutex`, by value
to `assert.Equal`. The same statement exists at `HEAD` before Task 8. This is
deferred because it belongs to unrelated stats/boolean test code; update that
fixture to compare snapshot counter fields or snapshot pointers, then rerun
server vet. Do not remove or weaken its assertion that invalid cleanup leaves
all statistics unchanged.

Execution cwd is a per-call location, not project selection. Relative
execute-file inputs remain project-relative. There is no automatic tilde/env
expansion, project inference from transcripts, extra Read grant, or global cwd
mutation. The combined cwd/project-identity ADR remains Task 20. No dependency,
CLI, CI or generated-artifact changes were required, and no new convention
needs a separate knowledge entry because these contracts are documented here.

The [isolated review](.reviews/task-8-code-review-2026-10-11.md) initially noted
one P3 about positional string fields in the new runtime matrix. Those three
literals now use named fields with identical values/assertions; the reviewer
confirmed the fix and approved with no remaining findings. The matrix was
rerun under `-race` after that test-only edit. PAL was unavailable. No review
finding or convention needs separate knowledge indexing.

## Task 9: Bounded background output

Implemented 2026-10-11. Scope: detached output retention, process/pipe ownership,
eventual cleanup and the ADR-005 amendment. Remaining sync tasks stay pending.

### Behavior and coverage

- Each stream atomically snapshots, drops its backing allocation and permanently
  discards later writes. A 256 MiB post-transition buffer fixture retains zero
  bytes/capacity and allocates nothing per write. Concurrent writers cannot
  restore retained content or its byte counter after the transition.
- A detached shell writes 4 MiB to each stream, then creates a progress marker
  and verifies its temporary script still exists. Natural exit and shutdown
  remove the directory and retire its registry entry. The partial response
  preserves both streams captured before detachment.
- Two Go readers retain pipe ownership through detachment. Exit observation does
  not reap the leader; both readers finish before the one final `cmd.Wait`.
  Reaping, registry changes and all group signals share a mutex. Exited-leader
  fixtures retain a pipe-owning descendant and verify foreground timeout, the
  100 MiB hard cap and background shutdown still terminate the group. The leader
  remains unreaped before cleanup, reserving its PID.
- Repeated exit/cancellation races retire the waiter and tracking. An injected
  observer EPERM tests attached error propagation, detached warning logging,
  no duplicate attached logging, and cleanup in both cases.
- Foreground timeout/truncation and the existing runtime/cwd matrix pass. No
  CLI options, schemas, CI or generated setup/routing artifacts changed. The
  existing pinned `x/sys` module supplies the observation APIs.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key` unless
cross-compiling the executor-only test binary with `CGO_ENABLED=0`.

| Command | Result |
|---|---|
| `go test -race -tags fts5 -count=1 ./internal/executor/...` | Passed after the final review fix, 7.641s. |
| `go test -race -tags fts5 ./internal/executor -run '^Test(SafeBufferConcurrentSnapshotAndDiscard\|BackgroundExitCancellationRace\|ExitedLeaderRetainsGroupOwnership)$' -count=10` | Passed, 15.268s. |
| `go test -race -tags fts5 ./internal/server ./cmd/capy -run 'Test(BatchExecute\|Execute\|MCPStdioBooleanArguments\|MCPStdioExecutionCwd)' -count=1` | Passed: server 17.500s, CLI 3.039s. |
| `env -u CLAUDE_PROJECT_DIR XDG_CONFIG_HOME=/tmp/capy-task9-config make test` | Final reviewed tree passed every package: CLI 223.355s, executor 5.252s, server 119.418s; unchanged packages used cached results. |
| `go vet -tags fts5 ./internal/executor/...` | Passed after the final review fix. |
| `GOOS=darwin GOARCH=arm64 go test -tags fts5 -c -o /tmp/capy-task9-darwin.test ./internal/executor` | Cross-compiled successfully; no Darwin runtime validation. |
| `GOOS=linux GOARCH=arm64 go test -tags fts5 -c -o /tmp/capy-task9-linux-arm64.test ./internal/executor` | Cross-compiled successfully. |
| `make build` | Passed; non-fatal read-only module-stat-cache warning. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task9` | Passed for store, server and vault. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task8.json bench-results/upstream-sync-task9.json` | Dataset verified; all 84 retrieval/context metrics match. |
| `git diff --check` | Passed. |

The initial full-suite run inside the sandbox failed on prohibited local
`httptest` sockets. Running with socket access, no inherited `CLAUDE_PROJECT_DIR`
and `XDG_CONFIG_HOME=/tmp/capy-task9-config` passed every package, including the
final rerun after the error-path review fix. Its log is
`/tmp/capy-task9-full-tests-reviewed.log`.

### Executor measurements and limits

Compared the reviewed working tree against a temporary copy of the executor at
`6b6e7f7`, using `go test -tags fts5 -run '^$' -bench
'^BenchmarkExecutorOverhead$/./executor$' -benchmem -count=3 ./internal/executor`.
Median measurements:

| Runtime | Baseline time | Reviewed time | Baseline allocations | Reviewed allocations |
|---|---:|---:|---:|---:|
| Shell | 1.015 ms | 0.949 ms | 342 | 329 |
| JavaScript | 20.136 ms | 21.023 ms | 363 | 352 |
| Python | 10.618 ms | 10.324 ms | 351 | 339 |

Three samples with other test work running are a smoke measurement, not a
statistical speedup/regression claim. JavaScript's median is 4.4% higher; these
samples cannot establish whether that is a regression. Allocations decrease in
every runtime.
The controlled feature-wide comparison against `c0bfff2` remains Task 20.
Logs are `/tmp/capy-task9-baseline-performance-final.log` and
`/tmp/capy-task9-performance-reviewed.log`; the quality report is
`bench-results/upstream-sync-task9.json` (an ignored benchmark artifact).

The broader pre-existing executor-overhead benchmark fails identically on the
baseline and changed tree: its JavaScript **direct** case uses `node -c` with
inline code, which Node treats as a filename. The narrowed command above selects
the working executor cases. Follow-up for Task 20: choose the runtime's correct
inline-code flag (`node -e`) in `bench_test.go` before comparing the direct case;
do not weaken its failure assertion. `benchstat` was unavailable.

### Review and plan refinement

The [isolated review](.reviews/task-9-code-review-2026-10-11.md) approved the final
implementation. Its two P1 process-lifetime findings and one P2 error-visibility
finding were fixed and re-reviewed; none remains deferred. PAL was unavailable.

The original S-sized plan assumed `exec.Cmd`'s internal readers could remain
unchanged. Its reap-before-drain ordering made that incompatible with both
descendant cleanup and safe PID ownership. The task expanded to M: explicit
readers and non-reaping Linux/Darwin exit observation provide the required
ordering. The design, implementation and ADR now state that refinement, while
preserving ADR-005's historical decision. No pipes are closed or replaced at
detachment. These findings are already documented, so no duplicate knowledge
notes were indexed. Shutdown remains best-effort SIGTERM without a new escalation
policy; a child that ignores it still owns its live resources.

## Task 10: Literal knowledge source selectors

Implemented on 2026-10-11. `knowledgeFilterClauses` binds an escaped literal
substring with SQL `ESCAPE '\'`; exact selectors still bind the original label
with equality. The shared retrieval engine and vault matching are unchanged.

### Behavioral evidence

`TestSearchLiteralSourceFiltering` indexes an ephemeral literal label and
durable decoys containing the same content. Nine selector cases cover `%`, `_`,
backslashes, a trailing backslash, backslash/wildcard combinations, quotes,
SQL-looking text and ordinary partial labels. Each runs porter-only, trigram-only,
fuzzy and AND-to-OR fallback queries, asserting the returned label and match
layer. A durable-only kind request still returns the explicitly selected
ephemeral source. Exact full labels match; exact fragments do not.

Before the production fix, the four wildcard-bearing selector cases failed in
all four retrieval paths by returning unrelated labels. The other cases and
exact-mode checks passed. All fixtures pass after the fix. The initial porter
fixture used a synonym-expanded word that also matched trigram; using
`ponies` against `pony` makes the porter-only assertion precise.

### Verification commands

Go commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/store -run '^TestSearchLiteralSourceFiltering$'` before the fix | Expected regression failures in 16 wildcard/path combinations; log `/tmp/capy-task10-regression-before.log`. |
| `go test -tags fts5 -count=1 ./internal/store/... ./internal/retrieval/...` | Passed: store 64.338s, retrieval 0.104s. |
| `go test -race -tags fts5 -count=1 ./internal/store/... ./internal/retrieval/...` | Passed: store 69.729s, retrieval 1.100s. |
| `go vet -tags fts5 ./internal/store/... ./internal/retrieval/...` | Passed. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task10-before` before the fix | Passed: store 5.384s, server 0.502s, vault 0.489s. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task10` | Passed: store 5.233s, server 0.474s, vault 0.479s. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task10-before.json bench-results/upstream-sync-task10.json` | Dataset verified; all 84 retrieval/context metrics unchanged. |
| `git diff --check` | Passed. |

Quality reports are ignored benchmark artifacts. Their metadata names the same
HEAD (`1ce1ba6`); the `before` report was generated before changing production
code and the other report includes the working-tree fix. Logs are under
`/tmp/capy-task10-{tests,race,vet,quality,quality-before,quality-comparison}.log`.
This slice validates the affected store/retrieval packages and existing quality
fixtures. Full feature-wide performance comparisons and validation remain Task
20. There are no dependency, schema, CLI, CI or generated-artifact changes.

### Review and scope

The [isolated code review](.reviews/task-10-code-review-2026-10-11.md) approved
the change with no findings. The reviewer independently inspected the diff,
retrieval integration and documentation; test execution was performed by the
implementing session. PAL was unavailable, so the skill's single-reviewer
fallback was used. No findings or new project conventions need indexing.

The implementation follows Task 10 without expanding runtime scope. The new
README paragraph documents that callers relying on wildcard selectors must now
use literal label fragments. Task 11 and later work remain pending.

## Task 11: Plaintext chunk byte cap

Implemented on 2026-10-11. Plaintext's short-output, blank-section and fixed-line
strategies now split their output into at most 4,096-byte chunks, choosing line,
Unicode whitespace and UTF-8 boundaries in that order. Fixed groups retain
ordinary two-line overlap and skip oversized lines when selecting overlap.
Titles remain deterministic and valid UTF-8; empty-source fallback remains one
bounded empty chunk. Malformed JSON follows the same plaintext path.

### Behavioral evidence

`chunk_plaintext_test.go` covers 4,096/4,097 bytes, 4,097/5,000-byte blank sections,
19 huge lines, a long single line, two-/three-/four-byte characters, Unicode
whitespace, title truncation and empty input. Content comparisons preserve every
non-whitespace character. Fixed-line fixtures count ordinary overlap markers
and verify a huge overlap line's 5,000 characters occur exactly once.

Encrypted-store fixtures check the byte length (SQL `CAST(content AS BLOB)`) and
row count in both `chunks` and `chunks_trigram`, then retrieve tail markers from
each oversized strategy and malformed JSON. Whitespace-only fallback cannot
reintroduce the original large input. The legacy fixture seeds an oversized
chunk, closes/reopens the store, verifies same-hash indexing keeps it, and then
verifies explicit eviction/reindex creates bounded chunks. The original
source-size, sanitization and hash/dedup tests also pass.

Before the production fix, the new regressions failed on oversized stored
content, split expectations, invalid titles, repeated huge overlap lines and
legacy reindexing. The focused tests pass after the fix. The fuzz target checks
the cap, non-whitespace retention and valid UTF-8, including invalid-byte seeds
to exercise progress without imposing a new invalid-input contract.

### Verification commands

Go commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.

| Command | Result |
|---|---|
| `go test -tags fts5 ./internal/store -run 'TestChunkPlainText\|TestIndexPlaintextByteCap\|TestPlaintextLegacyChunksRequireReindex'` before the fix | Expected regression failures; `/tmp/capy-task11-before.log`. |
| Same focused command with `-count=1` after the fix | Passed, 3.839s. |
| `go test -tags fts5 -count=1 ./internal/store/... ./internal/retrieval/...` | Passed: store 65.857s, retrieval 0.077s. |
| `go test -race -tags fts5 -count=1 ./internal/store/... ./internal/retrieval/...` | Final version passed: store 72.955s, retrieval 1.096s; initial version also passed. |
| `go vet -tags fts5 ./internal/store/... ./internal/retrieval/...` | Passed. |
| `go test -tags fts5 ./internal/store -run '^$' -fuzz '^FuzzPlaintextByteCap$' -fuzztime=10s -parallel=2` | Final version passed, 99 executions after six corpus entries; initial run passed with 107 executions. Short smoke runs, not exhaustive coverage. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task11-before` before the fix | Passed across store/server/vault. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task11` | Passed across store/server/vault. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task11-before.json bench-results/upstream-sync-task11.json` | Dataset verified; all 84 quality/context metrics unchanged. |

Quality reports are ignored artifacts with HEAD metadata `eba76ec`: the baseline
was generated before production edits and the target includes the working-tree
implementation. Their existing corpus remains unchanged; new pathological size
and retention cases are regression fixtures rather than changes to that corpus.
Logs are `/tmp/capy-task11-{before,focused,tests,race,vet,fuzz,quality-before,quality,quality-comparison}.log`.
After removing temporary splitter-slice allocations, repeated the full affected
race suite, vet, fuzz and quality checks; all passed. Those logs use
`/tmp/capy-task11-{race,vet,fuzz,quality}-final.log`.

### Performance comparison

Ran the same added pure-chunker benchmarks and existing plaintext indexing
benchmark before production edits and after the implementation, using
`go test -tags fts5 ./internal/store -run '^$' -bench 'BenchmarkChunkPlainText|BenchmarkIndex/plaintext' -benchmem -benchtime=200ms -count=6`.
The final run did not overlap other test/benchmark jobs. Six-sample medians:

| Fixture | Before time | After time | Before/after B/op | Before/after allocs/op |
|---|---:|---:|---:|---:|
| Existing plaintext indexing | 4.097 ms | 4.199 ms | 63,498 / 63,702 | 661.5 / 661.5 |
| Normal fixed lines | 12.82 µs | 12.48 µs | 5,187 / 5,192 | 12 / 12 |
| 100 KB single line | 1.24 µs | 25.17 µs | 80 / 3,936 | 2 / 7 |
| Twenty oversized blank sections | 34.11 µs | 55.81 µs | 8,304 / 12,414 | 31 / 32 |
| 300 KB Unicode line in overlap | 104.09 µs | 1,309.21 µs | 608,876 / 321,337 | 10 / 14 |

The oversized fixtures previously returned unbounded chunks without doing the
required splitting; the added scanning and chunk allocations account for their
increased cost. Skipping the repeated huge overlap line cuts its allocated bytes
by about 47%. An initial version added six allocations to normal fixed-line
output; appending into the destination slice removed that overhead. Normal
chunking/indexing medians are within 3% of baseline. `benchstat` is unavailable,
so these are descriptive medians, not claims of statistical significance.
Logs: `/tmp/capy-task11-perf-before.log`, `/tmp/capy-task11-perf.log` (initial
version), `/tmp/capy-task11-perf-final.log` (destination-slice version).

No module, schema, CLI, CI or generated artifacts change. Markdown/structured
JSON splitting remains D4; the cap is not a universal database-size guarantee.
The README documents explicit remove/reindex repair without an implicit
migration. Full feature-wide comparisons against `c0bfff2` remain Task 20.

### Review and scope

The [isolated code review](.reviews/task-11-code-review-2026-10-11.md) approved
both the initial change and the destination-slice revision with no findings.
PAL was unavailable, so the skill's independent single-reviewer fallback was
used. The implementing session completed the final verification reruns after
review. `git diff --check` passes. No novel conventions or findings require
knowledge indexing; the relevant decisions are captured in these feature docs.

The implementation follows Task 11's narrow plaintext scope. Measurement caught
avoidable temporary-slice allocations, which were removed before completion.
The remaining cost on oversized inputs is explicit splitting work; existing
quality fixtures are unchanged. Task 12 and later work remain pending.

## Task 12: Shared bounded file ingestion

Implemented 2026-10-11. MCP file indexing delegates to `internal/knowledge`;
initial ingestion and stale refresh share the dependency-free bounded reader in
`internal/fileio`. Inline indexing stays in the MCP handler. Existing lexical
absolute labels, durable kind, file-backed refresh and store-owned deduplication
are preserved. The selected project still owns policy, credentials and storage.

### Behavioral evidence

- Deterministic read seams grow an admitted file after descriptor stat. The
  reader stops at the configured raw-byte limit plus one detection byte, before
  sanitization, hashing or indexing. Partial data never replaces cached content.
- Before adding the bound, the extracted reader consumed 1,024 bytes for a
  64-byte ingestion limit. The stale fixture consumed 1,061 bytes and replaced
  the cached source because sanitization reduced that input below the store
  limit. Both regressions fail against the extracted unbounded implementation
  and pass with the bound; they do not rely on timing races.
- Reader cases cover empty/exact/over-limit content, partial read errors and
  invalid/overflowing detection-byte limits. Ingestion covers FIFO, device and
  directory rejection, missing files, absolute input matching a relative deny,
  symlink denies, unavailable policy, relative traversal and explicit external
  files. Failed admissions do not initialize the database.
- Real encrypted-store fixtures verify canonical-label deduplication, durable
  sources, explicit labels, automatic refresh and close/reopen. MCP fixtures
  preserve inline-plus-path admission and byte/call accounting, including
  unchanged input and oversize rejection. Existing prepared-policy refresh
  tests still verify denied backing files retain cached search results.
- Growth and read-error refreshes retain content and index time while logging
  the skip. Already-oversized files never reach the reader. Cancellation checks
  prevent indexing after cancellation; they are not an OS I/O deadline.

### Verification commands

Go commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`. Full/focused
tests and quality runs clear inherited `CLAUDE_PROJECT_DIR` and set
`XDG_CONFIG_HOME=/tmp/capy-task12-config`; policy fixtures use temporary homes.
Commands ran with writable temporary storage and local test socket access.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/fileio ./internal/knowledge ./internal/store -run 'TestReadAll\|TestIndexFile\|TestStaleRefreshBoundedReads'` before bounding the extracted reader | Expected failures: excessive reads, growth offered for indexing, and stale-cache replacement. `/tmp/capy-task12-regression-before.log`. |
| `go test -tags fts5 -count=1 ./internal/fileio ./internal/knowledge ./internal/store ./internal/server -run 'TestReadAll\|TestIndexFile\|TestStaleRefreshBoundedReads\|TestStaleDetection\|TestIndex_\|TestPreparedReadPolicy'` | Passed: 0.002s / 2.082s / 6.413s / 6.981s respectively. |
| `make test` | All packages passed. CLI 252.916s, server 140.011s, store 74.230s, vault 296.601s, TUI 22.223s; some unchanged packages used cache. |
| `go test -race -tags fts5 -count=1 ./internal/fileio ./internal/knowledge ./internal/store ./internal/server` | Passed: 1.009s / 4.132s / 86.524s / 143.573s respectively. |
| `go vet -tags fts5 ./internal/fileio ./internal/knowledge ./internal/store` | Passed. |
| Same vet command including `./internal/server` | Reports only the pre-existing `tool_boolean_test.go:110` lock-copy warning; see follow-up below. A new test diagnostic's lock copy was fixed before final vet/review. |
| `make build` | Passed with the required FTS5 tag. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task12-before` before production edits | Passed across store/server/vault. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task12` | Passed across store/server/vault. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task12-before.json bench-results/upstream-sync-task12.json` | Dataset verified; all 84 retrieval/context metrics unchanged. |
| `git diff --check` | Passed. |

Quality reports are ignored artifacts whose HEAD metadata is `9d5c659`; the
baseline predates production changes and the target includes this working tree.
Logs are `/tmp/capy-task12-{focused,full-tests,race,vet-final,vet-packages,build,quality-before,quality,quality-comparison}.log`.

### Reader performance

Compared the extracted unbounded reader with the final bounded reader using
`go test -tags fts5 ./internal/fileio -run '^$' -bench BenchmarkReadAll -benchmem
-benchtime=200ms -count=6`. Neither reader measurement overlapped test/benchmark
runs. Six-sample medians on Linux/amd64, Go 1.26.4:

| Input | Unbounded time | Bounded time | Before/after B/op | Before/after allocs/op |
|---|---:|---:|---:|---:|
| 4 KiB | 1.704 µs | 1.833 µs | 10,400 / 10,424 | 10 / 11 |
| 2 MiB | 690.184 µs | 643.993 µs | 4,702,006 / 4,702,034 | 27 / 28 |

The limiter adds one allocation (24 bytes in the small fixture). The small-read
median rises about 7.6%, or 0.13 µs; the larger-read median falls about 6.7%.
These in-memory reader measurements exclude filesystem, policy and encryption
costs. `benchstat` is unavailable, so these are descriptive results, not
statistical speedup/regression claims. Logs:
`/tmp/capy-task12-perf-before.log`, `/tmp/capy-task12-perf-after.log`.

### Review, follow-up and scope

The [isolated review](.reviews/task-12-code-review-2026-10-11.md) approved the final
code with no remaining P0–P3 findings. PAL was unavailable; an independent
code-reviewer performed the static review. Its README logging clarification was
applied and checked. The implementing session performed verification.

The server-vet warning is the same unrelated `SessionStats` comparison recorded
under Task 8. It remains deferred to avoid expanding this ingestion slice:
replace `assert.Equal` on the mutex-bearing value at `tool_boolean_test.go:110`
with comparison of all counter fields or snapshot pointers, preserving the
assertion that invalid cleanup leaves every statistic unchanged, then rerun
server vet. No new unresolved review findings remain.

No plan deviation was needed. The shared low-level reader avoids an import cycle;
the sanitization fixture exposed why checking only the eventual indexed size is
insufficient. D2's policy/open race remains explicit at both read sites. Task 12a
still owns aggregate stale-refresh bounds; directory/CLI work remains later.
Schemas, dependencies, generated artifacts and ranking are unchanged. CLI and CI
rubric topics are N/A because no commands, flags or workflows change. Relevant
decisions are captured here, so no duplicate knowledge note was indexed.

Optional editorial follow-up: `kk:clarify-docs` on `README.md` and
`docs/architecture.md`; this is not an unfinished Task 12 requirement.

## Task 12a: Bounded persistent stale refresh

Implemented 2026-10-11. Migration 020 adds `sources.file_check_seq` and the
partial scheduling index after column admission. Refresh now selects at most
32 oldest-checked rows, closes its cursor, then admits file checks within the
byte and time bounds. Scheduling survives store/process-like restarts; denied,
missing and unchanged sources advance, while unattempted sources stay eligible.

### Behavioral evidence

- An encrypted pre-020 database migrates under two concurrent store opens,
  retains its rows, and reopens idempotently. Injected migration failure rolls
  back the column and index together. Old-style inserts omitting the column
  receive zero. This tests schema/SQL compatibility, not a historical binary;
  the broader old-reader matrix remains Task 20.
- `EXPLAIN QUERY PLAN` confirms the partial index serves both bounded oldest
  selection and descending maximum lookup, without a temporary sorting tree.
  A 70-source fixture progresses across fresh store instances. A separate
  10,000-real-file fixture visits every source with at most 32 attempts per pass
  and immediately admits a later-added high-ID source with sequence zero.
- Maximum possible content plus its detection byte is reserved before open;
  only actual reads are charged. Changed, unchanged, failed and oversized reads
  all consume the allowance, including probe bytes. With a 3 MiB source limit,
  two full reads leave insufficient room for a third reservation and the
  remaining sources stay unchecked. A configured 9 MiB real file fits as the
  first attempt. An overflowing detection-byte limit admits no file.
- Clock seams cover metadata latency outside the 100 ms admission window,
  stopping new work at the boundary, and a slow read spanning the five-second
  cooldown. Channels prove the latter cannot admit an overlapping pass in the
  same store. Cancellation before/during work leaves unprocessed rows eligible;
  an MCP request cancellation reaches the store and preserves cached chunks.
- Same-backing-file replacements retain scheduling; different files reset it,
  even for identical content. Real writes during a read exercise deletion,
  inline/file replacement, same-hash path/kind changes and unchanged-content
  completion. Identity guards prevent overwrite, resurrection and advancing a
  replacement that was never checked. Two stores may both check the same files
  but allocate independent sequences transactionally.
- Injected scheduling-write errors and sequence exhaustion stop after the
  first attempted source, log the cause and leave later rows eligible. Existing
  Task 12 growth/cached-content and prepared-policy tests continue to pass.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-build`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`. Server/full
test and quality commands clear inherited `CLAUDE_PROJECT_DIR` and use
`XDG_CONFIG_HOME=/tmp/capy-task12a-config`. Tests own temporary files, policies
and encrypted databases. Full and server race suites ran with local socket access.

| Command | Result |
|---|---|
| `go test -tags fts5 -count=1 ./internal/store -run 'TestStale\|TestMigrat\|TestIndex'` | Passed, 12.159s. The first compile exposed a still-needed `slog` import after moving refresh code; restored before this run. |
| `go test -tags fts5 -count=1 ./internal/store ./internal/server -run 'TestStaleSchedule\|TestSearchCancellationSkipsStaleRefresh'` | Passed: store 12.815s, server 0.349s. |
| `make test` | Every package passed: CLI 252.112s, server 146.487s, store 97.912s, vault 297.268s; some unchanged packages used cache. |
| `go test -race -tags fts5 -count=1 ./internal/store ./internal/server ./internal/knowledge` | Passed: 123.613s / 150.075s / 3.254s respectively. |
| `go vet -tags fts5 ./internal/store ./internal/knowledge` | Passed. |
| Same vet command including `./internal/server` | Only the pre-existing `tool_boolean_test.go:110` mutex-copy warning; follow-up below. |
| `make build` | Passed. Non-fatal read-only module-stat-cache warning; binary build exited successfully. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task12a-before` before production edits | Passed across store/server/vault. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task12a` | Passed across store/server/vault. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task12a-before.json bench-results/upstream-sync-task12a.json` | Dataset verified; all 84 retrieval/context metrics unchanged. |
| `git diff --check` | Passed. |

Quality reports are ignored artifacts with HEAD metadata `1f4b90f`; the baseline
predates production changes and the target includes this working tree. Logs:
`/tmp/capy-task12a-{existing,focused,full-tests,race,vet,vet-store,build,quality-before,quality,quality-comparison}.log`.

### 10,000-source workload measurements

Ran the same added fixture before production changes and after the reviewed
implementation using `go test -tags fts5 ./internal/store -run '^$' -bench
'^BenchmarkStaleRefreshWorkload$' -benchmem -benchtime=1x -count=3`. Fixture setup
bulk-seeds source/FTS rows and creates 10,000 real backing files outside the timer;
the timed search uses production refresh, retrieval and writes. The changed tree
has 128 modified files. Neither measurement overlapped another test/benchmark run.

Three-sample medians on Linux/amd64, Go 1.26.4:

| Tree | Before / after latency | Before / after attempted files | Before / after allocated bytes | Before / after allocations |
|---|---:|---:|---:|---:|
| Unchanged | 93.403 / 45.330 ms | 10,000 / 32 | 12,970,136 / 240,080 | 189,997 / 3,647 |
| 128 changed | 645.022 / 154.885 ms | 10,000 / 14 | 14,535,192 / 365,320 | 226,229 / 5,562 |

Unchanged searches read zero content bytes in both versions. Changed searches
read 3,712 bytes before and 406 bytes afterward; the remaining changed files await
later passes. The after-run's 14 attempts reflect the admission deadline. Its
total search time exceeds 100 ms because the current operation can finish and
retrieval follows refresh; no total latency bound is claimed.

SQLite `total_changes()` is measured on a single pooled connection before/after
the timed search. It includes source/FTS maintenance and the normal access update:
unchanged goes from 1 to 33 changes, exposing the 32 added scheduling writes;
the changed tree goes from 7,265 to 1,489. These are changed-row events, not disk
page writes or bytes. Persistent fairness has real write overhead despite the
bounded work and lower allocations. `benchstat` is unavailable, so the medians
are descriptive, not statistical claims. Logs:
`/tmp/capy-task12a-perf-before.log`, `/tmp/capy-task12a-perf-after.log`.

### Review and scope

The [isolated review](.reviews/task-12a-code-review-2026-10-11.md) approved the code
with no P0–P3 findings. PAL was unavailable; an independent code-reviewer applied
the Go checklists and inspected tests. The implementing session ran verification
and wrote the subsequent documentation. No findings or new conventions need
duplicate knowledge indexing; the decisions and measurements are recorded here.

The plan needed mechanical request-context forwarding so cancellation reaches
actual MCP searches. Guarding only the scheduling update would have left a stale
read able to overwrite a newer source, so the indexing transaction also checks
the selected incarnation. Both implement the specified contracts. The pre-existing
server-vet warning remains the Task 8/12 follow-up: compare all snapshot counters
or snapshot pointers instead of copying the mutex-bearing value, then rerun vet;
preserve the invalid-cleanup assertion. It is outside this scheduling change.

D2 remains deferred. Directory ingestion and CLI commands remain pending; the
fresh-instance tests exercise their shared store primitive. Task 20 still owns
the feature-wide `c0bfff2` comparison and combined freshness/scheduling ADR. No
dependency, vault-schema, generated-artifact or ranking changes were made. CLI
and CI rubric topics are N/A: no commands, flags or workflows were added.

Optional editorial follow-up: `kk:clarify-docs` on `README.md` and
`docs/architecture.md`; it is not an unfinished Task 12a requirement.

## Task 13: Bounded directory ingestion

Implemented 2026-10-11 on `feat/upstream_sync`, starting from `362bc39`.
`capy_index(path)` now dispatches files or directories through the shared
knowledge indexer. Directory controls validate before storage side effects;
inline and explicit-file contracts are preserved. Descendant symlink following
remains Task 13a.

### Behavioral evidence

- Lexical traversal and limits: fixtures cover depth 0/5/20, file attempts
  1/200/1,000, and a 20,001-file directory that admits only 20,000 entries and
  indexes none of that partially enumerated directory. Ignored/denied entries
  consume traversal work without consuming file attempts. A 1,000-source import
  followed by search invokes at most 32 stale checks.
- Read accounting: growth-after-stat, partial I/O failure, store failure,
  unchanged input, cancellation and remaining-allowance boundaries retain actual
  read-byte counts. The 32 MiB test rejects incomplete content and reports a
  request cap separately from per-source size errors. Failed indexing still
  consumes bytes. Ten retained failure details plus an omitted count bound the
  diagnostic list; cancellation is counted once.
- Admission/selection: mandatory exclusions, case-normalized extensions, whole
  POSIX-path patterns, Unicode/newline filenames, invalid controls, FIFO skips,
  direct-root aliases, symlink-before-parent resolution and descendant symlink
  skips. Root-alias child denies are checked through both requested and canonical
  spellings. Existing direct-file lexical cleaning is preserved even when its
  physical spelling is absent, a different file or a directory.
- Git: real Git 2.34.1 fixtures cover the external root's worktree, ancestor and
  nested ignore rules, negation, deliberately ignored tracked files, bounded
  batches, inherited Git-variable poisoning, newline/tab/dash filenames, nested
  repositories, linked worktrees and submodule-style gitfiles. Explicit nested
  roots select their own worktree. Missing Git/corrupt metadata/check-ignore
  failures remain visible; non-Git scans work without Git installed.
- Storage/protocol: colon-prefix labels, same/different-prefix reruns, deliberate
  cross-root namespace collisions, unchanged stored chunk totals and no deletion
  of removed/excluded sources. Server tests verify relative Read denies for
  walking, direct absolute files and stale refresh. A real stdio fixture checks
  published schemas, strict controls, literal string booleans, incomplete-success
  status, deduplication and encrypted close/reopen/search with process cwd
  different from the selected project.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-cache`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development` and `CAPY_VAULT_KEY=test-key`.
Full/server-race/quality commands additionally clear `CLAUDE_PROJECT_DIR` and
set `XDG_CONFIG_HOME=/tmp/capy-task13-config`. Full and server-race suites require
local socket access for their existing HTTP fixtures.

| Command | Result |
|---|---|
| `go test -tags fts5 ./internal/knowledge ./internal/server ./cmd/capy -run 'Test(IndexDirectory\|Directory\|MCPStdioDirectory)' -count=1` | Passed: 12.291s / 1.204s / 2.078s. |
| `make test` with isolated configuration and local sockets | Every package passed: CLI 269.010s, knowledge 19.520s, server 157.317s, vault 315.799s; unchanged packages may use cache. |
| `go test -race -tags fts5 ./internal/knowledge ./internal/server -count=1` with isolated configuration and local sockets | Passed: 127.346s / 162.553s. |
| Final `go test -race -tags fts5 ./internal/knowledge -count=1` after review fixes | Passed, 109.732s; includes file-dispatch, cancellation, remaining-byte and 1,000-source stale-check assertions. |
| Final `go test -tags fts5 ./internal/server ./cmd/capy -run 'TestIndex\|TestMCPStdioDirectory' -count=1` | Passed: 7.741s / 2.393s. |
| `go vet -tags fts5 ./internal/knowledge` | Passed. |
| Vet including `./internal/server ./cmd/capy` | Only the pre-existing `tool_boolean_test.go:110` mutex-copy warning documented in Tasks 8/12/12a; no new warning. |
| Final `make build` | Passed; non-fatal read-only module-stat-cache warning. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task13` | Passed for store/server/vault. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task13-before.json bench-results/upstream-sync-task13.json` | Dataset verified; all 84 quality/context metrics unchanged. |
| `git diff --check` | Passed. |

The quality baseline is the saved Task 12a final report (HEAD metadata `1f4b90f`,
including that task's working-tree implementation), copied to
`upstream-sync-task13-before.json`; the target has HEAD metadata `362bc39` and
this working tree. These are quality comparisons, not timing/performance claims.
Ignored reports live in `bench-results/`; logs are
`/tmp/capy-task13-{focused,full-isolated,race-isolated,knowledge-final,dispatch-final,knowledge-vet,vet,build-final,quality,quality-comparison}.log`.

The first attempt encountered a read-only default Go cache. Later environmental
failures are retained in `full.log` and `race.log` under the same prefix:
inherited user configuration changed unrelated vault import thresholds, and
sandbox HTTP sockets were denied.
Verification was rerun with the cache/config/socket settings above. An empty
sandbox-created `/tmp/.git` also exposed an overbroad discovery assumption:
empty `.git` directories are now treated as nonrepositories, matching Git;
gitfile/HEAD candidates still require successful Git discovery.

### Review and scope

The [isolated review](.reviews/task-13-code-review-2026-10-11.md) approved the
final code. Its P2 file-dispatch and P3 duplicate-cancellation findings were
fixed and re-reviewed, with regression coverage. PAL was unavailable. No
unresolved findings or new conventions require duplicate knowledge indexing;
the decisions and evidence are documented here.

Task 13 introduced no module upgrade, store migration, ranking change, vault
format change or generated artifact change. Git command flags were verified
against the installed version's manual after Context7 lacked version-specific
coverage. CLI/CI rubric topics are N/A because no commands, flags or workflows
were added. D2, opt-in descendant following, terminal commands and the final
feature-wide benchmark/ADR work remain in their assigned later tasks.

Optional editorial follow-up: `kk:clarify-docs` on `README.md` and
`docs/architecture.md`; it is not an unfinished Task 13 requirement.

## Task 13a: Opt-in descendant symlink traversal

Implemented on 2026-10-11. `follow_symlinks` defaults to false and uses the
shared literal boolean parser. Opt-in following keeps targets within the
canonical ingestion root and preserves the selected project's policies/store.

### Behavioral evidence

- Alias and target admission: mandatory credential/component exclusions,
  canonical ancestor Read denies and user exclusions, external targets,
  requested-root aliases plus descendant aliases, and direct-root compatibility.
  Excluded targets are checked with a content-read seam that fails if invoked.
- Identity and selection: canonical extensions/include/exclude patterns,
  prefixed labels, stored refresh paths, overlapping file/directory aliases,
  root/self cycles, unchanged reruns and shallower canonical revisits. A deny
  through one alias does not erase a later admissible ordinary path.
- Git: a real command reproduces `beyond a symbolic link` for `alias/file`.
  Ingestion succeeds using physical link entries and canonical targets, honors
  ignored/tracked targets and ignored ancestors, and prunes nested Git targets
  with ignores enabled or disabled. Explicit nested-root aliases select their
  own worktree.
- Bounds/outcomes: canonical and traversal depth, one-file admission, exact
  20,000-entry allowance while following an alias, 32 MiB actual read cap with
  growth, no partial-content writes, cancellation, and ten retained diagnostics
  for dangling/unresolvable links. Operational failures retain committed files.
- MCP: schema discovery, invalid booleans before store initialization, native
  and literal string controls, default skipping, opt-in broken-link errors and
  encrypted close/reopen/search through the actual stdio transport.

### Verification commands

Commands use `GOCACHE=/tmp/capy-go-cache`, `CGO_ENABLED=1`,
`CAPY_DB_KEY=test-key-for-development`, and `CAPY_VAULT_KEY=test-key`.
Server/full/quality runs clear `CLAUDE_PROJECT_DIR` and use
`XDG_CONFIG_HOME=/tmp/capy-task13a-config`. Existing HTTP fixtures require local
socket access; that was enabled for the full/race reruns.

| Command | Result |
|---|---|
| `go test -tags fts5 ./internal/knowledge ./internal/server ./cmd/capy -run 'Test(IndexDirectory\|Directory\|MCPStdioDirectory)' -count=1` | Passed: knowledge 20.818s, server 1.223s, CLI 2.130s. |
| `make test` with local sockets | Every package passed: CLI 260.065s, server 149.677s, vault 310.332s; unchanged/tested packages may use cache. |
| `go test -race -tags fts5 ./internal/knowledge ./internal/server -count=1` with local sockets | Passed: knowledge 137.031s, server 156.497s. |
| `go test -race -tags fts5 ./internal/knowledge -run 'TestDirectoryFollowSymlinks(EntryLimit\|ShallowerRevisit)' -count=1` | Passed, 2.172s; covers the two tests added after the broader runs began. |
| `go vet -tags fts5 ./internal/knowledge` | Passed. |
| Vet including `./internal/server ./cmd/capy` | Only the existing `tool_boolean_test.go:110` mutex-copy warning recorded in earlier tasks. |
| `make build` | Passed; nonfatal read-only module-stat-cache warning. |
| `make bench-quality BENCH_BRANCH=upstream-sync-task13a` | Passed for store/server/vault. |
| `go run -tags fts5 ./cmd/qualstat bench-results/upstream-sync-task13a-before.json bench-results/upstream-sync-task13a.json` | Dataset verified; all 84 quality/context metrics unchanged. |
| `git diff --check` | Passed. |

The quality baseline is the saved Task 13 final report (HEAD metadata `362bc39`,
including that task's working-tree implementation); the target has HEAD metadata
`bf15083` plus this working tree. These comparisons do not claim timing changes.
Reports are ignored under `bench-results/`; command logs are
`/tmp/capy-task13a-{focused,full,full-sockets,race,race-sockets,extra,vet,build,quality,quality-comparison}.log`.
The initial default Go cache was read-only, and sandbox Unix/TCP sockets prevented
the first broad runs from completing successfully; the stated reruns address
those environment constraints.

### Review and scope

The [isolated review](.reviews/task-13a-code-review-2026-10-11.md) approved the
change with no findings. PAL's external review likewise recommended no fixes.
No findings or new conventions require duplicate knowledge indexing; decisions
and regression evidence are documented here and in the implementation notes.

The first alias-policy regression exposed a missing lexical descendant spelling
when both the explicit root and a child were symlinks; retaining both root
spellings fixed it. No module, schema, ranking or generated-artifact changes
were needed. CLI and CI rubric topics are N/A; terminal flags remain Task 14,
and existing build/test conventions are inherited from `AGENTS.md`. D2's atomic
policy/open handoff and Task 20's feature-wide comparisons/ADRs remain pending.

Optional editorial follow-up: `kk:clarify-docs` on `README.md` and
`docs/architecture.md`; this is separate from Task 13a completion.
