package platform

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupKeyFilePermissions(t *testing.T) {
	for _, platform := range []string{"claude-code", "codex"} {
		t.Run(platform, func(t *testing.T) {
			for _, state := range []string{"unsafe", "readonly", "missing", "invalid_config", "nonregular", "invalid_parent", "global_absolute"} {
				t.Run(state, func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv("XDG_CONFIG_HOME", t.TempDir())
					t.Setenv("CAPY_DB_KEY", "")
					path := filepath.Join(dir, "db.key")
					configPath := filepath.Join(dir, ".capy.toml")
					configText := "[store]\nkey_file = 'db.key'\n"
					wantMode := os.FileMode(0o600)
					switch state {
					case "global_absolute":
						path = filepath.Join(t.TempDir(), "external.key")
						configPath = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "capy", "config.toml")
						require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
						configText = "[store]\nkey_file = '" + path + "'\n"
					case "invalid_config":
						configText = "[store]\nkey_file = 42\n"
					case "nonregular":
						require.NoError(t, os.Mkdir(path, 0o755))
					case "invalid_parent":
						require.NoError(t, os.WriteFile(path, []byte("synthetic-key"), 0o600))
						configText = "[store]\nkey_file = 'db.key/child'\n"
					}
					require.NoError(t, os.WriteFile(configPath, []byte(configText), 0o600))
					if state == "unsafe" || state == "readonly" || state == "global_absolute" {
						// Deliberately invalid key contents prove setup never parses credentials.
						require.NoError(t, os.WriteFile(path, []byte("synthetic\ninvalid\nkey"), 0o600))
						mode := os.FileMode(0o644)
						if state == "readonly" {
							mode, wantMode = 0o400, 0o400
						}
						require.NoError(t, os.Chmod(path, mode))
					}
					var err error
					if platform == "claude-code" {
						err = SetupClaudeCode("/synthetic/capy", dir, SettingsProject)
					} else {
						err = SetupCodex("/synthetic/capy", dir)
					}
					if state == "invalid_config" || state == "nonregular" || state == "invalid_parent" {
						require.Error(t, err)
						assert.NoDirExists(t, filepath.Join(dir, ".claude"))
						assert.NoDirExists(t, filepath.Join(dir, ".codex"))
						assert.NoDirExists(t, filepath.Join(dir, ".capy"))
						return
					}
					require.NoError(t, err)
					if state == "missing" {
						assert.NoFileExists(t, path)
						return
					}
					info, err := os.Stat(path)
					require.NoError(t, err)
					assert.Equal(t, wantMode, info.Mode().Perm())
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, "synthetic\ninvalid\nkey", string(data))
				})
			}
		})
	}
}
