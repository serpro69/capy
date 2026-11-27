# Tool and knowledge reference

Use the [README](../../README.md#everyday-use) for the normal workflow. This page covers arguments, limits, and failure behavior for agents and scripts using MCP or the knowledge CLI.

## Argument validation

MCP boolean arguments accept JSON booleans and trimmed, case-insensitive `"true"` / `"false"` strings. Omission preserves defaults: `dry_run` defaults to true; `background`, `force`, cleanup action flags, and `all_projects` default to false. Null, numbers, collections, and other strings fail before work starts.

**String booleans take effect:** `dry_run: "false"` performs requested eviction. Older callers that relied on strings being ignored must use explicit booleans. Numeric and abbreviated `all_projects` values such as `1` or `"t"` are rejected.

`capy_search` rejects malformed arrays, blank queries, non-string selectors, and nonpositive or fractional limits before consuming its shared server budget. Serialized JSON string arrays and positive integral numeric strings remain accepted. Valid empty, partial, and blocked responses count toward the budget and report its admission-time limits/reset. A requested smaller limit is honored. See [search admission and ranking](../architecture.md#tool-execution-capy_execute--capy_batch_execute).

`capy_vault_search` now applies the same array and member-type checks to a supplied `queries` value, before opening the vault or recording usage. A `null` value, a string that is not a JSON array, and any non-string member are rejected even when `query` is also supplied. Unlike `capy_search`, it still accepts a blank query, which reports a miss. A disabled vault reports that it is disabled before `after`/`before` dates are checked. Earlier versions dropped non-string members of a native array, which shifted every later query number; read a serialized `null` member as a blank query; and fell back to `query` when the string did not parse. Native and serialized string arrays still work, and an absent or empty array still falls back to `query`. Invalid `after`/`before` dates are quoted in the error after redaction, shortened to 128 bytes.

## Execution and background processes

`capy_execute`, `capy_execute_file`, and `capy_batch_execute` run from the selected project unless `cwd` names another existing directory. Relative overrides resolve from the project; absolute external directories are accepted. Supplied empty, null, non-string, or invalid directories fail before execution. A batch uses one cwd for every worker.

Relative code reads and writes use that directory. Script files and `TMPDIR` stay in a temporary workspace; Rust compiles there before running from the requested cwd. Go discovers the execution directory's module/workspace, local imports, replace directives, vendor mode, and toolchain requirements; inherited `GOFLAGS` is stripped. Elixir's Mix discovery uses the same cwd. A relative `capy_execute_file(path)` remains project-relative, independent of cwd, storage, credentials, and policy.

Non-shell snippets no longer default to a disposable directory. Use an explicit temporary path for outputs you do not intend to leave in the project. Per-call MCP `timeout` uses milliseconds; the TOML executor timeout uses seconds.

All execution adapters capture into encrypted owner-local run storage before
returning bounded version-1 results. The configured deadline defaults to 300
seconds; `wait_ms` defaults to 25,000 ms and can yield earlier without cancelling
execution. Recover retained output using `capy_runs` or `capy runs`, independently
of the knowledge-search budget. CLI execution stays attached.

With legacy execute-only `background: true`, `timeout` becomes the initial wait
and execution has no deadline; output continues to be captured. It cannot be
combined with `wait_ms`. Server shutdown cancels its jobs; logs survive restart,
jobs do not. [Run operations](runs.md) describe cancellation, paging, raw reads,
retention and the Linux supervisor boundary.

## Batches

Commands run as submitted, including multiline commands and heredocs. The entire
inventory is validated before spawn. Each child has a unique run ID and ordinal;
exact stdout/stderr bytes are stored separately, with merged observation order.
Serial deadlines include queue time. Parallel children have dispatch-time
deadlines and an admission-time cutoff for undispatched work. All share owner
active/pending capacity.

Omit `queries` or supply `[]` to capture without indexing/searching. Supplied
queries must be string arrays; serialized arrays also work.

| `query_scope` | Corpus |
| --- | --- |
| `"batch"` (default) | Available captured output of this batch's child run IDs. |
| `"global"` | Batch run hits plus separately labelled durable/ephemeral knowledge; no vault sessions. |

Run hits carry stream/raw anchors and read cursors. Recover all ordinals with
`capy_runs(action=list,batch_id=...)`, then read each run. A manifest retains child
terminal status independently of log eviction until its own expiry. Duplicate
labels are display metadata and do not replace other runs.

The complete result defaults to 8,192 serialized bytes, including structured
content, fallback text, metadata and escaping. State and recovery IDs remain
available when optional content is omitted. `capy_runs` status/read/search are
successful queries even when the nested command failed. See the
[public contract](../feat/wip/routine-command-capture/contracts.md) for selectors,
error states and bounds.

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

## Keyword search replies

`capy_search` and `capy_vault_search` return one text reply of at most 40,960 bytes of valid UTF-8, counted after JSON decoding. The bound covers headings, entries, references, errors, notices and the `capy_search` budget footer. Argument errors, blocked searches and a disabled vault are bounded too.

### Layout

Each query that runs gets a group headed `## N. <query>`, where N is its position in the input, counting from 1. Below the heading, a group shows one of:

| Outcome | Group status |
| --- | --- |
| Hits returned | No status line; the entries follow. |
| Every pass completed with zero hits | `No results found.` |
| A corpus or FTS layer failed with surviving hits | `⚠ partial results (<corpus>: <cause>)`, then the surviving entries in rank order. |
| A failure left zero hits | `Error: <corpus>: <cause>`. |

Entries appear in rank order; `capy_search` places knowledge before the vault on a federated tie. The first time a reply shows a passage, its entry is numbered `[n]`. A later hit with the same corpus, location, selected window, passage text and attribution appears as `↑ same passage as [n]` and counts as shown. A different window into the same chunk gets its own entry. So does identical text that a knowledge search reached through the other FTS layer, and a hit whose stored location is unknown. Vault passages bracket the query's matched terms, so a later query usually needs the same matched terms in that chunk to cite it; knowledge passages add no brackets. Numbers last for one reply only.

A knowledge entry is a `--- [<source label>] ---` header, a `### <title>` line and the passage. A label that begins with `session:` is shown as `--- [session:…] (knowledge) ---`, keeping it distinct from vault hits. A vault entry is a `--- [session:<uuid>] ---` header, a `### <title>` line, a metadata line and the passage. The metadata line holds the project and end date when known, `chunk starts at line N` (the zero-based raw-JSONL line index where the chunk starts), then `main transcript` or `sidecar <id>` for the transcript holding that line. It ends with the platform and, for a sub-agent session, `child of <parent>`. A main-transcript entry whose complete session ID is at least eight bytes long and is shown intact, both here and in the brief's header, adds `Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.` The brief shows that session's latest indexed exchange, not the matched passage. Sidecar entries and shortened or redacted ID previews carry no such line.

### Shortened fields

Every field is redacted on its complete value before it is shortened. Metadata and query echoes stay on one line, with control characters escaped. Omitted text is marked with `…`, which counts toward the limit; a passage can be shortened at either end:

| Field | Limit (UTF-8 bytes) |
| --- | ---: |
| Query in a group heading | 256 |
| Each failure cause (up to two per group) | 192 |
| Passage, line breaks kept | 1,500 |
| Knowledge source label / title | 512 / 256 |
| Vault session ID / title / project / sidecar ID | 128 / 256 / 512 / 256 |

When the sidecar ID is redacted or shortened, the entry says so and adds the session-wide chunk index when it is known. Literal STX/ETX bytes stored in a passage appear as `�` (U+FFFD). Limits apply to display only: retrieval always uses the complete query and selectors. A shortened field still counts as shown and does not stop the batch.

### Interrupted batches and retries

Before running each query, the reply reserves room for that query's group heading and status. Hits are then added whole. If an entry does not fit, the batch stops without running later queries, and a completion notice names the first unfinished query:

- `Query N showed S of R hit(s) returned at limit L/query` — query N ran, and S of its R ranked hits fit (S can be 0).
- `Query N was not searched` — no room remained to run query N.

Then the notice counts the following queries that were not searched and gives the retry range. Retry from query N **inclusive** through the last query, with the original selectors and requested limit. The retry is a fresh search: numbering and references restart at 1, ranks start from the top, and the corpus may have changed. A shortened field alone never triggers this notice.

On `capy_search`, the limit defaults to 2 and is capped at 2, then at 1 after the shared budget tapers. Each valid call consumes one admission from that budget, so the retry can have a lower effective limit. When the boundary query returned more than one hit, its notice adds: `If your next call's effective limit is below R, retry after the reset time below to see the omitted ranks.` With defaults, calls 1–3 show two hits per query and calls 4–8 show one. A blocked call reports the reset time. `capy_vault_search` takes no admission and has no footer. Its limit defaults to 3 and is capped at 10. A query's complete group of ten maximum-size hits fits a fresh reply.

### Walkthrough

This `capy_vault_search` batch uses `limit: 5`. It has a reworded query that cites earlier passages, a later window into an already shown chunk, a layer failure and an interrupted final group. `TestVaultSearchReply_DocumentedWalkthrough` produces it from archived test sessions with real retrieval, snippets and coordinates. The test pins the temporary project path to `/src/capy` and injects query 4's failure. This batch fits 40,960 bytes, so the test also runs it under a reduced reply budget to interrupt the last group:

```text
## 1. wal checkpoint

[1] --- [session:0b6c2f4e-1111-4c1d-9a51-5e0c3f1d2a01] ---
### Why does capy checkpoint the WAL on close? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

Why does capy [checkpoint] the [WAL] on close? (turn 1)

ContentStore.Close closes its own connection pool, then r…

[2] --- [session:7d9e1a3b-2222-4f0e-8c62-6a1d4e2b3c02] ---
### How does vault rekey avoid PRAGMA rekey? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

How does vault rekey avoid PRAGMA rekey? (turn 1)

PRAGMA rekey is incompatible with the [WAL] journal mode, so sqliteutil.Rekey writes a fresh new-key file instead: open the old database, …

---

## 2. checkpoint wal

↑ same passage as [1]

↑ same passage as [2]

---

## 3. readers completing

[3] --- [session:0b6c2f4e-1111-4c1d-9a51-5e0c3f1d2a01] ---
### Why does capy checkpoint the WAL on close? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

…uns a TRUNCATE checkpoint on a fresh connection to fold the write-ahead log back into the database. [Readers] using the WAL can keep a TRUNCATE checkpoint from [completing]. Details for turn 1 with en…

---

## 4. rekey journal

⚠ partial results (vault: search query: no such table: vault_chunks_trigram)

[4] --- [session:7d9e1a3b-2222-4f0e-8c62-6a1d4e2b3c02] ---
### How does vault rekey avoid PRAGMA rekey? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

How does vault [rekey] avoid PRAGMA [rekey]? (turn 1)

PRAGMA [rekey] is incompatible with the WAL [journal] mode, …

---

## 5. backup copy

[5] --- [session:2b4d6f8a-6666-4a1c-8e3b-5d7f9a1c3e06] ---
### Why rekey through a backup copy instead of in place? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

Why rekey through a [backup] [copy] instead of in place? (turn 1)

The new-key [backup] [copy] is built in a separate fi…

[6] --- [session:3f5a7c9e-3333-4b2d-8e4f-6a8c0e2f4a03] ---
### Which commands share the backup copy rekey path? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

Which commands share the [backup] [copy] rekey path? (turn 1)

When rotating an existing key, both capy vault rekey and capy …

[7] --- [session:5e7a9c1b-4444-4d6f-8a2c-4e6a8c0e2a04] ---
### Does a backup copy rekey keep the original database? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

Does a [backup] [copy] rekey keep the original database? (turn 1)

SwapAndVerify renames the original to a …

[8] --- [session:9c1e3a5f-5555-4f8b-9c4d-2e4a6c8e0a05] ---
### Can a failed backup copy swap need manual recovery? (turn 1)
/src/capy · 2026-04-05 · chunk starts at line 0 · main transcript · claude-code
Latest exchange: call capy_vault_brief with this session ID and the same project/all_projects arguments.

Can a failed [backup] [copy] swap need manual recovery? (turn 1)

If verification fails and the rollback also fai…

⚠ Reply budget reached. Query 5 showed 4 of 5 hit(s) returned at limit 5/query. To continue, retry query 5 (inclusive) with the same selectors and requested limit; numbering restarts at 1 and the retry searches current data from the first rank.
```

Query 2 matches the same terms as query 1, so both of its hits are references. Query 3 matches later terms in the first session's chunk; that window is a new entry. Query 4's surviving hit appears beside the injected trigram-layer failure. The advertised retry is `queries: ["backup copy"], limit: 5`. It returns `## 1. backup copy` with ranks `[1]` to `[5]`, including the hit that query 5 omitted. A `capy_search` reply uses the same layout with knowledge entries and ends with the budget footer.

### Guidance and recovery

Request-wide guidance appears at most once per reply. After a clean miss with knowledge in scope, the reply names `capy_runs` for new captured command output. When ephemeral sources are excluded and exist, it also names both ways to include them: `include_kinds: ["durable","ephemeral"]` and `source: "<label>"`. The per-kind miss statement, such as `3 durable, 2 session source(s) indexed.`, appears only when every query ran, every pass succeeded and none returned a hit. If the queries that ran returned no hit, the reply can still report a disabled vault or a reindex backlog, even after a partial batch.

## Session brief

`capy_vault_brief` returns the same text as [`capy vault brief`](vault.md#session-brief): one archived session's metadata, its opening indexed request and its latest indexed exchange, in at most 4,096 bytes. "Latest" is the latest exchange in the vault's index, which can lag a live session. A session whose stored index version is below the current one is served with a `capy vault reindex` hint.

```json
{"session": "48285c38-32fd-46f3-b18f-121cba7471ce", "project": "billing"}
```

The tool accepts only `session`, `project` and `all_projects`. `session` is required: a complete session ID or a literal prefix, at least eight bytes long, in which `%` and `_` are ordinary characters. Any other argument is rejected before the vault is opened, including `through_line`; the brief has no historical cutoff. Use `capy vault show` for earlier messages.

Scope follows `capy_vault_search`. By default the lookup matches the current project's directory in the imported path, even after reassignment. A server started without a project directory needs an explicit selector. `project` matches a literal substring of the effective project, and exact `"*"` or `all_projects: true` widens to all projects. Scope applies before the prefix is resolved. An ambiguous prefix asks for a longer ID, and a miss names neither out-of-scope candidates nor a count. To follow a hit that offers the brief action, pass its full session ID with the same `project`/`all_projects` arguments as the search. Then check that the brief's `Session` line shows that complete ID; hits offer the action only for IDs the brief can show in full. On a direct call, a `Session ID preview` line cannot confirm the match. If a legacy filename-derived session was deleted after the search, its ID can resolve as a prefix of a longer surviving ID.

A brief, including one that reports no indexed entries, is a success. Invalid arguments, an ambiguous prefix, a scoped miss, a disabled vault, a missing or incompatible vault, an authentication or storage failure and the two-second read limit all return `isError`. A storage or compatibility diagnostic is shortened to 1,024 bytes after its `Error: ` prefix. Each call reads one committed snapshot through its own read-only connection, independent of the server's vault writer.

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

General cleanup previews expired or oversized ephemeral and legacy knowledge-session sources. Cleanup preserves a durable source at every age and size, including after a reduction in `store.max_source_bytes`. Reindexing or refetching the label as ephemeral makes its indexing age and size eligible for cleanup again. To delete one, use its exact label with `capy cleanup --source <label> --force` or MCP `source` and `dry_run: false`. Vault archives have a separate lifecycle.

The CLI or MCP server running cleanup enforces this policy. Older binaries sharing the same knowledge database can still evict durable sources under their earlier rules.

- `capy cleanup --force` or MCP `dry_run: false` applies requested eviction.
- MCP `purge_ephemeral: true` restricts eviction to **expired** ephemeral sources; it does not clear all recent scratch output. CLI `--kind ephemeral` has the same TTL restriction.
- `source` selects an **exact** label for eviction, unlike search's substring selector.
- MCP `purge_all: true` resets the selected knowledge database when paired with `dry_run: false`.
- `vacuum` reclaims freelist space; `optimize` rebuilds FTS indexes and vacuums to reclaim index bloat.

Standalone `capy cleanup --optimize` / `--vacuum`, or equivalent MCP flags without an eviction mode, run immediately despite the default dry-run value. Combined with eviction, reclamation follows it and is skipped on dry runs. See [reclamation rationale](../adr/029-fts-tombstone-bloat-reclamation.md).

New or changed plaintext sources, including malformed-JSON fallback, use chunks of at most 4,096 bytes. Markdown and structured JSON may exceed that limit. Same-hash content keeps its older chunks. To repair an oversized legacy plaintext source, first retain its original content, evict its exact label using `capy_cleanup` with `dry_run: false`, then index the original content/path under the same label. See [chunk limits](../adr/022-source-size-guard-and-db-bloat-prevention.md).
