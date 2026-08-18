package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/serpro69/capy/internal/retrieval"
	"github.com/serpro69/capy/internal/sanitize"
)

// SearchWithFallback runs the shared retrieval engine (Reciprocal Rank Fusion
// across porter and trigram layers, with fuzzy correction as a secondary pass
// when results are sparse) over the knowledge corpus, wrapped by the
// knowledge-only steps: stale-source refresh before and access tracking after.
func (s *ContentStore) SearchWithFallback(query string, limit int, opts SearchOptions) ([]SearchResult, error) {
	if _, err := s.getDB(); err != nil {
		return nil, err
	}

	// Auto-refresh file-backed sources whose on-disk content changed since
	// indexing, so RRF and all downstream passes operate on fresh data. The
	// call is internally throttled (staleRefreshCooldown) to avoid stat storms
	// on rapid query bursts.
	s.refreshStaleSources()

	corpus, err := s.searchCorpus(opts)
	if err != nil {
		return nil, err
	}
	results := retrieval.SearchWithFallback(s.ctx(), corpus, query, limit, opts.MaxPerSource)

	if len(results) > 0 {
		s.trackAccess(results)
	}
	return results, nil
}

// searchCorpus adapts the knowledge store to the shared retrieval engine.
// The db handle, row mapping, and knowledge-only filters (source label,
// content type, source kinds — including opts.IncludeKinds) stay on the store
// side, closed over by the Exec callback via execDynamicSearch. The
// vocabulary-backed fuzzy corrector is the optional corpus capability that
// corpora without a vocabulary table leave nil.
func (s *ContentStore) searchCorpus(opts SearchOptions) (retrieval.Corpus, error) {
	return retrieval.NewCorpus("chunks", "chunks_trigram",
		func(ctx context.Context, table, ftsQuery string, limit int) []retrieval.SearchResult {
			return s.execDynamicSearch(ctx, table, ftsQuery, limit, opts)
		},
		s.fuzzyCorrectWord,
	)
}

// staleRefreshCooldown throttles refreshStaleSources. Each search is an IPC
// round-trip and every file-backed source costs a stat (and possibly a read),
// so a short cooldown keeps rapid query bursts from triggering redundant stat
// storms while still catching genuinely changed files within a few searches.
const staleRefreshCooldown = 5 * time.Second

// SetDenyChecker installs the Read deny-policy predicate used by stale
// auto-refresh. The predicate returns true when a path is denied. It is
// consulted immediately before re-reading a file so a deny-policy change since
// the original index (TOCTOU) is honored. Passing nil disables deny filtering
// (the default for standalone store usage outside the server).
func (s *ContentStore) SetDenyChecker(fn func(string) bool) {
	s.denyChecker = fn
}

