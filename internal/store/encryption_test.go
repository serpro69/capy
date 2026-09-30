package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireEncryptionKey_Empty(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "")
	_, err := RequireEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), encryptionKeyEnv)
	assert.Contains(t, err.Error(), "required")
}

func TestRequireEncryptionKey_Short(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "short-key")
	key, err := RequireEncryptionKey()
	require.NoError(t, err)
	assert.Equal(t, "short-key", key)
}

func TestRequireEncryptionKey_Valid(t *testing.T) {
	long := "this-is-a-passphrase-that-is-at-least-32-characters"
	t.Setenv(encryptionKeyEnv, long)
	key, err := RequireEncryptionKey()
	require.NoError(t, err)
	assert.Equal(t, long, key)
}

func TestEncryptionKeyFromEnv_Unset(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "")
	assert.Equal(t, "", EncryptionKeyFromEnv())
}

func TestEncryptionKeyFromEnv_Set(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "my-key")
	assert.Equal(t, "my-key", EncryptionKeyFromEnv())
}

func TestEncryptedDSN(t *testing.T) {
	dsn := EncryptedDSN("/tmp/test.db", "my passphrase")
	assert.Equal(t, "file:/tmp/test.db?cipher=sqlcipher&legacy=4&key=my%20passphrase", dsn)
}

func TestEncryptedDSN_SpecialChars(t *testing.T) {
	dsn := EncryptedDSN("/tmp/test.db", "pass'phrase&with=special+chars")
	assert.Equal(t,
		"file:/tmp/test.db?cipher=sqlcipher&legacy=4&key=pass%27phrase%26with%3Dspecial%2Bchars",
		dsn)
}

func TestEncryptedDSN_PathWithSpecialChars(t *testing.T) {
	dsn := EncryptedDSN("/tmp/path with spaces/test#1.db", "key")
	assert.Equal(t,
		"file:/tmp/path with spaces/test%231.db?cipher=sqlcipher&legacy=4&key=key",
		dsn)

	dsn2 := EncryptedDSN("/tmp/path?query/test.db", "key")
	assert.Equal(t,
		"file:/tmp/path%3Fquery/test.db?cipher=sqlcipher&legacy=4&key=key",
		dsn2)
}

func TestEscapeSQLString(t *testing.T) {
	assert.Equal(t, "no quotes", EscapeSQLString("no quotes"))
	assert.Equal(t, "it''s escaped", EscapeSQLString("it's escaped"))
	assert.Equal(t, "double''''quote", EscapeSQLString("double''quote"))
}

func TestOpenDB_UnencryptedDB_ClearError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "unencrypted.db")

	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	db.Close()

	t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
	s := NewContentStore(dbPath, dir, 0, 0)
	defer s.Close()

	_, err = s.SearchWithFallback("anything", 5, SearchOptions{})
	require.Error(t, err)

	var unencErr *sqliteutil.UnencryptedDBError
	assert.ErrorAs(t, err, &unencErr, "should be UnencryptedDBError, got: %v", err)
	assert.Contains(t, err.Error(), "not encrypted")
	assert.Contains(t, err.Error(), "capy encrypt")
}

func TestValidateEncryptionReady(t *testing.T) {
	dir := t.TempDir()

	t.Run("no_key", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "")
		err := ValidateEncryptionReady(filepath.Join(dir, "any.db"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), encryptionKeyEnv)
	})

	t.Run("key_set_no_db", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
		err := ValidateEncryptionReady(filepath.Join(dir, "nonexistent.db"))
		assert.NoError(t, err)
	})

	t.Run("key_set_encrypted_db", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
		dbPath := filepath.Join(dir, "encrypted.db")
		db, err := sql.Open("sqlite3", EncryptedDSN(dbPath, "test-passphrase-at-least-32-characters!!"))
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()

		assert.NoError(t, ValidateEncryptionReady(dbPath))
	})

	t.Run("key_set_unencrypted_db", func(t *testing.T) {
		t.Setenv(encryptionKeyEnv, "test-passphrase-at-least-32-characters!!")
		dbPath := filepath.Join(dir, "plain.db")
		db, err := sql.Open("sqlite3", dbPath)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t (id INTEGER)")
		require.NoError(t, err)
		db.Close()

		err = ValidateEncryptionReady(dbPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not encrypted")
	})
}

