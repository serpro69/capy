package config

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeKeyFixture(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
}

func TestLoadKeyFilePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, global, middle, local, want string
	}{
		{"omitted", "", "", "", ""},
		{"global_inherited", `key_file = "global.key"`, "", "", "global.key"},
		{"middle_overrides", `key_file = "global.key"`, `key_file = "middle.key"`, "", "middle.key"},
		{"local_overrides", `key_file = "global.key"`, `key_file = "middle.key"`, `key_file = "local.key"`, "local.key"},
		{"middle_clears", `key_file = "global.key"`, `key_file = ""`, "", ""},
		{"local_clears", `key_file = "global.key"`, `key_file = "middle.key"`, `key_file = ""`, ""},
		{"local_after_clear", `key_file = "global.key"`, `key_file = ""`, `key_file = "local.key"`, "local.key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, xdg := t.TempDir(), t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			writeKeyFixture(t, filepath.Join(xdg, "capy", "config.toml"), "[store]\npath = 'shared.db'\n"+tc.global)
			writeKeyFixture(t, filepath.Join(project, ".capy", "config.toml"), "[store]\npath = ''\n"+tc.middle)
			writeKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = ''\n"+tc.local)
			cfg, err := Load(project)
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.Store.KeyFile)
			assert.Equal(t, "shared.db", cfg.Store.Path, "empty store.path still inherits")
		})
	}
}

func TestLoadKeyFileInvalidTypes(t *testing.T) {
	for _, layer := range []string{"global", "middle", "local"} {
		for _, value := range []string{"42", "false", "['key']", "{path = 'key'}"} {
			t.Run(layer+"/"+value, func(t *testing.T) {
				project, xdg := t.TempDir(), t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", xdg)
				paths := map[string]string{
					"global": filepath.Join(xdg, "capy", "config.toml"),
					"middle": filepath.Join(project, ".capy", "config.toml"),
					"local":  filepath.Join(project, ".capy.toml"),
				}
				writeKeyFixture(t, paths[layer], "[store]\nkey_file = "+value)
				cfg, err := Load(project)
				require.Error(t, err)
				assert.Nil(t, cfg)
				assert.Contains(t, err.Error(), paths[layer])
			})
		}
	}
}

func TestResolveStoreKeyFormat(t *testing.T) {
	const secret = "synthetic-file-passphrase"
	for _, tc := range []struct {
		name, data, want, reason string
	}{
		{"literal", secret, secret, ""},
		{"lf", secret + "\n", secret, ""},
		{"crlf", secret + "\r\n", secret, ""},
		{"spaces", " \t" + secret + " \t\n", " \t" + secret + " \t", ""},
		{"space_only", " \n", " ", ""},
		{"quotes_literal", "'" + secret + "'", "'" + secret + "'", ""},
		{"binary_literal", "\xff" + secret, "\xff" + secret, ""},
		{"empty", "", "", "empty"},
		{"empty_lf", "\n", "", "empty"},
		{"empty_crlf", "\r\n", "", "empty"},
		{"nul", secret + "\x00", "", "NUL"},
		{"cr", secret + "\r", "", "line ending"},
		{"embedded_lf", secret + "\nx", "", "line ending"},
		{"embedded_cr", secret + "\rx", "", "line ending"},
		{"two_lf", secret + "\n\n", "", "line ending"},
		{"two_crlf", secret + "\r\n\r\n", "", "line ending"},
		{"exact_limit", strings.Repeat("k", 4096), strings.Repeat("k", 4096), ""},
		{"exact_limit_lf", strings.Repeat("k", 4095) + "\n", strings.Repeat("k", 4095), ""},
		{"exact_limit_crlf", strings.Repeat("k", 4094) + "\r\n", strings.Repeat("k", 4094), ""},
		{"over_limit", strings.Repeat("k", 4097), "", "4096-byte"},
		{"lf_counts_toward_limit", strings.Repeat("k", 4096) + "\n", "", "4096-byte"},
		{"crlf_counts_toward_limit", strings.Repeat("k", 4095) + "\r\n", "", "4096-byte"},
		{"large_file", strings.Repeat("k", 1<<20), "", "4096-byte"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAPY_DB_KEY", "synthetic-inherited-passphrase")
			t.Setenv("CAPY_VAULT_KEY", "synthetic-vault-passphrase")
			project := t.TempDir()
			path := filepath.Join(project, ".capy", "db.key")
			writeKeyFixture(t, path, tc.data)
			cfg := DefaultConfig()
			cfg.Store.KeyFile = ".capy/db.key"
			key, source, err := cfg.ResolveStoreKey(project)
			assert.Equal(t, KeySource{Kind: KeySourceFile, Path: path}, source)
			assert.Contains(t, source.String(), path)
			if tc.reason != "" {
				require.Error(t, err)
				assert.Empty(t, key, "invalid source must not return a partial or inherited key")
				assert.Contains(t, err.Error(), tc.reason)
				assert.Contains(t, err.Error(), path)
				assert.NotContains(t, err.Error(), secret)
			} else {
				require.NoError(t, err)
				assert.True(t, key == tc.want, "resolved bytes differ from the expected literal passphrase")
			}
			assert.True(t, os.Getenv("CAPY_DB_KEY") == "synthetic-inherited-passphrase")
			assert.True(t, os.Getenv("CAPY_VAULT_KEY") == "synthetic-vault-passphrase")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.True(t, string(data) == tc.data, "credential file must not change")
		})
	}
}

