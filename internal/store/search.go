package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ftsSpecialRe    = regexp.MustCompile(`['"(){}[\]*:^~]`)
	trigramCleanRe  = regexp.MustCompile(`[^a-zA-Z0-9 _-]`)
	ftsKeywords     = map[string]bool{"AND": true, "OR": true, "NOT": true, "NEAR": true}
)

// SearchWithFallback runs Reciprocal Rank Fusion across porter and trigram
// layers, with fuzzy correction as a secondary pass when results are sparse.
func (s *ContentStore) SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	// RRF pass 1: synonym-expanded query (implicit AND between groups).
	synPorter := sanitizePorterQuery(query, "AND", true)
	synTrigram := sanitizeTrigramQuery(query, "AND", true)
	results := s.rrfSearch(synPorter, synTrigram, query, limit, opts)

	// Fallback: if synonym AND grouping returned zero results, retry with
	// flat OR using the user's original terms as a precision anchor.
	// Synonym expansion is intentionally dropped here to avoid relevance
	// dilution (e.g., "latency" expanding to "slow" in OR mode would drown
	// the user's intent with unrelated matches).
	if len(results) == 0 {
		flatPorter := sanitizePorterQuery(query, "OR", false)
		flatTrigram := sanitizeTrigramQuery(query, "OR", false)
		results = s.rrfSearch(flatPorter, flatTrigram, query, limit, opts)
	}

	// RRF pass 2: fuzzy correction (only if pass 1 returned fewer than limit).
	// Corrected queries re-enter the synonym AND pass first — a typo like
	// "authentcation" corrected to "authentication" should get full synonym
	// expansion, not just flat OR.
	if len(results) < limit {
		corrected := s.fuzzyCorrectQuery(query)
		if corrected != "" && corrected != query {
			// Try synonym AND on corrected query first.
			fzPorter := sanitizePorterQuery(corrected, "AND", true)
			fzTrigram := sanitizeTrigramQuery(corrected, "AND", true)
			fuzzyResults := s.rrfSearch(fzPorter, fzTrigram, corrected, limit, opts)
			// If synonym AND on corrected query also returns nothing, fall back to flat OR.
			if len(fuzzyResults) == 0 {
				fzPorter = sanitizePorterQuery(corrected, "OR", false)
				fzTrigram = sanitizeTrigramQuery(corrected, "OR", false)
				fuzzyResults = s.rrfSearch(fzPorter, fzTrigram, corrected, limit, opts)
			}
			for i := range fuzzyResults {
				fuzzyResults[i].MatchLayer = "fuzzy+" + fuzzyResults[i].MatchLayer
			}
			results = mergeRRFResults(results, fuzzyResults)
		}
	}

	// Per-source diversification: cap results from any single source,
	// then fill remaining slots with skipped results.
	maxPerSource := opts.MaxPerSource
	if maxPerSource <= 0 {
		maxPerSource = 2
	}
	results = diversifyBySource(results, limit, maxPerSource)

	// Entity-aware boosting: extract quoted phrases and capitalized
	// identifiers from the original query, boost results that contain them.
	entities := ExtractEntities(query)
	results = BoostByEntities(results, entities)

	if len(results) > 0 {
		s.trackAccess(results)
	}
	return results, nil
}

const rrfK = 60 // standard RRF constant

