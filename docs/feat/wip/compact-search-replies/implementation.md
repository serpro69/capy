# Implement compact multi-query search replies

## Surface partial retrieval failures

Give the two MCP handlers a way to retrieve ranked rows and a diagnostic from the same pass. In the knowledge and vault chunk adapters, opt in to the existing `retrieval.CorpusConfig.OnError` callback. It observes SQL query, row scan and iteration errors across Porter, trigram and fallback attempts. Record the first error per corpus and query with `sync.Once`, then read it after the retrieval workers join. Pass open/setup errors through the same corpus status. Keep usable results from successful work and retain the existing knowledge access tracking. The strict CLI path still discards results on layer failure and ordinary library callers keep their current error policy. [Retrieval callback](../../../../internal/retrieval/retrieval.go) · [Knowledge adapter](../../../../internal/store/search.go) · [Vault adapter](../../../../internal/vault/chunk_search.go)

In particular, change the dedicated vault loop's early `continue` on error so rows accompanied by a diagnostic reach the formatter. Per-query failures stay in-band on both tools, including when all attempted passes for a query failed. Preserve the existing MCP error flags for validation, blocked admission, empty-store guidance and a disabled dedicated vault.

Keep retrieval diagnostics separate from optional source-count and backlog lookups. A failed scoped vault availability check gets one request-wide notice and the normal passes still run. An unavailable optional statistic simply leaves its hint absent. Apply the empty-store shortcut only after its existing scoped preflight establishes emptiness.

## Bound the complete reply

Use a request-local writer in both keyword handlers. Apply the following ceilings to the **finished display text**, including punctuation, omission marks, line breaks and separators. The small reserves allow the writer to decide whether to issue the next query before it knows the result.

| Unit | Maximum UTF-8 bytes | Contents |
| --- | ---: | --- |
| Whole reply | 40,960 | The final MCP text item |
| Request-wide notices combined | 2,048 | Availability or no-hit guidance, completion notice and the admission-time footer where applicable |
| One query's framing and status | 1,024 | Input ordinal, query preview up to 256 bytes, outcome, and up to two corpus causes of 192 bytes each |
| One hit or reference | 3,072 | Passage up to 1,500 bytes, bounded attribution, reference and formatting |

For knowledge use up to 512 bytes for the source label and 256 for the title. For vault use up to 128 for the session ID, 256 for the title, 512 for the project and 256 for the sidecar ID. Preserve the line, platform, date and existing short parent ID in the remaining space. If a sidecar ID needs clipping or redaction, mark that fact and include the session-wide chunk index when available to distinguish similar previews. Keep corpus attribution explicit even when a knowledge label begins with `session:`. Ten full hit blocks, one query frame and all request-wide notices total at most 33,792 bytes. Test this upper bound with distinct hits and no reference savings.

### Prepare display text

Redact known secrets in complete fields before quoting or cutting them. For stored bodies, use the full display-safe content. Reuse FTS highlights only if stripping their markers reconstructs that content and the markers are unambiguous. Otherwise discard the highlights and select a safe window. This check belongs before the vault adapter builds its bracketed snippet. Keep offsets within the original byte string when Unicode case folding changes its length. [Current knowledge snippets](../../../../internal/server/snippet.go) · [Vault snippets](../../../../internal/vault/chunk_search.go)

Normalize invalid UTF-8 in the produced text, including interior window edges. Keep metadata and query echoes on one line and preserve line breaks and indentation in passages. Apply field caps on rune boundaries to the final escaped representation, with the omission marker inside the cap. For a validation error containing caller input, redact the full offending value before quoting it and bound the resulting preview before composing the error.

### Emit query groups

1. Set aside the request-wide reserve before rendering query groups. Budget each applicable notice there, including separators. Keep the exact admission snapshot footer on every valid federated reply. Give validation errors a bounded text path at their current tracking boundary. Dedicated vault search has no shared footer.
2. Before each query, require room for its frame and status outside that reserve. If space is insufficient, record its ordinal as unsearched and stop. Otherwise run the eligible passes, commit the actual header and outcome, then append whole ranked hits or references while they fit. Charge actual bytes, so unused frame space is available for hits and subsequent queries. A failure remains visible even when zero hit blocks fit.
3. At the first hit that cannot fit, stop without issuing another query. The reserved completion line reports the original ordinal, shown hits out of ranked hits returned at the effective limit (including `0 of N`), and the count of following queries never run. For an unsearched boundary query, give its ordinal and following count without guessing its hit count. Emit the line only for unfinished work. Per-field clipping alone does not trigger a retry.
4. Emit each applicable hint once. Check for excluded ephemeral sources on a clean miss with knowledge in scope. Make a whole-batch miss statement only if all queries ran, all eligible passes succeeded and none returned a hit. Report applicable disabled-vault or reindex-backlog information as scoped guidance even if only a prefix ran and that prefix had no hits. Preserve the explicit-source and kind opt-outs. Track final text once through `trackToolResponse` at the existing tracking boundary, including the footer.

