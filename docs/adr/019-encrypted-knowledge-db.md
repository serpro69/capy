# ADR-019: Encrypted knowledge DB

**Status:** Accepted
**Date:** 2026-04-26
**Supersedes:** [ADR-015](015-knowledge-db-not-tracked-in-git.md)

## Context

ADR-015 prohibited tracking `knowledge.db` in git due to WAL/SHM sidecar corruption on branch switches. The cross-machine portability use case (single user, multiple laptops) was deferred.

The sessionflow-rag feature (issue #24) adds session transcripts to the knowledge base — full human/assistant dialogue that can contain credentials, PII, business decisions, and security discussions. This makes the DB significantly more sensitive than before, when it held only documentation fetches and command output.

The core tension: session data is the most valuable content in the DB (decisions, rationale, context), but also the most sensitive. Making it unportable defeats the purpose of persisting it.

## Decision

`knowledge.db` is encrypted at rest using SQLite3MultipleCiphers (sqlite3mc) in SQLCipher v4 compatibility mode, via the [jgiannuzzi/go-sqlite3](https://github.com/jgiannuzzi/go-sqlite3) fork (`go.mod` replace directive). The cipher is AES-256-CBC with HMAC-SHA512 and PBKDF2-HMAC-SHA512 key derivation (256,000 iterations). Encryption is mandatory — capy refuses to start without a passphrase.

Committing the encrypted DB to git is safe when two conditions are met:

1. **The DB is encrypted.** A pre-commit hook verifies this by checking the file header (unencrypted SQLite DBs start with the 16-byte magic `"SQLite format 3\000"`; encrypted DBs have random bytes).
2. **The WAL is flushed.** `capy checkpoint` must run before commit (existing behavior from ADR-016). After checkpoint, WAL/SHM sidecars are empty or absent — git tracks only the self-contained main file.

Key management:

- Passphrase is provided via the `CAPY_DB_KEY` environment variable. Not stored in config files.
- `capy encrypt` CLI command handles initial encryption (unencrypted → encrypted) and key rotation.
- Passphrases under 32 characters trigger a warning but are not rejected (consistent with age, GPG, cryptsetup).

What this ADR does NOT change:

- **Default DB path stays XDG** (`~/.local/share/capy/<hash>/knowledge.db`). In-repo storage (`store.path = ".capy/knowledge.db"`) remains opt-in via config.
- **WAL corruption risk.** The sidecar desync problem from ADR-015 still exists. `capy checkpoint` mitigates it by flushing sidecars before commit. The pre-commit hook enforces this.
- **Unencrypted DBs remain uncommittable.** ADR-015's prohibition still applies to unencrypted DBs. This ADR narrows the scope: encrypted + checkpointed = safe to commit.

## Rationale

- Encryption at rest makes the DB file content-opaque without the passphrase. A cloned repo, stolen backup, or shared filesystem exposes only ciphertext.
- sqlite3mc operates below the SQLite API — FTS5, WAL mode, all queries work unchanged. No application-level encryption/decryption that would break full-text search. URI-parameter encryption (`?cipher=sqlcipher&legacy=4&key=...`) applies the key at `sqlite3_open_v2` time, avoiding PRAGMA-ordering issues with the `database/sql` connection pool.
- Environment variable for the key follows the 12-factor pattern and avoids secrets in config files, shell history (unlike CLI flags), or the DB itself.
- Mandatory encryption (vs optional) eliminates the risk class of "forgot to encrypt before sharing." The pre-commit hook is a second safety net.

## Consequences

- `CAPY_DB_KEY` is required to run capy. Breaking change for existing users — must run `capy encrypt` on existing DBs.
- Build system uses the jgiannuzzi/go-sqlite3 fork (sqlite3mc amalgamation bundled) via `go.mod` replace directive. No system library dependency.
- Cross-machine workflow becomes: set `CAPY_DB_KEY` → `capy encrypt` (once) → `capy checkpoint` → commit. On other machine: pull → set `CAPY_DB_KEY` → capy starts.
- README must document the encryption setup and workflow.
