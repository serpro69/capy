package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/fileio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scheduleStore(t *testing.T, path string, limit int) *ContentStore {
	t.Helper()
	s := NewContentStore(path, filepath.Dir(path), 0, limit, WithEncryptionKey(testEncryptionKey, "test"))
	s.refreshNow = func() time.Time { return time.Unix(1700000000, 0) }
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func checkedCount(t *testing.T, s *ContentStore) int {
	t.Helper()
	db, err := s.getDB()
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sources WHERE file_path IS NOT NULL AND file_check_seq > 0`).Scan(&count))
	return count
}

func sourceSequence(t *testing.T, s *ContentStore, label string) int64 {
	t.Helper()
	db, err := s.getDB()
	require.NoError(t, err)
	var seq int64
	require.NoError(t, db.QueryRow(`SELECT file_check_seq FROM sources WHERE label = ?`, label).Scan(&seq))
	return seq
}

func TestStaleScheduleMigrationAndPlans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite3", EncryptedDSN(path, testEncryptionKey))
	require.NoError(t, err)
	// This is the pre-020 schema, including real FTS tables and encryption.
	legacy := strings.Replace(schemaSQL, "file_path TEXT,\n  file_check_seq INTEGER NOT NULL DEFAULT 0", "file_path TEXT", 1)
	_, err = db.Exec(legacy)
	require.NoError(t, err)
	require.False(t, hasColumn(t, db, "sources", "file_check_seq"))
	require.NoError(t, migrate017AddSourceKind(db))
	require.NoError(t, ensureMigrationsTable(db))
	require.NoError(t, migrate018AddSessionKind(db))
	require.NoError(t, migrate019AddFilePath(db))
	_, err = db.Exec(`INSERT INTO sources (label, file_path) VALUES ('old-file', '/missing')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	s := scheduleStore(t, path, 0)
	// Two fresh stores may race to migrate the same encrypted legacy file.
	peer := scheduleStore(t, path, 0)
	errors := make(chan error, 2)
	for _, instance := range []*ContentStore{s, peer} {
		go func() { _, err := instance.getDB(); errors <- err }()
	}
	for range 2 {
		require.NoError(t, <-errors)
	}
	require.NoError(t, peer.Close())
	for range 2 {
		db, err = s.getDB()
		require.NoError(t, err)
		require.True(t, hasColumn(t, db, "sources", "file_check_seq"))
		assert.Zero(t, sourceSequence(t, s, "old-file"))
		for _, query := range []string{staleSourcesSQL, maxFileCheckSQL} {
			rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
			require.NoError(t, err)
			var plan strings.Builder
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
				plan.WriteString(detail)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			assert.Contains(t, plan.String(), "idx_sources_file_check")
			assert.NotContains(t, plan.String(), "TEMP B-TREE")
		}
		require.NoError(t, s.Close())
	}
	// An old-style writer omitting the new column still gets the safe default.
	db, err = s.getDB()
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sources (label, file_path) VALUES ('old-writer', '/other')`)
	require.NoError(t, err)
	assert.Zero(t, sourceSequence(t, s, "old-writer"))
}

func TestStaleScheduleMigrationRollback(t *testing.T) {
	db := openPreMigrationDB(t, []string{"legacy"})
	require.NoError(t, migrate017AddSourceKind(db))
	require.NoError(t, ensureMigrationsTable(db))
	require.NoError(t, migrate018AddSessionKind(db))
	require.NoError(t, migrate019AddFilePath(db))
	_, err := db.Exec(`CREATE TRIGGER reject020 BEFORE INSERT ON migrations
		WHEN NEW.name = '020_add_file_check_seq' BEGIN SELECT RAISE(ABORT, 'injected migration failure'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, migrate020AddFileCheckSeq(db), "injected migration failure")
	assert.False(t, hasColumn(t, db, "sources", "file_check_seq"))
	var indexes int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_sources_file_check'`).Scan(&indexes))
	assert.Zero(t, indexes)
	_, err = db.Exec(`DROP TRIGGER reject020`)
	require.NoError(t, err)
	require.NoError(t, migrate020AddFileCheckSeq(db))
	require.NoError(t, migrate020AddFileCheckSeq(db))
}

func TestStaleSchedulePersistsAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s := scheduleStore(t, path, 0)
	seedRefreshWorkload(t, s, dir, 70, 0)
	for _, want := range []int{32, 64, 70} {
		attempts := 0
		s.SetDenyChecker(func(string) bool { attempts++; return false })
		sources, err := s.listStaleSources(t.Context())
		require.NoError(t, err)
		assert.Len(t, sources, 32)
		s.refreshStaleSources()
		assert.LessOrEqual(t, attempts, 32)
		assert.Equal(t, want, checkedCount(t, s))
		require.NoError(t, s.Close())
		s = scheduleStore(t, path, 0) // Fresh process-like instance, no in-memory cursor.
	}
}

