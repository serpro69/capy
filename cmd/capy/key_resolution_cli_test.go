package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const projectFileTestKey = "synthetic-project-B-key-at-least-32-characters"

func writeCLIKeyFixture(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func seedKeyResolutionDB(t *testing.T, dbPath, owner string) *store.ContentStore {
	t.Helper()
	st := store.NewContentStore(dbPath, owner, 0, 0, store.WithEncryptionKey(projectFileTestKey, "synthetic fixture"))
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	_, err := st.Index("# Project B\n\nRetained orchard content.", "project-B-marker", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	return st
}

func TestDBSizeSubcommand_KeyFileOwnership(t *testing.T) {
	for _, mode := range []string{"checkout", "linked_relative", "linked_parent_relative", "linked_absolute", "linked_xdg", "submodule", "db_symlink", "key_symlink", "absolute_key", "global_relative_key", "explicit_subdir"} {
		t.Run(mode, func(t *testing.T) {
			project, _ := newCLIProject(t) // inherited A; the file and fixture use B
			mainDir, external := t.TempDir(), t.TempDir()
			owner := project
			storePath, keyFile := ".capy/knowledge.db", ".capy/db.key"
			if mode == "linked_relative" || mode == "linked_parent_relative" || mode == "linked_absolute" || mode == "linked_xdg" {
				gitDir := filepath.Join(mainDir, ".git", "worktrees", "linked")
				writeCLIKeyFixture(t, filepath.Join(gitDir, "commondir"), "../..\n")
				writeCLIKeyFixture(t, filepath.Join(project, ".git"), "gitdir: "+gitDir+"\n")
				writeCLIKeyFixture(t, filepath.Join(mainDir, ".capy.toml"), "invalid main config must not be loaded")
				switch mode {
				case "linked_relative":
					owner = mainDir
				case "linked_parent_relative":
					owner, storePath = mainDir, "../shared/knowledge.db"
				case "linked_absolute":
					storePath = filepath.Join(external, "knowledge.db")
				case "linked_xdg":
					storePath = ""
				}
				// A conflicting credential in the non-owner cannot win.
				other := mainDir
				if owner == mainDir {
					other = project
				}
				writeCLIKeyFixture(t, filepath.Join(other, ".capy", "db.key"), cliTestKey)
			}
			if mode == "submodule" {
				writeCLIKeyFixture(t, filepath.Join(project, ".git"), "gitdir: "+filepath.Join(mainDir, ".git", "modules", "sub")+"\n")
				writeCLIKeyFixture(t, filepath.Join(mainDir, ".capy", "db.key"), cliTestKey)
			}
			if mode == "explicit_subdir" {
				require.NoError(t, os.Mkdir(filepath.Join(project, ".git"), 0o700))
				writeCLIKeyFixture(t, filepath.Join(project, ".capy", "db.key"), cliTestKey)
				project = filepath.Join(project, "subdir")
				owner = project
			}
			keyPath := filepath.Join(owner, ".capy", "db.key")
			if mode == "absolute_key" {
				keyPath = filepath.Join(external, "db.key")
				keyFile = keyPath
			}
			if mode == "key_symlink" {
				writeCLIKeyFixture(t, filepath.Join(external, "db.key"), projectFileTestKey+"\r\n")
				require.NoError(t, os.MkdirAll(filepath.Dir(keyPath), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(external, "db.key"), keyPath))
			} else {
				writeCLIKeyFixture(t, keyPath, projectFileTestKey+"\n")
			}
			cfgText := fmt.Sprintf("[store]\npath = %q\nkey_file = %q\n", storePath, keyFile)
			if mode == "global_relative_key" {
				writeCLIKeyFixture(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "capy", "config.toml"), "[store]\nkey_file = '.capy/db.key'\n")
				cfgText = fmt.Sprintf("[store]\npath = %q\n", storePath)
			}
			writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), cfgText)
			dbPath := filepath.Join(owner, storePath)
			if filepath.IsAbs(storePath) {
				dbPath = storePath
			} else if storePath == "" {
				dbPath = filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy", config.ProjectHash(project), "knowledge.db")
			}
			seedPath := dbPath
			if mode == "db_symlink" {
				seedPath = filepath.Join(external, "knowledge.db")
				writeCLIKeyFixture(t, filepath.Join(external, ".capy", "db.key"), cliTestKey)
				require.NoError(t, os.Symlink(seedPath, dbPath))
			}
			st := seedKeyResolutionDB(t, seedPath, owner)
			// capy runs from cmd/capy, unrelated to the explicitly selected project.
			stdout, stderr, code := capy(t, "dbsize", "--project-dir", project)
			require.Equal(t, 0, code, stderr)
			assert.Contains(t, stdout, "Database: "+dbPath)
			assert.Contains(t, stdout, "project-B-marker", "must read the existing B database")
			assert.NotContains(t, stdout+stderr, projectFileTestKey)
			assert.NotContains(t, stdout+stderr, cliTestKey)
			assert.NotContains(t, stdout+stderr, "cipher=")
			sources, err := st.ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Equal(t, "project-B-marker", sources[0].Label)
			assert.True(t, os.Getenv("CAPY_DB_KEY") == cliTestKey)
			if mode != "linked_xdg" {
				assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
			}
			if owner != project {
				assert.NoFileExists(t, filepath.Join(project, ".capy", "knowledge.db"))
			}
		})
	}
}

