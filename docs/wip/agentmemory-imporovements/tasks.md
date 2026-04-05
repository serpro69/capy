# Tasks: Search & Security Improvements

**Design:** [design.md](./design.md)
**Implementation:** [implementation.md](./implementation.md)
**Status:** pending
**Feature branch:** `feat/agentmemory-ports`

---

## Task 1: Domain Synonym Expansion

**Status:** pending
**Dependencies:** none
**Docs section:** [implementation.md → Feature 1](./implementation.md#feature-1-domain-synonym-expansion)

- [ ] Create `internal/store/synonyms.go` with the synonym map (all ~40 groups from the design doc), `init()` to build the bidirectional lookup map, and exported functions `ExpandSynonyms(term string) []string` and `HasSynonym(term string) bool`
- [ ] Create `internal/store/synonyms_test.go` with tests for: bidirectional lookup, case insensitivity, unknown terms returning nil, `HasSynonym` true/false cases
- [ ] Add unexported function `sanitizeQueryWithSynonyms(query string) string` in `internal/store/search.go` — tokenize, expand each term via `ExpandSynonyms`, wrap groups in FTS5 parentheses `("term" OR "syn1")`, join with space (implicit AND)
- [ ] Add unexported function `sanitizeTrigramWithSynonyms(query string) string` in `internal/store/search.go` — same logic but apply existing min-3-char filter to each term/synonym
- [ ] Modify `rrfSearch` in `internal/store/search.go` to call the new synonym-aware sanitizers instead of `sanitizeQuery`/`sanitizeTrigramQuery`. Add zero-result fallback: if both layers return zero results with grouped AND, re-run with original flat OR sanitizers
- [ ] Add early return in `fuzzyCorrectWord` when `HasSynonym(word)` is true
- [ ] Add search integration tests in `internal/store/search_test.go`: `TestSynonymExpansionPorter`, `TestSynonymExpansionTrigram`, `TestSynonymFallbackToOR`, `TestSynonymSkipsFuzzy`, `TestNoSynonymPassthrough`
- [ ] Verify all existing search tests still pass (no regressions from the AND grouping change)

## Task 2: Secret Stripping Before Indexing

**Status:** pending
**Dependencies:** none
**Docs section:** [implementation.md → Feature 2](./implementation.md#feature-2-secret-stripping)

- [ ] Create `internal/sanitize/sanitize.go` with package-level compiled regexes (all 12 patterns from the design doc) in `init()`, and exported function `StripSecrets(content string) string`
- [ ] Create `internal/sanitize/sanitize_test.go` with tests for: each individual pattern type, multiple patterns in one string, private tags, non-secrets preserved, short tokens not false-positived
- [ ] Modify `ContentStore.Index()` in `internal/store/index.go` — add `content = sanitize.StripSecrets(content)` after `DetectContentType(content)` and before `contentHash(content)`. Add import for `internal/sanitize`
- [ ] Add integration test in `internal/store/store_test.go` or `search_test.go`: index content containing a known secret pattern, then search for it, verify the secret is absent from results and replaced with `[REDACTED_SECRET]`

## Task 3: Per-Source Result Diversification

**Status:** pending
**Dependencies:** none
**Docs section:** [implementation.md → Feature 3](./implementation.md#feature-3-per-source-result-diversification)

- [ ] Add `MaxPerSource int` field to `SearchOptions` in `internal/store/types.go`
- [ ] Add unexported function `diversifyBySource(results []SearchResult, limit, maxPerSource int) []SearchResult` in `internal/store/search.go` — two-pass algorithm: first pass enforces per-`SourceID` cap, second pass fills remaining slots
- [ ] Integrate `diversifyBySource` into `rrfSearch` — call after proximity reranking, before final `[:limit]`. Use `opts.MaxPerSource` with default 2 when zero
- [ ] Add tests in `internal/store/search_test.go`: `TestDiversifyBySource` (3 sources, broad query, verify cap), `TestDiversifyFillsRemaining` (second pass fills), `TestDiversifyNoReduction` (total count preserved), `TestDiversifySingleSource` (all from one source when no alternatives)

## Task 4: Entity-Aware Query Boosting

**Status:** pending
**Dependencies:** none
**Docs section:** [implementation.md → Feature 4](./implementation.md#feature-4-entity-aware-query-boosting)

- [ ] Create `internal/store/entity.go` with stop word map (init), exported `ExtractEntities(query string) []string` (quoted phrases + capitalized identifiers, filtered), and exported `BoostByEntities(results []SearchResult, entities []string) []SearchResult` (score multiplier + re-sort)
- [ ] Create `internal/store/entity_test.go` with tests for: quoted phrase extraction, capitalized identifier extraction, stop word filtering, no entities from lowercase query, deduplication, boost changes scores, boost re-sorts, no-entity passthrough
- [ ] Integrate into `SearchWithFallback` in `internal/store/search.go` — after fuzzy merge and diversification, extract entities from original query, apply `BoostByEntities` to results before returning

## Task 5: Retention-Scored Cleanup

**Status:** pending
**Dependencies:** none
**Docs section:** [implementation.md → Feature 5](./implementation.md#feature-5-retention-scored-cleanup)

- [ ] Add `RetentionScore float64` field to `SourceInfo` in `internal/store/types.go`
- [ ] Add unexported function `retentionScore(src SourceInfo, now time.Time) float64` in `internal/store/cleanup.go` — implements the formula from the design doc (salience by content type, temporal decay, access boost)
- [ ] Modify `classifyTier` signature in `internal/store/cleanup.go` to accept `SourceInfo` instead of just `LastAccessedAt`. Compute retention score and map to tier via thresholds (hot >= 0.7, warm >= 0.4, cold >= 0.15, below = evictable)
- [ ] Update `ClassifySources()` to pass full `SourceInfo` to `classifyTier` and populate `RetentionScore` field
- [ ] Update `Cleanup()` eviction logic to use `retentionScore < 0.15 AND access_count == 0`
- [ ] Add new tests in `internal/store/cleanup_test.go`: `TestRetentionScoreNewCodeSource`, `TestRetentionScoreOldUnaccessed`, `TestRetentionScoreOldButAccessed`, `TestRetentionScoreContentTypeWeight`, `TestClassifyTierThresholds`
- [ ] Update existing cleanup tests (`TestClassifySources`, `TestCleanupDryRun`, `TestCleanupForce`, `TestCleanupPreservesRecentlyAccessed`, `TestCleanupPreservesAccessedSources`) to work with the new scoring — verify backward-compatible behavior

## Task 6: Final Verification

**Status:** pending
**Dependencies:** Task 1, Task 2, Task 3, Task 4, Task 5
**Docs section:** n/a

- [ ] Run `testing-process` — full test suite passes with `make test` and `make test-race`
- [ ] Run `solid-code-review` with `go` — review all new and modified code
- [ ] Run `implementation-review` — verify implementation matches design.md and implementation.md
- [ ] Run `documentation-process` — update CONTRIBUTING.md if needed (new `internal/sanitize` package in project structure), no other doc changes expected
