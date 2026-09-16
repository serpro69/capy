package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// filterCodexByProject compares the rollout's recorded cwd with the server's
// project dir in canonical form (design Assumption 10). Pinned here as a pure
// table so the rules are legible without the sweep fixture: symlinks and
// redundant path elements do not break a match, a different directory never
// matches, and a rollout with no cwd hint is dropped — it cannot be attributed
// to any project, so only CAPY_VAULT_SWEEP_ALL or `capy vault import` reaches it.
func TestFilterCodexByProject(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "project-link")
	require.NoError(t, os.Symlink(real, link))
	other := t.TempDir()
	// A cwd whose directory no longer exists still compares by its cleaned form.
	stale := filepath.Join(t.TempDir(), "gone")

	sf := func(uuid, cwd string) vault.SessionFile {
		return vault.SessionFile{Platform: vault.PlatformCodex, UUID: uuid, ProjectPath: cwd}
	}
	all := []vault.SessionFile{
		sf("real", real),
		sf("via-link", link),
		sf("unclean", filepath.Join(real, "sub", "..")+string(filepath.Separator)),
		sf("other", other),
		sf("no-hint", ""),
		sf("stale", stale),
		sf("stale-unclean", filepath.Join(stale, ".")),
	}

	tests := []struct {
		name       string
		projectDir string
		want       []string
	}{
		{"real path matches itself, its symlink and its unclean spelling", real, []string{"real", "via-link", "unclean"}},
		{"server handed the symlink matches the same set", link, []string{"real", "via-link", "unclean"}},
		{"another directory matches only itself", other, []string{"other"}},
		{"a vanished cwd still matches by cleaned path", stale, []string{"stale", "stale-unclean"}},
		{"nothing matches an unrelated dir", filepath.Join(t.TempDir(), "elsewhere"), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, m := range filterCodexByProject(all, tt.projectDir) {
				got = append(got, m.UUID)
			}
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "no-hint", "a rollout without a cwd hint is never attributed to a project")
		})
	}
}

// countProjects keys Claude sessions by mangled project dir and Codex rollouts
// by cwd hint, so the all-projects summary counts each platform's notion of a
// project once.
func TestCountProjects_PlatformAware(t *testing.T) {
	sessions := []vault.SessionFile{
		{Platform: vault.PlatformClaudeCode, ProjectDir: "-home-user-a"},
		{Platform: vault.PlatformClaudeCode, ProjectDir: "-home-user-a"},
		{ProjectDir: "-home-user-b"}, // empty platform is Claude (OrClaude)
		{Platform: vault.PlatformCodex, ProjectPath: "/home/user/a", ProjectDir: ""},
		{Platform: vault.PlatformCodex, ProjectPath: "/home/user/c"},
		{Platform: vault.PlatformCodex, ProjectPath: "/home/user/c"},
	}
	assert.Equal(t, 4, countProjects(sessions), "two Claude dirs + two Codex cwds")
}
