# Task 8 implementation review

> Scope: project credentials for direct cleanup and checkpoint
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review and reported **“No issues found.”** No findings to index.

The review covered four Go files plus task bookkeeping (308 changed lines). Go SOLID, removal, security, style, errors, injection, database, and naming checklists applied. Completed Tasks 1–6 supplied regression context. Pending Tasks 7 and 9–11 were excluded from missing-implementation assessment. The independent agent performed static review; test results below come from the implementing session.

## Implementation and coverage

Cleanup and checkpoint now use `loadKnowledgeTarget`, `resolveKnowledgeKey`, and `newKnowledgeStore`, preserving the selected credential for every store connection. Malformed configuration fails before credential resolution or database access. Checkpoint retains its successful no-op for an absent database, even with a missing configured key file and no environment key; other stat failures return explicit errors before credential lookup. Cleanup's selector validation, dry-run, eviction, and reclamation behavior remain intact.

Tests reuse the existing CLI fixtures with isolated project/config/data directories and synthetic credentials. Key-file and dotenv fixtures override an unrelated inherited key for cleanup dry-run, source eviction, session selection, vacuum, and FTS optimization. Reopening with the selected key verifies retained labels and targeted deletion. Checkpoint covers a key-file success and a dotenv-backed database held busy by a read snapshot established before a subsequent write. The blocked invocation returns nonzero and retains WAL data; releasing the reader and closing the writer permits a successful retry with both sources retained.

Both commands reject missing explicit files, invalid dotenv, and wrong local keys despite a correct inherited key. Failures leave the encrypted database bytes and retained content intact. Tests also check secret/DSN exclusion, missing-database directory/marker non-creation, and absence of a fallback XDG database. The original malformed TOML in `TestCheckpointSubcommand_BadConfig` is unchanged; its former fallback-success expectation is now explicit failure.

## Verification

Passed:

- `go test -tags fts5 -count=1 ./cmd/capy -run 'Test(CleanupSubcommand|CheckpointSubcommand|MaintenanceSubcommands)'`.
- The same focused command with `-race`.
- Full `make test`, including platform generated-artifact guards.
- `go vet -tags fts5 ./cmd/capy`, `gofmt`, and `git diff --check`.

The first test attempt could not write the default Go build cache; subsequent runs used `/tmp/capy-go-build-cache`. The full suite ran with local socket access and isolated temporary home/config/data/vault paths, using synthetic `CAPY_DB_KEY` and `CAPY_VAULT_KEY` values. No test assertions were weakened for the environment. No real credential file or database was used for validation.

The race run instruments the test process, including the SQLite contention fixture. The existing CLI helper launches ordinary FTS5 `go run` subprocesses, so it does not establish race coverage inside those children. Full-feature race/matrix validation remains assigned to Task 11. Search/indexing algorithms were unchanged, so quality benchmarks were not required.

No design deviation, new dependency, or additional project convention was introduced. Generated wrapper/pre-commit integration belongs to Task 7, which is now unblocked. General close-error reporting remains outside this task, as documented in the feature plan.
