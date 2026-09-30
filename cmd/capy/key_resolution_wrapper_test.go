package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Run real setup and launch its output, selecting this fixture's candidate via
// PATH. No installed capy, project config, or credentials reach these children.
func wrapperEnv(t *testing.T, bin, key string) []string {
	t.Helper()
	for _, tool := range []string{"bash", "git"} {
		_, err := exec.LookPath(tool)
		require.NoError(t, err, "%s is required for generated wrapper integration tests", tool)
	}
	return replaceWrapperEnv(stdioEnv(t, key), "PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func replaceWrapperEnv(env []string, name, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, name+"=") {
			result = append(result, entry)
		}
	}
	return append(result, name+"="+value)
}

func runWrapperCommand(t *testing.T, command stdioCommand) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stdioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.executable, command.args...)
	cmd.Dir, cmd.Env = command.dir, command.env
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	for _, secret := range command.secrets {
		if secret != "" && strings.Contains(string(out), secret) {
			t.Fatal("credential appeared in command output")
		}
	}
	require.NoError(t, ctx.Err(), "fixture command timed out")
	return string(out), err
}

func setupWrapperProject(t *testing.T, bin, project string, env []string) {
	t.Helper()
	out, err := runWrapperCommand(t, stdioCommand{
		executable: bin, args: []string{"setup", "--project", "--project-dir", project}, dir: project, env: env,
	})
	require.NoError(t, err, out)
}

func wrapperGit(t *testing.T, project string, env []string, args ...string) string {
	t.Helper()
	out, err := runWrapperCommand(t, stdioCommand{
		executable: "git", args: append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...),
		dir: project, env: env,
	})
	require.NoError(t, err, out)
	return strings.TrimSpace(out)
}

func TestGeneratedWrapperProjectCredentials(t *testing.T) {
	bin := buildStdioCapy(t)
	const wrongKey = "synthetic-wrong-inherited-wrapper-key-12345"
	secrets := []string{projectFileTestKey, wrongKey}
	for _, platform := range []string{".claude", ".codex"} {
		t.Run(platform, func(t *testing.T) {
			for _, tc := range []struct {
				name, source, inherited string
				args                    []string
			}{
				{name: "file without inherited key", source: "key file", args: []string{"serve"}},
				{name: "dotenv overrides inheritance", source: "dotenv", inherited: wrongKey, args: []string{"serve"}},
				{name: "bare", source: "key file"},
				{name: "leading project flag", source: "key file", args: []string{"--project-dir", "PROJECT", "serve"}},
				{name: "environment-only rollback launch", inherited: projectFileTestKey, args: []string{"serve"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					project := stdioProject(t)
					if tc.source != "" {
						writeMCPProjectCredential(t, project, tc.source, projectFileTestKey)
					}
					env := wrapperEnv(t, bin, tc.inherited)
					if tc.name == "file without inherited key" {
						wrapperGit(t, project, env, "init", "-q")
						writeCLIKeyFixture(t, filepath.Join(project, ".env"), "touch wrapper-executed\nCAPY_DB_KEY=irrelevant\n")
					}
					setupWrapperProject(t, bin, project, env)
					wrapper := filepath.Join(project, platform, "scripts", "capy.sh")
					before, err := os.ReadFile(wrapper)
					require.NoError(t, err)
					setupWrapperProject(t, bin, project, env)
					after, err := os.ReadFile(wrapper)
					require.NoError(t, err)
					assert.Equal(t, before, after, "repeated setup must keep wrappers identical")
					args := append([]string{wrapper}, tc.args...)
					cwd := project
					if tc.name == "leading project flag" {
						args[2], cwd = project, stdioProject(t)
					}
					for _, reopen := range []bool{false, true} {
						p := startStdioProcess(t, stdioCommand{executable: "bash", args: args, dir: cwd, env: env, secrets: secrets})
						p.initialize(t)
						if !reopen {
							stdioIndex(t, p, "silvercapybara")
						}
						stdioSearch(t, p, "silvercapybara", "absentotter")
						stdioDoctor(t, p, project)
						p.close(t)
						stdioCheckpointed(t, project)
					}
					out, err := runWrapperCommand(t, stdioCommand{
						executable: "bash", args: []string{wrapper, "dbsize", "--project-dir", project}, dir: cwd, env: env, secrets: secrets,
					})
					require.NoError(t, err, out)
					assert.Contains(t, out, filepath.Join(project, "knowledge.db"))
					assert.Contains(t, out, "stdio-fixture")
					assert.NoFileExists(t, filepath.Join(project, "wrapper-executed"), "wrapper must not execute application dotenv")
				})
			}
			t.Run("hooks without knowledge credentials", func(t *testing.T) {
				project, env := stdioProject(t), wrapperEnv(t, bin, "")
				setupWrapperProject(t, bin, project, env)
				wrapper := filepath.Join(project, platform, "scripts", "capy.sh")
				for _, event := range []string{"pretooluse", "invalid-fixture-event"} {
					out, err := runWrapperCommand(t, stdioCommand{
						executable: "bash", args: []string{wrapper, "hook", event}, dir: project, env: env,
					})
					require.NoError(t, err, out)
					if event == "invalid-fixture-event" {
						assert.Contains(t, out, "unknown hook event", "must reach the candidate and mask its hook failure")
					}
					assert.NotContains(t, out, "CAPY_DB_KEY")
				}
				assert.NoFileExists(t, filepath.Join(project, "knowledge.db"))
			})
		})
	}
}

