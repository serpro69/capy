# Task 1 isolated code review

Scope: shell scanner, command evaluators, hook/MCP checks and their regression
tests. All other upstream-sync tasks remain pending and were excluded.

Reviewers: independent `code-reviewer` subagent and PAL
`gemini-3.1-pro-preview`. Go profile: SOLID, removal, security, code style, error
handling, injection, naming and performance checklists. The independent reviewer
inspected the subsequent fixes and approved the latest tree with no remaining
substantiated actionable findings. Its review was read-only; the implementation
agent ran all shell probes and tests. PAL reviewed the earlier implementation,
before the independent review's semantic corrections; its positive assessment is
not evidence that those later cases were covered.

## Independent reviewer findings and resolution

| Severity | Finding | Resolution and evidence |
|---|---|---|
| P1 | Escaped whitespace before `#` was misclassified as a comment, hiding a following substitution. | Track token boundaries through escapes; `escaped space before hash` regression. Harmless Bash probe confirmed execution. |
| P1 | ANSI-C escaped apostrophes desynchronized quotes and hid later substitutions. | Distinct ANSI-C quote handling in command and parameter contexts; two regression cases. Bash probes confirmed execution. |
| P1 | A `)` inside `${...}` prematurely closed an enclosing `$()`. | Bounded parameter frames retain nested substitutions and inherited quote context; parameter parenthesis/quote regressions. Bash probes confirmed execution. |
| P1 | A continued heredoc declaration became incorrectly quoted, suppressing executable body expansions. | Remove declaration continuations without marking the delimiter quoted; regression and Bash reproduction. |
| P1 | Dollar-quoted heredoc delimiters were misidentified, swallowing following commands as literal body text. | Explicitly reject ANSI-C/localized delimiter forms before execution. Regression cases cover simple, partial and escaped forms. Full delimiter decoding remains a documented unsupported feature. |
| P2 | The hook returned an earlier ask before checking later batch/extracted commands for denies or limits. | Shared request-level evaluation defers asks; tests cover later deny, limit and embedded commands. |
| P2 | Backslash-newline lost a genuine comment boundary and caused false denials. | Preserve the previous token boundary; literal-comment regression. |

The reviewer also prompted the unquoted-heredoc parameter-quote probe. Bash
confirmed that a nested substitution inside apparent single quotes still
executes there; inherited expansion context and regression coverage were added.

## PAL findings (native severity)

- **LOW — unnecessary block scope in `routeCapyTool`:** removed the block left
  behind by the old policy-count guard.
- **LOW — silent command type assertion in hook batches:** malformed commands
  now produce a structured block. The hook also normalizes supported serialized
  arrays and plain command strings before scanning.
- **LOW — nil private slices:** retained. Author context: these private slices
  are never serialized, nil append is ordinary repository Go style, and the
  scanner deliberately returns nil rather than a partial list on failure. There
  is no public null/empty-array ambiguity or runtime defect to defer.

## Limits and follow-up

ANSI-C/localized heredoc delimiter decoding is deliberately unsupported and
fails closed with ordinary-quote guidance. Before admitting it, add differential
Bash fixtures for escape decoding and localized delimiter bytes, preserving the
same scanner budgets. This follow-up is recorded at the affected code and in
`implementation.md`.

No complete shell interpreter, alias/dynamic command resolution, or arbitrary
non-shell containment is claimed. Existing heuristic non-shell extraction was
not redesigned. See [verification](../verification.md#task-1-shell-policy-evaluation)
for actual test commands, results and environment constraints.

The systemic lessons and repros are captured in this repository report, so no
duplicate `kk:review-findings` knowledge notes were indexed.
