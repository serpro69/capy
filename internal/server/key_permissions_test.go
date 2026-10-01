package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoctorKeyFilePermissionsPreserveSnapshot(t *testing.T) {
	for _, state := range []string{"safe", "unsafe", "missing", "directory", "readonly"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("CAPY_VAULT_KEY", "")
			project := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.Store.Path = filepath.Join(project, "knowledge.db")
			cfg.Store.KeyFile = "db.key"
			path := filepath.Join(project, cfg.Store.KeyFile)
			const original = "synthetic-original-doctor-key"
			const replacement = "synthetic-replacement-doctor-key"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			key, source, err := cfg.ResolveStoreKey(project)
			require.NoError(t, err)
			srv := NewServer(cfg, nil, executor.NewExecutor(project, 0), project, WithKnowledgeCredentials(key, source))
			t.Cleanup(srv.shutdown)

			// Prime the encrypted store with the original snapshot.
			checks := srv.knowledgeChecks()
			require.Equal(t, "Knowledge base", checks[len(checks)-1].Name)
			require.NoError(t, srv.getStore().Close()) // force authentication with the snapshot again
			switch state {
			case "unsafe":
				require.NoError(t, os.WriteFile(path, []byte(replacement), 0o600))
				require.NoError(t, os.Chmod(path, 0o644))
			case "missing":
				require.NoError(t, os.Remove(path))
			case "directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
			case "readonly":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.WriteFile(path, []byte(replacement), 0o400))
			}
			text := resultText(callDoctor(t, srv))
			assert.Contains(t, text, "[x] Knowledge credential:")
			assert.Contains(t, text, "[x] Knowledge base: 0 sources, 0 chunks")
			if state == "safe" || state == "readonly" {
				assert.Contains(t, text, "[x] Key file permissions:")
			} else {
				assert.Contains(t, text, "[ ] Key file permissions:")
				assert.Contains(t, text, path)
			}
			if state == "unsafe" {
				assert.Contains(t, text, "unsafe key file permissions")
			}
			assert.NotContains(t, text, original)
			assert.NotContains(t, text, replacement)
			assert.Equal(t, original, srv.knowledgeKey)
		})
	}
}