func TestValidateEncryptionReadyWithKey(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "missing", "knowledge.db")
	source := "key file /project/.capy/db.key"

	// Preflight neither needs an inherited key nor creates any database files.
	require.NoError(t, ValidateEncryptionReadyWithKey(dbPath, testEncryptionKey, source))
	require.NoError(t, ValidateEncryptionReadyWithKey(dbPath, "short-key", source))
	_, err := os.Stat(filepath.Dir(dbPath))
	require.True(t, os.IsNotExist(err), "preflight created a database directory")

	t.Setenv(encryptionKeyEnv, testEncryptionKey)
	err = ValidateEncryptionReadyWithKey(dbPath, "", source)
	require.Error(t, err)
	assert.Contains(t, err.Error(), source)
	assert.NotContains(t, err.Error(), testEncryptionKey)
	assert.NotContains(t, err.Error(), "CAPY_DB_KEY")

	plainPath := filepath.Join(dir, "plain.db")
	db, err := sql.Open("sqlite3", plainPath)
	require.NoError(t, err)
	_, err = db.Exec("CREATE TABLE t (id INTEGER)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	err = ValidateEncryptionReadyWithKey(plainPath, testEncryptionKey, source)
	var plainErr *sqliteutil.UnencryptedDBError
	require.ErrorAs(t, err, &plainErr)
}

func TestStoreEncryption_ExplicitKeysConcurrent(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "unrelated-inherited-passphrase-for-these-projects")
	keys := []string{"project-a-synthetic-passphrase-at-least-32", "project-b-synthetic-passphrase-at-least-32"}
	labels := []string{"project-a-marker", "project-b-marker"}
	stores := make([]*ContentStore, len(keys))
	paths := make([]string, len(keys))
	results := make(chan error, len(keys))
	for i, key := range keys {
		dir := t.TempDir()
		paths[i] = filepath.Join(dir, "knowledge.db")
		st := NewContentStore(paths[i], dir, 0, 0, WithEncryptionKey(key, "explicit project credential"))
		stores[i] = st
		t.Cleanup(func() { assert.NoError(t, st.Close()) })
		go func() {
			_, err := st.Index("# Project marker\n\n"+labels[i], labels[i], "", KindDurable)
			results <- err
		}()
	}
	// Drain both results before any fatal assertion so every worker has exited.
	var indexErrors []error
	for range stores {
		indexErrors = append(indexErrors, <-results)
	}
	for _, err := range indexErrors {
		require.NoError(t, err)
	}
	for i, st := range stores {
		sources, err := st.ListSources()
		require.NoError(t, err)
		require.Len(t, sources, 1)
		assert.Equal(t, labels[i], sources[0].Label)
		require.NoError(t, st.Close())

		reopened := NewContentStore(paths[i], "", 0, 0, WithEncryptionKey(keys[i], "explicit project credential"))
		t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
		sources, err = reopened.ListSources()
		require.NoError(t, err)
		require.Len(t, sources, 1)
		assert.Equal(t, labels[i], sources[0].Label)
		require.NoError(t, reopened.Close())

		crossed := NewContentStore(paths[i], "", 0, 0, WithEncryptionKey(keys[1-i], "other project's credential"))
		t.Cleanup(func() { assert.NoError(t, crossed.Close()) })
		_, err = crossed.ListSources()
		require.True(t, sqliteutil.IsWrongPassphrase(err), "crossed key must not read the database")
	}
}

func TestStoreEncryption_EmptyKeyHasNoSideEffects(t *testing.T) {
	operations := []struct {
		name string
		run  func(*ContentStore) error
	}{
		{"read", func(s *ContentStore) error { _, err := s.ListSources(); return err }},
		{"checkpoint", (*ContentStore).Checkpoint},
		{"vacuum", (*ContentStore).Vacuum},
		{"rebuild", (*ContentStore).RebuildFTS},
	}
	for _, explicit := range []bool{false, true} {
		name := "environment_snapshot"
		if explicit {
			name = "explicit_empty"
		}
		t.Run(name, func(t *testing.T) {
			for _, op := range operations {
				t.Run(op.name, func(t *testing.T) {
					dir := t.TempDir()
					dbDir := filepath.Join(dir, "missing")
					dbPath := filepath.Join(dbDir, "knowledge.db")
					var opts []Option
					t.Setenv(encryptionKeyEnv, "")
					if explicit {
						t.Setenv(encryptionKeyEnv, testEncryptionKey)
						opts = append(opts, WithEncryptionKey("", "explicit project credential"))
					}
					st := NewContentStore(dbPath, dir, 0, 0, opts...)
					// A later nonempty environment cannot rescue a captured empty key.
					t.Setenv(encryptionKeyEnv, testEncryptionKey)
					err := op.run(st)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "required")
					assert.NotContains(t, err.Error(), testEncryptionKey)
					require.NoError(t, st.Close())
					for _, path := range []string{dbDir, filepath.Join(dbDir, ".project"), dbPath, dbPath + "-wal", dbPath + "-shm"} {
						_, err := os.Stat(path)
						assert.True(t, os.IsNotExist(err), "empty key must leave %s absent", path)
					}
				})
			}
		})
	}
}

