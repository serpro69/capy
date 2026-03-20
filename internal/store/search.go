package store

import (
	"database/sql"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	ftsSpecialRe    = regexp.MustCompile(`['"(){}[\]*:^~]`)
	trigramCleanRe  = regexp.MustCompile(`[^a-zA-Z0-9 _-]`)
	ftsKeywords     = map[string]bool{"AND": true, "OR": true, "NOT": true, "NEAR": true}
)

// SearchWithFallback runs an 8-layer search, stopping at the first layer
// that returns results.
func (s *ContentStore) SearchWithFallback(query string, limit int, source string) ([]SearchResult, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	type layer struct {
		name string
		fn   func(q string) []SearchResult
	}

	layers := []layer{
		{"porter+AND", func(q string) []SearchResult { return s.searchPorter(q, limit, source, "AND") }},
		{"porter+OR", func(q string) []SearchResult { return s.searchPorter(q, limit, source, "OR") }},
		{"trigram+AND", func(q string) []SearchResult { return s.searchTrigramQuery(q, limit, source, "AND") }},
		{"trigram+OR", func(q string) []SearchResult { return s.searchTrigramQuery(q, limit, source, "OR") }},
	}

	// Layers 1-4: direct search.
	for _, l := range layers {
		if results := l.fn(query); len(results) > 0 {
			tagResults(results, l.name)
			s.trackAccessAsync(results)
			return results, nil
		}
	}

	// Layers 5-8: fuzzy correction, then re-run all 4 layers.
	corrected := s.fuzzyCorrectQuery(query)
	if corrected != "" && corrected != query {
		for _, l := range layers {
			if results := l.fn(corrected); len(results) > 0 {
				tagResults(results, "fuzzy+"+l.name)
				s.trackAccessAsync(results)
				return results, nil
			}
		}
	}

	return nil, nil
}

func tagResults(results []SearchResult, layer string) {
	for i := range results {
		results[i].MatchLayer = layer
	}
}

// searchPorter searches the Porter-stemmed FTS5 table.
func (s *ContentStore) searchPorter(query string, limit int, source, mode string) []SearchResult {
	sanitized := sanitizeQuery(query, mode)
	if sanitized == "" {
		return nil
	}
	return s.execSearch(s.stmtSearchPorter, s.stmtSearchPorterFiltered, sanitized, limit, source)
}

// searchTrigramQuery searches the trigram FTS5 table.
func (s *ContentStore) searchTrigramQuery(query string, limit int, source, mode string) []SearchResult {
	sanitized := sanitizeTrigramQuery(query, mode)
	if sanitized == "" {
		return nil
	}
	return s.execSearch(s.stmtSearchTrigram, s.stmtSearchTrigramFiltered, sanitized, limit, source)
}

func (s *ContentStore) execSearch(stmt, stmtFiltered *sql.Stmt, sanitized string, limit int, source string) []SearchResult {
	var rows *sql.Rows
	var err error

	if source != "" {
		rows, err = stmtFiltered.Query(sanitized, source, limit)
	} else {
		rows, err = stmt.Query(sanitized, limit)
	}
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

// trackAccessAsync updates last_accessed_at and access_count for sources
// that appeared in search results. Runs in a background goroutine.
func (s *ContentStore) trackAccessAsync(results []SearchResult) {
	seen := make(map[int64]bool)
	var sourceIDs []int64
	for _, r := range results {
		if !seen[r.SourceID] {
			seen[r.SourceID] = true
			sourceIDs = append(sourceIDs, r.SourceID)
		}
	}
	if len(sourceIDs) == 0 {
		return
	}
	go func() {
		for _, id := range sourceIDs {
			if _, err := s.stmtTrackAccess.Exec(id); err != nil {
				slog.Debug("access tracking failed", "source_id", id, "error", err)
			}
		}
	}()
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
	return results, nil
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
	return sources, nil
}
