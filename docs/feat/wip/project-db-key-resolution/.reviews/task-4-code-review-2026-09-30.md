# Task 4 implementation review

> Scope: literal project dotenv credential resolution through dbsize
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review and reported no defects or required fixes. No findings to index. No source/test changes followed review; final edits only record completion and validation in the feature documents.

Author context for PAL's broad file-race claims: the inherited admission helper checks type before open and on the descriptor, and tests bound pre-existing FIFO behavior. These tests do not simulate hostile concurrent replacement; the checks are not a guarantee against that race. This boundary is unchanged from [Task 3](task-3-code-review-2026-09-30.md).

The review covers five Go files and task bookkeeping. The Go profile activates through `.go` extensions; SOLID, removal, security, code style, error handling, injection, and naming checklists apply. No other profiles match. Tasks 1–3 provide regression context; pending Tasks 5–11 are excluded from missing-implementation assessment.

## Implementation

`parseDotenvKey` detects exact target declaration candidates before validating the whole file. A file with no candidate contributes no key. Once a candidate exists, unsupported syntax before or after it, duplicate declarations, empty targets, malformed assignments, and multiline input fail without returning a partial key. Literal quoting, comments, LF/CRLF, and supported double-quote escapes preserve the selected bytes. Non-target values are structurally validated and discarded; their expansions/escapes are never executed or exported. Diagnostics contain a source path, line number, fixed reason, and `store.key_file` migration hint, without source text or secret values.

`ResolveStoreKey` now tries the database owner's dotenv, then a distinct main-worktree fallback, then the inherited environment. Relative database paths ignore linked-worktree dotenv; absolute and XDG paths consult it first. A selected explicit key file bypasses dotenv entirely. Optional missing files or absent declarations permit fallback; other access/parse errors stop. The existing regular-file admission helper enforces the 1 MiB raw dotenv limit, including line endings. No database, environment, or vault credential is modified during resolution.

## Verification

Passed:

- Focused FTS5 tests in `internal/config` and `cmd/capy` using `Test(ParseDotenvKey|ResolveStoreKeyDotenv|DBSizeSubcommand_Dotenv)`.
- The same focused tests with `-race`.
- Full `make test`, including generated-artifact guards.
- `go vet -tags fts5 ./internal/config ./cmd/capy`, formatting, and whitespace checks.

Parser/resolver coverage includes literal bytes, false-positive variable names, expansion/escape rejection, embedded declarations, trailing malformed input, duplicate/empty keys, long single lines, exact/over-limit files, regular symlinks, FIFOs and FIFO symlinks, directories/devices, permission failures, and deterministic symlink-loop errors. FIFO tests use bounded child processes. Precedence tests cover owner/main/environment boundaries and bypass of invalid lower-priority sources.

Actual dbsize subprocesses read retained B content with A inherited in normal and linked checkouts, including absolute/XDG main-worktree fallback. Correct inherited credentials do not rescue invalid/wrong dotenv keys; explicit key files bypass those same files. Tests assert retained content, safe diagnostics, no database directory/marker creation on resolution failure, no execution sentinel, unchanged process/vault environment, and unchanged dotenv files.

The first full `make test` failed because the sandbox denied existing Unix socket and local HTTP listener tests. The approved rerun passed with those operations permitted, synthetic keys, and isolated home/config/data/vault paths. No assertions were weakened.

No design deviation, external dependency, or new convention requiring separate indexing was introduced. Search/indexing algorithms were unchanged, so quality benchmarks were not required. Full-feature integration, race/matrix verification, migration guidance, and the credential-policy ADR remain in their assigned pending tasks.
