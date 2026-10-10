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
