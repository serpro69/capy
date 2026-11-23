# Browse a knowledge source by section

**Status** Ready for implementation · [Implementation](implementation.md) · [Tasks](tasks.md)

A developer who has just indexed or fetched a manual can open it at the first section. A reader who found section 80 through keyword search can ask for section 81 without guessing its title or paging through the first 80 sections. The existing `source` filter selects labels by literal substring and still requires a keyword match. Two manuals can share that substring. [Knowledge search](../../../reference/tools.md#knowledge-search-from-the-terminal)

## User contract

| Starting point | What the reader receives |
| --- | --- |
| Successful single-file `capy index`, or single-file or inline `capy_index` | A decimal source ID, a bounded display label, and the retained section count from the committed write. An unchanged receipt identifies the same stored source. |
| Successful single-URL `capy_fetch_and_index` | A source ID on a fresh fetch, unchanged revalidation, or cache hit. Two URLs sharing a friendly label each give the ID of their own stored entry. |
| A knowledge hit in `capy search` or `capy_search` | The source ID opens the manual from the beginning. A position on the hit opens the first section strictly after the displayed hit, including when headings repeat. |
| A browse page with more entries | Continuation arguments for the same source, positioned after the last **displayed** entry. |

For example, after indexing source 42, open it with `capy search --sections-of 42`. A ranked hit may supply `--sections-of 42 --after-section t.905`. In MCP the corresponding call is `capy_search({"sections_of":"42","after_section":"t.905"})`. A hit position says where to resume. Only a page continuation promises that another entry existed in that snapshot. Reuse the same selected knowledge database, including the original CLI `--project-dir` when applicable.

### Requests and pages

- `sections_of` is a canonical positive decimal string up to 9223372036854775807. `after_section` is an optional position copied verbatim, with grammar `p.<rowid>` or `t.<rowid>`. The lowercase tag identifies the knowledge FTS table that supplied the position. The row ID follows the same decimal rules. Leading zeros, signs, whitespace, zero, overflow and JSON numeric IDs are invalid. Numeric-looking **labels** still work in ordinary keyword searches.
- Both interfaces request three entries by default. The browse limit accepts integers 1–8. The MCP schema advertises a number. The handler also tolerates the decimal strings `"1"` through `"8"` when supplied directly. CLI serves the requested size. MCP applies its existing shared admission policy, currently at most two entries before taper, then one, then zero when blocked. A continuation repeats an explicitly requested limit. The caller can change it on the next request.
- Browse accepts only the source ID, optional position and limit, plus CLI global options. Supplying any keyword argument or filter, even `queries: []` or `all_projects: false`, is invalid. A position without a source ID is invalid in either mode. Validation precedes credential lookup, store work and MCP budget admission.
- A page gives the source ID, display label, stored section count and entries in insertion order. Each entry gives its title, original stored body length in bytes and up to 500 UTF-8 bytes from the beginning of the display-safe body, including any ellipsis.
- Source labels have a 512-byte limit. Page titles have 160 bytes and ranked knowledge hit titles have 256. Redact recognized secrets in the complete field and normalize invalid UTF-8 and controls. For browse pages, single-source receipts and MCP ranked hits, bound the finished single-line metadata after display escaping, including quotes when used and any ellipsis. Existing CLI keyword hits keep their 512/256-byte limits on the safe values before CLI quoting. Protocol serialization is outside these limits. Apply the same rule to any repeated label in a hint. Generate browse instructions entirely from validated arguments. [Keyword excerpt contract](../knowledge-search-excerpts/design.md)

### Outcomes

| Request outcome | CLI | MCP |
| --- | --- | --- |
| Page, empty stored source, or position at the actual last section | Exit 0. An empty page has no continuation. | Success, with the effective limit and reset notice. |
| Well-formed ID absent, including an evicted or replaced source | Exit 1. Obtain the current ID from a fresh receipt or keyword hit. | `IsError`, source unavailable. |
| Source exists but the position does not belong to it in the named table | Exit 1. Restart at the source ID. | `IsError`, invalid position for this source. |
| Store or query failure, or cancellation | Exit 1. Output and close failures also propagate. | `IsError` with a diagnostic when a response can still be delivered. |
| Invalid arguments | Exit 1. | `IsError` before admission. |
| Shared budget exhausted | CLI has no throttle. | `IsError` with reset time. Retry the same arguments after reset. |

Every syntactically valid MCP browse request counts once, including misses, failures and blocked calls. Each response to such a request carries its admission-time notice. A keyword miss retains its existing meaning of zero matching hits.

## Storage and lifetime

A page reads one snapshot of the selected encrypted knowledge DB. Starting at the beginning uses `chunks`. A position continues in its own table, `chunks` (p) or `chunks_trigram` (t). Both hold the same ordered section bodies. Each allocates its own rowids. Pair the source ID with the position on every continuation. Changed content or exact-label eviction invalidates the old source ID. Same-hash indexing retains it. IDs and positions are local to this database, including when worktrees share it. [Index write](../../../../internal/store/index.go#L205) · [Shared database ownership](../../../adr/026-worktree-shared-knowledge-db.md)

An explicit ID may open durable, ephemeral or surviving legacy `session` rows in knowledge. Label the page for the latter as legacy knowledge even with the vault disabled. Genuine vault hits keep their session UUID and line. Page reads use cached content and perform no backing-file refresh, fetch validation, retention renewal or access-count update. [Session ownership](../../../adr/027-vault-is-sole-session-store.md) · [Freshness](../../../adr/035-fetch-validation-and-bounded-stale-checks.md)

## Assumptions

Both content-bearing FTS tables have been written in the same slice order inside one transaction since the original store. The supported delete, rebuild and vacuum paths preserve the positions of surviving sections within each table. Check these properties on historical-shaped data with different rowids across mirrors. The unindexed source column makes page cost depend on unrelated chunks, especially when starting a late source or reaching the end of an early one. The [verification workload](implementation.md#verification) sets the interactive target.

## Not Doing

Directory indexing and multi-URL fetches keep their aggregate reports. Someone who has only an old label can obtain an ID from a matching knowledge hit. There is no label-only directory or catalog lookup in this feature. Vault search, global batch knowledge results and run envelopes have separate output contracts. Full-body export would require within-section paging for oversized legacy chunks.

## Rejected alternatives

Turning a failed keyword query into an ordered scan would present unrelated text as a match. Parsing handles out of labels would collide with user names. A title or an unqualified rowid cannot reliably identify a ranked section. Continuing within its originating mirror gives the correct successor without a per-hit lookup, schema migration or dependency on the compact-replies feature.
