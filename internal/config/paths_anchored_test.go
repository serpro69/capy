package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectProjectRootFrom(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	for _, dir := range []string{root, other} {
		cmd := exec.Command("git", "init", "--quiet", dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	nested := filepath.Join(root, "nested", "deeper")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	// Git still outranks a nested capy marker, as in process-cwd discovery.
	require.NoError(t, os.WriteFile(filepath.Join(root, "nested", ".capy.toml"), nil, 0o600))
	t.Setenv("CLAUDE_PROJECT_DIR", other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_CEILING_DIRECTORIES", root)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.worktree")
	t.Setenv("GIT_CONFIG_VALUE_0", other)
	beforeCwd, err := os.Getwd()
	require.NoError(t, err)
	beforeEnv := os.Environ()
	want, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, want, DetectProjectRootFrom(nested))
	afterCwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, beforeCwd, afterCwd)
	assert.Equal(t, beforeEnv, os.Environ())
	assert.Equal(t, other, DetectProjectRoot(), "legacy environment override is preserved")
}

func TestDetectProjectRootFromMarkersAndFallback(t *testing.T) {
	// Make Git unavailable to exercise marker walking without inheriting the
	// repository of the test runner.
	t.Setenv("PATH", t.TempDir())
	for _, marker := range []string{".git", ".capy.toml", ".capy", ""} {
		t.Run("marker="+marker, func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, "nested")
			require.NoError(t, os.Mkdir(nested, 0o755))
			want := nested
			if marker != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, marker), nil, 0o600))
				want = root
			} else {
				// A temporary directory can itself be inside an ambient project.
				// Only exercise the no-marker fallback when its precondition holds.
				for dir := filepath.Dir(root); ; dir = filepath.Dir(dir) {
					for _, ancestorMarker := range []string{".git", ".capy.toml", ".capy"} {
						if _, err := os.Stat(filepath.Join(dir, ancestorMarker)); err == nil {
							t.Skipf("no-marker fallback requires unmarked ancestors; found %s", filepath.Join(dir, ancestorMarker))
						}
					}
					if filepath.Dir(dir) == dir {
						break
					}
				}
			}
			assert.Equal(t, want, DetectProjectRootFrom(nested))
		})
	}
}
