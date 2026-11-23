# Implement compact multi-query search replies

## Federated search

Give `handleSearch` in `internal/server/tool_search.go` one bounded writer for all return paths, including early exits and no-result guidance. Reserve room for a fixed-size completion status and the admission-time `budget.notice()`. Treat error text, query echoes and metadata as untrusted fields. Redact recognized secrets before display clipping. Bound each field on a UTF-8 boundary and distinguish clipped fields from omitted hits. Keep partial failures visible even if a corpus returned hits. Update empty-store and retry guidance to direct command output to `capy_runs` and manual knowledge/fetch content to `capy_search`.

Carry enough identity through `federateHits` to verify equality of corpus, source, underlying section and final passage before reusing a reference. For knowledge, carry the winning FTS table identity and its rowid from the actual layer read alongside the source ID. Each table owns its rowids. The final fused `MatchLayer` label does not identify which row supplied the text. A winner from the other table can be shown again. Allocate a reference only after its first passage fits. Bound duplicate tracking by entries actually emitted. Step → verify two queries sharing one passage, a second query selecting a different excerpt, a title collision, many large misses, a single oversized label, and a vault failure alongside a knowledge hit. Assert total returned text including the budget footer.

## Dedicated vault search

Use the same result writer for `handleVaultSearch`; keep its existing per-query limit and availability/backlog reporting. For reference identity preserve full session and subagent IDs, the actual chunk coordinate and excerpt. `first_line_index` alone can repeat across split windows. If necessary carry the existing `chunk_index` from the vault row mapper, or skip deduplication for hits lacking an unambiguous coordinate. Keep separate scopes distinct throughout. Step → verify main and sidecar hits with identical text, a query error, an all-empty batch, and a partial final query. Assert that unshown suffix queries were never issued.

## Verification

Check both public handlers, shared-budget taper/block cases and ordinary one-query output. Compare pre/post hit order on a fixed federated fixture. Use an intentionally long multibyte batch to measure the hard bound and a duplicate-heavy batch to measure reply bytes saved. Keep timing and byte figures as implementation evidence. Run the repository's FTS5 search tests and quality comparison. Document how to resume omitted work using the original query list. [Tool reference](../../../reference/tools.md)
