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
│  │  9 Tool      │  │  Stats       │  │  Search      │  │  Lifecycle │  │
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
│  Session Sweep  (internal/session)                                      │
│  Background goroutine on server start + CLI `capy sweep`                │
│                                                                         │
│  Parses Claude Code JSONL files → builds transcripts → chunks →         │
│  indexes as `session` kind sources in the knowledge base                │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│  Vault  (internal/vault)                                                │
│  Background goroutine on server start + CLI `capy vault`                │
│                                                                         │
│  Discovers session JSONL files → scans for FTS text → archives          │
│  verbatim in encrypted vault.db (separate from knowledge store)         │
│  Provides cross-project search, restore, resume, and TUI browsing       │
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

### Search (capy_search)

1. Query sanitized for FTS5 (strip special chars, expand synonyms)
2. Two-layer RRF (Reciprocal Rank Fusion):
   - Porter stemming FTS5 search (AND, then fallback to OR)
   - Trigram substring FTS5 search (AND, then fallback to OR)
3. Fuzzy correction pass if results < limit (Levenshtein against vocabulary table)
4. Post-processing: per-source diversification, title-match boost, proximity reranking, entity boosting
5. Access tracking (last_accessed_at, access_count) for retention scoring

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
| `session` | Strict TTL (default 60 days) | Included | `capy sweep` (indexes past conversation transcripts) |

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

The session subsystem parses Claude Code's JSONL conversation files and indexes them as searchable transcripts.

### Parse Pipeline

1. **JSONL parsing** — read session file, merge progressive assistant snapshots by message.id
2. **Noise filtering** — strip `<system-reminder>` tags, local command output tags
3. **Tool extraction** — registry-driven: ActionPromote (tool text becomes assistant text), ActionEnrich (metadata lines), ActionSkip
4. **Sub-agent discovery** — parse `subagents/` directory alongside main session file
5. **Turn pair building** — group user→assistant messages, emit away summaries as standalone entries
6. **Secret sanitization** — strip secrets from all text before transcript building
7. **Transcript building** — Human:/Assistant: format with metadata lines and subagent delimiters
8. **Chunking** — split by turn pair boundaries using byte offsets

### Indexability Threshold

Sessions require ≥2 non-subagent turn pairs and ≥200 chars of assistant text.

### Mtime-based Skip

Compares max(file.mtime, subagents_dir.mtime) against stored indexed_at to avoid re-indexing unchanged sessions.

## Vault

The vault (`internal/vault/`) provides verbatim, cross-project session archival with full-text search. It operates a separate encrypted SQLite database (`vault.db`) independent of the per-project FTS knowledge store.

### Architecture

Four layers:

1. **Storage** (`store.go`) — SQLite connection lifecycle, schema, encryption via `CAPY_VAULT_KEY`, CRUD operations. Shares the open/recovery path with the knowledge store via `internal/sqliteutil/`
2. **Scanner** (`scanner.go`) — single-pass JSONL reader that extracts searchable text, metadata (title, timestamps, branch), and tool summaries. Sanitizes output via `sanitize.StripSecrets()` before FTS insertion
3. **Discovery** (`discovery.go`) — walks Claude Code session directories to find all JSONL files and associated sidecars (subagents, tool-results) via `config.ClaudeProjectsDir()`
4. **CLI/TUI** (`cmd/capy/vault.go` + `internal/vault/tui/`) — cobra subcommand group with interactive bubbletea interface

### Schema

