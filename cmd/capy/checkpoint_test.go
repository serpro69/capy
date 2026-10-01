package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkpointSymlinkProject(t *testing.T, mode string) (project, link, realDB string) {
	t.Helper()
	project, link = newCLIProject(t)
	realDB = filepath.Join(t.TempDir(), "knowledge.db")
	writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'test.db'\nkey_file = 'db.key'\n")
	writeCLIKeyFixture(t, filepath.Join(project, "db.key"), projectFileTestKey)
	// Neither the inherited key nor credentials beside the physical DB may win.
	writeCLIKeyFixture(t, filepath.Join(filepath.Dir(realDB), "db.key"), cliTestKey)
	writeCLIKeyFixture(t, filepath.Join(filepath.Dir(realDB), ".env"), "CAPY_DB_KEY="+cliTestKey)
	target := realDB
	switch mode {
	case "relative":
		var err error
		target, err = filepath.Rel(project, realDB)
		require.NoError(t, err)
	case "chain":
		target = filepath.Join(project, "intermediate.db")
		require.NoError(t, os.Symlink(realDB, target))
	case "directory":
		require.NoError(t, os.Symlink(filepath.Dir(realDB), filepath.Join(project, "db-dir")))
		target = filepath.Join("db-dir", "knowledge.db")
	}
	require.NoError(t, os.Symlink(target, link))
	return project, link, realDB
}

func TestCheckpointSubcommand_SymlinkPendingWAL(t *testing.T) {
	for _, mode := range []string{"absolute", "relative", "chain", "directory"} {
		t.Run(mode, func(t *testing.T) {
			project, link, realDB := checkpointSymlinkProject(t, mode)
			fixtureDB := filepath.Join(t.TempDir(), "fixture.db")
			st := seedKeyResolutionDB(t, fixtureDB, project)
			mainBefore, err := os.ReadFile(fixtureDB)
			require.NoError(t, err)
			_, err = st.Index("# Pending\n\nOnly present in WAL before checkpoint.", "pending-marker", "", store.KindDurable)
			require.NoError(t, err)

			// Snapshot a quiescent writer's main file and WAL before closing it.
			// The copied DB has pending committed data and no open connections,
			// reproducing an unclean shutdown without timing or process races.
			for _, suffix := range []string{"", "-wal", "-shm"} {
				require.NoError(t, copyFile(fixtureDB+suffix, realDB+suffix))
			}
			copiedMain, err := os.ReadFile(realDB)
			require.NoError(t, err)
			require.Equal(t, mainBefore, copiedMain, "new data must still be confined to WAL")
			wal, err := os.Stat(realDB + "-wal")
			require.NoError(t, err)
			require.Positive(t, wal.Size())
			require.NoError(t, st.Close())

			stdout, stderr, code := capy(t, "checkpoint", "--project-dir", project)
			require.Zero(t, code, stderr)
			assert.Contains(t, stdout, "safe to commit")
			for _, suffix := range []string{"-wal", "-shm"} {
				_, err := os.Stat(realDB + suffix)
				assert.True(t, os.IsNotExist(err), "target sidecar must be removed: %s (%v)", suffix, err)
				assert.NoFileExists(t, link+suffix, "no sidecar symlink workaround needed")
			}
			info, err := os.Lstat(link)
			require.NoError(t, err)
			assert.NotZero(t, info.Mode()&os.ModeSymlink)

			// Read a copy of ONLY the main file: this proves WAL data was merged,
			// rather than merely remaining readable through the original sidecars.
			committedDB := filepath.Join(t.TempDir(), "committed.db")
			require.NoError(t, copyFile(realDB, committedDB))
			committed := store.NewContentStore(committedDB, project, 0, 0, store.WithEncryptionKey(projectFileTestKey, "synthetic fixture"))
			t.Cleanup(func() { assert.NoError(t, committed.Close()) })
			sources, err := committed.ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 2)
			assert.ElementsMatch(t, []string{"project-B-marker", "pending-marker"}, []string{sources[0].Label, sources[1].Label})
		})
	}
}

