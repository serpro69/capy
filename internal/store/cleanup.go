package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
)

const (
	lambdaDecay = 0.045 // temporal decay rate
	sigmaAccess = 0.3   // weight of recency boost

	hotThreshold  = 0.7
	warmThreshold = 0.4
	coldThreshold = 0.15
)

// retentionScore computes a continuous retention score for a source.
// Formula: salience × exp(-λ × daysSinceIndexed) + σ × recencyBoost
// where salience = baseSalience(contentType) + frequencyBonus(accessCount)
func retentionScore(src SourceInfo, now time.Time) float64 {
	// Base salience by content type.
	var salience float64
	switch {
	case src.CodeChunkCount > 0 && src.CodeChunkCount == src.ChunkCount:
		salience = 0.7 // pure code
	case src.CodeChunkCount > 0:
		salience = 0.6 // mixed
	default:
		salience = 0.5 // prose
	}

	// Frequency bonus: min(0.2, accessCount × 0.02) — folds into salience,
	// so it decays with age just like the base content-type weight.
	frequencyBonus := math.Min(0.2, float64(src.AccessCount)*0.02)
	salience += frequencyBonus

	// Temporal decay.
	daysSinceIndexed := now.Sub(src.IndexedAt).Hours() / 24
	if daysSinceIndexed < 0 {
		daysSinceIndexed = 0
	}
	temporalDecay := math.Exp(-lambdaDecay * daysSinceIndexed)

	// Recency boost: 1 / (1 + daysSinceLastAccess), or 0 if never accessed.
	// Guarded by AccessCount > 0 because the schema sets last_accessed_at to
	// CURRENT_TIMESTAMP on insert — IsZero() alone would always be true.
	var recencyBoost float64
	if src.AccessCount > 0 && !src.LastAccessedAt.IsZero() {
		daysSinceLastAccess := now.Sub(src.LastAccessedAt).Hours() / 24
		if daysSinceLastAccess < 0 {
			daysSinceLastAccess = 0
		}
		recencyBoost = 1.0 / (1.0 + daysSinceLastAccess)
	}

	return salience*temporalDecay + sigmaAccess*recencyBoost
}

// classifyTier maps a SourceInfo to a tier string based on retention score.
func classifyTier(src SourceInfo, now time.Time) (string, float64) {
	score := retentionScore(src, now)
	switch {
	case score >= hotThreshold:
		return "hot", score
	case score >= warmThreshold:
		return "warm", score
	case score >= coldThreshold:
		return "cold", score
	default:
		return "evictable", score
	}
}

// ClassifySources returns all sources with tier classification.
//
// Durable rows get retention-score-based tiers (hot/warm/cold/evictable,
// see classifyTier). Ephemeral and session rows are bucketed by age
// against their respective TTLs: "fresh" when indexed within the TTL
// window, "stale" when past it and awaiting the next Cleanup() sweep.
// RetentionScore is left at 0 for TTL-based rows — retention math is
// meaningless for TTL-lived content. Non-positive TTLs fall back to safe
// defaults (24h for ephemeral, 60 days for session).
func (s *ContentStore) ClassifySources(ephemeralTTL, sessionTTL time.Duration) ([]SourceInfo, error) {
	sources, err := s.ListSources()
	if err != nil {
		return nil, err
	}

	if ephemeralTTL <= 0 {
		ephemeralTTL = 24 * time.Hour
	}
	if sessionTTL <= 0 {
		sessionTTL = 60 * 24 * time.Hour
	}
	now := time.Now()
	ephCutoff := now.Add(-ephemeralTTL)
	sessCutoff := now.Add(-sessionTTL)

	for i := range sources {
		switch sources[i].Kind {
		case KindEphemeral:
			if sources[i].IndexedAt.Before(ephCutoff) {
				sources[i].Tier = "stale"
			} else {
				sources[i].Tier = "fresh"
			}
		case KindSession:
			if sources[i].IndexedAt.Before(sessCutoff) {
				sources[i].Tier = "stale"
			} else {
				sources[i].Tier = "fresh"
			}
		default:
			sources[i].Tier, sources[i].RetentionScore = classifyTier(sources[i], now)
		}
	}
	return sources, nil
}

