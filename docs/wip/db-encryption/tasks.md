# DB Encryption — Tasks

> **Design:** [./design.md](./design.md)
> **Implementation:** [./implementation.md](./implementation.md)
> **Issue:** TBD
> **Status:** pending
> **Created:** 2026-04-26

---

## Task 1: Driver Proof of Concept

**Status:** pending
**Dependencies:** none
**Docs:** [implementation.md §Phase 1](./implementation.md#phase-1-driver-proof-of-concept)

- [ ] Create PoC test file (`internal/store/encryption_poc_test.go` or temp binary)
- [ ] Test option 1: install `libsqlcipher-dev`, build with `-tags "fts5 libsqlite3"` + CGo flags, run validation checklist
- [ ] Test option 2: build sqlite3mc from amalgamation source, link against it, run validation checklist (including URI-param encryption)
- [ ] Test option 3 (only if 1 and 2 fail): add `go.mod` replace directive for jgiannuzzi fork, run validation checklist
- [ ] Validation checklist: encrypted DB creation, FTS5 search, reopen with correct key, reopen with wrong key fails, WAL mode works, `sqlcipher_export` (or equivalent) works for both unencrypted→encrypted and re-key, checkpoint works
- [ ] For PRAGMA path: verify ConnectHook applies key to all pool connections under concurrent access; verify DSN pragmas removed and moved into ConnectHook (after PRAGMA key)
- [ ] For URI path: verify key auto-applies to pool connections without ConnectHook
- [ ] Document which option was selected and any caveats

---

## Task 2: Build System Update

**Status:** pending
**Dependencies:** Task 1
**Docs:** [implementation.md §2.1](./implementation.md#21-update-build-system)

- [ ] Update `Makefile` with encryption-specific build tags and CGo flags (based on PoC winner)
- [ ] Update `go.mod` if option 3 was selected (replace directive)
- [ ] Verify `make build` succeeds
- [ ] Verify `make test` succeeds
- [ ] Verify correct library linkage (`ldd capy` for options 1-2)

---

## Task 3: Core Encryption Integration

**Status:** pending
**Dependencies:** Task 2
**Docs:** [implementation.md §2.2, §2.3](./implementation.md#22-key-reading-and-validation)

- [ ] Create `internal/store/encryption.go` with `RequireEncryptionKey()` (errors on empty env) and `EncryptionKeyFromEnv()` (returns empty string if unset, for use by `capy encrypt` prompt flow)
- [ ] Unit test: `RequireEncryptionKey` — empty → error, short → warning + returned, 32+ → returned. `EncryptionKeyFromEnv` — empty → empty string, set → value
- [ ] Modify `openDB()` in `store.go`: read key, apply via PRAGMA+ConnectHook or URI (based on PoC), add canary query
- [ ] If PRAGMA path: register custom `sqlite3.SQLiteDriver` with `ConnectHook` — apply PRAGMA key first, then all DSN pragmas (journal_mode, synchronous, busy_timeout, foreign_keys) in the hook; remove pragmas from DSN string
- [ ] If URI path: append cipher+key params to DSN string (URL-encode passphrase)
- [ ] Update `checkpoint()` and `Checkpoint()` to apply key to fresh connections
- [ ] Update test helpers to set `CAPY_DB_KEY` for all store tests
- [ ] Verify: `make test` passes, `make test-race` passes
- [ ] Manual verify: start capy with key → works, without key → clear error, wrong key → clear error

---

## Task 4: `capy encrypt` Command

**Status:** pending
**Dependencies:** Task 3
**Docs:** [implementation.md §Phase 3](./implementation.md#phase-3-capy-encrypt-command)

- [ ] Create `internal/terminal/prompt.go` with `ReadPassphrase()` and `ReadPassphraseConfirm()` using `golang.org/x/term`
- [ ] Manual test passphrase prompting (no echo, correct string)
- [ ] Create `cmd/capy/encrypt.go` with full encryption/re-key flow (open source with exclusive access → checkpoint source → attach target → sqlcipher_export → remove sidecars at original paths → rename)
- [ ] Register command in `cmd/capy/main.go`
- [ ] Manual test: initial encryption (unencrypted → encrypted, empty old key)
- [ ] Manual test: key rotation (encrypted → re-encrypted, old key provided)
- [ ] Manual test: wrong old key → clear error
- [ ] Manual test: passphrase < 32 chars → warning printed
- [ ] Verify backup file created and original recoverable on failure

---

## Task 5: Safety Guardrails

**Status:** pending
**Dependencies:** Task 3
**Docs:** [implementation.md §Phase 4](./implementation.md#phase-4-safety-guardrails)

- [ ] Update `preCommitHookScript()` in `internal/platform/setup.go`: add inline shell header check (first 15 bytes vs `"SQLite format 3"`)
- [ ] Reject commit with clear message if unencrypted DB is staged
- [ ] Update `.gitignore`: add `.capy/knowledge.db-wal` and `.capy/knowledge.db-shm`
- [ ] Verify: stage unencrypted DB → commit blocked
- [ ] Verify: stage encrypted DB → commit proceeds
- [ ] Verify: no DB staged → commit proceeds
- [ ] Verify: `git status` does not show WAL/SHM sidecars

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
