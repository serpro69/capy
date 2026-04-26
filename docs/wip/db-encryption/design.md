# DB Encryption — Design Document

> **Issue:** TBD
> **Status:** Draft
> **Created:** 2026-04-26
> **ADR:** [019-encrypted-knowledge-db](../../adr/019-encrypted-knowledge-db.md)

## Problem

capy's knowledge base (`knowledge.db`) contains indexed content that ranges from impersonal (documentation fetches, command output) to highly sensitive (session transcripts from sessionflow-rag containing full human/assistant dialogue — credentials, business decisions, PII, security discussions). The DB must be portable across a single user's machines, but must not be readable by anyone who gains access to the file — whether through a cloned repo, a shared filesystem, or a stolen laptop.

ADR-015 prohibited committing the DB to git due to WAL/SHM sidecar corruption. With encryption, the security concern is addressed; the WAL concern is handled by the existing `capy checkpoint` workflow. This feature enables the cross-machine portability story that ADR-015 deferred.

## Solution

Mandatory encryption at rest for `knowledge.db` using AES-256 (or ChaCha20-Poly1305, depending on backend) via a SQLite encryption extension. The passphrase is provided through the `CAPY_DB_KEY` environment variable. Without it, capy refuses to start.

A new `capy encrypt` CLI command handles initial encryption of existing unencrypted DBs and key rotation. The pre-commit hook is extended to reject commits containing unencrypted DB files.

## Design Decisions

### Driver Strategy

capy currently uses `mattn/go-sqlite3` (CGo, bundled SQLite amalgamation). Encryption requires replacing the underlying SQLite with an encryption-capable build. Three alternatives are evaluated in a PoC task, in order of preference:

**Option 1: `mattn/go-sqlite3` + `libsqlite3` tag + system SQLCipher.**
`mattn/go-sqlite3` has a `libsqlite3` build tag that links against a system library instead of the bundled amalgamation. With SQLCipher installed (`libsqlcipher-dev` on Debian/Ubuntu, `brew install sqlcipher` on macOS), the build links against it:
```bash
CGO_CFLAGS="-I/usr/include/sqlcipher" \
CGO_LDFLAGS="-lsqlcipher" \
go build -tags "fts5 libsqlite3" ./cmd/capy/
```
Pros: no code changes to the driver import, actively maintained upstream. Cons: SQLCipher becomes a system dependency; cipher limited to AES-256-CBC.

