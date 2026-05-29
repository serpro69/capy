package vault

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	vaultKeyEnv  = "CAPY_VAULT_KEY"
	vaultPathEnv = "CAPY_VAULT_PATH"
)

// RequireVaultKey reads CAPY_VAULT_KEY from the environment. The vault database
// is encrypted with this key and cannot be opened without it. Returns an error
// if the variable is empty.
func RequireVaultKey() (string, error) {
	key := os.Getenv(vaultKeyEnv)
	if key == "" {
		return "", fmt.Errorf("%s environment variable is required to use the vault", vaultKeyEnv)
	}
	return key, nil
}

// VaultDBPath resolves the vault database location:
//  1. CAPY_VAULT_PATH, if set
//  2. $XDG_DATA_HOME/capy/vault.db (default: ~/.local/share/capy/vault.db)
//
// The vault DB is data, not config, so it lives under XDG_DATA_HOME — the same
// convention the knowledge store uses (see config.ResolveDBPath).
func VaultDBPath() string {
	if p := os.Getenv(vaultPathEnv); p != "" {
		return p
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "capy", "vault.db")
}
