# Browse a knowledge source by section

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

A developer has found the right manual but does not know the vocabulary of the next section. Today `source` is a literal substring filter on a ranked query. Two manuals can share that substring, and a query with no matching words gives no way to browse either manual. Capy already assigns a numeric ID to each stored source and inserts its sections in order. [Knowledge search](../../../reference/tools.md#knowledge-search-from-the-terminal)

## User contract

Show the knowledge source ID on ranked knowledge hits and on a successful single-source index. Print it as a decimal string, separate from the user-assigned label. For example, `capy search --sections-of 42` or `capy_search({"sections_of":"42"})` opens the first page of that source in the selected knowledge database. Numeric-looking labels still work with the existing `source` filter. A fetched source is discoverable through its ordinary labelled search hit. A directory index continues to return its aggregate report.

Each page gives the source label, stored section count, and entries in insertion order. Cap the displayed label at 512 UTF-8 bytes and each title at 160. Each entry shows the section title, its stored content length in bytes, and up to 500 display-safe UTF-8 bytes of preview including any ellipsis. The CLI and MCP request three entries by default and accept `limit` between one and eight. MCP serves at most two before taper, then one, then blocks under its shared search policy. Emit a continuation only when another entry exists. It repeats the source ID and gives an `after_section` value (CLI `--after-section`). Both IDs use canonical positive decimal strings up to signed 64-bit maximum. Omit the latter to start.

This mode accepts just its source ID, optional continuation and limit, plus global CLI options. Reject query words and search filters in this mode before work or budget consumption. A valid page request counts once in MCP, including a miss. An unknown or replaced ID, a cursor outside that source and a database error are distinct from reaching the end. A cursor for the actual last section yields an empty final page. An ordinary keyword miss continues to mean zero matches.

## Storage and lifetime

Use one read transaction per page. Read from the primary knowledge chunk table only. Changed content is replaced under a fresh source ID. Same-hash revalidation can keep the ID while changing metadata. Deletion or replacement between pages invalidates the old selection. A page describes stored sections as of that read, including legacy chunks and explicitly selected ephemeral sources. It performs no file refresh and grants no access to the backing file. IDs are local to the selected database and valid during the life of that source.

## Assumptions

Insertion order corresponds to increasing `chunks.rowid` within a source, as in the current inventory query. Check behavior after store optimization and normal cleanup. Previewing a bounded number of sections must remain practical against a large knowledge store.

## Not Doing

Full-body export would require within-section paging for oversized legacy content. Run IDs and vault UUIDs belong to different stores and have their own read interfaces.

## Rejected alternatives

Silently converting a failed query to an ordered scan would mislabel unrelated text as matches. Parsing short handles out of the label would collide with user-defined source names.