**Option 2: `mattn/go-sqlite3` + `libsqlite3` tag + system sqlite3mc.**
Same mechanism as option 1 but linking against [SQLite3MultipleCiphers](https://github.com/utelle/sqlite3multipleciphers) built from its amalgamation source. sqlite3mc supports 7 cipher schemes (AES-128/256-CBC, ChaCha20-Poly1305, SQLCipher v1-4 compat, RC4, Ascon128, AEGIS) and offers URI-parameter encryption (`?cipher=chacha20&key=...`). Pros: broader cipher choice, modern AEAD options, URI-based key avoids PRAGMA-first requirement. Cons: no pre-built Linux packages; must build sqlite3mc from amalgamation source.

**Option 3: `jgiannuzzi/go-sqlite3` fork (sqlite3mc branch).**
A fork of `mattn/go-sqlite3` that bundles the sqlite3mc amalgamation directly. Used via `go.mod` replace directive:
```
replace github.com/mattn/go-sqlite3 => github.com/jgiannuzzi/go-sqlite3 v1.14.35-0.20260227142656-2c447b9a2806
```
Latest branch `sqlite3mc-2.2.7` (Feb 2026) bundles sqlite3mc 2.2.7 / SQLite 3.51.2. Pros: no system dependency, drop-in replacement. Cons: maintained by a single contributor who has expressed reluctance to maintain it long-term; upstream PR #1109 to `mattn/go-sqlite3` has been open since Nov 2022 with no maintainer response.

**Design is driver-agnostic.** The encryption integration point is `openDB()` in `internal/store/store.go`. The specific mechanism (PRAGMA key vs URI parameter) depends on which option wins the PoC. All other components (key management, `capy encrypt`, config, hooks) are identical regardless.

### Key Management

`CAPY_DB_KEY` environment variable is the single source of the encryption passphrase. It is:

- **Mandatory.** If unset, capy refuses to start with: `"CAPY_DB_KEY environment variable is required (see: capy encrypt --help)"`.
- **Not persisted.** Not in config files, not in the DB, not cached in memory beyond the `sql.Open` call. Config files are committed to repos and must never contain secrets.
- **Read at each connection.** Both `openDB()` and `checkpoint()` read from the env var when they open fresh connections. This means rotating the env var between runs works naturally.

Passphrase validation: warn (not reject) below 32 characters. Consistent with the approach of standard crypto tools (age, GPG, cryptsetup) which trust the user but surface risk.

### Connection Changes

`openDB()` in `store.go` is the single integration point. The change depends on the PoC result:

**PRAGMA path (SQLCipher / option 1 or 3):** After `sql.Open()`, execute `PRAGMA key = ?` as the very first statement — before mmap_size, schema init, migrations, or any other operation. Sequence becomes: open → PRAGMA key → PRAGMA mmap_size → schema → migrations → prepared statements.

**URI path (sqlite3mc / option 2):** Append `&cipher=chacha20&key=<url-encoded-passphrase>` to the DSN string. No post-open PRAGMA needed.

Both `checkpoint()` (internal, called from `Close()`) and `Checkpoint()` (public, called from `capy checkpoint` CLI) open fresh connections and must apply the same key via the same mechanism.

**Wrong-key detection:** After applying the key, execute a canary query (`SELECT count(*) FROM sqlite_master`). SQLCipher/sqlite3mc return a generic error on wrong key. The wrapper translates this to: `"wrong passphrase or corrupted database"`.

### `capy encrypt` Command

New CLI command in `cmd/capy/encrypt.go`. Dual-purpose: initial encryption (unencrypted → encrypted) and key rotation (old key → new key).

**Prompt flow:**

1. Prompt for current DB passphrase (terminal stdin, no echo). Empty = DB is currently unencrypted.
2. Read new passphrase from `CAPY_DB_KEY` if set. If unset, prompt interactively with confirmation.
3. If new passphrase < 32 characters, print warning. Proceed anyway.

**Encryption process:**

1. Resolve DB path from config (`config.Load` + `ResolveDBPath`).
2. Verify DB file exists.
3. Open source DB with old key (empty = unencrypted). Run canary query to verify access.
4. Create temp target: `<dbpath>.enc.tmp`.
5. Attach target with new key: `ATTACH DATABASE '<target>' AS target KEY '<new-key>'`.
6. Export: `SELECT sqlcipher_export('target')`.
7. Detach and close source.
8. Back up original: rename `<dbpath>` → `<dbpath>.bak`.
9. Rename temp to final: `<dbpath>.enc.tmp` → `<dbpath>`.
10. Remove orphaned WAL/SHM sidecars from original.
11. Verify: reopen new DB with new key, run canary query.
12. Print: `"Encrypted: <dbpath>. Backup at <dbpath>.bak"`.

If any step fails after the backup rename, `.bak` preserves the original. The temp file is cleaned up on error.

Key rotation is identical — step 3 opens with the old key instead of no key.

### Pre-commit Hook

The existing hook in `internal/platform/` calls `capy checkpoint` before commits. It gains one additional check: if `knowledge.db` is staged for commit, verify it's encrypted by inspecting the file header.

An unencrypted SQLite DB starts with the 16-byte magic string `"SQLite format 3\000"`. An encrypted DB has random bytes. If the header matches the plaintext signature, the hook rejects the commit: `"Refusing to commit unencrypted knowledge.db. Run 'capy encrypt' first."`.

The check only fires when the DB file is staged — it does not affect commits that don't touch the DB.

### .gitignore

The existing `!.capy/knowledge.db` (line 38) stays — it's now intentional. Two lines are added for the WAL/SHM sidecars:

```
.capy/knowledge.db-wal
.capy/knowledge.db-shm
```

These sidecars are always transient. `capy checkpoint` flushes them into the main file.

### README Documentation

The README gains a section documenting:

- Setting up `CAPY_DB_KEY` (shell profile, direnv, etc.)
- Initial encryption workflow (`capy encrypt`)
- Cross-machine sync workflow (encrypt → checkpoint → commit → pull → set key)
- Key rotation workflow
- Passphrase recommendations

## Integration Points

| Component | Change |
|-----------|--------|
| `internal/store/store.go` | `openDB()`: read key, apply via PRAGMA or URI, canary query |
| `internal/store/store.go` | `checkpoint()`, `Checkpoint()`: apply key to fresh connections |
| `cmd/capy/encrypt.go` | New CLI command for encryption and key rotation |
| `cmd/capy/main.go` | Register `encrypt` subcommand |
| `internal/platform/precommit.go` | Header check for unencrypted DB |
| `.gitignore` | Add WAL/SHM sidecar ignore rules |
| `Makefile` | Update build tags/flags for encryption backend |
| `go.mod` | Possibly add `replace` directive (option 3) or no change (options 1-2) |
| `README.md` | Encryption setup and workflow documentation |

## Open Questions

- **Which driver option wins the PoC?** Options 1-3 are tested in order. The design is intentionally driver-agnostic so the answer doesn't change anything beyond `openDB()` and the Makefile.
- **sqlite3mc cipher selection.** If option 2 or 3 wins, the default cipher is ChaCha20-Poly1305 (modern AEAD, fast without AES-NI). Should this be configurable via `.capy.toml`? Leaning no — unnecessary complexity for a single-user tool.

## References

- [ADR-015: Knowledge DB not tracked in git](../../adr/015-knowledge-db-not-tracked-in-git.md) — superseded by ADR-019
- [ADR-016: WAL mode and checkpoint strategy](../../adr/016-wal-mode-and-checkpoint-strategy.md) — checkpoint mechanism unchanged
- [ADR-019: Encrypted knowledge DB](../../adr/019-encrypted-knowledge-db.md) — companion ADR
- [SQLite3MultipleCiphers docs](https://utelle.github.io/SQLite3MultipleCiphers/)
- [SQLCipher API](https://www.zetetic.net/sqlcipher/sqlcipher-api/)
- [mattn/go-sqlite3 PR #1109](https://github.com/mattn/go-sqlite3/pull/1109) — sqlite3mc integration PR (open)
- [jgiannuzzi/go-sqlite3 sqlite3mc-2.2.7](https://github.com/jgiannuzzi/go-sqlite3/tree/sqlite3mc-2.2.7) — fork with sqlite3mc bundled
