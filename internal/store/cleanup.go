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

// ClassifySources returns all sources with tier classification
// based on retention scoring (salience, temporal decay, access boost).
func (s *ContentStore) ClassifySources() ([]SourceInfo, error) {
	sources, err := s.ListSources()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for i := range sources {
		sources[i].Tier, sources[i].RetentionScore = classifyTier(sources[i], now)
	}
	return sources, nil
}

// Cleanup removes evictable sources that have never been accessed (access_count = 0).
// If dryRun is true, returns what would be removed without deleting.
// Vocabulary is shared and never deleted.
func (s *ContentStore) Cleanup(dryRun bool) ([]SourceInfo, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	sources, err := s.ClassifySources()
	if err != nil {
		return nil, err
	}

	var candidates []SourceInfo
	for _, src := range sources {
		if src.RetentionScore >= evictableThreshold {
			continue
		}
		if src.AccessCount > 0 {
			continue
		}
		candidates = append(candidates, src)
	}

	if dryRun || len(candidates) == 0 {
		return candidates, nil
	}

	// Delete in transaction.
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning cleanup transaction: %w", err)
	}
	defer tx.Rollback()

	// TODO: tx.Stmt() inside the loop creates a new transaction-bound prepared
	// statement per iteration. Lift these three calls above the loop to reuse
	// the transaction-bound statements and avoid leaking statement handles when
	// the candidate list is large.
	for _, src := range candidates {
		if _, err := tx.Stmt(s.stmtDeleteChunksBySource).Exec(src.ID); err != nil {
			return nil, fmt.Errorf("deleting chunks for source %d: %w", src.ID, err)
		}
		if _, err := tx.Stmt(s.stmtDeleteTrigramBySource).Exec(src.ID); err != nil {
			return nil, fmt.Errorf("deleting trigram chunks for source %d: %w", src.ID, err)
		}
		if _, err := tx.Stmt(s.stmtDeleteSource).Exec(src.ID); err != nil {
			return nil, fmt.Errorf("deleting source %d: %w", src.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing cleanup: %w", err)
	}

	return candidates, nil
}

// Stats returns knowledge base statistics.
func (s *ContentStore) Stats() (*StoreStats, error) {
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

	// Tier distribution.
	sources, err := s.ClassifySources()
	if err != nil {
		return &stats, nil
	}
	for _, src := range sources {
		switch src.Tier {
		case "hot":
			stats.HotCount++
		case "warm":
			stats.WarmCount++
		case "cold":
			stats.ColdCount++
		case "evictable":
			stats.EvictableCount++
		}
	}

	return &stats, nil
}
