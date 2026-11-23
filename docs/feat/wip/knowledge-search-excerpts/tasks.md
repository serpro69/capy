# Knowledge search excerpt tasks

> [Design](design.md) · [Implementation plan](implementation.md) · [Review resolution](review-design-2026-11-23.md#resolution-after-recheck)
>
> Status: pending implementation · Created: 2026-11-23
>
> Not Doing: rank changes, cross-request paging, ingestion changes, run/vault presentation

## Task 1. Accept pasted keyword boundaries

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 2
- **Docs:** [Keyword normalization](implementation.md#keyword-normalization)

- [ ] Normalize NUL before both FTS query builders, fuzzy correction, reranking and the vault's non-raw per-line quoting. Apply the normalized blank check to vault chunks. Exercise direct strict knowledge, federated knowledge/vault, dedicated vault and run retrieval with a two-term query.
- [ ] Normalize every parsed `capy_search` keyword before admission. Exercise the alias, native and serialized arrays, a mixed valid/blank batch and ordinary stopword/punctuation controls. Check that invalid requests leave the shared counter untouched and a valid batch advances it once.
- [ ] Compare a fuzzy and an entity-boosted NUL query with their space equivalents. Confirm raw vault receives the original expression and run admission, saved batch queries and result ordinals retain their contracts.

## Task 2. Show the strongest passage on both surfaces

- **Status:** pending
- **Depends on:** —
- **Size:** M
- **Can run in parallel with:** Task 1
- **Docs:** [Passage selection](implementation.md#selecting-and-displaying-a-passage)

- [ ] Replace the MCP window collector and CLI prefix path with the shared selector. Pass query and winner's highlights through both public paths. Preserve CLI quoting, cancellation and output errors. Apply source/title redaction, one-line MCP escaping and the two per-field bounds independently of the compact-replies rollout.
- [ ] Return fitting display-safe bodies in full. For long bodies, score visible term groups, exact identifiers and validated marker locations. Reserve omission bytes, choose contiguous lines or a rune-safe slice of an oversized line, and exercise first-nonblank opening context.
- [ ] Compare the same late answer on CLI and MCP. Exercise short/full and title-only bodies, a repetitive weak prefix, invalid UTF-8, CJK, case folding, literal controls and secret redaction before a prospective cut. Check long multibyte metadata with an embedded newline and a synthetic credential.

## Task 2a. Recover body evidence across retrieval layers

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 2b
- **Docs:** [Match evidence](implementation.md#match-evidence)

- [ ] Retain display-only evidence for the winning content across both FTS layers and the fuzzy merge on the two knowledge display paths. Keep the original ranking highlight and winning metadata. Reject alternate marks from different content under the same source/title.
- [ ] Use the title `migrating` fixture to demonstrate the late body answer on both surfaces even when trigram wins. Exercise repeated synonyms, a corrected-only hit and a same-title/different-body collision. Distinguish a valid unmarked body with an incidental substring from invalid offsets with a safe synonym or no recoverable match. Compare ranking fields to the baseline.

## Task 2b. Preserve the matching code example

- **Status:** pending
- **Depends on:** Task 2
- **Size:** M
- **Can run in parallel with:** Task 2a
- **Docs:** [Passage and fence rules](design.md#choosing-a-passage) · [Windows and cost](implementation.md#windows-and-cost)

- [ ] Recognize backtick and tilde fences with the agreed opener and closer grammar. Prefer a complete fitting fenced identifier over an otherwise tied prose mention. Handle an oversized fence, one giant line, a shorter inner delimiter and a historical partial block using available source bytes.
- [ ] Verify the exact `RetryBudget` signature and return value through CLI and MCP, plus a prose-only control. Assert valid UTF-8, the visible slice and marker-inclusive byte count at the fit boundary. Check that an absent delimiter is never emitted as source text.
- [ ] Check scorer allocation growth on repetitive legacy bodies of increasing size with the best match near EOF. Separate selection workspace from full-field sanitization and input/highlight bytes.

## Task 3. Show a fitting JSON record

- **Status:** pending
- **Depends on:** Tasks 2a and 2b
- **Size:** M
- **Can run in parallel with:** —
- **Docs:** [JSON record selection](implementation.md#selecting-a-json-record)

- [ ] Carry source format alongside existing knowledge-specific coordinates. For eligible complete arrays rank whole records, select the winner, then fit its exact safe slice or a marked passage under the remaining item-label budget.
- [ ] Exercise a large exact-ID winner next to a fitting weak neighbor, two distinct IDs with common keys, a title-only hit, and escaped text with a distinctive value. Check whole-array fit, label-inclusive fit and one byte over, the 64 KiB threshold, mixed/trailing/partial values and JSON-looking plaintext.

## Task 4. Verify and document the feature

- **Status:** pending
- **Depends on:** Tasks 1 and 3
- **Size:** S
- **Can run in parallel with:** —
- **Docs:** [Verification](implementation.md#verification)

- [ ] Run `$kk:test` with the two synthetic keys and `-tags fts5`. Compare the ordered ordinary-query results and mixed-corpus ranks on a fixed pre/post fixture. Assert answer-bearing text and rendered byte limits separately and compare `make bench-quality` against the pre-change baseline.
- [ ] Run `$kk:document`. Update `cmd/capy/search.go` help, `docs/reference/tools.md` and the knowledge CLI paragraph in `docs/architecture.md`. Show a late answer, the field budgets, and the ordinary-keyword NUL contract.
- [ ] Run `$kk:review-code` for Go and `$kk:review-spec` against this design. Resolve discrepancies and record verification results and any remaining issues before closing the tasks.

## Dependency graph

```text
Task 1 -------------------------------> Task 4
Task 2 -> Task 2a -> Task 3 ----------> Task 4
       -> Task 2b -> Task 3
```
