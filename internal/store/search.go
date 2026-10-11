package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/serpro69/capy/internal/retrieval"
)

// SearchWithFallback runs the shared retrieval engine (Reciprocal Rank Fusion
// across porter and trigram layers, with fuzzy correction as a secondary pass
// when results are sparse) over the knowledge corpus, wrapped by the
// knowledge-only steps: stale-source refresh before and access tracking after.
func (s *ContentStore) SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error) {
	return s.SearchWithFallbackContext(s.ctx(), query, limit, opts)
}

// SearchWithFallbackContext propagates cancellation through bounded refresh and
// retrieval. The contextless entry point remains for existing library callers.
func (s *ContentStore) SearchWithFallbackContext(ctx context.Context, query string, limit int, opts SearchOptions) ([]SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	// Auto-refresh file-backed sources whose on-disk content changed since
	// indexing, so RRF and all downstream passes operate on fresh data. The
	// call is internally throttled (staleRefreshCooldown) to avoid stat storms
	// on rapid query bursts.
	s.refreshStaleSourcesContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var queryErr error
	var firstError sync.Once
	var onError func(error)
	if opts.FailOnError {
		onError = func(err error) { firstError.Do(func() { queryErr = err }) }
	}
	corpus, err := s.searchCorpusWithErrorHandler(opts, onError)
	if err != nil {
		return nil, err
	}
	results := retrieval.SearchWithFallback(ctx, corpus, query, limit, opts.MaxPerSource)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Retrieval has joined every layer worker before returning. Strict callers
	// cannot mistake an unavailable layer or skipped row for a complete search.
	if queryErr != nil {
		return nil, queryErr
	}

	if len(results) > 0 {
		s.trackAccess(results)
	}
	return results, nil
}

// searchCorpus adapts the knowledge store to the shared retrieval engine as a
// Corpus over chunks/chunks_trigram, supplying the corpus pieces the engine's
// query skeleton needs: the db handle, the knowledge SELECT/JOIN shape, the
// pre-bound knowledge-only filters (source label, content type, source kinds
// — including opts.IncludeKinds), and the knowledge row mapper. The
// vocabulary-backed fuzzy corrector is the optional corpus capability that
// corpora without a vocabulary table leave nil.
func (s *ContentStore) searchCorpus(opts SearchOptions) (retrieval.Corpus, error) {
	return s.searchCorpusWithErrorHandler(opts, nil)
}

func (s *ContentStore) searchCorpusWithErrorHandler(opts SearchOptions, onError func(error)) (retrieval.Corpus, error) {
	db, err := s.getDB()
	if err != nil {
		return retrieval.Corpus{}, err
	}
	filterSQL, filterParams := knowledgeFilterClauses(opts)
	return retrieval.NewCorpus(retrieval.CorpusConfig{
		DB:            db,
		PorterTable:   "chunks",
		TrigramTable:  "chunks_trigram",
		SelectColumns: "s.label, c.title, c.content, c.source_id, c.content_type",
		Join:          "JOIN sources s ON s.id = c.source_id",
		TitleWeight:   s.titleWeight,
		FilterSQL:     filterSQL,
		FilterParams:  filterParams,
		MapRow:        scanSearchRow,
		Fuzzy:         s.fuzzyCorrectWord,
		OnError:       onError,
	})
}