func TestStaleScheduleTenThousandSources(t *testing.T) {
	dir := t.TempDir()
	s := scheduleStore(t, filepath.Join(dir, "test.db"), 0)
	seedRefreshWorkload(t, s, dir, 10000, 0)
	var attempts int
	s.SetDenyChecker(func(string) bool { attempts++; return false })
	for n := 0; n < (10000+31)/32; n++ {
		before := attempts
		forceRefresh(s)
		require.LessOrEqual(t, attempts-before, 32)
	}
	assert.Equal(t, 10000, checkedCount(t, s))
	assert.Positive(t, sourceSequence(t, s, "fixture-09999"))
	// A late addition starts at zero even though its ID sorts after the corpus.
	late := writeAndIndexFile(t, s, dir, "late.txt", "late added content")
	var paths []string
	s.SetDenyChecker(func(path string) bool { paths = append(paths, path); return false })
	forceRefresh(s)
	require.NotEmpty(t, paths)
	assert.Equal(t, late, paths[0])
}

func TestStaleScheduleSourceReplacement(t *testing.T) {
	for _, sameContent := range []bool{false, true} {
		t.Run(fmt.Sprint(sameContent), func(t *testing.T) {
			s := scheduleStore(t, filepath.Join(t.TempDir(), "test.db"), 0)
			_, err := s.IndexWithFilePath("first", "label", "", KindDurable, "/first")
			require.NoError(t, err)
			db, err := s.getDB()
			require.NoError(t, err)
			_, err = db.Exec(`UPDATE sources SET file_check_seq = 42 WHERE label = 'label'`)
			require.NoError(t, err)
			content := "second"
			if sameContent {
				content = "first"
			}
			_, err = s.IndexWithFilePath(content, "label", "", KindDurable, "/first")
			require.NoError(t, err)
			assert.EqualValues(t, 42, sourceSequence(t, s, "label"))
			_, err = s.IndexWithFilePath(content, "label", "", KindDurable, "/second")
			require.NoError(t, err)
			assert.Zero(t, sourceSequence(t, s, "label"))
			assert.Equal(t, "/second", filePathForLabel(t, s, "label").String)
		})
	}
}

func TestStaleScheduleIncarnationGuard(t *testing.T) {
	for _, replacement := range []string{"inline", "other-file", "deleted", "same-hash-other-file", "same-hash-kind", "unchanged-read"} {
		t.Run(replacement, func(t *testing.T) {
			dir := t.TempDir()
			s := scheduleStore(t, filepath.Join(dir, "test.db"), 0)
			path := writeAndIndexFile(t, s, dir, "doc.txt", "originalmarker")
			modifyFile(t, path, "staleobservedmarker")
			if replacement == "unchanged-read" {
				modifyFile(t, path, "originalmarker")
			}
			s.readFileContent = func(r io.Reader, limit int) ([]byte, error) {
				data, err := fileio.ReadAll(r, limit)
				if err != nil {
					return data, err
				}
				switch replacement {
				case "inline", "unchanged-read":
					_, err = s.Index("newermarker", "src:doc.txt", "", KindDurable)
				case "other-file":
					_, err = s.IndexWithFilePath("newermarker", "src:doc.txt", "", KindDurable, dir+"/other")
				case "same-hash-other-file":
					_, err = s.IndexWithFilePath("originalmarker", "src:doc.txt", "", KindDurable, dir+"/other")
				case "same-hash-kind":
					_, err = s.IndexWithFilePath("originalmarker", "src:doc.txt", "", KindEphemeral, path)
				case "deleted":
					_, err = s.EvictByLabel("src:doc.txt", false)
				}
				return data, err
			}
			assert.Zero(t, forceRefresh(s), "stale data must not replace the new incarnation")
			if replacement == "deleted" {
				assert.Empty(t, sourceContent(t, s, "src:doc.txt"))
			} else {
				assert.NotContains(t, sourceContent(t, s, "src:doc.txt"), "staleobservedmarker")
				assert.Zero(t, sourceSequence(t, s, "src:doc.txt"), "new incarnation was never checked")
			}
		})
	}
}

