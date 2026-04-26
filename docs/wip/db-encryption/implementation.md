# DB Encryption — Implementation Plan

> **Design:** [./design.md](./design.md)
> **Issue:** TBD
> **Created:** 2026-04-26

This plan is ordered for incremental development. Each task builds on the previous and can be verified independently. The developer should be familiar with Go and CGo but may have no prior context on the capy codebase.

## Prerequisites

Read these files before starting:
- `CONTRIBUTING.md` — build instructions, test patterns, project structure
- `internal/store/store.go` — `ContentStore`, `openDB()`, `getDB()`, `checkpoint()`, `Checkpoint()`, `Close()`
- `internal/config/config.go` — `Config`, `StoreConfig`, `DefaultConfig()`
- `internal/config/paths.go` — `ResolveDBPath()`, `DetectProjectRoot()`
- `cmd/capy/checkpoint.go` — existing checkpoint CLI command (model for `encrypt`)
- `internal/platform/setup.go` — pre-commit hook setup
- `docs/adr/015-knowledge-db-not-tracked-in-git.md` — context on why DB tracking was prohibited
- `docs/adr/016-wal-mode-and-checkpoint-strategy.md` — WAL checkpoint architecture

All tests require `-tags fts5`. Use `make test` or `go test -tags fts5 -count=1 ./...`.

---

## Phase 1: Driver Proof of Concept

### 1.1 Test driver alternatives

Build a standalone test program (`internal/store/encryption_poc_test.go` or a temporary `cmd/poc/` binary) that validates the encryption integration end-to-end. Test each driver option in order until one passes all checks:

**Option 1: system SQLCipher**
- Install `libsqlcipher-dev` (apt) or `sqlcipher` (brew).
- Build with: `CGO_CFLAGS="-I/usr/include/sqlcipher" CGO_LDFLAGS="-lsqlcipher" go build -tags "fts5 libsqlite3"`.
- Open DB, execute `PRAGMA key = 'test-passphrase'`, create an FTS5 table, insert data, close.
- Reopen with correct key, verify FTS5 search returns results.
- Reopen with wrong key, verify error (not silent data corruption).
- Verify `sqlcipher_export` works: open encrypted, attach new DB with different key, export, verify.

