package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinPlatformRoots points both platform roots at fresh temp dirs and returns
// them (the Claude projects dir and the Codex home), creating neither.
func pinPlatformRoots(t *testing.T) (claudeProjects, codexHome string) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	codexHome = filepath.Join(t.TempDir(), ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	return filepath.Join(cfg, "projects"), codexHome
}

func TestPlatformRoots(t *testing.T) {
	t.Run("both roots present, counts from stats", func(t *testing.T) {
		claude, codex := pinPlatformRoots(t)
		require.NoError(t, os.MkdirAll(claude, 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(codex, "archived_sessions"), 0o755)) // either rollout root counts

		stats := &VaultStats{ByPlatform: []PlatformStat{
			{Platform: PlatformCodex, Sessions: 145},
			{Platform: PlatformClaudeCode, Sessions: 42},
		}}
		roots, err := PlatformRoots(stats)
		require.NoError(t, err)
		assert.Equal(t, []PlatformRoot{
			{Platform: PlatformClaudeCode, Root: claude, RootExists: true, Archived: 42},
			{Platform: PlatformCodex, Root: codex, RootExists: true, Archived: 145},
		}, roots, "knownPlatforms order regardless of ByPlatform order")
	})

	t.Run("codex home absent", func(t *testing.T) {
		claude, codex := pinPlatformRoots(t)
		require.NoError(t, os.MkdirAll(claude, 0o755))

		roots, err := PlatformRoots(&VaultStats{})
		require.NoError(t, err)
		require.Len(t, roots, 2)
		assert.True(t, roots[0].RootExists)
		assert.Equal(t, PlatformRoot{Platform: PlatformCodex, Root: codex}, roots[1])
	})

	t.Run("codex home without a rollout root is absent", func(t *testing.T) {
		// The sweep probes HasCodexRolloutRoot, not the home dir itself; the
		// doctor must agree with the sweep.
		_, codex := pinPlatformRoots(t)
		require.NoError(t, os.MkdirAll(codex, 0o755))

		roots, err := PlatformRoots(nil)
		require.NoError(t, err)
		assert.False(t, roots[1].RootExists)
		assert.False(t, roots[0].RootExists, "projects dir never created")
	})

	t.Run("nil stats yields zero counts", func(t *testing.T) {
		claude, codex := pinPlatformRoots(t)
		require.NoError(t, os.MkdirAll(claude, 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(codex, "sessions"), 0o755))

		roots, err := PlatformRoots(nil)
		require.NoError(t, err)
		for _, r := range roots {
			assert.True(t, r.RootExists, r.Platform)
			assert.Zero(t, r.Archived, r.Platform)
		}
	})

	t.Run("unknown platform row is ignored", func(t *testing.T) {
		pinPlatformRoots(t)
		roots, err := PlatformRoots(&VaultStats{ByPlatform: []PlatformStat{{Platform: "bogus", Sessions: 9}}})
		require.NoError(t, err)
		require.Len(t, roots, 2)
		assert.Zero(t, roots[0].Archived)
		assert.Zero(t, roots[1].Archived)
	})

	t.Run("unresolvable home is an error", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		t.Setenv("CODEX_HOME", "")
		t.Setenv("HOME", "")
		t.Setenv("USERPROFILE", "") // os.UserHomeDir reads this on Windows
		_, err := PlatformRoots(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving codex home")
	})
}

// TestResolvePlatformRoots_CoversKnownPlatforms pins the explicit root list to
// the closed platform set: a new Platform constant must gain a root and probe
// here (see the resolvePlatformRoots comment), in knownPlatforms order.
func TestResolvePlatformRoots_CoversKnownPlatforms(t *testing.T) {
	pinPlatformRoots(t)
	roots, err := resolvePlatformRoots()
	require.NoError(t, err)
	got := make([]Platform, 0, len(roots))
	for _, r := range roots {
		got = append(got, r.Platform)
	}
	assert.Equal(t, knownPlatforms, got, "resolvePlatformRoots must enumerate every known platform, in order")
}
