# Task 1 implementation review

> Scope: independent FTS5 diagnostics and CLI knowledge-path errors only
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review with no issues or recommended fixes. No findings to index.

The review covered four Go files plus task-status bookkeeping (195 changed lines). The Go SOLID, removal, security, style, error-handling, injection, and naming checklists applied. Tasks 2–11 remain pending; the feature-wide credential contract was not treated as implemented.

MCP doctor now probes FTS5 through the shared in-memory check, explicitly initializes the knowledge store, and reads its stats once. Opening errors remain visible, as do initialized-empty-store counts and legacy-session hints. CLI doctor reports non-absence stat errors without opening the store and retains its diagnostic exit convention.

## Verification

- Before the production changes, new regression tests failed on the incorrect FTS5 result for wrong keys, missing keys, and a database path pointing to a directory, plus CLI's incorrect missing-database message for an invalid parent path.
- After the changes, `go test -tags fts5 -count=1 ./internal/server ./cmd/capy -run TestDoctor` passed.
- `go test -race -tags fts5 -count=1 ./internal/server ./cmd/capy -run TestDoctor` passed.
- The wrong-key fixture was reopened with its original synthetic key and retained its indexed source. Tests also assert secret/DSN exclusion and CLI no-creation behavior.
- Full `make test` passed with FTS5 enabled, both synthetic key variables set, and the isolated environment described below. Generated-artifact checks passed as part of the platform package. Search/indexing algorithms were unchanged, so quality benchmarks were not required.

Tests use synthetic keys and disposable databases. Capy knowledge searches were attempted but failed with the existing wrong-passphrase error; local design and source files supplied the implementation context. No new project conventions need indexing beyond the existing plan.

## Environment notes and deferred test portability

The first `make test` run encountered sandbox-denied loopback listeners, macOS `/var` versus `/private/var` path comparisons in existing vault restore tests, and config tests that assume `CLAUDE_CONFIG_DIR` is unset. The second run fixed those issues but used a temp parent with mixed dash/dot punctuation, exposing an existing `unmangledProbe` limitation and consequent missing session-search hits. Final verification uses `TMPDIR=/private/tmp`, permits local listeners, and isolates the child home/config/data/vault paths with an empty Claude override.

The existing test-portability issues are deferred because they concern unrelated vault/config fixtures. To support arbitrary host temp paths, normalize expected restore paths with `filepath.EvalSymlinks` in the affected vault tests. To tolerate inherited Claude overrides, clear `CLAUDE_CONFIG_DIR` inside the two `TestResolveSourceProject_SessionDir*` fixtures before their fake-home setup. The rerun changes no assertions.

The mixed-punctuation path-resolution issue is also outside this diagnostic task: `internal/config/paths.go:unmangledProbe` tries each grouped component with all dashes or all dots, so it cannot recover a component such as `capy-task1.Pc4sAK`. A separate fix should consider mixed separators within a component, bound the probing cost, and add a regression fixture that also verifies the imported session remains discoverable under default project scope.

Pre-existing suppressed stats-query errors and ignored store-close errors retain the separate follow-ups in [the baseline verification](cove-2026-09-30.md#additional-findings-and-follow-ups).
