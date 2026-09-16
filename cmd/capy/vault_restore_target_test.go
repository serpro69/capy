package main

import (
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRestoreTarget_ResolvesThePlatformBeforeChoosingARoot pins the Task 14
// review's P2: `restore` used to read the platform off the row through
// OrClaude, so any value that was not exactly "codex" — including a corrupted
// Codex row — restored under the Claude projects tree at
// <projects>/<codex relative path>/<uuid>.jsonl. The target is now decided from
// vault.ResolveSessionPlatform: a corrupted token is sniffed from the blob, and
// an undetectable one is an error before anything is written.
func TestRestoreTarget_ResolvesThePlatformBeforeChoosingARoot(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	const (
		uuid = "01a0abcc-a1d5-7682-88a3-aec9626687f2"
		rel  = "sessions/2026/09/16/rollout-2026-09-16T21-58-29-" + uuid + ".jsonl"
	)
	codexBlob := []byte(`{"timestamp":"2026-09-16T19:58:29Z","type":"session_meta","payload":{"id":"` + uuid + `","cwd":"/p"}}` + "\n")

	t.Run("recognized codex row restores under the codex home at its hint", func(t *testing.T) {
		sess := &vault.Session{UUID: uuid, Platform: vault.PlatformCodex, ClaudeProjectDir: rel, RawJSONL: codexBlob}
		root, mainRel, err := restoreTarget(sess, "")
		require.NoError(t, err)
		assert.Equal(t, codexHome, root)
		assert.Equal(t, rel, mainRel)
	})

	t.Run("corrupted token over a codex blob is sniffed, not defaulted to claude", func(t *testing.T) {
		sess := &vault.Session{UUID: uuid, Platform: "bogus", ClaudeProjectDir: rel, RawJSONL: codexBlob}
		root, mainRel, err := restoreTarget(sess, "")
		require.NoError(t, err)
		assert.Equal(t, codexHome, root, "must not land under the Claude projects tree")
		assert.Equal(t, rel, mainRel)
	})

	t.Run("corrupted token over an undetectable blob fails before any path is chosen", func(t *testing.T) {
		sess := &vault.Session{UUID: uuid, Platform: "bogus", ClaudeProjectDir: rel, RawJSONL: []byte("garbage\n")}
		root, mainRel, err := restoreTarget(sess, "")
		require.Error(t, err)
		assert.ErrorIs(t, err, vault.ErrUndetectableFormat)
		assert.Contains(t, err.Error(), uuid)
		assert.Empty(t, root)
		assert.Empty(t, mainRel)
	})

	t.Run("claude rows keep their projects-dir target", func(t *testing.T) {
		sess := &vault.Session{UUID: uuid, ClaudeProjectDir: "-home-user-proj", RawJSONL: []byte(`{"type":"last-prompt"}` + "\n")}
		root, mainRel, err := restoreTarget(sess, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cfg, "projects", "-home-user-proj"), root)
		assert.Equal(t, uuid+".jsonl", mainRel)
	})

	t.Run("--output overrides the root but not the relative path", func(t *testing.T) {
		sess := &vault.Session{UUID: uuid, Platform: vault.PlatformCodex, ClaudeProjectDir: rel, RawJSONL: codexBlob}
		root, mainRel, err := restoreTarget(sess, "/tmp/elsewhere")
		require.NoError(t, err)
		assert.Equal(t, "/tmp/elsewhere", root)
		assert.Equal(t, rel, mainRel)
	})
}
