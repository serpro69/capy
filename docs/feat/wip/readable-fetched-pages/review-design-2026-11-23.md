# Design review of readable fetched pages

**Assessment — CONCERNS_FOUND.** The feature is worth building. Resolving a manual’s links against its final URL and preserving the last good copy when a fetch produces no article both solve concrete problems. The existing Go fetcher and Markdown converter can support them. Before implementation, settle which content wins inside a page-wide `main`, where redaction happens, and how much Markdown link expansion may allocate. The review found three P1 and five P2 issues. There are no P0 or P3 findings.

Reviewed the design, implementation plan and all three tasks at capy `6677d7b` using standard `kk:review-design`. [Design](design.md) · [Implementation](implementation.md) · [Tasks](tasks.md)

## Upstream comparison and capy evidence

The previous sync ended at `0dfbe8de`, 167 commits past the v1.0.169 tag. Capy recorded that gitlink in `c0bfff2` and removed it in `1a2cfc3`. The local reference checkout ends at `0fe3f5da`, 2,517 commits later. I filtered the changed-path inventory for runtime source, tests and fixtures, excluding CI, bundles, docs-only work and unsupported hosts, then followed the old and new fetch, conversion and FTS paths. The endpoint is the checked-out revision used for this review. [Last sync design](../../done/upstream-sync-v1.0.169/design.md) · [Upstream comparison](https://github.com/mksglu/context-mode/compare/0dfbe8de71abcb637a07dd6444bee5823c3186fc...0fe3f5dadfade06758de018a90e6e860274fb8de)

The old TS fetcher already used Turndown with tables and chrome removal. The v2 change in `7b47de82` parses a tree, selects a root, renders structured text, resolves links against the final URL, and checks actual search answers on saved manuals. It also stores a raw companion, falls back to raw input on converter failure, and uses a word-count/markup-ratio test for sparse pages. Upstream drops fragment-only destinations. The later header fix in `8c78852b` sends a User-Agent and `Accept: */*` on each hop. Capy’s single sanitized source, requested-URL cache identity, fragment preservation and preference for textual media are deliberate product choices. Proxy and host-specific changes do not establish requirements for this feature. [Original converter](https://github.com/mksglu/context-mode/blob/0dfbe8de71abcb637a07dd6444bee5823c3186fc/src/server.ts#L3096) · [Readable HTML change](https://github.com/mksglu/context-mode/commit/7b47de82a06c1d9e154eba77e9bca21ea12398b8) · [Header change](https://github.com/mksglu/context-mode/commit/8c78852b7aa247508fd59d96b7ca863bfc79bb84)

`capy_search` recovered the earlier `kk:arch-decisions` finding on fetch validation and the `kk:review-findings` note on response budgets. Current code and ADRs confirm that completed body admission sets the timestamp, successful indexing commits it, and same-hash revalidation preserves indexing age. Single and batch already share `indexFetchedContent`. The table plugin, request-order batch results, per-item failure handling and separate limits on downloaded bodies and indexed sources are already available. [Fetch architecture](../../../architecture.md#fetch-and-index-capy_fetch_and_index) · [Validation decision](../../../adr/035-fetch-validation-and-bounded-stale-checks.md) · [Source admission](../../../adr/022-source-size-guard-and-db-bloat-prevention.md)

I ran the pinned Go converter with capy’s current plugins and chrome rules on all six saved upstream pages. The comparison below changes only the root passed to the converter. It measures Markdown bytes and shows what root selection buys on its own. It does not measure search quality or the proposed pruning rules. [Fixture provenance](https://github.com/mksglu/context-mode/blob/0fe3f5dadfade06758de018a90e6e860274fb8de/tests/fixtures/html/README.md) · [Capy converter](../../../../internal/server/tool_fetch.go#L571)

| Saved page | Whole document | First main, or body | Observation |
| --- | ---: | ---: | --- |
| React useEffect | 49,639 | 49,639 | The cleanup example already includes both connection calls in order. |
| MDN Array.map | 13,090 | 13,022 | The page has a main and no article. |
| Next.js layouts | 12,595 | 12,555 | The main still starts with breadcrumbs and menu text. |
| Docusaurus config | 39,375 | 39,147 | The article H1 is in a header that capy currently removes. |
| GitHub Express README | 16,078 | 15,724 | Branch controls and the repository file table precede the README. |
| Excalidraw shell | 0 | 0 | Empty conversion already occurs. Today that successful conversion can reach the store. |

## P1 — resolve before implementation

### 1. The first main can still index the file listing ahead of the article · INCOMPLETE

The GitHub fixture has one main containing both a repository file table and a single README article. The file table survives removal of navigation, headers, footers, form controls and explicitly hidden nodes. It consists of ordinary visible table cells and links, exactly the elements the design promises to preserve in a reference manual. A fixture with a sidebar outside main can pass while the 384-byte batch preview still starts with repository UI. At the other extreme, removing every header loses the Docusaurus article’s own heading. **Confidence 10/10** from the saved trees and the converter comparison. [Selection rule](design.md#choosing-and-rendering-the-article) · [Task 1](tasks.md#task-1-index-the-visible-article) · [Upstream page cases](https://github.com/mksglu/context-mode/blob/0fe3f5dadfade06758de018a90e6e860274fb8de/tests/prd8-index-search.test.mjs#L2742)

Decide when a sole readable article inside main wins and when surrounding main content belongs in the index. A small capy-owned fixture with a file table outside the article and an API table inside would test that decision. Check the single and batch previews and a source-scoped search for an answer in the API table. Keep the main-without-article and multiple-article pages as controls. The ignored reference checkout is useful for comparison, but a clean capy checkout must own its regression fixture.

### 2. The preview and store need an explicit redaction and admission order · TECH_RISK

Today the HTML handler gives rendered Markdown to `IndexFetched` and returns its own copy for the preview. The store checks the converted byte length, detects type, then strips secrets before hashing and chunking. Returning the caller’s copy leaves recognized secrets in previews. Moving the only redaction earlier can instead change which documents pass `max_source_bytes`. A literal `<private>` block adds another ordering constraint. The converter renders children of an unknown element and discards its wrapper, while capy’s private-block sanitizer recognizes the wrapper. Post-render redaction alone cannot recover that boundary. **Confidence 10/10** from the handler, store and pinned renderer. [Task 1.2](tasks.md#task-1-index-the-visible-article) · [HTML handler](../../../../internal/server/tool_fetch.go#L373) · [Store admission](../../../../internal/store/index.go#L50) · [Sanitizer](../../../../internal/sanitize/sanitize.go#L50) · [Converter fallback](https://github.com/JohannesKaufmann/html-to-markdown/blob/v2.5.0/converter/render.go#L35)

Specify how private DOM subtrees are suppressed before rendering and how the store and preview share the final redacted Markdown. Preserve the converted-source size check before ordinary secret replacement can shrink it. Verify a harmless private-block sentinel and a recognized synthetic token in both previews and indexed text. An over-limit conversion that would shrink below the limit after redaction should still exercise the documented admission policy.

### 3. The HTTP body cap does not bound the new link-expansion work · TECH_RISK

Resolving many short references repeats the final origin and directory in each rendered destination. A link-heavy manual can fit the 10 MiB HTTP limit and exceed the configured source limit during rewriting. The pinned converter builds its output in a buffer before returning it. The current 2 MiB default store check runs afterward, and the batch path can already be retaining other response bodies. An iterative DOM walk or depth limit addresses a different cost. **Confidence 9/10** from the buffer and store call order. Peak memory for a future implementation has not been measured. [Design assumption](design.md#assumptions) · [Link plan](implementation.md#link-origin-and-negotiation) · [Batch buffering](../../../../internal/server/tool_fetch.go#L466) · [Converter output buffer](https://github.com/JohannesKaufmann/html-to-markdown/blob/v2.5.0/converter/convert.go#L101)

Budget the added URL bytes while rewriting retained nodes, or use a bounded rendering path. Stop when the converted source cannot fit. With a small configured limit, verify that expansion fails for that item, preserves its prior content and timestamp, and lets the next batch item succeed. This needs no new operator setting.

## P2 — clarify the behavior and acceptance checks

### 4. A nonempty loading message qualifies as an article · AMBIGUOUS

A visible status region containing only “Loading…” passes the non-whitespace rule and could replace a previously indexed manual. The saved Excalidraw page exercises the easier zero-output case. Upstream’s sparse-page heuristic covers a wider case but can also discard legitimate terse documents. **Confidence 9/10** in the distinction. Whether placeholder-only pages merit special treatment is a product decision. [Design and rejected alternatives](design.md#rejected-alternatives) · [Shell acceptance](implementation.md#article-selection)

Define the guarantee in observable terms. Either limit it to no visible article text and document the loading-message outcome, or exclude a narrowly defined status-only shell. Pair that fixture with a genuine one-sentence manual. Specify the visibility signals used by both selection and rendering, including the intended treatment of navigation roles and inline hiding. Document the limits of detecting visibility from markup when a stylesheet supplies the rule.

### 5. Fragment and image destinations have no agreed output · AMBIGUOUS

A bare `#examples` in an MCP search result has no originating web document. An absolute final-page URL with that fragment takes the reader back to the manual. Both readings fit “keep ... meaningful.” The installed converter’s URL resolver also runs for image sources, preserves data URIs, and its default anchor renderer can emit an empty Markdown link. Passing `WithDomain` alone will not implement the promised text-only behavior. **Confidence 9/10** from the pinned link and image handlers. [Destination contract](design.md#choosing-and-rendering-the-article) · [Task 2](tasks.md#task-2-resolve-references-after-redirects) · [URL resolver](https://github.com/JohannesKaufmann/html-to-markdown/blob/v2.5.0/converter/url.go#L37) · [Image renderer](https://github.com/JohannesKaufmann/html-to-markdown/blob/v2.5.0/plugin/commonmark/render_image.go)

Give exact Markdown expectations for a redirected fragment, an unsupported or userinfo-bearing destination and an inline image. Check the resolved destination, including credentials inherited from a base. Put a conflicting HTML `base` element in the redirect fixture to exercise the final-response-URL rule. Decide whether image alt text is retained and whether image URLs belong in searchable prose. Keep the request-count assertion for zero embedded fetches.

### 6. The Accept preference can change which representation replaces a source · INCOMPLETE

The header is sent for every URL, including API endpoints. A server can offer HTML and JSON at the same URL or only a vendor `+json` media type. Different textual Accept lists that pass a header-presence test can select different representations or produce HTTP 406. Revalidation then uses the same requested-URL cache key. Upstream’s wildcard does not settle capy’s preference order. **Confidence 9/10** in the interoperability gap, supported by current JSON routing and the HTTP negotiation rules. [Negotiation plan](implementation.md#link-origin-and-negotiation) · [Fetch dispatch](../../../../internal/server/tool_fetch.go#L367) · [HTTP Accept semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-12.5.1)

Specify the media ranges, relative priorities and wildcard fallback. Have one test transport actually select a representation from the request and return it after a redirect. Assert the intended HTML or JSON indexing path, including continued support for a vendor JSON response.

### 7. Normalizing Content-Type leaves the HTML encoding undecided · MISSING

Capy admits `text/html; charset=utf-16` in its binary check, returns `string(body)`, and the pinned HTML parser expects UTF-8. Lowercasing or stripping media-type parameters does not transcode the body. Garbled text could therefore qualify as nonempty during revalidation. **Confidence 10/10** in the input-contract mismatch. [Article selection plan](implementation.md#article-selection) · [Existing admission test](../../../../internal/server/tool_knowledge_test.go#L1073) · [Body handoff](../../../../internal/server/tool_fetch.go#L328) · [Pinned HTML parser contract](https://pkg.go.dev/golang.org/x/net@v0.55.0/html#Parse)

State the supported HTML encodings. Decode supported input within a bound, or return an explicit conversion failure for unsupported encodings. Test a declared non-UTF-8 page over a good source and assert either the correct readable text or the retained prior source. Keep JSON and plaintext handling outside this change.

### 8. A generated title can disappear as a search section title · INCOMPLETE

The HTML path passes an empty content type to `IndexFetched`. On a short manual with one H1 and plain paragraphs, capy’s detector sees only one Markdown indicator, selects plaintext, and titles the chunk `Output`. The heading can look correct in the preview and still lose its role in title-weighted retrieval. The same ambiguity applies to an H1 synthesized from the page title. **Confidence 10/10** from the detector and short plaintext path. [Heading requirement](design.md#choosing-and-rendering-the-article) · [Content-type detection](../../../../internal/store/detect.go#L9) · [Plaintext section title](../../../../internal/store/chunk.go#L178)

Pass the known `markdown` type for successfully converted HTML if searchable heading structure is part of the contract. Assert the actual chunk title on a new or changed article with a single heading, and retain the existing same-hash behavior for historical chunks. This works independently of the proposed changes to Markdown chunking.

## Feasibility and verification

The existing converter accepts a parsed `*html.Node`. The final URL is available from the successful Go HTTP response, and both fetch modes already converge at the indexing helper. Capturing the document title before removing the head and preserving article-local headers fit this design. Existing TTL hits can age out normally, and force can refresh a historical source. The evidence supports a local conversion change with the current dependencies and storage schema. [Converter entry point](https://github.com/JohannesKaufmann/html-to-markdown/blob/v2.5.0/converter/convert.go#L53) · [Single and batch paths](../../../../internal/server/tool_fetch.go#L363) · [Fetch freshness](../../../reference/tools.md#fetch-freshness-and-statistics)

The task dependencies, size tags and exclusions are coherent. Tasks 1 and 2 each reach an observable fetch result. Keep the existing checks for a failed revalidation counting as one attempted miss, request order, and timestamp capture before the serial batch queue. For feature acceptance, use a fetch-to-search fixture with a known answer and an identifiable piece of surrounding UI. Run the repository’s quality benchmark as a regression check. Its store retrieval and server threshold cases do not exercise HTML article selection. [Task 3](tasks.md#task-3-verify-and-document-the-feature) · [Freshness regression tests](../../../../internal/server/tool_fetch_freshness_test.go#L93) · [Quality benchmark target](../../../../Makefile#L58)

The existing `TestConvertHTMLToMarkdown_*`, `TestFetchFreshnessFailedRevalidation` and `TestFetchFreshnessRenewal` tests passed at the reviewed revision with FTS5 and synthetic keys. These runs establish the current converter and cache baseline. Measuring the proposed search improvement requires the revised fetch-to-search fixture and its implementation.
