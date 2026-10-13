# Tool and knowledge reference

Use the [README](../../README.md#everyday-use) for the normal workflow. This page covers arguments, limits, and failure behavior for agents and scripts using MCP or the knowledge CLI.

## Argument validation

MCP boolean arguments accept JSON booleans and trimmed, case-insensitive `"true"` / `"false"` strings. Omission preserves defaults: `dry_run` defaults to true; `background`, `force`, cleanup action flags, and `all_projects` default to false. Null, numbers, collections, and other strings fail before work starts.

**String booleans take effect:** `dry_run: "false"` performs requested eviction. Older callers that relied on strings being ignored must use explicit booleans. Numeric and abbreviated `all_projects` values such as `1` or `"t"` are rejected.

`capy_search` rejects malformed arrays, blank queries, non-string selectors, and nonpositive or fractional limits before consuming its shared server budget. Serialized JSON string arrays and positive integral numeric strings remain accepted. Valid empty, partial, and blocked responses count toward the budget and report its admission-time limits/reset. A requested smaller limit is honored. See [search admission and ranking](../architecture.md#tool-execution-capy_execute--capy_batch_execute).

## Execution and background processes

`capy_execute`, `capy_execute_file`, and `capy_batch_execute` run from the selected project unless `cwd` names another existing directory. Relative overrides resolve from the project; absolute external directories are accepted. Supplied empty, null, non-string, or invalid directories fail before execution. A batch uses one cwd for every worker.

Relative code reads and writes use that directory. Script files and `TMPDIR` stay in a temporary workspace; Rust compiles there before running from the requested cwd. Go discovers the execution directory's module/workspace, local imports, replace directives, vendor mode, and toolchain requirements; inherited `GOFLAGS` is stripped. Elixir's Mix discovery uses the same cwd. A relative `capy_execute_file(path)` remains project-relative, independent of cwd, storage, credentials, and policy.

Non-shell snippets no longer default to a disposable directory. Use an explicit temporary path for outputs you do not intend to leave in the project. Per-call MCP `timeout` uses milliseconds; the TOML executor timeout uses seconds.

With `background: true`, a process that outlasts its timeout returns a PID and partial output while continuing to run. Subsequent stdout/stderr are drained and discarded. **Write continuing logs to an explicit file.** Temporary scripts and `TMPDIR` survive until process exit; server shutdown sends SIGTERM to tracked background process groups. The [executor architecture](../architecture.md#executor) describes cleanup and its limits.

## Batches

Commands run as submitted, including multiline commands and heredocs. Indexed sections contain stdout followed by stderr, with separation when needed; they do not preserve stream interleaving. Serial batches share a timeout budget and skip remaining commands after timeout. Parallel commands have independent timeouts, with results retained in input order.

Omit `queries` or supply `[]` to execute and index without searching. Supplied queries must be string arrays; serialized arrays also work.

| `query_scope` | Corpus |
| --- | --- |
| `"batch"` (default) | The exact source label generated for this batch. |
| `"global"` | Durable and ephemeral knowledge in the selected database, including this batch; no vault sessions. |

Every hit names its source, and responses give a source selector for follow-up searches. **Concurrent batches with identical generated labels can replace each other's content.** Batch scope does not make those labels unique.

Each command section stores a sanitized, normalized preview of at most 500 UTF-8 bytes; skipped commands are marked not executed. Previews count toward source-size admission. Unchanged batches report the retained section count. Captured output remains indexed within executor/source limits even when response inventories omit entries.

The complete serialized result, including JSON escaping, is capped at 81,920 bytes. Command inventories have a 4,096-byte budget; section inventories have an 8,192-byte budget with at most 40 entries and 160-byte titles. Omission counts explain missing inventory items. Execution statistics count captured streams; indexing statistics count the payload including command previews. See [batch implementation](../architecture.md#batch-provenance-and-response-bounds).

## File and directory indexing

CLI examples:

```bash
capy index README.md
capy index docs --source project-docs --include '**/*.md'
capy index src --extensions go,md --exclude '**/*_test.go' --max-files 500
capy index docs --project-dir /path/to/project --follow-symlinks
```

The equivalent MCP directory call uses `path: "docs", source: "project-docs", include: ["**/*.md"]`. Inputs resolve from the selected project. External inputs still use that project's database, credentials, and Read policy. CLI writes are available to an MCP server using the same database/key.

| MCP argument | CLI flag | Meaning/default |
| --- | --- | --- |
| `max_depth` | `--max-depth` | 0–20, default 5; root depth is 0. |
| `max_files` | `--max-files` | 1–1,000 eligible open/read attempts, default 200. |
| `extensions` | `--extensions` | Nonempty list replaces defaults; case-insensitive, optional leading dots. |
| `include` / `exclude` | `--include` / `--exclude` | Whole root-relative path patterns; repeatable CLI flags. |
| `respect_gitignore` | `--respect-gitignore` | True; disable with `--respect-gitignore=false`. |
| `follow_symlinks` | `--follow-symlinks` | False; opt-in following stays inside the canonical root. |

Default extensions are `md, mdx, txt, json, yaml, yml, toml, ts, tsx, js, jsx, py, rs, go, sh`. CLI extensions accept comma-separated or repeated values; include/exclude flags retain literal commas. Quote globs in the shell.

Patterns support `/`, `*`, `?`, and `**`; `**/` also matches zero directories. Absolute paths, `..` components, bracket/brace patterns, and negation are rejected. These recursive controls apply to directories, not explicitly named files.

Scans always exclude these directory components:

```text
.git .hg .svn .capy .aws .ssh node_modules vendor dist build .next
coverage .venv venv __pycache__ target .cache
```

Credential names `.env`, `.env.*`, `*.pem`, `*.key`, `id_rsa`, `id_ed25519`, and `credentials` are also excluded. Includes cannot override mandatory exclusions. Nested Git repositories, worktrees, and submodules are always skipped.

Git ignores come from the ingestion root's worktree, including rules matching tracked files. Non-Git scans report that ignores were not applied; Git lookup failures are errors. An explicit root may be a symlink, subject to Read checks on requested and canonical paths. Following descendant links checks aliases and targets against exclusions and policy, uses canonical target-relative paths for selection/labels, and checks both Git link entries and targets. Cycles/duplicates are skipped; links cannot cross nested Git roots or bypass depth limits. Dangling links produce partial errors.

Directory labels are canonical absolute paths or `prefix:relative/path`. Repeating the same label and sanitized content deduplicates. Changing the prefix creates separate sources; sharing one across roots can replace matching relative paths. Removed/excluded files require explicit cleanup.

### Admission and partial progress

Only regular files are read. Raw reads enforce `store.max_source_bytes` (default 2 MiB), including files that grow after the size check, using at most one extra detection byte. Oversized or partial content is not indexed.

Each directory scan admits at most 20,000 entries and reads at most 32 MiB, including failed/unchanged reads and oversize probes. A directory must fit the remaining entry allowance before lexical processing. Summaries report limits, committed progress, and at most ten individual failures.

- Empty, filtered, denied-only, or limit-stopped scans are successful; limits are explicitly **incomplete**.
- Operational failures and cancellation report **partial error**, retaining files already committed.
- CLI indexing exits 0 for success/incomplete scans and 1 for invalid input, credential/policy/storage failures, cancellation, or output/close errors.

Search refresh is bounded: at most 32 backing files per pass, a five-second per-store cooldown, a raw-read budget of `max(8 MiB, store.max_source_bytes + 1)`, and a 100 ms admission window. An already-started operation can finish. Check order persists across restarts. Failed, denied, or deferred refreshes retain cached searchable content; a search result does not certify that its backing file was just checked. See [file ingestion](../architecture.md#file-ingestion) and [refresh scheduling](../architecture.md#bounded-stale-refresh).

## Knowledge search from the terminal

```bash
capy search handler --type code
capy search build --include-kind durable --include-kind ephemeral
capy search timeout --source 'project-docs:' --limit 10
capy search migrations --project-dir /path/to/project
```

Positional words join with spaces. Defaults are durable knowledge only and three results. Repeated `--include-kind` values replace the default and accept durable/ephemeral. `--source` is a literal label substring, including literal percent, underscore, and backslash characters, and bypasses kind filtering. `--type code|prose` is CLI-only. Limits are 1–100.

Output contains bounded source/title text and snippets of at most 1,500 UTF-8 bytes, with an ellipsis when shortened. Recognized secrets are redacted before truncation, and terminal controls are removed. No matches is exit 0; argument, credential, policy, storage, cancellation, output, and close failures are exit 1. The command closes/checkpoints its store and does not use the MCP throttle or vault; use `capy vault search` for conversations.

## Fetch freshness and statistics

Freshness starts at the last successful remote validation. Unchanged successful fetches report “revalidated, unchanged,” renew freshness, and preserve the section count. Hits and searches do not renew it. Failed fetching, conversion, or indexing leaves the previous entry intact; an HTML conversion failure is an error rather than a raw-HTML cache entry.

`ttl` uses milliseconds; omitted uses configured fetch hours, and zero or `force: true` bypasses cache. One top-level TTL applies to all URLs in a batch, including serialized `requests`, without being persisted. Accepted values are integral numbers or trimmed decimal digit strings from 0 through 9,223,372,036,854. Null, negatives, fractions, nonfinite/overflow values, and string signs, decimal points, or exponents fail before work. A 500 ms entry expires at exactly 500 ms.

Freshness does not extend retention. An old ephemeral source can expire after a successful unchanged validation; choose durable storage for lasting references.

Typed temporary DNS failures, uncanceled network timeouts, and unreachable-network errors may suggest one retry. There is no automatic retry. Cancellation, SSRF blocks, missing DNS names, certificate/permission failures, and HTTP/body/index errors do not get that hint.

Statistics count cache hits and attempted misses, including failures and force/zero-TTL bypasses. Malformed URLs, SSRF blocks, platform redirects, and cancellation before dispatch count as neither. Hit rate is `n/a` with no eligible outcomes; displayed default freshness is configuration, not remaining session time. Byte savings are estimates. Percentages use one decimal and remain below 100% if any bytes returned. A successful `purge_all` resets counters while preserving uptime. See [fetch implementation](../architecture.md#fetch-and-index-capy_fetch_and_index).

## Cleanup and older chunks

Normal cleanup previews oversized-source eviction, durable retention-score eviction, and TTL eviction of ephemeral/legacy knowledge-session sources. It never deletes the separate vault's archives.

- `capy cleanup --force` or MCP `dry_run: false` applies requested eviction.
- MCP `purge_ephemeral: true` restricts eviction to **expired** ephemeral sources; it does not clear all recent scratch output. CLI `--kind ephemeral` has the same TTL restriction.
- `source` selects an **exact** label for eviction, unlike search's substring selector.
- MCP `purge_all: true` resets the selected knowledge database when paired with `dry_run: false`.
- `vacuum` reclaims freelist space; `optimize` rebuilds FTS indexes and vacuums to reclaim index bloat.

Standalone `capy cleanup --optimize` / `--vacuum`, or equivalent MCP flags without an eviction mode, run immediately despite the default dry-run value. Combined with eviction, reclamation follows it and is skipped on dry runs. See [reclamation rationale](../adr/029-fts-tombstone-bloat-reclamation.md).

New or changed plaintext sources, including malformed-JSON fallback, use chunks of at most 4,096 bytes. Markdown and structured JSON may exceed that limit. Same-hash content keeps its older chunks. To repair an oversized legacy plaintext source, first retain its original content, evict its exact label using `capy_cleanup` with `dry_run: false`, then index the original content/path under the same label. See [chunk limits](../adr/022-source-size-guard-and-db-bloat-prevention.md).
