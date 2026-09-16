package vault

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codex_discovery_ctx_test.go covers the two Task 14 review P2s in discovery:
// the Codex walker honors the caller's context (the server sweep's 30 s budget
// used to be invisible to it — a large or cold corpus could spend the whole
// budget on first-line reads and then block shutdown), and DiscoverAll resolves
// only the roots it was asked for, per platform (a `--platform codex` run used
// to fail on an unresolvable $HOME that only the Claude root needs).

func TestDiscoverCodexSessions_HonorsContext(t *testing.T) {
	home := writeCodexDiscoveryHome(t)
	total, _, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	require.Greater(t, len(total), 2, "fixture must hold more rollouts than the mid-walk cancellation point")

	t.Run("pre-cancelled: the walk reads no first line", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		sessions, report, err := DiscoverCodexSessions(ctx, home, CodexDiscoverOptions{})
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, sessions)
		assert.Equal(t, 0, report.FirstLineReads, "cancellation is checked before any open")
	})

	t.Run("cancelled mid-walk: what was found so far comes back with the error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		accepted := 0
		opts := CodexDiscoverOptions{Skip: func(string, int64, bool) bool {
			accepted++
			if accepted == 2 {
				cancel() // the budget runs out while the second rollout is being processed
			}
			return false
		}}
		sessions, report, err := DiscoverCodexSessions(ctx, home, opts)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Len(t, sessions, 2, "the rollouts accepted before the cancellation was observed are returned")
		assert.Equal(t, 2, report.FirstLineReads, "no first line is read after the cancellation")
	})

	t.Run("DiscoverAll propagates a cancelled walk as an error, not an empty result", func(t *testing.T) {
		t.Setenv("CODEX_HOME", home)
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		sessions, _, err := DiscoverAll(ctx, nil, PlatformCodex)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, sessions)
	})
}

func TestDiscoverAll_ResolvesOnlySelectedRoots(t *testing.T) {
	home := writeCodexDiscoveryHome(t)
	want, _, err := DiscoverCodexSessions(context.Background(), home, CodexDiscoverOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, want)

	// No resolvable home directory, and only the Codex override set: the Claude
	// root cannot be resolved (ClaudeProjectsDir falls back to $HOME), the Codex
	// root can (CODEX_HOME needs no home). USERPROFILE is what os.UserHomeDir
	// reads on Windows.
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	ctx := context.Background()

	t.Run("--platform codex never resolves the Claude root", func(t *testing.T) {
		h := captureSlog(t)
		sessions, _, err := DiscoverAll(ctx, nil, PlatformCodex)
		require.NoError(t, err)
		assert.Len(t, sessions, len(want))
		assert.Empty(t, h.messagesWithPrefix("vault discovery: skipping platform whose root"), "the unselected platform is not even attempted")
	})

	t.Run("--platform claude-code surfaces its own resolution error", func(t *testing.T) {
		_, _, err := DiscoverAll(ctx, nil, PlatformClaudeCode)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving claude projects dir")
	})

	t.Run("no filter: the unresolvable platform is warned and skipped, the other is walked", func(t *testing.T) {
		h := captureSlog(t)
		sessions, _, err := DiscoverAll(ctx, nil)
		require.NoError(t, err)
		assert.Len(t, sessions, len(want))
		warns := h.recordsWithMessage("vault discovery: skipping platform whose root cannot be resolved")
		require.Len(t, warns, 1)
		assert.Equal(t, PlatformClaudeCode, recordAttrs(warns[0])["platform"])
	})

	t.Run("an unknown platform in the filter is an error, not an empty result", func(t *testing.T) {
		sessions, _, err := DiscoverAll(ctx, nil, Platform("bogus"))
		require.ErrorIs(t, err, ErrUnknownPlatform)
		assert.Empty(t, sessions)
	})

	t.Run("every selected platform unresolvable is an error, not an empty result", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		h := captureSlog(t)
		sessions, _, err := DiscoverAll(ctx, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolving claude projects dir")
		assert.Contains(t, err.Error(), "resolving codex home")
		assert.Empty(t, sessions)
		assert.Empty(t, h.messagesWithPrefix("vault discovery: skipping platform whose root"), "returned, not also logged")
	})
}
