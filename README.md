# capy 🦫

**Context-Aware Prompting** ...or "Yet another solution to LLM context problem"

[![GitHub stars](https://img.shields.io/github/stars/serpro69/capy?style=for-the-badge&color=yellow)](https://github.com/serpro69/capy/stargazers) [![GitHub forks](https://img.shields.io/github/forks/serpro69/capy?style=for-the-badge&color=blue)](https://github.com/serpro69/capy/network/members) [![Last commit](https://img.shields.io/github/last-commit/serpro69/capy?style=for-the-badge&color=green)](https://github.com/serpro69/capy/commits) [![License: ELv2](https://img.shields.io/badge/License-ELv2-blue.svg?style=for-the-badge)](LICENSE)

## Privacy & Architecture

Context Mode is not a CLI output filter or a cloud analytics dashboard. It operates at the MCP protocol layer - raw data stays in a sandboxed subprocess and never enters your context window. Web pages, API responses, file analysis, Playwright snapshots, log files - everything is processed in complete isolation.

**Nothing leaves your machine.** No telemetry, no cloud sync, no usage tracking, no account required. Your code, your prompts, your session data - all local. The SQLite databases live in your home directory and die when you're done.

This is a deliberate architectural choice, not a missing feature. Context optimization should happen at the source, not in a dashboard behind a per-seat subscription. Privacy-first is our philosophy - and every design decision follows from it. [License →](#license)

## The Problem

Every MCP tool call dumps raw data into your context window. A Playwright snapshot costs 56 KB. Twenty GitHub issues cost 59 KB. One access log - 45 KB. After 30 minutes, 40% of your context is gone. And when the agent compacts the conversation to free space, it forgets which files it was editing, what tasks are in progress, and what you last asked for.

Context Mode is an MCP server that solves both halves of this problem:

1. **Context Saving** - Sandbox tools keep raw data out of the context window. 315 KB becomes 5.4 KB. 98% reduction.
2. **Session Continuity** - Every file edit, git operation, task, error, and user decision is tracked in SQLite. When the conversation compacts, context-mode doesn't dump this data back into context - it indexes events into FTS5 and retrieves only what's relevant via BM25 search. The model picks up exactly where you left off. If you don't `--continue`, previous session data is deleted immediately - a fresh session means a clean slate.

## Security

`capy` enforces the same permission rules you already use - but extends them to the MCP sandbox. If you block `sudo`, it's also blocked inside `capy_execute`, `capy_execute_file`, and `capy_batch_execute`.

**Zero setup required.** If you haven't configured any permissions, nothing changes. This only activates when you add rules.

```json
{
  "permissions": {
    "deny": ["Bash(sudo *)", "Bash(rm -rf /*)", "Read(.env)", "Read(**/.env*)"],
    "allow": ["Bash(git:*)", "Bash(npm:*)"]
  }
}
```

Add this to your project's `.claude/settings.json` (or `~/.claude/settings.json` for global rules). All platforms read security policies from Claude Code's settings format - even on Gemini CLI, VS Code Copilot, and OpenCode. Codex CLI has no hook support, so security enforcement is not available.

The pattern is `Tool(what to match)` where `*` means "anything".

Commands chained with `&&`, `;`, or `|` are split - each part is checked separately. `echo hello && sudo rm -rf /tmp` is blocked because the `sudo` part matches the deny rule.

**deny** always wins over **allow**. More specific (project-level) rules override global ones.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow and TDD guidelines.

```bash
git clone https://github.com/serpro69/capy.git
cd context-mode && npm install && npm test
```

## License

Licensed under [Elastic License 2.0](LICENSE) (source-available). You can use it, fork it, modify it, and distribute it. Two things you can't do: offer it as a hosted/managed service, or remove the licensing notices. We chose ELv2 over MIT because MIT permits repackaging the code as a competing closed-source SaaS - ELv2 prevents that while keeping the source available to everyone.
