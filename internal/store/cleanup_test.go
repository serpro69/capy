package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Retention score unit tests ────────────────────────────────────────────

func TestRetentionScoreNewCodeSource(t *testing.T) {
	now := time.Now()
	src := SourceInfo{
		ChunkCount:     5,
		CodeChunkCount: 5,
		IndexedAt:      now,
		AccessCount:    0,
	}
	score := retentionScore(src, now)
	// Fresh code source: salience=0.7, decay=1.0, no access boost → score=0.7
	assert.InDelta(t, 0.7, score, 0.01, "freshly indexed code source should score ~0.7 (hot)")
	assert.GreaterOrEqual(t, score, hotThreshold)
}

func TestRetentionScoreOldUnaccessed(t *testing.T) {
	now := time.Now()
	src := SourceInfo{
		ChunkCount:     3,
		CodeChunkCount: 0,
		IndexedAt:      now.Add(-90 * 24 * time.Hour),
		AccessCount:    0,
	}
	score := retentionScore(src, now)
	// 90-day-old prose, never accessed: salience=0.5, decay≈0.02, no access boost → score≈0.01
	assert.Less(t, score, evictableThreshold, "90-day-old never-accessed prose should be evictable")
}

func TestRetentionScoreOldButAccessed(t *testing.T) {
	now := time.Now()
	src := SourceInfo{
		ChunkCount:     5,
		CodeChunkCount: 5,
		IndexedAt:      now.Add(-60 * 24 * time.Hour),
		LastAccessedAt: now.Add(-1 * 24 * time.Hour),
		AccessCount:    10,
	}
	score := retentionScore(src, now)
	// 60-day-old code with 10 accesses and recent access:
	// salience=0.7+0.2=0.9, decay≈0.07, access_boost≈0.5 → score≈0.9*0.07+0.3*0.5≈0.21
	assert.GreaterOrEqual(t, score, evictableThreshold,
		"old but frequently accessed source should not be evictable")
}

func TestRetentionScoreContentTypeWeight(t *testing.T) {
	now := time.Now()
	base := SourceInfo{
		ChunkCount:  5,
		IndexedAt:   now.Add(-7 * 24 * time.Hour),
		AccessCount: 0,
	}

	// Pure code
	code := base
	code.CodeChunkCount = 5
	codeScore := retentionScore(code, now)

	// Mixed
	mixed := base
	mixed.CodeChunkCount = 2
	mixedScore := retentionScore(mixed, now)

	// Pure prose
	prose := base
	prose.CodeChunkCount = 0
	proseScore := retentionScore(prose, now)

	assert.Greater(t, codeScore, mixedScore, "code should score higher than mixed")
	assert.Greater(t, mixedScore, proseScore, "mixed should score higher than prose")
}

// ─── classifyTier tests ────────────────────────────────────────────

func TestClassifyTierThresholds(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		src          SourceInfo
		expectedTier string
	}{
		{
			name: "fresh code is hot",
			src: SourceInfo{
				ChunkCount: 5, CodeChunkCount: 5,
				IndexedAt: now,
			},
			expectedTier: "hot",
		},
		{
			name: "7-day code is warm",
			src: SourceInfo{
				ChunkCount: 5, CodeChunkCount: 5,
				IndexedAt: now.Add(-7 * 24 * time.Hour),
			},
			expectedTier: "warm",
		},
		{
			name: "35-day unaccessed prose is evictable",
			src: SourceInfo{
				ChunkCount: 3, CodeChunkCount: 0,
				IndexedAt: now.Add(-40 * 24 * time.Hour),
			},
			expectedTier: "evictable",
		},
		{
			name: "30-day code is cold",
			src: SourceInfo{
				ChunkCount: 5, CodeChunkCount: 5,
				IndexedAt: now.Add(-30 * 24 * time.Hour),
			},
			expectedTier: "cold",
		},
		{
			name: "old but recently accessed is warm or better",
			src: SourceInfo{
				ChunkCount: 5, CodeChunkCount: 5,
				IndexedAt:      now.Add(-30 * 24 * time.Hour),
				LastAccessedAt: now,
				AccessCount:    10,
			},
			expectedTier: "warm", // access boost keeps it above cold
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tier, _ := classifyTier(tc.src, now)
			assert.Equal(t, tc.expectedTier, tier)
		})
	}
}

