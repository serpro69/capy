# Architecture

## Overview

capy is an MCP (Model Context Protocol) server that reduces LLM context window consumption by ~98%. It intercepts data-heavy tool calls, executes them in sandboxed subprocesses, indexes the output into a persistent SQLite FTS5 knowledge base, and returns only concise summaries to the LLM context.

```
┌─────────────────────────────────────────────────────────────────────────┐
│  LLM (Claude Code / Codex / Cursor / etc.)                             │
│                                                                         │
│  Tool calls: capy_execute, capy_search, capy_batch_execute, ...         │
└───────────────┬─────────────────────────────────────────────────────────┘
                │ MCP (JSON-RPC over stdio)
┌───────────────▼─────────────────────────────────────────────────────────┐
│  capy MCP Server  (internal/server)                                     │
│                                                                         │
│  ┌─────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────┐  │
│  │  10 Tool     │  │  Stats       │  │  Search      │  │  Lifecycle │  │
│  │  Handlers    │  │  Tracker     │  │  Throttle    │  │  Guard     │  │
│  └──────┬──────┘  └──────────────┘  └──────────────┘  └────────────┘  │
│         │                                                               │
│  ┌──────▼──────┐  ┌──────────────┐  ┌──────────────────────────────┐  │
│  │  Executor   │  │  Security    │  │  Content Store (FTS5 + WAL)  │  │
│  │  (sandbox)  │  │  (policies)  │  │  Encrypted with sqlite3mc   │  │
│  └─────────────┘  └──────────────┘  └──────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│  Hook System  (internal/hook)                                           │
│  Runs as short-lived processes: `capy hook <event>`                     │
│                                                                         │
│  PreToolUse:  curl/wget → block, WebFetch → deny, Bash → guidance,     │
│               Agent/Task → inject routing, capy_* → security check      │
│  SessionStart: inject routing block                                     │
│  SessionEnd:   no-op (WAL checkpoint handled by MCP server Close())     │
│  PostToolUse / PreCompact / UserPromptSubmit: stubs (future use)        │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│  Vault  (internal/vault)  — sole session store (ADR-027)                │
│  Background goroutine on server start + CLI `capy vault`                │
│                                                                         │
│  Discovers session JSONL files → scans for FTS text → archives          │
│  verbatim in encrypted vault.db (separate from knowledge store) →       │
│  chunks into vault_chunks FTS for semantic session search               │
│  Federates into capy_search via cross-corpus RRF (ADR-028); also        │
│  cross-project search, restore, resume, and TUI browsing                │
└─────────────────────────────────────────────────────────────────────────┘
```

## Data Flow

### Tool Execution (capy_execute / capy_batch_execute)

1. LLM calls capy tool via MCP
2. Server runs security check against deny policies
3. Executor spawns sandboxed subprocess (process group isolation, env sanitization)
4. Raw stdout captured in subprocess — never enters LLM context
5. If output > 5KB and `intent` provided: auto-index into FTS5, search with intent, return matching sections
6. Otherwise: return truncated stdout (configurable max_output_bytes, default 100KB)
7. Stats tracked for the session

The retrieval engine is corpus-agnostic (`internal/retrieval`, ADR-028): the same
two-layer RRF / rerank / entity-boost pipeline runs over any `Corpus` — the knowledge
store's `chunks`/`chunks_trigram` tables and the vault's `vault_chunks`/`vault_chunks_trigram`
tables both implement it. Fuzzy correction is corpus-supplied (knowledge has a
vocabulary table; the vault passes `nil`).

1. Query sanitized for FTS5 (strip special chars, expand synonyms)
2. Two-layer RRF (Reciprocal Rank Fusion):
   - Porter stemming FTS5 search (AND, then fallback to OR)
   - Trigram substring FTS5 search (AND, then fallback to OR)
3. Fuzzy correction pass if results < limit (Levenshtein against vocabulary table; knowledge corpus only)
4. Post-processing: per-source diversification, title-match boost, proximity reranking, entity boosting
5. Access tracking (last_accessed_at, access_count) for retention scoring
6. **Cross-corpus federation:** when the vault is enabled and `session` is in scope,
   the knowledge and vault result lists are each independently RRF-ranked, then
   rank-merged on `1/(k+rank)` — never on raw BM25, which is incomparable across
   tables (ADR-028). Vault hits are tagged `session:<uuid>`; ties keep knowledge
   before vault. `include_kinds:["session"]` targets the vault alone.

### Fetch and Index (capy_fetch_and_index)

1. SSRF validation (block localhost, private networks, cloud metadata)
2. Git platform URL detection → redirect to platform CLI
3. TTL cache check (skip re-fetch within configurable window)
4. HTTP fetch with timeout and size limits
5. Content-type routing: HTML → markdown conversion, JSON → key-path chunking, text → plaintext chunking
6. Index as ephemeral (default, 24h TTL) or durable (explicit)

## Knowledge Base

### Schema

```sql
sources          — one row per indexed document (label, kind, content_hash, timestamps, access_count)
chunks           — FTS5 virtual table (Porter tokenizer), one row per content chunk
chunks_trigram   — FTS5 virtual table (trigram tokenizer), mirrors chunks for substring search
vocabulary       — unique words extracted from indexed content, used for fuzzy correction
```

### Source Kinds

| Kind | Lifecycle | Default search visibility | Produced by |
|------|-----------|--------------------------|-------------|
| `durable` | Retention-score tiers (hot/warm/cold/evictable) | Included | `capy_index`, `capy_fetch_and_index(kind: "durable")` |
| `ephemeral` | Strict TTL (default 24h) | Excluded | `capy_execute`, `capy_execute_file`, `capy_batch_execute`, `capy_fetch_and_index` |
| `session` | Legacy tier — draining; no new writes (ADR-027) | Served from the vault via federation | The session **vault** (`vault_chunks`); knowledge.db no longer writes `session` rows — pre-existing ones reclaimed via `capy_cleanup purge_session` |

### Retention Scoring (Durable Sources)

```
score = salience × exp(-λ × daysSinceIndexed) + σ × recencyBoost
```

- **salience** = base (0.5 prose, 0.6 mixed, 0.7 code) + frequency bonus (min(0.2, accessCount × 0.02))
- **temporal decay** λ = 0.045
- **recency boost** = 1/(1 + daysSinceLastAccess) when accessCount > 0
- Tiers: hot (≥0.7), warm (≥0.4), cold (≥0.15), evictable (<0.15, never accessed)

### Content Deduplication

SHA-256 hash of content stored per source. On re-index with same label:
- Same hash → update access time only (no re-chunking)
- Different hash → delete old chunks, re-index

### Encryption

- Mandatory at rest via sqlite3mc (SQLCipher v4 compatible)
- Key from `CAPY_DB_KEY` environment variable
- DSN uses URI-parameter encryption: `file:path?cipher=sqlcipher&legacy=4&key=<escaped>`
- PRAGMA rekey incompatible with WAL mode — encryption path uses DELETE journal mode (ADR-020)

## Hook System

Hooks run as short-lived processes (`capy hook <event>`) invoked by the AI coding tool's hook system. Each invocation reads JSON from stdin, dispatches to the appropriate handler, and writes JSON to stdout.

### Hook Events

| Event | Handler | Purpose |
|-------|---------|---------|
| `PreToolUse` | Route Bash, block curl/wget/WebFetch, inject subagent routing, security checks | Main routing logic |
| `PostToolUse` | Stub | Future session continuity |
| `PreCompact` | Stub | Future resume snapshot |
| `SessionStart` | Inject routing block | Teach LLM about capy on session start |
| `SessionEnd` | No-op | WAL checkpoint handled by server Close() |
| `UserPromptSubmit` | Stub | Future user decision capture |

