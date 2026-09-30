package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEncryptedDB writes a real encrypted knowledge DB at dir/knowledge.db (using
// CAPY_DB_KEY) with one indexed row and no lingering WAL/SHM sidecars, then
// returns its path.
func newEncryptedDB(t *testing.T, dir string) string {
	t.Helper()
	dbPath := filepath.Join(dir, "knowledge.db")
	st := store.NewContentStore(dbPath, dir, 0, 0)
	_, err := st.Index("# Auth\n\nJWT validation middleware.", "auth-doc", "", store.KindDurable)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	return dbPath
}

// TestResolveDBSymlink_SwapPreservesSymlink is the regression guard for issue
// #90: `capy encrypt` on a knowledge.db kept in a separate DB repo and symlinked
// into the project's .capy/ must rename the REAL file, leaving the symlink
// intact. Without resolveDBSymlink, SwapAndVerify renames the link itself,
// replacing it with a regular file and stranding the DB-repo copy.
func TestResolveDBSymlink_SwapPreservesSymlink(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", cliTestKey)

	root := t.TempDir()
	realDir := filepath.Join(root, "db-repo", "proj")
	require.NoError(t, os.MkdirAll(realDir, 0o755))
	realDBPath := newEncryptedDB(t, realDir)

	projCapy := filepath.Join(root, "project", ".capy")
	require.NoError(t, os.MkdirAll(projCapy, 0o755))
	symlinkPath := filepath.Join(projCapy, "knowledge.db")
	require.NoError(t, os.Symlink(realDBPath, symlinkPath))

	// resolveDBSymlink must return the real file, not the link, so the swap acts
	// on the target. EvalSymlinks canonicalizes both sides (temp dirs may hide
	// behind their own links), so compare against the resolved real path.
	wantResolved, err := filepath.EvalSymlinks(realDBPath)
	require.NoError(t, err)
	resolved := resolveDBSymlink(symlinkPath)
	require.Equal(t, wantResolved, resolved, "resolveDBSymlink should return the symlink target")

	// Drive the swap the way runEncrypt does: onto the resolved path with a valid
	// encrypted replacement. A copy of the DB opens under the same key.
	tmpPath := resolved + ".enc.tmp"
	require.NoError(t, copyFile(resolved, tmpPath))
	bakPath, err := sqliteutil.SwapAndVerify(resolved, tmpPath, cliTestKey)
	require.NoError(t, err)
	assert.FileExists(t, bakPath)

	// The link must survive as a link and still point at the real file, which now
	// holds the freshly swapped encrypted DB.
	info, err := os.Lstat(symlinkPath)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "expected %s to still be a symlink", symlinkPath)

	target, err := os.Readlink(symlinkPath)
	require.NoError(t, err)
	assert.Equal(t, realDBPath, target, "symlink should still point at the real DB file")
	assert.FileExists(t, realDBPath)
}

func TestRunEncrypt_ProjectCredentialsDoNotSelectRotationKeys(t *testing.T) {
	const newKey = "synthetic-rotated-knowledge-key-at-least-32-characters"
	const vaultKey = "synthetic-vault-key-must-remain-in-environment"
	for _, source := range []string{"key_file", "dotenv"} {
		t.Run(source, func(t *testing.T) {
			for _, newKeySource := range []string{"environment", "prompt"} {
				t.Run(newKeySource, func(t *testing.T) {
					project, dbPath := newCLIProject(t)
					seedKeyResolutionDB(t, dbPath, project)
					keyPath, dotenvPath := filepath.Join(project, "db.key"), filepath.Join(project, ".env")
					keyContent := projectFileTestKey + "\r\n"
					dotenvContent := "# Existing project credentials\nCAPY_DB_KEY='" + projectFileTestKey +
						"'\nCAPY_VAULT_KEY=unused-project-vault-key\n"
					writeCLIKeyFixture(t, keyPath, keyContent)
					writeCLIKeyFixture(t, dotenvPath, dotenvContent)
					if source == "key_file" {
						writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "[store]\npath = 'test.db'\nkey_file = 'db.key'\n")
					}
					envKey := ""
					if newKeySource == "environment" {
						envKey = newKey
					}
					t.Setenv("CAPY_DB_KEY", envKey)
					t.Setenv("CAPY_VAULT_KEY", vaultKey)

					cmd := newEncryptCmd()
					cmd.Flags().String("project-dir", project, "")
					prompts := []string{}
					stdout, err := captureEncryptOutput(t, func() error {
						return runEncryptWithPrompts(cmd, func(prompt string) (string, error) {
							assert.Contains(t, prompt, "Current DB passphrase")
							prompts = append(prompts, "old")
							return projectFileTestKey, nil
						}, func(prompt string) (string, error) {
							assert.Contains(t, prompt, "New passphrase")
							prompts = append(prompts, "new-confirmed")
							return newKey, nil
						})
					})
					require.NoError(t, err)
					wantPrompts := []string{"old"}
					if newKeySource == "prompt" {
						wantPrompts = append(wantPrompts, "new-confirmed")
					}
					assert.Equal(t, wantPrompts, prompts)
					assert.Contains(t, stdout, "capy encrypt: done.")
					assert.Contains(t, stdout, "update any project credential (store.key_file or CAPY_DB_KEY in .env)")
					assert.Contains(t, stdout, "before restarting servers or other database users")
					for _, secret := range []string{projectFileTestKey, newKey, vaultKey, "unused-project-vault-key", "cipher=", "key="} {
						assert.False(t, strings.Contains(stdout, secret), "rotation output must not disclose credentials or encryption DSNs")
					}

					oldStore := store.NewContentStore(
						dbPath,
						project,
						0,
						0,
						store.WithEncryptionKey(projectFileTestKey, "synthetic old key"),
					)
					t.Cleanup(func() { assert.NoError(t, oldStore.Close()) })
					_, err = oldStore.ListSources()
					require.ErrorContains(t, err, "wrong passphrase or corrupted database")
					require.NoError(t, oldStore.Close())
					newStore := store.NewContentStore(
						dbPath,
						project,
						0,
						0,
						store.WithEncryptionKey(newKey, "synthetic new key"),
					)
					t.Cleanup(func() { assert.NoError(t, newStore.Close()) })
					sources, err := newStore.ListSources()
					require.NoError(t, err)
					require.Len(t, sources, 1)
					assert.Equal(t, "project-B-marker", sources[0].Label)
					require.NoError(t, newStore.Close())
					assert.FileExists(t, dbPath+".bak")

					for path, want := range map[string]string{keyPath: keyContent, dotenvPath: dotenvContent} {
						got, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.True(t, string(got) == want, "project credential files must remain byte-for-byte unchanged")
					}
					assert.True(t, os.Getenv("CAPY_DB_KEY") == envKey, "rotation must not change the knowledge environment key")
					assert.True(t, os.Getenv("CAPY_VAULT_KEY") == vaultKey, "rotation must not change the vault environment key")
					assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
				})
			}
		})
	}
}

