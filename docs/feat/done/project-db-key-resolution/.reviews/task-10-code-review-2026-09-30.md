# Task 10 implementation review

> Scope: strict path inspection and rotation compatibility
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review and found no production defects. Its sole **LOW** suggestion concerned closing the temporary stdout capture file before reading it in `captureEncryptOutput`.

The helper now runs its callback in an inner function whose defer restores stdout and checks the close error before the outer function reads the file. Cleanup still runs on panic or `t.Fatal`. The independent agent approved this adjustment without findings; the affected tests passed again normally and with `-race`.

Author context: the original helper used synchronous `os.File` writes and serial tests, and no file-sharing failure was reproduced. The suggestion was adopted to make the resource lifetime explicit, without ignoring errors or closing twice. No systemic P0/P1 findings to index; no new deferred issues.

The review covered four Go files and task bookkeeping. Go SOLID, removal, security, style, error-handling, injection, database, and naming checklists applied. Tasks 1–9 provided completed context; pending Task 11 was excluded from missing-implementation assessment. Reviewers inspected code and tests; execution evidence below comes from the implementing session.

## Behavior and coverage

`which` prints the configured path without credential lookup. `which` and `encrypt` reject configuration parse, type, validation, and read failures without a fallback target. Rotation rejects invalid configuration before prompts or database access and retains its old-key prompt plus new environment-key/confirmed-prompt contract.

Real encrypted fixtures cover key-file and dotenv projects with both new-key sources. They verify old-key rejection, retained data under the new key, unchanged credential-file bytes and knowledge/vault environment values, prompt failures, and secret-free output. Help states the operator requirement to stop attached processes; success reminds users to update project credentials before restarting. Tests verify guidance, not operator compliance. Existing symlink, plaintext encryption, and checkpoint tests remain intact.

## Verification

- Full `make test` passed with FTS5 and both synthetic key variables. CLI completed in 207.643 seconds, server in 103.756 seconds, and vault in 181.674 seconds. Generated-artifact tests passed.
- Focused path, rotation, symlink, plaintext encryption, and checkpoint checks passed normally and with `-race`.
- After the test-only capture cleanup, `TestRunEncrypt` passed again normally and with `-race`. The full run above preceded that helper adjustment; production code was unchanged.
- Targeted `go vet -tags fts5 ./cmd/capy` and `git diff --check` passed.

The runner isolates HOME, XDG paths, vault paths, and synthetic credentials, clearing inherited Capy/Claude/Codex/Git overrides. The full suite ran with local socket access for existing HTTP and Unix-socket fixtures. Direct rotation tests run under the race detector; subprocesses launched by the existing CLI helper use ordinary FTS5 builds. No real credential files or databases were used. Search/indexing algorithms were unchanged, so quality benchmarks were unnecessary.

No design deviation or external dependency change was needed. Task 11 retains final feature verification and durable README/architecture/ADR/AGENTS documentation.
