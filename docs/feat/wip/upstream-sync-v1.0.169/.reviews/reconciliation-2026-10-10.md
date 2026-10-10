# Reconciliation of the two upstream-sync design reviews

> Date: 2026-10-10
> Input: the user-supplied Codex review (R1–R10) and Claude review (findings identified below by title)
> Inspected capy: `d182825c824cfd154926d9f59ba7af6d8821f225`
> Runtime baseline: `c0bfff2800614b47a163949eef34645f854fff3a`
> Upstream: `f8d46390613f068f232eb14ad91804841c64bdfa..0dfbe8de71abcb637a07dd6444bee5823c3186fc`
> Revised: [design](../design.md), [implementation](../implementation.md), [tasks](../tasks.md), [audit](../upstream-audit.md)
> Status: all supplied findings evaluated; valid design defects corrected or resolved by an explicit scoped decision. Feature implementation remains pending.

## Method and provenance

Re-read all four feature artifacts, the current security/store/server/executor/hook/config owners, the existing probe, relevant ADRs, and the individual upstream diffs cited below. Existing runtime code is unchanged between `c0bfff2` and the inspected capy HEAD; intervening changes are design and agent/tool configuration. No earlier review conclusion was treated as proof of a current implementation.

Reconfirmed the gitlink update belongs to **`c0bfff2`**, not current HEAD; 620 commits comprise 581 non-merges and 39 merges. The complete range diff is 399 files, 49,372 insertions and 13,582 deletions. The endpoint describes as `v1.0.169-167-g0dfbe8d`. These audit facts remain valid; its movable-HEAD wording was corrected.

Claims about host contracts were checked against the primary permission, hook and subagent references, not inferred from upstream comments. SDK behavior was checked in the locally pinned `github.com/mark3labs/mcp-go@v0.46.0`; Go error fields were checked with local `go doc`. Proposed new behavior is distinguished from verified current behavior throughout this record.

## Consolidated dispositions

Priority is the highest assigned to the shared issue by either reviewer. A partially accepted finding means the factual problem is addressed with a different implementation or an explicit boundary, not that the recommendation was copied unexamined.

