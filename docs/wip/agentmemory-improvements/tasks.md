# Tasks: Search & Security Improvements

**Design:** [design.md](./design.md)
**Implementation:** [implementation.md](./implementation.md)
**Status:** in-progress
**Feature branch:** `feat/agentmemory-ports`

---

## Task 1: Domain Synonym Expansion

**Status:** done
**Dependencies:** none
**Docs section:** [implementation.md → Feature 1](./implementation.md#feature-1-domain-synonym-expansion)

- [x] Create `internal/store/synonyms.go` with the synonym map (all ~40 groups from the design doc), `init()` to build the bidirectional lookup map, and exported functions `ExpandSynonyms(term string) []string` and `HasSynonym(term string) bool`
- [x] Create `internal/store/synonyms_test.go` with tests for: bidirectional lookup, case insensitivity, unknown terms returning nil, `HasSynonym` true/false cases
- [x] Add unexported function `sanitizeQueryWithSynonyms(query string) string` in `internal/store/search.go` — tokenize, expand each term via `ExpandSynonyms`, wrap groups in FTS5 parentheses `("term" OR "syn1")`, join with space (implicit AND)
- [x] Add unexported function `sanitizeTrigramWithSynonyms(query string) string` in `internal/store/search.go` — same logic but apply existing min-3-char filter to each term/synonym
- [x] Modify `rrfSearch` in `internal/store/search.go` to call the new synonym-aware sanitizers instead of `sanitizeQuery`/`sanitizeTrigramQuery`. Add zero-result fallback: if both layers return zero results with grouped AND, re-run with original flat OR sanitizers
- [x] Add early return in `fuzzyCorrectWord` when `HasSynonym(word)` is true
- [x] Add search integration tests in `internal/store/search_test.go`: `TestSynonymExpansionPorter`, `TestSynonymExpansionTrigram`, `TestSynonymFallbackToOR`, `TestSynonymSkipsFuzzy`, `TestNoSynonymPassthrough`
- [x] Verify all existing search tests still pass (no regressions from the AND grouping change)

## Task 2: Secret Stripping Before Indexing

**Status:** done
**Dependencies:** none
**Docs section:** [implementation.md → Feature 2](./implementation.md#feature-2-secret-stripping)

- [x] Create `internal/sanitize/sanitize.go` with package-level compiled regexes (all 12 patterns from the design doc) in `init()`, and exported function `StripSecrets(content string) string`
- [x] Create `internal/sanitize/sanitize_test.go` with tests for: each individual pattern type, multiple patterns in one string, private tags, non-secrets preserved, short tokens not false-positived
- [x] Modify `ContentStore.Index()` in `internal/store/index.go` — add `content = sanitize.StripSecrets(content)` after `DetectContentType(content)` and before `contentHash(content)`. Add import for `internal/sanitize`
- [x] Add integration test in `internal/store/store_test.go` or `search_test.go`: index content containing a known secret pattern, then search for it, verify the secret is absent from results and replaced with `[REDACTED_SECRET]`

## Task 3: Per-Source Result Diversification

**Status:** done
**Dependencies:** none
**Docs section:** [implementation.md → Feature 3](./implementation.md#feature-3-per-source-result-diversification)

- [x] Add `MaxPerSource int` field to `SearchOptions` in `internal/store/types.go`
- [x] Add unexported function `diversifyBySource(results []SearchResult, limit, maxPerSource int) []SearchResult` in `internal/store/search.go` — two-pass algorithm: first pass enforces per-`SourceID` cap, second pass fills remaining slots
- [x] Integrate `diversifyBySource` into `SearchWithFallback` — call after fuzzy merge, before `trackAccess`. Use `opts.MaxPerSource` with default 2 when zero. Removed truncation from `rrfSearch` and `mergeRRFResults`; increased fetch multiplier to 5× for over-fetching
- [x] Add tests in `internal/store/search_test.go`: `TestDiversifyBySource` (3 sources, broad query, verify cap), `TestDiversifyFillsRemaining` (second pass fills), `TestDiversifyNoReduction` (total count preserved), `TestDiversifySingleSource` (all from one source when no alternatives). Also added unit tests: `TestDiversifyBySourceUnit`, `TestDiversifyBySourceEmpty`, `TestDiversifyBySourceAllUnique`

## Task 4: Entity-Aware Query Boosting

**Status:** done
**Dependencies:** none
**Docs section:** [implementation.md → Feature 4](./implementation.md#feature-4-entity-aware-query-boosting)

- [x] Create `internal/store/entity.go` with stop word map (init), exported `ExtractEntities(query string) []string` (quoted phrases + capitalized identifiers, filtered), and exported `BoostByEntities(results []SearchResult, entities []string) []SearchResult` (score multiplier + re-sort)
- [x] Create `internal/store/entity_test.go` with tests for: quoted phrase extraction, capitalized identifier extraction, stop word filtering, no entities from lowercase query, deduplication, boost changes scores, boost re-sorts, no-entity passthrough
- [x] Integrate into `SearchWithFallback` in `internal/store/search.go` — after fuzzy merge and diversification, extract entities from original query, apply `BoostByEntities` to results before returning

## Task 5: Retention-Scored Cleanup

**Status:** done
**Dependencies:** none
**Docs section:** [implementation.md → Feature 5](./implementation.md#feature-5-retention-scored-cleanup)

- [x] Add `RetentionScore float64` field to `SourceInfo` in `internal/store/types.go`
- [x] Add unexported function `retentionScore(src SourceInfo, now time.Time) float64` in `internal/store/cleanup.go` — implements the formula from the design doc (salience by content type, temporal decay, access boost)
- [x] Modify `classifyTier` signature in `internal/store/cleanup.go` to accept `SourceInfo` instead of just `LastAccessedAt`. Compute retention score and map to tier via thresholds (hot >= 0.7, warm >= 0.4, cold >= 0.15, below = evictable)
- [x] Update `ClassifySources()` to pass full `SourceInfo` to `classifyTier` and populate `RetentionScore` field
- [x] Update `Cleanup()` eviction logic to use `retentionScore < 0.15 AND access_count == 0`
- [x] Add new tests in `internal/store/cleanup_test.go`: `TestRetentionScoreNewCodeSource`, `TestRetentionScoreOldUnaccessed`, `TestRetentionScoreOldButAccessed`, `TestRetentionScoreContentTypeWeight`, `TestClassifyTierThresholds`
- [x] Update existing cleanup tests (`TestClassifySources`, `TestCleanupDryRun`, `TestCleanupForce`, `TestCleanupPreservesRecentlyAccessed`, `TestCleanupPreservesAccessedSources`) to work with the new scoring — verify backward-compatible behavior

## Task 6: Final Verification

**Status:** done
**Dependencies:** Task 1, Task 2, Task 3, Task 4, Task 5
**Docs section:** n/a

- [x] Run `testing-process` — full test suite passes with `make test` and `make test-race`
- [x] Run `solid-code-review` with `go` — review all new and modified code
- [x] Run `implementation-review` — verify implementation matches design.md and implementation.md
- [x] Run `documentation-process` — update CONTRIBUTING.md if needed (new `internal/sanitize` package in project structure), no other doc changes expected

## Task 7: Post-Implementation Improvements

**Status:** in-progress
**Dependencies:** Task 6
**Docs section:** [design.md → Addendum](./design.md#addendum-post-implementation-improvements)

- [x] Run `analysis-process` on the design addendum improvements (currently: Improvement A — synonym-aware proximity reranking). Produce `implementation-v2.md` and `tasks-v2.md` with detailed implementation steps
- [ ] Execute `tasks-v2.md` via `implementation-process`
