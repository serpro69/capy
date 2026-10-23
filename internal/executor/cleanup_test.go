package executor

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWholeRunCleanupAcrossPhases(t *testing.T) {
	for _, scope := range []CleanupScope{CleanupLinuxDescendants, CleanupDarwinObservation} {
		t.Run(string(scope), func(t *testing.T) {
			complete := CleanupRetired
			if scope == CleanupDarwinObservation {
				complete = CleanupObserved
			}
			for _, tc := range []struct {
				name                 string
				prior, current, want CleanupState
			}{
				{"nothing spawned", CleanupNotSpawned, CleanupNotSpawned, CleanupNotSpawned},
				{"runtime completed", CleanupNotSpawned, complete, complete},
				{"runtime failed to start after compiler", complete, CleanupNotSpawned, complete},
				{"both phases completed", complete, complete, complete},
				{"compiler unresolved", CleanupPending, CleanupNotSpawned, CleanupPending},
				{"runtime unresolved", complete, CleanupPending, CleanupPending},
			} {
				t.Run(tc.name, func(t *testing.T) {
					phaseErr := errors.New("preserve actual phase failure")
					proof, err := combinePhaseCleanup(CleanupEvidence{scope, tc.prior}, CleanupEvidence{scope, tc.current}, phaseErr)
					require.ErrorIs(t, err, phaseErr)
					require.Equal(t, CleanupEvidence{scope, tc.want}, proof)
				})
			}
		})
	}
	for _, bad := range []CleanupEvidence{{}, {CleanupLinuxDescendants, CleanupObserved}, {CleanupDarwinObservation, CleanupRetired}, {"unknown", CleanupRetired}} {
		proof, err := combinePhaseCleanup(CleanupEvidence{CleanupLinuxDescendants, CleanupRetired}, bad, nil)
		require.Error(t, err)
		require.Equal(t, CleanupPending, proof.State)
	}
}