```sql
vault_sessions   — one row per session: metadata, 1:1 location, raw JSONL blob,
                   encoding ('raw'|'zstd', NULL on legacy rows = raw)
vault_files      — associated files (subagents, tool-results), CASCADE on session
                   delete; same encoding column
vault_fts        — FTS5 virtual table, one row per message, Porter tokenizer
                   (tool_result rows tagged with their call summary; Read/NotebookRead
                    and Edit/Write result bodies excluded — see scanner.go ftsExcludedResult)
vault_meta       — key-value store; holds min_reader_version (the compression
                   forward-compat marker — see below)
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

The first `'zstd'` write stamps `vault_meta.min_reader_version = "2"`. `openDB`
reads it after migration and refuses to open a vault whose marker exceeds the
binary's `supportedReaderVersion` (2) — an old binary will not silently misread a
compressed vault. `capy vault compact` rewrites legacy (`NULL`) rows through the
codec and `VACUUM`s to reclaim the freed pages (SQLite never shrinks the file on
its own). Full rationale: the vault v2 design doc and the `kk:arch-decisions`
note (the explicit `encoding` column supersedes an earlier magic-byte design).

### Search index & `index_version`

`vault_fts` holds one row per message — `user` (human turns), `assistant`, `tool`
(tool_result output), and `system`. Tool-result rows are tagged with their
originating call summary (e.g. `Bash <cmd>`), correlated by `tool_use_id`. Result
bodies for tools matched by `scanner.go`'s `ftsExcludedResult` are **excluded from
the index**. That predicate is the union of two sets: `excludedResultTools` (`Read`,
`NotebookRead` — file/cell-content dumps) and `diffResultTools` (`Edit`, `Write` —
whose body is just a one-line "updated successfully" string; the real change is the
diff in the sibling `toolUseResult.structuredPatch`, never the indexable
`message.content`). Both are noise for conversation search. The call itself stays
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

Stale sessions are upgraded two ways, both rewriting **only** the FTS rows (never
the `raw_jsonl` blob): `capy vault reindex` rebuilds every session below
`currentIndexVersion` from the stored blob (so it reaches sessions already deleted
from disk), and a normal `capy vault import` opportunistically rebuilds an on-disk
session whose content is unchanged but whose index is stale. `capy vault stats`
prints the current version and how many sessions are still below it (the reindex
backlog).

Full rationale and the rejected alternatives: [ADR-025](adr/025-vault-index-version-and-reindex.md).

### Tool-result display (`show` vs `--tui`)

`raw_jsonl` is always stored verbatim, so `vault show --format json` and `restore`
are byte-faithful. Three **display/index surfaces** decide how a `tool_result` body
is *presented*. Two tool sets steer them (the readers live in different files, so the
coupling is non-obvious):

| Surface | Reader | Dump tool (`excludedResultTools`: Read/NotebookRead) | Diff tool (`diffResultTools`: Edit/Write) | Large other (e.g. a big `Bash` log) |
|---------|--------|------------------------------------------------------|-------------------------------------------|-------------------------------------|
| FTS index | `scanner.go` `extractUserBlocks` (via `ftsExcludedResult`) | body excluded | body excluded | indexed (head/tail truncated) |
| `vault show` (pager) | `render.go` `renderUserContent` | one-line "output omitted" marker | **unchanged — verbatim success body** | full inline |
| `vault show --tui` (viewer) | `transcript.go` `splitUserContentForViewer` | collapse-then-open marker (raw body) | collapse-then-open marker (**reconstructed diff**) | collapse-then-open marker if over threshold |

`excludedResultTools` (Read/NotebookRead) drives all three surfaces identically —
their body is a file/cell dump, so it is dropped from search, collapsed to a
one-liner in `show`, and a collapsible marker in the TUI. `diffResultTools`
(Edit/Write) shares only the **FTS** column (both sets feed `ftsExcludedResult`);
its display is **deliberately decoupled** (vault v2 § Addenda A3): plain `show` keeps
the verbatim one-line success body (collapsing it would hide nothing useful), while
the TUI collapses to a marker that expands to a **colored unified diff**.

The diff isn't in `message.content` (which is the success string) — it lives in the
sibling top-level JSONL field `toolUseResult.structuredPatch`, threaded through
`jsonlLine.ToolUseResult` → `transcriptEntry` → `splitUserContentForViewer`.
`diff.go`'s `diffBodyFromToolResult` reconstructs unified-diff *text* (kept in
package `vault`); the `tui` package owns the *color* (`render.go` `renderDiffBody`:
`+` green / `-` red / `@@` cyan), which bypasses the build-tagged `renderBody`/glamour
seam so a diff is never markdown-mangled. The marker shows a `(+a −b)` stat instead
of a line count. A diff tool with no `structuredPatch` falls back to the plain
success body. To extend the diff-view to another tool (e.g. `MultiEdit`), add it to
`diffResultTools`. The FTS exclusion is versioned (above); the two display paths are
not.

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
routes it through `renderDiffBody` instead of the plain word-wrap. To add a new
openable kind, touch `renderTranscript`'s marker branch + `markerRowFor` (`render.go`)
and `openFocusedMarker`'s role switch (`viewer.go`). `splitUserContentForViewer` is
TUI-only and forked from `renderUserContent` precisely so a viewer-only change
cannot perturb the static `show` output.

### Archival Paths

1. **MCP server startup** — background goroutine imports current project's sessions (opt-in via `CAPY_VAULT_KEY`). With `CAPY_VAULT_SWEEP_ALL` set, the sweep walks **all** projects under `config.ClaudeProjectsDir()` instead of just the current one (`server.go:vaultSweep`)
2. **`capy vault import`** — manual, all projects, idempotent (hash-based, larger-total-size wins)
3. **`capy vault merge --from <path>`** — non-destructive cross-machine union (`merge.go`): reads another vault's `vault_sessions`+`vault_files`, applies the same idempotent digest decision (distinct added, larger-wins on UUID overlap), carries source metadata verbatim, re-scans FTS. Feature-detects a v1 (no `encoding` column) source. Writes only the destination, so a concurrent server sweep is absorbed by busy-timeout retry

### CLI Commands

| Command | Description |
|---------|-------------|
| `capy vault import` | Scan and archive sessions (mutating; `--dry-run` to preview) |
| `capy vault reindex` | Rebuild the FTS index for sessions on an older `index_version` (DB-driven; no disk dependency; batched + WAL-checkpointed) |
| `capy vault list` | List sessions, reverse chronological |
| `capy vault search` | Full-text search with snippets |
| `capy vault show` | Display full session (pager, `--format` for export) |
| `capy vault restore` | Write JSONL + session files back to disk |
| `capy vault resume` | Restore + launch `claude --resume` |
| `capy vault delete` | Remove a session from the vault |
| `capy vault stats` | DB size, session count, per-project breakdown, index version + reindex backlog |
| `capy vault checkpoint` | Flush WAL (required before cross-machine copy) |
| `capy vault compact` | Recompress legacy (`encoding IS NULL`) blobs through the zstd codec + `VACUUM` to reclaim disk. No-op if nothing is uncompressed; aborts under `CAPY_VAULT_NO_COMPRESS` or a busy DB (stop the server first) |
| `capy vault merge --from <path>` | Non-destructive cross-machine union (see Archival Paths). Source key via `--key`/`CAPY_VAULT_MERGE_KEY`/`CAPY_VAULT_KEY`; `--project`, `--dry-run` |
| `capy vault rekey` | Rotate the encryption key to the current `CAPY_VAULT_KEY` via `sqliteutil.Rekey` (SQLite backup-API: open old → checkpoint → copy into a new file under the new key → swap+verify). Sidesteps the WAL/PRAGMA-rekey incompatibility (ADR-020) by writing a fresh file. Stop the server first; `--remove-backup` unlinks the old-key `.bak` |

`list`, `search`, and `show` support `--tui` for interactive browsing/search/viewing
(filter `f`, copy `c`, restore `r`, resume `R`); the mutating/exec commands do not.
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
| `capy cleanup` | Remove stale entries |
| `capy sweep` | Index past sessions (dry-run default) |
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
