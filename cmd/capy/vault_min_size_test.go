package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultMinSessionBytesCLI(t *testing.T) {
	root, _ := setupVaultEnv(t)
	project := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"),
		[]byte("[vault]\nmin_session_bytes = 1048576\n"), 0o644))
	sourceVault := filepath.Join(t.TempDir(), "source.db")
	_, stderr, code := capy(t, "vault", "import", "--source", root, "--path", sourceVault,
		"--project-dir", project, "--min-size-bytes", "0")
	require.Equal(t, 0, code, "%s", stderr)

	for _, operation := range []string{"import", "merge"} {
		t.Run(operation, func(t *testing.T) {
			args := []string{"vault", operation, "--project-dir", project,
				"--path", filepath.Join(t.TempDir(), "dest.db")}
			if operation == "import" {
				args = append(args, "--source", root)
			} else {
				args = append(args, "--from", sourceVault)
			}
			for _, dry := range []bool{true, false} {
				runArgs := append([]string(nil), args...)
				if dry {
					runArgs = append(runArgs, "--dry-run")
				}
				stdout, stderr, code := capy(t, runArgs...)
				require.Equal(t, 0, code, "%s", stderr)
				assert.Contains(t, stdout, "excluded 1")
				assert.Contains(t, stdout, "below minimum size")
			}
			stdout, stderr, code := capy(t, append(args, "--min-size-bytes", "0")...)
			require.Equal(t, 0, code, "%s", stderr)
			assert.Contains(t, stdout, "imported 1")
			_, stderr, code = capy(t, append(args, "--min-size-bytes", "-1")...)
			require.NotZero(t, code)
			assert.Contains(t, stderr, "--min-size-bytes must be >= 0")
		})
	}
}

func TestVaultImportSingleClaudeFile(t *testing.T) {
	root, id, main, sidecar := setupVaultWithSidecar(t)
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"),
		[]byte("[vault]\nmin_session_bytes = 1048576\n"), 0o600))
	source := filepath.Join(root, id+".jsonl")
	sibling := filepath.Join(root, "eeeeeeee-1111-2222-3333-444444444444.jsonl")
	require.NoError(t, os.WriteFile(sibling, main, 0o600))
	run := func(args ...string) string {
		t.Helper()
		stdout, stderr, code := capy(t, append([]string{"--project-dir", project, "vault"}, args...)...)
		require.Zero(t, code, stderr)
		return stdout
	}
	require.Contains(t, run("import", "--source", source, "--min-size-bytes", "1048576"), "below minimum size")
	require.Contains(t, run("import", "--source", source, "--platform", "codex"), "no sessions matched")
	require.Contains(t, run("import", "--source", source, "--dry-run"), "imported 1")
	require.Contains(t, run("list"), "no sessions archived")
	require.Contains(t, run("import", "--source", source), "imported 1")
	require.NotContains(t, run("list", "--json"), "eeeeeeee")
	out := t.TempDir()
	run("restore", id, "--output", out)
	got, err := os.ReadFile(filepath.Join(out, id, "subagents", "agent-7.jsonl"))
	require.NoError(t, err)
	require.Equal(t, sidecar, got)
	run("delete", id, "--yes")
	require.Contains(t, run("import", "--source", source), "deleted")
	require.NoError(t, os.WriteFile(sibling, []byte(`{"type":"ai-title","aiTitle":"metadata only"}`), 0o600))
	require.Contains(t, run("import", "--source", sibling), "no messages")
}
