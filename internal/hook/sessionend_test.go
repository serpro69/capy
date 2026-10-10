package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleSessionEnd_StableSessionOnly(t *testing.T) {
	t.Setenv("CAPY_DB_KEY", "")
	t.Setenv("CAPY_VAULT_KEY", "")
	s, now := observationFixture(t)
	project := filepath.Dir(s.dir)
	for _, agent := range []string{"child-1", "child-2"} {
		require.NoError(t, s.record("ending/session", agent, observedExecute))
	}
	require.NoError(t, s.record("live-session", "sibling", observedFetch))
	before, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	handleSessionEnd(project, &adapter.PreToolUseEvent{SessionID: "ending/session"})
	unchanged, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	assert.Equal(t, before, unchanged, "unstable identities must not delete observations")
	handleSessionEnd(project, &adapter.PreToolUseEvent{SessionID: "ending/session", SessionIDStable: true})
	state, err := readObservations(observationFile(s), now.UnixMilli())
	require.NoError(t, err)
	require.Len(t, state.Entries, 1)
	assert.Equal(t, "live-session", state.Entries[0].Session)
	assert.Equal(t, now.UnixMilli(), state.Entries[0].Tools[observedFetch], "even expired sibling entries must be untouched by SessionEnd")
	files, err := os.ReadDir(s.dir)
	require.NoError(t, err)
	assert.Len(t, files, 2, "cleanup retains only observation data and its permanent lock; no DB access")
}

func TestHandleSessionEnd_MissingState(t *testing.T) {
	project := t.TempDir()
	event := &adapter.PreToolUseEvent{SessionID: "session", SessionIDStable: true}
	handleSessionEnd("", event)
	handleSessionEnd("/nonexistent/path", event)
	handleSessionEnd(project, event)
	assert.NoDirExists(t, filepath.Join(project, ".capy"))
}
