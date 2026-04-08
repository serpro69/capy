# Tasks: Synonym-Aware Proximity Reranking

**Design:** [design.md → Addendum](./design.md#improvement-a-synonym-aware-proximity-reranking)
**Implementation:** [implementation-v2.md](./implementation-v2.md)
**Status:** pending

---

## Task 1: Implement Synonym-Aware Proximity Reranking

**Status:** done
**Dependencies:** none
**Docs section:** [implementation-v2.md](./implementation-v2.md)

- [x] Modify `proximityRerank` in `internal/store/search.go` — build `termGroups [][]string` from query words via `ExpandSynonyms`, pass to both highlight and content fallback paths
- [x] Modify `findMinSpanFromHighlights` signature from `(highlighted string, terms []string)` to `(highlighted string, termGroups [][]string)` — match any term in group against highlighted spans, break after first match per group
- [x] Modify content fallback in `proximityRerank` — for each group, find positions of all terms via `findAllPositions`, merge and `sort.Ints`, then call `findMinSpan`
- [x] Update existing `findMinSpanFromHighlights` tests to use `[][]string` signature (wrap each term in single-element group)
- [x] Add new tests: `TestProximityRerankWithSynonyms`, `TestProximityRerankSynonymHighlights`, `TestProximityRerankSynonymContentFallback`, `TestProximityRerankMixedTerms`, `TestProximityRerankNoSynonymPassthrough`
- [x] Run `make test` and `make test-race` — all tests pass

## Task 2: Final Verification

**Status:** pending
**Dependencies:** Task 1
**Docs section:** n/a

- [ ] Run `solid-code-review` — review all new and modified code
- [ ] Run `implementation-review` — verify implementation matches design.md addendum and implementation-v2.md