// Cleanup removes oversized or expired ephemeral and legacy session sources.
// Durable knowledge requires exact-label eviction or an explicit full reset.
// Oversize takes precedence when a source also exceeds its TTL.
func (s *ContentStore) Cleanup(dryRun bool, ephemeralTTL, sessionTTL time.Duration) ([]SourceInfo, error) {
	now := time.Now()
	removed, err := s.cleanupMatching(dryRun, `
		WITH oversized AS (
			SELECT s.id, SUM(LENGTH(c.content)) AS content_bytes
			FROM sources s JOIN chunks c ON CAST(c.source_id AS INTEGER) = s.id
			WHERE s.kind IN ('ephemeral', 'session')
			GROUP BY s.id HAVING content_bytes > ?
		)
		SELECT `+cleanupColumns+`, CASE WHEN o.id IS NULL THEN 'ttl' ELSE 'oversized' END
		FROM sources s LEFT JOIN oversized o ON o.id = s.id
		WHERE o.id IS NOT NULL OR (s.kind = 'ephemeral' AND s.indexed_at < ?)
			OR (s.kind = 'session' AND s.indexed_at < ?)
		ORDER BY o.content_bytes DESC, s.kind, s.id DESC`, s.maxSourceBytes,
		expiryCutoff(now, KindEphemeral, ephemeralTTL), expiryCutoff(now, KindSession, sessionTTL))
	if err != nil {
		return nil, err
	}

	// Auto-vacuum when freelist exceeds 20% of total pages after a non-dry-run
	// eviction. This reclaims dead pages left by deleted rows.
	if !dryRun && len(removed) > 0 {
		if ratio := s.FreelistRatio(); ratio > 0.20 {
			slog.Info("auto-vacuum triggered", "freelist_ratio", fmt.Sprintf("%.1f%%", ratio*100))
			if err := s.Vacuum(); err != nil {
				slog.Warn("auto-vacuum failed", "error", err)
			}
		}
	}

	return removed, nil
}

// PurgeCounts reports how many rows a PurgeAll removed (or, in dry-run mode,
// would remove) from the knowledge base.
type PurgeCounts struct {
	Sources int64
	Chunks  int64
	Vocab   int64
}

