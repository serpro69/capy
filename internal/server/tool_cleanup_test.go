package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── optimize / vacuum (issue #82: parity with `capy cleanup --optimize`) ──────

func TestCleanup_OptimizeStandaloneIgnoresDryRunDefault(t *testing.T) {
	srv := newTestServer(t, nil)
	callIndex(t, srv, map[string]any{"content": "# Auth\n\nJWT validation middleware.", "source": "auth-doc"})

	// optimize alone, dry_run left at its default: reclamation is maintenance,
	// not eviction, so it must run rather than silently no-op (ADR-029 §4).
	r := callCleanup(t, srv, map[string]any{"optimize": true})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "## Cleanup (optimize)")
	assert.Contains(t, text, "Optimize complete")
	assert.NotContains(t, text, "skipped")

	// Content survives the FTS rebuild.
	sources, err := srv.getStore().ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "auth-doc", sources[0].Label)
}

func TestCleanup_VacuumStandalone(t *testing.T) {
	srv := newTestServer(t, nil)

	r := callCleanup(t, srv, map[string]any{"vacuum": true})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "## Cleanup (vacuum)")
	assert.Contains(t, text, "Vacuum complete")
}

func TestCleanup_OptimizeSupersedesVacuum(t *testing.T) {
	srv := newTestServer(t, nil)

	r := callCleanup(t, srv, map[string]any{"optimize": true, "vacuum": true})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "## Cleanup (optimize)")
	assert.Contains(t, text, "Optimize complete")
	assert.NotContains(t, text, "Vacuum complete")
}

func TestCleanup_OptimizeSkippedLoudlyOnDryRunEviction(t *testing.T) {
	srv := newTestServer(t, nil)

	// An eviction mode with the dry_run default: the preview runs, but
	// reclamation must not — and the response must say so, not stay silent.
	for _, args := range []map[string]any{
		{"optimize": true, "purge_ephemeral": true},
		{"optimize": true, "purge_session": true},
		{"optimize": true, "purge_all": true},
		{"vacuum": true, "purge_ephemeral": true},
	} {
		r := callCleanup(t, srv, args)
		assert.False(t, r.IsError, "args=%v", args)
		text := resultText(r)
		assert.Contains(t, text, "skipped (dry run)", "args=%v", args)
		assert.NotContains(t, text, "complete", "args=%v", args)
	}
}

func TestCleanup_OptimizeRunsAfterRealEviction(t *testing.T) {
	srv := newTestServer(t, nil)

	r := callCleanup(t, srv, map[string]any{"optimize": true, "dry_run": false})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "No evictable sources found.")
	assert.Contains(t, text, "Optimize complete")
}

func TestCleanup_OptimizeRunsAfterSourceEviction(t *testing.T) {
	srv := newTestServer(t, nil)
	callIndex(t, srv, map[string]any{"content": "# Doomed\n\nThis source will be evicted.", "source": "doomed"})
	callIndex(t, srv, map[string]any{"content": "# Kept\n\nThis source stays.", "source": "kept"})

	r := callCleanup(t, srv, map[string]any{"source": "doomed", "optimize": true, "dry_run": false})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, `Source "doomed"`)
	assert.Contains(t, text, "removed")
	assert.Contains(t, text, "Optimize complete")

	sources, err := srv.getStore().ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, "kept", sources[0].Label)
}

func TestCleanup_OptimizeRunsAfterPurgeAll(t *testing.T) {
	srv := newTestServer(t, nil)
	callIndex(t, srv, map[string]any{"content": "# Gone\n\nEverything goes.", "source": "gone"})

	r := callCleanup(t, srv, map[string]any{"purge_all": true, "optimize": true, "dry_run": false})
	assert.False(t, r.IsError)
	text := resultText(r)
	assert.Contains(t, text, "Knowledge base reset")
	assert.Contains(t, text, "Optimize complete")

	sources, err := srv.getStore().ListSources()
	require.NoError(t, err)
	assert.Empty(t, sources)
}
