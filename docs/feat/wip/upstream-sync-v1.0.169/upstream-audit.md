# Upstream audit: context-mode after v1.0.136

> Audited: 2026-10-10
> Capy baseline: `c0bfff2800614b47a163949eef34645f854fff3a`
> Previous design: [upstream-sync-v1.0.136](../../done/upstream-sync-v1.0.136/design.md)
> Result: [design](design.md), [implementation](implementation.md), [tasks](tasks.md)
> Reconciled: 2026-10-10 at capy `d182825c824cfd154926d9f59ba7af6d8821f225` (runtime unchanged from `c0bfff2`)
> Evidence: source/history inspection, host documentation, rerun synthetic encrypted-store probe and focused existing tests; see [reconciliation](.reviews/reconciliation-2026-10-10.md).

## Exact comparison

Capy commit **`c0bfff2`** updates `context-mode` from `f8d46390613f068f232eb14ad91804841c64bdfa` to `0dfbe8de71abcb637a07dd6444bee5823c3186fc`; it is no longer HEAD. The previous design names that same `f8d4639` as its endpoint. Therefore the correct exclusive/inclusive comparison is `f8d4639..0dfbe8d`, with no gap between the documented sync and the old submodule pointer.

The range contains **620 commits: 581 non-merge commits and 39 merges**. Its endpoint has package version **1.0.169**, but `git describe` reports **`v1.0.169-167-g0dfbe8d`**. The feature directory uses the package version for continuity with previous syncs; the full endpoint SHA, not the release tag, defines its scope. The v1.0.169 tag resolves to `442f1eb6c7cdad3f0cb2411d00756f9e5cf2b587`.

Before filtering, the complete diff touches 399 files with 49,372 insertions and 13,582 deletions. These are range totals, not estimates of the selected Go implementation.

Reproduce the inventory with `git -C context-mode log --no-merges --format='%h %s' f8d4639..0dfbe8d` and inspect behavior with `git -C context-mode diff f8d4639 0dfbe8d -- <paths>`. Use `git show <commit> -- <paths>` to distinguish contributions within mixed commits. Do not compare against today's remote branch or stop at the v1.0.169 tag.

## Filtering method

1. Inspect the range's subjects and changed paths. Ignore merge duplication; use final source diffs to establish what survives at the endpoint.
2. Remove CI/install-stat updates, generated bundles, version/packaging-only changes, documentation/marketing pages, and test-infrastructure-only changes from port candidates. Tests accompanying runtime changes remain evidence.
3. Exclude Windows/PowerShell/MinGW, Bun, and unsupported host implementations: KiloCode, Kiro, Zed, Pi, OMP, OpenClaw, OpenCode, Gemini/Qwen/Kimi, Cursor, Copilot, Antigravity and related bridges.
4. Retain Claude Code and Codex changes when they affect capy's existing integrations, as confirmed by the user. Codex MCP setup and vault support do not imply capy has a Codex hook adapter.
5. Inspect mixed commits by hunk, not subject. In particular, `89f4e73` includes a portable working-directory fix despite its Windows-oriented subject; `ff5b0cf` includes portable MCP annotations; `547c18b` combines broadly relevant hardening with TS installer work.
6. Compare each surviving behavior against capy at the recorded baseline. Classify it as selected, covered, deliberately excluded, or deferred with a concrete follow-up. The tables group behavioral changes rather than claiming disjoint commit-category counts.

## Selected changes