func TestGeneratedWrapperVaultMigration(t *testing.T) {
	bin := buildStdioCapy(t)
	const vaultKey = "synthetic-wrapper-vault-environment-key-12345"
	const ignoredVaultKey = "synthetic-dotenv-vault-key-must-not-override"
	secrets := []string{projectFileTestKey, vaultKey, ignoredVaultKey}
	for _, platform := range []string{".claude", ".codex"} {
		t.Run(platform, func(t *testing.T) {
			mainDir := stdioProject(t)
			env := wrapperEnv(t, bin, "")
			wrapperGit(t, mainDir, env, "init", "-q")
			wrapperGit(t, mainDir, env, "add", ".capy.toml")
			wrapperGit(t, mainDir, env, "commit", "-qm", "fixture")
			linked := filepath.Join(t.TempDir(), "linked")
			wrapperGit(t, mainDir, env, "worktree", "add", "--detach", linked)
			writeCLIKeyFixture(t, filepath.Join(mainDir, ".env"), "CAPY_DB_KEY='"+projectFileTestKey+"'\nCAPY_VAULT_KEY='"+vaultKey+"'\n")
			setupWrapperProject(t, bin, linked, env)

			// Seed a separate vault through the candidate CLI and isolated Codex root.
			// Startup cannot discover the fixture after import, so a healthy doctor and
			// search prove reads of the existing archive, independent of sweep timing.
			enabledEnv := replaceWrapperEnv(env, "CAPY_VAULT_KEY", vaultKey)
			var codexRoot string
			for _, entry := range env {
				if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
					codexRoot = value
				}
			}
			require.NotEmpty(t, codexRoot)
			transcript := filepath.Join(codexRoot, filepath.FromSlash(codexCLIRel))
			writeCLIKeyFixture(t, transcript, string(codexCLIRollout))
			out, err := runWrapperCommand(t, stdioCommand{
				executable: bin, args: []string{"vault", "import"}, dir: linked, env: enabledEnv, secrets: secrets,
			})
			require.NoError(t, err, out)
			require.Contains(t, out, "imported 1")
			require.NoError(t, os.Remove(transcript))
			for _, enabled := range []bool{false, true} {
				childEnv := env
				dotenvKey := vaultKey
				if enabled {
					childEnv, dotenvKey = enabledEnv, ignoredVaultKey
				}
				writeCLIKeyFixture(t, filepath.Join(mainDir, ".env"), "CAPY_DB_KEY='"+projectFileTestKey+"'\nCAPY_VAULT_KEY='"+dotenvKey+"'\n")
				p := startStdioProcess(t, stdioCommand{
					executable: "bash", args: []string{filepath.Join(linked, platform, "scripts", "capy.sh"), "serve"},
					dir: linked, env: childEnv, secrets: secrets,
				})
				p.initialize(t)
				if !enabled {
					stdioIndex(t, p, "worktreecapybara")
				}
				stdioSearch(t, p, "worktreecapybara", "absentotter")
				text := p.tool(t, "capy_doctor", map[string]any{})
				assert.Contains(t, text, "[x] Knowledge base: 1 sources, 1 chunks")
				assert.Contains(t, text, "[x] Project: "+linked)
				if enabled {
					assert.Contains(t, text, "[x] Vault: 1 sessions archived")
					text = p.tool(t, "capy_vault_search", map[string]any{"queries": []string{"brontosaurus timeout"}, "all_projects": true})
					assert.Contains(t, text, "Please fix the brontosaurus timeout")
				} else {
					assert.Contains(t, text, "[-] Vault: disabled")
					assert.Contains(t, text, "wrappers no longer source .env")
					assert.Contains(t, text, "actual launch environment")
					assert.Contains(t, text, "restart the host/MCP process")
				}
				p.close(t)
				stdioCheckpointed(t, mainDir)
				assert.NoFileExists(t, filepath.Join(linked, "knowledge.db"))
			}
		})
	}
}