func TestResolveStoreKeyEnvironment(t *testing.T) {
	for _, value := range []string{"", "synthetic-env-passphrase", strings.Repeat("e", 4097)} {
		t.Run(fmt.Sprintf("length_%d", len(value)), func(t *testing.T) {
			t.Setenv("CAPY_DB_KEY", value)
			key, source, err := DefaultConfig().ResolveStoreKey(t.TempDir())
			assert.Equal(t, KeySource{Kind: KeySourceEnvironment}, source)
			assert.Equal(t, "CAPY_DB_KEY", source.String())
			if value == "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "store.key_file")
				assert.Contains(t, err.Error(), "capy encrypt --help")
			} else {
				require.NoError(t, err)
			}
			assert.True(t, key == value, "environment credentials have no file length or trimming policy")
		})
	}
}

func TestResolveStoreKeyFileAccess(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "synthetic-fallback-must-not-be-used")
	project := t.TempDir()
	regular := filepath.Join(project, "regular.key")
	writeKeyFixture(t, regular, "synthetic-file-key")
	link := filepath.Join(project, "link.key")
	require.NoError(t, os.Symlink(regular, link))
	fifo := filepath.Join(project, "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	fifoLink := filepath.Join(project, "fifo-link")
	require.NoError(t, os.Symlink(fifo, fifoLink))
	// A short path also fits macOS's small Unix socket path limit.
	socketDir, err := os.MkdirTemp("/tmp", "capy-socket-")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, os.RemoveAll(socketDir)) })
	socket := filepath.Join(socketDir, "sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, listener.Close()) })

	for _, tc := range []struct {
		name, path, reason string
	}{
		{"regular_symlink", link, ""},
		{"missing", filepath.Join(project, "missing"), "inspecting"},
		{"invalid_parent", filepath.Join(regular, "key"), "inspecting"},
		{"directory", project, "regular file"},
		{"device", "/dev/null", "regular file"},
		{"socket", socket, "regular file"},
		{"fifo", fifo, "regular file"},
		{"fifo_symlink", fifoLink, "regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "fifo" || tc.name == "fifo_symlink" {
				// A blocked regression must be killed/reaped, not leak a goroutine.
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestResolveStoreKeyFIFOChild$")
				cmd.Env = append(os.Environ(), "CAPY_TEST_FIFO_PATH="+tc.path)
				output, err := cmd.CombinedOutput()
				require.NoError(t, ctx.Err(), "resolver blocked on a pre-existing FIFO")
				require.NoError(t, err, "%s", output)
				return
			}
			cfg := DefaultConfig()
			cfg.Store.KeyFile = tc.path
			key, source, err := cfg.ResolveStoreKey(project)
			assert.Equal(t, tc.path, source.Path)
			if tc.reason == "" {
				require.NoError(t, err)
				assert.True(t, key == "synthetic-file-key")
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.reason)
				assert.Empty(t, key)
			}
		})
	}
}

func TestResolveStoreKeyFIFOChild(t *testing.T) {
	path := os.Getenv("CAPY_TEST_FIFO_PATH")
	if path == "" {
		return
	}
	cfg := DefaultConfig()
	cfg.Store.KeyFile = path
	key, _, err := cfg.ResolveStoreKey(filepath.Dir(path))
	require.ErrorContains(t, err, "regular file")
	require.Empty(t, key)
}

