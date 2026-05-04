# ADR-020: WAL mode incompatible with PRAGMA rekey

**Status:** Accepted
**Date:** 2026-05-04

## Context

`capy encrypt` uses two different mechanisms depending on whether the source database is already encrypted:

- **Unencrypted → encrypted:** `encryptPlain` copies the DB file, opens the copy with the sqlite3mc cipher codec (empty key), and runs `PRAGMA rekey` to encrypt in place.
- **Key rotation:** `rekeyEncrypted` uses the SQLite backup API to copy all pages from the source (opened with old key) to a new DB (opened with new key).

capy uses WAL journal mode for all databases (ADR-016). When `encryptPlain` opens the copied DB file, it inherits the WAL journal mode from the source. sqlite3mc's `PRAGMA rekey` implementation does not support rekeying in WAL mode — it requires rollback journal mode (DELETE, TRUNCATE, or PERSIST).

This caused `capy encrypt` to fail with:

```
PRAGMA rekey: Rekeying is not supported in WAL journal mode.
```

The backup API path (`rekeyEncrypted`) is unaffected because it creates a fresh destination DB that doesn't inherit the source's journal mode.

## Decision

Before executing `PRAGMA rekey`, switch the temporary copy to DELETE journal mode:

```go
tmpDB.Exec("PRAGMA journal_mode = DELETE")
tmpDB.Exec("PRAGMA rekey = '" + store.EscapeSQLString(newKey) + "'")
```

This is safe because:

1. The operation runs on a temporary copy (`<dbpath>.enc.tmp`), not the original.
2. After encryption, the copy is swapped into place and the normal store open (which sets `_journal_mode=WAL` in the DSN) restores WAL mode on first use.
3. No concurrent readers exist on the temporary copy.

## Consequences

- `encryptPlain` works on WAL-mode databases without user intervention.
- The temporary copy briefly uses DELETE journal mode during the rekey operation. This has no performance impact since it's a one-shot operation.
- Test coverage added in `TestEncryptPlainWALMode` with three subtests: confirming the original failure in WAL mode, confirming the fix with DELETE mode, and an end-to-end flow with FTS5 data.
