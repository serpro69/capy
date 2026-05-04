# DB Encryption — Tasks

> **Design:** [./design.md](./design.md)
> **Implementation:** [./implementation.md](./implementation.md)
> **Issue:** TBD
> **Status:** pending
> **Created:** 2026-04-26

---

## Task 1: Driver Proof of Concept

**Status:** done
**Dependencies:** none
**Docs:** [implementation.md §Phase 1](./implementation.md#phase-1-driver-proof-of-concept)

- [x] Create PoC test file (`internal/store/encryption_poc_test.go` or temp binary)
- [x] Test option 1: install `libsqlcipher-dev`, build with `-tags "fts5 libsqlite3"` + CGo flags, run validation checklist
- [ ] ~~Test option 2: build sqlite3mc from amalgamation source~~ (skipped — option 3 succeeded)
- [x] Test option 3 (only if 1 and 2 fail): add `go.mod` replace directive for jgiannuzzi fork, run validation checklist
- [x] Validation checklist: encrypted DB creation, FTS5 search, reopen with correct key, reopen with wrong key fails, WAL mode works, PRAGMA rekey + backup API (replaces sqlcipher_export) for both unencrypted→encrypted and re-key, checkpoint works
- [x] For PRAGMA path: verified ConnectHook NOT viable — mattn/go-sqlite3 runs PRAGMA synchronous before ConnectHook, which fails on encrypted DBs
- [x] For URI path: verify key auto-applies to pool connections without ConnectHook
- [x] Document which option was selected and any caveats

---

## Task 2: Build System Update

**Status:** done
**Dependencies:** Task 1
**Docs:** [implementation.md §2.1](./implementation.md#21-update-build-system)

- [x] Update `Makefile` — no CGo flag changes needed (option 3 bundles the amalgamation); remove `libsqlite3` references if any
- [x] Verify `go.mod` replace directive (already added in Task 1 PoC)
- [x] Verify `make build` succeeds
- [x] Verify `make test` succeeds

---

## Task 3: Core Encryption Integration

**Status:** done
**Dependencies:** Task 2
**Docs:** [implementation.md §2.2, §2.3](./implementation.md#22-key-reading-and-validation)

- [x] Create `internal/store/encryption.go` with `RequireEncryptionKey()` (errors on empty env) and `EncryptionKeyFromEnv()` (returns empty string if unset, for use by `capy encrypt` prompt flow)
- [x] Unit test: `RequireEncryptionKey` — empty → error, short → warning + returned, 32+ → returned. `EncryptionKeyFromEnv` — empty → empty string, set → value
- [x] Modify `openDB()` in `store.go`: read key, build URI DSN with `file:` prefix + `cipher=sqlcipher&legacy=4&key=<url-encoded>` + existing DSN pragmas, add canary query (`SELECT count(*) FROM sqlite_master`)
- [x] URI path (PoC winner): append cipher+key params to DSN string (URL-encode passphrase); keep existing `_journal_mode`, `_synchronous`, `_busy_timeout`, `_foreign_keys` as DSN params — they run after key is applied via URI
- [x] Update `checkpoint()` and `Checkpoint()` to build URI DSN with key for fresh connections
- [x] Update test helpers to set `CAPY_DB_KEY` for all store tests
- [x] Verify: `make test` passes, `make test-race` passes
- [x] Manual verify: start capy with key → works, without key → clear error, wrong key → clear error

---

## Task 4: `capy encrypt` Command

**Status:** done
**Dependencies:** Task 3
**Docs:** [implementation.md §Phase 3](./implementation.md#phase-3-capy-encrypt-command)

- [x] Create `internal/terminal/prompt.go` with `ReadPassphrase()` and `ReadPassphraseConfirm()` using `golang.org/x/term`
- [x] Manual test passphrase prompting (no echo, correct string)
- [x] Create `cmd/capy/encrypt.go` with full encryption/re-key flow: initial encryption uses file copy + `PRAGMA rekey` (in-place encrypt on the copy); re-key uses SQLite backup API between two encrypted connections. Both paths preserve the original via `.bak` rename before swapping. Note: `sqlcipher_export` is NOT available in sqlite3mc — see PoC test header for details.
- [x] Register command in `cmd/capy/main.go`
- [x] Manual test: initial encryption (unencrypted → encrypted, empty old key)
- [x] Manual test: key rotation (encrypted → re-encrypted, old key provided)
- [x] Manual test: wrong old key → clear error
- [x] Manual test: passphrase < 32 chars → warning printed
- [x] Verify backup file created and original recoverable on failure

---

## Task 5: Safety Guardrails

**Status:** done
**Dependencies:** Task 3
**Docs:** [implementation.md §Phase 4](./implementation.md#phase-4-safety-guardrails)

- [x] Update `preCommitHookScript()` in `internal/platform/setup.go`: add inline shell header check (first 15 bytes vs `"SQLite format 3"`)
- [x] Reject commit with clear message if unencrypted DB is staged
- [x] ~~Update `.gitignore`: add `.capy/knowledge.db-wal` and `.capy/knowledge.db-shm`~~ — skipped: already ignored by `.capy/**` rule; explicit entries would be redundant
- [x] Verify: stage unencrypted DB → commit blocked
- [x] Verify: stage encrypted DB → commit proceeds
- [x] Verify: no DB staged → commit proceeds
- [x] ~~Verify: `git status` does not show WAL/SHM sidecars~~ — covered by `.capy/**` rule

**Bonus fix:** Split `preCommitHookScript` into `preCommitHookBlock` (no shebang) + `preCommitHookScript` (with shebang). `installPreCommitHook` now uses the block-only variant for replace/append, fixing a pre-existing double-shebang bug.

---

## Task 6: Documentation

**Status:** pending
**Dependencies:** Task 4, Task 5
**Docs:** [implementation.md §Phase 5](./implementation.md#phase-5-documentation)

- [ ] Verify `docs/adr/019-encrypted-knowledge-db.md` matches final implementation (already drafted — update if PoC changed design decisions)
- [ ] Verify `docs/adr/015-knowledge-db-not-tracked-in-git.md` status is superseded (already done — confirm cross-references)
- [ ] Add encryption workflow section to `README.md`: CAPY_DB_KEY setup, initial encryption, cross-machine sync, key rotation, passphrase recommendations
- [ ] Follow the documented README workflow on a clean checkout to verify accuracy

---

## Task 7: Final Verification

**Status:** pending
**Dependencies:** Task 3, Task 4, Task 5, Task 6
**Docs:** [implementation.md §Phase 6](./implementation.md#phase-6-final-verification)

- [ ] `make test` — all tests pass
- [ ] `make test-race` — no race conditions
- [ ] Add integration test: full encryption lifecycle (create encrypted DB → index → close → reopen → search → re-key → verify)
- [ ] Manual cross-machine simulation: encrypt → checkpoint → commit → clone to new dir → set key → verify content searchable
- [ ] Run `kk:review-code` on final diff
- [ ] Run `kk:review-spec` to verify implementation matches design and implementation docs
- [ ] Run `kk:test` to verify test coverage
- [ ] Run `kk:document` to verify documentation completeness