func TestDBSizeSubcommand_KeyFileFailureBeforeCreation(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "invalid", "oversized", "directory"} {
		t.Run(mode, func(t *testing.T) {
			project, _ := newCLIProject(t)
			writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'absent/knowledge.db'\nkey_file = 'db.key'\n")
			keyPath := filepath.Join(project, "db.key")
			switch mode {
			case "empty":
				writeCLIKeyFixture(t, keyPath, "")
			case "invalid":
				writeCLIKeyFixture(t, keyPath, projectFileTestKey+"\n\n")
			case "oversized":
				require.NoError(t, os.WriteFile(keyPath, make([]byte, 4097), 0o600))
			case "directory":
				require.NoError(t, os.Mkdir(keyPath, 0o700))
			}
			stdout, stderr, code := capy(t, "dbsize", "--project-dir", project)
			assert.NotZero(t, code)
			assert.Contains(t, stderr, "resolving knowledge credential")
			assert.Contains(t, stderr, keyPath)
			assert.Contains(t, stderr, filepath.Join(project, "absent", "knowledge.db"))
			assert.NotContains(t, stdout+stderr, projectFileTestKey)
			assert.NotContains(t, stdout+stderr, cliTestKey)
			assert.NotContains(t, stdout+stderr, "cipher=")
			assert.NoDirExists(t, filepath.Join(project, "absent"), "no DB, sidecars, or marker may be created")
			assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
		})
	}
}

func TestDBSizeSubcommand_WrongFileKeyPreservesData(t *testing.T) {
	project, dbPath := newCLIProject(t)
	writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'test.db'\nkey_file = 'db.key'\n")
	writeCLIKeyFixture(t, filepath.Join(project, "db.key"), cliTestKey)
	st := seedKeyResolutionDB(t, dbPath, project)
	t.Setenv("CAPY_DB_KEY", projectFileTestKey) // correct inherited key must not rescue wrong file
	stdout, stderr, code := capy(t, "dbsize", "--project-dir", project)
	assert.NotZero(t, code)
	assert.Contains(t, stderr, "wrong passphrase or corrupted database")
	assert.Contains(t, stderr, filepath.Join(project, "db.key"))
	assert.NotContains(t, stdout+stderr, projectFileTestKey)
	assert.NotContains(t, stdout+stderr, cliTestKey)
	assert.NotContains(t, stdout+stderr, "cipher=")
	sources, err := st.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "project-B-marker", sources[0].Label)
}

func TestDBSizeSubcommand_ClearedKeyFileUsesEnvironment(t *testing.T) {
	project, dbPath := newCLIProject(t)
	writeCLIKeyFixture(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "capy", "config.toml"), "[store]\nkey_file = 'must-not-read.key'\n")
	writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'test.db'\nkey_file = ''\n")
	seedKeyResolutionDB(t, dbPath, project)
	t.Setenv("CAPY_DB_KEY", projectFileTestKey)
	stdout, stderr, code := capy(t, "dbsize", "--project-dir", project)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "project-B-marker")
}

func TestKnowledgeStore_KeyFileSnapshot(t *testing.T) {
	project, dbPath := newCLIProject(t)
	path := filepath.Join(project, "db.key")
	writeCLIKeyFixture(t, path, projectFileTestKey)
	cfg := config.DefaultConfig()
	cfg.Store.KeyFile = "db.key"
	target := &knowledgeTarget{cfg: cfg, projectDir: project, dbProjectDir: project, dbPath: dbPath}
	key, source, err := resolveKnowledgeKey(target)
	require.NoError(t, err)
	st := newKnowledgeStore(target, key, source.String())
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	writeCLIKeyFixture(t, path, cliTestKey)
	_, err = st.Index("# Retained\n\nSelected before file changed.", "snapshot-marker", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	sources, err := st.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "snapshot-marker", sources[0].Label)
	require.NoError(t, st.Close())

	key, source, err = resolveKnowledgeKey(target)
	require.NoError(t, err)
	other := newKnowledgeStore(target, key, source.String())
	t.Cleanup(func() { assert.NoError(t, other.Close()) })
	_, err = other.ListSources()
	require.ErrorContains(t, err, "wrong passphrase or corrupted database")
}
