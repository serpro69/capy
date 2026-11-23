# Implement readable fetched pages

## Article selection

Extend the existing HTML conversion path in `internal/server/tool_fetch.go`. Reuse the existing parser/converter stack and table support. Normalize the response media type once (parameters and case included). On the parsed, inert tree, first eliminate hidden and boilerplate nodes, then select a content root, preserving visible title and code. Treat `hidden` and `aria-hidden="true"` as explicit visibility signals. Avoid a CSS engine. Bound traversal by the admitted body and use an iterative walk or an explicit nesting limit with a conversion error. Send only the sanitized converted representation downstream for storage, comparison and previews. Propagate conversion/empty-body failures through both the single and batch paths before `IndexFetched` can renew freshness.

Step → verify a table and indented example under `main`, a hidden duplicate main, fallback to visible text outside an empty main, two sibling articles, a short valid page, and an empty application shell revalidated over a previously indexed manual. Check both returned preview and searchable content. On failure, other URLs in a batch can still succeed.

## Link origin and negotiation

Return the successful final response URL from `fetchRemoteContent`. Carry it through `fetchItemResult` to the serial conversion in batch and directly to conversion for a single fetch. Set the textual Accept header in request construction. Resolve links in the selected subtree against that final URL, disregarding untrusted document base overrides. Keep the requested URL in `composeFetchCacheKey`. Reuse the existing transport, redirect, response-size and binary checks. Step → verify a redirect across path bases with a relative reference and a fragment. Assert the initial and final requests' headers, request count, resulting target text, requested-URL dedup and a second call's cache hit.

## Verification

Use the existing in-process fetch fixtures and test transport. Compare search hits before/after on one article with a repeated term in its sidebar. Verify TTL, kind, validation time, mixed-batch order, existing fetch limits and the indexing quality benchmark with synthetic keys. Document output and failure examples in the tool reference. [Fetch implementation](../../../../internal/server/tool_fetch.go) · [Validation and retention](../../../adr/035-fetch-validation-and-bounded-stale-checks.md)