// rrfSearch runs porter and trigram searches concurrently, fuses results
// using Reciprocal Rank Fusion, applies proximity reranking, and returns
// candidates. It accepts pre-sanitized FTS5 query strings for both
// layers so the caller can control synonym expansion vs flat-OR fallback.
// rawQuery is the original unsanitized query, used for proximity reranking.
//
// Note: rrfSearch intentionally does NOT truncate results to limit. It fetches
// limit*5 candidates per layer to give the caller (SearchWithFallback) a large
// enough pool for diversification and entity boosting. The caller is responsible
// for applying the final limit after post-processing.
func (s *ContentStore) rrfSearch(porterQuery, trigramQuery, rawQuery string, limit int, opts SearchOptions) []SearchResult {
	fetchLimit := max(limit*5, 10)

	// Run both layers concurrently — SQLite WAL supports concurrent readers.
	var porterResults, trigramResults []SearchResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if porterQuery != "" {
			porterResults = s.execDynamicSearch("chunks", porterQuery, fetchLimit, opts)
		}
	}()
	go func() {
		defer wg.Done()
		if trigramQuery != "" {
			trigramResults = s.execDynamicSearch("chunks_trigram", trigramQuery, fetchLimit, opts)
		}
	}()
	wg.Wait()

	// Build fusion map keyed by (sourceID, title).
	type fusedEntry struct {
		result     SearchResult
		fusedScore float64
	}
	fusionMap := make(map[string]*fusedEntry)

	addLayer := func(results []SearchResult, layerName string) {
		for i, r := range results {
			key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
			score := 1.0 / (float64(rrfK) + float64(i))
			if entry, ok := fusionMap[key]; ok {
				entry.fusedScore += score
				// Keep the version with the better individual rank.
				if r.Rank < entry.result.Rank {
					entry.result = r
				}
			} else {
				r.MatchLayer = layerName
				fusionMap[key] = &fusedEntry{result: r, fusedScore: score}
			}
		}
	}

	addLayer(porterResults, "porter")
	addLayer(trigramResults, "trigram")

	// Flatten and sort by fused score descending.
	fused := make([]SearchResult, 0, len(fusionMap))
	for _, entry := range fusionMap {
		entry.result.FusedScore = entry.fusedScore
		fused = append(fused, entry.result)
	}
	sort.Slice(fused, func(i, j int) bool {
		return fused[i].FusedScore > fused[j].FusedScore
	})

	// Tag multi-layer hits. A result appearing in both layers scores above
	// the single-layer max of 1/(60+0) ≈ 0.01667.
	singleLayerMax := 1.0 / float64(rrfK)
	for i := range fused {
		if fused[i].FusedScore > singleLayerMax {
			fused[i].MatchLayer = "rrf(porter+trigram)"
		}
	}

	fused = rerank(fused, rawQuery)

	return fused
}

// mergeRRFResults deduplicates primary and secondary results by (sourceID, title).
// On conflict, the primary version is kept. Does not truncate — the caller
// applies diversification and final limit.
func mergeRRFResults(primary, secondary []SearchResult) []SearchResult {
	seen := make(map[string]bool, len(primary))
	for _, r := range primary {
		key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
		seen[key] = true
	}

	merged := make([]SearchResult, len(primary))
	copy(merged, primary)

	for _, r := range secondary {
		key := fmt.Sprintf("%d:%s", r.SourceID, r.Title)
		if !seen[key] {
			seen[key] = true
			merged = append(merged, r)
		}
	}

	return merged
}

// --- Per-source diversification ---

// diversifyBySource caps results from any single source to avoid a dominant
// source drowning out others. Two-pass: first pass enforces the per-source cap,
// second pass fills remaining slots with previously skipped results.
func diversifyBySource(results []SearchResult, limit, maxPerSource int) []SearchResult {
	if len(results) == 0 {
		return results
	}

	selected := make([]SearchResult, 0, min(limit, len(results)))
	var skipped []SearchResult
	counts := make(map[int64]int)

	// Pass 1: accept results in rank order, skip when source exceeds cap.
	for _, r := range results {
		if len(selected) >= limit {
			return selected
		}
		if counts[r.SourceID] >= maxPerSource {
			skipped = append(skipped, r)
			continue
		}
		counts[r.SourceID]++
		selected = append(selected, r)
	}

	// Pass 2: fill remaining slots with skipped results.
	for _, r := range skipped {
		if len(selected) >= limit {
			break
		}
		selected = append(selected, r)
	}

	return selected
}

// --- Proximity reranking ---

