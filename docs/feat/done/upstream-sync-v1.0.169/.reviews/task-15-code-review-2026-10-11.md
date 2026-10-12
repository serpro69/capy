# Task 15 isolated code review — 2026-10-11

Reviewers: independent `code-reviewer` agent without implementation-session
history; PAL `gemini-3.8-flash` was attempted but unavailable. Scope: terminal
knowledge search and regressions in completed tasks. Tasks 16a–20 and deferred
D1–D5 remain outside this slice.

**Final code-reviewer assessment: APPROVE. No unresolved P0–P3 findings.**
The final pass examined fourteen files and 859 changed lines, confirmed the
failure collector's synchronization/lifetime, and rechecked all new regressions.

The initial reviewer examined ten changed files and 689 changed lines, including
untracked command/tests and surrounding target, policy, retrieval and cleanup
contracts. Go profile activated through `.go` extensions. Loaded checklists:
SOLID, removal, security, code style, error handling and injection. The correction
also activates concurrency, naming, database and performance checks. The review
is static; author-run commands are recorded in
[verification.md](../verification.md#task-15-terminal-knowledge-search).

## Resolved finding

**P1 — retrieval storage failures can produce exit 0.**
Location: `cmd/capy/search.go`, the call to `SearchWithFallbackContext`.
Profile: Go; checklist: error handling. Confidence: 99%.

The shared retrieval executor logs SQL query, row-scan and row-iteration errors
and returns empty/partial results. The existing store wrapper therefore could
return a nil error after a query-stage failure. The new CLI would print successful
results or “No knowledge results found.” with exit 0, violating Task 15's error
contract even though credential/open failures already propagated.

Author change: introduce optional `CorpusConfig.OnError` reporting and
`SearchOptions.FailOnError`, enabled only by the new CLI. A per-search `sync.Once`
collector records the first failure across concurrent layers; the store reads it
after retrieval joins all layer workers and returns it before access tracking.
Existing MCP/vault callers retain their best-effort behavior and ranking/fallback
formulas. Strict CLI queries also report generated FTS-query failures rather
than silently accepting degraded results. Ordinary denied/failed/deferred stale
file checks remain nonfatal and preserve cached content.

Regression evidence includes query, scan and iteration failures; strict versus
legacy store behavior after both FTS tables are removed from an initialized
store; and a real CLI subprocess against an encrypted database containing a
matching FTS row with an unreadable NULL classification. The last fixture keeps
the schema/open path valid and verifies exit 1 with no successful stdout.
Focused affected tests pass under `-race`.

## External review availability

PAL listed `gemini-3.8-flash` as its most capable configured model. Step 1 created
a review continuation; step 2 failed with `API_KEY_INVALID` (HTTP 400). No expert
analysis was produced. The workflow continued with the independent code reviewer
under the skill's single-reviewer fallback; the failed request is not counted as
approval.

## Indexing

The P1 error-propagation pattern, implementation and regression evidence are
captured here and in the feature implementation notes. Repository knowledge-note
rules prohibit duplicating documented findings in the knowledge store; no
additional note was indexed. No removal candidates or other initial findings.