// ─── Integration tests (ClassifySources, Cleanup, Stats) ──────────────────

func TestClassifySources(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Hot: recently indexed code source with at least one access (recency boost fires).
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('hot-source', 'code', 5, 5, 'h1', datetime('now'), datetime('now'), 1)`)
	// Warm: 7-day-old code source.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at)
		VALUES ('warm-source', 'code', 3, 3, 'h2', datetime('now', '-7 days'), datetime('now', '-7 days'))`)
	// Cold/evictable: 60-day-old prose, never accessed.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('cold-source', 'plaintext', 1, 0, 'h3', datetime('now', '-60 days'), datetime('now', '-60 days'), 0)`)

	sources, err := s.ClassifySources()
	require.NoError(t, err)
	require.Len(t, sources, 3)

	tiers := make(map[string]string)
	for _, src := range sources {
		tiers[src.Label] = src.Tier
		// RetentionScore should be populated.
		assert.Greater(t, src.RetentionScore, 0.0, "retention score should be positive for %s", src.Label)
	}
	assert.Equal(t, "hot", tiers["hot-source"])
	assert.Equal(t, "warm", tiers["warm-source"])
	// 60-day-old prose with 0 access should be evictable.
	assert.Equal(t, "evictable", tiers["cold-source"])
}

func TestCleanupDryRun(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert an old source with access_count = 0 (evictable).
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('stale', 'plaintext', 1, 0, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 0)`)
	db.Exec(`INSERT INTO chunks (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)
	db.Exec(`INSERT INTO chunks_trigram (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)

	// Dry run should return candidates but not delete.
	candidates, err := s.Cleanup(true, 24*time.Hour)
	require.NoError(t, err)
	assert.Len(t, candidates, 1)
	assert.Equal(t, "stale", candidates[0].Label)

	// Source should still exist.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&count)
	assert.Equal(t, 1, count)
}

func TestCleanupForce(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert an old source with access_count = 0 (evictable).
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('stale', 'plaintext', 1, 0, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 0)`)
	db.Exec(`INSERT INTO chunks (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)
	db.Exec(`INSERT INTO chunks_trigram (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)

	// Force cleanup should delete.
	removed, err := s.Cleanup(false, 24*time.Hour)
	require.NoError(t, err)
	assert.Len(t, removed, 1)

	// Source and chunks should be gone.
	var srcCount, chunkCount, trigramCount int
	db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&srcCount)
	db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&chunkCount)
	db.QueryRow("SELECT COUNT(*) FROM chunks_trigram").Scan(&trigramCount)
	assert.Equal(t, 0, srcCount)
	assert.Equal(t, 0, chunkCount)
	assert.Equal(t, 0, trigramCount)
}

func TestCleanupPreservesRecentlyAccessed(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert a source that was indexed long ago but accessed recently (access_count=1
	// reflects a valid state — recency boost only applies when access_count > 0).
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('recent-access', 'plaintext', 1, 0, 'h1', datetime('now', '-60 days'), datetime('now', '-1 day'), 1)`)

	candidates, err := s.Cleanup(true, 24*time.Hour)
	require.NoError(t, err)
	assert.Empty(t, candidates, "recently accessed source should not be a cleanup candidate")
}

func TestCleanupPreservesAccessedSources(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert a cold source with access_count > 0.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('accessed', 'plaintext', 1, 0, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 5)`)

	candidates, err := s.Cleanup(true, 24*time.Hour)
	require.NoError(t, err)
	assert.Empty(t, candidates, "source with access_count > 0 should not be a cleanup candidate")
}

// ─── Cleanup split (durable retention + ephemeral TTL) ────────────────────