## Identify passages through ranking

Change `federateHits` to carry typed hits through the existing stable rank merge and limit. Render and consider references in that final order. For knowledge, add `c.rowid` to the adapter's SELECT list, scan it in `scanSearchRow`, and stamp the physical layer on the mapped result where `execSearch` knows its table. Carry both through winner replacement and fuzzy fallback. Use the winning table, rowid and source ID for location. The fused `MatchLayer` and a shared title are insufficient.

For vault, select the existing `c.chunk_index` in both layer queries and carry it through `chunkMeta` to the handler. Zero is a valid first index. Preserve presence separately so a row missing the coordinate still renders as a full hit. Use the full session UUID, full subagent ID (empty for main), chunk index and first-line anchor. Both MCP handlers identify the chunk start and use the same visible sidecar attribution. When the brief tool is present, include its fixed latest-exchange instruction in the first eligible full block's 3,072 bytes. Keep the original search selectors by reference and suppress the action when the ID is a preview. The existing schema supports this projection. [Chunk storage](../../../../internal/vault/store.go) · [Chunk numbering](../../../../internal/vault/import.go) · [Brief follow-up](../vault-session-brief/implementation.md#mcp-brief)

Compare the corpus and location together with the selected passage before reply-field clipping, its final display text and displayed attribution. If the session was renamed between queries, show its updated attribution in full. If different locations within a chunk can yield the same text, use the window span or spans to disambiguate them, or emit a full entry. A matching visible prefix alone cannot establish equality. Only register a reference after its entire first block is committed. Keep the map scoped to emitted entries and assign numbers in emission order. Repeat an entry when proving equality would require retaining an unbounded field. [Federation](../../../../internal/server/tool_search.go) · [Vault presentation](../../../../internal/server/tool_vault_search.go)

When source browsing is present, reuse its winning table/rowid pair. The first full knowledge hit owns the source ID and browse position within its hit budget. A later reference leads back to that entry. Keyword retries use query ordinals, while browse requests use section positions. Preserve any source-format metadata carried by the excerpt renderer when extending the row mapper. Each proposal can ship independently. [Source browsing](../knowledge-source-browsing/implementation.md) · [Knowledge excerpts](../knowledge-search-excerpts/implementation.md)

## Wire the tools and document recovery

In `handleVaultSearch`, use `stringArrayArg` for a supplied `queries` value. Reject a malformed value even when a fallback `query` is present. Keep valid empty-array fallback, existing string-query handling, vault date/limit behavior and scope resolution. In `handleSearch`, validate before `throttle.advance`, advance once per valid request and return a blocked reply before storage work. Source and kind selection still determine the eligible corpora. [Argument helpers](../../../../internal/server/coerce.go) · [Search admission](../../../../internal/server/server.go)

Update the empty-store, miss, block and budget-footer guidance alongside the registered `capy_search` and `include_kinds` descriptions in `internal/server/tools.go`. A blocked knowledge search directs the caller to the reset time. The tool and miss guidance identify `capy_runs` for new command output and both knowledge selectors for retained ephemeral material. Describe the retry behavior on both registered search tools and document the vault array compatibility change in the tool reference. [Tool schemas](../../../../internal/server/tools.go) · [Operator reference](../../../reference/tools.md)

## Verification

Exercise the public handlers with the implementation tasks' boundary cases. Check `utf8.ValidString` on returned text and its byte count after marshaling and decoding the MCP result. For tracked text replies, compare that count with the delta in response stats. Compare expanded references with the pre-change source, location and rank order on a fixed federated fixture. Measure bytes saved on duplicate-heavy input and record single-query overhead. Use the repository's FTS5 tests and pre/post quality comparison to check retrieval parity. A ranking benchmark alone cannot detect a missing reference, warning or suffix.
