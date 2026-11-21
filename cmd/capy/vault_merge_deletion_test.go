package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/require"
)

func TestVaultDeletionSurvivesStartupSweep(t *testing.T) {
	const knowledgeKey = "synthetic-deletion-workflow-knowledge-key"
	const deleted = "abcd1234-aaaa-bbbb-cccc-1234567890ab"
	const neighbor = "12345678-aaaa-bbbb-cccc-1234567890ab"
	bin := buildStdioCapy(t)
	sourceProject, peerProject := t.TempDir(), t.TempDir()
	// Serve resolves its project to a physical path before finding Claude's session directory.
	peerProject, err := filepath.EvalSymlinks(peerProject)
	require.NoError(t, err)
	sourceVault := filepath.Join(sourceProject, "source.db")
	require.NoError(t, os.WriteFile(filepath.Join(peerProject, ".capy.toml"),
		[]byte("[vault]\nremember_deletions = false\n"), 0o600))

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	sessions, err := vault.ProjectSessionDir(peerProject)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	writeTranscript := func(id, message string) {
		t.Helper()
		line := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`, message)
		require.NoError(t, os.WriteFile(filepath.Join(sessions, id+".jsonl"), []byte(line+"\n"), 0o600))
	}
	writeTranscript(deleted, "Archive this session before deleting it.")
	env := replaceWrapperEnv(stdioEnv(t, knowledgeKey), "CAPY_VAULT_KEY", testVaultKey)
	env = replaceWrapperEnv(env, "CLAUDE_CONFIG_DIR", os.Getenv("CLAUDE_CONFIG_DIR"))
	run := func(project string, args ...string) string {
		t.Helper()
		out, err := runWrapperCommand(t, stdioCommand{executable: bin, dir: project, env: env,
			args:    append([]string{"vault", "--project-dir", project}, args...),
			secrets: []string{knowledgeKey, testVaultKey}})
		require.NoError(t, err, out)
		return out
	}

	require.Contains(t, run(sourceProject, "import", "--path", sourceVault, "--source", sessions), "imported 1")
	require.Contains(t, run(peerProject, "merge", "--from", sourceVault), "imported 1")
	require.Contains(t, run(peerProject, "delete", deleted), "One-time local deletion")
	require.Contains(t, run(sourceProject, "delete", deleted, "--path", sourceVault, "--yes"), "cannot undo")
	require.Contains(t, run(peerProject, "merge", "--from", sourceVault),
		deleted+"  archives removed 1, deletion pairs added 1, already present 0")

	// Leave the original transcript on disk and add a candidate only the sweep can admit.
	writeTranscript(neighbor, "Archive this independent session at startup.")
	preview := run(peerProject, "import", "--source", sessions, "--dry-run")
	require.Contains(t, preview, deleted+"  excluded")
	require.Contains(t, preview, "imported 1, updated 0, skipped 0, excluded 1, errors 0")
	server := startStdioProcess(t, stdioCommand{executable: bin, dir: peerProject, env: env,
		args: []string{"serve", "--project-dir", peerProject}, secrets: []string{knowledgeKey, testVaultKey}})
	server.initialize(t)
	require.Eventually(t, func() bool {
		return strings.Contains(server.diagnostics(), "vault sweep platform=claude-code")
	}, stdioTimeout, 10*time.Millisecond, "waiting for the startup sweep")
	server.close(t)
	require.Contains(t, server.diagnostics(), "imported=1 updated=0 skipped=0 excluded=1 errors=0")
	list := run(peerProject, "list", "--json")
	require.Contains(t, list, neighbor)
	require.NotContains(t, list, deleted)
}

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
	const penultimate = "ffff1111-0000-0000-0000-000000000049"
	const last = "ffff1111-0000-0000-0000-000000000050"
	// Failing the last insert must roll back both UUIDs in the second transaction.
	for i := 1; i <= 50; i++ {
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
	require.Contains(t, preview, "total archives removed 1, deletion pairs added 53, already present 0")
	require.Contains(t, preview, id+"  archives removed 1, deletion pairs added 2")
	require.Contains(t, preview, neighbor+"  archives removed 0, deletion pairs added 1")
	out, stderr, code := run()
	require.NotZero(t, code)
	require.Contains(t, stderr, "second batch failed")
	require.Contains(t, stderr, "unprocessed deletion UUIDs "+penultimate+", "+last)
	require.Contains(t, out, "total archives removed 1, deletion pairs added 51, already present 0")
	require.NotContains(t, out, penultimate)
	require.NotContains(t, out, last)
	_, err = dest.Exec("DROP TRIGGER fail_last")
	require.NoError(t, err)
	out, stderr, code = run()
	require.Zero(t, code, stderr)
	require.Contains(t, out, "total archives removed 0, deletion pairs added 2, already present 51")
	out, stderr, code = run()
	require.Zero(t, code, stderr)
	require.Contains(t, out, "total archives removed 0, deletion pairs added 0, already present 53")
	require.NotContains(t, out, "no sessions matched")
}
