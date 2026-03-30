package store

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

const (
	hotThresholdDays  = 7
	warmThresholdDays = 30
)

// ClassifySources returns all sources with tier classification
// based on last_accessed_at: hot (<7d), warm (<30d), cold (>=30d).
func (s *ContentStore) ClassifySources() ([]SourceInfo, error) {
	sources, err := s.ListSources()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	for i := range sources {
		sources[i].Tier = classifyTier(sources[i].LastAccessedAt, now)
	}
	return sources, nil
}

func classifyTier(lastAccessed time.Time, now time.Time) string {
	if lastAccessed.IsZero() {
		return "cold"
	}
	days := int(now.Sub(lastAccessed).Hours() / 24)
	switch {
	case days < hotThresholdDays:
		return "hot"
	case days < warmThresholdDays:
		return "warm"
	default:
		return "cold"
	}
}

// Cleanup removes cold sources that have never been accessed (access_count = 0)
// and are older than maxAgeDays. If dryRun is true, returns what would be removed
// without deleting. Vocabulary is shared and never deleted.
func (s *ContentStore) Cleanup(maxAgeDays int, dryRun bool) ([]SourceInfo, error) {
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	sources, err := s.ClassifySources()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	var candidates []SourceInfo
	for _, src := range sources {
		if src.Tier != "cold" {
			continue
		}
		if src.AccessCount > 0 {
			continue
		}
		age := int(now.Sub(src.IndexedAt).Hours() / 24)
		if age < maxAgeDays {
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
		}
	}

	return &stats, nil
}
