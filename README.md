# capy

**C**ontext-**A**ware **P**rompting ...or "**Y**et another solution to LLM context problem"

[![GitHub stars](https://img.shields.io/github/stars/serpro69/capy?style=for-the-badge&color=yellow)](https://github.com/serpro69/capy/stargazers) [![GitHub forks](https://img.shields.io/github/forks/serpro69/capy?style=for-the-badge&color=blue)](https://github.com/serpro69/capy/network/members) [![Last commit](https://img.shields.io/github/last-commit/serpro69/capy?style=for-the-badge&color=green)](https://github.com/serpro69/capy/commits) [![License: ELv2](https://img.shields.io/badge/License-ELv2-blue.svg?style=for-the-badge)](LICENSE)

## Privacy & Architecture

`capy` is not a CLI output filter or a cloud analytics dashboard. It operates at the MCP protocol layer - raw data stays in a sandboxed subprocess and never enters your context window. Web pages, API responses, file analysis, log files - everything is processed in complete isolation.

**Nothing leaves your machine.** No telemetry, no cloud sync, no usage tracking, no account required. Your code, your prompts, your session data - all local. The SQLite databases live in your home directory and die when you're done.

This is a deliberate architectural choice, not a missing feature. Context optimization should happen at the source, not in a dashboard behind a per-seat subscription. Privacy-first is our philosophy - and every design decision follows from it. [License](#license)

## The Problem

Every MCP tool call dumps raw data into your context window. A single API response costs 56 KB. Twenty GitHub issues cost 59 KB. One access log - 45 KB. After 30 minutes, 40% of your context is gone. And when the agent compacts the conversation to free space, it forgets which files it was editing, what tasks are in progress, and what you last asked for.

`capy` is an MCP server and Claude Code plugin that solves this:

1. **Context Saving** - Sandbox tools keep raw data out of the context window. 315 KB becomes 5.4 KB. ~98% reduction.
2. **Searchable Knowledge Base** - All sandboxed output is indexed into SQLite FTS5 with BM25 ranking. Use `capy_search` to retrieve specific sections on demand. Three-tier fallback: Porter stemming, trigram substring, fuzzy Levenshtein correction.

## Installation

### Build from Source

Requires Go 1.23+ and a C compiler (CGO is needed for SQLite FTS5).

```bash
git clone https://github.com/serpro69/capy.git
cd capy
make build
```

This produces a `capy` binary in the project root. Move it to your PATH:

```bash
mv capy /usr/local/bin/
```

### Setup

Run the setup command in your project directory:

```bash
capy setup
```

This does four things:
1. Registers capy hooks in `.claude/settings.json`
2. Registers the MCP server in `.mcp.json`
3. Appends routing instructions to `CLAUDE.md`
4. Adds `.capy/` to `.gitignore`

Verify the installation:

```bash
capy doctor
```

## Configuration

capy uses TOML configuration with three-level precedence (lowest to highest):

1. `~/.config/capy/config.toml` (global)
2. `.capy/config.toml` (project)
3. `.capy.toml` (project root)

```toml
[store]
path = ".capy/knowledge.db"  # default: XDG data dir

[store.cleanup]
cold_threshold_days = 30
auto_prune = false

[executor]
timeout = 30             # seconds
max_output_bytes = 102400  # 100 KB

[server]
log_level = "info"
```

All settings have sensible defaults. Configuration files are optional.

## CLI Commands

| Command | Description |
|---------|-------------|
| `capy serve` | Start the MCP server (default when run with no args) |
| `capy setup` | Configure capy for the current project |
| `capy doctor` | Run diagnostics on the installation |
| `capy cleanup` | Remove stale knowledge base entries |
| `capy hook <event>` | Handle a Claude Code hook event (internal) |

### Flags

```
--project-dir    Override project directory detection
--version        Print version and exit
```

`cleanup` flags:
```
--max-age-days   Maximum age for cold sources (default: 30)
--dry-run        Preview what would be removed (default: true)
--force          Actually remove stale data
```

## MCP Tools

capy provides 9 MCP tools. The hook system automatically routes data-heavy operations to the sandbox.

### Execution Tools

| Tool | Purpose |
|------|---------|
| `capy_execute` | Run code in a sandboxed subprocess. 11 languages: JavaScript, TypeScript, Python, Shell, Ruby, Go, Rust, PHP, Perl, R, Elixir. Only stdout enters context. |
| `capy_execute_file` | Read a file into a sandbox variable (`FILE_CONTENT`) and process it. The raw file content never enters context. |
| `capy_batch_execute` | Run multiple shell commands, auto-index all output, and search with multiple queries in ONE call. The primary tool for research. |

### Knowledge Tools

| Tool | Purpose |
|------|---------|
| `capy_index` | Index text/markdown/file content into the FTS5 knowledge base for later search. |
| `capy_search` | Search indexed content with BM25 ranking. 8-layer fallback: Porter+AND, Porter+OR, Trigram+AND, Trigram+OR, then fuzzy-corrected versions of all four. |
| `capy_fetch_and_index` | Fetch a URL, convert HTML to markdown (stripping nav/script/style), index into the knowledge base, return a preview. |

### Utility Tools

| Tool | Purpose |
|------|---------|
| `capy_stats` | Session statistics: bytes saved, context reduction ratio, per-tool breakdown, knowledge base stats. |
| `capy_doctor` | Diagnostics: version, runtimes, FTS5, config, hooks, MCP registration, security policies. |
| `capy_cleanup` | Remove cold knowledge base entries that haven't been accessed. |

## Security

capy enforces the same permission rules you already use - but extends them to the MCP sandbox. If you block `sudo`, it's also blocked inside `capy_execute`, `capy_execute_file`, and `capy_batch_execute`.

**Zero setup required.** If you haven't configured any permissions, nothing changes. This only activates when you add rules.

```json
{
  "permissions": {
    "deny": ["Bash(sudo *)", "Bash(rm -rf /*)", "Read(.env)", "Read(**/.env*)"],
    "allow": ["Bash(git:*)", "Bash(npm:*)"]
  }
}
```

Add this to your project's `.claude/settings.json` (or `~/.claude/settings.json` for global rules).

The pattern is `Tool(what to match)` where `*` means "anything". Colon syntax (`git:*`) matches the command with or without arguments.

Commands chained with `&&`, `;`, or `|` are split - each part is checked separately. `echo hello && sudo rm -rf /tmp` is blocked because the `sudo` part matches the deny rule.

**deny** always wins over **allow**. More specific (project-level) rules override global ones.

### Executor Sandbox

The executor provides additional security layers:
- **Process group isolation** (`Setpgid`) - child processes can't escape cleanup
- **Environment sanitization** - ~50 dangerous env vars stripped (LD_PRELOAD, NODE_OPTIONS, PYTHONSTARTUP, etc.)
- **Output hard cap** - processes killed if stdout+stderr exceeds 100 MB
- **Timeout enforcement** - configurable per-call, default 30s
- **Shell-escape detection** - non-shell languages are scanned for embedded shell commands (Python `subprocess`, Go `exec.Command`, etc.)
- **SSRF protection** - `capy_fetch_and_index` blocks requests to localhost, private networks, and cloud metadata endpoints

## Hook System

capy uses Claude Code's hook system to intercept tool calls before they execute. The hooks route data-heavy operations to the sandbox automatically.

### What Gets Intercepted

| Tool | Action |
|------|--------|
| `curl`/`wget` in Bash | Replaced with echo message directing to `capy_fetch_and_index` |
| Inline HTTP (`fetch()`, `requests.get()`) in Bash | Replaced with echo message directing to `capy_execute` |
| Build tools (`gradle`, `maven`) in Bash | Redirected to `capy_execute` sandbox |
| `WebFetch` | Denied - use `capy_fetch_and_index` instead |
| `Read` | Advisory shown once: use `capy_execute_file` for analysis |
| `Grep` | Advisory shown once: use `capy_execute` for large searches |
| `Agent`/`Task` | Routing instructions injected into subagent prompt; Bash subagents upgraded to general-purpose |

### Supported Platforms

Hooks support tool name aliases for multiple platforms:

- **Claude Code** (native)
- **Gemini CLI** (`run_shell_command`, `read_file`, `grep_search`, `web_fetch`)
- **OpenCode** (`bash`, `view`, `grep`, `fetch`, `agent`)
- **Codex CLI** (`shell`, `shell_command`, `exec_command`, `container.exec`, `grep_files`)
- **Cursor** (`mcp_web_fetch`, `Shell`)
- **VS Code Copilot** (`run_in_terminal`)
- **Kiro CLI** (`fs_read`, `execute_bash`)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow.

```bash
git clone https://github.com/serpro69/capy.git
cd capy
make build && make test
```

## License

Licensed under [Elastic License 2.0](LICENSE) (source-available). You can use it, fork it, modify it, and distribute it. Two things you can't do: offer it as a hosted/managed service, or remove the licensing notices. We chose ELv2 over MIT because MIT permits repackaging the code as a competing closed-source SaaS - ELv2 prevents that while keeping the source available to everyone.
