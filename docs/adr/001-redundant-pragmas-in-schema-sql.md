# ADR-001: Redundant pragmas in schemaSQL

**Status:** Accepted (keep as-is)
**Date:** 2026-03-20

## Context

SQLite pragmas (`journal_mode`, `synchronous`, `busy_timeout`, `foreign_keys`) are set in two places:

1. Via DSN query string in `store.go`: `?_journal_mode=WAL&_synchronous=NORMAL&...`
2. Via `PRAGMA` statements in the `schemaSQL` constant in `schema.go`

## Decision

Keep the duplication.

## Rationale

- `mattn/go-sqlite3` applies DSN `_` pragmas at connection open time for **every** pooled connection. Empirically verified: all 5 connections in a pool get `foreign_keys=1`, `journal_mode=wal`, `busy_timeout=5000`.
- The `schemaSQL` pragmas are therefore redundant but harmless — they run once during init on one connection and set the same values.
- Removing the `schemaSQL` pragmas would make the schema file incomplete as a standalone reference for the DB structure. Keeping them serves as documentation.
- Removing the DSN pragmas would break connection-pool safety (new connections wouldn't get them).

## Consequences

Slightly redundant work on first init. No correctness impact.
