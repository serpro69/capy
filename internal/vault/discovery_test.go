package vault

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverSessions_ProjectsRoot(t *testing.T) {
	root := t.TempDir()
	writeSession(t, filepath.Join(root, "-home-user-proja"), "aaaaaaaa-1111-2222-3333-444444444444",
		sampleMainJSONL(t), map[string][]byte{"tool-results/t1.json": []byte(`{"ok":true}`)})
	writeSession(t, filepath.Join(root, "-home-user-projb"), "bbbbbbbb-1111-2222-3333-444444444444",
		sampleMainJSONL(t), nil)

	sessions, err := DiscoverSessions(root)
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	// Sorted by path → proja before projb.
	assert.Equal(t, "-home-user-proja", sessions[0].ProjectDir)
	assert.Equal(t, "aaaaaaaa-1111-2222-3333-444444444444", sessions[0].UUID)
	require.Len(t, sessions[0].AssociatedFiles, 1)
	assert.Equal(t, "tool-results/t1.json", sessions[0].AssociatedFiles[0].RelativePath)

	assert.Equal(t, "-home-user-projb", sessions[1].ProjectDir)
	assert.Empty(t, sessions[1].AssociatedFiles)
}

func TestDiscoverSessions_SingleProjectDir(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")
	writeSession(t, projDir, "cccccccc-1111-2222-3333-444444444444", sampleMainJSONL(t),
		map[string][]byte{"subagents/agent-x.jsonl": []byte(`{"type":"user"}` + "\n")})

	sessions, err := DiscoverSessions(projDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "-home-user-proj", sessions[0].ProjectDir)
	require.Len(t, sessions[0].AssociatedFiles, 1)
	assert.Equal(t, "subagents/agent-x.jsonl", sessions[0].AssociatedFiles[0].RelativePath)
}

func TestDiscoverSessions_ConfigDir(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	writeSession(t, filepath.Join(projects, "-home-user-proj"), "dddddddd-1111-2222-3333-444444444444",
		sampleMainJSONL(t), nil)

	sessions, err := DiscoverSessions(root)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "dddddddd-1111-2222-3333-444444444444", sessions[0].UUID)
}

func TestDiscoverSessions_EmptyDirErrors(t *testing.T) {
	_, err := DiscoverSessions(t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session files found")
}

func TestDiscoverSessions_HonorsClaudeConfigDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	writeSession(t, filepath.Join(cfg, "projects", "-home-user-proj"), "eeeeeeee-1111-2222-3333-444444444444",
		sampleMainJSONL(t), nil)

	// Empty rootDir resolves via config.ClaudeProjectsDir() → $CLAUDE_CONFIG_DIR/projects.
	sessions, err := DiscoverSessions("")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "eeeeeeee-1111-2222-3333-444444444444", sessions[0].UUID)
}

func TestDiscoverSessions_OversizeSidecarCap(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")

	big := bytes.Repeat([]byte("a"), maxSidecarBytes+1)
	writeSession(t, projDir, "ffffffff-1111-2222-3333-444444444444", sampleMainJSONL(t), map[string][]byte{
		"tool-results/small.json":     []byte(`{"ok":true}`),
		"tool-results/big.json":       big, // non-subagent > 5 MB → skipped
		"subagents/agent-big.jsonl":   big, // subagent > 5 MB → kept (irreproducible)
		"subagents/agent-small.jsonl": []byte(`{"type":"user"}` + "\n"),
	})

	sessions, err := DiscoverSessions(projDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	got := map[string]bool{}
	for _, af := range sessions[0].AssociatedFiles {
		got[af.RelativePath] = true
	}
	assert.True(t, got["tool-results/small.json"], "small sidecar kept")
	assert.True(t, got["subagents/agent-big.jsonl"], "oversize subagent JSONL kept")
	assert.True(t, got["subagents/agent-small.jsonl"], "small subagent kept")
	assert.False(t, got["tool-results/big.json"], "oversize non-subagent sidecar skipped")
}

// TestDiscoverSessions_SkipsSymlinks verifies that symlinks are never collected
// as session files. A symlink's lstat Size() is the link-target path length (a
// few bytes), so it would bypass the maxSidecarBytes cap, and os.ReadFile would
// later follow it — reading an arbitrary or oversized target into the vault.
// Both the sidecar walk and the main-JSONL listing must reject non-regular files.
func TestDiscoverSessions_SkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "-home-user-proj")
	uuid := "aaaaaaaa-1111-2222-3333-444444444444"
	writeSession(t, projDir, uuid, sampleMainJSONL(t),
		map[string][]byte{"tool-results/real.json": []byte(`{"ok":true}`)})

	// A target outside the session tree that a malicious/innocent symlink might
	// point at (e.g. ~/.ssh/id_rsa, or a huge file).
	outside := filepath.Join(root, "outside-secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("sensitive"), 0o600))

	// Symlinked sidecar inside <uuid>/tool-results/ — must be skipped.
	sidecarLink := filepath.Join(projDir, uuid, "tool-results", "link.json")
	if err := os.Symlink(outside, sidecarLink); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	// Symlink masquerading as a second main session JSONL — must be skipped.
	mainLink := filepath.Join(projDir, "bbbbbbbb-1111-2222-3333-444444444444.jsonl")
	require.NoError(t, os.Symlink(outside, mainLink))

	sessions, err := DiscoverSessions(projDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1, "symlinked main .jsonl must not be discovered")
	assert.Equal(t, uuid, sessions[0].UUID)

	got := map[string]bool{}
	for _, af := range sessions[0].AssociatedFiles {
		got[af.RelativePath] = true
	}
	assert.True(t, got["tool-results/real.json"], "real regular sidecar kept")
	assert.False(t, got["tool-results/link.json"], "symlinked sidecar must be skipped")
}