| ID | Reviews / priority | Verified evidence and assessment | Resolution |
|---|---|---|---|
| V01 | Codex R1; Claude “Read-rule matching” / P1 | `settings.go:ReadToolDenyPatterns` discards origin; `eval.go:EvaluateFilePath` has no project-relative candidate or host-anchor normalization. Probe reproduces both `//` mismatch and absolute-walker relative-deny bypass. | **Accepted.** New Task 2a prepares origin-aware policies and wires existing reads/refresh first. §3.2a defines supported anchors, physical paths, conservative legacy-deny union, diagnostic handling and allow restrictions. Tasks 2/12–15 consume it. |
| V02 | Codex R2/R4; Claude “TTL cannot re-arm” / P1 | Both fetch modes compare `SourceMeta.IndexedAt`; `indexPreparedChunks` same-hash branches update access/kind, not freshness. `stmtUpdateSourceIndexedAt` exists for stale files only. Timestamp parsing is whole-second. Probe confirms unchanged age and zero nanoseconds. | **Accepted.** Task 16a adds an atomic, nullable millisecond fetch-validation marker independent of indexed-at retention. Task 16 tests sub-second TTLs, unchanged/force/kind transitions, failure non-renewal, legacy misses and reopen. No timestamp-format rewrite. |
| V03 | Codex R3; Claude “batch response budget” / P1 | `handleBatchExecute` enumerates all chunk titles before its query-only budget. Probe produces a 184,021-byte inventory from 192,000 input bytes, under source admission limits. | **Accepted.** Task 18 caps the full serialized tool result at 81,920 bytes, including inventories, labels, queries, terms and notices; bounded metadata selection replaces loading all content for display. Indexing-only mode is covered. |
| V04 | Codex R5 / P2 | `fileChangedSince` checks descriptor size then uses unrestricted `io.ReadAll`; initial-ingestion extraction cannot protect that independent read. | **Accepted.** Task 12 bounds both reads, retains cached data on oversized refresh and requires a deterministic growth-after-stat fixture. Any shared reader stays below store/knowledge to avoid a cycle. |
| V05 | Codex R6; Claude “operator control” / P2 | `50ec3b9` adds portable threshold overrides; capy has hard-coded 60s/3/8 and no search config. ADR-008 assigns capy tuning to TOML. | **Accepted.** Task 19 adds `[search] window_ms`, `taper_after`, `block_after` with unchanged defaults, explicit-zero/integer/range/relationship validation and effective-value reporting. D1 remains: tunability does not prove per-agent identity. |
| V06 | Codex R7; Claude “boolean normalization” / P2 | `7c82220` changes common-server `background`, despite its OpenCode subject. Go execute/fetch/cleanup use direct bool assertions. **Correction:** SDK `GetBool` already accepts string booleans and numeric truthiness, so the claim that `all_projects: "true"` silently becomes false is wrong. The original draft also already accepted literal string force values; it did not reject `"true"`. | **Accepted with factual corrections.** Task 6a applies a presence-aware literal parser everywhere, preserving omitted defaults and rejecting malformed/null/numeric values before side effects. This intentionally narrows SDK numeric coercion and honors cleanup literal strings; both changes are documented/tested. |
| V07 | Codex R8 / P2 | `f7af3ca` stores a command preview under its indexed section; `c1030ca` adds the immediate inventory. Current Go stored output lacks it, and batch hit formatting omits `r.Label`. | **Accepted.** Task 18 ports persisted sanitized provenance and labels every global hit. Later retrieval without vault and equal-titled different-source tests prevent an immediate-response-only implementation. |
| V08 | Codex R9 / P2 | `tool_stats.go` subtracts server uptime from configured TTL, unrelated to fetch validation age. | **Accepted.** Task 17 replaces it with configured default freshness; individual responses carry effective TTL. Long-uptime/mixed-TTL tests and ADR-013 correction are planned. |
| V09 | Codex R10 / P3 | `49fa9d2` changes one-decimal reporting; Go has two `%.0f%%` sites. One decimal alone can still round an extremely near-100% ratio to 100.0%. | **Accepted and tightened.** Task 17 fixes both sites with one formatter, clamps nonzero-return cases to 99.9%, and keeps zero-return/zero-processed cases explicit. No analytics formula rewrite. |
| V10 | Claude “blanket child suppression” / P2 | Inherited child pools can include MCP, but `agent_id` is not a tool inventory. Higher-priority managed/CLI definitions and plugin sources defeat the proposed authoritative two-file lookup. Current capy records neither child identity nor observed capabilities. | **Partially accepted; replacement chosen.** Tasks 5/5a distinguish unknown from observed per-tool availability and restore applicable redirects after a child calls capy. The first-call limitation is explicit in D5. Reject the two-file lookup as an authority; do not invent a reliable host capability feed. |
| V11 | Claude “stale-refresh cost” / P2 | `refreshStaleSources` snapshots every file-backed row and checks it synchronously after a five-second gate. Repeatable directory imports have no total source-count cap. | **Accepted.** Task 12a adds persistent logical scheduling, 32-row/32-file and byte/admission-time budgets. Tests include 10,000 sources, repeated CLI restarts and fair progress; final benchmarks record latency/allocation/write effects. |
| V12 | Claude “scanner-overflow outcome” / P2 | Original design had neither numeric bounds nor the error-to-decision mapping; existing evaluators return only decisions. | **Accepted.** §3.1 names input/frame/element/visit bounds and fail-loud typed errors. Hook and all MCP evaluation paths block before spawn on evaluation failure, separately from unmatched ask. |
| V13 | Claude “markdown/JSON leaves uncapped” / P2 | `SplitOversized` can emit a large single paragraph/fence; `walkJSON` returns oversized primitives intact. Batch and HTML-derived content are markdown. However, upstream `6f699fa` and the original design explicitly target plaintext. | **Partially accepted as a limitation, not a missing port.** §5.2 now names affected producers and the remaining source-size bound. D4 records representation-aware splitting/quality fixtures as the next step. Whole-response bounding is implemented independently in the plan. Do not silently broaden this sync into a chunk-format redesign. |
| V14 | Claude “decision records” / P2 | Prior ADRs cover comparable contracts, but original tasks named only ADR-005/012 updates. | **Accepted.** §10/Task 20 name proposed cwd/project-identity, prepared Read/admission and freshness/scheduling records, plus affected existing ADR amendments. Numbering is allocated at implementation; nothing unimplemented is marked accepted. |
| V15 | Claude “over-serialized dependencies” / P3 | Original fetch→cwd, redirect→shell-parser and reporting→batch dependencies were file coordination, not behavior prerequisites. | **Accepted.** Remove those edges. Add only real prepared-policy, bounded-refresh, boolean-parser and freshness dependencies. Lettered tasks preserve reviewed task IDs; regenerate the graph and validate parallel markers. |
| V16 | Claude “four behavior changes” / P3 | New stream presentation and invalid-force handling were absent from the rollout list. Reconciliation introduces additional deliberate compatibility changes. | **Accepted.** §10 now lists all contract changes, including boolean numeric narrowing, host-anchor compatibility, cache migration, bounded refresh and response truncation. |
| V17 | Claude “directory labels/ignore scope” / P3 | Store identity is label+hash, not inode; upstream labels use a colon. Original prefix wording and external-root ignore origin were underspecified. | **Accepted.** Labels use `prefix:relative/posix/path`; changing prefix creates another source. Reusing a prefix can collide across roots. Ignore evaluation uses the canonical ingestion root's worktree; capy policy/DB/key identity stays with the selected project. |
| V18 | Claude “cache accounting and units” / P3 | Git-platform URLs redirect before cache/network work; millisecond tool TTL versus configured hours is an interface choice, not a Go limitation. | **Accepted clarification.** Explicitly exclude redirects, SSRF blocks and rejected syntax from attempts; count real fetch/post-fetch failures once. Milliseconds deliberately preserve upstream/MCP parity; convert configured hours at the boundary. |
| V19 | Claude “small portable deltas” / P3 | `9e29a94` includes a fetch retry hint. `7c82220` includes bare-string query lifting. **Correction:** `getInputProjectDir(input)` already prefers payload cwd at `f8d4639`; it is not newly introduced in this range. | **Mixed disposition.** Port typed, bounded transient guidance through Task 16; explicitly reject bare-string array lifting. Fix the old capy cwd gap in Task 5 as a labeled carry-over, selecting policy origin before loading it. Do not port `EPERM`/generic-getaddrinfo retry heuristics. |
| V20 | Claude “runtime edge cases” / P3 | Unquoted heredocs execute substitutions; `buildCommand` runs `go run <script>` and changing cwd changes module discovery. **Qualification:** `BuildSafeEnv` already strips `GOFLAGS`, so inherited `-mod=vendor` is not the right baseline claim. | **Accepted.** Task 1 covers substitutions in unquoted body data. Task 7 covers project imports/replace, automatic vendor behavior and toolchain directives while preserving safe-env filtering. |
| V21 | Codex separate AGENTS invariant follow-up | `codec.go` sets `supportedReaderVersion = readerVersionPlatform = 3`; zstd-only writes still require version 2. ADR-031 and architecture agree. | **Fixed now.** Root `AGENTS.md` states both milestones and the actual supported maximum without changing vault behavior. |
| V22 | Codex knowledge-label side note | `indexPreparedChunks` replaces changed content by label; a bare category label is not an append-only bucket. The reported recovery history is not required to establish that semantic conflict. | **Fixed locally.** Root `AGENTS.md` requires stable per-concept suffixes and skips already-documented findings. The external plugin's shared protocol remains a separately recorded upstream follow-up. |

