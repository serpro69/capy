# Operations and integration reference

The [README](../../README.md) covers installation and routine use. This reference covers permissions, database maintenance, upgrades, and integration details.

## Configuration defaults

Files merge from global `~/.config/capy/config.toml` (or `XDG_CONFIG_HOME`) through project `.capy/config.toml` to `.capy.toml`. Credentials and [worktree ownership](../../README.md#project-credentials) are resolved separately.

| Setting | Default | Meaning |
| --- | --- | --- |
| `store.path` | XDG per-project database | A relative override shares the main worktree's database. |
| `store.key_file` | Unset | Literal private passphrase file; an explicit empty string clears an inherited setting. |
| `store.title_weight` | `2.0` | BM25 title-column weight. |
| `store.max_source_bytes` | `2097152` | Source admission limit; not a database-size ceiling. |
| `store.cleanup.cold_threshold_days` | `30` | Durable retention scoring. |
| `store.cleanup.ephemeral_ttl_hours` | `24` | Ephemeral lifetime; minimum 1. |
| `store.cleanup.session_ttl_days` | `60` | Legacy session sources in the knowledge store; minimum 1. Does not expire vault archives. |
| `store.cleanup.auto_prune` | `false` | Reserved configuration; automatic durable pruning is not implemented. |
| `store.cache.fetch_ttl_hours` | `24` | Default remote freshness, independent of retention. |
| `executor.timeout` | `30` | Seconds per execution. |
| `executor.max_output_bytes` | `102400` | Captured-output limit in bytes. |
| `server.log_level` | `"info"` | Log verbosity. |
| `search.window_ms` | `60000` | Shared MCP search window; 1,000–3,600,000 ms. |
| `search.taper_after` | `3` | Later calls return at most one hit per query. |
| `search.block_after` | `8` | Later calls are blocked until reset. |
| `vault.min_session_bytes` | `0` | Minimum size for new archives; zero disables. |

Search fields merge by presence. Zero, wrong types, and out-of-range values fail; the merged thresholds require `1 <= taper_after < block_after <= 10000`. Restart MCP to load changes. Other fields have their own merge rules; do not assume zero clears an inherited value. See [configuration internals](../architecture.md#configuration).

## Permission rules

Capy reads Claude-format `permissions` from local, project, then user settings, even when used with Codex. For example, merge this object into `.claude/settings.local.json`, `.claude/settings.json`, or `~/.claude/settings.json`:

```json
{
  "permissions": {
    "deny": ["Bash(sudo *)", "Bash(rm -rf /*)", "Read(.env)", "Read(**/.env*)"],
    "allow": ["Bash(git:*)", "Bash(npm:*)"]
  }
}
```

Bash patterns use `*` for arbitrary text. Colon syntax, such as `git:*`, matches the command with or without arguments. Executable elements of chains, substitutions, and unquoted heredocs are checked individually; denies win. MCP command checks enforce denies; hook ask behavior depends on the platform. In Codex, a matched ask blocks with its rule/source because the hook cannot request approval.

| Read pattern | Anchor |
| --- | --- |
| `Read(//absolute/path/**)` | Filesystem root. |
| `Read(~/private/**)` | Home directory. |
| `Read(/private/**)` | Selected project for local/project settings; settings-file directory for user settings. |
| `Read(./private/**)` or `Read(private/**)` | Selected project for MCP file operations; validated rule cwd for hooks. |
| `Read(*.env)` | Any matching basename at any depth beneath that cwd. |
| `Read` | Every file read through the checked paths. |

Read patterns support `*` within a component, `**` across components, `?`, and escaped literals. Explicit `Read(./*.env)` only covers that directory. Bracket classes, leading negation, brace/extglob syntax, and dot components after wildcards are unsupported. Invalid rules/settings block file admission.

Single-slash denies retain both their settings-relative meaning and their old absolute interpretation, with a warning. **Migrate intended absolute rules to `//`.** Allows do not receive this compatibility behavior.

An external `capy_execute_file` path needs one explicit allow covering both requested and physical paths; a project-local symlink alone is insufficient. Denies still win. Explicit absolute `capy_index` inputs use deny-only admission. Restart MCP after policy changes. Denied refreshes keep cached content; use exact-source cleanup to remove an indexed copy.

These checks are not complete host-policy emulation or an OS sandbox. Concurrent path replacement between a check and read remains possible. The [security architecture](../architecture.md#security) records scanner limits, path ownership, and implementation boundaries.

## Knowledge databases in a separate Git repository

Use this recipe when an existing database is already at `.capy/knowledge.db` and the project's `store.path` is that relative path. Check `capy which` first: merely changing the path of an XDG database does not move its contents. Stop all processes using the database before moving it.

From the project root, with its existing credential available:

```bash
capy checkpoint
git init ~/capy-db
mkdir -p ~/capy-db/myproject
mv .capy/knowledge.db ~/capy-db/myproject/knowledge.db
ln -s ~/capy-db/myproject/knowledge.db .capy/knowledge.db
capy setup --db-repo --project-dir ~/capy-db
```

Use a new destination; do not overwrite another database. Keep this project configuration:

```toml
[store]
path = ".capy/knowledge.db"
```

The relative path preserves linked-worktree sharing; the symlink keeps binary history outside the source repository. Setup's `.capy/**` ignore rule keeps the link untracked. SQLite sidecars live beside the real file in `~/capy-db/myproject/`; do not make additional WAL/SHM symlinks. Credential ownership stays with the project.

After checkpointing, commit in the database repository:

```bash
git -C ~/capy-db add myproject/knowledge.db
git -C ~/capy-db commit -m "Update myproject knowledge base"
git -C ~/capy-db push
```

Configure its private remote before pushing. On another machine, clone that repository, recreate the symlink, and provide the same database passphrase. Existing project configuration alone does not recreate an ignored link.

The DB-repository pre-commit guard is a shell script requiring neither capy nor a key. It rejects a staged database with an unencrypted SQLite header, a nonempty WAL, or any SHM file. A zero-byte WAL is tolerated. The hook does not encrypt or checkpoint: repair from the owning project, then stage again.

`capy checkpoint` resolves database symlinks and verifies sidecars beside the real file. It fails if a nonempty WAL or any SHM remains, or inspection fails. Even an idle connection can keep SHM present. From elsewhere, use `capy checkpoint --project-dir <project>`.

**Stop sessions before pulling or replacing a database.** The commit guard cannot protect a pull; replacing a file held open by SQLite can lose writes or corrupt storage. See [checkpoint ordering](../adr/016-wal-mode-and-checkpoint-strategy.md) and [database ownership](../adr/026-worktree-shared-knowledge-db.md).

## Upgrades and compatibility

Run the new binary's setup directly for each installed platform, then restart the agent/MCP. Keep Claude's existing local/shared settings target. Updating only the binary can leave obsolete wrappers and routing in place. The README covers [credential migration and rollback](../../README.md#upgrade-and-credential-migration).

The following caller changes accompanied the selective context-mode sync from `f8d4639` through `0dfbe8d` (including commits after upstream v1.0.169):

| Area | Action or compatibility consequence |
| --- | --- |
| Execution cwd | Relative non-shell writes now land in the project or explicit cwd. Use temporary paths for disposable output; Go sees that directory's module/toolchain settings. |
| Shell and Read policies | Malformed/unsupported syntax fails closed. Use ordinary quoted heredoc delimiters, `//` for absolute Read patterns, and an explicit Read allow for external execute-file paths. |
| Boolean arguments | Strings now take effect: `dry_run: "false"` applies eviction. Numeric/abbreviated truth values are rejected. |
| Search inputs and source selectors | Invalid inputs fail before spending budget. Percent, underscore, and backslash in source selectors are literal rather than SQL wildcards. |
| Batches | Queries are optional; global scope searches knowledge only. Results are bounded, and stdout/stderr are stored sequentially. Identical generated labels can collide. |
| Background output | Output after detachment is discarded. Write persistent logs to a file. |
| Fetch caching | Legacy entries revalidate once. Successful unchanged fetches renew freshness but not retention. Conversion failures keep the old entry and return an error. |
| File refresh | Checks have persisted order and per-pass limits; results may still contain cached content. |
| Plaintext chunks | Same-hash legacy content is not rechunked; use the [explicit repair procedure](tools.md#cleanup-and-older-chunks). |
| Statistics | Reports show cache hit/attempted-miss rate and configured default freshness, not an uptime-based countdown. |

Knowledge migrations 020/021 add scheduling/validation metadata without changing encryption or source kinds. Downgrades keep content readable but lose those freshness/refresh guarantees, and older replacement writes reset new metadata. Reverting also restores old cwd, policy, and boolean behavior; adjust dependent scripts. Vault schemas/credentials were unchanged by this sync.

v0.16.4 ignores `store.key_file` while retaining `store.path`; before downgrading to it, supply the same passphrase through the launch environment. New wrappers paired with that binary do not restore dotenv loading. No database rewrite is needed solely for credential-file rollback.

Verification provenance and unresolved implementation work remain in the [upstream acceptance record](../feat/done/upstream-sync-v1.0.169/verification.md#task-20-final-verification-and-compatibility) and [deferred-work record](../feat/done/upstream-sync-v1.0.169/upstream-audit.md#deferred-work-with-concrete-next-steps). These retain the tested scope and next steps for per-agent budgets, atomic policy/read handoff, colliding batch labels, structured chunk limits, and tool discovery.

## Doctor and integration diagnostics

Normal knowledge access uses the selected project's merged configuration even when the main worktree owns its database. `capy which` resolves the path without reading credentials; checkpointing a missing database is a keyless no-op. Help, setup, version, and hook dispatch do not require a knowledge key. See [credential ownership and connection lifetime](../architecture.md#knowledge-credential-flow).

Direnv can supply keys to the launch environment, but capy does not read `.envrc`. A selected project credential still takes precedence over an inherited key. Credential-file byte limits do not cap environment-only passphrase length. Short knowledge passphrases are accepted with a warning; generated values of at least 32 characters are recommended. Key-file permission checks cover Unix mode bits, not extended ACLs or parent directories. Setup can repair a symlink's regular-file target outside the project, but it skips missing targets and does not create credentials.

Inspect doctor's report rather than treating exit 0 as proof every check passed. Credential selection and database authentication are separate checks, and FTS5 is tested independently in memory. CLI doctor does not create a missing database. MCP doctor can check with its captured key even if the current key file's permissions have become invalid; neither doctor repairs permissions.

Codex installation details remain in [launch and owner validation](../architecture.md#codex-installation-and-launch), [hook input bounds](../architecture.md#hook-system), [observation lifecycle](../architecture.md#hook-events), and [guidance state](../architecture.md#guidance-system). These cover the protocol version, generated artifacts, failure exit codes, EOF budgets, marker/lock ownership, and shutdown behavior. [Registration diagnostics](../architecture.md#registration-diagnostics-and-verification) distinguish static checks from live-host evidence and link the verification records.

The shared router also recognizes these aliases for manual integrations. Recognition alone does **not** establish compatible payloads, responses, discovery, or trust, and setup supports only Claude Code and Codex CLI:

| Platform | Recognized aliases |
| --- | --- |
| Gemini CLI | `run_shell_command`, `read_file`, `read_many_files`, `grep_search`, `search_file_content`, `web_fetch` |
| OpenCode | `bash`, `view`, `grep`, `fetch`, `agent` |
| Cursor | `mcp_web_fetch`, `mcp_fetch_tool`, `Shell` |
| VS Code Copilot | `run_in_terminal` |
| Kiro CLI | `fs_read`, `fs_write`, `execute_bash` |

## Shell completions

Homebrew installs completions. For other installations:

```bash
# Add the appropriate line to your shell's startup file:
source <(capy completion bash)
# For zsh:
source <(capy completion zsh)
```

For Fish, run `capy completion fish | source`, or save the generated output to `~/.config/fish/completions/capy.fish`.
