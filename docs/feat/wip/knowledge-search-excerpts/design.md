# Match-centered knowledge search

**Status** Ready for implementation · [Implementation](implementation.md) · [Tasks](tasks.md) · [Review resolution](review-design-2026-11-23.md#resolution-after-recheck)

A search for `RetryBudget` can rank the right section first and still hide the function. The CLI shows the start of the chunk. MCP joins windows in occurrence order and can split UTF-8 or spend its allowance on early mentions. Return the passage that answers the query on both surfaces.

## Result contract

Apply this contract to ranked knowledge hits in `capy search` and `capy_search`. Each body contains at most **1,500 UTF-8 bytes**. Return a fitting display-safe chunk in full. Otherwise quote one contiguous slice, preserving punctuation, blank lines and indentation. Show an ellipsis on a separate line at each omitted edge. Count the indicators and their newlines in the budget. Source and title have separate allowances.

Redact recognized secrets from each complete field before selecting or clipping text. Repair invalid UTF-8, preserve body newlines and tabs, normalize CRLF to LF, and replace other terminal controls. Preserve CLI quoted metadata and its existing 512-byte source and 256-byte title limits before quoting. MCP renders each field on one line, with the same limits measured **after** single-line escaping and including any ellipsis. The hit wrapper and any source ID are separate from these fields.

### Choosing a passage

Score only evidence inside the slice a reader can see. Prefer, in order, more distinct query-term groups, more distinct complete code identifiers, a complete fitting fence containing a matched identifier, the presence of additional unassigned FTS body evidence, the shortest span covering the credited evidence, then earliest source position. Repeated occurrences and synonym spellings count once per group. Spelling out the same symbol once in prose and once in a fitting fenced definition therefore selects the definition. A prose-only tie goes to the earliest equally compact passage.

Both Porter and trigram can contribute body matches for the selected text. A marker can locate a stem or corrected spelling even when its original query term is unknown. Such markers supply a location and at most one unassigned signal per candidate. When all available highlights are valid and show only a title match, return opening context. When highlights are unavailable or unsafe, search the display-safe body for query spellings and synonyms. Opening context starts at the first nonblank passage, or the start of an all-whitespace body. Passage scoring runs independently of chunk ranking, source diversification and federation order. [Retrieval decision](../../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md)

### Markdown and JSON

A fence opens with a run of at least three identical backticks or tildes after up to three spaces. For a backtick opener, the rest of its line must be free of backticks. Its closer admits the same indentation range and uses the same character at least as many times. Only spaces or tabs may follow that run on the closing line. Quote a complete fitting block with its actual delimiters. For an oversized block, quote contiguous whole lines around its best match. Split an individually oversized line only at a UTF-8 boundary. Interpret only the available chunk. An unmatched opener extends to its end. An unidentified continuation uses ordinary lines. [Structured-sections fence grammar](../structured-knowledge-sections/design.md#markdown)

For a JSON source, a fitting chunk still wins in full. For a longer chunk, consider records only if both stored and display-safe bodies are at most **64 KiB** and the safe text parses completely as a nonempty array of objects. Rank the objects by evidence in their stored keys and values **before** asking which one fits. A large object with the requested ID and common key beats a small neighbor matching only the key.

Prefix the winner with `Array item N (in this section)` and a newline, using its one-based position in this chunk. This label accounts for the omitted array wrapper and siblings. If the label and object fit, return the object's exact safe slice. Otherwise choose a marked passage within that object under the remaining allowance. If no object has body evidence, use the opening-passage fallback without an item label. Mixed arrays, fragments, malformed chunks and larger chunks use ordinary passage selection.

## Pasted keywords

Treat each NUL as one ASCII space in ordinary keyword retrieval, including knowledge, vault chunks, run windows and the vault's separate per-line keyword mode. Use the normalized query for matching, fallbacks, reranking and excerpt selection. In `capy_search` keyword mode validate the whole query list before advancing its shared budget. One member containing only NUL and Go whitespace makes the request invalid. The same rule applies to the single-query alias. Existing punctuation, stopword, selector and array-decoding semantics still apply. Raw vault MATCH receives the supplied expression verbatim. Other tools retain their own admission policies. [Input paths](implementation.md#keyword-normalization)

## Assumptions

The renderer can use offsets only after validating the markers against the exact selected body. A changed display representation requires a fresh literal/synonym search on safe text. A stem or typo with no recoverable safe spelling may yield opening context. The answer can only come from the returned chunk. Historical Markdown can lack delimiters, and historical JSON can contain several records or a partial value.

## Not Doing

Global rank paging, source browsing, full-response budgeting, vault/run previews and repartitioning have independent contracts. The existing storage format suffices for deployment and rollback.

## Rejected alternatives

A larger prefix still hides a late match. Joining distant fragments can imply false adjacency. Reassembling a JSON source would make a single hit depend on unrelated chunks.
