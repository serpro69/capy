# Readable fetched page tasks

> Design: [Design](design.md)
>
> Implementation: [Implementation plan](implementation.md)
>
> Status: pending
>
> Created: 2026-11-23
>
> Not Doing: JavaScript rendering, crawling, alternate network policy

## Task 1. Index the visible article

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#article-selection)

- [ ] 1.1 Extend `convertHTMLToMarkdown` and `indexFetchedContent` to choose the visible content root, preserve article headings/examples/tables and recognize an empty shell.
- [ ] 1.2 Return a conversion outcome before any store write on failure. Use the same sanitized readable body for indexing and the single or batch preview.
- [ ] 1.3 Exercise a valid short article, duplicate hidden content, an empty main, and revalidation of an empty shell over a good source. Check the retained hash, kind and validation clock.

## Task 2. Resolve references after redirects

- **Status:** pending
- **Depends on:** Task 1
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#link-origin-and-negotiation)

- [ ] 2.1 Carry the response's final URL through `fetchRemoteContent` and `fetchItemResult` and set a textual Accept header in request construction.
- [ ] 2.2 Resolve eligible link destinations during conversion. Keep the requested URL as the cache key and preserve text for unsupported/credential-bearing links.
- [ ] 2.3 Verify a redirected page in single and batch mode, headers on both requests, a fragment and a subsequent cache hit. Assert conversion makes no additional network request.

## Task 3. Verify and document the feature

- **Status:** pending
- **Depends on:** Task 1, Task 2
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Implementation plan](implementation.md#verification)

- [ ] 3.1 Run `$kk:test` with FTS5 and the two synthetic test keys. Run fetch freshness, errors, limits and batch cases, the article-search fixture and `make bench-quality` against baseline. Check that secrets disappear from previews as well as stored text.
- [ ] 3.2 Run `$kk:document`. Update fetch documentation with the article-selection order, relative-link behavior and how shell failures affect freshness.
- [ ] 3.3 Run `$kk:review-code` with Go as the language and `$kk:review-spec` against this design. Record any unresolved behavior in this task list before closing it.

## Dependency Graph

```text
Task 1 -> Task 2
Task 1 -> Task 3
Task 2 -> Task 3
```