// knowledgeFilterClauses builds the knowledge-only WHERE additions for one
// search: source-label match (exact or substring), content type, and the
// effective source-kind filter. Kind filtering is a knowledge concern and
// deliberately does not move into the retrieval package (review #R4).
func knowledgeFilterClauses(opts SearchOptions) (string, []any) {
	var clauses strings.Builder
	var params []any

	if opts.Source != "" {
		if opts.SourceMatchMode == "exact" {
			clauses.WriteString(" AND s.label = ?")
			params = append(params, opts.Source)
		} else {
			// Source selectors are literal substrings. Escape backslashes first
			// so the escapes added for LIKE wildcards are not escaped again.
			source := strings.ReplaceAll(opts.Source, `\`, `\\`)
			source = strings.ReplaceAll(source, `%`, `\%`)
			source = strings.ReplaceAll(source, `_`, `\_`)
			clauses.WriteString(` AND s.label LIKE ? ESCAPE '\'`)
			params = append(params, "%"+source+"%")
		}
	}
	if opts.ContentType != "" {
		clauses.WriteString(" AND c.content_type = ?")
		params = append(params, opts.ContentType)
	}
	if kinds := effectiveKindFilter(opts); kinds != nil {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
		clauses.WriteString(" AND s.kind IN (" + placeholders + ")")
		for _, k := range kinds {
			params = append(params, string(k))
		}
	}
	return clauses.String(), params
}

// scanSearchRow is the knowledge corpus's retrieval.RowMapper: it scans the
// SELECT list built by searchCorpus plus the skeleton's highlighted/rank
// columns.
func scanSearchRow(rows *sql.Rows) (retrieval.SearchResult, error) {
	var r retrieval.SearchResult
	err := rows.Scan(&r.Label, &r.Title, &r.Content, &r.SourceID, &r.ContentType, &r.Highlighted, &r.Rank)
	return r, err
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
//   - empty IncludeKinds: {KindDurable, KindSession} (default-exclude ephemeral)
//   - non-empty IncludeKinds: opts.IncludeKinds verbatim
//
// Returning nil means "no kind clause." Returning a slice means "filter to these kinds."
func effectiveKindFilter(opts SearchOptions) []SourceKind {
	if opts.Source != "" {
		return nil
	}
	if len(opts.IncludeKinds) == 0 {
		return []SourceKind{KindDurable, KindSession}
	}
	return opts.IncludeKinds
}

// KindScopeIncludes reports whether a search with the given options would
// include sources of the given kind, mirroring effectiveKindFilter so callers
// never drift from the store's actual filtering rule.
func KindScopeIncludes(opts SearchOptions, kind SourceKind) bool {
	kinds := effectiveKindFilter(opts)
	if kinds == nil {
		return true // explicit Source override — kind filter is bypassed
	}
	return slices.Contains(kinds, kind)
}

// KindScopeIncludesEphemeral is a convenience wrapper for backward compatibility.
func KindScopeIncludesEphemeral(opts SearchOptions) bool {
	return KindScopeIncludes(opts, KindEphemeral)
}

// trackAccess updates last_accessed_at and access_count for sources
// that appeared in search results. Runs synchronously to avoid race
// conditions with ContentStore.Close() finalizing prepared statements.
//
// Updates run inside a single transaction so the loop produces one fsync
// instead of one per source — the 5× fetch multiplier in RRFSearch pushes
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

// fuzzyCorrectWord finds the closest vocabulary word within edit distance.
// It is the knowledge store's retrieval.FuzzyCorrector — backed by the
// per-corpus vocabulary table, which corpora like the vault don't have.
// Returns "" if no correction needed (exact match, synonym-known, or no close candidate).
func (s *ContentStore) fuzzyCorrectWord(word string) string {
	// Synonym-known terms don't need fuzzy correction — they're expanded at query time.
	if retrieval.HasSynonym(word) {
		return ""
	}

	s.fuzzyCacheMu.RLock()
	if cached, ok := s.fuzzyCache[word]; ok {
		s.fuzzyCacheMu.RUnlock()
		if cached == nil {
			return ""
		}
		return *cached
	}
	s.fuzzyCacheMu.RUnlock()

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
			s.cacheFuzzy(word, nil)
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
		return "" // intentionally not cached — DB errors are transient
	}

	if bestDist <= maxDist {
		s.cacheFuzzy(word, &bestWord)
		return bestWord
	}
	s.cacheFuzzy(word, nil)
	return ""
}

func (s *ContentStore) cacheFuzzy(word string, result *string) {
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
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].word < candidates[j].word
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
		if len(w) < 3 || retrieval.IsStopword(w) || seen[w] {
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