### Guidance System

One-time advisories (Read, Grep, Bash) shown once per session. State persisted to `.capy/guidance-<sessionID>.json` since hooks are separate processes.

### Platform Adapter

The `HookAdapter` interface abstracts platform-specific JSON formats. Currently implemented: Claude Code. Tool name aliases map platform-specific names to canonical names (Gemini CLI, OpenCode, Codex, Cursor, VS Code Copilot, Kiro).

## Executor

The `PolyglotExecutor` runs code in sandboxed subprocesses supporting 11 languages: JavaScript, TypeScript, Python, Shell, Ruby, Go, Rust, PHP, Perl, R, Elixir.

### Sandbox Protections

- **Process group isolation** (`Setpgid`) — kills entire process tree on cleanup
- **Environment sanitization** — ~50 dangerous env vars stripped (LD_PRELOAD, NODE_OPTIONS, etc.)
- **Output hard cap** — process killed if stdout+stderr exceeds 100MB
- **Timeout enforcement** — configurable per-call, default 30s
- **Shell-escape detection** — non-shell languages scanned for embedded shell commands
- **Background mode** — process survives timeout, partial output returned, PID tracked for cleanup

## Security

### Command Evaluation

Security policies loaded from `.claude/settings.json` (project and global). Three-tier evaluation:
1. **deny** — command blocked (deny always wins)
2. **ask** — prompt user for confirmation (hook only, not MCP)
3. **allow** — command permitted

Chained commands (`&&`, `;`, `|`) split and checked individually. Pattern syntax: `Tool(glob)` with `*` wildcard and colon syntax for command prefix matching.

### File Path Evaluation

Read deny patterns (e.g., `Read(.env)`) checked for `capy_execute_file` paths.

### SSRF Protection

`capy_fetch_and_index` resolves hostnames and blocks loopback, private, and link-local addresses.

### Secret Sanitization

All indexed content passes through `sanitize.StripSecrets()` which redacts:
- Provider API keys (Anthropic, GitHub, Slack, AWS, Google, npm, GitLab, DigitalOcean)
- JWTs
- Generic key=value secrets
- `<private>` tag blocks

## Session Indexing

