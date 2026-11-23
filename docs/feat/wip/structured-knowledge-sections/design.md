# Structured knowledge sections

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

When a developer indexes a manual, a shorter fence inside a code example can turn the next example line into a section heading. When they index a JSON response, a single value or a pretty-printed array can exceed the 4,096-byte section size. Both failures affect the text and titles fed to the two knowledge search indexes. The unit of work is one new or changed source, normally at most 2 MiB before processing. [Current chunk limits](../../../reference/tools.md#cleanup-and-older-chunks)

## Markdown

Recognize ATX headings at levels one through six and preserve their ancestry as the search title. Track the opener character and length for backtick and tilde fences with up to three spaces of indentation. A closing line uses the same character, at least the opening length, followed only by whitespace. Treat an unclosed block as code through the end of the input. Headings and horizontal rules inside a fence are searchable example text. Preserve blank lines and indentation inside code.

Keep a fitting fenced example together. For an oversized section, pack paragraphs, fences and table rows in order within `store.MaxChunkBytes`. Split oversized blocks by whole lines, then a single oversized line at a UTF-8 boundary. Keep the heading path and code classification on continuation chunks. Give parts distinct ordinal suffixes within the title budget because rank fusion groups by source ID and title. Charge overlap against the cap and omit overlap when it would repeat a whole chunk or displace new text. Apply the bound to the heading-only fallback as well. For automatic detection, a genuine fence opener suffices on its own. Retain the existing multi-indicator test for prose and keep a leading interpreter shebang on the plaintext path.

## JSON

Keep neighboring objects in one array chunk when their actual encoded representation, including brackets and separators, fits. Preserve row order, numeric literals and the existing preference for an identity field in the title. Encode a fitting row whole. Give an individually oversized record its own index-qualified path and divide it by fields. Break a remaining oversized scalar into ordered UTF-8-safe fragments, all with the same path and numbered titles. A fragment is searchable text and must be identifiable as part of a value. Empty arrays, empty objects and malformed input must still produce the currently supported searchable representation. Include punctuation and escaping in size checks. Titles derived from a giant identifier or heading need their own UTF-8-safe display prefix of at most 256 bytes.

## Compatibility and acceptance

Apply at normal inline, file, directory, refreshed-file and fetched knowledge writes. The sanitized input still determines deduplication. A same-hash write retains the previous chunks. Operators with an original and a requirement for immediate re-chunking can use the existing exact-label cleanup and re-index procedure. Both FTS tables must contain the same ordered partitions. Grade a manual with an embedded fence and a JSON array with a late identifying field by retrieving the example or record through the existing search surface.

### Assumptions

The existing heading/identity titles are valuable ranking inputs. Compare Markdown and JSON quality fixtures before changing their contents. Vault transcript windows also call `store.SplitOversized`; their indexing and versioning contract is independent.

### Not Doing

Vault and run repartitioning have separate retention and versioning rules. A store-wide rebuild would require originals that inline knowledge need not retain.

### Rejected alternatives

A global byte cut would sever ordinary records and fitting examples. A row per tiny JSON object would charge both FTS tables for a separate entry for every record in a large array.
