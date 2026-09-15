package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEncryptedDB writes a real encrypted knowledge DB at dir/knowledge.db (using
// CAPY_DB_KEY) with one indexed row and no lingering WAL/SHM sidecars, then
// returns its path.
func newEncryptedDB(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, "knowledge.db")
	st := store.NewContentStore(dbPath, dir, 0, 0)
	_, err := st.Index("# Auth\n\nJWT validation middleware.", "auth-doc", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	return dbPath
}

// TestResolveDBSymlink_SwapPreservesSymlink is the regression guard for issue
// #90: `capy encrypt` on a knowledge.db kept in a separate DB repo and symlinked
// into the project's .capy/ must rename the REAL file, leaving the symlink
// intact. Without resolveDBSymlink, SwapAndVerify renames the link itself,
// replacing it with a regular file and stranding the DB-repo copy.
func TestResolveDBSymlink_SwapPreservesSymlink(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", cliTestKey)

	root := t.TempDir()
	realDir := filepath.Join(root, "db-repo", "proj")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	realDBPath := newEncryptedDB(t, realDir)

	projCapy := filepath.Join(root, "project", ".capy")
	require.NoError(t, os.MkdirAll(projCapy, 0o755))
	symlinkPath := filepath.Join(projCapy, "knowledge.db")
	require.NoError(t, os.Symlink(realDBPath, symlinkPath))

	// resolveDBSymlink must return the real file, not the link, so the swap acts
	// on the target. EvalSymlinks canonicalizes both sides (temp dirs may hide
	// behind their own links), so compare against the resolved real path.
	wantResolved, err := filepath.EvalSymlinks(realDBPath)
	require.NoError(t, err)
	resolved := resolveDBSymlink(symlinkPath)
	require.Equal(t, wantResolved, resolved, "resolveDBSymlink should return the symlink target")

	// Drive the swap the way runEncrypt does: onto the resolved path with a valid
	// encrypted replacement. A copy of the DB opens under the same key.
	tmpPath := resolved + ".enc.tmp"
	require.NoError(t, copyFile(resolved, tmpPath))
	bakPath, err := sqliteutil.SwapAndVerify(resolved, tmpPath, cliTestKey)
	require.NoError(t, err)
	assert.FileExists(t, bakPath)

	// The link must survive as a link and still point at the real file, which now
	// holds the freshly swapped encrypted DB.
	info, err := os.Lstat(symlinkPath)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "expected %s to still be a symlink", symlinkPath)

	target, err := os.Readlink(symlinkPath)
	require.NoError(t, err)
	assert.Equal(t, realDBPath, target, "symlink should still point at the real DB file")
	assert.FileExists(t, realDBPath)
}
