package execution

import (
	"testing"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestLinuxV1CleanupAdapter(t *testing.T) {
	for _, tc := range []struct {
		name             string
		started, retired bool
		state            executor.CleanupState
	}{
		{"not-started", false, true, executor.CleanupNotSpawned},
		{"retired", true, true, executor.CleanupRetired},
		{"unverified", true, false, executor.CleanupPending},
		{"uncertain-start", false, false, executor.CleanupPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := Outcome{Result: executor.StreamResult{Started: tc.started, Retired: tc.retired}}
			c, err := linuxV1Completion(executor.CleanupLinuxDescendants, out)
			require.NoError(t, err)
			require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: tc.state}, c.Cleanup)
			for _, scope := range []executor.CleanupScope{"", "unknown", executor.CleanupDarwinObservation} {
				_, err := linuxV1Completion(scope, out)
				require.Error(t, err)
			}
		})
	}
}

func TestLinuxV1FailedFinalizationKeepsDurablePending(t *testing.T) {
	for _, started := range []bool{false, true} {
		result := runstore.Result{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupNotSpawned}}
		out := Outcome{Result: executor.StreamResult{Started: started, Retired: true}}
		got := knownOutcome(result, out, nil)
		require.Equal(t, executor.CleanupPending, got.Cleanup.State)
		require.False(t, got.Capture.TerminalPersisted)
		out.TerminalPersisted = true
		got = knownOutcome(result, out, nil)
		want := executor.CleanupRetired
		if !started {
			want = executor.CleanupNotSpawned
		}
		require.Equal(t, want, got.Cleanup.State)
	}
}
