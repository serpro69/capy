# Task 3 implementation review

> Scope: explicit `store.key_file` configuration and resolution through dbsize
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review and reported no defects or required fixes. No findings to index. No source/test changes followed the reviews; final edits only record completion and validation in the feature documents.

The reviewed snapshot contained seven Go files and task bookkeeping. The Go profile activated through `.go` extensions; SOLID, removal, security, code style, error handling, injection, and naming checklists applied. No other profiles matched. Tasks 1–2 supplied regression context; pending Tasks 4–11 were excluded from missing-implementation assessment. Neither review establishes completion of the full feature.

## Implementation

The existing pointer overlay distinguishes omitted `store.key_file` from explicit empty values, preserving inherited settings or clearing them respectively. Other configuration merging is unchanged. Relative credential paths use `DBProjectDir`; absolute paths, project aliases, and filesystem symlinks retain the agreed identity rules. Environment variables and `~` are not expanded in paths.

The resolver follows regular-file symlinks, checks target type before opening and descriptor type before reading, and limits actual reads to 4,097 bytes to detect overflow beyond 4,096 raw bytes. It removes at most one terminal LF or CRLF and rejects empty results, NUL, or remaining CR/LF without returning partial content or a fallback credential. The pre-open check covers pre-existing FIFOs; this is not a guarantee against hostile concurrent target replacement. Keys and source metadata are returned separately, and no credential is exported to the process environment.

`dbsize` resolves credentials before lazy store construction. Its errors retain target/source context without file contents or DSNs. Invalid file selection cannot create a DB directory or marker. Wrong selected credentials retain the existing authentication error and preserve an existing encrypted database. Store construction captures the resolved value: later file changes affect a newly resolved store, not the old store's close/reopen behavior.

## Verification

Checks passed:

- Focused `go test -tags fts5 -count=1 -timeout=120s ./internal/config ./cmd/capy` with the `Test(LoadKeyFile|ResolveStoreKey|DBSizeSubcommand|KnowledgeStore_KeyFileSnapshot)` filter.
- The same focused checks with `-race`.
- Full `make test`, including generated-artifact guards.
- `go vet -tags fts5 ./internal/config ./cmd/capy`, `gofmt`, and `git diff --check`.

Coverage includes all configuration layers, empty clearing and malformed types; exact/over-limit raw files with LF/CRLF; preserved whitespace/quotes/non-UTF-8 bytes; rejected NUL/newlines; missing, unreadable, directory, device, socket, FIFO, and FIFO-symlink inputs; regular symlinks; unchanged vault/process credentials; and credential-file preservation. FIFO checks run in deadline-bounded child processes so a blocking regression is killed and reaped.

CLI fixtures read retained project-B content with project-A credentials inherited, from an unrelated cwd, including linked-worktree relative/parent-relative/absolute/XDG DB modes, submodules, absolute/global-relative credentials, DB/credential symlinks, and explicit project-subdirectory overrides. Resolver tests also cover project aliases and literal unexpanded paths. No real credential files or live databases were used as test fixtures.

The first focused run hit the sandbox's local-socket restriction and two test assertions using different wording from the existing wrong-passphrase error. The assertions were corrected to the established message; the rerun permitted temporary local listeners. Final full-suite validation used synthetic keys, a disposable child-process home/config/data/vault location, `/private/tmp` temp/build-cache paths, empty Claude overrides, and no inherited Codex-home override. No required new credential cases were skipped.

Search/indexing algorithms were unchanged, so quality benchmarks were not required. Full-feature race/matrix verification remains Task 11. No new convention needs indexing beyond the agreed design and this implementation record.