| ID | Upstream evidence | What changed and why it applies | Capy disposition |
|---|---|---|---|
| S1 | `c1933fb` (#763), `src/security.ts` | Inspect nested command substitutions; split newline/background operators; evaluate allow/ask over every executable element. | `split.go` lacks those boundaries and `eval.go` checks allow/ask against the whole string. Add explicit error/limit semantics and unquoted-heredoc expansion cases. Task 1. |
| S2 | `d1ae562` (#852) | Constrain the explicit execute-file path to the project, with Read allows as exceptions. | Task 2a first repairs capy's origin-free matcher for supported host anchors and absolute walker/refresh inputs, preserving legacy denies. Task 2 then uses it for a consistent checked path and explicit external grants. This matcher work is a capy prerequisite, not a claim upstream already solved it. |
| S3 | `547c18b` (#716), session-ID filename hardening | Prevent a session identifier from escaping its state directory. | Capy does not persist upstream stats files, but `internal/hook/guidance.go` interpolates an unchecked session ID into a filename. Adapt to that actual sink. Task 3. |
| H1 | `4bc292f`, `hooks/core/formatters.mjs` | Use an explicit denial for a Bash redirect rather than depend on command replacement. | `routeBash` currently emits an echo command through `FormatModify`. Express the intended block directly; keep Agent prompt edits separate. Task 4. |
| H2 | `85f64d2` (#834), `b1ba5cc` (#724/#725) | Avoid routing a subagent into tools it cannot call; bootstrap deferred tools when available. | Preserve child identity, keep unknown availability advisory, and restore redirects for suitable per-child observed tools. Do not equate child status with missing MCP or trust two local definition files. Tasks 5/5a; first-call limitation D5. |
| H3 | `getInputProjectDir` already present at **baseline `f8d4639`** | Hook payload cwd can differ from process cwd/environment. | Pre-existing capy gap found during reconciliation, not a new-range delta. Task 5 separates working-directory context from selected project and loads rules after selection. |
| E1 | `3988090` (#657) | Stop appending shell redirection to user commands; merge captured streams afterward. | Both Go batch execution paths append ` 2>&1`, which can invalidate a terminal heredoc delimiter. Task 6. |
| E2 | `a12a64c` (#45), `498460d` (#765), portable hunk of `89f4e73` (#788) | Make execution cwd deliberate and consistent across runtimes; support per-call overrides. | Shell uses capy's project, other runtimes and Rust use temporary directories; execute-file uses process cwd. Unify defaults and add explicit overrides without transcript guessing or changing database identity. Tasks 7–8. |
| E3 | `0fb15c7` (#848) | Drain detached child output without closing its read pipe or accumulating it. | Go already keeps its pipe readers alive, so the exact SIGPIPE fix is unnecessary. However, `safeBuffer` keeps appending after background return while the cap monitor exits. Adapt to bounded discard-after-detach and eventual temp cleanup, explicitly revisiting ADR-005's accepted accumulation. Task 9. |
| R1 | `9dd92d7` (#646) | Treat `%`, `_`, and escape characters in a source selector literally. | `knowledgeFilterClauses` uses an unescaped LIKE pattern. Both knowledge FTS tables share this filter; it is not the vault project filter. Task 10. |
| R2 | `6f699fa` (#789) | Enforce the chunk byte cap in every plaintext strategy, including oversized single lines. | `chunkPlainText` has no 4,096-byte enforcement, despite the source-level 2 MiB cap. Task 11. |
| I1 | `f749957` (#687), root-symlink hunk of `547c18b` | Add bounded directory ingestion while retaining per-file read checks. | Tasks 12/13 share prepared admission and bounded initial/stale reads. Task 12a adds persistent bounded stale scheduling because repeated directory imports otherwise multiply synchronous all-file work. Scheduling is a capy adaptation, not an upstream port. |
| C1 | `b51873f` (#721) | Expose indexing and knowledge retrieval outside an MCP host. | Capy has neither `capy index` nor knowledge `capy search`; existing `capy vault search` is a different corpus. Reuse ADR-032 credential selection and the shared ingestion path. Tasks 14–15. |
| F1 | `04ff30f` (#666), `7a3482e` (#679) | Per-call fetch freshness and robust arguments. | Same-hash indexing leaves `indexed_at` old and second-resolution timestamps cannot honor sub-second TTLs. Task 16a adds independent persisted millisecond validation; Task 16 adds overrides, accurate unchanged outcomes and retention separation. |
| A1 | Portable shared-server hunk of `7c82220` (#627), plus `7a3482e` | Literal boolean coercion for background/force despite a platform-labeled commit. | Task 6a shares strict presence-aware handling across booleans, preserving defaults and rejecting malformed inputs. `GetBool` already accepts string booleans and numbers; direct assertions do not. Do not conflate them. |
| F2 | `9e29a94` (#683), fetch-error hunks | Suggest one retry for transient fetch failure. | Task 16 uses typed Go causes for selected DNS/timeout/unreachable cases, in both modes; reject upstream's permission-error/string heuristics and automatic/network-guarantee claims. |
| B1 | `50ec3b9` (#697/#698), `9e29a94` (#683) | Optional batch queries and explicit batch/global search scope. | Queries are mandatory and always batch-local. Preserve that default scope; allow indexing-only calls and explicit whole-knowledge-store retrieval, including ephemeral output. Task 18. |
| B2 | `f7af3ca`, `c1030ca` (#717/#736) | Persist command provenance and also show an immediate inventory. | Task 18 implements both parts, labels every global hit, and caps the entire serialized response including section inventory, query text and notices. Stored content is independent of response truncation. |
| O1 | `50ec3b9` (#697/#698) | Cache outcome reporting, visible throttle state **and operator-tunable thresholds**. | Task 17 reports eligible outcomes and configured default freshness; Task 19 adds validated `[search]` settings with unchanged defaults through capy's own config. No upstream env names or unverified per-agent keys. |
| O2 | `49fa9d2` | Avoid rounding an ordinary 99.9% saving to 100%. | Portable display hunk separated from excluded analytics. Task 17 fixes both Go display sites, clamping nonzero-return cases below 100.0%, without changing the savings formula. |

## Already covered or deliberately different

| Upstream evidence | Finding in capy | Decision |
|---|---|---|
| `9c5c8f7` (#659/#660), fetch preview truncation | `truncateRunes` already preserves UTF-8 boundaries in single and batch previews. | Covered; retain regression tests. |
| `14ce9c1` (#715), redundant rerank sort | `internal/retrieval/rerank.go:findMinSpan` already consumes ordered lists without copying/sorting. The earlier synonym-position merge still needs sorting. | Covered; do not remove the necessary synonym sort. |
| `ff5b0cf` (#846), tool annotations | `internal/server/tools.go` already supplies behavior-specific annotations to all ten tools. | Covered; preserve actual descriptor tests, no blanket annotation rewrite. |
| `547c18b`, bounded remote response | Native fetch uses `fetchMaxBody`, a bounded reader, safe transport, and a 20-request batch cap. | Preserve Go implementation; do not port embedded JS fetchers or raise its limit to upstream's 50 MiB. |
| `7a3482e`, stringified request arrays | `coerceFetchRequests` already handles this. | Covered portion of F1; only missing argument behavior is selected. |
| `7c82220`, bare-string query lifting and numeric limit coercion | Capy deliberately accepts actual/serialized query arrays plus the separate `query` alias; the pinned SDK's `GetFloat` already accepts numeric strings. | Retain the explicit array contract: a malformed supplied `queries` is an error, not a singleton lift or an indexing-only request. Literal-boolean behavior is independently selected as A1. |
| `19a7590`, `ba571f7`, project-filter portion of `3a45eb1` | Knowledge is selected through capy's configured DB target; vault project scopes already distinguish effective labels and imported paths. | Preserve ADR-027/028 and the newer project-name contract. Do not add session IDs to knowledge chunks or interpret a `project:` query token as a new selector. Explicitly sharing a knowledge DB continues to share its content. |
| `3d8db08`, `fa6541f`, runtime storage overrides | `internal/config` already handles project/global config, XDG/absolute/relative store paths, worktrees, and credentials. | Preserve capy configuration; no `CONTEXT_MODE_*` aliases. |
| `afd109b`, auto-memory project leakage | Capy has no upstream auto-memory directory scanner; vault selection owns project isolation. | Not applicable. |
| `38117ad`, `a54c666`, unconditional execute/source echoes | Capy's vault decoders preserve submitted tool inputs, including the recent Codex viewer work. Returning another 2 KB of source on every execute call consumes the context capy is intended to save. | Reject unconditional source echoes. B2 covers missing batch provenance with a separate aggregate bound. |
| `dcab56f` (#817), short-command routing threshold | The patch gates a generic Bash nudge by command-string length; it does not measure output or broadly exempt small WebFetch calls. Capy already gives a once-per-session comprehension-aware nudge. | Reject the heuristic: a short command can produce unbounded output. No new size-threshold setting. |
| `a888cac`, lifecycle idle-shutdown revert; `77e22a4` / `bebc752` bridge follow-ups | Capy has stdio shutdown, parent/signal monitoring and process-group cleanup, with no idle timer or Pi/OMP bridge child. | Do not import idle reaping. |
| `c5e0fbc`, Node uncaught-exception storm | No Node event loop or equivalent process-wide exception handler in the Go server. | Not applicable; keep normal Go error and shutdown paths. |
| `7afa0f4`, Node SQLite busy timeout | Go's SQLite DSN already sets the driver busy timeout. | Covered; retain WAL/encryption invariants. |
| `a12a64c`, `e939b9a`, `498460d`, root recovery from host session logs | Capy selects project/config/credentials before constructing its executor. Adopting a recent-transcript heuristic could switch the wrong checkout's execution or credentials. | Take explicit cwd controls from E2; do not infer active project from newest transcripts or plugin paths. |
| `ff5b0cf` (#844/#845), readiness sentinel and Codex hook formatter; `a4052f2`, `ec48166`, `98d239b`, `a90d399`, `115afc3`, `4986019` | `SetupCodex` installs MCP configuration, a wrapper, and routing instructions. `internal/adapter` only implements Claude Code hooks; capy has no TS plugin-manager install/cache/sentinel architecture. | Reviewed because Codex is supported; do not introduce a new Codex hook adapter or copy npm/plugin lifecycle fixes. |
| `7cc9d3b` (#810), standalone Claude hook installation | `SetupClaudeCode` already merges hooks into the selected project settings rather than assuming an npm package's `hooks.json` is active. | Covered by a different setup path; retain artifact/idempotence checks. |
| `9e29a94`, broad tool-description rewrite | Capy's descriptions deliberately separate comprehension from extraction and preserve source kinds/vault behavior. | Update affected parameters and routing instructions only; reject upstream's universal sandbox-first wording and claims of unified event memory. |

## Excluded subsystem families

These are explicit scope exclusions, not unfinished ports.

| Family | Representative evidence | Reason |
|---|---|---|
| CI, install statistics, version bumps, generated bundles, packaging | Repeated `ci: update install stats`, `*.bundle.mjs`, manifests and release scripts | No independent Go runtime behavior. Source changes in mixed commits were inspected separately. |
| Documentation, website, marketing and hosted dashboard | `11b8b58`, `8db9102`, `web/`, removed `insight/`, landing-page series | Capy has no hosted analytics product or browser dashboard to synchronize. |
| Unsupported operating systems/runtimes/hosts | Windows/Bun/MinGW patches and the adapter list above | User exclusion. Portable hunks were retained even when their commit subject names an excluded platform. |
| JS executable/runtime/cache healing | `813da2a`, `b1cca82`, `3bff2d5`, `97792c5`, `606383e`, `6274f45`, `73955f3`, `6c671dc`, `63d0a3a`, `ed91326`, `79e7bb5`, `1147411`, `caba924`, `76559cf`, `6cb1490` | Capy distributes a Go binary and portable wrappers, not versioned JS bundles, npm modules, or repaired shell snapshots. |
| Analytics bridge, pricing, token cost and prompt profiling | `043c38f`, `83ecb5b`, `ac46932`, `13bfdd3`, `d4c3647`, `b977902`, `c084707`, `1d7affa`, `32211a4`, `25f74f8`, `aaf7916`, `82a8d0c`, `3182d82`, `4f22887`, `320ed3e`, `549308c` and related cost/stat series | Upstream is forwarding event envelopes to an external analytics platform. Capy's local byte counters are a different contract. O1 takes only directly useful local cache/throttle visibility. |
| Session event capture, goal/directive replay and compaction snapshots | `9e2e623`, `f262852`, `fcc4483`, `9f63a0b`, `973969e`, `f616663`, `9480b55`, `ec43e5b`, `ee71b5a`, `4f58e4f`, `2e7a543` | Capy archives transcripts through platform decoders and queries vault chunks; it does not extract/reinject behavioral directives or maintain upstream's event/snapshot DB. Adding goal replay would require its own product design. |
| Test-only infrastructure and cross-platform harness repairs | Platform smoke fixtures, home isolation and bundle parity scripts | Do not port TS test plumbing; use the selected fixes' cases as behavioral evidence for Go tests. |

## Deferred work with concrete next steps

### D1: Per-agent search budgets

`3a45eb1` adds a bounded map of counters keyed by `currentAttribution().sessionId`. That function uses an in-process override or environment/latest-session lookup. Those are not proof of which concurrent Claude subagent issued a particular stdio MCP request.

Capy's `searchThrottle` is shared by one server, and the pinned `mcp-go` session represents the transport connection, not necessarily a child agent. **The shared-budget limitation remains after this sync.** Task 19 makes limits configurable and visible, but must not claim to fix isolation.

Next step: capture real request envelopes from two simultaneously active agents on each supported host, establish a stable host-supplied per-request identity, then design bounded keyed counters and a shared fallback. Do not add a model-chosen `agent_id` argument, hash queries, or key on the last hook file written. The implementer records the follow-up at `searchThrottle` and in the final verification notes.

### D2: Filesystem replacement between policy checks and runtime reads

Existing execute-file wrappers open the supplied filename in the child. Canonicalization in S2 prevents ordinary lexical/symlink escape, but cannot make a later child open atomic with the parent's policy check. Existing index descriptor checks establish regular-file/size properties, not an operating-system sandbox for arbitrary submitted code.

Next step: separately evaluate a bounded snapshot or descriptor-based file handoff across all eleven runtimes before promising protection against a hostile concurrent filesystem writer. This sync documents that limit at the path admission helper and preserves deny checks; it does not advertise full filesystem confinement of executed code.

### D3: Concurrent batch source-label collisions

`handleBatchExecute` stores output under `batch:` plus `truncateLabel(commands)`. Separate calls can therefore select the same source, including through the 80-byte truncation, and an overlapping call can replace the content before the first searches it. B1 preserves this existing storage identity rather than silently replacing dedup with one-source-per-call accumulation.

Next step: design a request-specific source identity or source-ID snapshot strategy with explicit ephemeral-retention and friendly-label semantics, then add overlapping-identical-label regression tests. Task 18 records this at the label builder. This sync's batch-scope wording must describe label scoping without promising per-request isolation.

### D4: Markdown and structured JSON leaf chunk limits

`SplitOversized` can retain a large paragraph/fence, and `walkJSON` can retain a large primitive. Upstream `6f699fa` fixes plaintext, not those representations. The selected plaintext port therefore does not establish a universal 4 KiB invariant. Existing source-size admission (2 MiB by default) still applies, and Task 18 separately caps rendered batch responses.

Next step: design representation-aware splitting with fence/key-path and overlap fixtures, then benchmark those corpora before extending the cap. Task 11 documents the remaining paths beside the new plaintext guarantee. This preserves the explicit scope recognized by Reviewer 1 while addressing Reviewer 2's concern that readers could overgeneralize it.

### D5: Authoritative child tool pools before the first call

The hook fields identify the child, not its complete effective tool pool. Project/user files can be shadowed by higher-priority definitions or supplied through plugins/CLI; omission of `tools` does not itself prove the parent's capy server is callable. Tasks 5/5a use advisory fallback until an exact capy capability is observed, then enforce applicable redirects for that child. The first unobserved native call can still pass through, even for an inherit-all child.

Next step: identify a supported host capability feed covering effective definitions and live tool availability, with managed/CLI/plugin precedence and disconnected-server tests. Do not advertise a two-file frontmatter reader as that feed or force fixed-tool children into unavailable tools. Record this limitation in hook routing tests and user guidance.

## External contract checks

- [Claude Code hooks reference](https://code.claude.com/docs/en/hooks#pretooluse-decision-control), checked 2026-10-10, documents input replacement and explicit denial. Thus upstream's claim that Bash rewriting is broken is historical evidence, not a universal current-host fact. H1 chooses denial because blocking is the intended operation. The [common input fields](https://code.claude.com/docs/en/hooks#common-input-fields) identify subagents with `agent_id`; an `agent_type` alone can identify a main-thread custom agent, so do not copy upstream's OR test blindly.
- Git ignore handling was checked through Context7 `/git/htmldocs`, then against the installed **Git 2.34.1** `git-check-ignore(1)` manual because Context7 did not provide a version-pinned result. `--stdin -z` uses NUL-separated paths; `--no-index` applies ignore rules even to tracked files; exits 0 and 1 are normal, while 128 denotes failure. [Git reference](https://git-scm.com/docs/git-check-ignore).

## Audit completion

- [x] Exact prior-sync and submodule endpoints reconciled.
- [x] Whole-range subjects/paths and surviving core source diffs inspected.
- [x] Mixed platform commits separated by behavior.
- [x] Selected items mapped to current Go owners and implementation tasks.
- [x] Covered behavior, intentional differences and deferred gaps recorded.
- [x] Two independent reviews supplied by the user; every finding mapped to evidence/disposition in the reconciliation.
- [x] Design/implementation/tasks revised for valid findings and explicit alternative decisions.
- [ ] Independent review of the revised artifacts; author reconciliation is not independent approval.
