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

**PRAGMA path (SQLCipher / option 1 or 3):** `PRAGMA key` is per-connection, but `database/sql` maintains a connection pool. The main pool in `openDB()` does NOT call `SetMaxOpenConns(1)` — only `checkpoint()` and `Checkpoint()` do. Under concurrent MCP tool calls, the pool creates additional connections that would lack the encryption key, causing silent failures. This is the same class of issue identified for `PRAGMA mmap_size` (see `kk:arch-decisions` — "mmap_size pragma requires Exec, not DSN"), where `ConnectHook` was identified as the upgrade path.

**Critical: DSN pragma ordering.** In `mattn/go-sqlite3` v1.14.37, `SQLiteDriver.Open` executes DSN-driven pragmas (`_journal_mode`, `_synchronous`, `_busy_timeout`, `_foreign_keys`) *before* invoking `ConnectHook`. On an encrypted DB, these pragmas would fail before `PRAGMA key` is applied. Therefore, for the PRAGMA path, all pragmas must be removed from the DSN and moved into the ConnectHook, executed after `PRAGMA key`. The DSN becomes just the bare DB path.

For the PRAGMA path, there are two sub-options:
- **ConnectHook (preferred):** Register a custom `sqlite3.SQLiteDriver` with a `ConnectHook` that executes, in order: (1) `PRAGMA key`, (2) `PRAGMA journal_mode=WAL`, (3) `PRAGMA synchronous=NORMAL`, (4) `PRAGMA busy_timeout=5000`, (5) `PRAGMA foreign_keys=ON`. `retry.go` already uses a named import of `mattn/go-sqlite3` (not blank import), so the pattern is available in the codebase. This ensures every pool connection is keyed and configured.
- **SetMaxOpenConns(1) (fallback):** Serialize all DB access through one connection. Acceptable for a single-user tool but limits concurrency under parallel MCP calls. DSN pragmas must still be removed and applied via post-open Exec calls (after PRAGMA key).

**URI path (sqlite3mc / option 3 — PoC winner):** Append `&cipher=sqlcipher&legacy=4&key=<url-encoded-passphrase>` to the DSN string. The key is applied at `sqlite3_open_v2` time, automatically to every connection the pool creates — **no pool issue**. This is the path the PoC selected.

Both `checkpoint()` (internal, called from `Close()`) and `Checkpoint()` (public, called from `capy checkpoint` CLI) open fresh connections with `SetMaxOpenConns(1)` and must apply the same key via the same mechanism.

**Wrong-key detection:** After applying the key, execute a canary query (`SELECT count(*) FROM sqlite_master`). SQLCipher/sqlite3mc return a generic error on wrong key. The wrapper translates this to: `"wrong passphrase or corrupted database"`.

### `capy encrypt` Command

New CLI command in `cmd/capy/encrypt.go`. Dual-purpose: initial encryption (unencrypted → encrypted) and key rotation (old key → new key).

**Prompt flow:**

1. Prompt for current DB passphrase (terminal stdin, no echo). Empty = DB is currently unencrypted.
2. Read new passphrase from `CAPY_DB_KEY` if set. If unset, prompt interactively with confirmation.
3. If new passphrase < 32 characters, print warning. Proceed anyway.

**Encryption process:**

sqlite3mc does NOT provide `sqlcipher_export()`. Two separate paths are used depending on whether the source DB is unencrypted or already encrypted:

**Initial encryption (unencrypted → encrypted):** File copy + `PRAGMA rekey`.

1. Resolve DB path from config (`config.Load` + `ResolveDBPath`).
2. Verify DB file exists.
3. Open source DB without encryption, `SetMaxOpenConns(1)`. Run canary query.
4. Checkpoint source (`PRAGMA wal_checkpoint(TRUNCATE)`). Close source.
5. Copy file to `<dbpath>.enc.tmp`.
6. Open copy with sqlite3mc cipher codec (empty key — DB is still unencrypted at this point).
7. `PRAGMA rekey = '<new-key>'` — encrypts in place.
8. Close. Swap via rename: original → `.bak`, temp → original.
9. Verify: reopen with new key, canary query.

**Key rotation (encrypted → encrypted):** SQLite backup API.

1–2. Same as above.
3. Open source DB with old key, `SetMaxOpenConns(1)`. Run canary query.
4. Checkpoint source.
5. Open new empty DB at `<dbpath>.enc.tmp` with new key.
6. Backup API (`sqlite3_backup_init/step/finish`) copies all pages from source to dest.
7. Close both. Swap via rename.
8. Verify: reopen with new key, canary query.

The backup API does NOT work across the unencrypted/encrypted boundary, which is why initial encryption uses the PRAGMA rekey path instead.

Both paths: remove WAL/SHM sidecars before rename, preserve original as `.bak`. If any step fails after the backup rename, `.bak` preserves the original. The temp file is cleaned up on error.

### Pre-commit Hook

The existing hook is a shell script generated by `preCommitHookScript()` in `internal/platform/setup.go` (line 151). It runs `capy checkpoint` when the DB file is staged. The script gains an inline header check before the checkpoint call:

```sh
# Check for unencrypted DB — first 15 bytes of an unencrypted SQLite DB are "SQLite format 3"
if head -c 15 "$f" 2>/dev/null | grep -q 'SQLite format 3'; then
  echo "capy: refusing to commit unencrypted $f. Run 'capy encrypt' first." >&2
  exit 1
fi
```

An unencrypted SQLite DB starts with the 16-byte magic string `"SQLite format 3\000"`. An encrypted DB has random bytes at the start. The check is inline shell (no capy subcommand dependency) — `head` and `grep` are available in any POSIX environment.

The check only fires when the DB file is staged — it does not affect commits that don't touch the DB.

### .gitignore

The existing `!.capy/knowledge.db` stays — it's now intentional. No explicit WAL/SHM ignore rules are needed: the `.capy/**` glob already ignores all files under `.capy/` except those with explicit `!` exceptions. The sidecars (`.db-wal`, `.db-shm`) are always transient — `capy checkpoint` flushes them into the main file.

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
| `internal/platform/setup.go` | Header check in generated pre-commit hook script (`preCommitHookScript()`) |
| `.gitignore` | Add WAL/SHM sidecar ignore rules |
| `Makefile` | Update build tags/flags for encryption backend |
| `go.mod` | Possibly add `replace` directive (option 3) or no change (options 1-2) |
| `README.md` | Encryption setup and workflow documentation |

## Breaking Changes

Mandatory encryption is a breaking change. Existing users who upgrade capy will be unable to start until they set `CAPY_DB_KEY` and run `capy encrypt`. This warrants a major version bump (v1.0.0 or next major).

**First-run experience for upgrading users:**

1. User upgrades capy, runs it → error: `"CAPY_DB_KEY environment variable is required (see: capy encrypt --help)"`.
2. User sets `CAPY_DB_KEY` in their shell profile.
3. User runs `capy encrypt` → prompted for current key (empty, since DB is unencrypted) → DB encrypted.
4. capy starts normally.

The error message must be actionable — it names the env var, points to `capy encrypt --help`, and explains why. The `capy encrypt --help` text includes the full setup workflow.

**CHANGELOG entry** must call this out prominently as a breaking change with step-by-step migration instructions.

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