// PurgeAll performs a full project-scope reset of the knowledge base: it
// deletes ALL rows from sources, chunks, chunks_trigram, and vocabulary,
// clears the in-memory fuzzy-correction cache, and VACUUMs to reclaim disk
// space. Unlike Cleanup, this is unconditional — retention score, TTL, and
// access count are all ignored.
//
// Vocabulary is normally preserved across cleanups (it aids fuzzy correction),
// but a full reset must leave the store genuinely empty, so it is purged too.
// The fuzzy cache is cleared after the delete commits because it holds
// corrections pointing at vocabulary words that no longer exist; a plain DELETE
// does not trigger the cache invalidation that vocabulary insertion does.
//
// When dryRun is true, PurgeAll reports the row counts that would be removed
// without modifying the database.
func (s *ContentStore) PurgeAll(dryRun bool) (PurgeCounts, error) {
	db, err := s.getDB()
	if err != nil {
		return PurgeCounts{}, err
	}

	var counts PurgeCounts
	if err := db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&counts.Sources); err != nil {
		return PurgeCounts{}, fmt.Errorf("counting sources: %w", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&counts.Chunks); err != nil {
		return PurgeCounts{}, fmt.Errorf("counting chunks: %w", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&counts.Vocab); err != nil {
		return PurgeCounts{}, fmt.Errorf("counting vocabulary: %w", err)
	}

	if dryRun {
		return counts, nil
	}

	// Wrap the four deletes in a single immediate transaction so the reset is
	// atomic and gets the same retry-on-BUSY backoff as the eviction sweep.
	tx, err := sqliteutil.BeginImmediate(db, "sources")
	if err != nil {
		return PurgeCounts{}, fmt.Errorf("beginning purge transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	for _, stmt := range []string{
		"DELETE FROM chunks",
		"DELETE FROM chunks_trigram",
		"DELETE FROM sources",
		"DELETE FROM vocabulary",
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return PurgeCounts{}, fmt.Errorf("purge %q: %w", stmt, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return PurgeCounts{}, fmt.Errorf("committing purge: %w", err)
	}

	// Drop stale fuzzy corrections that now point at deleted vocabulary.
	s.fuzzyCacheMu.Lock()
	s.fuzzyCache = make(map[string]*string)
	s.fuzzyCacheMu.Unlock()

	// The reset has already committed — the tables are empty and the cache is
	// cleared. VACUUM only reclaims disk space, so a failure here (e.g.
	// SQLITE_BUSY under WAL contention) must NOT be propagated: doing so would
	// make handleCleanup discard the counts and report a total failure to the
	// caller even though the purge succeeded. Log and continue, mirroring the
	// auto-vacuum path in Cleanup.
	if err := s.Vacuum(); err != nil {
		slog.Warn("vacuum after purge_all failed", "error", err)
	}

	return counts, nil
}

// PurgeEphemeral evicts ephemeral sources past the TTL window.
func (s *ContentStore) PurgeEphemeral(dryRun bool, ttl time.Duration) ([]SourceInfo, error) {
	return s.cleanupByTTL(KindEphemeral, dryRun, ttl)
}

// PurgeSession evicts session sources past the TTL window. Mirrors
// PurgeEphemeral for the session kind.
func (s *ContentStore) PurgeSession(dryRun bool, ttl time.Duration) ([]SourceInfo, error) {
	return s.cleanupByTTL(KindSession, dryRun, ttl)
}

const cleanupColumns = `s.id, s.label, s.content_type, s.chunk_count, s.code_chunk_count,
	COALESCE(s.indexed_at, ''), COALESCE(s.last_accessed_at, ''), s.access_count, COALESCE(s.content_hash, ''), s.kind`

func (s *ContentStore) cleanupByTTL(kind SourceKind, dryRun bool, ttl time.Duration) ([]SourceInfo, error) {
	return s.cleanupMatching(dryRun, `SELECT `+cleanupColumns+`, 'ttl' FROM sources s
		WHERE s.kind = ? AND s.indexed_at < ? ORDER BY s.id DESC`, kind, expiryCutoff(time.Now(), kind, ttl))
}

func expiryCutoff(now time.Time, kind SourceKind, ttl time.Duration) string {
	if ttl <= 0 {
		switch kind {
		case KindSession:
			ttl = 60 * 24 * time.Hour
		default:
			ttl = 24 * time.Hour
		}
		slog.Warn("cleanupByTTL: non-positive TTL supplied, using default",
			"kind", kind, "ttl", ttl)
	}
	return now.Add(-ttl).UTC().Format(time.DateTime)
}

func (s *ContentStore) cleanupMatching(dryRun bool, query string, args ...any) ([]SourceInfo, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("selecting cleanup candidates: %w", err)
	}
	defer rows.Close()

	var candidates []SourceInfo
	for rows.Next() {
		var si SourceInfo
		var indexedAt, lastAccessedAt string
		if err := rows.Scan(&si.ID, &si.Label, &si.ContentType, &si.ChunkCount,
			&si.CodeChunkCount, &indexedAt, &lastAccessedAt, &si.AccessCount,
			&si.ContentHash, &si.Kind, &si.EvictionReason); err != nil {
			return nil, fmt.Errorf("reading cleanup candidate: %w", err)
		}
		si.IndexedAt, _ = time.Parse("2006-01-02 15:04:05", indexedAt)
		si.LastAccessedAt, _ = time.Parse("2006-01-02 15:04:05", lastAccessedAt)
		candidates = append(candidates, si)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return s.evict(candidates, dryRun)
}

// evict rechecks the selected kind under the writer lock. Same-content
// indexing can promote a source to durable while retaining its ID.
func (s *ContentStore) evict(candidates []SourceInfo, dryRun bool) ([]SourceInfo, error) {
	if dryRun || len(candidates) == 0 {
		return candidates, nil
	}

	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	// Use sqliteutil.BeginImmediate so the eviction sweep gets the same
	// retry-on-BUSY backoff as the migration and Index paths; a plain db.Begin()
	// surfaces SQLITE_BUSY mid-transaction under write contention.
	tx, err := sqliteutil.BeginImmediate(db, "sources")
	if err != nil {
		return nil, fmt.Errorf("beginning cleanup transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// Bind the three prepared statements to the transaction once. Calling
	// tx.Stmt inside the loop would allocate a fresh transaction-bound
	// statement per candidate, which compounds on large eviction sweeps.
	delChunks := tx.Stmt(s.stmtDeleteChunksBySource)
	delTrigram := tx.Stmt(s.stmtDeleteTrigramBySource)
	delSource, err := tx.Prepare("DELETE FROM sources WHERE id = ? AND kind = ? AND kind IN ('ephemeral','session')")
	if err != nil {
		return nil, fmt.Errorf("preparing source cleanup: %w", err)
	}

	removed := candidates[:0]
	for _, src := range candidates {
		res, err := delSource.Exec(src.ID, src.Kind)
		if err != nil {
			return nil, fmt.Errorf("deleting source %d: %w", src.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		if _, err := delChunks.Exec(src.ID); err != nil {
			return nil, fmt.Errorf("deleting chunks for source %d: %w", src.ID, err)
		}
		if _, err := delTrigram.Exec(src.ID); err != nil {
			return nil, fmt.Errorf("deleting trigram chunks for source %d: %w", src.ID, err)
		}
		removed = append(removed, src)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing cleanup: %w", err)
	}
	return removed, nil
}

// EvictByLabel force-evicts a source by its exact label, regardless of
// retention score, TTL, or access count. Returns the evicted SourceInfo
// or an error if the label is not found.
func (s *ContentStore) EvictByLabel(label string, dryRun bool) (*SourceInfo, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	if dryRun {
		return sourceByExactLabel(db.QueryRow, label)
	}
	tx, err := sqliteutil.BeginImmediate(db, "sources")
	if err != nil {
		return nil, fmt.Errorf("beginning exact-label eviction: %w", err)
	}
	defer tx.Rollback()
	target, err := sourceByExactLabel(tx.QueryRow, label)
	if err != nil {
		return nil, err
	}
	if err := s.deleteSourceTx(tx, target.ID); err != nil {
		return nil, fmt.Errorf("deleting source %d: %w", target.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing exact-label eviction: %w", err)
	}
	return target, nil
}

func sourceByExactLabel(queryRow func(string, ...any) *sql.Row, label string) (*SourceInfo, error) {
	var si SourceInfo
	var indexedAt, lastAccessedAt string
	err := queryRow("SELECT "+cleanupColumns+" FROM sources s WHERE s.label = ?", label).Scan(
		&si.ID, &si.Label, &si.ContentType, &si.ChunkCount,
		&si.CodeChunkCount, &indexedAt, &lastAccessedAt, &si.AccessCount,
		&si.ContentHash, &si.Kind)
	if err != nil {
		return nil, fmt.Errorf("source not found: %q", label)
	}
	si.IndexedAt, _ = time.Parse("2006-01-02 15:04:05", indexedAt)
	si.LastAccessedAt, _ = time.Parse("2006-01-02 15:04:05", lastAccessedAt)
	si.EvictionReason = "manual"

	return &si, nil
}

// Stats returns knowledge base statistics. ephemeralTTL is required to
// bucket ephemeral sources into fresh/stale — callers should pass the
// same TTL they use for Cleanup (typically
// `config.Store.Cleanup.EphemeralTTLHours`).
func (s *ContentStore) Stats(ephemeralTTL, sessionTTL time.Duration) (*StoreStats, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	var stats StoreStats

	if err := db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&stats.SourceCount); err != nil {
		slog.Warn("failed to count sources", "error", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&stats.ChunkCount); err != nil {
		slog.Warn("failed to count chunks", "error", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM vocabulary").Scan(&stats.VocabCount); err != nil {
		slog.Warn("failed to count vocabulary", "error", err)
	}

	// DB file size.
	if fi, err := os.Stat(s.dbPath); err == nil {
		stats.DBSizeBytes = fi.Size()
	}

	// Per-kind counts and tier distribution.
	sources, err := s.ClassifySources(ephemeralTTL, sessionTTL)
	if err != nil {
		return &stats, nil
	}
	for _, src := range sources {
		switch src.Kind {
		case KindEphemeral:
			stats.EphemeralSourceCount++
			switch src.Tier {
			case "fresh":
				stats.EphemeralFreshCount++
			case "stale":
				stats.EphemeralStaleCount++
			}
		case KindSession:
			stats.SessionSourceCount++
			switch src.Tier {
			case "fresh":
				stats.SessionFreshCount++
			case "stale":
				stats.SessionStaleCount++
			}
		default:
			stats.DurableSourceCount++
			switch src.Tier {
			case "hot":
				stats.DurableHotCount++
			case "warm":
				stats.DurableWarmCount++
			case "cold":
				stats.DurableColdCount++
			case "evictable":
				stats.DurableEvictableCount++
			}
		}
	}

	return &stats, nil
}
