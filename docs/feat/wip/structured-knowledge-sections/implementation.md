# Implement structured knowledge sections

## Markdown ingestion

Start at `chunkMarkdown`, `ChunkHasCode` and `looksLikeMarkdown` in `internal/store/chunk.go` and `detect.go`. Replace the knowledge Markdown toggle with one fence recognizer for classification and boundary selection. Keep title-path and fallback processing on the bounded writer. Give knowledge Markdown its own block packing path so the vault's existing `SplitOversized` caller continues to read and write its versioned format. Do not trim code indentation as a side effect of trimming prose. Step → verify a four-backtick tutorial enclosing a shorter fence, a tilde fence, an unclosed fence, a heading-only source, and a Unicode line longer than the limit. Search for text on both sides of each split.

## JSON ingestion

Update `chunkJSON` in `internal/store/index.go` and the array, object and leaf cases in `internal/store/chunk.go`. Use a number-preserving decoder with validation of the whole document. Pack using the bytes actually written. Bound work by serializing an item once when charging it to a batch, avoiding repeated marshaling of each growing prefix. Preserve empty containers when decomposing an oversized record. Fit repeated path/identity titles separately from bodies while retaining row and part coordinates. Feed both layers through the existing transactional write. Step → verify escaped strings and integers above the exact range of floating point, the last field of an oversized row, a huge identifier and a large array of small objects. Malformed input must take the bounded plaintext path.

## Verification

Load synthetic keys for both encrypted stores and use the repository's FTS5 build tag. Exercise a public index followed by search, stale refresh, and dedup against pre-existing oversized chunks. Check stored byte lengths in both FTS layers and the existing vault chunker fixtures. Compare `make bench-quality` against the pre-change baseline, with particular attention to Markdown/JSON recall and the number of indexed chunks for small rows. Update the tool-reference size contract and the decisions on fence recognition and source limits. [Fence decision](../../../adr/002-naive-code-fence-toggle.md) · [Source-size decision](../../../adr/022-source-size-guard-and-db-bloat-prevention.md)
