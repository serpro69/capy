# Match-centered knowledge search

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

A developer searching for a function can receive a high-ranked hit whose displayed text never reaches the function. The terminal prints the beginning of the chunk. MCP gathers windows around early matches and can cut a code point, a code sample, or the strongest later match. A separate input issue affects the same workflow when text pasted into a search contains a NUL between words.

## Result contract

For a knowledge hit, display up to 1,500 UTF-8 bytes including omission markers. A fitting chunk is shown in full. For a longer chunk choose one contiguous excerpt from display-safe stored text. Rank candidate passages by distinct matched terms, then full identifier matches, then compactness of the matching span, then source order. FTS highlights provide match positions for stems and synonyms. A title-only hit gets the first useful passage. Ordinary punctuation and code indentation carry meaning and should survive rendering. Source/title headers keep their own bounds.

Prefer an entire Markdown fence, including delimiters, when it fits. For an oversized fence, show contiguous source lines around the match and mark the omission outside the quoted text. A single oversized line can be cut at valid code points. When an entire JSON array chunk already fits, show it in full. When it exceeds the excerpt budget, consists of complete objects and is at most 64 KiB, choose the best matching **whole object** if it fits, using its keys and values. Return the object's stored slice and identify it as an array item. An oversized object receives a marked excerpt. Selection is within a ranked chunk. The existing search rank, source diversification and vault hit ordering still determine which chunks appear.

Treat NUL as a word boundary in ordinary keyword retrieval, consistently across its Porter, trigram and fallback paths. At the MCP boundary a query consisting only of separators is invalid before shared-budget admission. Raw vault MATCH syntax is a separate interface. [Retrieval architecture](../../../adr/028-corpus-agnostic-retrieval-and-rrf-federation.md)

## Assumptions

Highlights can be mapped back to the original content only when stripping their sentinel bytes yields exactly that content. Redaction or a literal sentinel may invalidate the map. Rescan display-safe text in that case. Existing JSON chunks can contain several objects. Only attempt object selection on a complete parse of a bounded candidate.

## Not Doing

Global result pagination and scoring changes require decisions about moving cross-corpus ranks. Vault and run result formatters already have distinct budgets and navigation contracts.

## Rejected alternatives

Increasing the snippet limit would still hide late hits. Joining distant fragments can imply adjacency between unrelated lines of code. Parsing an entire source for every JSON hit would make response cost depend on corpus size.