func TestCheckpointSubcommand_SymlinkOpenConnection(t *testing.T) {
	for _, mode := range []string{"idle", "busy_reader"} {
		t.Run(mode, func(t *testing.T) {
			project, _, realDB := checkpointSymlinkProject(t, "relative")
			st := seedKeyResolutionDB(t, realDB, project)
			db, err := sql.Open("sqlite3", store.EncryptedDSN(realDB, projectFileTestKey)+"&_journal_mode=WAL")
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, db.Close()) })
			if mode == "busy_reader" {
				tx, err := db.Begin()
				require.NoError(t, err)
				defer tx.Rollback()
				var count int
				require.NoError(t, tx.QueryRow("SELECT count(*) FROM sources").Scan(&count))
				_, err = st.Index("# Pending\n\nHeld behind a reader.", "pending-marker", "", store.KindDurable)
				require.NoError(t, err)
			} else {
				require.NoError(t, db.Ping())
			}
			stdout, stderr, code := capy(t, "checkpoint", "--project-dir", project)
			assert.NotZero(t, code, "open target connections must block commit safety")
			assert.NotContains(t, stdout, "safe to commit")
			assert.Contains(t, stderr, "checkpoint incomplete")
			if mode == "idle" {
				resolved, err := filepath.EvalSymlinks(realDB)
				require.NoError(t, err)
				assert.Contains(t, stderr, resolved+"-shm")
			} else {
				assert.Contains(t, stderr, "pages busy")
				wal, err := os.Stat(realDB + "-wal")
				require.NoError(t, err)
				assert.Positive(t, wal.Size(), "busy checkpoint must retain pending data")
			}
		})
	}
}

func TestCheckpointSubcommand_SymlinkMissingAndLoop(t *testing.T) {
	for _, mode := range []string{"dangling", "loop"} {
		t.Run(mode, func(t *testing.T) {
			project, link := newCLIProject(t)
			t.Setenv("CAPY_DB_KEY", "")
			target := filepath.Join(project, "absent", "knowledge.db")
			if mode == "loop" {
				target = link
			}
			require.NoError(t, os.Symlink(target, link))
			stdout, stderr, code := capy(t, "checkpoint", "--project-dir", project)
			if mode == "dangling" {
				assert.Zero(t, code, stderr)
				assert.Contains(t, stdout, "no knowledge base")
			} else {
				assert.NotZero(t, code)
				assert.Contains(t, stderr, "accessing knowledge database")
			}
			assert.NotContains(t, stdout, "safe to commit")
			assert.NoDirExists(t, filepath.Join(project, "absent"))
		})
	}
}

func TestVerifyCheckpointSidecars(t *testing.T) {
	for _, tc := range []struct {
		name      string
		suffix    string
		content   string
		wantError string
	}{
		{name: "absent"},
		{name: "empty_wal", suffix: "-wal"},
		{name: "pending_wal", suffix: "-wal", content: "pending data", wantError: "checkpoint incomplete"},
		{name: "empty_shm", suffix: "-shm", wantError: "checkpoint incomplete"},
		{name: "nonempty_shm", suffix: "-shm", content: "shared memory", wantError: "checkpoint incomplete"},
		{name: "directory", suffix: "-wal", wantError: "checkpoint incomplete"},
		{name: "stat_error", suffix: "-shm", wantError: "checking checkpoint sidecar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "knowledge.db")
			path := dbPath + tc.suffix
			switch tc.name {
			case "absent":
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "stat_error":
				require.NoError(t, os.Symlink(path, path))
			default:
				require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			}
			err := verifyCheckpointSidecars(dbPath)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
				assert.Contains(t, err.Error(), path)
				_, statErr := os.Lstat(path)
				assert.NoError(t, statErr, "verification must not remove sidecars")
			}
		})
	}
}
