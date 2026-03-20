package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifySources(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert sources with varying last_accessed_at.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, last_accessed_at)
		VALUES ('hot-source', 'plaintext', 1, 'h1', datetime('now', '-1 day'))`)
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, last_accessed_at)
		VALUES ('warm-source', 'plaintext', 1, 'h2', datetime('now', '-15 days'))`)
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, last_accessed_at)
		VALUES ('cold-source', 'plaintext', 1, 'h3', datetime('now', '-60 days'))`)

	sources, err := s.ClassifySources()
	require.NoError(t, err)
	require.Len(t, sources, 3)

	tiers := make(map[string]string)
	for _, src := range sources {
		tiers[src.Label] = src.Tier
	}
	assert.Equal(t, "hot", tiers["hot-source"])
	assert.Equal(t, "warm", tiers["warm-source"])
	assert.Equal(t, "cold", tiers["cold-source"])
}

func TestCleanupDryRun(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert a cold source with access_count = 0.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('stale', 'plaintext', 1, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 0)`)
	db.Exec(`INSERT INTO chunks (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)
	db.Exec(`INSERT INTO chunks_trigram (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)

	// Dry run should return candidates but not delete.
	candidates, err := s.Cleanup(30, true)
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

	// Insert a cold source with access_count = 0.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('stale', 'plaintext', 1, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 0)`)
	db.Exec(`INSERT INTO chunks (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)
	db.Exec(`INSERT INTO chunks_trigram (title, content, source_id, content_type) VALUES ('t', 'c', 1, 'prose')`)

	// Force cleanup should delete.
	removed, err := s.Cleanup(30, false)
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

	// Insert a source that was indexed long ago but accessed recently.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('recent-access', 'plaintext', 1, 'h1', datetime('now', '-60 days'), datetime('now', '-1 day'), 0)`)

	candidates, err := s.Cleanup(30, true)
	require.NoError(t, err)
	assert.Empty(t, candidates, "recently accessed source should not be a cleanup candidate")
}

func TestCleanupPreservesAccessedSources(t *testing.T) {
	s := newTestStore(t)
	db, err := s.getDB()
	require.NoError(t, err)

	// Insert a cold source with access_count > 0.
	db.Exec(`INSERT INTO sources (label, content_type, chunk_count, content_hash, indexed_at, last_accessed_at, access_count)
		VALUES ('accessed', 'plaintext', 1, 'h1', datetime('now', '-60 days'), datetime('now', '-60 days'), 5)`)

	candidates, err := s.Cleanup(30, true)
	require.NoError(t, err)
	assert.Empty(t, candidates, "source with access_count > 0 should not be a cleanup candidate")
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
	_, err = s.Index("authentication middleware validates tokens", "test-src", "plaintext")
	require.NoError(t, err)

	stats, err = s.Stats()
	require.NoError(t, err)
	assert.Equal(t, 1, stats.SourceCount)
	assert.Greater(t, stats.ChunkCount, 0)
	assert.Greater(t, stats.VocabCount, 0)
	assert.Equal(t, 1, stats.HotCount, "freshly indexed source should be hot")
	assert.Equal(t, 0, stats.ColdCount)
}
