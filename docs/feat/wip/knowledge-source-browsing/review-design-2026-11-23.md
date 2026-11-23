# Design review of knowledge source browsing

**Original assessment at `6677d7b` — CONCERNS_FOUND.** Browsing stored sections by an exact source ID is useful and feasible. The original proposal opened the beginning of a manual even when the reader had already found a particular section. A reader holding only a fetch receipt could also lack the ID needed to start. The [resolution](#resolution) records the subsequent design decisions.

Reviewed capy `6677d7b`, including the [design](design.md), [implementation plan](implementation.md), [tasks](tasks.md) and the [eight-proposal overview](../knowledge-session-and-diagnostics.md). The neighboring search, chunking and fetch proposals were considered for interactions. The findings below apply to knowledge source browsing. There are three high, five medium and one low finding.

## Basis for the review

The last sync ended at `0dfbe8de`, 167 commits beyond the tag in the feature name. Capy commit `c0bfff2` updated that gitlink and `1a2cfc3` subsequently removed it. The current capy HEAD adds the proposals. The separate local reference checkout is at `0fe3f5da`, two commits past v2.2.2. The comparison spans 2,517 commits before filtering. I compared the old store/server/search code with the new shared store, index and search code and the behavior tests. CI, generated bundles, docs-only changes and unsupported runtime/host integrations were excluded from the behavioral comparison. [Previous sync](../../done/upstream-sync-v1.0.169/design.md) · [Exact upstream comparison](https://github.com/mksglu/context-mode/compare/0dfbe8de71abcb637a07dd6444bee5823c3186fc...0fe3f5dadfade06758de018a90e6e860274fb8de)

To reproduce the feature-specific portion of the diff locally, run

```sh
git -C context-mode diff 0dfbe8de 0fe3f5da -- \
  src/server.ts src/store.ts src/search \
  src/core/store/fts.ts src/core/store/schema.ts src/core/tools/search.ts \
  src/core/tools/index.ts tests/prd8-index-search.test.mjs
```

| Upstream behavior in that range | Consequence for capy |
| --- | --- |
| Single-source indexing, including a URL, returns a handle. A real label can have the spelling of a handle. An upstream fixture uses a 17 KB label and checks that content and a usable continuation still fit. [Handle and collision change](https://github.com/mksglu/context-mode/commit/a621d67d595a2448682b00e5365f2d0053b91347) | Separate decimal ID arguments fit capy's existing literal-substring selector. Keep the ID visible on the response where the reader first learns about the source. |
| Empty queries on a resolved ordinary source open a metadata card and page its **label**. Separately, a source-scoped keyword query with no matches can list chunks by position. The positional read selects page identities before projecting bodies. [Source card and search dispatch](https://github.com/mksglu/context-mode/blob/0fe3f5dadfade06758de018a90e6e860274fb8de/src/core/tools/search.ts#L117) · [Positional fallback](https://github.com/mksglu/context-mode/commit/ce89bfc496829a631870b297491b8ec94ef40bef) | An explicit ordered preview page is a capy product choice. It can serve the navigation use case independently of ranked retrieval, whole-label paging and run-output recovery. |
| Unknown-source handling distinguishes an absent source from an exhausted page, even with an offset. Resolution accounts for the requested scope. [Upstream correction](https://github.com/mksglu/context-mode/commit/8dfee0cf608afec6f22d82139da4f68c9eff1cdb) | Use the selected knowledge DB as the scope, distinguish EOF from an invalid selection and give the caller a recovery action. |

I searched both `kk:arch-decisions` and `kk:review-findings`. Relevant records covered separate knowledge/vault ownership, fetch validation versus read access, the cost of scanning file-backed sources, and an 8,000-title inventory that exceeded its response budget. Current code and ADRs qualify those historical notes. Captured commands now have owner-local run IDs, general cleanup protects durable knowledge, and stored legacy session rows can still exist. [Run contract](../../../reference/runs.md) · [Knowledge tool reference](../../../reference/tools.md) · [ADR-027](../../../adr/027-vault-is-sole-session-store.md) · [ADR-035](../../../adr/035-fetch-validation-and-bounded-stale-checks.md)

## P0 — Critical

None.

## P1 — High

### 1. A ranked hit gives no starting position for “the next section” · INCOMPLETE

The overview promises the next section of a known manual. Suppose a hit identifies section 80 and the reader needs section 81. The hit supplies only `SourceID`, so the first browse call starts at section 1. Even with a fresh default MCP budget devoted entirely to browsing, only 11 sections fit before the first reset (three calls of two, five of one). The discovery call itself can consume part of that window. A title lookup cannot establish position when headings repeat. **Confidence 9/10.** Opening a manual at its beginning is a valid narrower product, and the documents currently describe both expectations. [Workflow](../knowledge-session-and-diagnostics.md) · [User contract](design.md#user-contract) · [Current result fields](../../../../internal/retrieval/retrieval.go#L28) · [Throttle](../../../../internal/server/server.go#L45)

Decide between browsing from the start and continuing from a hit. For the former, align the use case and acceptance example. For the latter, supply a continuation tied to the winning hit's **primary-table** section, and test a late hit with repeated headings. The two FTS tables own their rowids independently. The table-local coordinate proposed for compact replies needs a verified mapping before it can become a primary-table browse cursor. [Compact replies implementation](../compact-search-replies/implementation.md#identify-passages-through-ranking)

### 2. A fetched manual can require a successful keyword search to get its browse ID · INCOMPLETE

The design directs the reader to an “ordinary labelled search hit.” The label filters candidates, while the query still needs a match. A single-URL cache hit returns the friendly label and chunk count without a body or ID. Two different URLs can share that label. A reader who has just selected the exact URL can therefore be sent back to the original vocabulary and ambiguity problem. **Confidence 10/10.** The cache-hit formatter and the composite URL storage key exhibit both conditions. [Design](design.md#user-contract) · [Fetch cache hit](../../../../internal/server/tool_fetch.go#L121) · [Literal source filtering](../../../../internal/store/search.go#L110)

Return the ID on single-URL fresh, unchanged and cache-hit outcomes. Fresh and unchanged indexing already return it. For a cache hit, select the ID in the same metadata read that supplied the count. Verify that two URLs with one friendly label each open the right source directly from their own receipt. The directory aggregate can stay bounded. Document how far discovery extends if all the reader has is an old label.

### 3. The responses that introduce an ID can still echo a full, unredacted label · MISSING

The page has field limits and a redaction rule. The proposal also edits the responses that lead into it. Today's MCP index formatter interpolates the complete label twice, once into a suggested search. The ranked MCP formatter inserts the label and title verbatim. CLI index quotes the label but places no display bound on it. The store sanitizes input **content** before hashing and stores the supplied label separately. Simply adding an ID to those strings preserves the very long-label failure upstream fixed and can repeat a recognized secret from the label. **Confidence 9/10.** “Use the new formatter for safe fields” could cover these sites, but the limits and acceptance tests cover page entries only. [Implementation plan](implementation.md#mcp-page) · [MCP index output](../../../../internal/server/tool_index.go#L67) · [Ranked hit formatter](../../../../internal/server/tool_search.go#L337) · [CLI index output](../../../../cmd/capy/index.go#L124) · [Store write](../../../../internal/store/index.go#L63)

Apply a bounded, redacted metadata renderer to the touched discovery responses, including retry hints. Use the validated decimal ID in browse instructions. Exercise a long multibyte label with a synthetic secret crossing the display cutoff. Verify that the ID remains copyable on index, ranked hit and page. Keep existing keyword snippet limits independent of the page preview limit.

## P2 — Medium

### 4. A fast middle page can conceal an expensive first or final page · TECH_RISK

The plan measures “one late page.” Rowid seeking helps when the requested rows immediately follow the cursor. It does little to locate the first row of a late source or establish EOF for an early one, because `chunks.source_id` is UNINDEXED. In a synthetic encrypted fixture using capy's pinned SQLite 3.51.2 driver, the identity-only query `WHERE source_id=? AND rowid>? ORDER BY rowid LIMIT 4` produced `SCAN chunks VIRTUAL TABLE INDEX 64:>`. Results below are medians of five local calls with 300-byte synthetic bodies. **Confidence 10/10 in the scan shape.** Deployed latency depends on the corpus, cache and storage. [Performance assumption](design.md#assumptions) · [Verification plan](implementation.md#verification) · [FTS definition](../../../../internal/store/schema.go#L25)

| Identity lookup | 1,000 unrelated sections | About 50,000 unrelated sections |
| --- | ---: | ---: |
| First page of the later source | 109 µs | 50.7 ms |
| Interior page of that source | 9 µs | 10 µs |
| Final partial page of the early source | 111 µs | 50.4 ms |
| Cursor at EOF of the early source | 107 µs | 50.2 ms |

Set a representative store size and an acceptable interactive cost. Measure first, interior and final requests on that layout before accepting the query shape. Include one large legacy field in the full-page measurement because redacting the whole field still costs work even when only 500 bytes are displayed. The existing search-quality benchmark covers ranking. A secondary index or migration is a decision to make against the page measurements.

### 5. The wire-to-store conversion can silently admit bad limits or return false EOF · TECH_RISK

The MCP limit helper already clamps every valid positive value to two. Passing its result to a browse range check would admit `limit: 9` and charge the shared budget. At the SQL boundary, the wire IDs are strings while capy inserts integer `source_id` values into FTS. In the encrypted fixture, the same page query returned four rows with an integer parameter and zero with the equivalent decimal text. **Confidence 10/10.** The clamp is in current code and the parameter difference reproduced on the pinned driver. [User contract](design.md#user-contract) · [Limit coercion](../../../../internal/server/coerce.go#L144) · [Chunk insertion](../../../../internal/store/index.go#L221)

Validate the original browse limit against 1–8 before throttle admission. Parse both canonical ID strings into positive `int64` values and bind those integers. Reject an orphan `after_section` / `--after-section` even when keyword arguments are otherwise valid. Test 8 and 9, a valid ID above JavaScript's exact-integer range, overflow and a misplaced cursor. Keep the current keyword limit behavior.

### 6. An unchanged index receipt can pair the right ID with the wrong section count · INCONSISTENT

On ordinary same-hash indexing, `IndexResult` contains the retained ID but leaves both chunk counts zero. MCP presently formats those zeros as an indexing result. CLI re-reads the count by **label** after the transaction, so a concurrent replacement can supply its count alongside the earlier ID. Fetch revalidation already reads retained metadata for the exact ID inside the transaction. **Confidence 10/10.** The existing store inventory test explicitly asserts the ordinary dedup zero. [Terminal plan](implementation.md#terminal-page) · [Dedup paths](../../../../internal/store/index.go#L184) · [CLI lookup](../../../../cmd/capy/index.go#L111) · [Inventory test](../../../../internal/store/inventory_test.go#L36)

Have the receipt describe one committed incarnation. An unchanged receipt can print its ID and dedup status alone, or obtain the retained count for that ID in the deciding transaction. Specify the meaning of counts for existing callers before changing `IndexResult`. Test ordinary inline and file dedup as well as a later replacement.

### 7. Empty pages and errors need a CLI and MCP outcome contract · INCOMPLETE

The design distinguishes an unknown source, a foreign cursor, a storage failure and a valid final page in prose. An implementer could print different messages for all four and return success for each. Existing keyword paths include both successful zero-match replies and in-band partial errors. Scripts and MCP clients need to know when to stop, refresh their selection or retry. **Confidence 9/10.** The plan specifies error distinctions and lacks a mapping to exit status or MCP `IsError`. [User contract](design.md#user-contract) · [MCP search](../../../../internal/server/tool_search.go#L83) · [Existing CLI convention](../../../reference/tools.md#knowledge-search-from-the-terminal)

Specify CLI exit 0 / MCP success for an empty source and for a valid end cursor. Use CLI exit 1 / MCP error for missing or replaced IDs, foreign cursors and storage failures, with distinct diagnostics for each recoverable condition. A missing ID need not identify the historical cause of its disappearance. Assert that valid MCP failures carry the admission notice and that invalid requests leave the counter untouched.

### 8. A surviving knowledge row of kind `session` needs an explicit eligibility rule · AMBIGUOUS

“Including legacy chunks” can mean an old chunk format or a retained transcript row. Capy's schema allows both. An explicit label filter bypasses kinds and can still surface a legacy knowledge hit. Its numeric source ID can then reach this feature even on a host with the vault disabled. **Confidence 9/10.** ADR-027 preserves existing rows, and the source filter bypass is explicit. [Storage and lifetime](design.md#storage-and-lifetime) · [Kind filtering](../../../../internal/store/search.go#L165) · [Legacy session decision](../../../adr/027-vault-is-sole-session-store.md#d2-the-schema-check-keeps-session-we-stop-writing-not-allowing)

The simplest consistent rule is to allow an explicitly selected surviving knowledge row and describe its provenance as knowledge. If product policy excludes such rows, suppress unusable browse links on those hits and define the response. Test one legacy row with the vault disabled and one genuine vault hit, which must retain its session identity.

## P3 — Low

### 9. “Ranked knowledge hits” also describes the global batch result · AMBIGUOUS

The tasks cover `capy search` and `capy_search`. Global batch search also returns ranked knowledge hits in `knowledge_results`. Its adapter drops `SourceID` while constructing each hit. **Confidence 9/10.** This is an existing public output with an independent envelope and byte limits. Narrow the wording to the two intended search entry points, or explicitly include the optional knowledge results and verify their budget. Run IDs themselves remain a separate concept. [Design](design.md#user-contract) · [Global knowledge adapter](../../../../internal/execution/knowledge.go#L21)

## Clean areas and feasibility

- The core storage choice fits the current implementation. Source IDs use `INTEGER PRIMARY KEY AUTOINCREMENT`. Replacement deletes and reinserts in one transaction, same-hash writes retain the ID, and the writer inserts a source's chunks in slice order. A single read snapshot for metadata, cursor ownership, page and lookahead can describe one incarnation. An encrypted probe retained primary rowids 4–7 after deleting earlier rows, FTS optimize, rebuild and VACUUM. The implementation acceptance test should exercise capy's maintenance path. [Schema](../../../../internal/store/schema.go) · [Index transaction](../../../../internal/store/index.go#L129) · [Maintenance](../../../../internal/store/store.go#L504)
- The dedicated read can skip ranking, vault federation and file refresh. Computing stored byte lengths before display conversion and redacting whole returned fields before clipping are compatible. The inventory already measures bytes as BLOBs, including text after an embedded NUL. Fetching only page bodies and a lookahead identity avoids materializing an entire manual. [Current inventory](../../../../internal/store/inventory.go#L24) · [Existing boundary fixtures](../../../../internal/store/inventory_test.go#L14)
- The CLI and MCP tasks each deliver a testable user path. The dependency graph, size tags, assumptions and exclusions are present. Full-body export and changes to ranking can be left outside this feature. The remaining acceptance work is about the navigation promise, public receipts, failure semantics and actual page cost.

The runtime evidence above comes from isolated synthetic SQL probes against the page query and maintenance operations. There is no browse handler on this branch to test end to end. The Go feature suite and search-quality comparison belong to implementation acceptance.

## Resolution

Rechecked the nine findings against the store writers and queries, both search entry points, index and fetch receipts, shared admission and the separate global batch result. Each finding identified a real gap in the original documents. Two called for a choice about scope. The revised [design](design.md), [implementation plan](implementation.md) and [tasks](tasks.md) settle those choices as follows.

| Finding | Resolution in the revised design |
| --- | --- |
| 1. Continue from a hit | Support direct next-section navigation. One correction to the original recommendation above: a primary-table mapping would be necessary **only** for a primary-only reader. The original writer (`58dc5f9`) and the current writer store identical ordered sections in both tables in one transaction. Keep the winning row's table and continue inside that mirror. In an independent FTS fixture with shifted rowids, using the trigram hit's rowid in the primary table returned section 82. The tagged position returned section 81 before and after maintenance. |
| 2. Fetch discovery | Provide the correct source ID on single-URL new/changed fetches, unchanged revalidation and cache hits. The cache key still distinguishes URLs with the same friendly label. |
| 3. Metadata disclosure | Bound and redact all discovery metadata, including repeated label hints. Give usable positional arguments separately from display text. |
| 4. Page cost | Measure four page shapes and rendering of a legacy field. Use the documented 50,000-section workload and 250 ms interactive target to decide whether to revise the read path. |
| 5. Validation and bindings | Validate browse inputs before admission, parse IDs into signed 64-bit integers, and bind them as integers. Block before storage access and report the effective page size. |
| 6. Retained counts | Preserve ordinary dedup counters for current callers. Return an explicit retained count from the deciding transaction alongside the source ID. |
| 7. Outcome contract | Specify CLI exit status, MCP error status, budget accounting and how to recover for each expected outcome. |
| 8. Legacy sessions | Permit explicit selection of surviving knowledge rows of every stored kind. Mark legacy session rows as knowledge and keep vault hits on their own UUID/line interface. |
| 9. Global batch scope | Limit ranked-hit navigation to terminal knowledge search and `capy_search`. The global batch result belongs to the run interface. |

The second pass aligned MCP ranked metadata limits with compact replies on the final escaped text and preserved the CLI keyword limits before quoting, as specified by knowledge excerpts. The hit position fits inside the compact hit budget. An explicitly requested page size survives continuation. The pass also found the directory indexer's post-commit label lookup. The plan uses each file's committed retained count for that aggregate.

**Design disposition — ready for implementation.** The new page timings, CLI/MCP behavior and recovery cases are implementation acceptance checks. The measurements earlier in this report describe the baseline queries.
