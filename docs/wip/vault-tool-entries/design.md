# Design: Tool name + inputs in vault tool-result entries

> Status: done
> Created: 2026-06-16
> Branch: vault_tool_cmd
> Related: [v2 plan](../vault/v2/) — this feature implements **v2 Task 1** (the
> `beginImmediate`/`isBusy` → `sqliteutil` consolidation) ahead of v2, and stands
> up the vault migration apply-loop that v2 also depends on.

## Problem

Vault tool-result entries store and render only the tool's *result text*. They do
not record **which tool was called** or **its inputs**. So when a tool-result row
surfaces in `capy vault search` (`role:tool`), in `capy vault show`, or in the TUI
viewer, there is no context tying the output back to its originating call (e.g. a
`Bash` result gives no hint of the command, a `Read` result no file path).

A `tool_result` block lives in a *user* message and carries only a `tool_use_id`.
The tool **name** + **input** live in the earlier *assistant* `tool_use` block,
keyed by `id`. Correlating the two is the core of the feature.

## Three parsers, one correlation

Tool results are processed by three independent JSONL parsers, all of which must
gain the same enrichment:

| Parser | Surface | Persistence |
|--------|---------|-------------|
| `scanner.go` (`extractUserBlocks`) | FTS5 search index rows (`roleTool`) | **Persisted** at import |
| `render.go` (`renderUserContent`) | `capy vault show` text/markdown | Re-parsed from `raw_jsonl` every read |
| `transcript.go` (`renderUserContent`) | TUI viewer (`RoleTool`) | Re-parsed from `raw_jsonl` every read |

`render.go` and `transcript.go` **share** `renderUserContent`, so one signature
change covers both display surfaces. `scanner.go` has its own (sanitizing,
truncating) `extractUserBlocks`.

`contentBlock` already parses `ToolUseID`, `ID`, `Name`, `Input`. The existing
`toolUseSummary(name, input)` already renders the desired summary (`Bash <cmd>`,
`Read <path>`, `Agent <prompt…>`, bare name otherwise) and is already used for
assistant-side `tool_use` rendering — reusing it keeps the result-side label
**consistent** with the call-side label.

### Enrichment format

Each parser builds a `map[toolUseID]summary` from the assistant `tool_use` blocks
(tool_use always precedes its result), then prefixes each tool-result entry:

```
<summary>\n<result text>
```

When the id is unknown (older/malformed transcripts, or a result with no matching
call) the prefix is omitted — graceful degradation, never an error.

**Deferred:** `toolUseSummary` renders inputs only for the common tools
(Bash/Read/Edit/Write/Agent/Task) and the bare name for everything else (MCP tools
etc.). Generic rendering of arbitrary tool inputs is **out of scope** here — see
tasks.md "Not doing". The tool *name* is always added, which satisfies the core
"what tool was called" ask for every tool.

## Backwards compatibility

- **Display (`show` + TUI):** re-parses `raw_jsonl` on every read → existing vaults
  render the enrichment immediately, **no migration**.
- **FTS index:** persisted at import → existing rows are stale. We need a way to
  rebuild them.

### `index_version` as a DB-driven source of truth (decided)

Two options were weighed (second opinion: pal/gemini, see chat). The pure
"re-index only on `capy vault import`" gate has a **fatal flaw**: `import` only
processes sessions still present on disk, so a session archived then deleted
locally would be permanently trapped with a stale index. A background goroutine
sweep was rejected as over-engineering given capy's strict WAL-checkpoint-on-close
invariant and fail-loud ethos.

**Decision:** a per-session `index_version` column is the source of truth, updated
by two decoupled paths:

1. **`import` skip gate (opportunistic, on-disk).** Skip a found session only when
   `hash == existing_hash AND existing_index_version >= currentIndexVersion`.
   A version-stale-but-content-identical on-disk session is re-scanned
   (`ReplaceSession`, which already has `mainBytes` in memory) and bumped.
2. **`capy vault reindex` (explicit, DB-driven).** Re-scans `raw_jsonl` + sidecars
   **from the DB** (no disk dependency) for every session with
   `index_version < currentIndexVersion`, rebuilds its FTS rows, bumps the version.
   This closes the archived-then-deleted-from-disk gap. An explicit command —
   not a background worker — keeps it off the server hot path, observable, and
   free of goroutine/lifecycle/concurrency complexity.

`UpdateSessionFTS(uuid, newVersion, fts)` performs the reindex write: delete FTS by
session + insert new rows + bump `index_version` only. It deliberately does **not**
reuse `ReplaceSession` — that rewrites `raw_jsonl` and every sidecar blob, causing
massive WAL bloat / write amplification across a multi-thousand-session vault for
zero benefit.

### Version semantics

- `currentIndexVersion = 2`. v1 = result-only indexer; v2 = tool-enriched indexer.
- Fresh DBs: `vault_sessions.index_version` is in `schemaSQL` (DEFAULT 1) and every
  INSERT/UPDATE stamps `currentIndexVersion` explicitly → fresh rows are current.
- Migrated (existing) DBs: migration `0003_add_index_version` does
  `ALTER TABLE … ADD COLUMN index_version INTEGER NOT NULL DEFAULT 1` → all
  pre-existing rows become stale (1) and qualify for reindex.

## Migration runner (shared with v2)

`migrations.go` today has only the tracking-table scaffold. This feature builds the
apply-loop + `vaultMigrationApplied(tx, name)` guard, mirroring
`internal/store/migrate.go`. It is extensible: v2 appends its own
`migrate0001_blob_encoding` / `migrate0002_vault_snapshots` functions. Migrations
are name-keyed (PK) and idempotent, so add-order does not matter.

**Naming:** this feature uses `0003_add_index_version`, deliberately skipping the
`0001`/`0002` slots v2's design reserved, so v2 keeps its planned names.

## Consolidation (== v2 Task 1)

Per user decision, `beginImmediate`/`isBusy` are consolidated into `sqliteutil`
first (exported `BeginImmediate(db, lockTable)` / `IsBusy(err)`), the three
duplicate copies deleted, and all call sites updated. The lock-table no-op write is
parameterized (`sources` for the store, `vault_meta` for the vault). This fulfills
v2 Task 1; v2's tasks.md is updated to reflect it.