func TestStaleScheduleBudgets(t *testing.T) {
	for _, outcome := range []string{"changed", "unchanged", "read-error", "oversize"} {
		t.Run(outcome, func(t *testing.T) {
			const limit = 3 * 1024 * 1024
			dir := t.TempDir()
			s := scheduleStore(t, filepath.Join(dir, "test.db"), limit)
			for n := range 4 {
				content := "oldmarker"
				if outcome == "unchanged" {
					content = "[REDACTED]"
				}
				path := writeAndIndexFile(t, s, dir, fmt.Sprintf("%d.txt", n), content)
				modifyFile(t, path, "small stat size")
			}
			payload := "<private>" + strings.Repeat("x", limit-19) + "</private>"
			if outcome == "oversize" {
				payload += "oversized"
			}
			attempts, readBytes := 0, 0
			s.readFileContent = func(_ io.Reader, maxBytes int) ([]byte, error) {
				attempts++
				data, err := fileio.ReadAll(strings.NewReader(payload), maxBytes)
				readBytes += len(data)
				if outcome == "read-error" {
					err = syscall.EIO
				}
				return data, err
			}
			forceRefresh(s)
			assert.Equal(t, 2, attempts)
			assert.Equal(t, 2, checkedCount(t, s))
			wantBytes := 2 * limit
			if outcome == "oversize" {
				wantBytes += 2
			}
			assert.Equal(t, wantBytes, readBytes)
			assert.Zero(t, sourceSequence(t, s, "src:2.txt"))
			assert.Zero(t, sourceSequence(t, s, "src:3.txt"))
		})
	}
}

func TestStaleScheduleLargeConfiguredFile(t *testing.T) {
	const limit = 9 * 1024 * 1024
	dir := t.TempDir()
	s := scheduleStore(t, filepath.Join(dir, "test.db"), limit)
	path := writeAndIndexFile(t, s, dir, "large.txt", "[REDACTED]")
	modifyFile(t, path, "<private>"+strings.Repeat("x", limit-19)+"</private>")
	var readBytes int
	s.readFileContent = func(r io.Reader, n int) ([]byte, error) {
		data, err := fileio.ReadAll(r, n)
		readBytes += len(data)
		return data, err
	}
	forceRefresh(s)
	assert.Equal(t, limit, readBytes)
	assert.Equal(t, 1, checkedCount(t, s))
}

func TestStaleScheduleClockAndCancellation(t *testing.T) {
	for _, mode := range []string{"metadata-latency", "slow-io", "canceled-before", "canceled-during"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s := scheduleStore(t, filepath.Join(dir, "test.db"), 0)
			seedRefreshWorkload(t, s, dir, 3, 3)
			var ticks atomic.Int64
			ticks.Store(time.Unix(1700000000, 0).UnixNano())
			clockCalls := 0
			s.refreshNow = func() time.Time {
				clockCalls++
				if mode == "metadata-latency" && clockCalls == 2 {
					ticks.Add(int64(time.Second))
				}
				return time.Unix(0, ticks.Load())
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled-before" {
				cancel()
			}
			reads := 0
			s.readFileContent = func(r io.Reader, limit int) ([]byte, error) {
				reads++
				data, err := fileio.ReadAll(r, limit)
				if mode == "slow-io" {
					ticks.Add(int64(staleRefreshAdmission))
				}
				if mode == "canceled-during" {
					cancel()
				}
				return data, err
			}
			_, err := s.SearchWithFallbackContext(ctx, "refreshmarker", 1, SearchOptions{})
			switch mode {
			case "metadata-latency":
				require.NoError(t, err)
				assert.Equal(t, 3, reads)
				assert.Equal(t, 3, checkedCount(t, s))
			case "slow-io":
				require.NoError(t, err)
				assert.Equal(t, 1, reads)
				assert.Equal(t, 1, checkedCount(t, s))
			case "canceled-before":
				require.ErrorIs(t, err, context.Canceled)
				assert.Zero(t, reads)
				assert.Zero(t, checkedCount(t, s))
			case "canceled-during":
				require.ErrorIs(t, err, context.Canceled)
				assert.Equal(t, 1, reads)
				assert.Zero(t, checkedCount(t, s))
			}
		})
	}
}

func TestStaleScheduleOneActivePass(t *testing.T) {
	dir := t.TempDir()
	s := scheduleStore(t, filepath.Join(dir, "test.db"), 0)
	seedRefreshWorkload(t, s, dir, 1, 1)
	var ticks atomic.Int64
	ticks.Store(time.Unix(1700000000, 0).UnixNano())
	s.refreshNow = func() time.Time { return time.Unix(0, ticks.Load()) }
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan int, 1)
	s.readFileContent = func(r io.Reader, limit int) ([]byte, error) {
		close(entered)
		select {
		case <-release:
		case <-t.Context().Done():
			return nil, t.Context().Err()
		}
		return fileio.ReadAll(r, limit)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	go func() { done <- s.refreshStaleSources() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach reader")
	}
	ticks.Add(int64(2 * staleRefreshCooldown))
	assert.Zero(t, s.refreshStaleSources(), "cooldown expiry must not admit overlapping I/O")
	once.Do(func() { close(release) })
	select {
	case n := <-done:
		assert.Equal(t, 1, n)
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not finish")
	}
}