func TestGeneratedPreCommitProjectCredentials(t *testing.T) {
	bin := buildStdioCapy(t)
	const inherited = "synthetic-unrelated-git-launch-key-123456"
	for _, source := range []string{"key file", "dotenv"} {
		t.Run(source, func(t *testing.T) {
			project := stdioProject(t)
			env := wrapperEnv(t, bin, inherited)
			wrapperGit(t, project, env, "init", "-q")
			wrapperGit(t, project, env, "commit", "--allow-empty", "-qm", "initial")
			writeMCPProjectCredential(t, project, source, projectFileTestKey)
			setupWrapperProject(t, bin, project, env)
			dbPath := filepath.Join(project, "knowledge.db")
			st := seedKeyResolutionDB(t, dbPath, project)
			wrapperGit(t, project, env, "add", "-f", "knowledge.db")

			// A live reader pins a WAL snapshot before a subsequent write. The
			// generated hook must propagate the real checkpoint's busy failure.
			db, err := sql.Open("sqlite3", store.EncryptedDSN(dbPath, projectFileTestKey)+"&_journal_mode=WAL")
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, db.Close()) })
			tx, err := db.Begin()
			require.NoError(t, err)
			defer tx.Rollback()
			var count int
			require.NoError(t, tx.QueryRow("SELECT count(*) FROM sources").Scan(&count))
			require.Equal(t, 1, count)
			_, err = st.Index("# Pending\n\nRetained WAL content.", "pending-marker", "", store.KindDurable)
			require.NoError(t, err)
			head := wrapperGit(t, project, env, "rev-parse", "HEAD")
			out, err := runWrapperCommand(t, stdioCommand{
				executable: "git", args: []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "blocked"},
				dir: project, env: env, secrets: []string{projectFileTestKey, inherited},
			})
			require.Error(t, err)
			assert.Contains(t, out, "pages busy")
			assert.Contains(t, out, "checkpoint failed; commit aborted")
			assert.Equal(t, head, wrapperGit(t, project, env, "rev-parse", "HEAD"))
			require.NoError(t, tx.Rollback())
			require.NoError(t, db.Close())
			wrapperGit(t, project, env, "commit", "-qm", "checkpointed")
			assert.NotEqual(t, head, wrapperGit(t, project, env, "rev-parse", "HEAD"))
			assert.Equal(t, wrapperGit(t, project, env, "hash-object", "knowledge.db"), wrapperGit(t, project, env, "rev-parse", "HEAD:knowledge.db"))
			require.NoError(t, st.Close())
			stdioCheckpointed(t, project)
			sources, err := st.ListSources()
			require.NoError(t, err)
			require.Len(t, sources, 2)
			assert.ElementsMatch(t, []string{"project-B-marker", "pending-marker"}, []string{sources[0].Label, sources[1].Label})
		})
	}

	t.Run("DB repository guard needs no key or wrapper", func(t *testing.T) {
		project, env := t.TempDir(), wrapperEnv(t, bin, "")
		wrapperGit(t, project, env, "init", "-q")
		out, err := runWrapperCommand(t, stdioCommand{
			executable: bin, args: []string{"setup", "--db-repo"}, dir: project, env: env,
		})
		require.NoError(t, err, out)
		seedKeyResolutionDB(t, filepath.Join(project, "knowledge.db"), project)
		assert.NoFileExists(t, filepath.Join(project, ".claude", "scripts", "capy.sh"))
		wrapperGit(t, project, env, "add", "knowledge.db")
		wrapperGit(t, project, env, "commit", "-qm", "encrypted checkpointed fixture")
	})
}
