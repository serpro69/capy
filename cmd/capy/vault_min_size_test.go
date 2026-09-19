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