func TestResolveStoreKeyUnreadable(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "denied.key")
	writeKeyFixture(t, path, "synthetic-unreadable-key")
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { assert.NoError(t, os.Chmod(path, 0o600)) })
	if f, err := os.Open(path); err == nil {
		require.NoError(t, f.Close())
		t.Skip("process bypasses file permissions; deterministic invalid-parent failure covered separately")
	}
	t.Setenv("CAPY_DB_KEY", "synthetic-valid-fallback")
	cfg := DefaultConfig()
	cfg.Store.KeyFile = path
	key, _, err := cfg.ResolveStoreKey(project)
	require.ErrorContains(t, err, "opening credential file")
	assert.Empty(t, key)
}

func TestResolveStoreKeyOwnership(t *testing.T) {
	for _, mode := range []string{"relative", "parent_relative", "absolute", "xdg"} {
		t.Run(mode, func(t *testing.T) {
			mainDir, linked := makeLinkedWorktree(t, true)
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("CAPY_DB_KEY", "synthetic-env-key")
			storePath := map[string]string{"relative": ".capy/db", "parent_relative": "../shared/db", "absolute": filepath.Join(t.TempDir(), "db"), "xdg": ""}[mode]
			// Only selected-project config is loaded, even when the DB owner is main.
			writeKeyFixture(t, filepath.Join(mainDir, ".capy.toml"), "invalid main config must never be read")
			writeKeyFixture(t, filepath.Join(linked, ".capy.toml"), fmt.Sprintf("[store]\npath = %q\nkey_file = '.capy/db.key'", storePath))
			writeKeyFixture(t, filepath.Join(mainDir, ".capy", "db.key"), "synthetic-main-key")
			writeKeyFixture(t, filepath.Join(linked, ".capy", "db.key"), "synthetic-linked-key")
			owner, want := linked, "synthetic-linked-key"
			if mode == "relative" || mode == "parent_relative" {
				owner, want = mainDir, "synthetic-main-key"
			}
			cfg, err := Load(linked)
			require.NoError(t, err)
			key, source, err := cfg.ResolveStoreKey(linked)
			require.NoError(t, err)
			assert.True(t, key == want)
			assert.Equal(t, filepath.Join(owner, ".capy", "db.key"), source.Path)
		})
	}
}

func TestResolveStoreKeyPathRules(t *testing.T) {
	project, external, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CAPY_DB_KEY", "synthetic-env-key")
	writeKeyFixture(t, filepath.Join(project, "db.key"), "synthetic-project-key")
	writeKeyFixture(t, filepath.Join(external, "db.key"), "synthetic-external-key")
	// Global relative credentials are anchored to the owner, not the config dir.
	writeKeyFixture(t, filepath.Join(xdg, "capy", "config.toml"), "[store]\nkey_file = 'db.key'")
	cfg, err := Load(project)
	require.NoError(t, err)
	key, source, err := cfg.ResolveStoreKey(project)
	require.NoError(t, err)
	assert.True(t, key == "synthetic-project-key")
	assert.Equal(t, filepath.Join(project, "db.key"), source.Path)

	for _, setting := range []string{filepath.Join(external, "db.key"), "../" + filepath.Base(external) + "/db.key"} {
		cfg.Store.KeyFile = setting
		key, source, err = cfg.ResolveStoreKey(project)
		require.NoError(t, err)
		assert.True(t, key == "synthetic-external-key")
		assert.Equal(t, filepath.Join(external, "db.key"), source.Path)
	}

	for _, setting := range []string{"~/db.key", "$CAPY_TEST_KEY_DIR/db.key"} {
		t.Setenv("CAPY_TEST_KEY_DIR", external)
		writeKeyFixture(t, filepath.Join(project, setting), "synthetic-unexpanded-key")
		cfg.Store.KeyFile = setting
		key, source, err = cfg.ResolveStoreKey(project)
		require.NoError(t, err)
		assert.True(t, key == "synthetic-unexpanded-key")
		assert.Equal(t, filepath.Join(project, setting), source.Path)
	}

	// A submodule's .git file must not redirect credentials to the superproject.
	writeKeyFixture(t, filepath.Join(project, ".git"), "gitdir: "+filepath.Join(external, ".git", "modules", "sub")+"\n")
	cfg.Store.Path, cfg.Store.KeyFile = "db", "db.key"
	key, source, err = cfg.ResolveStoreKey(project)
	require.NoError(t, err)
	assert.True(t, key == "synthetic-project-key")
	assert.Equal(t, filepath.Join(project, "db.key"), source.Path)

	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(project, alias))
	key, source, err = cfg.ResolveStoreKey(alias)
	require.NoError(t, err)
	assert.True(t, key == "synthetic-project-key")
	assert.Equal(t, filepath.Join(alias, "db.key"), source.Path, "project aliases must not be canonicalized")
}