**Option 2: system sqlite3mc**
- Build sqlite3mc from amalgamation source (download from [releases](https://github.com/utelle/SQLite3MultipleCiphers/releases), build with `./configure && make && make install`).
- Build with: `CGO_CFLAGS="-I/usr/local/include" CGO_LDFLAGS="-L/usr/local/lib -lsqlite3mc"` and `-tags "fts5 libsqlite3"`.
- Same test matrix as option 1, but also test URI-parameter encryption: `file:test.db?cipher=chacha20&key=test-passphrase`.
- If URI params work, this is the preferred path (no PRAGMA-first requirement).

**Option 3: jgiannuzzi fork**
- Add `replace` directive to `go.mod`: `replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 v1.14.35-0.20260227142656-2c447b9a2806`.
- Build with standard: `go build -tags "fts5"` (no `libsqlite3` tag — fork bundles the amalgamation).
- Same test matrix.

**PoC validation checklist:**
- [ ] Encrypted DB creation with passphrase
- [ ] FTS5 full-text search on encrypted DB
- [ ] Close and reopen with correct key succeeds
- [ ] Reopen with wrong key fails cleanly
- [ ] WAL mode works with encryption
- [ ] `sqlcipher_export` or equivalent migration works
- [ ] Checkpoint (PRAGMA wal_checkpoint(TRUNCATE)) works on encrypted DB

**Verify:** The PoC test passes with at least one option. Document which option was selected and any caveats in a comment at the top of the test file.

---

## Phase 2: Core Encryption Integration

### 2.1 Update build system

**File:** `Makefile`

Update build tags and CGo flags based on the PoC winner:
- If option 1 or 2: add CGo flag variables and `libsqlite3` to BUILD_TAGS.
- If option 3: no Makefile change (but `go.mod` gets the replace directive).

Provide a `Makefile` comment or variable explaining the encryption backend choice.

**Verify:** `make build` succeeds. `make test` succeeds. The binary links against the correct library (`ldd capy | grep -i cipher` for options 1-2).

### 2.2 Key reading and validation

**New file:** `internal/store/encryption.go`

Add a function to read and validate the encryption passphrase:

```
func readEncryptionKey() (string, error)
```

- Reads `CAPY_DB_KEY` from environment.
- Returns error if empty: `"CAPY_DB_KEY environment variable is required"`.
- Logs a warning (not error) if length < 32 characters.

This is a standalone function, not a method on `ContentStore`, because `capy encrypt` needs it too without constructing a full store.

**Verify:** Unit test with mocked env var: empty → error, short → warning logged + key returned, 32+ chars → key returned.

### 2.3 Integrate encryption into `openDB()`

**File:** `internal/store/store.go`

Modify `openDB()` to apply encryption. The exact mechanism depends on the PoC result:

**PRAGMA path:** After `sql.Open()`, before any other statement, execute `PRAGMA key = ?` with the passphrase. Sequence: open → PRAGMA key → canary query → PRAGMA mmap_size → schema → migrations → prepared statements.

**URI path:** Construct DSN with encryption parameters appended before the existing pragmas. Passphrase must be URL-encoded. Then: open → canary query → PRAGMA mmap_size → schema → migrations → prepared statements.

The canary query (`SELECT count(*) FROM sqlite_master`) detects wrong-key errors early. On failure, wrap the error: `"wrong passphrase or corrupted database (check CAPY_DB_KEY)"`.

**File:** `internal/store/store.go` — `checkpoint()` and `Checkpoint()`

Both methods open fresh `sql.Open()` connections. Apply the same key mechanism (PRAGMA or URI) to these connections. Read the passphrase from `os.Getenv("CAPY_DB_KEY")` at call time.

**Verify:** `make test` — all existing tests must pass. Tests now require `CAPY_DB_KEY` to be set (update test helpers to set a test key). Manually verify: start capy with key set → works; start without key → clear error message; start with wrong key → clear error message.

---

## Phase 3: `capy encrypt` Command

### 3.1 Terminal passphrase prompting

**New file:** `internal/terminal/prompt.go`

Add a function for password-style input (no echo):

```
func ReadPassphrase(prompt string) (string, error)
```

Uses `golang.org/x/term.ReadPassword` on the raw file descriptor of `/dev/tty` (or `os.Stdin` as fallback). Returns the entered string (trimmed of trailing newline).

Add a confirmation variant:

```
func ReadPassphraseConfirm(prompt string) (string, error)
```

Prompts twice, returns error if they don't match.

**Verify:** Manual test — run a small program that calls `ReadPassphrase`, verify no echo, verify correct string returned.

### 3.2 Implement `capy encrypt`

**New file:** `cmd/capy/encrypt.go`

New cobra command registered in `main.go`. Flow:

1. Resolve DB path (same pattern as `checkpoint.go`: `--project-dir` flag, `config.Load`, `ResolveDBPath`).
2. Verify DB file exists.
3. Prompt for current passphrase (empty = unencrypted).
4. Read new passphrase from `CAPY_DB_KEY` or prompt interactively (with confirm).
5. Validate new passphrase length (warn if < 32).
6. Open source DB with old key (if empty, open without encryption pragma). Run canary query.
7. Create temp file `<dbpath>.enc.tmp`.
8. Execute: `ATTACH DATABASE '<temp>' AS target KEY '<new-key>'`.
9. Execute: `SELECT sqlcipher_export('target')`.
10. Execute: `DETACH DATABASE target`.
11. Close source DB.
12. Rename `<dbpath>` → `<dbpath>.bak`.
13. Rename `<dbpath>.enc.tmp` → `<dbpath>`.
14. Remove stale `<dbpath>.bak-wal` and `<dbpath>.bak-shm` if present.
15. Verify: open new DB with new key, canary query.
16. Print success message with backup path.

Error handling: if steps 8-10 fail, remove temp file. If step 12-13 fail, the `.bak` preserves the original. Print clear instructions on failure.

**File:** `cmd/capy/main.go`

Register: `rootCmd.AddCommand(newEncryptCmd())`.

**Verify:** End-to-end manual test:
- Create an unencrypted DB (run capy without encryption, index some content).
- Run `capy encrypt`, enter empty old key, set `CAPY_DB_KEY`.
- Verify capy starts with the key and indexed content is searchable.
- Run `capy encrypt` again with a new key (key rotation).
- Verify old key fails, new key works.

---

## Phase 4: Safety Guardrails

### 4.1 Pre-commit hook: reject unencrypted DB

**File:** `internal/platform/precommit.go` (or wherever the pre-commit hook logic lives)

Add a check: if `knowledge.db` (or the configured DB filename) is in the git staging area, read its first 16 bytes. If they match `"SQLite format 3\000"` (the plaintext SQLite magic header), reject the commit with:

```
capy: refusing to commit unencrypted knowledge.db. Run 'capy encrypt' first.
```

The check only fires when the DB file is staged — normal commits that don't touch the DB are unaffected.

**Verify:** Stage an unencrypted DB → commit blocked. Stage an encrypted DB → commit proceeds. No DB staged → commit proceeds.

### 4.2 Update `.gitignore`

**File:** `.gitignore`

Add WAL/SHM sidecar ignore rules after the existing `!.capy/knowledge.db` line:

```
.capy/knowledge.db-wal
.capy/knowledge.db-shm
```

**Verify:** `git status` does not show WAL/SHM files after capy runs.

---

## Phase 5: Documentation

### 5.1 ADR-019

**New file:** `docs/adr/019-encrypted-knowledge-db.md`

Write the ADR as designed (see design.md §ADR-019 section). Supersedes ADR-015.

**File:** `docs/adr/015-knowledge-db-not-tracked-in-git.md`

Update status from `Accepted` to `Superseded by ADR-019`.

**Verify:** Both ADR files exist and cross-reference each other.

### 5.2 README: encryption workflow

**File:** `README.md`

Add a section covering:
- `CAPY_DB_KEY` setup (shell profile, direnv, CI secrets).
- Initial encryption: `export CAPY_DB_KEY=... && capy encrypt`.
- Cross-machine sync: encrypt → `capy checkpoint` → commit → push. On other machine: pull → set `CAPY_DB_KEY` → capy starts.
- Key rotation: set new `CAPY_DB_KEY` → `capy encrypt` → enter old key when prompted.
- Passphrase recommendations (32+ chars, generated with `openssl rand -base64 48` or similar).

**Verify:** Follow the documented workflow on a clean checkout. Verify each step works as described.

---

## Phase 6: Final Verification

### 6.1 Full test suite

Run `make test` and `make test-race`. All existing tests must pass. No regressions.

### 6.2 Integration test

Add an integration test that exercises the full encryption lifecycle:
1. Create an encrypted DB (set test key).
2. Index content via the store API.
3. Close and reopen with correct key — verify content is searchable.
4. Attempt open with wrong key — verify clean error.
5. Run `capy encrypt` equivalent (re-key).
6. Verify old key fails, new key works, content survives.
7. Verify checkpoint works on encrypted DB.

### 6.3 Cross-machine simulation

Manual test:
1. Encrypt DB, checkpoint, commit.
2. Clone to a different directory.
3. Set `CAPY_DB_KEY`, start capy, search for content.
4. Verify everything works.

### 6.4 Code review

Run `kk:review-code` on the final diff. Run `kk:review-spec` to verify implementation matches this plan and the design doc.
