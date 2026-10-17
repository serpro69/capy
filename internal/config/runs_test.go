package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunsConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"defaults", "", true}, {"zero wait", "[runs]\nwait_ms=0\n", true},
		{"negative wait", "[runs]\nwait_ms=-1\n", false}, {"large wait", "[runs]\nwait_ms=30001\n", false},
		{"fractional", "[runs]\nmax_active=2.5\n", false}, {"zero active", "[runs]\nmax_active=0\n", false},
		{"zero ttl", "[runs]\nttl_hours=0\n", false}, {"overflow ttl", "[runs]\nttl_hours=9223372036854775807\n", false},
		{"quota relationship", "[runs]\nmax_run_bytes=20\nmax_retained_bytes=10\n", false},
		{"zero timeout", "[executor]\ntimeout=0\n", false}, {"fractional timeout", "[executor]\ntimeout=1.2\n", false},
		{"overflow timeout", "[executor]\ntimeout=9223372036854775807\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, ".capy.toml"), []byte(tc.body), 0o600))
			cfg, err := Load(root)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.name == "zero wait" {
				require.Zero(t, cfg.Runs.WaitMS)
			} else {
				require.Equal(t, 25000, cfg.Runs.WaitMS)
			}
			require.Equal(t, 300, cfg.Executor.Timeout)
			require.Equal(t, int64(64<<20), cfg.Runs.MaxRunBytes)
		})
	}
}

func TestRunPathDoesNotShareKnowledgeLocation(t *testing.T) {
	root := t.TempDir()
	linked := filepath.Join(root, "linked")
	require.NoError(t, os.Mkdir(linked, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "worktrees", "linked"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+filepath.Join(root, ".git", "worktrees", "linked")), 0o600))
	cfg := DefaultConfig()
	cfg.Store.Path = "knowledge.db"
	require.Equal(t, filepath.Join(root, "knowledge.db"), cfg.ResolveDBPath(linked))
	path, err := ResolveRunDBPath(linked)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(linked, ".capy", "runs", "runs.db"), path)
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(linked, alias))
	resolved, err := ResolveRunDBPath(alias)
	require.NoError(t, err)
	require.Equal(t, path, resolved)
}