// rerank applies title-match boost and proximity reranking to fused results.
// Title-match boost applies to all queries (including single-term).
// Proximity boost only applies for multi-term queries (2+ terms).
func rerank(results []SearchResult, query string) []SearchResult {
	terms := filterQueryTerms(query)
	if len(terms) == 0 {
		return results
	}

	// Title-match boost: reward results whose title contains query terms.
	for i := range results {
		r := &results[i]
		lowerTitle := strings.ToLower(r.Title)
		titleHits := 0
		for _, t := range terms {
			if strings.Contains(lowerTitle, t) {
				titleHits++
			}
		}
		if titleHits > 0 {
			weight := 0.3
			if r.ContentType == "code" {
				weight = 0.6
			}
			titleBoost := weight * (float64(titleHits) / float64(len(terms)))
			r.FusedScore *= (1.0 + titleBoost)
		}
	}

	// Proximity span boost (multi-term only).
	if len(terms) >= 2 {
		termGroups := make([][]string, len(terms))
		for i, w := range terms {
			if syns := ExpandSynonyms(w); len(syns) > 0 {
				group := make([]string, 0, len(syns)+1)
				group = append(group, w)
				group = append(group, syns...)
				termGroups[i] = group
			} else {
				termGroups[i] = []string{w}
			}
		}

		for i := range results {
			r := &results[i]
			minSpan := -1

			if r.Highlighted != "" {
				minSpan = findMinSpanFromHighlights(r.Highlighted, termGroups)
			}

			if minSpan < 0 {
				posLists := make([][]int, len(termGroups))
				content := strings.ToLower(r.Content)
				allFound := true
				for j, group := range termGroups {
					var merged []int
					for _, term := range group {
						merged = append(merged, findAllPositions(content, term)...)
					}
					if len(merged) == 0 {
						allFound = false
						break
					}
					sort.Ints(merged)
					posLists[j] = merged
				}
				if allFound {
					minSpan = findMinSpan(posLists)
				}
			}

			if minSpan >= 0 {
				contentLen := max(len(r.Content), 1)
				boost := 1.0 / (1.0 + float64(minSpan)/float64(contentLen))
				r.FusedScore *= (1.0 + boost)
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].FusedScore > results[j].FusedScore
	})
	return results
}

// findMinSpanFromHighlights extracts match positions from FTS5 highlight
// markers (char(2) = start, char(3) = end) and finds the minimum window
// containing all term groups. Each group is a synonym set — a match
// against any term in the group counts. Single-pass: tracks stripped
// byte offset incrementally to avoid repeated string allocations.
func findMinSpanFromHighlights(highlighted string, termGroups [][]string) int {
	posLists := make([][]int, len(termGroups))
	pos := 0
	strippedPos := 0

	for {
		startIdx := strings.IndexByte(highlighted[pos:], 2)
		if startIdx < 0 {
			break
		}
		startIdx += pos

		endIdx := strings.IndexByte(highlighted[startIdx:], 3)
		if endIdx < 0 {
			break
		}
		endIdx += startIdx

		// Advance stripped position by the unhighlighted text before this marker.
		strippedPos += startIdx - pos

		matched := strings.ToLower(highlighted[startIdx+1 : endIdx])
		for i, group := range termGroups {
			for _, term := range group {
				if strings.Contains(matched, term) {
					posLists[i] = append(posLists[i], strippedPos)
					break // one match per group per highlight span
				}
			}
		}

		// Advance stripped position by the matched text length.
		strippedPos += endIdx - (startIdx + 1)
		pos = endIdx + 1
	}

	for _, list := range posLists {
		if len(list) == 0 {
			return -1
		}
	}
	return findMinSpan(posLists)
}

// findAllPositions returns all start positions of term in text.
func findAllPositions(text, term string) []int {
	var positions []int
	start := 0
	for {
		idx := strings.Index(text[start:], term)
		if idx < 0 {
			break
		}
		positions = append(positions, start+idx)
		start += idx + 1
	}
	return positions
}

