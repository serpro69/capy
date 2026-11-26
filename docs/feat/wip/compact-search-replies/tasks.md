# Compact search reply tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: persistent rank cursors, CLI format and scoring changes

## Task 1. Show failures alongside surviving search results

- **Status:** done
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Retrieval failures](implementation.md#surface-partial-retrieval-failures)

- [x] 1.1 Collect a query-local diagnostic through the existing retrieval error callback in the knowledge and vault chunk adapters. Feed surviving ranked hits and failure status to both MCP handlers. Retain strict CLI and ordinary library behavior.
- [x] 1.2 Exercise one failing FTS layer with a surviving hit, loss of both layers with zero hits, and a failing vault alongside healthy knowledge. Check a clean miss as a control. Verify the concurrent callbacks under the race detector and keep the scoped availability failure visible once.

### Execution context

- 2026-11-26 · observed · base `ae5a4dc` (master, clean). `handleSearch` calls non-strict `SearchWithFallbackContext` and `handleVaultSearch`/`handleSearch` call `SearchChunks`; neither sets `retrieval.CorpusConfig.OnError`, so a failing Porter/trigram layer is only logged (`slog.Warn`) and the query silently degrades (`internal/retrieval/retrieval.go` `execSearch`). Setup errors (`getDB`, corpus build) already reach the handlers. `handleVaultSearch` drops all rows on any error via `continue`. Only the CLI (`cmd/capy/search.go`) and captured-execution knowledge lookup (`internal/execution/knowledge.go`) use strict `FailOnError`; `SearchChunks` has no non-MCP production caller.
- 2026-11-26 · observed · the request-wide availability notice already appears once (`TestSearch_VaultAvailabilityFailure`); optional stats/counts already leave hints absent on failure; the empty-store shortcut already requires a successful scoped preflight.
- 2026-11-26 · approach · add non-strict diagnostic entry points (`store.ContentStore.SearchPartialContext`, `vault.VaultStore.SearchChunksPartial`) that record the first layer error per call with `sync.Once` and return it alongside surviving ranked rows; knowledge access tracking still runs on survivors. Existing `SearchWithFallbackContext` (strict and best-effort) and `SearchChunks` keep their policies. Handlers report layer diagnostics through the existing per-corpus `errs` status, so zero survivors render `Error:` and survivors render `⚠ partial results`.
- 2026-11-26 · deferred to Task 2 (owner: Task 2 implementer) · a batch whose only outcomes are failures still receives the whole-batch “source(s) indexed. Refine your query terms” line in `handleSearch`; implementation step 4 of [Emit query groups](implementation.md#emit-query-groups) restricts that statement to batches where all passes succeeded. Fix with the Task 2 writer; verify under 2.4. Error causes are not yet bounded to 192 bytes (Task 2 reply budget).
- 2026-11-26 · implemented · `store.PartialSearch` / `ContentStore.SearchPartialContext` and `vault.PartialChunkSearch` / `VaultStore.SearchChunksPartial` record the first layer failure with `sync.Once` (read after retrieval joins) and keep surviving ranked rows; knowledge access tracking still runs on survivors. Both closures also `slog.Warn` every layer failure, preserving the operator log best-effort search emitted before (review P3). `SearchWithFallbackContext` (strict and best-effort) and `SearchChunks` are unchanged in policy. `handleSearch` reports layer failures in the existing per-corpus status (`Error:` with zero survivors, `⚠ partial results (…)` otherwise); `handleVaultSearch` now passes rows accompanied by a diagnostic to the formatter with the same banner.
- 2026-11-26 · verified (author-run, capy runs) · new tests `TestSearchPartialContext`, `TestSearchChunksPartial`, `TestSearch_KnowledgeLayerFailure`, `TestSearch_VaultLayerFailure`, `TestVaultSearch_LayerFailure` cover one failing layer with a survivor, both layers lost, failing vault beside healthy knowledge, clean-miss controls and the preserved strict/best-effort policies; `TestSearch_VaultAvailabilityFailure` still shows the availability notice once. Post-fix `go test -race -count=2` focused (server/store/vault) and `-race -short` vault + retrieval pass; full non-race store, vault, vault/tui, server, retrieval, execution pass; pre-fix full `-race` store passes. Failures are injected by dropping FTS tables; SQLite reports the drop at prepare or during iteration, so assertions accept both phases. Not exercised: row-scan failure injection, a corrupted (not dropped) table, full `-race` server package after the logging fix.
- 2026-11-26 · reviewed · isolated review (code-reviewer + clink `codex`, OpenAI provider assumed from PAL's shipped manifest; `claude` excluded as authoring provider). No P0–P2. Two P3s (lost operator logging, ambiguous `Results` doc) fixed and re-reviewed clean by both. Accepted residual: MCP-path logs no longer carry the sanitized FTS expression attribute that `execSearch` logged; recover it inside `execSearch` only if operators need it.
- 2026-11-26 · observed, unrelated pre-existing (owner: unassigned) · `cmd/capy` `TestCodexMixedStartupContext` fails reading `../../.codex/scripts/session-start.sh`, which commit `8ee8f85` deleted; the package also needs ~820 s, exceeding `go test`'s default 10 m timeout. Next action: update or remove the test's dependency on the deleted toolbox script, and raise the package timeout (e.g. in the Makefile); verify with `go test -tags fts5 ./cmd/capy/`.

## Task 2. Bound federated replies and support a fresh retry

- **Status:** done
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Reply budget](implementation.md#bound-the-complete-reply) · [Tool guidance](implementation.md#wire-the-tools-and-document-recovery)

- [x] 2.1 Route every keyword return path in `handleSearch` through bounded rendering. Reserve the group frame, request notices and admission footer. Preserve the error flag, budget and stats behavior on validation, empty-store, blocked and normal paths.
- [x] 2.2 Sanitize complete display fields before extraction and clipping. Exercise an interior multibyte cut, a secret crossing a cut, an oversized label, a long invalid kind, and a long batch of misses. Check valid UTF-8, the complete JSON-decoded text byte count and tracked response bytes.
- [x] 2.3 With a fixed clock and ranked corpus, cut call 3 after one of two hits in a boundary query. Verify the notice, then retry from that query on call 4 and after reset. Check the effective limits and ranks on both retries. Count query dispatches to show that the original suffix never ran.
- [x] 2.4 Update empty-store, miss, block, footer and tool-schema guidance. Check one shared hint for many genuine misses, both historical ephemeral selectors, `capy_runs` for new output, and absence of whole-batch miss claims for an unsearched suffix.

### Execution context

- 2026-11-26 · observed · base `3e1188f` (branch `compact-search-replies`, clean). `handleSearch` bounds only accumulated hit-block bytes and checks the cap *before* each query (`totalSize > searchMaxTotalBytes`), so frames, misses, causes, hints and the footer are unbounded and one query can overshoot by its whole group. Labels, titles, query echoes and layer causes are unclipped. `ExtractSnippet` (`internal/server/snippet.go`) can exceed `maxLen` (markers/joins), cut inside a rune (prefix fallback) and misplace windows because offsets come from `strings.ToLower(content)`. The ephemeral miss hint is emitted per missed query.
- 2026-11-26 · observed · knowledge content is redacted at index time (`store/index.go` `indexContent`), but `IndexChunked` redacts only the transcript, not chunk bodies, and source labels are never redacted; vault chunk text and titles are redacted by the scanner, project paths are not. Display-time redaction therefore protects real rows.
- 2026-11-26 · observed · `searchLimitArg` defaults an absent limit to 2 and caps at 2 before `throttle.advance`; validation errors stay untracked (`TestSearchBudgetInvalidRequests` asserts empty stats). `budget.notice()` and the block/empty-store copy still point at `batch_execute`. New executions no longer index into knowledge (run storage), and the design directs a blocked caller to the reset time; `capy_batch_execute` can still search knowledge through `query_scope: "global"` (superseded note, corrected 2026-11-26 after Codex review).
- 2026-11-26 · approach · a request-local `replyWriter` in `internal/server` enforces the implementation table (40,960 total; 2,048 notice reserve; 1,024 frame reserve before dispatch; ≤3,072 per hit with a 1,500-byte passage). Groups are numbered `## N. <query preview>`; hits are committed whole in federated rank order; the first non-fitting hit or frame stops the batch and a reserved completion notice names the first unfinished query, `shown of returned` (or unsearched) and the inclusive retry range. Hints (ephemeral, vault disabled/backlog, whole-batch miss) are request-wide and emitted once. Display fields are redacted on the complete value, made valid UTF-8, flattened to one line where metadata, and clipped on rune boundaries with the marker inside the cap. `handleSearch` delegates to an internal `search` that takes the reply limit so the cut test can force a boundary without depending on exact rendered sizes; a nil-by-default dispatch hook counts query dispatches in tests. The vault adapter's `chunkSnippet` reuses highlights only when stripping markers reproduces the redacted content and the content has no marker bytes. Sidecar attribution and references remain Task 3; dedicated vault rendering, its retry wording and its schema description remain Task 4.
- 2026-11-26 · implemented · `internal/server/search_reply.go` (budget constants, `displayField`/`displayPassage`/`argPreview`, `replyWriter`, `queryFrame`, `completionNotice`); `handleSearch` → `search(ctx, req, replyMax)` with numbered groups, whole-hit commits, `searchHints` (one miss hint naming `capy_runs` and both ephemeral selectors; vault disabled/backlog hint when the queries that ran returned no hit; whole-batch miss line only when every query ran cleanly — resolves the Task 1 deferral); bounded `formatKnowledgeHit` (adds `(knowledge)` to a `session:`-prefixed label) and `formatVaultHit` fields; `ExtractSnippet` cap now includes markers/joins, cuts on rune boundaries, maps case-folded matches to original offsets and reuses highlights only when they reproduce the content; vault `chunkSnippet(content, highlighted)` redacts first and discards non-matching highlights. Empty-store, block and footer copy no longer point at `batch_execute`; `capy_search`/`include_kinds` schema text describes numbering, the inclusive retry and `capy_runs`. Test-only `Server.searchDispatched` hook counts dispatches. Existing tests updated for `## N. query` headings and the new guidance wording (old ephemeral hint named `capy_execute_file`, no longer an ephemeral producer).
- 2026-11-26 · verified (author-run, capy runs) · new `search_reply_test.go` (field/snippet bounds, hit-block and notice-reserve worst cases, sanitized label/passage secrets, interior multibyte cut, long invalid kind untracked, 3,000-query miss batch, retry across call 3 → tapered call 4 → reset with dispatch counts, guidance) and extended `TestChunkSnippet`. Decoded JSON text is checked for UTF-8, ≤ 40,960 bytes and equality with the tracked byte delta. Full non-race `server`, `vault`, `store`, `retrieval` pass except one wording test, fixed afterwards; post-fix `-race` focused server run (`TestSearch*|TestVaultSearch|TestTool|…`) and earlier `-race -count=2` focused server+vault runs pass with no data race. The call-3 cut uses a forced reply of notice reserve + one frame + one hit, so the boundary follows from the size invariants rather than exact rendered bytes. Not exercised: the full server package after the final hint wording fix (focused race run only), real-budget boundary at exactly 40,960 bytes with maximal hits (Task 4.3 covers the vault maximum).
- 2026-11-26 · reviewed · isolated review, six rounds: internal `kk:code-reviewer` plus clink `codex` (OpenAI, assumed from PAL's shipped manifest; `claude` excluded as the authoring provider; `gemini` retired; `agy` not installed; no PAL API providers). Round 1: codex P2s — vault `chunkSnippet` removed literal STX/ETX after redaction (could rejoin a split secret); `formatVaultHit` shortened `ParentUUID` before redaction (inherited); call-3 notice lacked reduced-limit guidance. Both reviewers (corroborated) P3: one-line metadata escaped only CR/LF. Other P3s: weak handler redaction assertions, notes/`limit` description, untested all-failed batch omitting the whole-batch miss line, untested highlight reuse, six-parameter `completionNotice`. Fixed: `displayField` uses `terminal.EscapeField`; parent redacted before shortening; `partialNotice` (reduced-limit sentence only when `returned > 1`) / `unsearchedNotice`; `capy_search.limit` text "default and maximum: 2"; stronger tests. Rounds 2–4 iterated on the marker-byte branch (strip-then-redact could close a cross-message `<private>` span early; redact-then-strip could leave a labelled value's suffix; a two-order agreement check still showed an index-time `[REDACTED_SECRET]\x02SUFFIX` joined). Round 5 (decision, author): redact the complete content once and show literal STX/ETX as U+FFFD, never deleting them, in both vault and knowledge passages — display never joins fragments; a suffix after any delimiter remains the existing sanitizer limitation (HEAD showed it too). Codex confirmed no display-caused disclosure; its last P3 (normalize the query the same way for knowledge snippet selection) fixed in round 6. Final verdicts: code-reviewer APPROVE (round 5, no findings), codex APPROVE (round 6, no findings). No P0/P1, so nothing indexed as `kk:review-findings`.
- 2026-11-26 · verified · Task 1 deferral (whole-batch miss claim after failures) now covered by `TestSearch_KnowledgeLayerFailure` (`NotContains "Refine your query terms"` after both knowledge layers fail).
- 2026-11-26 · deferred (owner: Task 3 implementer) · `federateHits` formats (redacts and extracts snippets for) up to 2×limit hits before cutting to `limit`; Task 3 already moves typed hits through the merge and renders only the final order. Next action: render after the merge in Task 3; verify with the 5.1 bench comparison.
- 2026-11-26 · deferred (owner: Task 5 implementer) · `docs/reference/tools.md` still describes the old `## <query>` / "output cap reached" format; document numbering, the inclusive retry and reduced-limit sentence, the `limit` default, and that literal STX/ETX bytes appear as U+FFFD in passages. Verify against both shipped handlers under 5.2.
- 2026-11-26 · release note (owner: unassigned) · the MCP server attached to this session still runs the pre-change binary (old footer); rebuild and restart capy MCP servers to activate. No data, schema or CLI change.
- 2026-11-26 · verified (author-run, final candidate) · gofmt/vet clean; full non-race `go test -tags fts5 -count=1` of `internal/server` (295 s), `internal/vault` (357 s), `internal/store` (91 s), `internal/retrieval` pass; focused `-race` server+vault run (search, snippet, reply, vault, tool tests) passes with no data race. Not exercised: a real-budget reply filled to exactly 40,960 bytes with maximal hits (bounded by per-unit tests; Task 4.3 covers the vault maximum); full `-race` server package.

## Task 3. Cite repeated passages in federated rank order

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Passage identity](implementation.md#identify-passages-through-ranking)

- [ ] 3.1 Carry the winning knowledge rowid and table, and the vault session/sidecar and chunk coordinate, through retrieval and the existing rank merge. Reuse the browse coordinate if present. Attach navigation to the first full block and allocate its reference only after it fits.
- [ ] 3.2 Compare a duplicate-heavy reply with its expanded source/rank sequence. Cover a different window of the same chunk, colliding titles or clipped prefixes, distinct FTS tables sharing a rowid, and identical passages in different corpora. Verify that a later query can consist entirely of references.
- [ ] 3.3 Label the chunk start and show its sidecar ID when present. If the brief has landed, verify the latest-exchange action fits in the first eligible full hit and can be reached from a reference. Use main and sidecar hits from the same session with equal line numbers and text, and split windows with a shared first line. Verify distinct attribution and correct reference reuse. Exercise chunk index zero and a missing coordinate that still produces a full hit.

## Task 4. Apply the contract to dedicated vault search

- **Status:** pending
- **Depends on:** Task 3
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Reply budget](implementation.md#bound-the-complete-reply) · [Vault arguments](implementation.md#wire-the-tools-and-document-recovery)

- [ ] 4.1 Use the writer and vault hit formatter in `handleVaultSearch`. Exercise disabled, an oversized invalid date, clean miss with backlog, partial FTS failure, and main/sidecar attribution. Check existing project/date filters and the absence of federated admission or footer.
- [ ] 4.2 Reject malformed native and serialized query arrays before storage or tracking. Cover a non-string member with a valid fallback supplied, a valid serialized array and the single-query fallback. Confirm original input positions for a boundary query.
- [ ] 4.3 Exercise a boundary query that ran with zero hits fitting, a partly shown query and a boundary query skipped before dispatch. Assert counts and untouched suffixes. Retry the boundary query alone with ten distinct maximum-size vault hits and confirm all ten fit within 40,960 decoded bytes.

## Task 5. Verify and document the public behavior

- **Status:** pending
- **Depends on:** Task 4
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification](implementation.md#verification)

- [ ] 5.1 Run focused and full FTS5 tests with synthetic knowledge and vault keys. Compare pre/post `make bench-quality` results and record fixed-fixture rank parity, reply bytes saved on duplicates, single-query overhead and timing.
- [ ] 5.2 Update both tool descriptions and `docs/reference/tools.md` with query numbering, references and retrying the inclusive suffix. Document field omissions and the vault validation change. Walk through a mixed batch with a repeated passage, distinct excerpt, partial failure and interrupted final group, then issue the advertised retry.
- [ ] 5.3 Review the Go changes and check both shipped handlers against the reply contract before closing the tasks.

## Dependency Graph

```text
Task 1 -> Task 2 -> Task 3 -> Task 4 -> Task 5
```
