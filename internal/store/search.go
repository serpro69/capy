package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
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

	// RRF pass 1: direct query.
	results := s.rrfSearch(query, limit, opts)

	// RRF pass 2: fuzzy correction (only if pass 1 returned fewer than limit).
	if len(results) < limit {
		corrected := s.fuzzyCorrectQuery(query)
		if corrected != "" && corrected != query {
			fuzzyResults := s.rrfSearch(corrected, limit, opts)
			for i := range fuzzyResults {
				fuzzyResults[i].MatchLayer = "fuzzy+" + fuzzyResults[i].MatchLayer
			}
			results = mergeRRFResults(results, fuzzyResults, limit)
		}
	}

	if len(results) > 0 {
		s.trackAccess(results)
	}
	return results, nil
}

const rrfK = 60 // standard RRF constant

// rrfSearch runs porter and trigram searches concurrently, fuses results
// using Reciprocal Rank Fusion, applies proximity reranking, and returns
// the top results.
func (s *ContentStore) rrfSearch(query string, limit int, opts SearchOptions) []SearchResult {
	fetchLimit := max(limit*2, 10)

	// Run both layers concurrently — SQLite WAL supports concurrent readers.
	var porterResults, trigramResults []SearchResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		porterResults = s.searchPorter(query, fetchLimit, opts)
	}()
	go func() {
		defer wg.Done()
		trigramResults = s.searchTrigramQuery(query, fetchLimit, opts)
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

	addLayer(porterResults, "porter+OR")
	addLayer(trigramResults, "trigram+OR")

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

	// Apply proximity reranking for multi-term queries.
	fused = proximityRerank(fused, query)

	if len(fused) > limit {
		fused = fused[:limit]
	}
	return fused
}

// mergeRRFResults deduplicates primary and secondary results by (sourceID, title).
// On conflict, the primary version is kept. Truncates to limit.
func mergeRRFResults(primary, secondary []SearchResult, limit int) []SearchResult {
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

	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// --- Proximity reranking ---

// proximityRerank boosts results where query terms appear close together.
// Only applies for multi-term queries (2+ words).
func proximityRerank(results []SearchResult, query string) []SearchResult {
	// Strip FTS5 special chars so terms match tokenized output (e.g. "error:" → "error").
	cleaned := ftsSpecialRe.ReplaceAllString(strings.ToLower(query), " ")
	words := strings.Fields(cleaned)
	if len(words) < 2 {
		return results
	}

	for i := range results {
		r := &results[i]
		minSpan := -1

		// Primary: use FTS5 highlight markers (char(2)/char(3)).
		if r.Highlighted != "" {
			minSpan = findMinSpanFromHighlights(r.Highlighted, words)
		}

		// Fallback: strings.Index on raw content.
		if minSpan < 0 {
			posLists := make([][]int, len(words))
			content := strings.ToLower(r.Content)
			allFound := true
			for j, w := range words {
				posLists[j] = findAllPositions(content, w)
				if len(posLists[j]) == 0 {
					allFound = false
					break
				}
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

	// Re-sort by boosted fused score.
	sort.Slice(results, func(i, j int) bool {
		return results[i].FusedScore > results[j].FusedScore
	})
	return results
}

// findMinSpanFromHighlights extracts match positions from FTS5 highlight
// markers (char(2) = start, char(3) = end) and finds the minimum window
// containing all query terms. Single-pass: tracks stripped byte offset
// incrementally to avoid repeated string allocations.
func findMinSpanFromHighlights(highlighted string, terms []string) int {
	posLists := make([][]int, len(terms))
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
		for i, term := range terms {
			if strings.Contains(matched, term) {
				posLists[i] = append(posLists[i], strippedPos)
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

// searchPorter searches the Porter-stemmed FTS5 table (always OR mode).
func (s *ContentStore) searchPorter(query string, limit int, opts SearchOptions) []SearchResult {
	sanitized := sanitizeQuery(query, "OR")
	if sanitized == "" {
		return nil
	}
	return s.execDynamicSearch("chunks", sanitized, limit, opts)
}

// searchTrigramQuery searches the trigram FTS5 table (always OR mode).
func (s *ContentStore) searchTrigramQuery(query string, limit int, opts SearchOptions) []SearchResult {
	sanitized := sanitizeTrigramQuery(query, "OR")
	if sanitized == "" {
		return nil
	}
	return s.execDynamicSearch("chunks_trigram", sanitized, limit, opts)
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

	query += " ORDER BY rank LIMIT ?"
	params = append(params, limit)

	rows, err := db.Query(query, params...)
	if err != nil {
		slog.Debug("search query failed", "error", err, "query", sanitized)
		return nil
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.Label, &r.Title, &r.Content, &r.SourceID, &r.ContentType, &r.Highlighted, &r.Rank); err != nil {
			continue
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		slog.Debug("search row iteration failed", "error", err)
		return nil
	}
	return results
}

// sanitizeQuery removes FTS5 special chars, filters keywords, quotes words.
func sanitizeQuery(query, mode string) string {
	cleaned := ftsSpecialRe.ReplaceAllString(query, " ")
	words := strings.Fields(cleaned)
	var filtered []string
	for _, w := range words {
		if w == "" || ftsKeywords[strings.ToUpper(w)] {
			continue
		}
		filtered = append(filtered, `"`+w+`"`)
	}
	if len(filtered) == 0 {
		return ""
	}
	sep := " "
	if mode == "OR" {
		sep = " OR "
	}
	return strings.Join(filtered, sep)
}

// sanitizeTrigramQuery cleans and filters for trigram search (min 3 chars per word).
func sanitizeTrigramQuery(query, mode string) string {
	cleaned := trigramCleanRe.ReplaceAllString(query, " ")
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) < 3 {
		return ""
	}
	words := strings.Fields(cleaned)
	var filtered []string
	for _, w := range words {
		if len(w) >= 3 {
			filtered = append(filtered, `"`+w+`"`)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	sep := " "
	if mode == "OR" {
		sep = " OR "
	}
	return strings.Join(filtered, sep)
}

// trackAccess updates last_accessed_at and access_count for sources
// that appeared in search results. Runs synchronously to avoid race
// conditions with ContentStore.Close() finalizing prepared statements.
func (s *ContentStore) trackAccess(results []SearchResult) {
	seen := make(map[int64]bool)
	for _, r := range results {
		if seen[r.SourceID] {
			continue
		}
		seen[r.SourceID] = true
		if _, err := s.stmtTrackAccess.Exec(r.SourceID); err != nil {
			slog.Debug("access tracking failed", "source_id", r.SourceID, "error", err)
		}
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
// Returns "" if no correction needed (exact match or no close candidate).
func (s *ContentStore) fuzzyCorrectWord(word string) string {
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
			return "" // Exact match — no correction needed.
		}
		dist := levenshteinDistance(word, candidate)
		if dist < bestDist {
			bestDist = dist
			bestWord = candidate
		}
	}
	if err := rows.Err(); err != nil {
		slog.Debug("fuzzy vocab iteration failed", "error", err)
		return "" // fall back to original word
	}

	if bestDist <= maxDist {
		return bestWord
	}
	return ""
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
	err := s.stmtGetSourceMeta.QueryRow(label).Scan(&meta.Label, &meta.ChunkCount, &indexedAt)
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
			&si.CodeChunkCount, &indexedAt, &lastAccessedAt, &si.AccessCount, &si.ContentHash); err != nil {
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