// findMinSpan finds the minimum window containing at least one element from
// each position list using a sweep-line algorithm.
func findMinSpan(positionLists [][]int) int {
	n := len(positionLists)
	if n == 0 {
		return 0
	}

	// Initialize pointers — one per list.
	ptrs := make([]int, n)
	best := math.MaxInt

	for {
		// Find current min and max positions across all pointers.
		curMin, curMax := math.MaxInt, math.MinInt
		minList := 0
		for i, p := range ptrs {
			val := positionLists[i][p]
			if val < curMin {
				curMin = val
				minList = i
			}
			if val > curMax {
				curMax = val
			}
		}

		span := curMax - curMin
		if span < best {
			best = span
		}

		// Advance the pointer at the minimum position.
		ptrs[minList]++
		if ptrs[minList] >= len(positionLists[minList]) {
			break
		}
	}

	return best
}

// filterQueryTerms strips FTS5 special chars, splits, lowercases,
// deduplicates case-insensitively, and filters stopwords. Falls back
// to the deduplicated (unfiltered) list if all terms are stopwords.
func filterQueryTerms(query string) []string {
	cleaned := ftsSpecialRe.ReplaceAllString(query, " ")
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(words))
	deduped := make([]string, 0, len(words))
	for _, w := range words {
		lower := strings.ToLower(strings.Trim(w, ".,!?;:"))
		if lower == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		deduped = append(deduped, lower)
	}

	filtered := make([]string, 0, len(deduped))
	for _, w := range deduped {
		if !IsStopword(w) {
			filtered = append(filtered, w)
		}
	}

	if len(filtered) == 0 {
		return deduped
	}
	return filtered
}

// sanitizePorterQuery cleans a query for the Porter FTS5 table. When
// expandSyns is true, each term is expanded via the synonym map into an OR
// group. Mode controls how groups are joined: "AND" uses space (implicit AND
// in FTS5), "OR" uses " OR ".
// Note: quoted phrase preservation is not yet implemented — all FTS5 special
// characters (including quotes) are stripped before tokenization.
func sanitizePorterQuery(query, mode string, expandSyns bool) string {
	terms := filterQueryTerms(query)
	var groups []string
	for _, w := range terms {
		if ftsKeywords[strings.ToUpper(w)] {
			continue
		}
		if expandSyns {
			if syns := ExpandSynonyms(w); len(syns) > 0 {
				parts := make([]string, 0, len(syns)+1)
				parts = append(parts, `"`+w+`"`)
				for _, s := range syns {
					parts = append(parts, `"`+s+`"`)
				}
				groups = append(groups, "("+strings.Join(parts, " OR ")+")")
				continue
			}
		}
		groups = append(groups, `"`+w+`"`)
	}
	if len(groups) == 0 {
		return ""
	}
	sep := " "
	if mode == "OR" {
		sep = " OR "
	}
	return strings.Join(groups, sep)
}

// sanitizeTrigramQuery cleans a query for the trigram FTS5 table (min 3 chars
// per term). When expandSyns is true, each term is expanded via the synonym
// map; short terms (<3 chars) are dropped but their longer synonyms are kept.
func sanitizeTrigramQuery(query, mode string, expandSyns bool) string {
	terms := filterQueryTerms(query)
	var groups []string
	for _, w := range terms {
		subs := strings.Fields(trigramCleanRe.ReplaceAllString(w, " "))
		for _, sub := range subs {
			if expandSyns {
				if syns := ExpandSynonyms(sub); len(syns) > 0 {
					parts := make([]string, 0, len(syns)+1)
					if len(sub) >= 3 {
						parts = append(parts, `"`+sub+`"`)
					}
					for _, s := range syns {
						sc := trigramCleanRe.ReplaceAllString(s, "")
						if len(sc) >= 3 {
							parts = append(parts, `"`+sc+`"`)
						}
					}
					if len(parts) > 0 {
						if len(parts) == 1 {
							groups = append(groups, parts[0])
						} else {
							groups = append(groups, "("+strings.Join(parts, " OR ")+")")
						}
					}
					continue
				}
			}
			if len(sub) >= 3 {
				groups = append(groups, `"`+sub+`"`)
			}
		}
	}
	if len(groups) == 0 {
		return ""
	}
	sep := " "
	if mode == "OR" {
		sep = " OR "
	}
	return strings.Join(groups, sep)
}

