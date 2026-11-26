# Task 2 isolated code review

Scope: codex-hooks Task 2, portable owner binding, ancestor inspection,
bootstrap/MCP launch, failure supervision and generated artifacts. Task 1 is
existing context; Tasks 3–6 remain outside this review. The Go profile applies:
SOLID, removal, security, style, errors, injection, naming and concurrency.

Reviewers: independent `code-reviewer` agent; PAL `gemini-3.8-flash` attempted
but failed because the configured API key was rejected. No PAL findings exist.

## Independent reviewer finding

**P1 — wrapper completion could leave descendants running.**
`superviseCodexProcess` cleaned up the process group on cancellation/signals but
returned directly when `Wait` completed. A wrapper could fork a server and exit,
leaving the server alive after the launcher returned.

Author context and fix: all normal/failed completion paths now kill remaining
members of the wrapper's process group. `TestCodexWrapperExitCleansDescendants`
exercises both successful and status-42 wrappers with a long-lived descendant.
The test permits an already-dead reparented process awaiting the host init's
reap, but rejects a running descendant. Existing signal/stdin/MCP status tests
and the focused CLI/platform race selection pass after the fix.

## Author-sourced finding

**P2 — comment-only TOML was rejected.** The existing parser leaves a nil map
for an empty/comment-only document. The owner inspector treated that as invalid
configuration. It now initializes the map before decoding, and
`TestCodexOwnerBindings/empty_TOML_is_valid_unrelated_configuration` proves such
configurations remain marker-free. The full platform race suite passes.

The findings and remedies are recorded here; knowledge indexing is skipped to
avoid duplicating repository documentation, following AGENTS.md.

## Final disposition: APPROVE

The designated reviewer failed at model capacity after reporting P1. Its retry
was replaced by a fresh, context-isolated reviewer, which verified both fixes
and independently reran the focused owner, descendant-cleanup, wrapper-open,
stdin/signal and TOML table-boundary tests with `-tags fts5 -count=1`: all passed.
It found no remaining Task 2 correctness issue after checking ancestor binding
validation, copied-ID overlap rejection, canonical recognition, pre-tool error
mapping, MCP ownership/status/stdin, signal forwarding and TOML merging.
No new findings to index. Tasks 3–6 and live host delivery remain out of scope.
