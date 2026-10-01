# Task 2 implementation review

> Scope: captured store credentials, explicit-key preflight, and strict environment-only dbsize
> Review mode: `kk:review-code:isolated`, mid-implementation
> Reviewers: independent `code-reviewer` agent and PAL `gemini-3.1-pro-preview`

## Result

The independent agent approved the changes with no P0–P3 findings. PAL completed its two-step external review and reported one **LOW** formatting finding at `cmd/capy/knowledge.go:50`: place each constructor argument on its own line. That formatting fix was applied. Neither reviewer found a correctness, security, concurrency, or architecture issue. No findings remain deferred; no systemic P0/P1 findings to index.

The review covered seven Go files plus task bookkeeping. The Go profile activated through `.go` extensions; SOLID, removal, security, style, error-handling, injection, naming, database, and concurrency checklists applied. No other profiles matched. Task 1 provided regression context; Tasks 3–11 were explicitly outside the review scope.

## Implementation and verification

Store construction captures one private key/source pair. Empty keys fail before DB directory/marker creation and before standalone maintenance opens. Normal access, recovery retries, FTS rebuild, vacuum, checkpoint, close, and reopening the same object all use that pair. Pool-close-before-checkpoint ordering remains intact. Wrong-key errors retain their classification and carry the selected safe source hint. Existing vault callers and the encryption algorithm are unchanged.

`dbsize` separates strict configuration/target selection from credential lookup and lazy store construction. Parse, type, validation, and deterministic read errors cannot select or create a fallback database.

Checks passed:

- Focused FTS5 tests for `StoreEncryption`, `ValidateEncryptionReady`, `RequireEncryptionKey`, and `DBSizeSubcommand` in store and CLI packages, with `-count=1`.
- The same focused checks plus doctor, close/checkpoint, and corruption recovery cases with `-race -tags fts5 -count=1` across store, server, and CLI packages.
- Full `make test`, including generated-artifact tests. No assertions were relaxed or new credential cases skipped.
- Final `gofmt` and `git diff --check`. The only post-test source change was the reviewed argument-layout fix.

Tests assert actual retained content, crossed-key rejection, recovery backup preservation, safe errors, and missing or empty sidecars. Search/indexing algorithms were unchanged, so quality benchmarks were not required. Full-feature race/matrix verification remains Task 11; the Task 2 race checks above passed.

## Environment and scope notes

The first focused invocation could not write the sandbox-protected default Go build cache. Tests then used `/private/tmp/capy-go-build`. The full suite used a disposable home/config/data/vault location, synthetic keys, `TMPDIR=/private/tmp`, empty Claude project/config overrides, and permission for local test-server listeners. The inherited `CODEX_HOME` override was absent. No real credential files or live databases were used as validation fixtures.

The implementation follows slice 2 without a design change. Safe hints reuse the existing SQLite error field, whose documentation now allows a credential-file hint as well as an environment-variable name. File/dotenv resolution, server injection, and remaining command paths retain their explicit pending tasks. Existing unrelated follow-ups remain recorded in the Task 1 and baseline verification reports. No new project convention needs indexing beyond this feature's design and implementation documents.