func TestStaleScheduleWriteFailureStopsPass(t *testing.T) {
	for _, mode := range []string{"write-error", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			s := scheduleStore(t, filepath.Join(dir, "test.db"), 0)
			seedRefreshWorkload(t, s, dir, 3, 0)
			db, err := s.getDB()
			require.NoError(t, err)
			if mode == "write-error" {
				_, err = db.Exec(`CREATE TRIGGER reject_check BEFORE UPDATE OF file_check_seq ON sources BEGIN SELECT RAISE(ABORT, 'injected scheduling failure'); END`)
			} else {
				_, err = db.Exec(`UPDATE sources SET file_check_seq = ? WHERE label = 'fixture-00002'`, int64(math.MaxInt64))
			}
			require.NoError(t, err)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			attempts := 0
			s.SetDenyChecker(func(string) bool { attempts++; return false })
			forceRefresh(s)
			assert.Equal(t, 1, attempts)
			assert.Zero(t, sourceSequence(t, s, "fixture-00000"))
			assert.Zero(t, sourceSequence(t, s, "fixture-00001"))
			assert.Contains(t, logs.String(), "scheduling failed, stopping pass")
		})
	}
}

func TestStaleScheduleDeniedAndMissingProgress(t *testing.T) {
	dir := t.TempDir()
	s := scheduleStore(t, filepath.Join(dir, "test.db"), 0)
	denied := writeAndIndexFile(t, s, dir, "denied.txt", "denied cached content")
	missing := writeAndIndexFile(t, s, dir, "missing.txt", "missing cached content")
	require.NoError(t, os.Remove(missing))
	s.SetDenyChecker(func(path string) bool { return path == denied })
	s.readFileContent = func(io.Reader, int) ([]byte, error) {
		t.Error("denied/missing file reached content reader")
		return nil, syscall.EIO
	}
	forceRefresh(s)
	assert.Equal(t, 2, checkedCount(t, s))
	assert.Contains(t, sourceContent(t, s, "src:denied.txt"), "denied cached content")
	assert.Contains(t, sourceContent(t, s, "src:missing.txt"), "missing cached content")
}

func TestStaleScheduleInvalidReadBudget(t *testing.T) {
	dir := t.TempDir()
	s := scheduleStore(t, filepath.Join(dir, "test.db"), math.MaxInt)
	writeAndIndexFile(t, s, dir, "doc.txt", "original content")
	s.SetDenyChecker(func(string) bool { t.Error("invalid byte budget must not admit a file"); return false })
	forceRefresh(s)
	assert.Zero(t, checkedCount(t, s))
}

func TestStaleScheduleConcurrentStores(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	first, second := scheduleStore(t, path, 0), scheduleStore(t, path, 0)
	seedRefreshWorkload(t, first, dir, 2, 0)
	for n := range 2 {
		modifyFile(t, filepath.Join(dir, fmt.Sprintf("backing-%05d.txt", n)), "refreshmarker original content")
	}
	_, err := second.getDB()
	require.NoError(t, err)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var closeOnce sync.Once
	t.Cleanup(func() { closeOnce.Do(func() { close(release) }) })
	for _, s := range []*ContentStore{first, second} {
		var once sync.Once
		s.readFileContent = func(r io.Reader, limit int) ([]byte, error) {
			once.Do(func() {
				entered <- struct{}{}
				select {
				case <-release:
				case <-t.Context().Done():
				}
			})
			return fileio.ReadAll(r, limit)
		}
	}
	var wg sync.WaitGroup
	for _, s := range []*ContentStore{first, second} {
		wg.Add(1)
		go func() { defer wg.Done(); s.refreshStaleSources() }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("both stores must reach the read before release")
		}
	}
	closeOnce.Do(func() { close(release) })
	wg.Wait()
	db, err := first.getDB()
	require.NoError(t, err)
	var maxSeq, distinct int
	require.NoError(t, db.QueryRow(`SELECT MAX(file_check_seq), COUNT(DISTINCT file_check_seq) FROM sources`).Scan(&maxSeq, &distinct))
	assert.Equal(t, 4, maxSeq, "both stores' two checks must allocate independent sequences")
	assert.Equal(t, 2, distinct)
}
