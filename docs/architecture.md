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

## Configuration

Three-level precedence (lowest to highest):
1. `~/.config/capy/config.toml` (global/XDG)
2. `.capy/config.toml` (project)
3. `.capy.toml` (project root)

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full config reference.

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
make bench           # runs both perf and quality
make bench-perf      # testing.B benchmarks → bench-results/{branch}.txt
make bench-quality   # quality benchmarks → bench-results/{branch}.json
make compare BASE=main TARGET=feature  # benchstat + qualstat side by side
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
