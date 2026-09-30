# Task 9 implementation review

> Scope: credential diagnostics in CLI and MCP doctor
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review and reported no defects or recommended fixes. No systemic P0/P1 findings to index.

The review covered six Go files and task bookkeeping. The Go SOLID, removal, security, style, error-handling, injection, naming, and database checklists applied. Tasks 1–8 supplied completed context; pending Tasks 10–11 were excluded from missing-implementation assessment. Reviewers inspected the implementation and tests; execution evidence below comes from the implementing session.

## Behavior and coverage

Both doctors now show a separate Knowledge credential check naming `store.key_file`, the dotenv declaration's path, or environment `CAPY_DB_KEY`. A successful selection explicitly leaves database authentication to the knowledge-base check. Formatting receives only safe source hints and safe errors, never the credential itself.

CLI doctor uses the strict target and credential helpers. Invalid configuration produces a failed Config check and skips knowledge checks without choosing a fallback database path. Resolution failures report their source and error, then skip database access. Independent runtime, FTS5, and vault checks continue, including the existing vault migration guidance. Missing databases remain uncreated, and reported diagnostic failures retain the command's zero exit convention.

MCP doctor uses the server's captured credential and source. Direct construction with an empty credential now reports a selection failure and skips store initialization. Nonempty credentials retain lazy initialization and actual canary/open errors. Normal serve rejects invalid configuration and resolution failures before starting MCP, as covered by Task 6.

Regression cases exercise all three sources under successful reads, wrong keys, and database-access failures. CLI cases also cover absent databases, configuration parse/type/validation/read failures, and missing or invalid credentials with both absent and existing databases. They verify no fallback XDG directory, marker, or sidecars are created on resolution failure; existing bytes remain unchanged. MCP cases mutate the environment and make credential-source paths nonregular after construction to verify snapshot use. Both surfaces preserve retained content after wrong-key attempts and exclude synthetic secrets, assignment lines, and encryption DSN parameters from output.

## Verification

- Focused FTS5 doctor tests in `cmd/capy` and `internal/server`, including the explicit-empty direct-server case, passed normally and with `-race`.
- Targeted `go vet -tags fts5` for CLI, platform, and server packages passed.
- `git diff --check` passed.
- Full `make test` passed with FTS5 and both synthetic key variables set, including generated-artifact guards. The CLI package completed in 196 seconds, server in 102 seconds, and vault in 179 seconds.

Tests use synthetic credentials and disposable databases. The isolated runner clears inherited Capy, Claude, Codex, and Git overrides, uses temporary HOME/XDG/vault paths, and retains only the Go module/cache locations needed for builds. The race flag instruments the Go test process; CLI commands launched through the existing `go run` helper are ordinary FTS5 builds. Search/indexing algorithms did not change, so quality benchmarks were not required.

The first focused run exposed an incorrect expected dotenv error format in a new test: the parser emits a quoted path followed by `line 2`, rather than `path:2`. The assertion now checks the existing path, line number, and fixed reason. No production parser behavior or assertion strength changed.

The first full-suite run passed the CLI package but failed existing Unix-socket and HTTP-listener fixtures because the sandbox denied socket operations. A direct loopback probe confirmed the restriction. The full suite passed when rerun with permission for local listeners, using the same isolated runner and synthetic credentials; no tests were skipped or weakened.

No design deviation, dependency change, or new project convention was needed. Existing stats-query error suppression and shutdown reporting remain the explicitly deferred follow-ups in the baseline verification. Task 10 owns path inspection and rotation; Task 11 owns the complete regression matrix and final README/architecture/ADR/AGENTS documentation.
