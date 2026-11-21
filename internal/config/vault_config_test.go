package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadVaultMinSessionBytes(t *testing.T) {
	for _, tt := range []struct {
		name, global, project, root string
		want                        int64
	}{
		{name: "disabled by default"},
		{name: "global", global: "102400", want: 102400},
		{name: "project overrides global", global: "102400", project: "2048", want: 2048},
		{name: "root overrides project", global: "102400", project: "2048", root: "4096", want: 4096},
		{name: "root zero disables", global: "102400", project: "2048", root: "0"},
		{name: "project zero survives omitted root", global: "102400", project: "0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, xdg := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			for path, value := range map[string]string{
				filepath.Join(xdg, "capy", "config.toml"):  tt.global,
				filepath.Join(dir, ".capy", "config.toml"): tt.project,
				filepath.Join(dir, ".capy.toml"):           tt.root,
			} {
				content := "[server]\nlog_level = \"info\"\n"
				if value != "" {
					content += "[vault]\nmin_session_bytes = " + value + "\n"
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			}
			cfg, err := Load(dir)
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Vault.MinSessionBytes)
		})
	}
}

func TestLoadVaultRememberDeletions(t *testing.T) {
	for _, tt := range []struct {
		name, global, project, root string
		want                        bool
	}{
		{name: "enabled by default", want: true},
		{name: "global false", global: "false"},
		{name: "project false", global: "true", project: "false"},
		{name: "root false", project: "true", root: "false"},
		{name: "project true overrides global", global: "false", project: "true", want: true},
		{name: "root true overrides project", project: "false", root: "true", want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir, xdg := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			for path, value := range map[string]string{
				filepath.Join(xdg, "capy", "config.toml"):  tt.global,
				filepath.Join(dir, ".capy", "config.toml"): tt.project,
				filepath.Join(dir, ".capy.toml"):           tt.root,
			} {
				content := "[vault]\n"
				if value != "" {
					content += "remember_deletions = " + value + "\n"
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
			}
			cfg, err := Load(dir)
			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Vault.RememberDeletions)
		})
	}
}

func TestLoadVaultMinSessionBytesRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"-1", "9223372036854775808", "1.5", "\"100KB\""} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"),
				[]byte("[vault]\nmin_session_bytes = "+value+"\n"), 0o644))
			_, err := Load(dir)
			require.Error(t, err)
		})
	}
}
