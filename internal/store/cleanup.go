package store

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"time"
)

const (
	lambdaDecay = 0.045 // temporal decay rate
	sigmaAccess = 0.3   // weight of recency boost

	hotThreshold       = 0.7
	warmThreshold      = 0.4
	coldThreshold      = 0.15
	evictableThreshold = coldThreshold // sources below coldThreshold are evictable
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
// see classifyTier). Ephemeral rows are bucketed by age against
// ephemeralTTL: "fresh" when indexed within the TTL window, "stale" when
// past it and awaiting the next Cleanup() sweep. RetentionScore is left
// at 0 for ephemeral rows — retention math is meaningless for TTL-lived
// content. Non-positive ephemeralTTL falls back to the safe 24h default,
// matching cleanupEphemeral.
func (s *ContentStore) ClassifySources(ephemeralTTL time.Duration) ([]SourceInfo, error) {
	sources, err := s.ListSources()
	if err != nil {
		return nil, err
	}

	if ephemeralTTL <= 0 {
		ephemeralTTL = 24 * time.Hour
	}
	now := time.Now()
	ttlCutoff := now.Add(-ephemeralTTL)

	for i := range sources {
		if sources[i].Kind == KindEphemeral {
			if sources[i].IndexedAt.Before(ttlCutoff) {
				sources[i].Tier = "stale"
			} else {
				sources[i].Tier = "fresh"
			}
			continue
		}
		sources[i].Tier, sources[i].RetentionScore = classifyTier(sources[i], now)
	}
	return sources, nil
}

// Cleanup removes sources via two independent paths:
//   - durable: retention-score-based eviction (ADR-011) for `kind = 'durable'`.
//   - ephemeral: strict TTL eviction for `kind = 'ephemeral'` — ignores
//     access_count so intent-search hits don't extend lifetime (ADR-017).
//
// If dryRun is true, returns what would be removed without deleting.
// The returned slice tags each SourceInfo with EvictionReason ("retention"
// or "ttl") so callers can render per-kind breakdowns.
// Vocabulary is shared and never deleted.
func (s *ContentStore) Cleanup(dryRun bool, ephemeralTTL time.Duration) ([]SourceInfo, error) {
	durable, err := s.cleanupDurable(dryRun, ephemeralTTL)
	if err != nil {
		return nil, err
	}
	ephemeral, err := s.cleanupEphemeral(ephemeralTTL, dryRun)
	if err != nil {
		return nil, err
	}

	merged := make([]SourceInfo, 0, len(durable)+len(ephemeral))
	for _, src := range durable {
		src.EvictionReason = "retention"
		merged = append(merged, src)
	}
	for _, src := range ephemeral {
		src.EvictionReason = "ttl"
		merged = append(merged, src)
	}
	return merged, nil
}

// PurgeEphemeral evicts ephemeral sources past the TTL window, skipping
// durable retention entirely. Intended for a one-shot "clear scratch"
// operation exposed via capy_cleanup's purge_ephemeral flag. Durable
// rows are never touched, regardless of retention score.
func (s *ContentStore) PurgeEphemeral(ttl time.Duration, dryRun bool) ([]SourceInfo, error) {
	ephemeral, err := s.cleanupEphemeral(ttl, dryRun)
	if err != nil {
		return nil, err
	}
	for i := range ephemeral {
		ephemeral[i].EvictionReason = "ttl"
	}
	return ephemeral, nil
}

// cleanupDurable applies retention-score-based eviction to durable sources.
// A source is a candidate iff its retentionScore is below the evictable
// threshold AND it has never been accessed. ephemeralTTL is threaded
// through only so ClassifySources can bucket ephemeral rows consistently;
// it does not influence durable eviction.
func (s *ContentStore) cleanupDurable(dryRun bool, ephemeralTTL time.Duration) ([]SourceInfo, error) {
	// ClassifySources walks both kinds; the ephemeral branch is wasted
	// work here but negligible at the corpus sizes this store targets.
	// Keep the single entrypoint for consistency with Stats().
	sources, err := s.ClassifySources(ephemeralTTL)
	if err != nil {
		return nil, err
	}

	var candidates []SourceInfo
	for _, src := range sources {
		if src.Kind != KindDurable {
			continue
		}
		if src.RetentionScore >= evictableThreshold {
			continue
		}
		if src.AccessCount > 0 {
			continue
		}
		candidates = append(candidates, src)
	}

	return candidates, s.evict(candidates, dryRun)
}

// cleanupEphemeral evicts ephemeral sources whose indexed_at is older than
// ttl. access_count is intentionally ignored — intent-search writes must not
// extend ephemeral lifetime (ADR-017). Non-positive ttl is treated as the
// safe default of 24 h.
func (s *ContentStore) cleanupEphemeral(ttl time.Duration, dryRun bool) ([]SourceInfo, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	if ttl <= 0 {
		slog.Warn("cleanupEphemeral: non-positive TTL supplied, falling back to 24h",
			"ttl", ttl)
		ttl = 24 * time.Hour
	}
	cutoff := time.Now().Add(-ttl).UTC().Format("2006-01-02 15:04:05")

	rows, err := db.Query(`
		SELECT id, label, content_type, chunk_count, code_chunk_count,
			indexed_at, last_accessed_at, access_count, content_hash, kind
		FROM sources
		WHERE kind = ? AND indexed_at < ?
		ORDER BY id DESC`, KindEphemeral, cutoff)
	if err != nil {
		return nil, fmt.Errorf("selecting stale ephemeral sources: %w", err)
	}
	defer rows.Close()

	var candidates []SourceInfo
	for rows.Next() {
		var si SourceInfo
		var indexedAt, lastAccessedAt string
		if err := rows.Scan(&si.ID, &si.Label, &si.ContentType, &si.ChunkCount,
			&si.CodeChunkCount, &indexedAt, &lastAccessedAt, &si.AccessCount,
			&si.ContentHash, &si.Kind); err != nil {
			continue
		}
		si.IndexedAt, _ = time.Parse("2006-01-02 15:04:05", indexedAt)
		si.LastAccessedAt, _ = time.Parse("2006-01-02 15:04:05", lastAccessedAt)
		candidates = append(candidates, si)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return candidates, s.evict(candidates, dryRun)
}

// evict performs the transactional delete for a list of candidates. On
// dryRun or an empty list it is a no-op. Safe to call from both cleanup
// paths — the three prepared statements are keyed by source id.
func (s *ContentStore) evict(candidates []SourceInfo, dryRun bool) error {
	if dryRun || len(candidates) == 0 {
		return nil
	}

	db, err := s.getDB()
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("beginning cleanup transaction: %w", err)
	}
	defer tx.Rollback()

	// TODO: tx.Stmt() inside the loop creates a new transaction-bound prepared
	// statement per iteration. Lift these three calls above the loop to reuse
	// the transaction-bound statements and avoid leaking statement handles when
	// the candidate list is large.
	for _, src := range candidates {
		if _, err := tx.Stmt(s.stmtDeleteChunksBySource).Exec(src.ID); err != nil {
			return fmt.Errorf("deleting chunks for source %d: %w", src.ID, err)
		}
		if _, err := tx.Stmt(s.stmtDeleteTrigramBySource).Exec(src.ID); err != nil {
			return fmt.Errorf("deleting trigram chunks for source %d: %w", src.ID, err)
		}
		if _, err := tx.Stmt(s.stmtDeleteSource).Exec(src.ID); err != nil {
			return fmt.Errorf("deleting source %d: %w", src.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing cleanup: %w", err)
	}
	return nil
}

// Stats returns knowledge base statistics. ephemeralTTL is required to
// bucket ephemeral sources into fresh/stale — callers should pass the
// same TTL they use for Cleanup (typically
// `config.Store.Cleanup.EphemeralTTLHours`).
func (s *ContentStore) Stats(ephemeralTTL time.Duration) (*StoreStats, error) {
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

	// Per-kind counts and durable tier / ephemeral fresh-stale distribution.
	sources, err := s.ClassifySources(ephemeralTTL)
	if err != nil {
		return &stats, nil
	}
	for _, src := range sources {
		if src.Kind == KindEphemeral {
			stats.EphemeralSourceCount++
			switch src.Tier {
			case "fresh":
				stats.EphemeralFreshCount++
			case "stale":
				stats.EphemeralStaleCount++
			}
			continue
		}
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

	return &stats, nil
}
