package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEncryptionLifecycle exercises the full encryption lifecycle through the
// public ContentStore API: create → index → close → reopen → search → re-key
// → verify old key fails → verify new key works → checkpoint.
func TestEncryptionLifecycle(t *testing.T) {
	const key1 = "test-lifecycle-key-must-be-at-least-32-chars!!"
	const key2 = "rotated-key-also-must-be-at-least-32-chars!!"
	const wrongKey = "wrong-key-that-should-never-work-at-all!!"

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "lifecycle.db")

	// Phase 1: Create encrypted DB, index content, close.
	t.Setenv(encryptionKeyEnv, key1)
	s := NewContentStore(dbPath, dir, 0)

	_, err := s.Index("# Encryption Test\n\nThe quick brown fox jumps over the lazy dog.", "lifecycle-doc", "", KindDurable)
	require.NoError(t, err)

	_, err = s.Index("# Another Document\n\nAuthentication tokens must be rotated regularly.", "lifecycle-auth", "", KindDurable)
	require.NoError(t, err)

	require.NoError(t, s.Close())

	// Verify the file is actually encrypted (no plaintext SQLite header).
	raw, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.True(t, len(raw) >= 15, "DB file too small")
	assert.NotEqual(t, "SQLite format 3", string(raw[:15]), "DB should be encrypted, not plaintext")

	// Phase 2: Reopen with correct key, verify content is searchable.
	t.Setenv(encryptionKeyEnv, key1)
	s2 := NewContentStore(dbPath, dir, 0)
	defer s2.Close()

	results, err := s2.SearchWithFallback("quick brown fox", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "should find indexed content after reopen")
	assert.Contains(t, results[0].Content, "quick brown fox")

	results, err = s2.SearchWithFallback("authentication tokens", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "should find second document after reopen")

	require.NoError(t, s2.Close())

	// Phase 3: Wrong key fails cleanly.
	t.Setenv(encryptionKeyEnv, wrongKey)
	s3 := NewContentStore(dbPath, dir, 0)
	_, err = s3.SearchWithFallback("anything", 5, SearchOptions{})
	require.Error(t, err, "wrong key should produce an error")
	assert.True(t, isWrongPassphrase(err), "error should be errWrongPassphrase, got: %v", err)
	s3.Close()

	// Phase 4: Re-key using backup API (same mechanism as capy encrypt re-key).
	tmpPath := dbPath + ".enc.tmp"
	srcDB, err := openEncryptedDB(dbPath, key1)
	require.NoError(t, err)

	destDB, err := openEncryptedDB(tmpPath, key2)
	require.NoError(t, err)

	err = backupViaAPI(destDB, srcDB)
	require.NoError(t, err)

	destDB.Close()
	srcDB.Close()

	bakPath := dbPath + ".bak"
	require.NoError(t, os.Rename(dbPath, bakPath))
	require.NoError(t, os.Rename(tmpPath, dbPath))

	// Phase 5: Old key fails on re-keyed DB.
	t.Setenv(encryptionKeyEnv, key1)
	s4 := NewContentStore(dbPath, dir, 0)
	_, err = s4.SearchWithFallback("anything", 5, SearchOptions{})
	require.Error(t, err, "old key should fail after re-key")
	assert.True(t, isWrongPassphrase(err), "error should be errWrongPassphrase, got: %v", err)
	s4.Close()

	// Phase 6: New key works, content survived re-key.
	t.Setenv(encryptionKeyEnv, key2)
	s5 := NewContentStore(dbPath, dir, 0)
	defer s5.Close()

	results, err = s5.SearchWithFallback("quick brown fox", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "content should survive re-key")
	assert.Contains(t, results[0].Content, "quick brown fox")

	results, err = s5.SearchWithFallback("authentication tokens", 5, SearchOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, results, "second document should survive re-key")

	// Phase 7: Checkpoint works on encrypted DB.
	require.NoError(t, s5.Checkpoint())

	walPath := dbPath + "-wal"
	if info, err := os.Stat(walPath); err == nil {
		assert.Equal(t, int64(0), info.Size(), "WAL should be empty after checkpoint")
	}
}

func openEncryptedDB(dbPath, key string) (*sql.DB, error) {
	dsn := EncryptedDSN(dbPath, key) + "&_busy_timeout=5000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening encrypted database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("SELECT count(*) FROM sqlite_master"); err != nil {
		db.Close()
		return nil, fmt.Errorf("wrong passphrase or corrupted database: %w", err)
	}
	return db, nil
}

func backupViaAPI(destDB, srcDB *sql.DB) error {
	destConn, err := destDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting dest connection: %w", err)
	}
	defer destConn.Close()

	srcConn, err := srcDB.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("getting src connection: %w", err)
	}
	defer srcConn.Close()

	return destConn.Raw(func(destRaw any) error {
		return srcConn.Raw(func(srcRaw any) error {
			destSC := destRaw.(*sqlite3.SQLiteConn)
			srcSC := srcRaw.(*sqlite3.SQLiteConn)

			backup, err := destSC.Backup("main", srcSC, "main")
			if err != nil {
				return fmt.Errorf("starting backup: %w", err)
			}
			_, err = backup.Step(-1)
			finishErr := backup.Finish()
			if err != nil {
				return fmt.Errorf("backup step: %w", err)
			}
			if finishErr != nil {
				return fmt.Errorf("backup finish: %w", finishErr)
			}
			return nil
		})
	})
}
