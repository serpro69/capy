# Issue #125: checkpoint verification through database symlinks

Implemented on 2026-10-01. [Issue](https://github.com/serpro69/capy/issues/125).

## Reproduction and fix

SQLite already follows a symlinked database and recovers pending WAL data. The
confirmed failure was CLI verification: it looked beside the configured link,
missing the SHM file beside the physical database when an idle connection kept
it open. The pre-fix regression returned zero and printed "safe to commit" in
that state. The pending-WAL recovery cases passed before the change.

Checkpoint now resolves the physical path once for its dedicated connection and
sidecar checks. It resolves credentials first and retains the selected project's
ownership, preserving [ADR-032](../adr/032-project-db-key-resolution.md). Resolution
and inspection errors propagate. Verification follows the existing DB-repo guard:
an empty regular WAL is allowed; non-empty WAL, nonregular sidecars, or any SHM
produce an error. Verification never deletes sidecars. The missing-database
keyless no-op, including a dangling database symlink, is preserved.

## Validation

- All checkpoint CLI and sidecar-verification tests passed normally and with
  `-race`. The race flag instruments the test process and contention fixtures;
  the existing CLI helper launches an ordinary FTS5 subprocess binary.
- New tests cover absolute, relative, chained, and directory-containing links;
  pending WAL recovery; preservation of the database symlink and project key
  ownership; idle connections; busy readers; dangling links and loops; and
  sidecar inspection failures. Recovery is verified from a copy of only the
  checkpointed main file, with both indexed sources present.
- `go vet -tags fts5 ./...`, formatting, and `git diff --check` passed.
- The generated pre-commit integration test passed with `-race` after the
  fixture correction below; CLI vet passed again afterward.
- Final `go test -tags fts5 -count=1 -timeout=600s ./...` passed for every package
  in the isolated environment below. CLI: 193.668 s; vault: 108.527 s.

The initial full-suite run inherited the host environment and used macOS's
symlinked `/var` temporary directory. Existing MCP and vault fixtures failed on
path comparisons and session discovery. The rerun uses a temporary child home,
isolated XDG paths, cleared discovery overrides, preserved Go caches, synthetic
keys, and `TMPDIR=/private/tmp`, following the repository's documented test
environment. No assertions were weakened. The pre-existing portability work and
concrete next steps remain recorded in the
[credential-resolution review](../feat/wip/project-db-key-resolution/.reviews/task-1-code-review-2026-09-30.md#environment-notes-and-deferred-test-portability);
they are outside this checkpoint fix.

The isolated rerun then exposed a relevant pre-commit fixture assumption: it
closed its reader but kept the writer store open while asserting commit success.
That depended on the old false-success behavior. The fixture now verifies that
the idle writer also blocks the commit and leaves HEAD unchanged, closes the
writer, and then verifies successful commit, staged database contents, and both
retained sources. Production code did not change after the initial review.

Search, indexing, chunking, and executor behavior did not change, so quality
benchmarks were not required. No generator or generated setup artifact changed.

## Independent review and limits

`kk:review-code:isolated` used a fresh `code-reviewer` agent and two-step PAL
`gemini-3.1-pro-preview` review. Go was the sole active profile. SOLID, removal,
security, style, error-handling, injection, database, and naming checklists
applied. Both reviewers reported no actionable findings; the independent agent
returned APPROVE. Neither reviewer independently ran the tests. No systemic
P0/P1 findings or new project conventions required indexing.

Both reviewers also approved the pre-commit fixture correction and this record
with no actionable findings, before the final full-suite result was added.

The command still requires all database processes to remain stopped through
checkpoint, staging, and commit. Filesystem checks do not lock out a process
that opens or replaces the database afterward. This remains the existing
[ADR-016](../adr/016-wal-mode-and-checkpoint-strategy.md) operating contract.
