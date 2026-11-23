# Implement match-centered knowledge search

## Keyword normalization

Normalize embedded NULs before keyword validation and every fallback that sees the original query. Trace `internal/retrieval/query.go`, the fallback in `retrieval.go`, per-tool argument admission in `internal/server/tool_search.go`, and the CLI search path. Shell argv cannot carry an embedded NUL, so exercise that input through MCP and the shared Go API. Preserve ordinary stopword and synonym behavior. Step → verify the same hits for two known tokens separated by space or NUL, including a strict layer-error test and a separator-only MCP call that consumes no budget.

## Selecting and displaying a passage

Move the knowledge excerpt logic in `internal/server/snippet.go` to a package usable by `formatKnowledgeHit` and `cmd/capy/search.go`. Pass the query and highlights to the terminal formatter. Redact the complete candidate before slicing, normalize display controls, then map matches against that representation. A case-folded copy may have different byte offsets. Track positions in the original UTF-8 string. Bound the work per candidate and avoid generating a window per duplicate term in a long legacy chunk. Retain CLI metadata escaping and output error propagation.

Find the strongest ordered paragraph/line group. A fitting fenced block takes precedence over a partial view of the same match. For an old oversized chunk, score in a forward scan and keep only the best bounded region. Step → verify the same late identifier appears in CLI and MCP, including when a repetitive prefix contains a weak term. Cover CJK, a length-changing case fold, literal control bytes, a secret straddling the prospective cut, whole and oversized fences, and title-only matches.

## Selecting a JSON record

For a stored JSON source, carry its source format through the knowledge row mapper in `internal/store/search.go` as needed. A chunk's code/prose filter alone cannot identify its source format. For eligible array chunks of at most 64 KiB, use a decoder over the safe text to identify object byte spans, select by the same match evidence and preserve the winning span verbatim. Larger legacy chunks, mixed arrays and partial values use ordinary passage selection. Charge the record/omission indicator to the excerpt budget. Step → verify two objects with similar keys and distinct IDs, an oversized record, and a plaintext example that contains JSON-like syntax. If the row mapper also carries a section coordinate for compact replies, preserve both fields in its knowledge-specific metadata.

## Verification

Compare a fixed set of returned IDs and ranks before and after the formatter change. Assert answer-bearing text, valid UTF-8 and total excerpt bytes separately. Existing retrieval scores do not measure whether the rendered excerpt actually contains the answer. Check CLI search, MCP federation and the existing quality benchmark with synthetic keys and `-tags fts5`. Update CLI help which currently promises a prefix. [Tool reference](../../../reference/tools.md)