## Why the larger recommendations were not copied literally

**Freshness needs independent persisted state.** Advancing generic `indexed_at` would also change cleanup/decay semantics, and switching its representation would affect multiple readers and older binaries. A nullable validation marker makes the successful-fetch transition explicit, permits millisecond checks and leaves retention unchanged. Legacy cache entries miss once; that is preferable to claiming unsupported precision. The new scheduling column is likewise separate from freshness/retention. These are deliberate amendments to the earlier no-migration assumption.

**Subagent availability needs evidence, not a guessed definition.** The host docs confirm tool inheritance, exclusions and definition precedence. Two filesystem reads omit higher-priority and non-file definitions and cannot prove server availability. The revised bounded observation mechanism restores protection when it has evidence, while preserving the escape from unavailable tools. It deliberately does not promise first-call protection; D5 records the concrete remaining requirement.

**Plaintext scope remains intentional.** The review correctly identifies larger markdown/JSON leaves, but does not show they are part of the selected upstream fix. Porting a byte splitter blindly into code fences/serialized primitives would add new content/quality decisions. Their existing source bound and the new complete output bound are named; D4 captures the separate work rather than leaving the gap only in chat.

## Source checks

The following direct source locations were re-read; paths are repository-relative unless noted.

| Claim group | Real source |
|---|---|
| Permission preparation | `internal/security/settings.go:ReadToolDenyPatterns`, `eval.go:EvaluateFilePath`, `glob.go:fileGlobToRegex`; `internal/server/security_check.go`, `server.go:getStore`; `internal/store/search.go:refreshStaleSources` |
| Freshness and retention | `internal/server/tool_fetch.go:handleFetchAndIndex/fetchOne/indexFetchedContent`; `internal/store/index.go:indexPreparedChunks`; `store.go:stmtUpdateSourceAccess/stmtUpdateSourceKind/stmtUpdateSourceIndexedAt/stmtGetSourceMeta`; `search.go:GetSourceMeta`; `cleanup.go:cleanupByTTL/retentionScore`; `schema.go`, `migrate.go` |
| Budgets and provenance | `internal/server/tool_batch.go:handleBatchExecute/executeBatchSerial/executeBatchParallel`; upstream `f7af3ca`, `c1030ca`, `50ec3b9` and the endpoint formatter |
| Stale bounds and scheduling | `internal/store/search.go:refreshStaleSources/fileChangedSince`, `internal/store/stale_test.go`, `store.go:stmtListFileBackedSources`; directory changes in `f749957` |
| Boolean semantics | `internal/server/tool_execute.go`, `tool_fetch.go`, `tool_cleanup.go:boolArg`, `tool_vault_search.go:vaultProjectScope`; pinned MCP SDK `mcp/tools.go:GetBool/GetFloat`; portable server hunks in `7c82220`/`7a3482e` |
| Visibility/config | `internal/config/config.go`, `loader.go:detectionOverlay/mergeConfig/validate`; `internal/server/server.go:searchThrottle`, `tool_search.go`, both percentage sites and TTL cache table in `tool_stats.go`; upstream `50ec3b9`, `49fa9d2`; ADR-008/013 |
| Hook context | `cmd/capy/hook.go`, `internal/adapter/claudecode.go:ParsePreToolUse`, `internal/hook/pretooluse.go`, `posttooluse.go`, `guidance.go`; upstream `85f64d2`/`b1ba5cc`, and `getInputProjectDir` at both range endpoints |
| Chunk boundaries | `internal/store/chunk.go:chunkPlainText/SplitOversized/walkJSON`, `index.go:chunkContent`; upstream `6f699fa` |
| Runtime/error handling | `internal/executor/runtime.go:buildCommand`, `executor.go`, `env.go:deniedEnvVars/BuildSafeEnv`; `internal/server/tool_fetch.go:fetchRemoteContent`; upstream `9e29a94`, `3988090`, `0fb15c7`, `89f4e73` |
| Vault milestone | `internal/vault/codec.go:readerVersionZstd/readerVersionPlatform/supportedReaderVersion`, ADR-031 and architecture's reader-version section |