func TestCleanupEphemeralTTL(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Old ephemeral (48h ago) — should be evicted by TTL.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('execute:shell-old', 'plaintext', 1, 0, 'eph-old', datetime('now', '-48 hours'), datetime('now', '-48 hours'), 0, 'ephemeral')`)
	// Young ephemeral (1h ago) — should be preserved.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('execute:shell-young', 'plaintext', 1, 0, 'eph-young', datetime('now', '-1 hours'), datetime('now', '-1 hours'), 0, 'ephemeral')`)
	// Old ephemeral with many accesses — immortality-gate regression test.
	// Retention-based cleanup would spare this (access_count > 0); TTL must not.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('execute:shell-hot', 'plaintext', 1, 0, 'eph-hot', datetime('now', '-48 hours'), datetime('now', '-1 hours'), 5, 'ephemeral')`)

	pruned, err := s.Cleanup(true, 24*time.Hour)
	require.NoError(t, err)

	labels := make(map[string]string)
	for _, p := range pruned {
		labels[p.Label] = p.EvictionReason
	}
	assert.Equal(t, "ttl", labels["execute:shell-old"], "old ephemeral evicted with reason 'ttl'")
	assert.Equal(t, "ttl", labels["execute:shell-hot"], "ephemeral past TTL evicted even with access_count > 0")
	_, youngPresent := labels["execute:shell-young"]
	assert.False(t, youngPresent, "young ephemeral within TTL preserved")
}

func TestCleanupDurableOnlyRespectsKind(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Old, never-accessed durable prose — retention path should evict.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('notes', 'plaintext', 1, 0, 'dur-old', datetime('now', '-60 days'), datetime('now', '-60 days'), 0, 'durable')`)
	// Young ephemeral that would score "cold" by age but is too fresh for TTL.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('execute:shell-young', 'plaintext', 1, 0, 'eph-young', datetime('now', '-1 hours'), datetime('now', '-1 hours'), 0, 'ephemeral')`)

	pruned, err := s.Cleanup(true, 24*time.Hour)
	require.NoError(t, err)

	require.Len(t, pruned, 1, "only the durable candidate should be pruned")
	assert.Equal(t, "notes", pruned[0].Label)
	assert.Equal(t, "retention", pruned[0].EvictionReason)
}

func TestCleanupMergedReasons(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// One candidate per path.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('stale-prose', 'plaintext', 1, 0, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 0, 'durable')`)
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('execute:shell-old', 'plaintext', 1, 0, 'h2', datetime('now', '-48 hours'), datetime('now', '-48 hours'), 0, 'ephemeral')`)

	pruned, err := s.Cleanup(false, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, pruned, 2)

	reasons := map[string]string{}
	for _, p := range pruned {
		reasons[p.Label] = p.EvictionReason
	}
	assert.Equal(t, "retention", reasons["stale-prose"])
	assert.Equal(t, "ttl", reasons["execute:shell-old"])

	// Both actually deleted.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&count)
	assert.Equal(t, 0, count)
}

func TestCleanupEphemeralIgnoresAccessCount(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// This is the scenario ADR-017 exists to fix: a frequently-hit ephemeral row
	// would otherwise ride access_count past every retention sweep forever.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, code_chunk_count, content_hash, indexed_at, last_accessed_at, access_count, kind)
		VALUES ('execute:shell', 'plaintext', 1, 0, 'h1', datetime('now', '-48 hours'), datetime('now', '-5 minutes'), 50, 'ephemeral')`)

	pruned, err := s.Cleanup(true, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, pruned, 1)
	assert.Equal(t, "ttl", pruned[0].EvictionReason)
}

func TestStats(t *testing.T) {
	s := newTestStore(t)

	// Empty store.
	stats, err := s.Stats()
	require.NoError(t, err)
	assert.Equal(t, 0, stats.SourceCount)
	assert.Equal(t, 0, stats.ChunkCount)
	assert.Equal(t, 0, stats.VocabCount)
	assert.Greater(t, stats.DBSizeBytes, int64(0), "DB file should exist")

	// Populated store.
	_, err = s.Index("authentication middleware validates tokens", "test-src", "plaintext", KindDurable)
	require.NoError(t, err)

	stats, err = s.Stats()
	require.NoError(t, err)
	assert.Equal(t, 1, stats.SourceCount)
	assert.Greater(t, stats.ChunkCount, 0)
	assert.Greater(t, stats.VocabCount, 0)
	// Freshly indexed but never-searched source scores right at the hot/warm boundary
	// (code: 0.7 × ~1.0 = ~0.7). Sub-millisecond timing between SQL insert and Go
	// scoring may push it to warm. Either tier is correct for a fresh, unused source.
	assert.Equal(t, 1, stats.HotCount+stats.WarmCount, "freshly indexed source should be hot or warm")
	assert.Equal(t, 0, stats.ColdCount)
	assert.Equal(t, 0, stats.EvictableCount)
}