// refreshStaleSources scans file-backed sources, detects on-disk content
// changes via an mtime gate + SHA-256 comparison, and re-indexes changed files
// so subsequent search passes see fresh data. It returns the number of sources
// re-indexed.
//
// Throttling: the first caller within each staleRefreshCooldown window claims
// the window via an atomic CAS and does the work; concurrent/rapid callers
// return 0 immediately. Failures (missing files, read errors, denied paths) are
// skipped, never fatal — stale detection must never break search.
func (s *ContentStore) refreshStaleSources() int {
	now := time.Now().UnixNano()
	last := s.lastRefreshTime.Load()
	if now-last < int64(staleRefreshCooldown) {
		return 0
	}
	// Claim the window. If another goroutine claimed it first, defer to it.
	if !s.lastRefreshTime.CompareAndSwap(last, now) {
		return 0
	}

	if _, err := s.getDB(); err != nil {
		return 0
	}

	// Snapshot the file-backed sources up front so the cursor is closed before
	// any re-index opens its own write transaction (avoids holding a read
	// cursor across writes).
	type fileSource struct {
		id                                                   int64
		label, filePath, contentHash, indexedAt, contentType string
		kind                                                 SourceKind
	}
	rows, err := s.stmtListFileBackedSources.Query()
	if err != nil {
		slog.Warn("stale refresh: listing file-backed sources failed", "error", err)
		return 0
	}
	var sources []fileSource
	for rows.Next() {
		var fs fileSource
		var filePath sql.NullString
		if err := rows.Scan(&fs.id, &fs.label, &filePath, &fs.contentHash, &fs.indexedAt, &fs.contentType, &fs.kind); err != nil {
			slog.Warn("stale refresh: row scan failed, skipping", "error", err)
			continue
		}
		if !filePath.Valid || filePath.String == "" {
			continue
		}
		fs.filePath = filePath.String
		sources = append(sources, fs)
	}
	// Close the cursor before any re-index opens a write tx — a deferred close
	// would hold the read cursor across writes.
	rows.Close()
	if err := rows.Err(); err != nil {
		slog.Warn("stale refresh: row iteration failed", "error", err)
		return 0
	}

	refreshed := 0
	for _, fs := range sources {
		// TOCTOU defense: a path's deny status may have changed since indexing.
		// Fail closed — skip the re-read if denied.
		if s.denyChecker != nil && s.denyChecker(fs.filePath) {
			continue
		}

		// SQLite CURRENT_TIMESTAMP is UTC and time.Parse with no zone returns
		// UTC, so the mtime comparison below converts mtime to UTC too.
		indexedAt, perr := time.Parse("2006-01-02 15:04:05", fs.indexedAt)
		if perr != nil {
			slog.Warn("stale refresh: unparseable indexed_at, skipping", "label", fs.label, "value", fs.indexedAt)
			continue
		}

		changed, sanitized, current := s.fileChangedSince(fs.filePath, fs.contentHash, indexedAt)
		switch {
		case changed:
			// Reuse the stored content_type rather than re-detecting: file-backed
			// sources are first indexed via handleIndex with contentType "", so the
			// stored value is already whatever DetectContentType chose for this file
			// at first index — preserving it keeps chunking stable across refreshes.
			if _, err := s.IndexWithFilePath(sanitized, fs.label, fs.contentType, fs.kind, fs.filePath); err != nil {
				slog.Warn("stale refresh: re-index failed", "path", fs.filePath, "error", err)
				continue
			}
			refreshed++
		case current:
			// Touched on disk but content unchanged: advance indexed_at so the
			// mtime fast-path skips this file on subsequent searches instead of
			// re-reading and re-hashing it every time the gate stays open.
			if _, err := s.stmtUpdateSourceIndexedAt.Exec(fs.id); err != nil {
				slog.Warn("stale refresh: advancing indexed_at failed", "label", fs.label, "error", err)
			}
		}
	}
	return refreshed
}

// fileChangedSince inspects the file at path against the stored sanitized
// content hash. It returns:
//   - changed: the file's sanitized content differs — re-index with sanitized.
//   - sanitized: the freshly-sanitized content (only when changed is true).
//   - current: the file was read and its hash matched — content is confirmed
//     unchanged, so the caller should advance indexed_at to close the mtime gate.
//
// When the file is skipped without reading (deleted, non-regular, oversized, or
// the mtime gate says unchanged), all three are zero values.
//
// It binds stat and read to a single descriptor (fd-bound: Open → Stat →
// ReadAll) so the file cannot be swapped mid-check, and rejects non-regular
// files (FIFO/device/socket/dir). O_NONBLOCK keeps the open from blocking on a
// FIFO before the IsRegular guard can reject it (a no-op for regular files);
// IsRegular is what rejects char/block devices and sockets before ReadAll.
//
// The hash is compared against the sanitized byte stream because capy stores
// content_hash post-StripSecrets (index.go) — comparing raw file bytes would
// mismatch forever for any secret-bearing file, causing perpetual re-indexing.
func (s *ContentStore) fileChangedSince(path, storedHash string, indexedAt time.Time) (changed bool, sanitized string, current bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		// Deleted or unreadable — keep cached results.
		return false, "", false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false, "", false
	}

	// mtime gate (fast path): unchanged files never get read or hashed.
	if !info.ModTime().UTC().After(indexedAt) {
		return false, "", false
	}

	// Size guard mirrors handleIndex: a file that grew past the limit after
	// indexing must not be slurped into memory (OOM risk) only for the
	// subsequent re-index to reject it with SourceTooLargeError. Skip without
	// reading — the next IndexWithFilePath would fail on it anyway.
	if info.Size() > int64(s.maxSourceBytes) {
		slog.Warn("stale refresh: file exceeds max source size, skipping", "path", path, "size", info.Size(), "limit", s.maxSourceBytes)
		return false, "", false
	}

	raw, err := io.ReadAll(f)
	if err != nil {
		slog.Warn("stale refresh: read failed", "path", path, "error", err)
		return false, "", false
	}

	sanitized = sanitize.StripSecrets(string(raw))
	if contentHash(sanitized) == storedHash {
		// Touched but content unchanged after sanitization.
		return false, "", true
	}
	return true, sanitized, false
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

// execDynamicSearch builds and executes a search query with dynamic WHERE clauses.
// table must be "chunks" or "chunks_trigram" (hardcoded by callers, never from user input).
func (s *ContentStore) execDynamicSearch(ctx context.Context, table, sanitized string, limit int, opts SearchOptions) []SearchResult {
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

	rows, err := db.QueryContext(ctx, query, params...)
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
