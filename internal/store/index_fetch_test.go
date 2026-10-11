package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fetchMeta(t *testing.T, s *ContentStore, label string) *SourceMeta {
	t.Helper()
	meta, err := s.GetSourceMeta(label)
	require.NoError(t, err)
	require.NotNil(t, meta)
	return meta
}

func TestIndexFetchedRenewalAndRetention(t *testing.T) {
	for _, kind := range []SourceKind{KindDurable, KindEphemeral} {
		t.Run(string(kind), func(t *testing.T) {
			s := newTestStore(t)
			const body = "# First\n\nquokka marker\n\n# Second\n\nwombat marker"
			firstTime := time.UnixMilli(1700000000123)
			first, err := s.IndexFetched(body, "url", "markdown", kind, firstTime)
			require.NoError(t, err)
			require.Greater(t, first.TotalChunks, 1)
			assert.Equal(t, firstTime.UnixMilli(), *fetchMeta(t, s, "url").FetchValidatedAtMS)
			db, err := s.getDB()
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE sources SET indexed_at = '2001-01-01 00:00:00' WHERE label = 'url'`)
			require.NoError(t, err)
			before := fetchMeta(t, s, "url")
			validated := time.Now().Truncate(time.Millisecond)
			// Deliberately offer another chunking strategy. Dedup must report the
			// retained chunks, never the freshly prepared plaintext count.
			second, err := s.IndexFetched(body, "url", "plaintext", kind, validated)
			require.NoError(t, err)
			assert.True(t, second.AlreadyIndexed)
			assert.Equal(t, first.SourceID, second.SourceID)
			assert.Equal(t, first.TotalChunks, second.TotalChunks)
			assert.Equal(t, first.ContentType, second.ContentType)
			after := fetchMeta(t, s, "url")
			assert.Equal(t, before.IndexedAt, after.IndexedAt)
			assert.Equal(t, validated.UnixMilli(), *after.FetchValidatedAtMS)
			_, err = s.SearchWithFallback("quokka", 3, SearchOptions{Source: "url"})
			require.NoError(t, err)
			assert.Equal(t, validated.UnixMilli(), *fetchMeta(t, s, "url").FetchValidatedAtMS)
			require.NoError(t, s.Close())
			assert.Equal(t, validated.UnixMilli(), *fetchMeta(t, s, "url").FetchValidatedAtMS)
			if kind == KindEphemeral {
				sources, err := s.ClassifySources(24*time.Hour, 0)
				require.NoError(t, err)
				require.Len(t, sources, 1)
				assert.Equal(t, "stale", sources[0].Tier, "fresh validation must not extend retention")
				evicted, err := s.Cleanup(false, 24*time.Hour, 0)
				require.NoError(t, err)
				require.Len(t, evicted, 1)
				meta, err := s.GetSourceMeta("url")
				require.NoError(t, err)
				assert.Nil(t, meta)
			}
		})
	}
}

func TestIndexFetchedKindTransitions(t *testing.T) {
	s := newTestStore(t)
	validated := time.UnixMilli(1700000000456)
	first, err := s.Index("same content", "url", "plaintext", KindEphemeral)
	require.NoError(t, err)
	assert.Nil(t, fetchMeta(t, s, "url").FetchValidatedAtMS)
	before := fetchMeta(t, s, "url").IndexedAt
	for i, kind := range []SourceKind{KindDurable, KindEphemeral, KindEphemeral} {
		at := validated.Add(time.Duration(i) * time.Second)
		got, err := s.IndexFetched("same content", "url", "plaintext", kind, at)
		require.NoError(t, err)
		assert.True(t, got.AlreadyIndexed)
		assert.Equal(t, first.SourceID, got.SourceID)
		assert.Equal(t, first.TotalChunks, got.TotalChunks)
		meta := fetchMeta(t, s, "url")
		assert.Equal(t, kind, meta.Kind)
		assert.Equal(t, at.UnixMilli(), *meta.FetchValidatedAtMS)
		assert.Equal(t, before, meta.IndexedAt)
	}
}

func TestIndexFetchedOutOfOrderAndGenericWrites(t *testing.T) {
	s := newTestStore(t)
	older := time.UnixMilli(1700000000001)
	newer := older.Add(time.Hour)
	_, err := s.IndexFetched("newer response", "url", "plaintext", KindDurable, newer)
	require.NoError(t, err)
	// A delayed older response commits last: its own timestamp must travel with
	// its bytes, even though that makes freshness go backwards.
	got, err := s.IndexFetched("older response", "url", "plaintext", KindDurable, older)
	require.NoError(t, err)
	assert.False(t, got.AlreadyIndexed)
	assert.Equal(t, older.UnixMilli(), *fetchMeta(t, s, "url").FetchValidatedAtMS)
	chunks, err := s.GetChunksBySource(got.SourceID)
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	assert.Equal(t, "older response", chunks[0].Content)
	_, err = s.Index("older response", "url", "plaintext", KindDurable)
	require.NoError(t, err)
	assert.Equal(t, older.UnixMilli(), *fetchMeta(t, s, "url").FetchValidatedAtMS, "generic dedup cannot renew freshness")
	for _, tc := range []struct {
		name  string
		write func() (*IndexResult, error)
	}{
		{"inline", func() (*IndexResult, error) { return s.Index("replacement", "url", "plaintext", KindDurable) }},
		{"file", func() (*IndexResult, error) {
			return s.IndexWithFilePath("replacement", "url", "plaintext", KindDurable, "/missing")
		}},
		{"chunked", func() (*IndexResult, error) {
			return s.IndexChunked("replacement", "url", "plaintext", KindDurable, []Chunk{{Content: "replacement"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.IndexFetched("remote original", "url", "plaintext", KindDurable, newer)
			require.NoError(t, err)
			_, err = tc.write()
			require.NoError(t, err)
			assert.Nil(t, fetchMeta(t, s, "url").FetchValidatedAtMS)
		})
	}
}

func TestIndexFetchedFailureRollsBackContentAndMarker(t *testing.T) {
	for _, content := range []string{"original", "replacement"} {
		t.Run(content, func(t *testing.T) {
			s := newTestStore(t)
			first, err := s.IndexFetched("original", "url", "plaintext", KindEphemeral, time.UnixMilli(1700000000001))
			require.NoError(t, err)
			before := fetchMeta(t, s, "url")
			db, err := s.getDB()
			require.NoError(t, err)
			for _, ddl := range []string{
				`CREATE TRIGGER reject_validation_update BEFORE UPDATE OF fetch_validated_at_ms ON sources BEGIN SELECT RAISE(ABORT, 'validation write failed'); END`,
				`CREATE TRIGGER reject_validation_insert BEFORE INSERT ON sources BEGIN SELECT RAISE(ABORT, 'validation write failed'); END`,
			} {
				_, err := db.Exec(ddl)
				require.NoError(t, err)
			}
			_, err = s.IndexFetched(content, "url", "plaintext", KindDurable, time.Now())
			require.ErrorContains(t, err, "validation write failed")
			assert.Equal(t, before, fetchMeta(t, s, "url"), "kind, retention and validation must roll back together")
			chunks, err := s.GetChunksBySource(first.SourceID)
			require.NoError(t, err)
			require.Len(t, chunks, 1)
			assert.Equal(t, "original", chunks[0].Content)
		})
	}
}

func TestIndexFetchedValidationBeforeStorage(t *testing.T) {
	s := newTestStore(t)
	for _, at := range []time.Time{{}, time.UnixMilli(0), time.UnixMilli(-1)} {
		_, err := s.IndexFetched("content", "url", "plaintext", KindDurable, at)
		require.ErrorContains(t, err, "invalid fetch validation timestamp")
	}
	_, err := s.IndexFetched("content", "url", "plaintext", SourceKind("invalid"), time.Now())
	require.ErrorContains(t, err, "invalid source kind")
	_, err = s.IndexFetched(strings.Repeat("x", DefaultMaxSourceBytes+1), "url", "plaintext", KindDurable, time.Now())
	require.Error(t, err)
	assert.NoFileExists(t, s.dbPath)
}

func TestFetchValidationMigrationReopenAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite3", EncryptedDSN(path, testEncryptionKey))
	require.NoError(t, err)
	legacy := strings.Replace(schemaSQL, ",\n  fetch_validated_at_ms INTEGER", "", 1)
	_, err = db.Exec(legacy)
	require.NoError(t, err)
	require.False(t, hasColumn(t, db, "sources", "fetch_validated_at_ms"))
	_, err = db.Exec(`INSERT INTO sources (label) VALUES ('legacy')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	s := scheduleStore(t, path, 0)
	peer := scheduleStore(t, path, 0)
	errs := make(chan error, 2)
	for _, instance := range []*ContentStore{s, peer} {
		go func() { _, err := instance.getDB(); errs <- err }()
	}
	for range 2 {
		require.NoError(t, <-errs)
	}
	require.NoError(t, peer.Close())
	for range 2 {
		assert.Nil(t, fetchMeta(t, s, "legacy").FetchValidatedAtMS)
		db, err := s.getDB()
		require.NoError(t, err)
		require.NoError(t, applyMigrations(db))
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM migrations WHERE name = '021_add_fetch_validated_at_ms'`).Scan(&count))
		assert.Equal(t, 1, count)
		require.NoError(t, s.Close())
	}
}

func TestFetchValidationMigrationRollback(t *testing.T) {
	db := openPreMigrationDB(t, []string{"legacy"})
	require.NoError(t, migrate017AddSourceKind(db))
	require.NoError(t, ensureMigrationsTable(db))
	_, err := db.Exec(`CREATE TRIGGER reject021 BEFORE INSERT ON migrations
		WHEN NEW.name = '021_add_fetch_validated_at_ms' BEGIN SELECT RAISE(ABORT, 'injected migration failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, migrate021AddFetchValidatedAt(db), "injected migration failure")
	assert.False(t, hasColumn(t, db, "sources", "fetch_validated_at_ms"))
	_, err = db.Exec(`DROP TRIGGER reject021`)
	require.NoError(t, err)
	require.NoError(t, migrate021AddFetchValidatedAt(db))
	require.NoError(t, migrate021AddFetchValidatedAt(db))
}
