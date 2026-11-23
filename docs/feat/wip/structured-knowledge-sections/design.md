# Structured knowledge sections

**Status** Ready for implementation · [Implementation](implementation.md) · [Tasks](tasks.md) · [Review resolution](review-design-2026-11-23.md#resolution-after-recheck)

A developer indexing a Markdown tutorial should find the example under its real heading. Someone searching an API response should be able to identify the record that supplied a matching value. Today an inner fence can change the next section’s title, and a 3,244-byte array can produce a 5,783-byte stored body. This design applies to new and changed knowledge writes. [Current limits](../../../reference/tools.md#cleanup-and-older-chunks)

## Selecting a format

| Write | Format selection |
| --- | --- |
| Inline knowledge, single file or directory member | Auto-detect from the admitted source bytes. A complete JSON object or array selects JSON. Other inputs use the Markdown detection rules below, then plaintext. |
| Changed file found by search-time refresh | Detect again on the newly read bytes when the sanitized hash changes. Commit the selected format and chunks together under the existing file-replacement guard. This also applies to historical file rows. An explicit Go format argument controls that individual write. |
| Single or batch fetch | Use the response’s lowercased media type without parameters. Index `text/markdown` as Markdown, HTML/XHTML converted by the existing fetcher as Markdown, and `application/json` or a subtype ending in `+json` as JSON. Other admitted responses, including `text/plain`, use plaintext. |
| Explicit store format | Use the supplied format. A selected JSON parser accepts one complete JSON value, including a scalar. Invalid or trailing input takes the existing bounded plaintext fallback. |

The store admits the source against its configured size limit before chunking. The hash still covers the complete sanitized source representation, including converted text for HTML. Parsing and partitioning operate on that sanitized input. Writing the same label and sanitized hash keeps the stored format, source ID and partitions, even when a new format would be selected. A JSON parse failure retains the selected JSON source format and stores plaintext fallback bodies. Unchanged fetch revalidation renews the validation timestamp and reports the retained metadata. [Store writes](../../../../internal/store/index.go#L50) · [Refresh and validation](../../../adr/035-fetch-validation-and-bounded-stale-checks.md)

## Stored sections and titles

Each new Markdown or valid JSON section has a body of at most **4,096 bytes** and a distinct UTF-8 title of at most **256 bytes** within its source. Both FTS tables receive the same ordered titles, bodies and code/prose types. A search can then return two matching sections even when their headings or record IDs repeat. [Rank fusion](../../../../internal/retrieval/retrieval.go#L327)

Keep a fitting heading path or JSON path and identity. When space is tight, reserve the row index and part number where applicable, then retain the current heading or field and record identity ahead of distant ancestors. Shorten long labels on rune boundaries with a visible ellipsis. Render title controls on one line. Resolve remaining title collisions in emission order with a reserved suffix such as ` [section 7]`, using the one-based position in this source. A literal heading that already ends in that suffix grammar also receives its own suffix. Count all markers in the title budget.

Every original heading appears once in the section bodies at its occurrence. JSON retains each sanitized key and identity in a body or in a complete search title. When decomposition would remove the only complete copy of a key, store the key once in bounded pieces beside its value. When a detached key’s name must be shortened, give its key pieces and scalar value pieces the same field number. For a container value, the first child group carries that number. A distinctive word beyond the title limit can then find its owning heading or field. Searches across an arbitrary split inside a single long token have the usual FTS boundary limitation.

## Markdown

For Markdown detection, a leading interpreter shebang (`#!` at the start of the first line) selects plaintext. Otherwise a genuine fence opener on any physical line suffices. For prose, count the existing hash-prefix and Markdown-link indicators in the first 50 physical lines. Two indicators suffice. Inspect indentation on the original line. Treat CRLF as one line ending and ignore an initial UTF-8 BOM for parsing.

A fence starts after zero to three ASCII spaces with at least three identical backticks or tildes. A backtick opener’s info string may contain no backticks. Its closer has the same indentation allowance and delimiter character, at least the opening length, and only spaces or tabs after the run. An unclosed fence extends to EOF. Outside fences, recognize ATX headings at levels 1–6 and keep their ancestry. A horizontal rule ends the current block within the same heading path. Inside fences, heading-like lines, rules, indentation and blank lines are literal example text. [Fence grammar](https://spec.commonmark.org/0.31.2/#fenced-code-blocks)

Pack headings with their sections, paragraphs, table rows and fenced examples in source order. Keep a fitting fence whole, starting a fresh chunk when necessary. Split an oversized block on whole lines, then a single oversized line at a UTF-8 boundary. Store its literal pieces, with the real delimiters only where they occur. Give split sections numbered titles. A chunk containing any fenced lines is code, including a continuation entirely inside a fence. A prose-only continuation is prose. Where spare space permits, retain up to the last two complete prose lines as overlap. Keep overlap within one section and exclude headings, table rows and fenced lines. Omit overlap that consumes an entire prior chunk or displaces new text. Heading-only input follows the same body limit.

## JSON

Use compact JSON with deterministic key order, input array order and exact number literals. Pack consecutive fitting elements into complete arrays, charging brackets, separators and escaping. When redaction gives different keys the same spelling, emit those members in separate field groups so readers can recover both values. Preserve capy’s existing preference for an array identity field such as `id` or `name`. If a single element fits alone and its one-element array exceeds the cap, emit the element whole with its zero-based array index. This includes mixed arrays and nested arrays.

For an oversized object, pack consecutive fitting fields in sorted key order into complete JSON objects. Flush before descending into an individual field that cannot fit with its key. A fitting nested container can travel intact with its field. Preserve ordinary named paths when traversing small nested objects outside array rows. Identify each decomposed record’s pieces by its row index and available identity. Within nested records, the nearest record supplies these coordinates and ancestor paths use the remaining title space. Empty containers retain their literal `[]` or `{}`. All valid JSON pieces, including keys and scalar continuations, use the code classification. Malformed JSON fallback uses plaintext classification.

Redact complete decoded string values and keys before using them in a body or title. Split an oversized string by decoded runes and encode each piece as an independently quoted JSON string. Use the same quoted-piece representation for the digits and punctuation of an oversized numeric literal. Number pieces in order. Joining their decoded strings recovers the complete sanitized value or exact number spelling. Count the quotes and escapes in each piece.

For example, consider a one-row array with numeric ID `9007199254740993`, a `notes` string containing 4,093 ASCII `x` characters followed by a newline and `ridge`, and `zone` equal to `upland`. The following are representative stored results. All are code. The short complete-batch example uses a separate fitting input.

| Stored title | Stored body |
| --- | --- |
| `(root) > 12` | `[{"id":12,"zone":"upland"}]` |
| `[0] 9007199254740993 · fields 1` | `{"id":9007199254740993}` |
| `[0] 9007199254740993 > notes · value 1` | 4,093 `x` characters enclosed in double quotes, 4,095 bytes total |
| `[0] 9007199254740993 > notes · value 2` | `"\nridge"` |
| `[0] 9007199254740993 · fields 2` | `{"zone":"upland"}` |

## Compatibility and acceptance

The design needs no schema migration. Operators who retain the original can evict its exact label and re-index for immediate conversion. Rolling back the binary restores the previous partitioning on subsequent changed writes. Existing rows remain readable. [Operator procedure](../../../reference/tools.md#cleanup-and-older-chunks)

Acceptance requires the stored byte and identity checks in both FTS tables and the correct tutorial example and JSON record in public search results. Resolve regressions against the immediate quality baseline before release. The implementation plan specifies the fixtures and checks. [Verification](implementation.md#verification)

### Assumptions

Heading ancestry and JSON identities contribute to rank. Preserve ordinary short titles and compare retrieval on the existing corpus. File-backed production writes select their format automatically. The store persists the selected format only.

### Not Doing

Vault transcript windows and run capture have separate formats and lifetimes. Historical knowledge can lack an original for a bulk rebuild. Search presentation, source browsing and full-source reconstruction have independent contracts.

### Rejected alternatives

A global byte cut would sever ordinary records and fitting examples. One FTS row per tiny object or field would multiply inserts into both indexes. Changing the shared fusion key would extend this write-format change into vault retrieval.