func TestStoreEncryption_KeyLifetime(t *testing.T) {
	for _, mode := range []string{"environment_snapshot", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "knowledge.db")
			t.Setenv(encryptionKeyEnv, testEncryptionKey)
			var opts []Option
			if mode == "explicit" {
				opts = append(opts, WithEncryptionKey(testEncryptionKey, "key file /project/.capy/db.key"))
			}
			st := NewContentStore(dbPath, dir, 0, 0, opts...)
			t.Cleanup(func() { assert.NoError(t, st.Close()) })
			t.Setenv(encryptionKeyEnv, "different-synthetic-key-before-lazy-open")
			_, err := st.Index("# Preserved\n\nOrchard content survives maintenance.", "preserved-marker", "", KindDurable)
			require.NoError(t, err)
			t.Setenv(encryptionKeyEnv, "")
			require.NoError(t, st.RebuildFTS())
			require.NoError(t, st.Vacuum())
			require.NoError(t, st.Checkpoint())
			require.NoError(t, st.Close())
			for _, suffix := range []string{"-wal", "-shm"} {
				info, err := os.Stat(dbPath + suffix)
				if !os.IsNotExist(err) {
					require.NoError(t, err)
					assert.Zero(t, info.Size(), "sidecar %s must be empty after close", suffix)
				}
			}
			// Reuse the same object after Close, still without an environment key.
			results, err := st.SearchWithFallback("orchard", 5, SearchOptions{})
			require.NoError(t, err)
			require.Len(t, results, 1)
			assert.Contains(t, results[0].Content, "Orchard content survives maintenance.")
			require.NoError(t, st.Close())
			require.NoError(t, st.Checkpoint(), "standalone checkpoint must use the captured key")

			wrongKey := "different-synthetic-key-for-new-store"
			t.Setenv(encryptionKeyEnv, wrongKey)
			newStore := NewContentStore(dbPath, dir, 0, 0)
			t.Cleanup(func() { assert.NoError(t, newStore.Close()) })
			_, err = newStore.ListSources()
			require.True(t, sqliteutil.IsWrongPassphrase(err), "new environment must not open the old database")
			assert.NotContains(t, err.Error(), wrongKey)
			assert.NotContains(t, err.Error(), testEncryptionKey)
			assert.NotContains(t, err.Error(), "cipher=")
			backups, err := filepath.Glob(dbPath + ".corrupt.*")
			require.NoError(t, err)
			assert.Empty(t, backups, "wrong credentials must not trigger recovery")
			results, err = st.SearchWithFallback("orchard", 5, SearchOptions{})
			require.NoError(t, err)
			require.Len(t, results, 1, "wrong-key attempt must preserve indexed content")
		})
	}
}

func TestStoreEncryption_RecoveryUsesCapturedKey(t *testing.T) {
	t.Setenv(encryptionKeyEnv, "unrelated-synthetic-process-passphrase")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "knowledge.db")
	garbage := []byte("not a sqlite database")
	require.NoError(t, os.WriteFile(dbPath, garbage, 0o600))
	st := NewContentStore(dbPath, dir, 0, 0, WithEncryptionKey(testEncryptionKey, "explicit recovery credential"))
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	_, err := st.Index("Recovered orchard content.", "recovery-marker", "", KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	backups, err := filepath.Glob(dbPath + ".corrupt.*")
	require.NoError(t, err)
	require.Len(t, backups, 1)
	backup, err := os.ReadFile(backups[0])
	require.NoError(t, err)
	assert.Equal(t, garbage, backup)

	source := "key file /project/.capy/db.key"
	wrong := NewContentStore(dbPath, dir, 0, 0, WithEncryptionKey("wrong-synthetic-passphrase-for-recovery", source))
	t.Cleanup(func() { assert.NoError(t, wrong.Close()) })
	_, err = wrong.ListSources()
	require.True(t, sqliteutil.IsWrongPassphrase(err))
	assert.Contains(t, err.Error(), source)
	assert.NotContains(t, err.Error(), "CAPY_DB_KEY")
	assert.NotContains(t, err.Error(), "wrong-synthetic-passphrase-for-recovery")
	assert.NotContains(t, err.Error(), "cipher=")

	reopened := NewContentStore(dbPath, dir, 0, 0, WithEncryptionKey(testEncryptionKey, "explicit recovery credential"))
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	results, err := reopened.SearchWithFallback("orchard", 5, SearchOptions{})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Contains(t, results[0].Content, "Recovered orchard content.")
}
