package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreKeyFilePermissions(t *testing.T) {
	const secret = "synthetic-permission-key"
	for _, mode := range []os.FileMode{0o400, 0o600, 0o644, 0o640, 0o604, 0o660, 0o666, 0o700, 0o200, 0, 0o600 | os.ModeSticky} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			for _, symlink := range []bool{false, true} {
				t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
					project := t.TempDir()
					path := filepath.Join(project, "db.key")
					writeKeyFixture(t, path, secret+"\n")
					require.NoError(t, os.Chmod(path, mode)) // do not depend on umask
					t.Cleanup(func() { assert.NoError(t, os.Chmod(path, 0o600)) })
					cfg := DefaultConfig()
					cfg.Store.KeyFile = path
					if symlink {
						cfg.Store.KeyFile = filepath.Join(project, "linked.key")
						require.NoError(t, os.Symlink(path, cfg.Store.KeyFile))
					}
					t.Setenv("CAPY_DB_KEY", "synthetic-fallback-must-not-win")
					writeKeyFixture(t, filepath.Join(project, ".env"), "CAPY_DB_KEY=synthetic-dotenv-fallback\n")
					key, source, err := cfg.ResolveStoreKey(project)
					if mode == 0o400 || mode == 0o600 {
						require.NoError(t, err)
						assert.Equal(t, secret, key)
						require.NoError(t, source.CheckFilePermissions())
					} else {
						require.ErrorContains(t, err, "unsafe key file permissions")
						assert.Empty(t, key)
						assert.Contains(t, err.Error(), cfg.Store.KeyFile)
						assert.Contains(t, err.Error(), "capy setup")
						assert.NotContains(t, err.Error(), secret)
						require.ErrorContains(t, source.CheckFilePermissions(), "unsafe key file permissions")
					}
					before, err := os.Stat(path)
					require.NoError(t, err)
					assert.Equal(t, mode, before.Mode(), "readers never repair permissions")
					require.NoError(t, cfg.EnsureStoreKeyFilePermissions(project))
					require.NoError(t, cfg.EnsureStoreKeyFilePermissions(project)) // idempotent
					info, err := os.Stat(path)
					require.NoError(t, err)
					wantMode := os.FileMode(0o600)
					if mode == 0o400 {
						wantMode = 0o400
					}
					assert.Equal(t, wantMode, info.Mode())
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, secret+"\n", string(data), "setup preserves literal bytes")
					if symlink {
						info, err := os.Lstat(cfg.Store.KeyFile)
						require.NoError(t, err)
						assert.NotZero(t, info.Mode()&os.ModeSymlink)
					}
				})
			}
		})
	}
}

func TestEnsureStoreKeyFilePermissionsAdmission(t *testing.T) {
	for _, state := range []string{"unset", "missing", "dangling", "directory", "fifo", "loop", "invalid_parent"} {
		t.Run(state, func(t *testing.T) {
			project := t.TempDir()
			cfg := DefaultConfig()
			cfg.Store.KeyFile = "db.key"
			path := filepath.Join(project, cfg.Store.KeyFile)
			switch state {
			case "unset":
				cfg.Store.KeyFile = ""
			case "dangling":
				require.NoError(t, os.Symlink(filepath.Join(project, "missing"), path))
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o755))
			case "fifo":
				require.NoError(t, syscall.Mkfifo(path, 0o644))
			case "loop":
				require.NoError(t, os.Symlink(path, path))
			case "invalid_parent":
				writeKeyFixture(t, path, "synthetic-key")
				cfg.Store.KeyFile = "db.key/child"
			}
			err := cfg.EnsureStoreKeyFilePermissions(project)
			switch state {
			case "unset", "missing", "dangling":
				require.NoError(t, err)
				assert.NoFileExists(t, filepath.Join(project, "missing"))
				if state == "dangling" {
					target, err := os.Readlink(path)
					require.NoError(t, err)
					assert.Equal(t, filepath.Join(project, "missing"), target)
				} else {
					assert.NoFileExists(t, path)
				}
			case "directory", "fifo":
				require.ErrorContains(t, err, "regular file")
			default:
				require.ErrorContains(t, err, "inspecting key file permissions")
			}
		})
	}
}

func TestKeyFileSpecialPermissionBits(t *testing.T) {
	// Some filesystems clear setuid/setgid on non-executable files at chmod
	// time. Exercise the admission policy independently of that host behavior.
	for _, bit := range []os.FileMode{os.ModeSetuid, os.ModeSetgid, os.ModeSticky} {
		t.Run(fmt.Sprint(bit), func(t *testing.T) {
			require.ErrorContains(t, checkKeyFileMode(0o600|bit), "no special bits")
			require.ErrorContains(t, checkKeyFileMode(0o400|bit), "no special bits")
		})
	}
}

func TestKeyFilePermissionsDoNotApplyToDotenv(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".env")
	writeKeyFixture(t, path, "CAPY_DB_KEY=synthetic-dotenv-key\n")
	require.NoError(t, os.Chmod(path, 0o644))
	cfg := DefaultConfig()
	key, source, err := cfg.ResolveStoreKey(project)
	require.NoError(t, err)
	assert.Equal(t, "synthetic-dotenv-key", key)
	require.NoError(t, source.CheckFilePermissions())
	require.NoError(t, cfg.EnsureStoreKeyFilePermissions(project))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestEnsureStoreKeyFilePermissionsOwnership(t *testing.T) {
	for _, mode := range []string{"relative", "absolute", "xdg"} {
		t.Run(mode, func(t *testing.T) {
			mainDir, linked := makeLinkedWorktree(t, true)
			cfg := DefaultConfig()
			cfg.Store.Path = map[string]string{"relative": ".capy/db", "absolute": filepath.Join(t.TempDir(), "db"), "xdg": ""}[mode]
			cfg.Store.KeyFile = "db.key"
			for _, dir := range []string{mainDir, linked} {
				writeKeyFixture(t, filepath.Join(dir, "db.key"), "synthetic-key")
				require.NoError(t, os.Chmod(filepath.Join(dir, "db.key"), 0o644))
			}
			require.NoError(t, cfg.EnsureStoreKeyFilePermissions(linked))
			for _, dir := range []string{mainDir, linked} {
				info, err := os.Stat(filepath.Join(dir, "db.key"))
				require.NoError(t, err)
				want := os.FileMode(0o644)
				if (mode == "relative" && dir == mainDir) || (mode != "relative" && dir == linked) {
					want = 0o600
				}
				assert.Equal(t, want, info.Mode().Perm())
			}
		})
	}
}