Session transcripts are indexed **only by the vault** (ADR-027). The standalone
`internal/session` sweep that formerly wrote `session`-kind rows into `knowledge.db`
was removed once the vault became a searchable corpus — there is no longer a second
subsystem parsing the same JSONL. The vault's extraction pipeline — a per-platform
decoder feeding the `scanner.go` consumer, operating on stored blobs with no disk
dependency (see [Vault](#vault)) — produces two index layers from each archived
session:

1. **`vault_fts`** — one row per message, for per-line `capy vault search` snippets.
2. **`vault_chunks`** / **`vault_chunks_trigram`** — overlapping turn windows
   (window 4 / overlap 1, mirroring the retired `internal/session` chunk sizing) with
   a `first_line_index` anchor back into the raw JSONL, for semantic session search
   through the shared retrieval core. Produced by `chunker.go` off the scanner's
   per-message `ScanResult`s during import, reindex, and merge.

Both layers are (re)built in the same batched, WAL-checkpointed pass and versioned by
`currentIndexVersion` (see [Search index & `index_version`](#search-index--index_version)).

## Vault

The vault (`internal/vault/`) provides verbatim, cross-project session archival with full-text search for **Claude Code and Codex CLI** sessions. It operates a separate encrypted SQLite database (`vault.db`) independent of the per-project FTS knowledge store.

### Architecture

Five layers:

1. **Storage** (`store.go`) — SQLite connection lifecycle, schema, encryption via `CAPY_VAULT_KEY`, CRUD operations. Shares the open/recovery path with the knowledge store via `internal/sqliteutil/`
2. **Decoders** (`platform.go`, `transcript_model.go`, `claude_decoder.go`, `codex_decoder.go` + `codex_types.go`/`codex_patch.go`) — one per platform; turn archived bytes into the pre-policy transcript model (below). Everything below this seam is per-platform, everything above it is platform-blind
3. **Consumers** (`scanner.go` for FTS extraction, `render.go` for `show`, `transcript.go` for the TUI) — thin policy loops over the transcript model: secret stripping (`sanitize.StripSecrets()`), truncation, FTS exclusion, collapse thresholds, labels
4. **Discovery** (`discovery.go`) — a `Discoverer` per platform: `claudeDiscoverer` walks Claude Code project directories under `config.ClaudeProjectsDir()` for JSONL files and their sidecars (subagents, tool-results); `codexDiscoverer` walks `config.CodexHome()`'s rollout roots. `DiscoverAll` runs every root that exists
5. **CLI/TUI** (`cmd/capy/vault.go` + `internal/vault/tui/`) — cobra subcommand group with interactive bubbletea interface

### Transcript model & platform decoders

Turning archived bytes into something the vault can index or display happens at
exactly one seam. A **decoder** (the `Decoder` interface in `transcript_model.go`;
`DecoderFor(Platform)` selects one) reads a session's raw JSONL and returns a
`Transcript`: session-level `Meta` (platform, the platform's own session id
`PlatformID`, explicit title and untruncated `TitleFallback`, cwd, branch, start/end
time, `ParentUUID`, source) plus an ordered `[]Entry`. Entries are Human, Assistant,
ToolResult or System, each anchored to the physical 0-based `LineIndex` it came from
(the first snapshot's line for a merged Claude progressive snapshot), so
search-to-view anchors stay physical. An Assistant entry is an **ordered** `[]Part`,
each a text part or a `ToolCall` (`Name`, a platform `Summary` such as `Bash <cmd>`,
`exec_command <cmd>` or `apply_patch <files>`, raw `Input`, and an optional
`Launch{Label, ChildUUID}` for a sub-agent spawn). Part order is load-bearing: the
FTS row text and the `show` arrow lines interleave text and calls in block order. A
ToolResult carries the resolved `CallName`/`CallSummary` (decoders correlate call ids
themselves), the verbatim `Body`, and an optional unified `Diff`. A System entry may
be `SearchOnly` — indexed but never displayed, the pre-existing attachment asymmetry
kept deliberately.

Decoders are **pure and pre-policy**: no secret stripping, truncation, exclusion or
collapse. `claude_decoder.go` owns type inference from `message.role`,
progressive-snapshot merge by `message.id`, `queued_command` normalization,
`pr-link`/`away_summary` system text, `<system-reminder>` stripping and
`toolUseResult.structuredPatch` → `Diff`; `codex_decoder.go` is described under
[Codex sessions](#codex-sessions). Contract details consumers rely on: an empty
Human/Assistant/System entry is not emitted, a ToolResult with an empty body **is**;
the Claude title fallback is the plain-string-only rule, computed as
`Meta.TitleFallback` so the scanner never sees Claude content shapes. The stored
title is `sanitize(explicit)`, else `truncate(sanitize(fallback), 120)`. Malformed
physical lines are logged and skipped, with one narrow Claude recovery: when a
torn record is followed without a newline by a complete canonical
`{"parentUuid":...}` record, the decoder discards the torn prefix, keeps the valid
suffix at the same physical `LineIndex`, and logs the recovery. It never attempts
to reconstruct the incomplete prefix.

The three former readers are now thin **consumers** over `[]Entry` and own every
policy. `ScanTranscript` (`scanner.go`, behind `ScanSession(platform, r)`) applies
`ftsExcludedResult`, `sanitize.StripSecrets`, the call-summary prefix and the
head/tail bound, and composes an assistant row from its parts in order (a tool-only
assistant entry is still a row). `displayMessages` (`render.go`, behind
`RenderText(platform, raw)` / `RenderMarkdown`) renders verbatim, collapses
`excludedResultTools` results to a marker and skips `SearchOnly`. `transcriptMessages`
(`transcript.go`, behind `ParseTranscript(platform, raw, subagentIDs)`) adds the
collapse thresholds, diff markers and launch markers and marks a launch marker with a
`ChildUUID` as `Openable`. Both display consumers share `decodeForDisplay`; a
platform-dispatch failure there is logged at warn and yields empty output rather than
a silent Claude default. `ScanSubagent` stays Claude-only (sidecars are a Claude
concept). A new line type is therefore handled **once**, in its decoder — the old
"three parsers must stay in sync" convention is retired.

**Platform dispatch.** `Platform` (`platform.go`) is a string type over a closed
constant set (`PlatformClaudeCode` = `claude-code`, `PlatformCodex` = `codex`)
validated in Go by `ParsePlatform`; there is deliberately **no SQL `CHECK`**, so a
third platform is a constant, not a migration. Import stamps the platform the
discoverer walked into `vault_sessions.platform` and never sniffs; reindex, merge,
show, the TUI, restore and resume dispatch on the stored column.
`DetectFormat(firstLine)` is Codex-positive only (`type == "session_meta"` with an
object payload ⇒ Codex, any other JSON object ⇒ Claude, non-JSON ⇒
`ErrUndetectableFormat`) and is consulted in exactly one situation — a stored value
that is **present but unrecognized** (`resolveStoredPlatform`, shared by merge and
reindex): reindex rebuilds with the sniffed platform, warns, and does **not** rewrite
the column (the FTS-only path, ADR-025); merge stores the resolved value; an
undetectable blob is a per-session error, never Claude by default. An **absent**
column (a pre-0006 merge source) is Claude, unsniffed — 8.8 % of real Claude sessions
open with a `file-history-snapshot` line that a "Claude-shaped keys" test would
reject. The zero-value convention is asymmetric on purpose: an empty in-memory
`Platform` is Claude on both sides (`writePlatform` on write, `Platform.OrClaude()` at
display dispatch — every pre-0006 caller and fixture builds a `Session` without the
field), while `DecoderFor("")` fails with `ErrUnknownPlatform`. Never loosen the
decoder seam to absorb a zero value. `OrClaude` maps **only** the empty value: a
surface that acts on a row (`restore`, and `resume` through the same `restoreTarget`)
resolves the stored value with `ResolveSessionPlatform` — empty ⇒ Claude, recognized
⇒ itself, corrupted ⇒ the same sniff-with-warning, undetectable ⇒ an error before
anything is written — because defaulting a corrupted Codex row to Claude would
restore its rollout under the Claude projects tree.

"Any other JSON object is Claude" is safe only because **every new platform constant
is a reader-version bump** (see [Reader version](#reader-version-min_reader_version)):
a vault holding a third platform's rows is refused at open by a binary that lacks the
constant, so an unrecognized value can only be a corrupted or hand-edited row.
`platform_test.go` pairs each constant with its `readerVersion*` and fails on an
unpaired one. A third agent CLI is one `Discoverer`, one `Decoder`, one constant and
that bump.

**Refactor gate.** The Claude output of all four public readers is byte-identical to
the pre-model readers. `golden_test.go` pins one case per former `switch` arm
(`testdata/golden/`; regenerate only with `-update` in a deliberate commit),
`testdata/golden/DIVERGENCES.md` maps every behavioral divergence between the old
readers to a model field or a consumer policy, and `parity_canary_test.go` (gated on
`CAPY_VAULT_PARITY_BASELINE`) digests the real local corpus. `currentIndexVersion`
was **not** bumped — Claude extraction did not change.

Rationale and rejected alternatives: [ADR-031](adr/031-transcript-model-seam-and-multi-platform-vault.md).

### Schema

```sql
vault_sessions   — one row per session: metadata, raw JSONL blob, encoding
                   ('raw'|'zstd', NULL on legacy rows = raw); platform (NOT NULL,
                   DEFAULT 'claude-code'; allowed 'claude-code' | 'codex', validated
                   in Go, no CHECK) and
                   nullable parent_uuid (no FK; migration 0006 — see Codex sessions).
                   claude_project_dir is the platform LOCATION HINT: mangled project
                   dir for Claude, relative rollout path under $CODEX_HOME for Codex.
                   idx_sessions_parent lives in migration 0006 only, never schemaSQL
vault_files      — associated files (subagents, tool-results), CASCADE on session
                   delete; same encoding column
vault_fts        — FTS5 virtual table, one row per message, Porter tokenizer
                   (tool_result rows tagged with their call summary; Read/NotebookRead
                    and Edit/Write result bodies excluded — see scanner.go ftsExcludedResult)
vault_chunks     — FTS5 virtual table (Porter), one row per overlapping turn window;
                   title + content_text indexed, first_line_index UNINDEXED anchor
vault_chunks_trigram — FTS5 virtual table (trigram), mirrors vault_chunks for substring
                   search; both feed the shared retrieval core (migration 0004, ADR-028)
vault_session_names — capy-owned custom titles, one row per session ever renamed or
                   cleared: session_uuid PK → vault_sessions (CASCADE), custom_title
                   (NULL = clear tombstone, never empty), renamed_at_ns, machine_id
                   (migration 0005; see Session names below)
vault_session_projects — capy-owned project labels: session_uuid PK →
		   vault_sessions (CASCADE), custom_project (NULL = clear,
		   never empty), updated_at_ns, machine_id (migration 0007)
vault_meta       — key-value store; holds min_reader_version (the forward-compat
                   marker: 2 = zstd blobs, 3 = non-Claude platform rows — see below)
vault_migrations — migration tracking (by-name); migration runner lives in migrations.go
```

### Blob compression & storage encoding

Session transcripts and sidecar files are stored zstd-compressed at the blob seam
(`codec.go`: one shared `*zstd.Encoder`/`*zstd.Decoder`, `EncodeAll`/`DecodeAll` —
thread-safe and reentrant). The per-row `encoding` column is **authoritative**:
`encodeBlob` returns `'zstd'` when compression shrinks the input, else `'raw'`;
`decodeBlob` switches on the stored column. There is **no magic-byte auto-detection**
— `vault_files.raw_content` holds arbitrary sidecar bytes (screenshots, build logs,
already-compressed files) that could collide with the zstd frame magic, so the
column, not the bytes, decides. Legacy rows (`encoding IS NULL`) read as raw.
Set `CAPY_VAULT_NO_COMPRESS` to force `'raw'` writes.

`content_hash`, `size_bytes`, and the FTS text are all computed on the
**uncompressed** bytes, so dedup, the larger-wins merge rule, and search are
byte-for-byte unchanged — compression is purely a storage encoding.

The first `'zstd'` write stamps `vault_meta.min_reader_version = "2"` (see
[Reader version](#reader-version-min_reader_version)). `capy vault compact` rewrites
legacy (`NULL`) rows through the codec and `VACUUM`s to reclaim the freed pages
(SQLite never shrinks the file on its own). Full rationale: the vault v2 design doc
and the `kk:arch-decisions` note (the explicit `encoding` column supersedes an
earlier magic-byte design).

### Reader version (`min_reader_version`)

`vault_meta.min_reader_version` is the forward-compat marker: `openDB` reads it after
migration and refuses a vault whose marker exceeds the binary's
`supportedReaderVersion` (now **3**), so an old binary never silently misreads a newer
vault; `MergeFrom` applies the same check to its source. Two named versions live in
`codec.go`: `readerVersionZstd = 2` (the vault holds a `'zstd'` blob) and
`readerVersionPlatform = 3` (the vault holds a row whose platform is not Claude — an
older binary is platform-blind and would restore a Codex row under
`~/.claude/projects/`, `claude --resume` a Codex uuid, or merge a Codex blob as
`claude-code` with empty FTS, permanently). `markMinReaderVersion` is a monotonic
upsert that only raises the numeric value, and **every stamping site passes the
version its own write requires**: the batch writer stamps the highest version any
record in the transaction needs; `compact.go`'s `markCompressed` passes
`readerVersionZstd`, so compacting a Claude-only vault never locks older binaries out.
A vault that never receives a Codex row stays at 2. **Standing rule:** a new platform
constant is a reader-version bump (pinned by `platform_test.go`). There is no opt-out
env var — as with the v2 zstd bump, upgrade the older binary before merging.

### Search index & `index_version`

`vault_fts` holds one row per message — `user` (human turns), `assistant`, `tool`
(tool_result output), and `system`. Tool-result rows are tagged with their
originating call summary (e.g. `Bash <cmd>`), correlated by `tool_use_id`. Result
bodies for tools matched by `scanner.go`'s `ftsExcludedResult` are **excluded from
the index**. That predicate is the union of two sets: `excludedResultTools` (`Read`,
`NotebookRead` — file/cell-content dumps) and `diffResultTools` (`Edit`, `Write`, and
Codex's `apply_patch` — whose body is just a one-line success string; the real change
is the diff — Claude's sibling `toolUseResult.structuredPatch`, Codex's
`*** Begin Patch` call input — never the indexable result body). Both are noise for
conversation search. Codex `exec_command` file dumps (`cat`, `sed -n`) have no
Read-analog exclusion: no tool name distinguishes a read from any other shell
command, so they are indexed like a large `Bash` output, bounded by the head/tail cap. The call itself stays
searchable on the assistant row, and `raw_jsonl` keeps the body for `show`/restore
(it is collapsed only in the *rendered* view — see
[Tool-result display](#tool-result-display-show-vs---tui)).

The extraction logic is **versioned**. `currentIndexVersion` (a constant in
`store.go`) stamps the indexer; every session row records the `index_version` it
was indexed at. Changing what `scanner.go` extracts (including the exclusion set)
makes existing rows stale, so:

- **Bump `currentIndexVersion`** when the change ships across a *released* boundary
  — a vault written by an older release must be detected as stale.
- **Redefine the version in place** (no bump) when the change is still unreleased
  and no shipped/durable vault holds that version yet — a single reindex already
  yields the complete result, so a bump would only force a redundant second pass.
- To stop indexing a tool's result body, add its name to `excludedResultTools`
  (also collapses it in every display) or `diffResultTools` (FTS-excluded but the
  display stays special — see [Tool-result display](#tool-result-display-show-vs---tui)).
  The same bump rule applies to either.

Version 5 adds the Claude torn-line suffix recovery above. Older indexes can lack
an otherwise complete trailing user turn, so this decoder-layer extraction change
also requires a rebuild even though `scanner.go` itself is unchanged.

Stale sessions are upgraded two ways, both rewriting **only** the FTS rows (never
the `raw_jsonl` blob): `capy vault reindex` rebuilds every session below
`currentIndexVersion` from the stored blob (so it reaches sessions already deleted
from disk), and a normal `capy vault import` opportunistically rebuilds an on-disk
session whose content is unchanged but whose index is stale. `capy vault stats`
prints the current version and how many sessions are still below it (the reindex
backlog).

Full rationale and the rejected alternatives: [ADR-025](adr/025-vault-index-version-and-reindex.md).

### Session names & the effective title

`vault_sessions.title` is **imported** metadata — derived by `scanner.go` from the
last `ai-title` record (else the first significant user prompt) and rewritten by the
same insert/replace path whenever a larger transcript arrives. A user-chosen name has
a different owner and lifecycle, so it lives in its own table, `vault_session_names`
(`session_name.go`; migration `0005_session_names`, whose DDL is shared with
`schemaSQL` so fresh and migrated vaults cannot drift). The rename feature never
writes `vault_sessions.title`, and import, `reindex`, `compact`, and `rekey` never
touch `vault_session_names` — a name row simply persists beside whatever the imported
title becomes. Deleting a session cascades its name row.

A row is *state*, not history: a non-NULL `custom_title` is the active override and
NULL is an explicit **clear tombstone**, so merge can tell "never named here" from
"deliberately cleared". The column is never the empty string — `NormalizeSessionName`
rejects an empty rename and merge normalizes an empty source value to a tombstone.

**Precedence lives in exactly one place**: `Session.EffectiveTitle()` (custom title
when present, else imported). Reads select both columns through one `LEFT JOIN`
fragment (`sessionMetaJoin`) and resolve in Go — deliberately no SQL-side `COALESCE`,
so the two layers cannot drift. `GetSession`, `ListSessions`, ambiguous-prefix
candidates, per-line `Search`, chunk `SearchChunks` (`SearchResult.Title`), the CLI,
the MCP result mapping, and every TUI model consume that one resolver. `MATCH`,
BM25/RRF, snippets, and the indexed columns are untouched, and `currentIndexVersion`
is **not** bumped — extraction and indexed content did not change.

**Normalization** (`NormalizeSessionName`, shared by CLI and TUI; strictly in this
order): trim → `sanitize.StripSecrets` → reject empty → reject invalid UTF-8 →
reject control characters → reject more than 120 code points (measured *after*
redaction, since redaction changes length). A name matching a credential pattern is
therefore stored redacted — the same invariant search snippets already keep.

**Local writes** (`RenameSession` → `renameSessionAt`) resolve the UUID prefix
(literally — `LIKE` metacharacters are escaped) and upsert inside one
`BeginImmediateContext` transaction, so a concurrent delete cannot orphan a row. The
stored timestamp is `max(now.UnixNano(), stored renamed_at_ns + 1)`: an explicit local
action is always newer than the state it edits, even across a backward clock step.
`machine_id` is `MachineID()`. This bump applies to local operations only — merge
writes a winning source tuple verbatim (see Archival Paths).

**Name lookup** (`ListOptions.Name` → `vault list --name`; the TUI `f` filter) is a
literal, case-insensitive substring over the *effective* title, folded in Go with
`ContainsFold` (`strings.ToLower` on both sides) because SQLite's `lower()`/`NOCASE`
fold ASCII only. The project predicate stays in SQL; the name predicate and `Limit`
are applied in Go **after** title resolution (a SQL `LIMIT` would pre-truncate the
candidates). Name terms are deliberately not part of transcript or chunk FTS.

Rationale and rejected alternatives: [ADR-030](adr/030-vault-session-names-and-latest-wins-merge.md).

### Session project assignments

`session_project.go` owns local project edits beside the imported
`vault_sessions.project_path`. `SetSessionProject` resolves a literal UUID prefix
and writes `vault_session_projects` within one immediate transaction. Its local
clock advances as `max(now, previous + 1)` with an explicit overflow error,
independently of title edits. Fresh schema and migration `0007_session_projects`
share the table DDL. No row means never edited; a NULL override is a clear
tombstone. Session deletion cascades either state.

`Session.ProjectOverride` travels through the shared metadata projection and
`EffectiveProject()` returns the override or latest imported path. Title and
project labels share normalization, with field-specific error messages. Local
set/clear preserves transcript bytes, sidecars, imported metadata, FTS/chunk rows
and index versions; it neither reindexes nor changes the reader-version marker.

`ListSessions` applies a shared effective-project SQL expression
(`COALESCE(p.custom_project, s.project_path)`) and an escaped, bound LIKE predicate
before LIMIT. SQL and Go precedence are tested together for absent, overridden,
path-equal and cleared state. With a title filter, the existing Go-side title
predicate still runs before the limit. These reads use the one-to-one metadata
join and never decode transcript blobs.

Per-line `Search` joins project metadata once and applies `projectScopePredicate`
before `ORDER BY rank LIMIT`. `SearchOptions.Project` selects the effective
project; `ProjectPath` selects only the imported path for implicit filesystem
scope. Supplying both is an error, including for an empty transcript query.
`SearchResult.Project` and `Session.EffectiveProject()` share the Go resolver;
`CustomProject` preserves display provenance, and `ProjectPath` stays imported.
Project words never enter MATCH; titles, snippets, ranking and navigation anchors
remain unchanged for equivalent candidate sets.

CLI browsing and per-line search display the effective project; override
provenance keeps custom labels literal even when they equal an imported path. Show/delete details retain
the original path separately when different. List/search JSON expose both `project`
(effective) and `project_path` (imported); raw show JSON is unchanged.

`SearchChunks` uses the same scope predicate in `CorpusConfig.FilterSQL` for both
porter and trigram queries, before their candidate limits. Its one-to-one project
join carries the nullable override through `chunkMeta`; the shared Go resolver
populates `SearchResult.Project` without changing imported `ProjectPath`, indexed
content, rank inputs, or navigation metadata.

Both MCP search handlers resolve request scope with `vaultProjectScope`: widening
(`all_projects` or exact `project: "*"`) first, then a non-empty explicit effective
project, otherwise the server directory as raw `ProjectPath`. The helper never
changes `Server.projectDir`. `formatVaultHit` displays the resolved project
literally for both MCP search tools.

Federated `capy_search` uses `HasSessionsInProject` for its empty-knowledge-store
preflight. This metadata-only `SELECT EXISTS` uses `projectScopePredicate`, with
the same effective/raw scope passed to `SearchChunks`. It neither reads transcript
blobs nor requires indexed chunks. Statistics remain the source of backlog hints,
but do not determine availability. A failed existence query is reported once
in-band and leaves both search passes enabled; only a definitive empty scope can
trigger the empty-KB guide. Knowledge scoping, source filters and kind selection
retain their existing behavior.

`Stats` preserves `VaultStats.ByProject` as the raw-path aggregation and adds
`ByEffectiveProject` with `EffectiveProjectStat.Project`/`Count`. The additional
metadata-only query joins project state once and groups by the shared effective
expression, ordered by count descending then exact project value. Case-distinct
values stay separate, and each session (including children) contributes once to
each breakdown. No transcript decoding or per-session query is involved.
Ordinary `vault stats` prints these effective groups literally. JSON retains
`projects` (`project_path`/`count`) and adds `project_groups` (`project`/`count`),
with empty arrays for an empty vault. Platform and child counts remain unchanged.

Merge feature-detects `vault_session_projects` without migrating the source.
Filtered source enumeration reads only UUID/path/override metadata, normalizes
foreign empty or whitespace-only overrides to clear, then uses an ASCII-only
literal substring matcher. It closes the cursor before loading any transcript.
Unfiltered enumeration stays UUID-only; legacy sources use imported paths.

Project reconciliation orders `(updated_at_ns, machine_id, value)` independently
of titles, with non-null values beating null at equal clock/writer tuples and
bytewise ordering between values. Winning tuples are stored verbatim. The
metadata-only branches reconcile both fields in one immediate transaction;
`SessionWrite.Project` carries project state alongside titles for new/replaced
transcripts. A failure rolls back both fields and any transcript write. Dry runs
project the same winners, and successful writes report committed metadata through
a metadata-only read. `ImportedSession.ProjectPath` remains imported;
`Project`/`CustomProject` carry the effective value and display provenance.
Disk-import discovery and reports retain physical paths.

TUI `Options.Project` carries the CLI list/search effective-project scope into
the initial read, every list refresh/child toggle and live search. The local `f`
finder uses `EffectiveProject()` alongside the existing Unicode-folded title/UUID
operands. List/search/viewer projections preserve literal labels; a differing
original path gets a separate viewer row with reserved height. Metadata rows are
bounded in terminal display cells.

The root TUI editor distinguishes title and project targets. `ctrl+g` routes
before list/search inputs but after an already open editor. Entry reads the
selected session's authoritative metadata, prefills only the override, and
reserves a separate original-path row. `SetSessionProject` runs as a Bubble Tea
command with the program context; the store owns normalization and length
validation, with no preceding input truncation. Pending writes consume duplicate
submissions. Failures retain the editor text; successful writes refresh viewer
metadata and independently refresh the scoped list and search. Search sequence
IDs invalidate older results, including when a list reload fails, and refresh
failures explicitly report that the write succeeded. Height-only viewer layout
changes preserve offsets within messages; suspended parent frames stay untouched.
The [design](feat/wip/vault-project-names/design.md) defines the complete contract;
[Task 10](feat/wip/vault-project-names/tasks.md#task-10-maintenance-documentation-and-final-verification)
tracks the remaining maintenance and full-feature checks.

### Tool-result display (`show` vs `--tui`)

`raw_jsonl` is always stored verbatim, so `vault show --format json` and `restore`
are byte-faithful. Three **display/index surfaces** decide how a `tool_result` body
is *presented*. Two tool sets steer them (the readers live in different files, so the
coupling is non-obvious):

| Surface | Consumer | Dump tool (`excludedResultTools`: Read/NotebookRead) | Diff tool (`diffResultTools`: Edit/Write/apply_patch) | Large other (e.g. a big `Bash` log) |
|---------|----------|------------------------------------------------------|-------------------------------------------------------|-------------------------------------|
| FTS index | `scanner.go` `ScanTranscript` (via `ftsExcludedResult`) | body excluded | body excluded | indexed (head/tail truncated) |
| `vault show` (pager) | `render.go` `displayMessages` | one-line "output omitted" marker | **unchanged — verbatim success body** | full inline |
| `vault show --tui` (viewer) | `transcript.go` `transcriptMessages` / `viewerToolMessage` | collapse-then-open marker (raw body) | collapse-then-open marker (**reconstructed diff**) | collapse-then-open marker if over threshold |

`excludedResultTools` (Read/NotebookRead) drives all three surfaces identically —
their body is a file/cell dump, so it is dropped from search, collapsed to a
one-liner in `show`, and a collapsible marker in the TUI. `diffResultTools`
(Edit/Write/apply_patch) shares only the **FTS** column (both sets feed `ftsExcludedResult`);
its display is **deliberately decoupled** (vault v2 § Addenda A3): plain `show` keeps
the verbatim one-line success body (collapsing it would hide nothing useful), while
the TUI collapses to a marker that expands to a **colored unified diff**.

The diff isn't in the result body (which is the success string). The **decoder**
fills `ToolResult.Diff`: the Claude decoder from the sibling top-level JSONL field
`toolUseResult.structuredPatch` via `diff.go`'s `diffBodyFromToolResult`, the Codex
decoder from the matching `apply_patch` call's `*** Begin Patch` input via
`codex_patch.go`'s `codexPatchToDiff` — **only when the result reports success**, so
the viewer never shows a diff that was not applied. Both produce unified-diff *text*
(kept in package `vault`); `viewerToolMessage` (`transcript.go`) turns a `Diff` into
the marker (a `Diff` wins over an empty body), and the `tui` package owns the *color*
(`render.go` `renderDiffBody`: `+` green / `-` red / `@@` cyan), which bypasses the
build-tagged `renderBody`/glamour seam so a diff is never markdown-mangled. The marker
shows a `(+a −b)` stat instead of a line count. A diff tool whose result carries no
diff falls back to the plain success body. To extend the diff-view to another tool
(e.g. `MultiEdit`), add it to `diffResultTools`. The FTS exclusion is versioned
(above); the two display paths are not.

The TUI adds one display-only behavior beyond the shared set (vault v2 § Addenda
A1): **any** large result collapses, not just excluded ones —
`overCollapseThreshold` (>20 lines or >2000 bytes; constants in `transcript.go`).
`vault show` and the FTS index ignore size; only the viewer collapses by threshold.

A collapsed result renders as a **focusable openable marker**, the same `]`/`[` +
`enter` mechanism subagent launch points use. Markers are *role-dispatched* at the
open seam: a subagent marker opens a sidecar transcript by id (`openSubagent` →
`GetFiles`), whereas a collapsed `tool_result` body is inline in `raw_jsonl`, so it
opens a distinct in-memory target (`openInlineContent`, rendering the body carried
on `TranscriptMessage.Body`); both return with `esc`/`q`. An Edit/Write diff uses
that **same** inline target — only the render differs: `TranscriptMessage.Diff`
routes it through `renderDiffBody` instead of the plain word-wrap. A sub-agent
marker whose `TranscriptMessage.ChildUUID` is set (a Codex child) names a *session*,
not a sidecar, and the viewer owns no store handle — so `openFocusedMarker` returns
`openChildAction(uuid)` and the root `Model` (`app.go`) loads the child through
`dataStore`, pushes the current viewer onto `viewerStack` as a `viewerFrame`, and pops
it verbatim on `esc`/`q` (an unarchived child is a transient status line, not an
error). To add a new openable kind, touch `renderTranscript`'s marker branch +
`markerRowFor` (`render.go`) and `openFocusedMarker`'s role switch (`viewer.go`).
`transcriptMessages` is TUI-only and kept separate from `displayMessages` precisely so
a viewer-only change cannot perturb the static `show` output.

**Viewer invariant — one rendered row is exactly one viewport line.**
`renderTranscript` builds a `[]rows` slice joined with `"\n"`, and the bubbletea
viewport splits that string back on `"\n"`. The entire scroll/anchor layer
(`msgRowStart`, `rowForMarker`, `rowForLine`/`lineForRow`, and `focusMarker`'s
`SetYOffset`) treats the row index and the viewport line index as the same number,
so it holds only while no row carries an embedded newline. Body rows are safe —
`wrapBody`/`renderDiffBody` split per line — but a **marker** row `Render`s a label
that can contain newlines (a subagent launch label falls back to the multi-line
Task `prompt`), so marker rows are flattened through `singleLine` (`render.go`). A
multi-line row silently shifts every later row's true line below its recorded
`msgRowStart`; the symptom is `]`/`[` scrolling the focused marker off-screen
(regressed once, June 2026). Any new `rows` producer must emit single-line rows.

### Codex sessions

**Discovery.** `codexDiscoverer` (`discovery.go`) walks `$CODEX_HOME/sessions` and
then `$CODEX_HOME/archived_sessions` (`config.CodexHome()`, honoring `CODEX_HOME`,
default `~/.codex`; depth-bounded by `codexRolloutMaxDepth`) for regular files named
`rollout-<local ts>-<uuid>[_<rollout_id>].jsonl[.zst]` (`parseRolloutFilename`;
unparseable names warn and skip). Roots are visited in that fixed order and sorted
within each root only, so an active copy of a thread always precedes its archived
copy. Each survivor costs one bounded first-line read (`readRolloutFirstLine` — a
streaming zstd reader for `.zst`, never a whole-frame decode) to pick up
`session_meta.cwd` as the `ProjectPath` hint. `CodexDiscoverOptions.Skip` is
evaluated from directory metadata **before** the open, so the sweep's already-archived
predicate keeps unchanged files unopened. A `_<rollout_id>` **revert variant**
(Codex `thread/revert`) is recognized and skipped with a warning — v1 does not
archive them, divergent files are not orderable by size — and counted in
`DiscoveryReport.SkippedRevertVariants`, which `import` and the sweep log print.
`DiscoverAll(opts, only...)` walks every platform root that exists;
`DiscoverSessionsReport(root)` autodetects a Claude or Codex layout for `--source`.
`HasCodexRolloutRoot` is the existence probe the sweep and `doctor` share.

**Identity and location.** The row key is the **filename uuid** (what Codex's own
DB-less resume scans for); `session_meta.id` (`Meta.PlatformID`) is informational and
a mismatch logs a warning. `claude_project_dir` keeps its name and generalizes to
**the platform location hint**: the mangled project dir for Claude, the
slash-separated rollout path relative to `$CODEX_HOME` with `.zst` stripped for Codex
(the only faithful way to restore a local-time filename). Codex archive/unarchive is
a move, so the first same-hash sighting of a Codex uuid at a different path performs
a metadata-only `UpdateLocationHint` (its own immediate transaction, gated on
`PlatformCodex` — a Claude hint is never touched) and reports `updated` — **only if
nothing still exists at the stored hint** (`codexRolloutPresent` stats the plain file
and its `.zst` twin under `SessionFile.Root`, the home the walker was given). With
both copies on disk the file in hand is a lingering duplicate and is `skipped`,
whichever copy import happens to see: the sweep's skip predicate drops the copy at
the stored hint unopened, so discovery order alone would have flipped the hint on
every start. The hint follows the file once the old location is empty. Import keeps
an in-run map of every uuid it has seen so two files for one thread in one run never
collide on the primary key (`SessionDigest` sees only committed state). `.zst`
rollouts are decompressed before hashing: `raw_jsonl`, `content_hash` and
`size_bytes` are always the plain JSONL bytes. The zero-message exclusion is
unchanged — a rollout aborted before the model answered (`task_started` then
`turn_aborted`) reports `excluded`, at debug, like an empty Claude session.

**Sub-agents.** Codex writes each spawned agent as its own rollout, and the vault
mirrors that: a child is its own row with `parent_uuid` from its `session_meta`
(lifted `parent_thread_id` or `source.subagent.thread_spawn`), **no foreign key** —
a child may arrive before its parent, and deleting a parent never cascades
(`delete` warns via `printDeletePreview`). Children are hidden from `list` and the
TUI list by default (`ListOptions.IncludeChildren`, `--include-children`, TUI key
`s`), listed in the parent's `show` header (`Children`), and opened from the parent's
launch markers in the TUI (see [Tool-result display](#tool-result-display-show-vs---tui)):
`Launch.ChildUUID` resolves from a `collab_agent_spawn_end` event (legacy) or a
`SubAgentActivity` item (paginated). A child from Codex ≥ 0.147 records no human
turn; its title falls back to `agent_nickname · agent_role`. A parent and its
children always share one platform.

**Decoder specifics.** Human turns come from the event stream (`user_message`
events, or paginated `item_completed` `UserMessage` items), falling back to
noise-filtered user `response_item`s only when events yield none (drop the
`developer` role and any text starting with `<` or `# AGENTS.md instructions`).
Assistant messages become Assistant entries; `function_call`, `custom_tool_call` and
`web_search_call` attach as `ToolCall` parts (`exec_command <cmd>`, `exec <first
line>`, `apply_patch <files>`, `spawn_agent <task> (<type>)`, `web_search <query>`,
bare MCP names). Results are correlated by `call_id`; the `exec_command` wrapper
header is stripped (`stripExecHeader`, keeping the `Process exited with code N`
status line as search signal); a **successful** `apply_patch` result gets a `Diff`
converted from Codex's `*** Begin Patch` format (`codexPatchToDiff`). `reasoning`,
`compacted`, `turn_context`, `token_count`, `world_state` and unknown types are
skipped (ADR-021) with a per-file debug fingerprint of unseen types; a non-subagent
file with assistant entries but no human turn logs one warning with `cli_version` and
`history_mode` (the Codex analog of the zero-turns signal). `codex_canary_test.go`
runs decoder, discovery and consumer assertions over the real local `$CODEX_HOME`
(skipped when absent).

**Restore and resume.** `RestoreSessionAt(uuid, mainRel, …)` writes the main file at
its stored relative path under the platform root (`$CODEX_HOME` for Codex),
byte-identical to the decompressed input, through the same path-safety validation as
sidecars (`validateSidecarRel` before the root is created); a `.jsonl.zst` twin at
that path is left untouched and reported in `RestoreResult.Notes`. `resume` refuses a
Codex row **before** any lookup or restore (`checkResumable`) with a message naming
`capy vault restore` and `codex resume <uuid>`; launching Codex is a recorded
follow-up.

**Display.** `Platform.ShortID` shows 12 characters for Codex ids (UUIDv7 prefixes
collide at 8) and 8 for Claude wherever the CLI or MCP prints a short id; the TUI
already truncates at 12. Assistant headings use `Platform.DisplayName()`
(`Claude`/`Codex`); every other surface — `list`, the `show` header, `search`,
`--json`, MCP hit meta lines (`· <token>[ · child of <short id>]`, `formatVaultHit`),
`stats`, `doctor` — prints the **stored token** (`claude-code`/`codex`), the value
`--platform` accepts. `capy doctor` and
`capy_doctor` report each platform root's existence and archived count through
`vault.PlatformRoots` (the same `resolvePlatformRoots` the sweep uses) into the
strings-only `platform.CheckVaultPlatforms` — `internal/platform` must not import
`internal/vault`, pinned by a test.

### Archival Paths

**Minimum-size admission (issue #106).** `[vault] min_session_bytes` is an opt-in, non-negative byte limit, defaulting to zero (disabled). `ImportOptions.MinSessionBytes` and `MergeOptions.MinSessionBytes` gate only UUIDs absent from the destination, after its digest lookup and before scanning/queuing new records. Imports use the existing uncompressed transcript-plus-sidecar total; merges use the source's uncompressed `size_bytes`. Values strictly below the minimum report `excluded` with a reason; equality qualifies. There is no exclusion tombstone: later growth or a lowered limit permits admission. Existing archives still receive updates, index upgrades, location changes, and name reconciliation. The separate zero-message exclusion remains in force.

Both platform startup sweeps pass the server's loaded config to the importer. Manual import and merge load config for the invocation project (`--project-dir` or root detection), apply one policy across the run, and accept `--min-size-bytes` as an override, including zero. Pointer-based overlay detection preserves explicit zero overrides across config layers. Dry runs use the same admission rule. Filtering does not purge archived sessions or remove source files; it changes no schema, stored size/hash definition, or index format.

1. **MCP server startup** — background goroutine (`server.go` `vaultSweep`, opt-in via `CAPY_VAULT_KEY`) discovers **each platform independently** — a Claude failure or empty result never skips Codex. It imports the current project's Claude sessions, then the Codex rollouts whose first-line `cwd` equals the project dir (cleaned, symlink-resolved; a rollout with no cwd hint is unreachable by the sweep). Before Codex discovery it loads `CodexLocationSizes` once and hands the discoverer a `Skip` predicate — plain rollout: relative path **and** on-disk size match; `.zst`: path matches (rollouts are append-only, compressed ones immutable) — so an archived, unchanged file is never opened. Accepted bound, stated in the function comment: one bounded first-line read per rollout not archived at its current `(path, size)`, other projects' rollouts included, on every start, under the 30 s budget. Discovery honors that budget too — every `Discoverer.Discover` takes the sweep's context, the Codex walker checks it before each entry and hands back what it found with `ctx.Err()`, and the sweep drops a cancelled walk at debug (Import would refuse the partial list at its first session boundary anyway) so the next start resumes from what is archived and shutdown never waits on a walk. `CAPY_VAULT_SWEEP_ALL` widens both platforms. A missing `$CODEX_HOME` is a debug line; a Codex home with no rollout root does not even open the vault (`HasCodexRolloutRoot`)
2. **`capy vault import`** — manual; every platform root that exists (`DiscoverAll`), `--platform` to restrict, `--source <dir>` with layout autodetection; idempotent (hash-based, larger-total-size wins) plus the in-run reconciliation and the Codex location policy above. The summary is per platform when a run touched more than one, and counts skipped revert variants
3. **`capy vault merge --from <path>`** — non-destructive cross-machine union (`merge.go`): reads another vault's `vault_sessions`+`vault_files`, applies the same idempotent digest decision (distinct added, larger-wins on UUID overlap), carries source metadata verbatim, re-scans FTS with the destination's decoder for the carried platform. Feature-detects a v1 (no `encoding` column) source and a pre-0006 (no `platform`/`parent_uuid`) source — absent columns mean Claude, **never sniffed**; a present but unrecognized value is sniffed with a warning and the resolved value is stored (`resolveStoredPlatform`). `--project` matches the source's effective project with literal ASCII case-insensitive substring semantics; location hints are no longer aliases. Legacy sources fall back to imported `project_path`. A source whose `min_reader_version` exceeds this binary's is refused before any row. Destination writes tolerate a concurrent server sweep via busy-timeout retry. The established source opener enables WAL and checkpoints it; source schema and archived contents are not migrated or rewritten.

   **Custom names reconcile on an independent track.** The source `vault_session_names` table is feature-detected (a pre-0005 source contributes none). For every source session whose UUID exists in the destination — *including* sessions the zero-message exclusion drops and transcripts skipped as same-hash or smaller — the source name state wins when its `(renamed_at_ns, machine_id)` tuple is greater; an absent destination row loses to any tuple; an equal tuple (machine IDs can collide via `CAPY_MACHINE_ID` or a synced dotfile) is broken by value — a non-NULL title beats a tombstone, two titles compare bytewise and the greater wins — so convergence never depends on unique machine IDs. A winning state is written **verbatim** (never re-stamped with the local `max(now, stored+1)` bump, which would break idempotence), a NULL title clears, and a new session plus its name commit in one transaction to satisfy the foreign key. Name-only changes report `updated`, identical/older states `skipped`, and dry-run reports the prospective effective title. Known non-destructive gap: an older binary merging *from* a newer vault reads no `vault_session_names` and carries no names; re-running with an upgraded binary carries them.

### CLI Commands

| Command | Description |
|---------|-------------|
| `capy vault import` | Scan and archive sessions from every platform root that exists (mutating; `--dry-run` to preview; `--platform claude-code\|codex` to restrict; `--source <dir>` autodetects a Claude or Codex layout; `--project` matches the Claude project dir or the Codex cwd hint) |
| `capy vault reindex` | Rebuild the FTS index for sessions on an older `index_version` (DB-driven; no disk dependency; batched + WAL-checkpointed; dispatches on the stored platform) |
| `capy vault list` | List sessions, reverse chronological (`--project` in SQL; `--name` — literal, Unicode case-folded substring over the effective title, applied in Go before `--limit`; `--platform`; `--include-children` — child sessions are hidden by default and show `↳ <parent id>` in the title cell; a PLATFORM column; JSON carries `platform` and `parent_uuid`) |
| `capy vault search` | Full-text search with snippets (PLATFORM column reads `codex (child)` for a child hit; JSON carries `platform`/`parent_uuid`) |
| `capy vault show` | Display full session (pager, `--format` for export). The header prints the platform, a child's parent and a parent's children; the assistant heading is `Claude`/`Codex` |
| `capy vault restore` | Write JSONL + session files back to disk — under the Claude projects dir as `<uuid>.jsonl` + sidecars, or under `$CODEX_HOME` at the stored relative rollout path (a `.zst` twin is left untouched and noted) |
| `capy vault resume` | Restore + launch `claude --resume` — Claude Code only; a Codex session fails loudly before anything is restored, naming `capy vault restore` and `codex resume <uuid>` |
| `capy vault delete` | Remove a session from the vault (cascades its name row; warns when the session has children, which are never cascaded) |
| `capy vault rename` | Set (`<name>`) or clear (`--clear`) the capy-owned name of a session; prints the resulting effective title. Writes only `vault_session_names` — never `raw_jsonl`, hashes, or FTS |
| `capy vault stats` | DB size, session count, per-project and per-platform breakdown, child count, index version + reindex backlog |
| `capy vault checkpoint` | Flush WAL (required before cross-machine copy) |
| `capy vault compact` | Recompress legacy (`encoding IS NULL`) blobs through the zstd codec + `VACUUM` to reclaim disk. No-op if nothing is uncompressed; aborts under `CAPY_VAULT_NO_COMPRESS` or a busy DB (stop the server first) |
| `capy vault merge --from <path>` | Non-destructive cross-machine union (see Archival Paths). Titles and projects reconcile independently. Source key via `--key`/`CAPY_VAULT_MERGE_KEY`/`CAPY_VAULT_KEY`; `--project` (source effective project), `--dry-run` |
| `capy vault rekey` | Rotate the encryption key to the current `CAPY_VAULT_KEY` via `sqliteutil.Rekey` (SQLite backup-API: open old → checkpoint → copy into a new file under the new key → swap+verify). Sidesteps the WAL/PRAGMA-rekey incompatibility (ADR-020) by writing a fresh file. Stop the server first; `--remove-backup` unlinks the old-key `.bak` |

`list`, `search`, and `show` support `--tui` for interactive browsing/search/viewing.
A `list --platform <platform> --tui` launch retains that predicate for the initial
list, every list reload, and live search queries. `list` and `search` launches
also retain `--project` across reloads and mode changes. Other interactions are: filter `f`
— effective title, effective project, or UUID via the shared `ContainsFold`
matcher; rename `e` in the list and viewer, `ctrl+e` in search because its query
input is always focused and a printable key would be untypeable; project edit
`ctrl+g` in list (including its active finder), search and viewer; copy `c`; restore
`r`; resume `R`; `s` in the list toggles child sessions, re-querying the store since
hiding children is a SQL predicate, not an in-memory filter. The mutating/exec
commands do not. A rename runs as a Bubble Tea
command and, on success, reloads authoritative store state (list re-query with the
active filter reapplied, search rerun, viewer metadata refresh) rather than patching
the cached presentation.
**Raw archive view** (`internal/vault/tui/raw.go`, key `v` in list/view mode)
displays the archived main JSONL or the currently open Claude subagent sidecar.
A Codex child uses its own session blob; an inline tool detail uses its containing
session. It bypasses the platform decoders and Markdown renderer so unknown fields,
duplicate keys, and malformed records remain inspectable. `json.Indent` formats
each physical line independently; invalid lines retain their content with a
source-line diagnostic, and terminal controls/invalid UTF-8 display as escapes.
Formatting and viewport preparation run in a cancellable Bubble Tea command,
with a sequence guard rejecting stale results. The formatted content is kept only
while raw mode is open. The previous screen and child-session stack stay suspended;
return restores them, rewrapping the transcript only if the terminal changed size.
Horizontal scrolling makes long strings accessible without altering JSON escapes.
The view reads existing `GetSession`/sidecar bytes and performs no archive writes.

The viewer's markdown rendering upgrades from plain word-wrap to styled
[glamour](https://github.com/charmbracelet/glamour) output when built with the
optional `glamour` build tag (`make build-glamour` / `-tags fts5,glamour`) — a
`//go:build glamour` vs `!glamour` `renderBody` seam (`render_glamour.go` /
`render_default.go`), kept off the default build so it links no extra dependency. A
CI linkage guard asserts the default binary excludes glamour symbols and the tagged
binary includes them.

## Configuration

Three-level precedence (lowest to highest):
1. `~/.config/capy/config.toml` (global/XDG)
2. `.capy/config.toml` (project)
3. `.capy.toml` (project root)

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full config reference.

**DB path resolution** (`config.ResolveDBPath`): an absolute `store.path` is used
verbatim; a relative `store.path` is resolved against the project directory; an
empty `store.path` falls back to the XDG default
`~/.local/share/capy/<project-hash>/knowledge.db`. For a relative (project-scoped)
`store.path`, a session running inside a linked **git worktree** resolves the DB
against the repository's **main worktree** so all worktrees share one committed DB
(`config.MainWorktreeDir` / `config.DBProjectDir`; submodules excluded). See
[ADR-026](adr/026-worktree-shared-knowledge-db.md).

## CLI Commands

| Command | Description |
|---------|-------------|
| `capy` / `capy serve` | Start MCP server (stdio transport) |
| `capy setup` | Configure capy for current project (Claude Code or Codex) |
| `capy doctor` | Run diagnostics |
| `capy which` | Print knowledge base path |
| `capy cleanup` | Remove stale entries; `--vacuum` reclaims freelist pages, `--optimize` rebuilds FTS indexes + VACUUM to reclaim FTS tombstone bloat (ADR-029) |
| `capy checkpoint` | Flush WAL into main DB file |
| `capy encrypt` | Encrypt DB or rotate key |
| `capy dbsize` | Show DB disk usage |
| `capy hook <event>` | Handle hook event (called by AI tool) |
| `capy vault <cmd>` | Session vault (import, list, search, show, restore, resume, delete, stats, checkpoint) |

## Benchmarks

The benchmark suite validates capy's two core claims — context reduction effectiveness and retrieval quality — and tracks performance regressions.

### Tracks

| Track | What it measures | Tool | Output |
|-------|-----------------|------|--------|
| **Retrieval Quality** | R@K, NDCG, MRR, match-layer accuracy, rank ceiling | `testing.T` (quality) | JSON report |
| **Context Reduction (NIAH)** | Compression ratio, context recall, perfect recall, effective compression | `testing.T` (quality) | JSON report |
| **Performance** | Index throughput, search latency, executor overhead, 5000-byte threshold | `testing.B` (perf) | benchstat-compatible text |

### Fixture-Driven Design

Five content types (`markdown`, `json`, `plaintext`, `transcript`, `curated`) with JSONL fixtures in `internal/store/testdata/bench/`. Each fixture defines haystacks (content to index), queries, needles (information that must survive reduction), expected match layers, and rank ceilings.

### Running

```bash
make bench                                   # runs both perf and quality
make bench-perf                              # testing.B benchmarks → bench-results/{branch}.txt
make bench-quality                           # quality benchmarks → bench-results/{branch}.json
make bench-compare BASE=main TARGET=feature  # benchstat + qualstat side by side
```

Quality benchmarks skip under `go test ./...` (gated by `CAPY_BENCH_RESULTS` env var).

### qualstat

`cmd/qualstat/` — stdlib-only CLI for viewing and comparing quality reports. Mirrors `benchstat` UX: single-file mode for absolute metrics, two-file mode for delta comparison with regression markers and configurable warning thresholds.

### Further Reading

- [benchmark/RESULTS.md](../benchmark/RESULTS.md) — current numbers, methodology, known limitations
- [benchmark/COMPARISON.md](../benchmark/COMPARISON.md) — cross-tool comparison
- [benchmark/FIXTURES.md](../benchmark/FIXTURES.md) — fixture schema and authoring guide

## ADRs

All Architecture Decision Records are in [docs/adr/](docs/adr/). See the directory listing for the complete set.
