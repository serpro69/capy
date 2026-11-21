package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/require"
)

func markerFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	st := vault.NewVaultStore(path)
	require.NoError(t, st.Open(t.Context()))
	require.NoError(t, st.Close())
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, testVaultKey))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestVaultMergeReportsMarkerOnlyAndPartialCommits(t *testing.T) {
	root, id := setupVaultEnv(t)
	const neighbor = "abcd1234-bbbb-cccc-dddd-1234567890ab"
	destPath := os.Getenv("CAPY_VAULT_PATH")
	srcPath := filepath.Join(t.TempDir(), "src.db")
	src := markerFixtureDB(t, srcPath)
	tx, err := src.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO vault_session_deletions VALUES (?, 'alpha'), (?, 'beta'), (?, 'alpha')", id, id, neighbor)
	require.NoError(t, err)
	const last = "ffff1111-0000-0000-0000-000000000049"
	// 51 UUIDs total. The last UUID starts a second deletion transaction.
	for i := 1; i <= 49; i++ {
		_, err = tx.Exec("INSERT INTO vault_session_deletions VALUES (?, 'extra')", fmt.Sprintf("ffff1111-0000-0000-0000-%012d", i))
		require.NoError(t, err)
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO vault_meta VALUES ('min_reader_version','4')")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	_, stderr, code := capy(t, "vault", "import", "--source", root)
	require.Zero(t, code, stderr)
	dest := markerFixtureDB(t, destPath)
	_, err = dest.Exec("CREATE TRIGGER fail_last BEFORE INSERT ON vault_session_deletions WHEN NEW.session_uuid='" + last + "' BEGIN SELECT RAISE(ABORT,'second batch failed'); END")
	require.NoError(t, err)

	run := func(args ...string) (string, string, int) {
		return capy(t, append([]string{"vault", "merge", "--from", srcPath}, args...)...)
	}
	preview, stderr, code := run("--dry-run")
	require.Zero(t, code, stderr)
	require.Contains(t, preview, "total archives removed 1, deletion pairs added 52, already present 0")
	require.Contains(t, preview, id+"  archives removed 1, deletion pairs added 2")
	require.Contains(t, preview, neighbor+"  archives removed 0, deletion pairs added 1")
	out, stderr, code := run()
	require.NotZero(t, code)
	require.Contains(t, stderr, "second batch failed")
	require.Contains(t, stderr, last)
	require.Contains(t, out, "total archives removed 1, deletion pairs added 51, already present 0")
	require.NotContains(t, out, last)
	_, err = dest.Exec("DROP TRIGGER fail_last")
	require.NoError(t, err)
	out, stderr, code = run()
	require.Zero(t, code, stderr)
	require.Contains(t, out, "total archives removed 0, deletion pairs added 1, already present 51")
	out, stderr, code = run()
	require.Zero(t, code, stderr)
	require.Contains(t, out, "total archives removed 0, deletion pairs added 0, already present 52")
	require.NotContains(t, out, "no sessions matched")
}