func TestRunEncrypt_InvalidConfigBeforePrompts(t *testing.T) {
	project, dbPath := newCLIProject(t)
	seedKeyResolutionDB(t, dbPath, project)
	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	writeCLIKeyFixture(t, filepath.Join(project, ".capy.toml"), "this is not [valid toml\n")
	cmd := newEncryptCmd()
	cmd.Flags().String("project-dir", project, "")
	unexpectedPrompt := func(string) (string, error) {
		t.Fatal("invalid configuration must fail before any passphrase prompt")
		return "", nil
	}
	err = runEncryptWithPrompts(cmd, unexpectedPrompt, unexpectedPrompt)
	require.ErrorContains(t, err, "loading configuration")
	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "invalid configuration must not modify the existing database")
	assert.NoFileExists(t, dbPath+".bak")
	assert.NoFileExists(t, dbPath+".enc.tmp")
	assert.NoDirExists(t, filepath.Join(os.Getenv("XDG_DATA_HOME"), "capy"))
}

func TestRunEncrypt_PromptFailuresPreserveDatabase(t *testing.T) {
	for _, mode := range []string{"current_prompt_error", "new_prompt_error", "empty_new_key", "wrong_old_key"} {
		t.Run(mode, func(t *testing.T) {
			project, dbPath := newCLIProject(t)
			st := seedKeyResolutionDB(t, dbPath, project)
			writeCLIKeyFixture(t, filepath.Join(project, ".env"), "CAPY_DB_KEY="+projectFileTestKey+"\n")
			t.Setenv("CAPY_DB_KEY", "")
			cmd := newEncryptCmd()
			cmd.Flags().String("project-dir", project, "")
			prompts := []string{}
			stdout, err := captureEncryptOutput(t, func() error {
				return runEncryptWithPrompts(cmd, func(string) (string, error) {
					prompts = append(prompts, "old")
					if mode == "current_prompt_error" {
						return "", os.ErrPermission
					}
					if mode == "wrong_old_key" {
						return cliTestKey, nil
					}
					return projectFileTestKey, nil
				}, func(string) (string, error) {
					prompts = append(prompts, "new-confirmed")
					if mode == "new_prompt_error" {
						return "", os.ErrPermission
					}
					if mode == "empty_new_key" {
						return "", nil
					}
					return cliTestKey, nil
				})
			})
			require.Error(t, err)
			assert.Empty(t, stdout, "failed rotation must not print success or update reminders")
			wantPrompts := []string{"old", "new-confirmed"}
			switch mode {
			case "current_prompt_error":
				wantPrompts = []string{"old"}
				assert.ErrorIs(t, err, os.ErrPermission)
				assert.ErrorContains(t, err, "reading current passphrase")
			case "new_prompt_error":
				assert.ErrorIs(t, err, os.ErrPermission)
				assert.ErrorContains(t, err, "reading new passphrase")
			case "empty_new_key":
				assert.ErrorContains(t, err, "new passphrase cannot be empty")
			case "wrong_old_key":
				assert.ErrorContains(t, err, "wrong passphrase")
			}
			assert.Equal(t, wantPrompts, prompts)
			sources, err := st.ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Equal(t, "project-B-marker", sources[0].Label)
			assert.NoFileExists(t, dbPath+".bak")
			assert.NoFileExists(t, dbPath+".enc.tmp")
		})
	}
}

// captureEncryptOutput is used only by serial tests (which also use t.Setenv).
// A temporary file captures the legacy stdout messages without pipe buffering
// or any dependence on a controlling terminal.
func captureEncryptOutput(t *testing.T, run func() error) (string, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "encrypt-output-*")
	require.NoError(t, err)
	runErr := func() error {
		previous := os.Stdout
		os.Stdout = output
		defer func() {
			os.Stdout = previous
			assert.NoError(t, output.Close())
		}()
		return run()
	}()
	data, err := os.ReadFile(output.Name())
	require.NoError(t, err)
	return string(data), runErr
}
