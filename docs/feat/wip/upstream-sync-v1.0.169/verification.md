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