// CountSourcesByKind returns the number of sources with the given kind.
func (s *ContentStore) CountSourcesByKind(kind SourceKind) (int, error) {
	db, err := s.getDB()
	if err != nil {
		return 0, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sources WHERE kind = ?`, string(kind)).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// effectiveKindFilter resolves the source-kind filter to apply for a search:
//   - explicit Source set: nil (caller named a source; trust the intent)
//   - empty IncludeKinds: {KindDurable} (default-exclude ephemeral)
//   - non-empty IncludeKinds: opts.IncludeKinds verbatim
//
// Returning nil means "no kind clause." Returning a slice means "filter to these kinds."
func effectiveKindFilter(opts SearchOptions) []SourceKind {
	if opts.Source != "" {
		return nil
	}
	if len(opts.IncludeKinds) == 0 {
		return []SourceKind{KindDurable}
	}
	return opts.IncludeKinds
}

// KindScopeIncludesEphemeral reports whether a search with the given options
// would include ephemeral sources, mirroring effectiveKindFilter so callers
// (e.g., the MCP layer deciding whether to surface ephemeral-recovery hints)
// never drift from the store's actual filtering rule.
func KindScopeIncludesEphemeral(opts SearchOptions) bool {
	kinds := effectiveKindFilter(opts)
	if kinds == nil {
		return true // explicit Source override — kind filter is bypassed
	}
	return slices.Contains(kinds, KindEphemeral)
}

// execDynamicSearch builds and executes a search query with dynamic WHERE clauses.
// table must be "chunks" or "chunks_trigram" (hardcoded by callers, never from user input).
func (s *ContentStore) execDynamicSearch(table, sanitized string, limit int, opts SearchOptions) []SearchResult {
	db, err := s.getDB()
	if err != nil {
		return nil
	}

	query := fmt.Sprintf(
		`SELECT s.label, c.title, c.content, c.source_id, c.content_type,
			highlight(%s, 1, char(2), char(3)) AS highlighted,
			bm25(%s, %.1f, 1.0) AS rank
		FROM %s c
		JOIN sources s ON s.id = c.source_id
		WHERE %s MATCH ?`,
		table, table, s.titleWeight, table, table,
	)
	params := []any{sanitized}

	if opts.Source != "" {
		if opts.SourceMatchMode == "exact" {
			query += " AND s.label = ?"
		} else {
			query += " AND s.label LIKE '%' || ? || '%'"
		}
		params = append(params, opts.Source)
	}
	if opts.ContentType != "" {
		query += " AND c.content_type = ?"
		params = append(params, opts.ContentType)
	}
	if kinds := effectiveKindFilter(opts); kinds != nil {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
		query += " AND s.kind IN (" + placeholders + ")"
		for _, k := range kinds {
			params = append(params, string(k))
		}
	}

	query += " ORDER BY rank LIMIT ?"
	params = append(params, limit)

	rows, err := db.Query(query, params...)
	if err != nil {
		// Warn, not Debug: a malformed query degrades silently to "no results"
		// otherwise, hiding real SQL bugs from operators.
		slog.Warn("search query failed", "error", err, "query", sanitized)
		return nil
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Label, &r.Title, &r.Content, &r.SourceID, &r.ContentType, &r.Highlighted, &r.Rank); err != nil {
			slog.Warn("search row scan failed, skipping", "error", err)
			continue
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("search row iteration failed", "error", err)
		return nil
	}
	return results
}


// trackAccess updates last_accessed_at and access_count for sources
// that appeared in search results. Runs synchronously to avoid race
// conditions with ContentStore.Close() finalizing prepared statements.
//
// Updates run inside a single transaction so the loop produces one fsync
// instead of one per source — the 5× fetch multiplier in rrfSearch pushes
// more unique sources through here than the pre-RRF path.
func (s *ContentStore) trackAccess(results []SearchResult) {
	db, err := s.getDB()
	if err != nil {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		slog.Debug("access tracking: begin tx failed", "error", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	stmt := tx.Stmt(s.stmtTrackAccess)
	seen := make(map[int64]bool)
	for _, r := range results {
		if seen[r.SourceID] {
			continue
		}
		seen[r.SourceID] = true
		if _, err := stmt.Exec(r.SourceID); err != nil {
			slog.Debug("access tracking failed", "source_id", r.SourceID, "error", err)
		}
	}
	if err := tx.Commit(); err != nil {
		slog.Debug("access tracking: commit failed", "error", err)
	}
}

// --- Levenshtein + fuzzy correction ---

func levenshteinDistance(a, b string) int {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr := make([]int, len(b)+1)
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1]
			} else {
				curr[j] = 1 + min(prev[j], curr[j-1], prev[j-1])
			}
		}
		prev = curr
	}
	return prev[len(b)]
}

func maxEditDistance(wordLen int) int {
	switch {
	case wordLen <= 4:
		return 1
	case wordLen <= 12:
		return 2
	default:
		return 3
	}
}

// fuzzyCorrectQuery corrects each word in the query using vocabulary.
// Returns the corrected query, or "" if no correction was made.
func (s *ContentStore) fuzzyCorrectQuery(query string) string {
	words := strings.Fields(strings.ToLower(query))
	corrected := false
	var result []string

	for _, word := range words {
		if len(word) < 3 {
			result = append(result, word)
			continue
		}
		if IsStopword(word) {
			result = append(result, word)
			continue
		}
		fix := s.fuzzyCorrectWord(word)
		if fix != "" {
			result = append(result, fix)
			corrected = true
		} else {
			result = append(result, word)
		}
	}

	if !corrected {
		return ""
	}
	return strings.Join(result, " ")
}

// fuzzyCorrectWord finds the closest vocabulary word within edit distance.
// Returns "" if no correction needed (exact match, synonym-known, or no close candidate).
func (s *ContentStore) fuzzyCorrectWord(word string) string {
	// Synonym-known terms don't need fuzzy correction — they're expanded at query time.
	if HasSynonym(word) {
		return ""
	}

	s.fuzzyCacheMu.Lock()
	if cached, ok := s.fuzzyCache[word]; ok {
		s.fuzzyCacheMu.Unlock()
		if cached == nil {
			return ""
		}
		return *cached
	}
	s.fuzzyCacheMu.Unlock()

	maxDist := maxEditDistance(len(word))
	minLen := len(word) - maxDist
	maxLen := len(word) + maxDist

	rows, err := s.stmtFuzzyVocab.Query(minLen, maxLen)
	if err != nil {
		return ""
	}
	defer rows.Close()

	bestWord := ""
	bestDist := maxDist + 1

	for rows.Next() {
		var candidate string
		if err := rows.Scan(&candidate); err != nil {
			continue
		}
		if candidate == word {
			s.cachefuzzy(word, nil)
			return ""
		}
		dist := levenshteinDistance(word, candidate)
		if dist < bestDist {
			bestDist = dist
			bestWord = candidate
		}
	}
	if err := rows.Err(); err != nil {
		slog.Debug("fuzzy vocab iteration failed", "error", err)
		return ""
	}

	if bestDist <= maxDist {
		s.cachefuzzy(word, &bestWord)
		return bestWord
	}
	s.cachefuzzy(word, nil)
	return ""
}

func (s *ContentStore) cachefuzzy(word string, result *string) {
	s.fuzzyCacheMu.Lock()
	if len(s.fuzzyCache) >= fuzzyCacheMaxSize {
		s.fuzzyCache = make(map[string]*string)
	}
	s.fuzzyCache[word] = result
	s.fuzzyCacheMu.Unlock()
}

// --- Distinctive terms ---

// GetDistinctiveTerms returns the most distinctive terms for a source
// based on IDF scoring across its chunks.
func (s *ContentStore) GetDistinctiveTerms(sourceID int64, maxTerms int) ([]string, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	var chunkCount int
	if err := s.stmtSourceChunkCount.QueryRow(sourceID).Scan(&chunkCount); err != nil {
		return nil, nil
	}
	if chunkCount < 3 {
		return nil, nil
	}

	totalChunks := float64(chunkCount)
	maxAppearances := max(3, int(math.Ceil(totalChunks*0.4)))

	// Count document frequency per word.
	rows, err := s.stmtChunkContent.Query(sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	docFreq := make(map[string]int)
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			continue
		}
		words := uniqueWords(content)
		for _, w := range words {
			docFreq[w]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Filter and score.
	type scored struct {
		word  string
		score float64
	}
	var candidates []scored
	for word, count := range docFreq {
		if count < 2 || count > maxAppearances {
			continue
		}
		idf := math.Log(totalChunks / float64(count))
		lenBonus := min(float64(len(word))/20.0, 0.5)
		var identifierBonus float64
		if strings.Contains(word, "_") {
			identifierBonus = 1.5
		} else if len(word) >= 12 {
			identifierBonus = 0.8
		}
		candidates = append(candidates, scored{word, idf + lenBonus + identifierBonus})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	n := min(len(candidates), maxTerms)
	result := make([]string, n)
	for i := range n {
		result[i] = candidates[i].word
	}
	return result, nil
}

// uniqueWords extracts unique words from content (3+ chars, not stopwords).
func uniqueWords(content string) []string {
	parts := wordSplitRe.Split(strings.ToLower(content), -1)
	seen := make(map[string]bool)
	var result []string
	for _, w := range parts {
		if len(w) < 3 || IsStopword(w) || seen[w] {
			continue
		}
		seen[w] = true
		result = append(result, w)
	}
	return result
}

// --- Direct queries ---

// GetChunksBySource returns all chunks for a given source.
func (s *ContentStore) GetChunksBySource(sourceID int64) ([]SearchResult, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	rows, err := s.stmtChunksBySource.Query(sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Title, &r.Content, &r.ContentType, &r.Label, &r.SourceID); err != nil {
			continue
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// GetSourceMeta returns lightweight metadata for a single source by label.
// Returns nil, nil when the source is not found.
func (s *ContentStore) GetSourceMeta(label string) (*SourceMeta, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	var meta SourceMeta
	var indexedAt string
	err := s.stmtGetSourceMeta.QueryRow(label).Scan(&meta.Label, &meta.ChunkCount, &indexedAt, &meta.Kind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	meta.IndexedAt, _ = time.Parse("2006-01-02 15:04:05", indexedAt)
	return &meta, nil
}

// ListSources returns all indexed sources.
func (s *ContentStore) ListSources() ([]SourceInfo, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	rows, err := s.stmtListSources.Query()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []SourceInfo
	for rows.Next() {
		var si SourceInfo
		var indexedAt, lastAccessedAt string
		if err := rows.Scan(&si.ID, &si.Label, &si.ContentType, &si.ChunkCount,
			&si.CodeChunkCount, &indexedAt, &lastAccessedAt, &si.AccessCount, &si.ContentHash, &si.Kind); err != nil {
			continue
		}
		si.IndexedAt, _ = time.Parse("2006-01-02 15:04:05", indexedAt)
		si.LastAccessedAt, _ = time.Parse("2006-01-02 15:04:05", lastAccessedAt)
		sources = append(sources, si)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return sources, nil
}
