package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- SourceTooLargeError ---

func TestSourceTooLargeError_Message(t *testing.T) {
	err := &SourceTooLargeError{Size: 3_000_000, Limit: 2_097_152}
	msg := err.Error()
	assert.Contains(t, msg, "source too large")
	assert.Contains(t, msg, "3000000 bytes")
	assert.Contains(t, msg, "2097152 byte limit")
	assert.Contains(t, msg, "store.max_source_bytes")
}

func TestSourceTooLargeError_ErrorsAs(t *testing.T) {
	err := &SourceTooLargeError{Size: 5, Limit: 3}
	var target *SourceTooLargeError
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, 5, target.Size)
	assert.Equal(t, 3, target.Limit)
}

// --- Index size gate ---

func TestIndex_RejectsOversizedContent(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	limit := 1024
	s := NewContentStore(dbPath, dir, 0, limit)
	t.Cleanup(func() { s.Close() })

	content := strings.Repeat("a", limit+1)
	_, err := s.Index(content, "too-big", "", KindDurable)
	require.Error(t, err)

	var sErr *SourceTooLargeError
	require.ErrorAs(t, err, &sErr)
	assert.Equal(t, limit+1, sErr.Size)
	assert.Equal(t, limit, sErr.Limit)
}

func TestIndex_AcceptsContentAtLimit(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	limit := 2048
	s := NewContentStore(dbPath, dir, 0, limit)
	t.Cleanup(func() { s.Close() })

	content := strings.Repeat("a", limit)
	result, err := s.Index(content, "just-right", "", KindDurable)
	require.NoError(t, err)
	assert.Greater(t, result.TotalChunks, 0)
}

func TestIndex_AcceptsContentUnderLimit(t *testing.T) {
	s := newTestStore(t)
	content := "small content"
	result, err := s.Index(content, "small", "", KindDurable)
	require.NoError(t, err)
	assert.Equal(t, "small", result.Label)
}

// --- IndexChunked size gate ---

func TestIndexChunked_RejectsOversizedTranscript(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	limit := 512
	s := NewContentStore(dbPath, dir, 0, limit)
	t.Cleanup(func() { s.Close() })

	transcript := strings.Repeat("x", limit+1)
	chunks := []Chunk{{Title: "test", Content: "chunk"}}
	_, err := s.IndexChunked(transcript, "chunked-too-big", "plaintext", KindSession, chunks)
	require.Error(t, err)

	var sErr *SourceTooLargeError
	require.ErrorAs(t, err, &sErr)
}

func TestIndexChunked_AcceptsTranscriptAtLimit(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	limit := 1024
	s := NewContentStore(dbPath, dir, 0, limit)
	t.Cleanup(func() { s.Close() })

	transcript := strings.Repeat("x", limit)
	chunks := []Chunk{{Title: "ok", Content: "data"}}
	result, err := s.IndexChunked(transcript, "chunked-ok", "plaintext", KindSession, chunks)
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalChunks)
}

// --- DefaultMaxSourceBytes ---

func TestNewContentStore_DefaultsMaxSourceBytes(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := NewContentStore(dbPath, dir, 0, 0)
	defer s.Close()
	assert.Equal(t, DefaultMaxSourceBytes, s.maxSourceBytes)
}

func TestNewContentStore_CustomMaxSourceBytes(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s := NewContentStore(dbPath, dir, 0, 4096)
	defer s.Close()
	assert.Equal(t, 4096, s.maxSourceBytes)
}

// --- Cleanup oversized eviction ---

