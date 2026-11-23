# Implement knowledge section browsing

## Terminal page

Extend `cmd/capy/search.go` with mutually exclusive keyword and section modes, validating before credential selection. Reuse knowledge target selection, captured credentials and close/error handling. Print the ID from `SearchResult.SourceID` on knowledge hits and from `IndexResult.SourceID` in single-source `cmd/capy/index.go` output, including dedup. Keep ordinary search defaults and positional argument handling.

Add a context-aware page query alongside `GetSectionInventory` in `internal/store/inventory.go`. Validate the source and optional cursor membership in the same snapshot as the row read. Use bound parameters, a rowid seek and a one-row lookahead. Limit metadata fields independently. Redact entire stored field values before clipping previews for display, including legacy labels and controls. Inspect at most the requested number of bodies, fetch lookahead identity only, and do not call `GetChunksBySource`, which materializes the entire source. Avoid holding a transaction during output. Step → verify ordered pages from two sources with colliding label substrings and an old section larger than 4 KiB.

## MCP page

Add `sections_of` and `after_section` in `internal/server/tools.go`. Branch before the query-required preflight in `tool_search.go`. Admit a syntactically valid call once with the requested page size. Use the effective size in the store request and preserve the normal reset/blocked notice. Use the new formatter for safe fields, and expose the ID on knowledge hits and `tool_index.go` single-source results. A vault hit continues to show its own session identity. Give compact continuation arguments generated from validated numeric fields. Step → verify a complete read after a tapered page, with a literal-numeric label, an invalid mixed call and a replacement between requests.

## Verification

Test empty sources, exact last-page behavior, stale IDs after exact-label eviction and concurrent replace/read. Simulate a query failure distinguishable from absence, and a CLI writer failure. Check the query plan and measure one late page in a database with unrelated sources. The FTS source column is unindexed. If the read is too costly for an interactive search, record the measured plan and cost before proposing a persistent secondary index. Run the repository search-quality comparison and update the tool reference for both interfaces. [Knowledge schema](../../../../internal/store/schema.go)
