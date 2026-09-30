package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBSizeSubcommand_EnvironmentKey(t *testing.T) {
	dir, dbPath := newCLIProject(t)
	st := store.NewContentStore(dbPath, dir, 0, 0)
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	_, err := st.Index("# Orchard\n\nContent for disk usage.", "dbsize-marker", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	stdout, stderr, code := capy(t, "dbsize", "--project-dir", dir)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "Database: "+dbPath)
	assert.Contains(t, stdout, "dbsize-marker")
	assert.Contains(t, stdout, "durable")
	assert.NotContains(t, stdout+stderr, cliTestKey)
	assert.NotContains(t, stdout+stderr, "cipher=")
	sources, err := st.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "dbsize-marker", sources[0].Label)
}

func TestDBSizeSubcommand_InvalidConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
	}{
		{"malformed", "[store\npath = \"other.db\"\n"},
		{"invalid_type", "[store]\npath = 42\n"},
		{"invalid_value", "[store.cleanup]\nephemeral_ttl_hours = 0\n"},
		{"unreadable", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, dbPath := newCLIProject(t)
			configPath := filepath.Join(dir, ".capy.toml")
			if tc.name == "unreadable" {
				require.NoError(t, os.Remove(configPath))
				require.NoError(t, os.Mkdir(configPath, 0o755))
			} else {
				require.NoError(t, os.WriteFile(configPath, []byte(tc.config), 0o600))
			}
			stdout, stderr, code := capy(t, "dbsize", "--project-dir", dir)
			assert.NotZero(t, code)
			assert.Contains(t, stderr, "loading configuration")
			assert.NotContains(t, stdout, "Database:")
			assert.NotContains(t, stdout+stderr, cliTestKey)
			for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", filepath.Join(dir, ".project"), filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy")} {
				_, err := os.Stat(path)
				assert.True(t, os.IsNotExist(err), "invalid configuration must leave %s absent", path)
			}
		})
	}
}
