package vault

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireVaultKey(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		t.Setenv(vaultKeyEnv, "")
		_, err := RequireVaultKey()
		require.Error(t, err)
		assert.Contains(t, err.Error(), vaultKeyEnv)
	})

	t.Run("set", func(t *testing.T) {
		t.Setenv(vaultKeyEnv, "a-vault-key")
		key, err := RequireVaultKey()
		require.NoError(t, err)
		assert.Equal(t, "a-vault-key", key)
	})
}

func TestVaultDBPath(t *testing.T) {
	t.Run("explicit_path_wins", func(t *testing.T) {
		t.Setenv(vaultPathEnv, "/custom/vault.db")
		assert.Equal(t, "/custom/vault.db", VaultDBPath())
	})

	t.Run("xdg_data_home_default", func(t *testing.T) {
		t.Setenv(vaultPathEnv, "")
		t.Setenv("XDG_DATA_HOME", "/xdg/data")
		assert.Equal(t, filepath.Join("/xdg/data", "capy", "vault.db"), VaultDBPath())
	})
}
