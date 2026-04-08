# Implementation Plan: Search & Security Improvements

**Design doc:** [design.md](./design.md)
**Task list:** [tasks.md](./tasks.md)

## Architecture

All five features are additive and independent. They slot into the existing `internal/store` pipeline at well-defined points. One new package (`internal/sanitize`) is introduced.

### Data flow with new features

```
Content arrives (tool handler)
    │
    ▼
ContentStore.Index()
    ├── content-type detection (existing)
    ├── ★ StripSecrets()              ← Feature 2
    ├── content hash (existing, now post-stripping)
    ├── chunking (existing)
    └── FTS5 insert (existing)

Search query arrives (tool handler)
    │
    ▼
ContentStore.SearchWithFallback()
    ├── ★ extractEntities(query)      ← Feature 4 (extraction only)
    ├── ★ expandSynonyms(query)       ← Feature 1
    ├── rrfSearch(expanded)
    │   ├── searchPorter (concurrent) ← uses expanded query
    │   ├── searchTrigram (concurrent) ← uses expanded query
    │   ├── RRF fusion (existing)
    │   └── proximity rerank (existing)
    ├── fuzzy fallback (existing, skips synonym-known terms)
    ├── ★ diversifyBySource()         ← Feature 3
    ├── ★ boostEntities()             ← Feature 4 (boosting)
    └── return results

Cleanup / Stats
    │
    ▼
ContentStore.ClassifySources()
    └── ★ retentionScore()            ← Feature 5
```

## Feature 1: Domain Synonym Expansion

### Files to create

- `internal/store/synonyms.go` — synonym map and expansion function

### Files to modify

- `internal/store/search.go` — integrate synonym expansion into `sanitizeQuery` and `sanitizeTrigramQuery`

### Implementation details

**`internal/store/synonyms.go`:**

- Define a package-level `var synonymMap map[string][]string` initialized in `init()`. The map key is each word in a synonym group (lowercased), the value is all other words in that group. For the group `["db", "database", "datastore"]`, the map has three entries: `"db" → ["database", "datastore"]`, `"database" → ["db", "datastore"]`, `"datastore" → ["db", "database"]`. The `init()` function panics if any term appears in multiple synonym groups (prevents silent overwrites when extending the list) or overlaps with the stopword list (prevents silent query failures — stopwords are stripped at index time so synonym expansion would match nothing).
- Export function `ExpandSynonyms(term string) []string` — returns synonyms for the lowercased term, or nil if no match.
- Export function `HasSynonym(term string) bool` — used by fuzzy correction to skip known terms.

**`internal/store/search.go`:**