Primary external checks (2026-10-10): [permission anchors and symlink rules](https://code.claude.com/docs/en/permissions#read-and-edit), [subagent definition precedence](https://code.claude.com/docs/en/sub-agents#choose-the-subagent-scope), [subagent tool controls](https://code.claude.com/docs/en/sub-agents#available-tools), and [hook common fields](https://code.claude.com/docs/en/hooks#common-input-fields). These establish the host facts; capy's proposed limited grammar, legacy-deny compatibility and unknown-capability fallback are its own explicit design choices.

## Verification actually performed

The existing reviewer-owned [probe](probes/main.go) was read before execution and left unchanged. It uses an explicit synthetic key and a temporary encrypted database removed after close. Re-running it produced:

```text
permission: host absolute allow: matched=false
permission: single-slash treated as absolute: matched=true
permission: relative deny on direct input: matched=true
permission: relative deny on walker input: matched=false
cache: same_hash=true indexed_at_advanced=false immediately_fresh_at_1s=false
cache: timestamp_nanoseconds=0 dedup_result_chunks=0 persisted_chunks=1
batch: admitted_source_bytes=192000 section_count=8000 section_inventory_bytes=184021 query_cap_bytes=81920
```

Commands used `GOCACHE=/tmp/capy-upstream-review-go-cache`, `CGO_ENABLED=1`, `CAPY_DB_KEY=test-key-for-development`, `CAPY_VAULT_KEY=test-key` and `-tags fts5`:

- `go run -tags fts5 ./docs/feat/wip/upstream-sync-v1.0.169/.reviews/probes` — passed, observations above.
- `go test -tags fts5 -count=1 ./internal/security ./internal/store -run 'Test(Evaluate|SplitChained|IndexAndDedup|StaleDetection|ChunkPlainText)'` — passed (security 0.003s; store 4.302s). Passing existing tests does not refute the missing cases shown by the probe.
- `go env GOMOD` — selected this repository's `go.mod` from the project and `/dev/null` from `/tmp`, confirming cwd-dependent module discovery. This is not a completed fixture for the future executor behavior.
- `go doc net.DNSError` and `go doc net.OpError.Timeout` — verified available typed error classification before recommending it.

No runtime feature implementation, full suite, race run, benchmark comparison or new live-host capability experiment was performed in this reconciliation. Planned tests remain unchecked. Documentation checks passed for 61 local links and all 25 tasks, including dependency cycles, invalid parallel pairings, final-task coverage and fenced blocks. Tracked diff and new reconciliation-file whitespace checks passed; the normal no-index diff exit 1 means the file is new, not a whitespace error.

## Review-process side notes

The earlier knowledge-index overwrite/recovery narrative was supplied by Reviewer 1. It was not used as runtime evidence and no database recovery was repeated here. This reconciliation performs no `capy_index` writes: the decisions are already durable in these documents. Root `AGENTS.md` now prevents bare-category writes locally. The external plugin protocol still needs the same unique per-concept suffix rule so other projects avoid replacement; that global skill change is outside this repository correction. No external skill files or real knowledge content were edited.

The reviewers' conclusions about encryption, separate vault ownership, generator/copy synchronization, rejected platform/analytics architecture ports and D1–D3 remain valid after rechecking their owners. D4/D5 make additional limitations explicit; they are not hidden implementation checkboxes marked done.

Optional editorial follow-up is `kk:clarify-docs` on the four revised feature artifacts named above. The next independent gate is `kk:review-design upstream-sync-v1.0.169` against the revised contracts and this reconciliation. Author checks are not a replacement for that gate.