func TestCleanup_EvictsOversizedSources(t *testing.T) {
	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create store with a high limit to index large content.
	s := NewContentStore(dbPath, dir, 0, 100_000)
	_, err := s.Index(strings.Repeat("big ", 5000), "big-source", "", KindDurable)
	require.NoError(t, err)
	_, err = s.Index("small content", "small-source", "", KindDurable)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Reopen with a smaller limit so "big-source" is now oversized.
	s2 := NewContentStore(dbPath, dir, 0, 1024)
	t.Cleanup(func() { s2.Close() })

	// Dry run should report the oversized source.
	pruned, err := s2.Cleanup(true, 24*time.Hour, 60*24*time.Hour)
	require.NoError(t, err)

	var oversizedLabels []string
	for _, src := range pruned {
		if src.EvictionReason == "oversized" {
			oversizedLabels = append(oversizedLabels, src.Label)
		}
	}
	assert.Contains(t, oversizedLabels, "big-source")
	assert.NotContains(t, oversizedLabels, "small-source")

	// Verify source still exists (dry run).
	sources, err := s2.ListSources()
	require.NoError(t, err)
	assert.Len(t, sources, 2)

	// Actual eviction.
	pruned, err = s2.Cleanup(false, 24*time.Hour, 60*24*time.Hour)
	require.NoError(t, err)

	sources, err = s2.ListSources()
	require.NoError(t, err)
	assert.Len(t, sources, 1)
	assert.Equal(t, "small-source", sources[0].Label)
}

// --- EvictByLabel ---

func TestEvictByLabel_RemovesSource(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index("content to evict", "evict-me", "", KindDurable)
	require.NoError(t, err)

	evicted, err := s.EvictByLabel("evict-me", false)
	require.NoError(t, err)
	assert.Equal(t, "evict-me", evicted.Label)
	assert.Equal(t, "manual", evicted.EvictionReason)

	sources, err := s.ListSources()
	require.NoError(t, err)
	assert.Empty(t, sources)
}

func TestEvictByLabel_DryRun(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index("content to keep", "keep-me", "", KindDurable)
	require.NoError(t, err)

	evicted, err := s.EvictByLabel("keep-me", true)
	require.NoError(t, err)
	assert.Equal(t, "keep-me", evicted.Label)

	// Source should still exist.
	sources, err := s.ListSources()
	require.NoError(t, err)
	assert.Len(t, sources, 1)
}

func TestEvictByLabel_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.EvictByLabel("nonexistent", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source not found")
}

// --- FreelistRatio ---

func TestFreelistRatio_ReturnsNonNegative(t *testing.T) {
	s := newTestStore(t)

	// Force DB initialization.
	_, err := s.Index("init content", "init", "", KindDurable)
	require.NoError(t, err)

	ratio := s.FreelistRatio()
	assert.GreaterOrEqual(t, ratio, 0.0)
	assert.LessOrEqual(t, ratio, 1.0)
}

// --- Vacuum ---

func TestVacuum_RunsWithoutError(t *testing.T) {
	s := newTestStore(t)

	// Index and delete to create freelist pages.
	_, err := s.Index(strings.Repeat("data ", 1000), "vacuum-test", "", KindDurable)
	require.NoError(t, err)
	_, err = s.EvictByLabel("vacuum-test", false)
	require.NoError(t, err)

	err = s.Vacuum()
	assert.NoError(t, err)
}

// --- DiskUsage ---

func TestDiskUsage_ReturnsValidBreakdown(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Index("# Heading\n\nSome content here.", "du-test", "", KindDurable)
	require.NoError(t, err)

	breakdown, err := s.DiskUsage()
	require.NoError(t, err)

	assert.Greater(t, breakdown.DBFileSize, int64(0))
	assert.Greater(t, breakdown.PageSize, int64(0))
	assert.Greater(t, breakdown.TotalPages, int64(0))
	assert.GreaterOrEqual(t, breakdown.FreelistPages, int64(0))

	// Should have at least one kind entry.
	assert.NotEmpty(t, breakdown.Kinds)
	durableFound := false
	for _, k := range breakdown.Kinds {
		if k.Kind == "durable" {
			durableFound = true
			assert.Greater(t, k.Sources, int64(0))
			assert.Greater(t, k.Chunks, int64(0))
		}
	}
	assert.True(t, durableFound, "should have durable kind in breakdown")

	// Top sources should include our test source.
	assert.NotEmpty(t, breakdown.TopSources)
	assert.Equal(t, "du-test", breakdown.TopSources[0].Label)
}

func TestDiskUsage_EmptyDB(t *testing.T) {
	s := newTestStore(t)

	// Force DB initialization without indexing.
	_, err := s.getDB()
	require.NoError(t, err)

	breakdown, err := s.DiskUsage()
	require.NoError(t, err)
	assert.Greater(t, breakdown.PageSize, int64(0))
	assert.Empty(t, breakdown.Kinds)
	assert.Empty(t, breakdown.TopSources)
}
