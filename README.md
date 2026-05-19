# capy

**C**ontext-**A**ware **P**rompting ...or "**Y**et another solution to LLM context problem"

[![GitHub stars](https://img.shields.io/github/stars/serpro69/capy?style=for-the-badge&color=yellow)](https://github.com/serpro69/capy/stargazers) [![GitHub forks](https://img.shields.io/github/forks/serpro69/capy?style=for-the-badge&color=blue)](https://github.com/serpro69/capy/network/members) [![Last commit](https://img.shields.io/github/last-commit/serpro69/capy?style=for-the-badge&color=green)](https://github.com/serpro69/capy/commits) [![License: ELv2](https://img.shields.io/badge/License-ELv2-blue.svg?style=for-the-badge)](LICENSE)

> [!IMPORTANT]
> This project was created with the help of Claude-Code. It is, however, reviewed, tested, and reworked with a human-in-the-loop.
>
> No AI slop here. Purely AI-made skills are hot garbage, and that's putting it mildly.
>
> That said, if you have any problems with code that is written by AI - you've been warned. But, then again, why would you be interested in AI-related configs and skills in the first place... `¯\_(ツ)_/¯`

## Privacy & Architecture

`capy` is not a CLI output filter or a cloud analytics dashboard. It operates at the MCP protocol layer — raw data stays in a sandboxed subprocess and never enters your context window. Web pages, API responses, file analysis, log files — everything is processed in complete isolation.

**Nothing leaves your machine.** No telemetry, no cloud sync, no usage tracking, no account required. Your code, your prompts, your session data — all local. The SQLite databases live in your home directory and are encrypted at rest. The encryption key (`CAPY_DB_KEY`) never leaves your environment.

This is a deliberate architectural choice, not a missing feature. Context optimization should happen at the source, not in a dashboard behind a per-seat subscription. Privacy-first is our philosophy — and every design decision follows from it. [License](#license)

## The Problem

Every MCP tool call dumps raw data into your context window. A single API response costs 56 KB. Twenty GitHub issues cost 59 KB. One access log — 45 KB. After 30 minutes, 40% of your context is gone. And when the agent compacts the conversation to free space, it forgets which files it was editing, what tasks are in progress, and what you last asked for.

`capy` is an MCP server and Claude Code plugin that solves both halves of this problem:

1. **Context Saving** — Sandbox tools keep raw data out of the context window. 315 KB becomes 5.4 KB. ~98% reduction.
2. **Searchable Knowledge Base** — All sandboxed output is indexed into SQLite FTS5 with BM25 ranking. Use `capy_search` to retrieve specific sections on demand. Multi-layer search: Porter stemming, trigram substring, fuzzy Levenshtein correction, with Reciprocal Rank Fusion and proximity reranking.
3. **Session Memory** — Past conversation transcripts are automatically indexed on server start. When the conversation compacts, the LLM can search prior sessions for context via BM25 search.

## Benchmarks

capy ships with a benchmark suite that validates its claims with deterministic, reproducible metrics — no LLM-in-the-loop evaluation. Run `make bench` to reproduce on your machine. Measured across 156 synthetic test cases spanning 5 content types (markdown, JSON, plaintext, transcripts, curated knowledge).

### Retrieval Quality

| Metric | Score |
|--------|-------|
| R@1 (at least one relevant result in top 1) | 0.897 |
| R@5 | 0.987 |
| R@10 | 0.994 |
| MRR (mean reciprocal rank) | 0.938 |
| NDCG@10 | 0.950 |

### Context Reduction

"Bytes saved" is a vanity metric. NIAH measures whether specific facts survive compression — not just how many bytes were removed:

| Metric | Score |
|--------|-------|
| Compression Ratio | 49.8% |
| Context Recall (fraction of specific facts preserved) | 0.983 |
| Perfect Recall Rate (cases with all facts preserved) | 97.1% |
| Effective Compression (compression x recall) | 49.7% |

On realistic content, capy achieves ~50% compression while preserving ~98% of the specific information needed. The "~98% reduction" claim in the problem statement above applies to raw byte savings on large uniform outputs — the NIAH numbers are the honest picture for information preservation on diverse content.

Full results with per-content-type breakdowns, methodology, and known limitations: [`benchmark/RESULTS.md`](benchmark/RESULTS.md)
Cross-tool comparison: [`benchmark/COMPARISON.md`](benchmark/COMPARISON.md)
Fixture authoring guide: [`benchmark/FIXTURES.md`](benchmark/FIXTURES.md)

## Quick Start

### Install

**Homebrew** (macOS/Linux):

```bash
brew install serpro69/tap/capy
```

**Shell script** (any Unix):

```bash
curl -sSfL https://raw.githubusercontent.com/serpro69/capy/master/install.sh | sh
```

**Build from source** (requires Go 1.25+ and a C compiler):

```bash
git clone https://github.com/serpro69/capy.git
cd capy
make build
mv capy /usr/local/bin/   # or anywhere on your PATH
```

### Setup

**1. Set an encryption key** (required — capy refuses to start without it):

```bash
export CAPY_DB_KEY=$(openssl rand -base64 48)
echo "export CAPY_DB_KEY='$CAPY_DB_KEY'" >> ~/.zshrc  # or ~/.bashrc
```

**2. Configure your project** (Claude Code):

```bash
capy setup
capy doctor   # verify everything is green
```

**3. Use normally.** Start using Claude Code — capy works automatically:

- **Bash commands** producing large output are nudged toward the sandbox
- **curl/wget** calls are intercepted and redirected to `capy_fetch_and_index`
- **WebFetch** is blocked in favor of `capy_fetch_and_index`
- **Read** for analysis (not editing) is nudged toward `capy_execute_file`
- **Subagents** get routing instructions injected automatically
- **Past sessions** are indexed on server start for cross-session search

You don't need to call capy tools yourself. The LLM learns the routing from the hooks and CLAUDE.md instructions that `capy setup` installed. But you can ask it directly: "use capy_batch_execute to research X" if you want to be explicit.

### Setup (Codex CLI)

```bash
capy setup --platform codex
capy doctor
```

## How It Works — By Example

### Before capy

```
You: "Check what's failing in the test suite"
Claude: *runs `npm test` via Bash*
→ 89 KB of test output floods context
→ Context is 40% full after one command
```

### After capy

```
You: "Check what's failing in the test suite"
Claude: *runs capy_batch_execute with commands=["npm test"] and queries=["failing tests", "error messages"]*
→ 89 KB stays in sandbox, indexed into knowledge base
→ Only 2.1 KB of matched sections enter context
→ Claude sees: "3 sections matched 'failing tests' (1,847 lines, 89.2KB)"
```

### The `intent` parameter

When `capy_execute` or `capy_execute_file` is called with an `intent` parameter and the output exceeds 5 KB, capy automatically:

1. Indexes the full output into the knowledge base
2. Searches for sections matching the intent
3. Returns section titles + previews instead of the full output

```
capy_execute(language: "shell", code: "git log --oneline -100", intent: "recent authentication changes")
→ Full git log stays in sandbox
→ Returns: "4 sections matched 'recent authentication changes'"
→ Use capy_search to drill into specific sections
```

## Configuration

capy uses TOML configuration with three-level precedence (lowest to highest):

1. `~/.config/capy/config.toml` (global)
2. `.capy/config.toml` (project)
3. `.capy.toml` (project root)

```toml
[store]
# path = ".capy/knowledge.db"  # optional override; default: ~/.local/share/capy/<project-hash>/knowledge.db
# title_weight = 2.0           # BM25 title column weight
# max_source_bytes = 2097152   # 2 MB hard cap on total content per source

[store.cleanup]
cold_threshold_days = 30
ephemeral_ttl_hours = 24    # lifetime for ephemeral sources (minimum 1)
session_ttl_days = 60       # lifetime for session transcript sources (minimum 1)
auto_prune = false

[store.cache]
fetch_ttl_hours = 24        # skip re-fetch within this window

[executor]
timeout = 30               # seconds
max_output_bytes = 102400  # 100 KB

[server]
log_level = "info"
```

All settings have sensible defaults. Configuration files are optional — capy works out of the box.

> **Caution with git and the knowledge DB.** SQLite WAL sidecar files (`.db-wal`, `.db-shm`) are created when the DB is written to during a session. capy flushes the WAL on session close automatically, but the files only get cleaned up if the session actually wrote to the DB. If you see stale WAL files (e.g., after upgrading capy or after an unclean shutdown), run `capy checkpoint` to flush them manually. If you want to commit the DB to git (e.g., to share across machines), run `capy checkpoint` first — it flushes the WAL into the main file and removes the sidecar files.

## Encryption

The knowledge database is encrypted at rest using sqlite3mc (SQLCipher v4 compatible). capy refuses to start without a passphrase.

### Setup

Set `CAPY_DB_KEY` in your shell profile:

```bash
# Generate a strong passphrase (32+ characters recommended)
export CAPY_DB_KEY=$(openssl rand -base64 48)
echo "export CAPY_DB_KEY='$CAPY_DB_KEY'" >> ~/.zshrc  # or ~/.bashrc
```

Or use [direnv](https://direnv.net/) for per-project keys:

```bash
echo "export CAPY_DB_KEY='your-passphrase-here'" >> .envrc
direnv allow
```

### Initial encryption

Existing unencrypted databases must be encrypted before capy will use them:

```bash
export CAPY_DB_KEY='your-passphrase-here'
capy encrypt
# When prompted for the current passphrase, press Enter (empty = unencrypted).
```

The original database is preserved as `<path>.bak`.

### Cross-machine sync

By default, the knowledge DB lives under `~/.local/share/capy/` (XDG data), which is outside git. To enable cross-machine sync, configure a project-local path first:

```toml
# .capy.toml (or .capy/config.toml)
[store]
path = ".capy/knowledge.db"
```

Then encrypt, checkpoint, and commit:

```bash
capy encrypt       # encrypt if not already done
capy checkpoint    # flush WAL into main file
git add .capy/knowledge.db
git commit -m "Update knowledge base"
git push
```

On the other machine (same `store.path` config must be present):

```bash
git pull
export CAPY_DB_KEY='same-passphrase'
capy serve         # DB opens with your key
```

The pre-commit hook rejects unencrypted databases automatically — run `capy encrypt` first if the commit is blocked.

### Key rotation

```bash
export CAPY_DB_KEY='new-passphrase'
capy encrypt
# Enter the OLD passphrase when prompted.
```

### Passphrase recommendations

- **32+ characters.** Shorter passphrases work but trigger a warning.
- **Generated, not memorized.** `openssl rand -base64 48` or a password manager.
- **Never in config files.** Use environment variables, direnv, or a secrets manager.

## CLI Commands

| Command | Description |
|---------|-------------|
| `capy` or `capy serve` | Start the MCP server (stdio transport) |
| `capy setup` | Configure capy for the current project (`--platform codex` for Codex CLI) |
| `capy doctor` | Run diagnostics on the installation |
| `capy which` | Print the knowledge base path for the current project |
| `capy cleanup` | Remove stale knowledge base entries |
| `capy sweep` | Index past sessions (dry-run by default, `--force` to index, `--reindex` to re-parse all) |
| `capy checkpoint` | Flush WAL into main DB file for safe git commits |
| `capy encrypt` | Encrypt the knowledge DB or rotate its encryption key |
| `capy dbsize` | Show knowledge DB disk usage |
| `capy hook <event>` | Handle a hook event (called by the AI tool, not you) |

Global flags: `--project-dir`, `--version`

Cleanup flags: `--max-age-days` (default 30), `--dry-run` (default true), `--force`

### Shell Completions

Homebrew installs completions automatically. For other installation methods:

```bash
# Bash (add to ~/.bashrc)
source <(capy completion bash)

# Zsh (add to ~/.zshrc)
source <(capy completion zsh)

# Fish
capy completion fish | source
# To persist: capy completion fish > ~/.config/fish/completions/capy.fish
```

## MCP Tools

### Execution

| Tool | What It Does |
|------|-------------|
| `capy_execute` | Run code in a sandboxed subprocess. Supports 11 languages: JavaScript, TypeScript, Python, Shell, Ruby, Go, Rust, PHP, Perl, R, Elixir. Only stdout enters context. Pass `intent` to auto-index large output. |
| `capy_execute_file` | Inject a file into a sandbox variable (`FILE_CONTENT`) and process it with code you write. The raw file never enters context — only your printed summary does. |
| `capy_batch_execute` | The primary research tool. Runs multiple shell commands, auto-indexes all output as markdown, and searches with multiple queries — all in ONE call. |

### Knowledge

| Tool | What It Does |
|------|-------------|
| `capy_index` | Index text, markdown, or a file path into the FTS5 knowledge base for later search. Stored as `durable` (persists across sessions). |
| `capy_search` | Search indexed content. Multi-layer search: Porter stemming + trigram substring + fuzzy Levenshtein, fused with Reciprocal Rank Fusion. Defaults to durable + session sources; pass `include_kinds` to search ephemeral content. |
| `capy_fetch_and_index` | Fetch a URL, convert HTML to markdown, index into the knowledge base, return a ~3 KB preview. Default ephemeral (24h TTL). Git platform issue/PR URLs are blocked with CLI redirect guidance. |

### Utility

| Tool | What It Does |
|------|-------------|
| `capy_stats` | Session report: bytes saved, context reduction ratio, per-tool breakdown, knowledge base tier distribution. |
| `capy_doctor` | Diagnostics: version, available runtimes, FTS5 status, config, knowledge base status, hook registration, MCP registration, security policies. |
| `capy_cleanup` | Remove evictable knowledge base entries via four paths: oversized source eviction, retention-score eviction (durable), TTL eviction (ephemeral), TTL eviction (session). Pass `purge_ephemeral=true` for a one-shot scratch clear. |

## Security

capy enforces the same permission rules you already use — but extends them to the MCP sandbox. If you block `sudo` in Claude Code settings, it's also blocked inside `capy_execute`, `capy_execute_file`, and `capy_batch_execute`.

**Zero setup required.** If you haven't configured any permissions, nothing changes.

```json
{
  "permissions": {
    "deny": ["Bash(sudo *)", "Bash(rm -rf /*)", "Read(.env)", "Read(**/.env*)"],
    "allow": ["Bash(git:*)", "Bash(npm:*)"]
  }
}
```

Add to `.claude/settings.json` (project) or `~/.claude/settings.json` (global). Pattern: `Tool(glob)` where `*` = anything. Colon syntax (`git:*`) matches the command with or without arguments.

Chained commands (`&&`, `;`, `|`) are split and checked individually. **deny always wins over allow.**

### Sandbox protections

- **Process group isolation** — child processes can't escape cleanup
- **Environment sanitization** — ~50 dangerous env vars stripped (LD_PRELOAD, NODE_OPTIONS, PYTHONSTARTUP, etc.)
- **Output hard cap** — processes killed if stdout+stderr exceeds 100 MB
- **Timeout enforcement** — configurable per-call, default 30s
- **Shell-escape detection** — non-shell languages scanned for embedded shell commands
- **SSRF protection** — `capy_fetch_and_index` blocks requests to localhost, private networks, and cloud metadata endpoints
- **Secret sanitization** — all indexed content is scanned and redacted for API keys, tokens, JWTs, and other credential patterns

## Hook System

capy uses Claude Code's hook system to intercept tool calls before they execute. After `capy setup`, this works automatically — you don't need to configure anything.

### What gets intercepted

| Pattern | What happens |
|---------|-------------|
| `curl`/`wget` in Bash | Command replaced with message directing to `capy_fetch_and_index` (file-output flags like `-o` are allowed through) |
| `fetch()`, `requests.get()`, `http.get()` in Bash | Command replaced with message directing to `capy_execute` |
| `WebFetch` tool | Denied — use `capy_fetch_and_index` instead (git platform URLs get CLI-specific redirect guidance) |
| `Read` tool | One-time advisory: prefer `capy_execute_file` for analysis |
| `Grep` tool | One-time advisory: prefer `capy_execute` for large searches |
| `Agent`/`Task` tools | Routing block injected into subagent prompt; Bash subagents upgraded to general-purpose |
| `capy_fetch_and_index` | Git platform issue/PR/MR URLs blocked with platform CLI redirect; gist URLs get soft guidance |
| `capy_*` tools (shell) | Security policy enforcement on shell code and batch commands |

### Platform support

`capy setup` generates configuration for Claude Code (default) and Codex CLI (`--platform codex`). Automated setup for other platforms is planned.

Hooks already recognize tool name aliases for these platforms, so the routing logic works once you wire up the MCP server and hook commands manually:

| Platform | Recognized tool aliases |
|----------|------------------------|
| Gemini CLI | `run_shell_command`, `read_file`, `read_many_files`, `grep_search`, `search_file_content`, `web_fetch` |
| OpenCode | `bash`, `view`, `grep`, `fetch`, `agent` |
| Codex CLI | `shell`, `shell_command`, `exec_command`, `container.exec`, `local_shell`, `grep_files` |
| Cursor | `mcp_web_fetch`, `mcp_fetch_tool`, `Shell` |
| VS Code Copilot | `run_in_terminal` |
| Kiro CLI | `fs_read`, `fs_write`, `execute_bash` |

Manual setup: register `capy serve` as an MCP server (stdio transport) and `capy hook <event>` as the hook command in your platform's configuration.

## Troubleshooting

Run `capy doctor` to diagnose issues. Common problems:

| Check | Fix |
|-------|-----|
| **FTS5: unavailable** | The binary wasn't built with `-tags fts5`. Rebuild with `make build`. |
| **Runtimes: 0/11** | No language runtimes found in PATH. Install at least `bash` and `python3`. |
| **Hooks: not registered** | Run `capy setup` in your project directory. |
| **MCP: not registered** | Run `capy setup`. Check `.mcp.json` exists in project root. |
| **MCP: binary not found** | The `capy` binary isn't in PATH. Move it or run `capy setup --binary /path/to/capy`. |
| **CAPY_DB_KEY not set** | Set `CAPY_DB_KEY` in your shell profile (see [Encryption](#encryption)). |

## Acknowledgements

capy is a Go reimplementation of [context-mode](https://github.com/mksglu/context-mode), originally written in TypeScript by [@mksglu](https://github.com/mksglu). The core algorithms — FTS5 search with BM25 ranking, three-tier fallback, smart chunking, sandbox architecture — originate from that project.

**Why rewrite it?** context-mode works well, BUT! I wanted to experiment with ideas that are hard to retrofit into the existing context-mode architecture — persistent cross-session knowledge bases, tiered freshness metadata, content deduplication, mandatory encryption, and session transcript indexing. Go gives me a single static binary with no Node.js dependency, which removes an entire class of installation and compatibility issues. I also have a very acute allergy to anything in the JS/TS/Node ecosystem. This is primarily a personal tool, but it's open source in case others find it useful.

### capy vs context-mode

|  | context-mode (TypeScript) | capy (Go) |
|--|--------------------------|-----------|
| **Install** | `npm install` — requires Node.js, native module compilation | Single static binary — `brew install` or `curl \| sh` |
| **Startup** | Node.js VM boot + module resolution | Native binary, starts in milliseconds |
| **Memory** | Node.js baseline (~50-80 MB typical) | Go baseline (~10-20 MB typical) |
| **SQLite** | `better-sqlite3` native addon with Bun fallback | `mattn/go-sqlite3` via CGO — one driver |
| **Encryption** | Not supported | Mandatory at rest (sqlite3mc, SQLCipher v4) |
| **Knowledge base** | Originally ephemeral; persistence added later | Persistent per-project from day one |
| **Content dedup** | Re-indexes on every call | SHA-256 content hashing — skips unchanged |
| **Freshness** | Added via TTL cache | Tiered retention (hot/warm/cold) + TTL-based lifecycle by source kind |
| **Source kinds** | Single type | Three kinds: durable, ephemeral, session — distinct lifecycle and search visibility |
| **Session indexing** | Tracks events across compactions | Indexes past JSONL transcripts into searchable knowledge base |
| **Process isolation** | `child_process.execFileSync` | Process group isolation (`Setpgid`) — kills entire tree |
| **Secret sanitization** | Not supported | Regex-based redaction before indexing |
| **Configuration** | Reads `.claude/settings.json` | Own config system (TOML, XDG dirs) plus reads `.claude/settings.json` for security |
| **Platform support** | Claude Code, Cursor, Kiro, Zed, Pi, OpenClaw, OpenCode, Gemini CLI, Codex CLI | Claude Code, Codex CLI (more planned) |

### What's shared

The search algorithm (FTS5 BM25 with Porter stemming, trigram, and fuzzy Levenshtein correction), sandbox execution model, hook-based routing, chunking strategies, and security policy evaluation all originate from context-mode. capy ports these faithfully, diverging only where Go offers a meaningfully better approach.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, architecture overview, and code conventions.

```bash
git clone https://github.com/serpro69/capy.git
cd capy
export CAPY_DB_KEY=test-key-for-development
make build && make test
```

## License

Licensed under [Elastic License 2.0](LICENSE) (source-available). You can use it, fork it, modify it, and distribute it. Two things you can't do: offer it as a hosted/managed service, or remove the licensing notices.
