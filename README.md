# capy

**C**ontext-**A**ware **P**rompting ...or "**Y**et another solution to LLM context problem"

[![GitHub stars](https://img.shields.io/github/stars/serpro69/capy?style=for-the-badge&color=yellow)](https://github.com/serpro69/capy/stargazers) [![GitHub forks](https://img.shields.io/github/forks/serpro69/capy?style=for-the-badge&color=blue)](https://github.com/serpro69/capy/network/members) [![Last commit](https://img.shields.io/github/last-commit/serpro69/capy?style=for-the-badge&color=green)](https://github.com/serpro69/capy/commits) [![License: ELv2](https://img.shields.io/badge/License-ELv2-blue.svg?style=for-the-badge)](LICENSE)

## Privacy & Architecture

`capy` is not a CLI output filter or a cloud analytics dashboard. It operates at the MCP protocol layer - raw data stays in a sandboxed subprocess and never enters your context window. Web pages, API responses, file analysis, log files - everything is processed in complete isolation.

**Nothing leaves your machine.** No telemetry, no cloud sync, no usage tracking, no account required. Your code, your prompts, your session data - all local. The SQLite databases live in your home directory and die when you're done.

This is a deliberate architectural choice, not a missing feature. Context optimization should happen at the source, not in a dashboard behind a per-seat subscription. Privacy-first is our philosophy - and every design decision follows from it. [License](#license)

## The Problem

Every MCP tool call dumps raw data into your context window. A single API response costs 56 KB. Twenty GitHub issues cost 59 KB. One access log - 45 KB. After 30 minutes, 40% of your context is gone. And when the agent compacts the conversation to free space, it forgets which files it was editing, what tasks are in progress, and what you last asked for.

`capy` is an MCP server and Claude Code plugin that solves both halves of this problem:

1. **Context Saving** - Sandbox tools keep raw data out of the context window. 315 KB becomes 5.4 KB. ~98% reduction.
2. **Searchable Knowledge Base** - All sandboxed output is indexed into SQLite FTS5 with BM25 ranking. Use `capy_search` to retrieve specific sections on demand. Three-tier fallback: Porter stemming, trigram substring, fuzzy Levenshtein correction.
3. **Session Continuity** _(planned)_ - Track file edits, git operations, tasks, errors, and user decisions in SQLite. When the conversation compacts, retrieve only what's relevant via BM25 search instead of dumping everything back into context.

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

**Build from source** (requires Go 1.23+ and a C compiler):
```bash
git clone https://github.com/serpro69/capy.git
cd capy
make build
mv capy /usr/local/bin/   # or anywhere on your PATH
```

### Setup (Claude Code)

In your project directory:

```bash
capy setup
capy doctor   # verify everything is green
```

That's it. Start using Claude Code normally. capy works automatically:

- **Bash commands** producing large output are nudged toward the sandbox
- **curl/wget** calls are intercepted and redirected to `capy_fetch_and_index`
- **WebFetch** is blocked in favor of `capy_fetch_and_index`
- **Read** for analysis (not editing) is nudged toward `capy_execute_file`
- **Subagents** get routing instructions injected automatically

You don't need to call capy tools yourself. The LLM learns the routing from the hooks and CLAUDE.md instructions that `capy setup` installed. But you can ask it directly: "use capy_batch_execute to research X" if you want to be explicit.

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

[store.cleanup]
cold_threshold_days = 30
auto_prune = false

[executor]
timeout = 30               # seconds
max_output_bytes = 102400  # 100 KB

[server]
log_level = "info"
```

All settings have sensible defaults. Configuration files are optional — capy works out of the box.

## CLI Commands

| Command | Description |
|---------|-------------|
| `capy` or `capy serve` | Start the MCP server (stdio transport) |
| `capy setup` | Configure capy for the current project |
| `capy doctor` | Run diagnostics on the installation |
| `capy which` | Print the knowledge base path for the current project |
| `capy cleanup` | Remove stale knowledge base entries |
| `capy hook <event>` | Handle a hook event (called by Claude Code, not you) |

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
|------|--------------|
| `capy_execute` | Run code in a sandboxed subprocess. Supports 11 languages: JavaScript, TypeScript, Python, Shell, Ruby, Go, Rust, PHP, Perl, R, Elixir. Only stdout enters context. Pass `intent` to auto-index large output. |
| `capy_execute_file` | Inject a file into a sandbox variable (`FILE_CONTENT`) and process it with code you write. The raw file never enters context — only your printed summary does. |
| `capy_batch_execute` | The primary research tool. Runs multiple shell commands, auto-indexes all output as markdown, and searches with multiple queries — all in ONE call. Replaces dozens of individual Bash calls. |

### Knowledge

| Tool | What It Does |
|------|--------------|
| `capy_index` | Index text, markdown, or a file path into the FTS5 knowledge base for later search. |
| `capy_search` | Search indexed content. 8-layer fallback: Porter stemming (AND/OR), trigram substring (AND/OR), then fuzzy Levenshtein-corrected versions of all four. Progressive throttling prevents context flooding from excessive search calls. |
| `capy_fetch_and_index` | Fetch a URL, convert HTML to markdown (strips nav/script/style/header/footer), index into the knowledge base, return a ~3 KB preview. |

### Utility

| Tool | What It Does |
|------|--------------|
| `capy_stats` | Session report: bytes saved, context reduction ratio, per-tool breakdown, knowledge base tier distribution. |
| `capy_doctor` | Diagnostics: version, available runtimes, FTS5 status, config, hook registration, MCP registration, security policies. |
| `capy_cleanup` | Remove cold knowledge base entries (never accessed, older than threshold). |

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

## Hook System

capy uses Claude Code's hook system to intercept tool calls before they execute. After `capy setup`, this works automatically — you don't need to configure anything.

### What gets intercepted

| Pattern | What happens |
|---------|-------------|
| `curl`/`wget` in Bash | Command replaced with message directing to `capy_fetch_and_index` |
| `fetch()`, `requests.get()`, `http.get()` in Bash | Command replaced with message directing to `capy_execute` |
| `gradle`, `maven` in Bash | Redirected to `capy_execute` sandbox |
| `WebFetch` tool | Denied — use `capy_fetch_and_index` instead |
| `Read` tool | One-time advisory: prefer `capy_execute_file` for analysis |
| `Grep` tool | One-time advisory: prefer `capy_execute` for large searches |
| `Agent`/`Task` tools | Routing block injected into subagent prompt; Bash subagents upgraded to general-purpose |

### Other platforms

`capy setup` currently generates Claude Code configuration. Automated setup for other platforms is planned (`capy setup --platform <name>`).

Hooks already recognize tool name aliases for these platforms, so the routing logic works once you wire up the MCP server and hook commands manually:

| Platform | Recognized tool aliases |
|----------|------------------------|
| Gemini CLI | `run_shell_command`, `read_file`, `grep_search`, `web_fetch` |
| OpenCode | `bash`, `view`, `grep`, `fetch`, `agent` |
| Codex CLI | `shell`, `shell_command`, `exec_command`, `container.exec`, `grep_files` |
| Cursor | `mcp_web_fetch`, `Shell` |
| VS Code Copilot | `run_in_terminal` |
| Kiro CLI | `fs_read`, `execute_bash` |

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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow.

```bash
git clone https://github.com/serpro69/capy.git
cd capy
make build && make test
```

## License

Licensed under [Elastic License 2.0](LICENSE) (source-available). You can use it, fork it, modify it, and distribute it. Two things you can't do: offer it as a hosted/managed service, or remove the licensing notices.