- New unexported function `sanitizePorterQuery(query, mode string, expandSyns bool) string` — applies `ftsSpecialRe` cleanup, tokenizes terms, expands each via `ExpandSynonyms` when `expandSyns` is true, wraps synonym groups in FTS5 parentheses `("term" OR "syn1" OR "syn2")`, joins groups with space (implicit AND) or `" OR "` depending on `mode`. Note: quoted phrase preservation is not yet implemented — all FTS5 special characters (including quotes) are stripped before tokenization.
- New unexported function `sanitizeTrigramQuery(query, mode string, expandSyns bool) string` — same logic but applies the existing trigram min-3-char filter to each term/synonym; short terms (<3 chars) are dropped but their longer synonyms are kept.
- `SearchWithFallback` calls the new functions and passes pre-sanitized queries to `rrfSearch`.
- **Fallback:** `SearchWithFallback` calls `rrfSearch` with the synonym-expanded AND query. If the result count is zero, it re-calls `rrfSearch` with a flat-OR query (using `sanitizePorterQuery`/`sanitizeTrigramQuery` with `expandSyns=false`). Synonym expansion is intentionally dropped in the OR fallback to avoid relevance dilution. `rrfSearch` accepts pre-sanitized Porter and trigram query strings as parameters rather than calling sanitize functions internally.
- `fuzzyCorrectWord` — add early return if `HasSynonym(word)` is true (synonym-known words don't need fuzzy correction).

### Testing

- `internal/store/synonyms_test.go` — unit tests for `ExpandSynonyms`, `HasSynonym`, edge cases (unknown terms, case insensitivity, bidirectional lookup)
- `internal/store/search_test.go` — new tests:
  - `TestSynonymExpansionPorter` — index content with "database performance", search "db perf", verify match
  - `TestSynonymExpansionTrigram` — same for trigram layer
  - `TestSynonymFallbackToOR` — search with 2 terms where only 1 exists in corpus, verify results still returned
  - `TestSynonymSkipsFuzzy` — verify synonym-known terms bypass fuzzy correction
  - `TestNoSynonymPassthrough` — terms without synonyms are unaffected

## Feature 2: Secret Stripping

### Files to create

- `internal/sanitize/sanitize.go` — `StripSecrets` function with compiled regexes
- `internal/sanitize/sanitize_test.go` — tests

### Files to modify

- `internal/store/index.go` — call `StripSecrets` in `Index()`

### Implementation details

**`internal/sanitize/sanitize.go`:**

- Package-level `var secretPatterns []*regexp.Regexp` compiled at package level via `regexp.MustCompile`.
- Private tag pattern compiled separately: `(?is)<private>.*?</private>`.
- Export function `StripSecrets(content string) string` — applies private tag stripping first, then all secret patterns. Replacements: private tags → `[REDACTED]`, secrets → `[REDACTED_SECRET]`. Returns the sanitized string.
- For the private tag regex, guard with `strings.Contains(strings.ToLower(content), "<private>")` before running the regex — avoids expensive regex evaluation on the common case where no private tags are present, while correctly handling mixed-case variants.
- Patterns are compiled once at init — no per-call regex compilation.

**`internal/store/index.go`:**

- **Code reorder required:** The current `Index()` calls `contentHash()` *before* `DetectContentType()`. Both must be moved below `StripSecrets()` so that: (1) content type is detected on original content, (2) secrets are stripped, (3) hash is computed on stripped content. The new order is: `DetectContentType(content)` → `content = sanitize.StripSecrets(content)` → `contentHash(content)`.
- Import `"github.com/serpro69/capy/internal/sanitize"`.

### Testing

- `internal/sanitize/sanitize_test.go`:
  - `TestStripGenericKeyValue` — `api_key=sk-abc123...` → redacted
  - `TestStripAnthropicKey` — `sk-ant-api03-xxx...` → redacted
  - `TestStripGitHubPAT` — `ghp_xxx...` → redacted
  - `TestStripAWSKey` — `AKIAIOSFODNN7EXAMPLE` → redacted
  - `TestStripJWT` — three-segment base64url → redacted
  - `TestStripPrivateTag` — `<private>secret data</private>` → redacted
  - `TestStripMultiplePatterns` — content with several different secret types
  - `TestPreservesNonSecrets` — normal code/text passes through unchanged
  - `TestShortTokensNotStripped` — short strings that look like prefixes but aren't long enough
  - `TestFalsePositiveStructLiteral` — Go struct literal like `token: "SomeLongTestValueForMocking"` is not redacted
  - `TestFalsePositiveMockToken` — test code with mock tokens (e.g., `testToken := "abcdef..."`) is not redacted unless it matches a known prefix pattern
- Integration: add a test in `internal/store/search_test.go` or `store_test.go` verifying that indexing content containing a secret results in the secret being absent from search results

## Feature 3: Per-Source Result Diversification

### Files to modify

- `internal/store/search.go` — add diversification function, integrate into `SearchWithFallback`
- `internal/store/types.go` — add `MaxPerSource` field to `SearchOptions`

### Implementation details

**`internal/store/search.go`:**

- New unexported function `diversifyBySource(results []SearchResult, limit, maxPerSource int) []SearchResult`:
  - First pass: walk results in rank order, count per `SourceID`. Skip results where count >= `maxPerSource`.
  - Second pass: if selected < limit, fill with previously-skipped results.
  - Return selected slice.
- **Over-fetching:** `rrfSearch` uses a fetch multiplier of 5× (`fetchLimit = requestedLimit * 5`) to ensure diversification has a meaningful candidate pool. Without this, a single dominant source could fill all `limit` slots, leaving the second pass with nothing to backfill.
- Call `diversifyBySource` in `SearchWithFallback` after `mergeRRFResults` (fuzzy merge) and before entity boosting. This ensures diversification sees the full candidate set and is applied exactly once. Do **not** call inside `rrfSearch` (would run twice and be undone by fuzzy merge). Final truncation to the requested `limit` happens after diversification.
- Default `maxPerSource`: 2 (when `SearchOptions.MaxPerSource` is 0).

**`internal/store/types.go`:**

- Add `MaxPerSource int` field to `SearchOptions` struct.

### Testing

- `internal/store/search_test.go`:
  - `TestDiversifyBySource` — index 3 sources (A: 5 chunks, B: 2 chunks, C: 1 chunk), search broad query, verify max 2 results from source A
  - `TestDiversifyFillsRemaining` — when cap limits source A to 2, verify B and C results fill remaining slots
  - `TestDiversifyNoReduction` — total result count is not reduced by diversification (second pass fills)
  - `TestDiversifySingleSource` — when only one source exists, all results come from it (second pass)

## Feature 4: Entity-Aware Query Boosting

### Files to create

- `internal/store/entity.go` — entity extraction and boosting functions

### Files to modify

- `internal/store/search.go` — integrate entity boosting into `SearchWithFallback`

### Implementation details

**`internal/store/entity.go`:**

- Package-level `var entityStopWords map[string]bool` initialized in `init()` with the stop word list from the design doc.
- Export function `ExtractEntities(query string) []string`:
  - Extract quoted strings via regex `"([^"]+)"`.
  - Extract capitalized identifiers via regex `\b[A-Z][a-zA-Z0-9_.-]+\b`.
  - **Sentence-starter filter:** A single capitalized word at position 0 that lacks identifier patterns (no underscores, dots, or interior capitals) is excluded.
  - Filter stop words.
  - Filter length < 2.
  - Deduplicate and return.
- Export function `BoostByEntities(results []SearchResult, entities []string) []SearchResult`:
  - If no entities, return results unchanged.
  - For each result, count **word-boundary** matches of entities in `result.Content`. Single-word entities use case-sensitive matching; multi-word quoted phrases use case-insensitive matching. This prevents short entities like `"DB"` from matching inside words like `"sandbox"`.
  - Cap `matchCount` at 5 (max boost of 2.5×).
  - Multiply `FusedScore` by `(1.0 + 0.3 * min(matchCount, 5))`.
  - Re-sort by FusedScore descending.
  - Return.

**`internal/store/search.go`:**

- In `SearchWithFallback`, after `rrfSearch` returns (and after the fuzzy merge), extract entities and apply boosting before returning.
- Ordering: `rrfSearch` → fuzzy merge → diversify → entity boost → return.

### Testing

- `internal/store/entity_test.go`:
  - `TestExtractQuotedPhrases` — `"React useEffect" cleanup` → `["React useEffect"]`
  - `TestExtractCapitalizedIdentifiers` — `ContentStore FTS5 search` → `["ContentStore", "FTS5"]`
  - `TestExtractFiltersStopWords` — `The ContentStore` → `["ContentStore"]` (not "The")
  - `TestExtractNoEntities` — `database migration` → `[]`
  - `TestExtractDeduplicates` — repeated entities → deduplicated
  - `TestBoostWithEntities` — results containing entity get higher score
  - `TestBoostNoEntitiesPassthrough` — no entities → results unchanged
  - `TestBoostResortsResults` — lower-ranked result with entity match moves up
  - `TestExtractSentenceStarter` — `Getting started with deploy` → `[]` (sentence starter excluded)
  - `TestExtractCamelCaseNotFiltered` — `GetConfig options` → `["GetConfig"]` (camelCase preserved)
  - `TestBoostWordBoundary` — entity `"DB"` does not boost result containing only `"sandbox"`
  - `TestBoostMatchCountCapped` — entity mentioned 50 times → matchCount capped at 5

## Feature 5: Retention-Scored Cleanup

### Files to modify

- `internal/store/cleanup.go` — replace `classifyTier` with retention scoring
- `internal/store/types.go` — add `RetentionScore float64` to `SourceInfo` (optional, for observability)

### Implementation details

**`internal/store/cleanup.go`:**

- New unexported function `retentionScore(src SourceInfo, now time.Time) float64`:
  - Compute salience from content type: if `src.CodeChunkCount > 0 && src.CodeChunkCount == src.ChunkCount` → 0.7, if `src.CodeChunkCount > 0` → 0.6, else 0.5.
  - Add access bonus: `min(0.2, float64(src.AccessCount) * 0.02)`.
  - Compute temporal decay: `math.Exp(-0.045 * daysSinceIndexed)`.
  - Compute access boost: `1.0 / (1.0 + daysSinceLastAccess)`. If `LastAccessedAt` is zero, access boost is 0.
  - Return `salience * temporalDecay + 0.3 * accessBoost`.
- Replace `classifyTier(lastAccessed time.Time, now time.Time) string` with `classifyTier(src SourceInfo, now time.Time) (string, float64)` — calls `retentionScore` and maps to tier string using thresholds (hot >= 0.7, warm >= 0.4, cold >= 0.15, "evictable" below). Returns the score alongside the tier to avoid recomputation in `ClassifySources`.
- Update `ClassifySources()` — passes full `SourceInfo` to `classifyTier`.
- Update `Cleanup()` — eviction candidates are sources with `retentionScore < 0.15 AND access_count == 0`. **Important:** The current code checks `Tier != "cold"` to skip non-candidates — this must be updated to also accept "evictable" (or switch to score-based check directly).
- Update `Stats()` — the current switch handles only "hot", "warm", "cold". Add "evictable" case as a new `EvictableCount` field on `StoreStats` (dedicated field preferred over folding into `ColdCount` — consistent with the observability goal of exposing `RetentionScore`). Update the `SourceInfo.Tier` comment in `types.go` to list all four tier values.
- Populate `SourceInfo.RetentionScore` during `ClassifySources()` for observability in `capy_stats`.

**`internal/store/types.go`:**

- Add `RetentionScore float64` field to `SourceInfo`.

### Testing

- `internal/store/cleanup_test.go`:
  - `TestRetentionScoreNewCodeSource` — recently indexed code source scores high (hot)
  - `TestRetentionScoreOldUnaccessed` — 90-day-old never-accessed prose source scores low (evictable)
  - `TestRetentionScoreOldButAccessed` — old source with high access count scores warm/hot due to access boost
  - `TestRetentionScoreContentTypeWeight` — code scores higher than prose for same age/access
  - `TestClassifyTierThresholds` — verify each tier boundary
  - `TestCleanupUsesRetentionScore` — verify eviction uses score < 0.15 AND access_count == 0
  - Update existing tests (`TestClassifySources`, `TestCleanupDryRun`, `TestCleanupForce`, etc.) to work with the new scoring — the behavior should be backward-compatible for the common cases (very old never-accessed sources still get cleaned up)
