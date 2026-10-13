<div align="center">
  <img width="340" height="340" alt="logo" src="docs/assets/capy_logo_no_text.svg" />
</div>

# capy

**C**ontext-**A**ware **P**rompting ...or "**Y**et another solution to LLM context problem"

[![GitHub stars](https://img.shields.io/github/stars/serpro69/capy?style=for-the-badge&color=yellow)](https://github.com/serpro69/capy/stargazers) [![GitHub forks](https://img.shields.io/github/forks/serpro69/capy?style=for-the-badge&color=blue)](https://github.com/serpro69/capy/network/members) [![Last commit](https://img.shields.io/github/last-commit/serpro69/capy?style=for-the-badge&color=green)](https://github.com/serpro69/capy/commits) [![License: ELv2](https://img.shields.io/badge/License-ELv2-blue.svg?style=for-the-badge)](LICENSE.md)

> [!IMPORTANT]
> This project was created with the help of Claude-Code. It is, however, reviewed, tested, and reworked with a human-in-the-loop.
>
> No AI slop here. Purely AI-made skills are hot garbage, and that's putting it mildly.
>
> That said, if you have any problems with code that is written by AI - you've been warned. But, then again, why would you be interested in AI-related configs and skills in the first place... `¯\_(ツ)_/¯`

## ToC

<!--toc:start-->
- [Privacy & Architecture](#privacy--architecture)
- [The Problem](#the-problem)
- [Benchmarks](#benchmarks)
  - [Retrieval Quality](#retrieval-quality)
  - [Context Reduction](#context-reduction)
- [Quick Start](#quick-start)
  - [Install](#install)
  - [Setup](#setup)
  - [Setup (Codex CLI)](#setup-codex-cli)
- [How It Works — By Example](#how-it-works--by-example)
  - [Before capy](#before-capy)
  - [After capy](#after-capy)
  - [The `intent` parameter](#the-intent-parameter)
- [Configuration](#configuration)
- [Encryption](#encryption)
  - [Setup](#setup-1)
  - [Project credentials](#project-credentials)
  - [Upgrade and credential migration](#upgrade-and-credential-migration)
    - [Rollback](#rollback)
  - [Initial encryption](#initial-encryption)
  - [Cross-machine sync](#cross-machine-sync)
  - [Keeping the DB out of your project repo (recommended)](#keeping-the-db-out-of-your-project-repo-recommended)
  - [Key rotation](#key-rotation)
  - [Passphrase recommendations](#passphrase-recommendations)
- [Session Vault](#session-vault)
  - [Setup](#setup-2)
  - [Quick start](#quick-start-1)
  - [Archiving sessions](#archiving-sessions)
  - [Codex sessions](#codex-sessions)
  - [Commands](#commands)
  - [Restore and resume](#restore-and-resume)
  - [Naming sessions](#naming-sessions)
  - [Cross-machine sync](#cross-machine-sync-1)
  - [Key rotation](#key-rotation-1)
  - [Storage and limits](#storage-and-limits)
  - [Environment variables](#environment-variables)
- [CLI Commands](#cli-commands)
  - [Knowledge indexing](#knowledge-indexing)
  - [Knowledge search](#knowledge-search)
  - [Shell Completions](#shell-completions)
- [MCP Tools](#mcp-tools)
  - [Execution](#execution)
  - [Knowledge](#knowledge)
  - [Utility](#utility)
- [Upstream sync compatibility notes](#upstream-sync-compatibility-notes)
- [Security](#security)
  - [Sandbox protections](#sandbox-protections)
- [Hook System](#hook-system)
  - [What gets intercepted](#what-gets-intercepted)
  - [Platform support](#platform-support)
  - [Codex hook recovery](#codex-hook-recovery)
- [Troubleshooting](#troubleshooting)
- [Acknowledgements](#acknowledgements)
- [Contributing](#contributing)
- [License](#license)
<!--toc:end-->

## Privacy & Architecture

`capy` is not a CLI output filter or a cloud analytics dashboard. It operates at the MCP protocol layer — raw data stays in a sandboxed subprocess and never enters your context window. Web pages, API responses, file analysis, log files — everything is processed in complete isolation.

**Nothing leaves your machine.** No telemetry, no cloud sync, no usage tracking, no account required. Your code, your prompts, your session data — all local. The SQLite databases are encrypted at rest. Knowledge credentials come from local project files or the launch environment and are never sent to a remote service.

This is a deliberate architectural choice, not a missing feature. Context optimization should happen at the source, not in a dashboard behind a per-seat subscription. Privacy-first is our philosophy — and every design decision follows from it. [License](#license)

## The Problem

Every MCP tool call dumps raw data into your context window. A single API response costs 56 KB. Twenty GitHub issues cost 59 KB. One access log — 45 KB. After 30 minutes, 40% of your context is gone. And when the agent compacts the conversation to free space, it forgets which files it was editing, what tasks are in progress, and what you last asked for. The sessions themselves are disposable, too — Claude Code deletes them after 30 days, `/compact` rewrites them in place, and nothing survives across projects or an accidental delete.

`capy` is an MCP server and Claude Code plugin that addresses every layer of this — from the live context window down to the permanence of the sessions themselves:

1. **Context Saving** — Sandbox tools keep raw data out of the context window. 315 KB becomes 5.4 KB. ~98% reduction.
2. **Searchable Knowledge Base** — All sandboxed output is indexed into SQLite FTS5 with BM25 ranking. Use `capy_search` to retrieve specific sections on demand. Multi-layer search: Porter stemming, trigram substring, fuzzy Levenshtein correction, with Reciprocal Rank Fusion and proximity reranking.
3. **Session Memory** — Past conversation transcripts are automatically indexed on server start. When the conversation compacts, the LLM can search prior sessions for context via BM25 search.
4. **Session Vault** — A durable, cross-project, encrypted archive of every Claude Code session. Search, view, restore, or resume past conversations long after Claude Code's 30-day cleanup, compaction, or accidental deletion would have lost them. See [Session Vault](#session-vault).

## Benchmarks

capy ships with a benchmark suite that validates its claims with deterministic, reproducible metrics — no LLM-in-the-loop evaluation. Run `make bench` to reproduce on your machine. Measured across 156 synthetic test cases spanning 5 content types (markdown, JSON, plaintext, transcripts, curated knowledge).

### Retrieval Quality

| Metric                                      | Score |
| ------------------------------------------- | ----- |
| R@1 (at least one relevant result in top 1) | 0.897 |
| R@5                                         | 0.987 |
| R@10                                        | 0.994 |
| MRR (mean reciprocal rank)                  | 0.938 |
| NDCG@10                                     | 0.950 |

### Context Reduction

"Bytes saved" is a vanity metric. NIAH measures whether specific facts survive compression — not just how many bytes were removed:

| Metric                                                | Score |
| ----------------------------------------------------- | ----- |
| Compression Ratio                                     | 49.8% |
| Context Recall (fraction of specific facts preserved) | 0.983 |
| Perfect Recall Rate (cases with all facts preserved)  | 97.1% |
| Effective Compression (compression x recall)          | 49.7% |

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

For a new database, the environment-only setup below works. For separate project keys, use a [project credential file](#project-credentials). Keep the existing passphrase when connecting to an existing database.

```bash
export CAPY_DB_KEY=$(openssl rand -base64 48)
echo "export CAPY_DB_KEY='$CAPY_DB_KEY'" >> ~/.zshrc  # or ~/.bashrc
```

**2. Configure your project** (Claude Code only; omit `--platform` to install both platforms):

```bash
capy setup --platform claude-code
capy doctor   # verify everything is green
```

Use `capy setup --platform claude-code --local` to write Claude hooks to
`.claude/settings.local.json`. The `--local`/`--project` flags select the Claude
settings file only; without `--platform`, setup still configures both platforms.
Codex's read-only owner checks inspect ancestor `.codex` configurations,
including the home directory when it is an ancestor, to detect conflicting
installations. Codex-generated `hooks.state` trust metadata is preserved and
does not count as an executable hook or a capy installation.

**3. Use normally.** Start using Claude Code — capy works automatically:

- **Bash commands** producing large output are nudged toward the sandbox
- **curl/wget** calls are denied with guidance to use `capy_fetch_and_index` or `capy_execute`; silent file downloads remain allowed
- **WebFetch** is blocked in favor of `capy_fetch_and_index`
- **Read** for analysis (not editing) is nudged toward `capy_execute_file`
- **Subagents** get routing instructions with one tool-discovery attempt when supported and native-tool fallback when capy tools are unavailable
- **Past sessions** are indexed on server start for cross-session search

You don't need to call capy tools yourself. The LLM learns the routing from the hooks and CLAUDE.md instructions that `capy setup` installed. But you can ask it directly: "use capy_batch_execute to research X" if you want to be explicit.

### Setup (Codex CLI)

Install a compatible capy executable on each collaborator's PATH and configure
encryption as above, then run from the project root:

```bash
capy setup --platform codex
capy doctor
```

Setup registers SessionStart, SubagentStart, PreToolUse, PostToolUse and
SessionEnd in `.codex/hooks.json`, and MCP in `.codex/config.toml`. Commit both
with `.codex/capy-owner`. The marker is a portable
installation ID, not a secret; rerunning setup preserves it. Existing toolbox
and other external hooks remain installed.

Hooks call `capy codex-launch` through a compact shell supervisor; MCP calls it
directly. Both validate the owner and dispatch in Go without a project wrapper.
Setup checks its PATH candidate with a bounded protocol probe before writing
either platform. `./capy setup` alone does not make that binary available on
PATH; `--binary` must identify the same executable found there. Add the intended
binary's directory to PATH and restart Codex so it inherits that environment.
Claude-only and DB-repo setup do not require the Codex PATH probe.

Only the final `capy-codex-launch-v2` format is supported. For a checkout with
earlier artifacts from this PR, stop affected sessions and restore the updated
committed bindings, or remove only obsolete capy entries from an external
terminal before rerunning setup. Preserve external hooks and the owner marker.

Activate the project `.codex/` layer by trusting the project in Codex, then
review and trust the exact generated definitions in `/hooks`. These are separate
gates; changed definitions may need renewed trust. Restart/resume as needed.
Setup never changes trust records or an explicit hooks feature override.
See [Codex's hooks reference](https://learn.chatgpt.com/docs/hooks).

Both doctor interfaces check registration on disk and report visible disabling
or overlap. They cannot verify trust or event delivery. The integration targets
Codex CLI 0.162.1. The [v1 verification record](docs/feat/wip/codex-hooks/verification.md)
is historical; [v2 verification](docs/feat/wip/codex-hooks/verification-v2.md)
records controlled tests and selected live CLI evidence. Other versions
and Codex surfaces need their own runtime verification.

Hooks and MCP resolve the same owner when launched from an ordinary project
subdirectory. Moved trees, paths with spaces and sibling worktrees have
installed-command coverage. Nested installations are rejected, including copies
with identical IDs. Non-Git command resolution works, but automatic Codex hook
discovery outside Git projects remains unverified.

For installation failures or rollback, see [Codex hook recovery](#codex-hook-recovery).

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
# key_file = ".capy/db.key"   # optional literal passphrase file; relative to the database owner
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

[search]
window_ms = 60000          # shared MCP search window (1000–3600000 ms)
taper_after = 3            # later calls return at most one result/query
block_after = 8            # later calls are blocked until reset

[vault]
min_session_bytes = 0      # minimum size for new archives; 0 disables
```

Configuration files are optional; knowledge access still requires a [credential](#encryption). Invalid configuration stops knowledge commands instead of selecting a default database. `capy doctor` reports the configuration failure and continues independent checks.

The `[search]` integers require `1 <= taper_after < block_after <= 10000`.
Omitted fields inherit lower-priority settings; explicit zero, wrong types and
out-of-range values fail loading. Restart the MCP server after changes.
The budget is shared by every caller of that server, including child agents;
raising the thresholds does not create separate agent budgets. With defaults,
calls 1–3 return at most two hits per query, calls 4–8 at most one, and call 9
onward is blocked until the 60-second window resets. A smaller requested `limit`
is honored. Every valid response reports its call count, effective limit,
untapered/unblocked calls left and reset time as measured when admitted, including
empty, partial and blocked results. Invalid requests consume no budget:
malformed arrays, blank queries, non-string selectors and nonpositive/fractional
limits now return errors instead of being coerced or ignored. Serialized string
arrays and positive integer numeric strings remain accepted. This budget applies
to `capy_search`; terminal `capy search` and `capy_vault_search` are unaffected.

> **Caution with git and the knowledge DB.** SQLite WAL sidecar files (`.db-wal`, `.db-shm`) are created when the DB is written to during a session. capy flushes the WAL on session close automatically, but the files only get cleaned up if the session actually wrote to the DB. If you see stale WAL files (e.g., after upgrading capy or after an unclean shutdown), run `capy checkpoint` to flush them manually. If you want to commit the DB to git (e.g., to share across machines), run `capy checkpoint` first — it flushes the WAL into the main file and removes the sidecar files.

## Encryption

The knowledge database is encrypted at rest using sqlite3mc (SQLCipher v4 compatible). capy refuses to start without a passphrase.

### Setup

For a new database, either provision a private [project credential file](#project-credentials) or set `CAPY_DB_KEY` in the environment that launches capy. For an existing database, reuse its current passphrase. This environment-only example stores a generated key in your shell profile:

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

### Project credentials

`capy serve` (including bare `capy`), `dbsize`, `cleanup`, `checkpoint`, and CLI `doctor` resolve knowledge credentials in Go. A declared project credential takes precedence over an inherited `CAPY_DB_KEY`, including a nonempty key from another project. `capy which` inspects the configured path without reading credentials; checkpointing a missing database is a successful no-op without a key. Help, setup, version, and Claude hook events do not require a knowledge key.

To avoid depending on the launcher's inherited knowledge key, put the **existing database passphrase** in a literal file such as `.capy/db.key` and add its path to the project's `.capy.toml` (merge this into an existing `[store]` section):

```toml
[store]
key_file = ".capy/db.key"
```

Keep the file private (`chmod 600 .capy/db.key`) and outside version control. Key files must have mode `0400` or `0600`, with no execute, group/other, or special permission bits. Commands reject unsafe permissions before reading the passphrase, without falling back to another credential. Project `capy setup` repairs existing configured key files to `0600`, preserving files already at `0400` or `0600`. For symlinks it checks and repairs the regular-file target, including targets outside the project. Setup reports access or repair failures, skips missing targets, and never creates credentials or changes their contents. The `--db-repo` setup mode only installs its database-repository artifacts. This permission policy applies to `store.key_file`; dotenv files retain their existing admission rules. It checks Unix mode bits, not extended ACLs or parent-directory permissions.

`capy setup` ignores `.capy/**`; other credential locations need their own ignore rules. The file contains the passphrase itself; assignment syntax and quotes would become part of the passphrase. Leading/trailing spaces are preserved, and one final LF or CRLF is removed. Its raw size limit is 4,096 bytes, including that newline. Empty files, embedded line endings/NULs, nonregular files, and oversized files fail without fallback. Symlinks to regular files are supported.

Selection order is `store.key_file`, the database owner's `.env`, a distinct main-worktree `.env`, then inherited `CAPY_DB_KEY`. Relative key-file paths resolve from the database owner. With a relative `store.path`, linked worktrees share the main checkout's database and credentials; their own `.env` is ignored. Absolute/XDG database modes consult the selected worktree first. An explicitly empty `key_file = ""` clears an inherited config setting. Invalid or inaccessible selected credentials fail rather than trying another key.

Configuration still comes from the selected project and the global layer, even when the main checkout owns the database. This also applies to relative key-file paths declared globally. Paths do not expand `~` or environment variables, and a database symlink does not change the credential owner. `--project-dir` selects the project before resolution; an explicit subdirectory is used as supplied.

The dotenv reader is literal and limited: blank lines, comments, and single-line assignments, with optional `export`. It never executes shell code, expands variables, reads `.envrc`, or exports resolved values. Optional dotenv files must be regular and no larger than 1 MiB. If no knowledge-key declaration exists, readable dotenv content does not supply a key and environment-only/direnv usage continues to work.

The exact name `CAPY_DB_KEY` is recognized with spaces/tabs around `=`. LF and CRLF files work. Unquoted target values cannot contain whitespace, quotes, backslashes, expansion markers, or shell operators; `#` starts an unquoted comment only after whitespace. Single quotes preserve literal bytes. Double quotes accept only escaped backslash and double quote, and reject `$` and backticks. After a closing quote, only whitespace and an optional comment are allowed. Other assignments are checked for structure but discarded. These file-size limits do not limit environment-only passphrase length.

Resolution errors occur before creating the knowledge database or its `.project` marker. A selected key that cannot decrypt an existing database produces an authentication error; capy does not retry another credential or replace that database. A running server captures its key once, so restart it after changing credentials. See [ADR-032](docs/adr/032-project-db-key-resolution.md) for the policy and connection-lifetime contract.

CLI `capy doctor` reports unsafe key-file permissions as a failed credential check and skips database access. The MCP `capy_doctor` tool checks the current key-file metadata separately: a failed permission check does not replace the running server's captured key or stop its database diagnostics. Neither doctor repairs permissions.

### Upgrade and credential migration

**Two breaking changes require attention before regenerating wrappers:**

- **Whole-file dotenv validation.** If `.env` declares `CAPY_DB_KEY`, unsupported syntax anywhere in that file, before or after the declaration, blocks resolution even when an inherited key is correct. Duplicate, empty, multiline, or executable knowledge-key declarations also fail. For an application dotenv containing shell constructs, put the same passphrase in a literal file and configure `store.key_file`; that explicit file bypasses dotenv parsing entirely.
- **Vault keys remain environment-only.** Older wrappers sometimes sourced the main-worktree `.env` when `serve` inherited no knowledge key, incidentally loading `CAPY_VAULT_KEY` too. New wrappers do not source `.env`. A vault key stored only there no longer enables the vault. Set `CAPY_VAULT_KEY` in the environment of the process that actually launches MCP, then restart that host/daemon and MCP. Exporting it in a new terminal does not change an already-running daemon's environment. Knowledge credential resolution never reads or overwrites the vault key.

**Key-file permission enforcement:** existing `store.key_file` credentials with broader permissions now block normal access and MCP startup. Run project `capy setup` or set the target file to `0600` before restarting. Existing `0400` files remain supported.

Upgrade the binary, then invoke it **directly** in each project to regenerate
Claude wrappers and Codex bindings. Codex also requires that binary on PATH:

```bash
capy setup --platform claude-code --project # use --local if that is your existing hook target
capy setup --platform codex
```

Restart the launcher/MCP processes. Confirm actual knowledge reads with `capy_search` and vault status with `capy_doctor` through MCP; terminal doctor alone does not verify the launcher's environment. Both doctors explain the vault migration when its key is absent. Updating only the binary leaves old wrappers able to execute dotenv or stop startup before Go can select a project credential.

#### Rollback

Stop MCP cleanly, then run `capy checkpoint` with the current binary and valid project credential. Before downgrading, supply the **same existing knowledge passphrase** as `CAPY_DB_KEY` in the actual launch environment, and supply `CAPY_VAULT_KEY` separately if needed. Downgrade the binary, run that binary's setup for each installed platform, restart the launcher/MCP, and verify actual database reads and vault status.

v0.16.4 ignores unknown `store.key_file` configuration while retaining `store.path`; it cannot obtain a passphrase from that file. Leaving the setting is compatible with that version, but leaving the credential only in the file is not. New wrappers paired with an old binary do not restore dotenv loading. This feature changes no database format: do not rotate or rewrite the database just to roll back.

### Initial encryption

Existing unencrypted databases must be encrypted before capy will use them. Stop all processes using the database first:

```bash
export CAPY_DB_KEY='your-passphrase-here'
capy encrypt
# When prompted for the current passphrase, press Enter (empty = unencrypted).
```

The original database is preserved as `<path>.bak`. After encryption, update any project credential to the new passphrase before restarting capy.

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

The pre-commit hook rejects unencrypted databases automatically — run `capy encrypt` first if the commit is blocked. It also **blocks the commit if the checkpoint fails** (e.g. the MCP server is still holding the DB): the `capy setup` wrapper propagates real exit codes for every subcommand except `hook` events, so a busy checkpoint now fails the commit instead of silently committing a stale file. Stop the session (or wait for the server to release the DB) and re-commit.

> **Git worktrees.** With a project-scoped `store.path`, sessions running in a [git
> worktree](https://git-scm.com/docs/git-worktree) automatically use the **main**
> worktree's DB instead of a per-worktree copy — so worktree knowledge persists to
> the shared `.capy/knowledge.db` and never produces an unresolvable binary merge
> conflict. Detection reads the worktree's `.git` file directly (no `git` required).
> CLI commands (`capy sweep`, `cleanup`, `checkpoint`, `dbsize`, `which`) run from a
> worktree operate on the main worktree's DB too. Absolute paths and the XDG default
> are unaffected. See [ADR-026](docs/adr/026-worktree-shared-knowledge-db.md).

### Keeping the DB out of your project repo (recommended)

Committing `knowledge.db` directly into your project (as above) works, but every
`capy checkpoint` + commit appends a **fresh full binary blob** to that repo's
history — encrypted SQLite doesn't delta-compress, so the DB never diffs cleanly.
Over time this bloats the repo and slows clones for everyone who pulls it, even
teammates who don't use capy.

A cleaner approach — field-tested in capy's own repo — keeps the DB in a
**dedicated private repo** and symlinks it into your project. The project repo
never grows; the DB's history lives (and can be pruned) in its own repo.

```bash
# 1. One-time: a private repo to hold your capy databases across projects.
git init ~/capy-db && mkdir -p ~/capy-db/myproject

# 2. In your project, point store.path at the usual project-local location.
#    .capy.toml
#    [store]
#    path = ".capy/knowledge.db"

# 3. Move the existing DB into the private repo and symlink it back.
capy checkpoint                                        # flush WAL first
mv .capy/knowledge.db ~/capy-db/myproject/knowledge.db
ln -s ~/capy-db/myproject/knowledge.db .capy/knowledge.db

# 4. One-time: install the guard hook in the private repo.
cd ~/capy-db && capy setup --db-repo
```

capy resolves the symlink transparently — `store.path` is joined, not
symlink-expanded, so SQLite opens the real file and creates its WAL/SHM sidecars
in `~/capy-db/`, **not** in your project. The project repo ignores the symlink
automatically (`.capy/**` is gitignored), so `git status` there stays clean.

`capy checkpoint` resolves the database symlink and verifies the WAL/SHM files
beside the real database in the private repo. Credentials still come from the
owning project. Stop all processes using that database before checkpointing:
the command returns an error if a non-empty WAL or any SHM file remains, or if
it cannot inspect the sidecars. An idle connection can keep SHM present even
after the WAL is flushed. No additional WAL/SHM symlinks are needed.

Commit and sync from the private repo instead — commit **as usual**; the guard
hook installed in step 4 makes it safe by construction:

```bash
git -C ~/capy-db add myproject/knowledge.db
git -C ~/capy-db commit -m "Update myproject knowledge base"
git -C ~/capy-db push
```

`capy setup --db-repo` installs a pure-`sh` pre-commit hook (no capy binary or
key needed) plus `.gitignore` entries for the `*.db-wal`/`*.db-shm` sidecars. The
hook does not *repair* a database — it only *refuses* an unsafe one, so committing
either succeeds or is blocked with the remedy named:

| Blocked when a staged `*.db` … | Remedy |
|---|---|
| starts with `SQLite format 3` (unencrypted) | run `capy encrypt` **in the project that owns it**, then re-stage |
| has a non-empty `<db>-wal` (pending WAL) | stop active capy sessions for that project, or run `capy checkpoint --project-dir <project>` (works from anywhere), then re-stage |
| has a `<db>-shm` (open connection) | same as above |

No sidecars beside the file means the last connection closed cleanly and the main
file is complete, so a quiet checkout with no running sessions commits with no
extra steps. A zero-byte `-wal` (which `PRAGMA wal_checkpoint(TRUNCATE)` can leave
behind) is tolerated.

> **Pull only when no sessions are running.** Git has no pre-pull hook, so this
> one rule is on you: a `git pull` that replaces the main DB file while a session
> still holds the old inode is exactly the ADR-015 corruption vector. The hook's
> "no sidecars" condition is the same precondition — check for `*.db-wal`/`*.db-shm`
> beside the file (and stop sessions) before pulling.

On another machine, clone the private repo to the same path and re-create the
symlink (the symlink itself isn't tracked by the project repo). The
`store.path` config committed to your project ties it all together.

> **Why a symlink and not just an absolute `store.path`?** An absolute path also
> keeps the DB outside the repo, but a project-relative `.capy/knowledge.db` is
> what enables the git-worktree sharing above and keeps the config portable across
> machines and checkouts. The symlink gives you both: relative path in-repo, DB
> body out-of-repo.

### Key rotation

Stop all processes using the target database before rotating its key. After success, update any project credential file or dotenv declaration before restarting MCP; running servers retain their selected key until restart.

```bash
export CAPY_DB_KEY='new-passphrase'
capy encrypt
# Enter the OLD passphrase when prompted.
```

The old key is always prompted for. The new key comes from `CAPY_DB_KEY`, or a confirmed prompt if that variable is empty. Project files supply neither rotation input and are never rewritten by `capy encrypt`; update them yourself before restarting. The command does not stop other processes or change the vault key. Its backup remains readable with the old key.

### Passphrase recommendations

- **32+ characters.** Shorter passphrases work but trigger a warning.
- **Generated, not memorized.** `openssl rand -base64 48` or a password manager.
- **Never in tracked configuration.** Store only a credential path in TOML; keep the passphrase in a private, ignored file, the launch environment, or a secrets manager.

## Session Vault

<img width="1690" height="896" alt="capy_vault" src="https://github.com/user-attachments/assets/c588e8a0-86d0-44bb-ab9d-c7cb1f7ab74e" />

Claude Code sessions are ephemeral, project-scoped, and destructible — lost to compaction (`/compact` rewrites the JSONL), Claude Code's 30-day auto-cleanup, or accidental deletion. The **vault** inverts all three: a **permanent, global, verbatim** archive of every session across every project — from **Claude Code and Codex CLI** alike — in its own encrypted SQLite database. It is both a full-text search index _and_ a backup/restore system — the raw JSONL is preserved byte-for-byte, so any archived session can be restored or resumed.

The vault is independent of the rest of capy. You can use it even if you don't run the MCP server or use any context-window features — the only prerequisite is the `CAPY_VAULT_KEY` environment variable.

### Setup

The vault uses its own encryption key, **separate from `CAPY_DB_KEY`**:

```bash
export CAPY_VAULT_KEY=$(openssl rand -base64 48)
echo "export CAPY_VAULT_KEY='$CAPY_VAULT_KEY'" >> ~/.zshrc  # or ~/.bashrc
```

Every `capy vault` command refuses to run without it. There is no separate `setup` step — set the key and run `import`.

### Quick start

```bash
capy vault import              # archive every session across all projects
capy vault list                # newest first
capy vault search "rate limiter"   # full-text search across all sessions
capy vault show 3f8a1c2b       # view a session (partial UUID, 8+ chars)
```

### Archiving sessions

Two archival paths populate the vault:

- **MCP server startup sweep** — when the capy MCP server boots, a background task archives the **current project's** sessions automatically (opt-in: silently skipped unless `CAPY_VAULT_KEY` is set) — Claude Code sessions from the project's directory and Codex rollouts whose recorded working directory is the project. Captures sessions that ended since the last boot. Set `CAPY_VAULT_SWEEP_ALL` (any non-empty value) to sweep **all** projects on every boot instead of just the current one — convenient if you don't run a periodic `import`, at the cost of a heavier startup scan.
- **`capy vault import`** — manual, scans **all projects** on every platform root that exists on disk (`~/.claude/projects`, and `~/.codex/sessions` + `~/.codex/archived_sessions` when present). This is the primary path. Because the startup sweep only covers the current project, sessions from projects you haven't reopened can age past Claude Code's 30-day cleanup. **Run `capy vault import` periodically — a cron job or shell habit** — to catch everything:

  ```bash
  # crontab -e — archive all sessions every morning at 9am
  0 9 * * *  CAPY_VAULT_KEY='…' /usr/local/bin/capy vault import
  ```

Import is idempotent: unchanged sessions are skipped, grown sessions are updated in place, and a smaller (likely compacted) variant never overwrites a fuller archive. Use `--dry-run` to preview, `--project <substr>` to scope, `--platform claude-code|codex` to restrict the run to one tool, `--source <dir>` to import from a non-default location (a Claude Code config or projects dir, or a Codex home — the layout is autodetected).

To exclude small sessions, set `[vault] min_session_bytes = 102400` in your TOML config (100 KiB), or pass `--min-size-bytes 102400` to `vault import` or `vault merge`. The setting defaults to `0` (disabled). An explicit `0` in a higher-priority config or CLI flag disables an inherited limit; negative values are rejected. Preview with:

```bash
capy vault import --min-size-bytes 102400 --dry-run
capy vault merge --from /path/to/other-vault.db --min-size-bytes 102400 --dry-run
```

The minimum applies to **new sessions** in manual imports, startup sweeps, and merges. Size means uncompressed transcript **plus sidecar** bytes, matching the import table; a session exactly at the limit qualifies, and compressed and plain copies behave identically. Exclusions report the actual size and limit. Already archived sessions continue to update and reindex, and excluded sessions can qualify on later runs after growing. This filter does not delete existing archives or local session files. Zero-message sessions remain excluded even when the size filter is disabled.

Config is loaded for the invocation's project (`--project-dir` overrides detection), and that one policy applies to the entire run, including imports or sweeps across projects. Use global config for a consistent limit in every project. Size is a coarse filter: metadata and tool output count too, so it does not classify conversation quality.

> **Compaction:** `/compact` is append-only — it appends a summary entry and never rewrites earlier turns — so the full pre-compaction transcript stays in the session file, and the next startup sweep or `import` still archives it verbatim. The only residual risk is *deleting* the session file before it's been swept/imported. Import often to minimize that window.

### Codex sessions

The vault archives [Codex CLI](https://github.com/openai/codex) sessions alongside Claude Code's, with no extra setup: whenever `~/.codex` (or `$CODEX_HOME`) holds a `sessions/` or `archived_sessions/` directory, `import` and the startup sweep pick up its `rollout-*.jsonl` and `rollout-*.jsonl.zst` files. Everything else works the same way — search hits are rank-merged across both tools, `show` prints a `Codex` heading, and `list`, `search` and `--json` tag every row with its platform (`claude-code` or `codex`). `capy doctor` reports which platform roots were found and how many sessions of each are archived.

Codex-specific behavior worth knowing:

- **Sub-agent sessions are children.** Codex records each spawned agent as its own rollout; the vault archives it as its own session linked to the parent. Children are **hidden from `list` by default** — `capy vault list --include-children` shows them under their parent (`↳ <parent id>`), `show <parent>` lists them, and in the TUI press `s` to toggle them or open a child straight from the parent's launch marker (`esc` returns to the parent). Deleting a parent warns about its children and never deletes them.
- **Short ids are 12 characters** for Codex sessions (Codex uses UUIDv7, whose 8-character prefixes collide often). Lookups still accept any unambiguous prefix of 8+ characters.
- **Restore goes back where Codex keeps it.** `restore` writes the plain `.jsonl` at the rollout's original path under `$CODEX_HOME` (or under `--output <dir>`), byte-identical to the archived file even when the original was `.zst`-compressed. A compressed twin already at that path is left untouched and noted.
- **Resume is not supported yet.** `capy vault resume` on a Codex session stops before touching disk and prints how to do it by hand: `capy vault restore <id>`, then `codex resume <uuid>`.
- **Revert variants are skipped.** Codex's `thread/revert` writes a second rollout for the same thread (`rollout-…_<id>.jsonl`); the vault archives only the base rollout and warns about each variant it skipped.
- **Older capy binaries refuse a vault that holds Codex sessions.** The first Codex session raises the vault's reader marker to 3, so a capy built before Codex support fails to open the file instead of mislabeling those sessions. Upgrade every machine before you `merge` or copy such a vault; a vault holding only Claude Code sessions is unaffected.

### Commands

All commands live under `capy vault` and require `CAPY_VAULT_KEY`. A persistent `--path <vault.db>` flag targets a specific vault file, overriding `CAPY_VAULT_PATH` and the XDG default. Lookups (`show`/`restore`/`resume`/`delete`/`rename`/`project`) accept a **partial UUID of 8+ characters**, git-style; an ambiguous prefix prints candidates to disambiguate.

| Command                                                                               | Description                                                                                                                                                                 |
| ------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `import [--source <dir>] [--project <substr>] [--platform claude-code\|codex] [--dry-run]` | Scan and archive sessions from every platform root that exists (Claude Code, Codex). Mutating by default.                                                              |
| `reindex`                                                                             | Rebuild the search index for sessions archived by an older indexer (reads stored blobs, not disk; rewrites only the FTS index). Run once after upgrading capy.              |
| `list [--project <substr>] [--name <substr>] [--platform claude-code\|codex] [--include-children] [--limit N] [--json]` | List sessions, newest first (`--limit` default 50, `0` = no limit). `--name` is a case-insensitive literal substring over the displayed title (custom name if set, else imported); combines with `--project`. Sub-agent (child) sessions are hidden unless `--include-children`. |
| `search <query> [--raw] [--project] [--role] [--after] [--before] [--limit] [--json]` | Full-text search with snippets. Plain keywords by default; `--raw` for FTS5 `MATCH` syntax. `--role user\|assistant\|tool\|system`; `--after`/`--before` take `YYYY-MM-DD`. |
| `show <session-id> [--format text\|markdown\|json]`                                   | Display a full session (the header names its platform and any parent or child sessions). Defaults to your `$PAGER`; `--format markdown\|json` for export.                  |
| `restore <session-id> [--output <path>]`                                              | Write the JSONL + all preserved sidecars back to disk (defaults to the session's Claude Code project dir, or its original path under the Codex home).                       |
| `resume <session-id> [--dir <path>]`                                                  | Restore, then launch `claude --resume`. Requires `claude` on `PATH`. Claude Code sessions only — see [Codex sessions](#codex-sessions).                                     |
| `delete <session-id> [--yes]`                                                         | Remove a session from the vault (does not touch on-disk copies). Prompts unless `--yes`; warns when the session has child sessions (they are kept).                         |
| `rename <session-id> <name>` · `rename <session-id> --clear`                          | Give a session a name of your own, or clear it to fall back to the imported title. Shown everywhere (`list`, `show`, `search`, JSON, TUI); the archived transcript is untouched. See [Naming sessions](#naming-sessions). |
| `project <session-id> <name>` · `project <session-id> --clear` | Set or clear one session's custom project label; prints the resulting project. Preserves the imported path, title and archived transcript. |
| `stats [--json]`                                                                      | Session count, content size, DB file size, effective-project and per-platform breakdown, child-session count, and the search-index version (with a count of sessions still below it — i.e. a `reindex` backlog). JSON retains raw-path `projects` and adds effective `project_groups`. |
| `checkpoint`                                                                          | Flush the WAL into `vault.db` — run before copying it to another machine.                                                                                                   |
| `rekey [--remove-backup]`                                                             | Rotate the vault's encryption key to the current `CAPY_VAULT_KEY`. **Stop the MCP server first.** Leaves `<vault>.bak` (still decryptable by the old key) unless `--remove-backup`.  |
| `compact`                                                                             | Recompress sessions archived before compression existed (zstd) and `VACUUM` to reclaim disk. No-op if nothing is left uncompressed. **Stop the MCP server first.**           |
| `merge --from <vault.db> [--key] [--project] [--dry-run]`                             | Non-destructively unite another machine's vault into this one — distinct sessions added, larger copy wins on UUID overlap; titles and projects reconcile independently, latest edit or clear wins. `--project` selects the source's effective project. Idempotent. Source key via `--key`/`CAPY_VAULT_MERGE_KEY`/`CAPY_VAULT_KEY`. |

`list`, `search`, and `show` also accept **`--tui`** for an interactive terminal UI (browse, live search, vim-style viewer) built on bubbletea. Starting it with `list --platform claude-code|codex --tui` keeps that platform scope while browsing, refreshing, and searching inside the TUI. A `--project` scope from `list` or `search` is retained too, including across child toggles and mode changes. `--tui` is not supported on the mutating/exec commands (`restore`, `resume`, `delete`). In the viewer, large tool results (and any `Read`/`NotebookRead` output) collapse to a marker — cycle markers with `]`/`[` and press `enter` to expand one inline, `esc`/`q` to return. `Edit`/`Write` results expand to a colored diff (the marker shows a `(+a −b)` stat). Plain `vault show` is unaffected. Other keys: `f` filter the list by title, effective project, or UUID, `e` rename the selected/open session (`ctrl+e` in search, where `e` types into the query), `ctrl+g` edit the project in list, search, or viewer (also while the list filter is active), `c` copy the current message to the clipboard (OSC-52), `r` restore and `R` resume the selected/open session.

#### Codex edits and executable inputs

In `capy vault show <id> --tui`, a recorded Codex patch appears as one expandable
group, such as `4 files changed (+23 −14)`. Use `]`/`[` to select it and `enter`
to see every file, sorted by original path, with add/delete/update/move labels,
per-file counts, and colored diffs. Paths beneath the session's recorded working
directory are shortened; moves show both source and destination.

The viewer supports both legacy patch events and paginated file-change events.
The marker reports the recorded outcome:

| Recorded evidence | Marker | Detail heading |
| --- | --- | --- |
| Completed with complete diffs | `N files changed (+A −R)` | `File changes · completed` |
| Completed with some unavailable diffs | `N files changed · diff incomplete` | `File changes · completed` |
| Completed with an empty file list | `Patch completed · no file changes recorded` | `File changes · completed` |
| Completed with unreadable changes | `Patch completed · diff unavailable` | `File changes · completed` |
| Failed | `Patch failed` | `File changes · failed` |
| Declined | `Patch declined` | `File changes · declined` |
| Missing or conflicting outcome/identity | `Patch unconfirmed` | `File changes · unconfirmed` |

The marker uses `file` for a single file. Failed, declined and unconfirmed groups
show known paths and diagnostics without presenting candidate hunks as completed
edits. Failure does not establish that no partial change occurred. A completed
group describes archived evidence; it does not check today's filesystem.

Executable `exec` inputs over 20 lines or 2,000 bytes appear as `exec · input`
markers under the owning Codex turn. Their `Tool input` detail retains the full
original code without executing it. Correlated results use the compact label
`exec · output`. A successful direct patch result associated with a completed
group appears as `apply_patch · output`, avoiding repeated success text while
retaining the full `Tool result` body. Short executable inputs keep their usual
inline presentation.

These details use the existing navigation: `esc`/`q` returns, `c` copies the
current body, `/` and `ctrl+f` search the complete hidden content, and `v` opens
the containing raw transcript. When find is active, the first `esc` clears it.
Global search lands on the owning Codex turn when its input markers share the
same source line. Both default and glamour builds show code and diffs without
Markdown interpretation. Existing archives gain this presentation when reopened:
no import or reindex is needed. Plain-text/Markdown exports, global search
indexing, and archived bytes are unchanged.

#### Find within an open transcript

Press **`/`** in the viewer to find a case-sensitive literal passage. Typing previews a match; `enter` commits it, then `n`/`N` visits every occurrence, including overlaps, and wraps at either end. The counter shows the selected occurrence, whose text is enclosed in ASCII brackets. Spaces and punctuation are literal. Queries are single-line and limited to 256 Unicode code points.

Press **`ctrl+f`** for a fuzzy line picker. It matches characters in order, ignoring case (`ndl` can match `needle`); spaces still count. Empty input browses nonempty content lines. Use Up/Down or `ctrl+p`/`ctrl+n` to select, PageUp/PageDown to page, and `enter` to open the selected passage. Each result shows its role and content-line location; `>` marks the selected row. Accepting a fuzzy result replaces exact find, so `n`/`N` return to marker navigation.

Both searches include full collapsed tool bodies, summaries and reconstructed diffs. A hidden hit opens its detail at the match. Each manually opened sidecar, child session or tool detail has its own scope; a parent search does not scan unopened transcripts. Search uses the parsed transcript: raw-only metadata and text omitted by the existing display policy are outside its scope. **No reindex is needed.**

| Key / action | While finding in the viewer |
| --- | --- |
| `esc` in the editor or picker | Cancel and restore the previous query, target and reading position |
| `esc` after accepting | Clear search and restore normal rendering at the containing message; a hidden detail stays open |
| A later `esc`, or `q` immediately | Go back; manually opened details/children restore their parent's search, while a search-opened detail returns with its owner's query cleared |
| `]` / `[` or Tab / Shift+Tab | Navigate markers outside the editor/picker, even with exact find active |
| Resize, raw view and return | Preserve the selected occurrence; returning from a manually opened detail also restores the parent's reading position |

Active search uses **“find · plain text”** presentation in both builds, including when normal viewing uses glamour. Clearing restores normal rendering. While either query input is open, printable action keys type into the query; `ctrl+c` still quits. Search runs in memory and never writes, indexes or saves queries or excerpts.

Press **`v`** in the session list or viewer, outside query editing, to inspect the **archived raw JSONL** in a read-only view. Each record is pretty-printed, including metadata and unknown fields. In a Claude subagent view, `v` opens that subagent's transcript; in a Codex child session, it opens the child's transcript. From an expanded tool result, it opens the containing transcript, including a sidecar when the tool belongs to one. From global search, press `enter` to open a result, then `v`. Scroll with `j`/`k`, pan long lines with `h`/`l` (or arrow keys), use `0` to return to the left edge and `g`/`G` for top/bottom. `esc`, `q`, or `v` returns to the previous screen with its selection and reading position preserved. Resizing preserves an active search occurrence; normal viewing restores the containing source message. Malformed records remain visible with a source-line diagnostic; terminal control characters are displayed as escapes. This shows the vault's archived copy, so it works even when the original session file is gone.

Markdown in user/assistant turns is word-wrapped by default. For styled rendering (headings, lists, code blocks via [glamour](https://github.com/charmbracelet/glamour)), build with the optional `glamour` tag — `make build-glamour`. It is off by default so the default binary stays small and adds no extra dependency to the release build.

### Restore and resume

`restore` writes a session's main JSONL and every preserved sidecar (subagent transcripts, tool-results) back under the Claude Code projects directory so Claude Code can find it again, or to `--output <dir>`. A Codex session is written at its original relative path under `$CODEX_HOME` instead (see [Codex sessions](#codex-sessions)). Existing files are never clobbered without confirmation, and unsafe paths (absolute or `..`-escaping sidecars) are skipped.

`resume` does the same, then launches `claude --resume <uuid>`. The working directory is chosen from `--dir`, then the session's recorded project path, then the current directory. For a Codex session it stops before restoring anything and prints the manual steps instead.

### Naming sessions

Every archived session carries an **imported title** — Claude Code's own `ai-title` when it recorded one, otherwise the first significant user prompt. It is often good enough, and often not. `capy vault rename` puts your own name on a session without touching the archive:

```bash
capy vault rename 3f8a1c2b "Rate limiter — token bucket rewrite"
capy vault rename 3f8a1c2b --clear        # back to the imported title
capy vault list --name "rate limiter"     # find it again (case-insensitive substring)
```

How names behave:

- **Precedence.** A custom name wins over the imported title everywhere a title is shown — `list`, `show`, `search` rows, `--json` output (the `title` field), MCP session results, and the TUI. Clearing removes the override and reveals the *current* imported title: if the transcript grew and its title changed after you renamed it, that newer title is what comes back.
- **Stored beside the archive, never in it.** Names live in their own table keyed by session UUID. `raw_jsonl`, sidecars, content hashes, and the search index are byte-for-byte unchanged by a rename, and a name survives re-import, `reindex`, `compact`, `rekey`, and `merge`. Deleting the session deletes its name.
- **Not propagated to Claude Code.** Renaming in the vault does not rename the session inside Claude Code, and Claude Code's own `/rename` is not imported as a vault name. The two are independent.
- **Validation.** Names are trimmed, must be non-empty valid UTF-8 with no control characters, and are capped at 120 characters. Duplicate names are allowed — the UUID stays the identity. Because names come back through CLI and MCP output, they pass through the same secret stripping as search snippets: a name that itself looks like a credential is stored **redacted**, not verbatim.
- **Lookup, not search.** `list --name` matches the displayed title literally — `%`, `_`, quotes, and FTS operators are ordinary characters — with Unicode-aware case folding (`café` matches `Café`). `search` still matches transcript text only; a word that appears only in a name is not a search hit.
- **TUI.** Press `e` in the list or the viewer (`ctrl+e` in search, where `e` types into the query) to edit the name in place; an emptied field clears it. The `f` filter matches names too.

### Assigning a session project

```bash
capy vault project 3f8a1c2b "capy"
capy vault list --project "capy"    # match the assigned project name
capy vault search "timeout" --project "capy" # search transcripts in that project
capy vault list --project "capy" --tui # retain project scope in browsing/live search
capy vault project 3f8a1c2b --clear   # reveal the latest imported project path
```

Labels follow the same trim, secret-redaction and 120-code-point validation as
session names. Duplicate labels are allowed. A path-looking label stays literal;
restore and resume continue using the imported path. Each edit affects one UUID,
independently of its title, parent and children. Clearing retains a tombstone.
The command requires `CAPY_VAULT_KEY` and rejects `--tui`.

`vault list --project` and `vault search --project` (including `--tui`) match the effective
project: the custom label when set, otherwise the imported path. An override replaces the path for this filter.
Matching is a literal substring with SQLite's ASCII case-insensitivity; `%`, `_`,
backslash and `*` are ordinary characters. In listing, the filter composes with
`--name`, `--platform` and `--include-children` before `--limit`. In transcript
search, it runs before rank selection and `--limit`, alongside role/date filters.
Project labels are display/filter metadata; their words do not become transcript
matches, and setting a label needs no reindex.

CLI list/search rows, show headers, ambiguity candidates and delete previews
display the effective project. Custom labels stay literal, including labels equal to the
imported path; fallback paths may be shortened to `~/…`. Show and delete details
include the original path separately when it differs. `list --json` and
`search --json` add `project` while retaining the original `project_path`;
`show --format json` remains the verbatim archived JSONL.

TUI list/search rows and the viewer also display effective projects. The viewer
shows the original path on a separate row when different. The local `f` finder
uses Unicode-folded substrings across title, effective project and UUID within
the launch scope; a replaced path is not an additional project alias.

`capy_search` and `capy_vault_search` accept an explicit `project` substring against the effective
project. Omitting `project` or passing an empty string scopes by the current
directory in the **imported path**, so assigning a label keeps the session in its
original directory's default search. A label alone does not associate a session
with another checkout. `all_projects: true` overrides the selector; exact
`project: "*"` also searches all projects and cannot select a star-only label.
Chunk filters apply before retrieval limits, and MCP session hits display the
effective project without shortening custom labels.

Federated `capy_search` checks for archived sessions in the same project scope
before returning empty-knowledge-base guidance, so explicit labels also work
when no knowledge has been indexed. An availability-check failure is reported
in-band and search still runs; successful knowledge results remain usable.
Project selectors affect only session results, and an explicit `source` filter
keeps the search knowledge-only.

`vault stats` groups sessions by their effective project and prints group names
literally. Different imported paths with the same label share one group; clearing
an assignment returns that session to its imported-path group. Grouping uses exact
strings, so `capy` and `Capy` stay separate even though a project filter can match
both. Groups are ordered by count descending, then project value. Children count
once like every other session, and platform totals are unchanged.
`stats --json` preserves `projects` rows with `project_path`/`count` and adds
`project_groups` rows with `project`/`count`; both are empty arrays in an empty vault.

Project assignments and clear tombstones survive transcript growth, re-import,
`reindex`, `compact`, backup-API `rekey`, and cross-vault merge, independently of
session titles. Clearing reveals the latest imported path, including a new path
from a replacement transcript. Deleting a session removes its assignment too.
Set/clear never changes archived JSONL, sidecars, hashes, sizes or either search
index; restore locations and resume working directories remain physical.

For example, assigning `capy` to a session imported from `/tmp/worktree` produces
`"project": "capy"` alongside `"project_path": "/tmp/worktree"` in list/search
JSON. Its stats contribution is `{"project_path":"/tmp/worktree","count":1}`
under `projects` and `{"project":"capy","count":1}` under `project_groups`.

In the TUI, press `ctrl+g` in the list, active list filter, search, or viewer to
edit the selected session's project. The editor starts with only its custom
label (blank when unset or cleared) and shows the original path separately.
Enter saves, whitespace-only input clears, and Esc cancels. Invalid input or a
failed write keeps your text for correction; a failure to refresh after a
successful save is reported separately. The list and search retain their active
scopes, so reassignment can remove a session from the current results. Editing a
child affects only that child; the viewer keeps its transcript and navigation
position. `e`/`ctrl+e` continue to edit titles.

### Cross-machine sync

The vault is local-only — there is no cloud sync. Two ways to move sessions between machines:

**Merge (non-destructive, preferred).** `capy vault merge --from <path>` unites another vault into this one without overwriting — distinct sessions are added, and where both hold the same UUID the larger-content copy wins. Re-running is idempotent.

Custom names travel on their own track. For each session both vaults hold, the most recent rename or clear wins — by timestamp, then machine ID, then a deterministic value tie-break — regardless of which transcript copy won, so a name set on machine A reaches machine B even when both already hold identical transcripts. A vault written by a capy version without names contributes none. An older capy merging *from* a newer vault leaves the destination's names untouched and carries none across until it is upgraded and the merge is re-run.

Project assignments use the same ordering on a separate track: a title edit on one
machine and a project edit on another both survive. Clears travel as tombstones.
Metadata can update even when a source transcript is identical, smaller, or an
excluded empty shell with an existing destination session. Both fields commit
together, and a session with either change counts as one update. Reports and
`--dry-run` display the winning destination project, keeping custom labels literal.

`merge --project <substring>` selects the **source's effective project** (custom
label, otherwise imported path), using literal, ASCII case-insensitive matching.
This replaces the old location-hint-or-path selector: a mangled Claude directory
or Codex rollout-location substring alone no longer matches, even for legacy
sources. `import --project` retains physical discovery filtering. Legacy vaults
without assignments contribute no project edits and cannot clear destination
labels. Upgrade all merging machines and rerun merge to carry project metadata
omitted by older binaries.

```bash
# Copy machine A's vault somewhere on machine B, then:
capy vault checkpoint                                  # on A first — flush its WAL
# (the source must be WRITABLE: merge checkpoints the source's WAL before reading)
CAPY_VAULT_MERGE_KEY='<A's key>' capy vault merge --from /path/to/A-vault.db
# --dry-run to preview, --project <substr> to scope. Source key falls back to
# CAPY_VAULT_KEY when both machines share a passphrase.
```

**Copy the file (destructive).** Replacing `vault.db` wholesale is simpler but overwrites the destination:

```bash
# On machine A
capy vault checkpoint                 # flush the WAL into vault.db (required!)
scp ~/.local/share/capy/vault.db  B:~/.local/share/capy/vault.db

# On machine B — must export the SAME CAPY_VAULT_KEY
capy vault import                     # re-archive B's own local sessions alongside A's
```

> **Copying `vault.db` replaces machine B's vault entirely.** If B already had archived sessions that no longer exist on disk, copy B's `vault.db` elsewhere first (or use `merge`, which never overwrites). When `import` opens a vault whose sessions all come from another machine, it prints a machine-ID mismatch warning to guard against silently overwriting unarchived local sessions.

Machine identity is resolved from `CAPY_MACHINE_ID`, then `~/.config/capy/machine-id` (auto-generated), so it survives DB copies — each machine tags its own imports.

### Key rotation

`capy vault rekey` re-encrypts `vault.db` under a new key without a decrypt-and-reimport cycle. Export the **new** passphrase as `CAPY_VAULT_KEY`, then run `rekey` and enter the **current (old)** passphrase when prompted. The vault is copied into a fresh database encrypted with the new key. (Unlike `capy encrypt`, `rekey` requires the new key in `CAPY_VAULT_KEY` and refuses a new key identical to the old.)

```bash
export CAPY_VAULT_KEY="<new passphrase>"
capy vault rekey                     # enter the OLD passphrase when prompted
```

> **Stop the MCP server first.** Rotation finishes by renaming files into place, which SQLite's locking does not mediate — a still-attached server could keep writing to the old file and lose those writes. There is no reliable busy check for this (the old-key checkpoint inside `rekey` is best-effort only); stopping the server is your responsibility.

> **The `<vault>.bak` left behind is still decryptable by the OLD key.** When rotating a *compromised* key, pass `--remove-backup` to delete it once the new vault verifies open. Deletion is **not** guaranteed erasure — on SSD and copy-on-write filesystems, recoverable copies may remain; true erasure depends on your disk and filesystem.

### Storage and limits

- **Location:** `$XDG_DATA_HOME/capy/vault.db` (default `~/.local/share/capy/vault.db`). Override per-invocation with `--path`, or environment-wide with `CAPY_VAULT_PATH`.
- **Encrypted at rest** with `CAPY_VAULT_KEY` (sqlite3mc / SQLCipher-compatible, same as the knowledge store). A different key cannot open the DB.
- **Archives forever** — no TTL, no automatic cleanup. Reclaim space with `capy vault delete`. Expect ~50 MB/month for an active user; `stats` shows current size.
- **Verbatim, not redacted.** `vault.db` concentrates every secret/credential/PII that appeared in any archived session, and `restore` writes them back as plaintext. This mirrors data that already lives unencrypted under `~/.claude/projects/` and `~/.codex/sessions/` on the same host — but treat `vault.db` and its key accordingly. (Search snippets _are_ secret-stripped; the stored blobs are not.) A redacted-export pipeline is deferred to a future version.

### Environment variables

| Variable            | Purpose                                                                           |
| ------------------- | --------------------------------------------------------------------------------- |
| `CAPY_VAULT_KEY`       | **Required.** Encryption passphrase for `vault.db` (separate from `CAPY_DB_KEY`).                                       |
| `CAPY_VAULT_PATH`      | Override the vault database path (the `--path` flag takes precedence per-invocation).                                  |
| `CAPY_VAULT_SWEEP_ALL` | When set (any non-empty value), the MCP server startup sweep archives **all** projects, not just the current one.       |
| `CAPY_VAULT_MERGE_KEY` | Default source-vault passphrase for `capy vault merge` (overridden by `--key`, falls back to `CAPY_VAULT_KEY`).         |
| `CAPY_VAULT_NO_COMPRESS` | When set, store new blobs uncompressed (`encoding='raw'`); `capy vault compact` refuses to run. For debugging/benchmarking. |
| `CAPY_MACHINE_ID`      | Stable machine identity (useful in Docker/CI).                                                                         |
| `CLAUDE_CONFIG_DIR`    | Non-default Claude Code config dir; vault discovery and restore honor it.                                              |
| `CODEX_HOME`           | Non-default Codex home (default `~/.codex`); vault discovery, the startup sweep and restore honor it.                  |

## CLI Commands

| Command                | Description                                                                               |
| ---------------------- | ----------------------------------------------------------------------------------------- |
| `capy` or `capy serve` | Start the MCP server (stdio transport)                                                    |
| `capy setup`           | Configure capy for the current project (`--platform codex` for Codex CLI)                 |
| `capy setup --db-repo` | Configure a repo that *holds* knowledge DBs (installs a pure-`sh` commit guard hook only)  |
| `capy doctor`          | Run diagnostics on the installation                                                       |
| `capy which`           | Print the knowledge base path for the current project                                     |
| `capy cleanup`         | Remove stale knowledge base entries                                                       |
| `capy index <path>`    | Index a file or directory as durable knowledge using the selected project's database       |
| `capy search <query...>` | Search knowledge with source, chunk-type, kind and result-limit filters                    |
| `capy sweep`           | Index past sessions (dry-run by default, `--force` to index, `--reindex` to re-parse all) |
| `capy checkpoint`      | Flush WAL into main DB file for safe git commits                                          |
| `capy encrypt`         | Encrypt the knowledge DB or rotate its encryption key                                     |
| `capy dbsize`          | Show knowledge DB disk usage                                                              |
| `capy vault <cmd>`     | Archive, search, restore, and resume past sessions — see [Session Vault](#session-vault)  |
| `capy hook <event> [--platform claude-code\|codex]` | Handle a hook event; generated commands select Codex explicitly, default is Claude Code |

Global flags: `--project-dir`, `--version`

Cleanup flags: `--dry-run` (default true), `--force`, `--kind` (`ephemeral`/`session`), `--source <label>`, `--vacuum` (reclaim freelist pages), `--optimize` (rebuild FTS indexes + VACUUM to reclaim FTS bloat that `--vacuum` alone cannot — see [ADR-029](docs/adr/029-fts-tombstone-bloat-reclamation.md))

### Knowledge indexing

`capy index <path>` writes the same durable, file-backed sources as the
`capy_index` MCP tool. Relative inputs resolve against the selected project;
absolute external inputs still use that project's database, credentials and
Read policy. Content is available immediately to an MCP server using the same
project and key. Each invocation closes and checkpoints its store before exit.

```bash
capy index README.md
capy index docs --source project-docs --include '**/*.md'
capy index src --extensions go,md --exclude '**/*_test.go' --max-files 500
capy index docs --project-dir /path/to/project --follow-symlinks
```

Directory flags match the [MCP ingestion controls](#knowledge): `--max-depth`
(0–20, default 5), `--max-files` (1–1,000, default 200), `--extensions`,
repeatable `--include` and `--exclude`, `--respect-gitignore` (default true;
disable with `--respect-gitignore=false`), and `--follow-symlinks` (default false).
Extensions accept comma-separated or repeated values; empty uses the standard
extension list. Quote glob patterns so your shell passes them unchanged.
Include/exclude flags preserve commas as literal filename characters.

`--source` names a single file's source or prefixes directory labels such as
`project-docs:guide/setup.md`; omission uses absolute paths. Repeating a label
with unchanged content deduplicates. Changing prefixes creates separate sources;
removed or excluded files require explicit cleanup. Recursive filters apply
only to directories; explicitly named files keep their direct-path behavior.

Exit **0** means success, including empty/all-filtered/all-denied scans and
scans stopped by a limit; the summary marks those limits as **incomplete**.
Exit **1** means invalid input, credential/policy/storage failure, or an
operational failure/cancellation. A directory **partial error** retains files
already committed. SIGINT and SIGTERM request cancellation and store cleanup;
the summary reports retained progress. Output and close failures also return 1.

### Knowledge search

`capy search <query...>` searches the selected project's encrypted knowledge
store, including content indexed through MCP. It uses the same project and
[credential selection](#project-credentials) as `capy index`; positional query
arguments are joined with spaces.

```bash
capy search connection pooling
capy search timeout --source 'project-docs:' --limit 10
capy search handler --type code
capy search build --include-kind durable --include-kind ephemeral
capy search migrations --project-dir /path/to/project
```

The default kind is `durable`. Repeat `--include-kind` with `durable` and/or
`ephemeral`; supplied kinds replace the default. A nonempty `--source` matches
a literal label substring (including literal `%`, `_` and backslash) and bypasses
kind filtering. `--type code|prose` selects stored chunk classifications;
omission searches both. This type option is CLI-only ([ADR-012](docs/adr/012-contenttype-internal-only.md)).
`--limit` accepts 1–100 results, default 3. The normal retrieval ranking and
per-source diversification apply, without the MCP call throttle.

Each result prints its source, title and a snippet. Snippets show at most 1,500
UTF-8 bytes from the start of the chunk, including an ellipsis when shortened;
displayed source/title text is also bounded. Known secret patterns are redacted
before truncation and terminal controls are removed. No results is a successful
exit **0**. Invalid arguments, credential/policy/storage errors, cancellation,
output failures and close failures return **1**.

Each invocation runs the existing bounded stale-file pass using the selected
project's Read policy, and closes/checkpoints its store. Persisted scheduling
lets later invocations check later files. Denied, failed or deferred refreshes
retain cached searchable content; a result does not promise every backing file
was just validated. This command searches knowledge only; use `capy vault search`
for archived sessions.

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

Boolean arguments accept native JSON booleans or whitespace-trimmed,
case-insensitive `"true"`/`"false"` strings. Omission preserves each tool's default:
`dry_run` defaults to true; `background`, `force`, cleanup action flags and
`all_projects` default to false. Null, numbers, arrays, objects and other strings
are errors before work begins.

Compatibility: string booleans that cleanup, execution and fetch previously
ignored now take effect. In particular, **`dry_run: "false"` performs eviction**
when an eviction is requested. Search no longer accepts numeric `all_projects`
values or shortcuts such as `"1"` or `"t"`; use native booleans instead.

### Execution

| Tool                 | What It Does                                                                                                                                                                                                  |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `capy_execute`       | Run code in a sandboxed subprocess. Supports 11 languages: JavaScript, TypeScript, Python, Shell, Ruby, Go, Rust, PHP, Perl, R, Elixir. Only stdout enters context. Pass `intent` to auto-index large output. |
| `capy_execute_file`  | Inject a file into a sandbox variable (`FILE_CONTENT`) and process it with code you write. The raw file never enters context — only your printed summary does.                                                |
| `capy_batch_execute` | The primary research tool. Runs multiple shell commands, auto-indexes all output as markdown, and searches with multiple queries — all in ONE call.                                                           |

Every runtime defaults to the selected project directory. Set `cwd` on
`capy_execute`, `capy_execute_file` or `capy_batch_execute` to use another existing
directory for that call. Relative values, such as `packages/api`, resolve from
the selected project; absolute external directories are also accepted. A batch
uses one cwd for all commands, including parallel workers. Omit `cwd` for the
default; supplied empty strings, nulls, non-string values and invalid directories
return errors before execution.

Relative code reads and writes use the execution directory. Scripts and `TMPDIR`
remain in a per-call temporary workspace; Rust compiles there, then runs its
binary from the execution directory. The override leaves storage, credentials,
security policy and vault scope tied to the selected project. Relative
`capy_execute_file(path)` inputs still resolve from the project, independently
of `cwd`.

Compatibility: non-shell snippets previously ran from the temporary workspace.
Use an explicit temporary path for disposable outputs. Go snippets now discover
the execution directory's module/workspace, local imports, `replace` directives, automatic
vendor mode and toolchain requirements. Inherited `GOFLAGS` is still stripped.
Elixir's existing Mix build-path discovery now runs from the same directory it
inspects.

Batch commands run as submitted, including multiline commands and heredocs.
Each indexed command section presents captured stdout followed by stderr, with a
separating newline when needed; it does not preserve stream interleaving. Serial
batches share a timeout budget and skip remaining commands after a timeout.
Parallel batches give each command its own timeout and retain command order.

Batch `queries` are optional: omit them or pass `[]` to execute and index without
searching. Supplied queries must be an array of strings (serialized JSON arrays
also work); malformed values fail before execution. `query_scope: "batch"` is
the default and searches the exact batch source. Choose `query_scope: "global"`
to search durable and ephemeral knowledge in the selected database, including
the new batch. Global batch search does not query vault sessions. Every hit names
its source, and the response includes an exact source selector for follow-up.

Each indexed command section stores a sanitized, whitespace-normalized command
preview of at most 500 UTF-8 bytes; skipped commands are marked not executed.
Previews are searchable later without the vault. Complete admitted output stays
indexed, subject to the existing executor capture and store source-size limits;
the added provenance counts toward source admission. Unchanged batches report
their retained section count. The complete serialized MCP result is capped at
81,920 bytes, including JSON escaping. Command inventories use at most 4,096
bytes; section inventories show at most 40 entries with 160-byte titles within
8,192 bytes. Omitted counts identify what the response leaves out. Sandbox byte
statistics count captured stdout/stderr; indexing statistics count the complete
payload offered to the store, including provenance.

Batch source labels still derive from truncated command labels. Concurrent
batches with identical resulting labels can replace each other's content; exact
batch scope does not guarantee isolation between those requests.

With `capy_execute(background: true)`, a process that outlasts its timeout returns
its PID and partial output while continuing to run. Later stdout/stderr are drained
and discarded, so they no longer accumulate in server memory. Write logs to an
explicit file if you need them after detachment. Temporary scripts and `TMPDIR`
remain available until the process exits, when they are removed. Server shutdown
sends SIGTERM to tracked background process groups.

### Knowledge

| Tool                   | What It Does                                                                                                                                                                                                                     |
| ---------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `capy_index`           | Index text, markdown, a file, or a directory into the FTS5 knowledge base. Directory scans create one source per file and report their limits. Stored as `durable` (persists across sessions). |
| `capy_search`          | Search indexed content. Multi-layer search: Porter stemming + trigram substring + fuzzy Levenshtein, fused with Reciprocal Rank Fusion. Defaults to durable + session sources; pass `include_kinds` to search ephemeral content. |
| `capy_fetch_and_index` | Fetch a URL, convert HTML to markdown, index into the knowledge base, return a ~3 KB preview. Default ephemeral (24h TTL). Git platform issue/PR URLs are blocked with CLI redirect guidance.                                    |

Fetch cache freshness uses the last successful remote validation, persisted in
milliseconds. An expired or forced fetch with identical content reports
“revalidated, unchanged” with the retained section count and renews freshness.
Omitting `ttl` uses the configured `store.cache.fetch_ttl_hours` window. Cache
hits and searches do not renew it.
Existing cache entries revalidate once after upgrade because they lack this
timestamp. Failed fetches, conversions and indexing leave the previous entry
unchanged; HTML conversion errors are reported rather than cached as raw HTML.

Freshness is separate from retention: unchanged validation does not reset the
source's indexing age. An old ephemeral entry can still be removed by its
retention TTL; use `kind: "durable"` for persistent reference material. Older
binaries can read the additive metadata schema, but their replacements clear
the validation marker and require another successful fetch after upgrading again.

Set `ttl: 500` for a 500 ms freshness window, or `ttl: 0` to bypass the cache.
`force: true` also bypasses it. The top-level value applies to every URL in a
batch, including serialized `requests` arrays, and is never persisted. Native
integral numbers and trimmed decimal digit strings such as `"500"` are accepted,
from 0 through 9,223,372,036,854 ms. Null, negative, fractional, nonfinite and
overflowing values fail before cache, network or indexing work. String signs,
decimal points and exponent notation are rejected. A 500 ms entry is expired
at exactly 500 ms. Responses identify effective freshness separately from the
configured ephemeral retention period or durable retention scoring.

Temporary DNS failures, network timeouts while the request is still active,
and unreachable-network errors may suggest one retry of the same capy call.
Capy does not retry automatically. Cancellation, SSRF blocks, missing DNS names,
certificate/permission failures and HTTP/body/index errors do not receive that
hint; the original cause remains visible.

`capy_stats` reports cache hits, attempted misses and their hit rate, including
failed fetches and `force`/zero-TTL bypasses. Malformed URLs, SSRF blocks,
Git-platform redirects and cancellation before dispatch count as neither.
With no eligible outcomes the rate is `n/a`; all misses show `0.0%`. The report
shows configured **Default fetch freshness**, independent of session uptime
and individual TTL overrides. Cache byte savings remain an estimate. Context
savings use one decimal and stay at most `99.9%` whenever any bytes were returned.
`purge_all` resets these counters while preserving session uptime.

The `capy_search` `source` filter matches a literal substring of a knowledge
label: `%`, `_`, and backslashes match themselves. For example,
`source: "team_notes"` matches `docs:team_notes` but not `docs:teamXnotes`. An explicit
source includes matching ephemeral entries and skips vault search. Selectors
that previously relied on SQL wildcards must use a literal label fragment.

New or changed plaintext sources use chunks of at most 4,096 bytes, including
malformed JSON that falls back to plaintext. Splits preserve non-whitespace
content and valid UTF-8. Markdown and structured JSON chunks can still exceed
this limit. Reindexing identical content keeps its existing chunks. To repair
an older oversized plaintext source, retain its original content, call
`capy_cleanup` with `source: "<exact label>"` and `dry_run: false`, then call
`capy_index` again with the original content or path and the same source label.

File indexing and automatic stale-file refresh enforce `store.max_source_bytes`
(2 MiB by default) on the raw bytes read, including files that grow after the
initial size check. They read at most the limit plus one detection byte and
reject oversized content before sanitization or indexing. An oversized file or
failed content read during refresh is logged; previously indexed content stays
searchable. Only regular files are admitted; a FIFO cannot block the server
waiting for a writer.

To index a directory, call `capy_index` with, for example,
`path: "docs", source: "project-docs", include: ["**/*.md"]`. Relative paths
resolve from the selected capy project. Sources receive labels such as
`project-docs:guide/setup.md`; omitting `source` uses each canonical absolute
file path. The same label and sanitized content deduplicate on reruns. Changing
the prefix creates separate sources; sharing a prefix across roots can replace
equal relative paths. Removed or excluded files remain indexed until explicit
cleanup.

Directory controls are `max_depth` (0–20, default 5; root depth 0), `max_files`
(1–1,000 eligible open/read attempts, default 200), `extensions`, `include`,
`exclude`, `respect_gitignore` (default true), and `follow_symlinks` (default false). Empty extensions use
`md, mdx, txt, json, yaml, yml, toml, ts, tsx, js, jsx, py, rs, go, sh`;
a nonempty list replaces them, case-insensitively with optional leading dots.
Include/exclude patterns match entire root-relative paths using `/`, `*`, `?`
and `**`; `**/` also matches zero directories. Absolute paths, `..` components,
bracket/brace patterns and negation are rejected. Includes cannot override
mandatory exclusions.

Scans always prune `.git`, `.hg`, `.svn`, `.capy`, `.aws`, `.ssh`, `node_modules`,
`vendor`, `dist`, `build`, `.next`, `coverage`, `.venv`, `venv`, `__pycache__`,
`target`, `.cache`, and credential names `.env`, `.env.*`, `*.pem`, `*.key`,
`id_rsa`, `id_ed25519`, `credentials`. Descendant symlinks are skipped by default;
nested Git repositories/worktrees/submodules are always skipped. An explicitly named root may
be a symlink; its requested and canonical paths remain subject to Read denies.
Git ignores come from that root's worktree, including rules for tracked files;
an external root still uses the selected capy project's database, key and Read
policy. Non-Git scans report that ignores were not applied. Git lookup failures
are errors. Direct file inputs retain their existing explicit-path behavior.

Set `follow_symlinks: true` to follow descendant links within the canonical
ingestion root. Aliases and targets must pass mandatory exclusions and Read
policy. Extensions, include/exclude patterns, source labels and Git ignores use
the canonical target-relative path, so an alias cannot rename a filtered target
into eligibility. Git also checks the link entry itself. Cycles and duplicate
targets are skipped; links cannot cross nested Git roots or bypass depth limits.
Dangling or unresolvable links produce a partial error, retaining committed files.
The existing entry, file and byte limits still apply.

Each scan admits at most 20,000 directory entries and reads at most 32 MiB of
file content, counting unchanged/failed reads and oversize probes. A directory
must fit wholly in the remaining entry allowance before its entries are
processed in lexical order; reaching the allowance before proving end-of-directory
reports an entry limit. Partial file content is never indexed. Limits return a
successful **incomplete** result; operational failures and cancellation return a
**partial error**, retaining already committed files. The summary reports counts,
raw bytes, retained sections and at most ten individual failures. An empty or
entirely filtered scan succeeds.

Search checks at most 32 backing files per refresh pass, with a five-second
cooldown per store instance. Check order persists in the database, so restarting
the server does not repeatedly favor the first files. Each pass allows at most
`max(8 MiB, store.max_source_bytes + 1)` raw read bytes and stops admitting files
after 100 ms of file work; an operation already in progress may finish. Searches
can therefore return cached content for files awaiting their turn. Failed checks
also advance through the schedule, while files not attempted stay eligible.

### Utility

| Tool           | What It Does                                                                                                                                                                                                                       |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `capy_stats`   | Session report: bytes saved, context reduction ratio, per-tool breakdown, knowledge base tier distribution.                                                                                                                        |
| `capy_doctor`  | Diagnostics: version, available runtimes, FTS5 status, config, knowledge base status, hook registration, MCP registration, security policies.                                                                                      |
| `capy_cleanup` | Remove evictable knowledge base entries via four paths: oversized source eviction, retention-score eviction (durable), TTL eviction (ephemeral), TTL eviction (session). Pass `purge_ephemeral=true` for a one-shot scratch clear, `optimize=true` to reclaim FTS bloat (rebuild FTS + VACUUM; mirrors `capy cleanup --optimize`), or `vacuum=true` for a plain VACUUM. |

## Upstream sync compatibility notes

This selective sync covers context-mode `f8d4639..0dfbe8d` (the target includes
commits after tag v1.0.169). Existing Claude Code and Codex MCP integrations keep
their encrypted knowledge/vault ownership. After upgrading, run `capy setup`
for the platform you use and restart its MCP server to load the updated routing,
schemas, configuration and Read-policy snapshot.

| Area | Changed behavior and migration |
| --- | --- |
| Shell policies | Every executable element, including substitutions and unquoted-heredoc expansions, is checked. Literal quoted data stays data. Inputs over 1 MiB, 64 scanner frames, 4,096 elements or `8 × input bytes + 4,096` visits fail before execution. Malformed syntax and unsupported ANSI-C/localized heredoc delimiters fail closed; use ordinary quoted delimiters. Denies win; unmatched hook asks retain their old behavior. |
| Execution directories | All runtimes run in the selected project by default; optional `cwd` changes only that call's execution directory. Relative non-shell writes now land there. Use explicit temporary paths for disposable output. Go discovers that directory's module/workspace, replace, vendor and toolchain settings; inherited `GOFLAGS` remains stripped. |
| Read policies | Rules retain settings origins and root/home/source/cwd anchors. Invalid or unsupported rules block file admission. Single-slash denies conservatively match both their source anchor and legacy absolute path; migrate absolute rules to `//`. Allows never inherit that legacy interpretation. |
| Execute-file admission | External paths require one explicit Read allow covering both requested and physical paths; denies still win. `cwd` does not re-anchor `path`, policies, keys, databases or vault scope. Direct absolute `capy_index` inputs retain deny-only admission. |
| Hook routing | Claude main HTTP redirects are enforced. Codex main sessions and children receive discovery/native advice without their own recent suitable success; one success permits one redirect within 60 seconds. Security denies and matched asks still apply. First-call flood prevention and permanent availability are not guaranteed. |
| Batch output | Commands, including heredocs, execute unchanged. Indexed output presents stdout then stderr, without interleaving. Queries are optional; `global` searches durable+ephemeral knowledge only. Stored bounded command previews count toward source admission. The complete serialized result is capped at 81,920 bytes with omission counts and a source selector; admitted stored output is retained. |
| Supplied booleans | Native booleans and trimmed case-insensitive `"true"`/`"false"` work. **`dry_run: "false"` now performs requested eviction.** Null, numbers, collections and other strings fail before work; numeric/shortcut `all_projects` coercion is removed. Omitted defaults stay the same. |
| Source selectors | `%`, `_` and backslash are literal label characters, no longer SQL wildcard syntax. Use a literal label fragment. Explicit sources still include matching ephemeral knowledge and bypass vault federation. |
| Fetch cache | Legacy rows miss once until successfully revalidated. Optional `ttl` uses milliseconds; zero/force bypass. Unchanged validation renews freshness, preserves retention age and reports retained sections. Failed conversion is an error instead of a raw-HTML cache entry. Retry hints are limited to selected typed transient failures; no automatic retry occurs. |
| File ingestion and refresh | Raw reads enforce the source-size limit plus one detection byte. Directory scans have explicit file/depth/entry/byte bounds and complete/incomplete/partial outcomes. Automatic refresh checks at most 32 files per pass with persisted order and byte/time admission limits; returned cached content may await a later check. |
| Plaintext chunks | New/changed plaintext, including malformed-JSON fallback, is capped at 4,096 bytes per chunk. Same-hash sources keep their old chunks; explicitly evict/reindex to repair them. Markdown/structured JSON remain outside this cap. |
| Background processes | Captured output is returned once; subsequent output drains without retention. Write persistent logs to a file. Temporary workspace cleanup follows process exit. |
| Search and statistics | `[search]` configures a shared-server budget with visible limits/reset time; malformed queries, kinds, selectors and limits fail without consuming it. Terminal `capy search` is knowledge-only and unthrottled. Stats show hit/attempted-miss rates and default freshness, replacing uptime-based TTL remaining. |

Knowledge migrations 020/021 add scheduling and validation metadata without
changing ciphertext, existing timestamp formats or source kinds. Downgrading
keeps content readable but loses bounded-refresh/freshness guarantees; old
replacement writes reset new metadata. Vault schemas and credentials are
unchanged. Reverting the binary also restores old cwd, policy and boolean
semantics, so scripts relying on the new behavior need adjustment.

The [verification ledger](docs/feat/done/upstream-sync-v1.0.169/verification.md#task-20-final-verification-and-compatibility)
records tested revisions, commands, benchmarks and platform limits. Deferred
[D1–D5](docs/feat/done/upstream-sync-v1.0.169/upstream-audit.md#deferred-work-with-concrete-next-steps)
remain: per-agent budgets, atomic policy/read handoff, colliding concurrent batch
labels, structured chunk splitting and authoritative child tool discovery.

## Security

capy enforces the same permission rules you already use — but extends them to the MCP sandbox. If you block `sudo` in Claude Code settings, it's also blocked inside `capy_execute`, `capy_execute_file`, and `capy_batch_execute`.

Configured deny rules apply automatically. Even with no permission rules,
shell scanner limits and execute-file project containment apply; external
execute-file inputs need an explicit Read allow as described below.

```json
{
  "permissions": {
    "deny": ["Bash(sudo *)", "Bash(rm -rf /*)", "Read(.env)", "Read(**/.env*)"],
    "allow": ["Bash(git:*)", "Bash(npm:*)"]
  }
}
```

Add to `.claude/settings.local.json` (local), `.claude/settings.json` (project), or `~/.claude/settings.json` (user). Bash patterns use `*` for any text; colon syntax (`git:*`) matches the command with or without arguments.

Chained commands (`&&`, `;`, `|`) are split and checked individually. **deny always wins over allow.**

Read denies protect `capy_index(path)`, `capy_execute_file(path)`, and automatic
refreshes of indexed files. Rules keep their settings origin and apply to
relative, absolute, and symlink-resolved paths:

| Read pattern | Anchor |
| --- | --- |
| `Read(//absolute/path/**)` | Filesystem root |
| `Read(~/private/**)` | Home directory |
| `Read(/private/**)` | Selected project for local/project settings; the settings file's directory for user settings |
| `Read(./private/**)` or `Read(private/**)` | Selected project for MCP file operations |
| `Read(*.env)` | Any basename at any depth under that directory |
| `Read` | Every file read through these paths |

Path patterns support `*` (within one component), `**` (across components), `?`
(one character), and backslash-escaped literals. Explicit anchors keep their
path scope: `Read(./*.env)` only covers that directory. Bracket classes, leading
negation, brace/extglob expressions, and dot components after wildcards are not
supported; invalid rules or malformed settings block file admission with a
diagnostic. This is a supported subset of host permissions, not complete host
policy emulation.

For compatibility, single-slash **denies** also keep their former absolute-path
interpretation and emit a warning. Migrate an intended absolute deny to `//`;
new allows never receive the legacy interpretation. Servers capture policy at
startup, so restart after settings changes. A blocked stale refresh keeps cached
content searchable and logs the skipped read; it does not remove an already
indexed source. These checks protect the explicit path inputs, not all file
access by arbitrary submitted code.

`capy_execute_file(path)` resolves relative paths from the selected project.
Both the requested path and its physical target must be inside that project,
unless one explicit Read allow covers both. To process an external file, add a
matching rule such as `"Read(//absolute/path/**)"` or `"Read(~/reports/**)"` to
`permissions.allow` in your settings and restart the server. Denies always win;
allowing only a project-local symlink does not grant access to its external target.
Missing or unresolvable paths fail before code starts. The executor receives the
checked physical absolute path, including when process cwd differs from the
selected project. `capy_index(path)` retains its explicit absolute-file behavior
subject to Read denies. Concurrent replacement between checking and reading
remains possible; see [D2](docs/feat/done/upstream-sync-v1.0.169/upstream-audit.md#d2-filesystem-replacement-between-policy-checks-and-runtime-reads).

### Sandbox protections

- **Process group isolation** — child processes can't escape cleanup
- **Environment sanitization** — ~50 dangerous env vars stripped (LD_PRELOAD, NODE_OPTIONS, PYTHONSTARTUP, etc.)
- **Output hard cap** — processes killed if stdout+stderr exceeds 100 MB
- **Timeout enforcement** — configurable per-call, default 30s
- **Shell-escape detection** — non-shell languages scanned for embedded shell commands
- **SSRF protection** — `capy_fetch_and_index` blocks requests to localhost, private networks, and cloud metadata endpoints
- **Secret sanitization** — all indexed content is scanned and redacted for API keys, tokens, JWTs, and other credential patterns

## Hook System

Capy installs hooks for Claude Code and Codex CLI. Codex activation requires
the two trust gates described in [setup](#setup-codex-cli). Hooks route supported
calls and enforce capy's additional policies; ordinary command output is not
captured or budgeted by this integration.

### What gets intercepted

Claude main-agent HTTP routing denials remain enforced. Codex main sessions
receive advisory guidance while tool availability is unverified; a successful
`capy_execute` call in that session permits one HTTP routing redirect within
60 seconds. Fetch, batch and other tool successes cannot authorize that redirect.
The observation is consumed before denying; a subsequent attempt returns to
advisory guidance unless a later success renews the evidence. No registration,
trust record or process-liveness check establishes tool availability.

Calls with a nonempty child `agent_id` receive advisory guidance while their
own capy tools are unverified:
discover deferred capy schemas once if the host supports discovery, then use
available tools; otherwise use native tools with bounded extraction output.
An agent type or local definition alone cannot establish tool availability.
Security denies and matched asks still apply before routing and do not consume
evidence. This fallback can permit a Codex main session's or child's first native
HTTP call even when it ultimately has capy tools; the hook never grants permission.

A successful child `capy_execute` or `capy_fetch_and_index` call provides evidence
for one suitable redirect during the next 60 seconds. Bash HTTP requires execute
evidence; WebFetch can use either tool, while Git issue/PR comprehension keeps
native CLI advice. A redirect consumes all alternatives for that child. Failed
or canceled calls do not renew the evidence, so a failed retry leaves native
fallback available. Missing stable identities or failed state access also fall
back to advice; observations do not establish permanent availability.

| Pattern                                           | What happens                                                                                                        |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `curl`/`wget` in Bash                             | Caller-specific HTTP routing above; silent/quiet file downloads remain allowed without consuming evidence          |
| `fetch()`, `requests.get()`, `http.get()` in Bash | Claude main: routing denial; Codex main and children: one redirect with execute evidence, otherwise advisory        |
| `WebFetch` tool (Claude Code)                     | Main: denial with extraction/CLI guidance; children: one suitable observed-tool redirect, otherwise advisory        |
| `Read` tool (Claude Code)                         | One-time advisory: prefer `capy_execute_file` for analysis                                                          |
| `Grep` tool (Claude Code)                         | One-time advisory: prefer `capy_execute` for large searches                                                         |
| `Agent`/`Task` tools (Claude Code)                | Routing block injected into subagent prompt; Bash subagents upgraded to general-purpose                             |
| `capy_fetch_and_index`                            | Git platform issue/PR/MR URLs blocked with platform CLI redirect; gist URLs get soft guidance                       |
| `capy_*` tools (shell)                            | Security policy enforcement on shell code and batch commands                                                        |

Claude hooks select their project before loading policies: `--project-dir` wins over
`CLAUDE_PROJECT_DIR`, followed by project detection from a valid payload `cwd`,
then the process working directory. Invalid explicit/environment selections fail;
invalid payload directories emit a diagnostic and fall back. Settings and
guidance state belong to the selected project. Cwd-relative Read rules use the
validated payload cwd, falling back to the selected project when absent.

Codex normalizes native shell calls, including `exec_command` and code-mode
shell calls, to canonical Bash. Capy's matcher covers Bash and its ten MCP
tools under any server alias. Hosted web/search calls are outside that coverage.
SubagentStart injects child guidance without rewriting launch arguments or types.
Codex uses the supplied session ID, namespaced internally with `codex:`, and
the supplied child ID; it ignores Claude identity/project environment variables.
Generated launchers pass the validated owner directly to the handler. Manual
hooks use `--project-dir`, then validated payload cwd, then process-cwd discovery.

Capy reads additional command policies from `.claude/settings.local.json`,
`.claude/settings.json` and `~/.claude/settings.json`, in that order; Claude
need not be installed. Denies take precedence. A matched ask rule blocks in
Codex and names the exact rule and source file, because this hook cannot request
approval. Unmatched default asks still pass. This does not translate Codex's
native sandbox or permission rules, which continue to apply independently.

Child observations use `.capy/tool-observations.json`; Codex main observations
use a separate `.capy/codex-main-observations.json`, scoped to the selected owner
and namespaced session. Each has its own permanent `.lock` file, a 20-ms lock
wait, and independent 128-entry/64-KiB bounds. Main, child, sibling-session and
different-owner evidence cannot authorize one another. Root Codex SessionEnd
clears its session from both stores independently, retaining siblings and lock
files; child-labelled end events clear neither. Claude retains child-store
cleanup. Missing SessionEnd events are bounded by expiry and storage caps.
These hooks do not open the knowledge or vault databases.

### Input bounds and guidance state

Codex hooks require EOF before handling an event. PreToolUse, PostToolUse,
SessionStart and SubagentStart allow 2 seconds of idle input, 10 seconds total
read time and at most 8 MiB of serialized JSON. SessionEnd allows 100 ms idle,
250 ms total and 64 KiB. Positive reads renew only the idle deadline; a complete
JSON object on an open pipe is still incomplete transport. Over-limit, stalled,
unreadable or unsupported input never reaches policy or state handling.

PreToolUse transport failures exit 2 with diagnostic stderr and empty stdout;
other events return a diagnostic nonzero exit. EOF-complete malformed PreToolUse
JSON retains its exit-0 structured denial. Supported pipes have deadline-capable
reads; regular files have byte limits and deadline checks between reads, which
cannot interrupt blocked filesystem/kernel I/O. Terminal and device stdin,
including `/dev/null`, are unsupported. Budgets begin after owner preflight and
descriptor setup, so a shorter host timeout or stalled startup may win. Claude
input handling is unchanged.

Once-per-session Bash/Read/Grep guidance uses empty, exclusive 0600
`.capy/guidance-<session-component>-<type>.once` markers. Older regular,
non-symlink JSON guidance files remain readable within 4 KiB; a known key still
counts as shown when its value is false. Invalid or unavailable state repeats
the ordinary advisory with a bounded warning. Missing identity creates no state.
This is at-most-once claiming: a process killed after claiming can leave its
optional advisory undelivered. SessionEnd leaves guidance markers alone, and no
automatic migration or retention sweep is added. Restart affected launchers
across binary changes because older JSON writers do not coordinate with markers.

### Platform support

`capy setup` installs both Claude Code and Codex by default; `--platform` selects
one. Automated setup for other platforms is planned.

Codex setup forwards `CAPY_DB_KEY` and `CAPY_VAULT_KEY` already present in its launch environment through `.codex/config.toml`. Forwarding does not load absent credentials from files. Knowledge can use [project credentials](#project-credentials); the vault requires `CAPY_VAULT_KEY` in the actual launch environment. Existing installations must [regenerate wrappers and restart the launcher](#upgrade-and-credential-migration).

The shared router recognizes aliases for other platforms. Aliases alone do not
establish compatible host payloads, responses, discovery or trust; a manual
integration must verify those contracts:

| Platform        | Recognized tool aliases                                                                                |
| --------------- | ------------------------------------------------------------------------------------------------------ |
| Gemini CLI      | `run_shell_command`, `read_file`, `read_many_files`, `grep_search`, `search_file_content`, `web_fetch` |
| OpenCode        | `bash`, `view`, `grep`, `fetch`, `agent`                                                               |
| Cursor          | `mcp_web_fetch`, `mcp_fetch_tool`, `Shell`                                                             |
| VS Code Copilot | `run_in_terminal`                                                                                      |
| Kiro CLI        | `fs_read`, `fs_write`, `execute_bash`                                                                  |

MCP-only clients can register `capy serve` using stdio. Codex users should use
the generated installation above.

### Codex hook recovery

Trusted PreToolUse hooks fail closed when PATH capy is missing or incompatible,
or owner/config validation fails. These failures exit 2
with repair text. Successful policy denials instead exit 0 with denial JSON.
Claude's legacy wrapper behavior on a running binary's failure is unchanged;
MCP failures preserve diagnostic stderr and protocol stdout separation.

A collaborator who trusts committed hooks without installing capy can lose
matched Bash and capy calls. Install/upgrade capy on PATH from an **external
terminal**, repair permissions and rerun `capy setup --platform codex` in the
intended root. Restart Codex to inherit the corrected PATH.
During infrastructure repair, the user can disable the capy-owned entries in
`/hooks`, then review/re-enable them after repair. Do not disable hooks or switch
tools to evade a matched policy denial; address that policy with its owner.

Restore a missing or corrupt live `.codex/capy-owner` from committed configuration;
do not rotate its ID. Each generated binding must match the marker beside it.
The launcher inspects every physical ancestor, including beyond Git boundaries,
and rejects unreadable, malformed, oversized or symlinked configuration instead
of substituting another owner. Even copied IDs in nested directories overlap:
consolidate to one installation or use a non-overlapping checkout.

**Stop affected sessions before removing or moving a loaded installation**, then
restart/reload after repair. Cached commands cannot recover their origin once
all originating artifacts have been deleted. For rollback, remove only capy's
versioned, owner-bound handlers from `.codex/hooks.json`, preserving external
handlers and mixed-group metadata. Keep the marker and compatible launcher while
any hook or MCP binding references them. Before restoring an older binary that
lacks the final protocol, restore its matching configuration from version control
as well. Mixed interim PR formats are unsupported. Leave unrelated
inline/global/plugin hooks and stored data intact.

Missing trust, disabled hooks, host timeouts or unsupported runtimes can prevent
hook execution; registration is not proof of enforcement. Retained toolbox
startup hooks temporarily duplicate capy guidance. Measurements and the separate
[toolbox follow-up](docs/feat/wip/codex-hooks/implementation.md#claude-toolbox-enhancement-pr)
are recorded with the feature; routine output budgets remain separate work.

## Troubleshooting

Run `capy doctor` to diagnose issues, and use `capy_doctor` through MCP to check the actual server. A successful **Knowledge credential** check identifies the selected source; the separate **Knowledge base** check tests access. FTS5 is checked independently in memory, so a wrong key is not an FTS5 failure. CLI doctor does not create a missing database and reports failed checks without treating every diagnostic failure as a nonzero command exit.

| Check                     | Fix                                                                                  |
| ------------------------- | ------------------------------------------------------------------------------------ |
| **FTS5: unavailable**     | The binary wasn't built with `-tags fts5`. Rebuild with `make build`.                |
| **Runtimes: 0/11**        | No language runtimes found in PATH. Install at least `bash` and `python3`.           |
| **Hooks: not registered** | Run `capy setup` in your project directory.                                          |
| **MCP: not registered**   | Run `capy setup`. Check `.mcp.json` exists in project root.                          |
| **MCP: binary not found** | The `capy` binary isn't in PATH. Move it or run `capy setup --binary /path/to/capy`. |
| **Knowledge credential missing** | Configure a private `store.key_file` or supply `CAPY_DB_KEY` in the actual launch environment (see [Encryption](#encryption)). |
| **Config failure** | Fix the reported TOML read, parse, or validation error. Knowledge checks are skipped; no fallback database is selected. |
| **Knowledge base: wrong passphrase** | Check the source/path reported by doctor and restore that database's existing passphrase there. An inherited key cannot override a project declaration. Restart MCP after correction; do not delete the database. |
| **Dotenv rejected** | Move the existing passphrase to `store.key_file` to bypass an application's unsupported dotenv syntax. |
| **Vault disabled after upgrade** | Supply `CAPY_VAULT_KEY` in the actual launch environment and restart the host/MCP; wrappers no longer source `.env`. |

## Acknowledgements

Capy started is a Go reimplementation of [context-mode](https://github.com/mksglu/context-mode), but has since significantly evolved and stands on it's own feet. Capy has many features added beyond the initial port; the core algorithms — FTS5 search with BM25 ranking, three-tier fallback, smart chunking, sandbox architecture — originate from the ported project.

**Why rewrite it?** context-mode (probably) works well, BUT! I wanted to experiment with ideas that are hard to retrofit into the existing context-mode architecture — persistent cross-session knowledge bases, tiered freshness metadata, content deduplication, mandatory encryption, session transcript indexing, and many other stuff. Go gives me a single static binary with no Node.js dependency, which removes an entire class of installation and compatibility issues. I also have a very acute allergy to anything in the JS/TS/Node ecosystem. This is primarily a personal tool, but it's open source in case others find it useful.

### capy vs context-mode

|                         | context-mode (TypeScript)                                                     | capy (Go)                                                                           |
| ----------------------- | ----------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| **Install**             | `npm install` — requires Node.js, native module compilation                   | Single static binary — `brew install` or `curl \| sh`                               |
| **Startup**             | Node.js VM boot + module resolution                                           | Native binary, starts in milliseconds                                               |
| **Memory**              | Node.js baseline (~50-80 MB typical)                                          | Go baseline (~10-20 MB typical)                                                     |
| **SQLite**              | `better-sqlite3` native addon with Bun fallback                               | `mattn/go-sqlite3` via CGO — one driver                                             |
| **Encryption**          | Not supported                                                                 | Mandatory at rest (sqlite3mc, SQLCipher v4)                                         |
| **Knowledge base**      | Originally ephemeral; persistence added later                                 | Persistent per-project from day one                                                 |
| **Content dedup**       | Re-indexes on every call                                                      | SHA-256 content hashing — skips unchanged                                           |
| **Freshness**           | Added via TTL cache                                                           | Tiered retention (hot/warm/cold) + TTL-based lifecycle by source kind               |
| **Source kinds**        | Single type                                                                   | Three kinds: durable, ephemeral, session — distinct lifecycle and search visibility |
| **Session indexing**    | Tracks events across compactions                                              | Indexes past JSONL transcripts into searchable knowledge base                       |
| **Process isolation**   | `child_process.execFileSync`                                                  | Process group isolation (`Setpgid`) — kills entire tree                             |
| **Secret sanitization** | Not supported                                                                 | Regex-based redaction before indexing                                               |
| **Configuration**       | Reads `.claude/settings.json`                                                 | Own config system (TOML, XDG dirs) plus reads `.claude/settings.json` for security  |
| **Platform support**    | Claude Code, Cursor, Kiro, Zed, Pi, OpenClaw, OpenCode, Gemini CLI, Codex CLI | Claude Code, Codex CLI (more planned)                                               |

### What's shared

The search algorithm (FTS5 BM25 with Porter stemming, trigram, and fuzzy Levenshtein correction), sandbox execution model, hook-based routing, chunking strategies, and security policy evaluation all originate from context-mode. capy ports these faithfully, diverging only where Go offers a meaningfully better approach.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, architecture overview, and code conventions.

```bash
git clone https://github.com/serpro69/capy.git
cd capy
export CAPY_DB_KEY=test-key-for-development
export CAPY_VAULT_KEY=test-key
make build && make test
```

## License

Licensed under [Elastic License 2.0](LICENSE.md) (source-available). You can use it, fork it, modify it, and distribute it. Two things you can't do: offer it as a hosted/managed service, or remove the licensing notices.
