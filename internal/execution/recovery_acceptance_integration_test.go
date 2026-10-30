//go:build (linux || darwin) && cgo && integration

package execution

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

// Exercise recovery after a real native helper reaches cleanup but cannot
// persist it. The injected failure happens after local cleanup; this fixture
// knows its originating command has stopped. A free lease alone is not that
// evidence, and the resulting disposition must not become execution proof.
func TestNativeRecoveryAfterRuntimeIncident(t *testing.T) {
	for _, mode := range []string{"finish failure", "helper loss"} {
		t.Run(mode, func(t *testing.T) {
			s := newRuntimeIncidentService(t, "")
			sibling := s.Execute(t.Context(), executor.ExecRequest{
				Language: executor.Shell, Code: "printf 'retained sibling output'; printf x >> sibling-once",
			}, "", 30*time.Second)
			require.NotNil(t, sibling.RunID)
			require.True(t, sibling.Capture.TerminalPersisted)
			prefix, err := s.ReadRaw(t.Context(), *sibling.RunID, "stdout", "", 7)
			require.NoError(t, err)
			require.NotNil(t, prefix.NextCursor)

			if runtime.GOOS == "darwin" {
				t.Setenv("CAPY_DARWIN_CAPTURE_MODE", mode)
			} else {
				fixtureMode := mode
				if mode == "finish failure" {
					fixtureMode = "failed finish"
				}
				t.Setenv("CAPY_STORED_MODE", fixtureMode)
			}
			uncertain := s.Execute(t.Context(), executor.ExecRequest{
				Language: executor.Shell, Code: "printf 'retained uncertain output'; printf x >> uncertain-once; exit 7",
			}, "", 30*time.Second)
			require.NotNil(t, uncertain.RunID)
			require.Equal(t, executor.CleanupPending, uncertain.Cleanup.State)
			require.False(t, uncertain.Capture.TerminalPersisted)
			path := s.store.Path()
			original := recoveryAcceptanceResult(t, path, s.key, *uncertain.RunID)
			siblingOriginal := recoveryAcceptanceResult(t, path, s.key, *sibling.RunID)
			require.NoError(t, s.Close())

			// A different session must refuse before admission and retain reads.
			peer := NewService(s.project, s.key, s.policy)
			t.Cleanup(func() { require.NoError(t, peer.Close()) })
			refused := peer.Execute(t.Context(), executor.ExecRequest{
				Language: executor.Shell, Code: "touch forbidden",
			}, "", time.Second)
			require.Nil(t, refused.RunID)
			require.NotNil(t, refused.Error)
			require.Equal(t, "cleanup_unverified", refused.Error.Code)
			require.NotNil(t, refused.Error.RecoveryHint)
			require.Equal(t, "retained uncertain output", runtimeRawText(t, peer, *uncertain.RunID, "stdout"))
			options := runstore.RecoveryPlanOptions{Limit: 16, Timeout: time.Minute}
			_, err = runstore.PlanRecovery(t.Context(), path, s.key, options)
			var busy *runstore.Error
			require.ErrorAs(t, err, &busy)
			require.Equal(t, "storage_busy", busy.Code)
			require.NoError(t, peer.Close())

			plan, err := runstore.PlanRecovery(t.Context(), path, s.key, options)
			require.NoError(t, err)
			require.True(t, plan.Complete)
			require.EqualValues(t, 1, plan.Counts.Dispositions)
			applied, err := runstore.ApplyRecovery(t.Context(), path, s.key, runstore.RecoveryApplyOptions{
				Plan: plan.Token, ConfirmStopped: true, Basis: "groups_stopped",
				Explanation: "Native fixture completed local cleanup and joined its helper", Timeout: time.Minute,
			})
			require.NoError(t, err)
			require.NotNil(t, applied.Admission)
			require.False(t, applied.Admission.Blocked)
			require.NotNil(t, applied.Recovery)

			t.Setenv("CAPY_DARWIN_CAPTURE_MODE", "")
			t.Setenv("CAPY_STORED_MODE", "")
			fresh := NewService(s.project, s.key, s.policy)
			t.Cleanup(func() { require.NoError(t, fresh.Close()) })
			verified := fresh.Execute(t.Context(), executor.ExecRequest{
				Language: executor.Shell, Code: "printf verification",
			}, "", 30*time.Second)
			require.NotNil(t, verified.RunID)
			require.NotNil(t, verified.ExitCode)
			require.Equal(t, 0, *verified.ExitCode)
			require.True(t, verified.Capture.TerminalPersisted)
			require.True(t, verified.Cleanup.Complete())
			require.Nil(t, verified.Recovery)
			suffix, err := fresh.ReadRaw(t.Context(), *sibling.RunID, "stdout", *prefix.NextCursor, 65536)
			require.NoError(t, err)
			var retained []byte
			for _, page := range []runstore.ReadPage{prefix, suffix} {
				for _, fragment := range page.Fragments {
					retained = append(retained, fragment.Data...)
				}
			}
			require.Equal(t, "retained sibling output", string(retained))
			page, err := fresh.ReadRaw(t.Context(), *uncertain.RunID, "stdout", "", 65536)
			require.NoError(t, err)
			require.False(t, page.EOF)
			require.True(t, page.EndOfAvailable)
			require.NotNil(t, page.Run.Recovery)
			require.True(t, page.Run.Recovery.CaptureClosed)
			require.Equal(t, applied.Recovery.ID, page.Run.Recovery.ReceiptID)
			require.Equal(t, executor.CleanupPending, page.Run.Cleanup.State)
			require.False(t, page.Run.Capture.TerminalPersisted)
			require.Equal(t, "retained uncertain output", runtimeRawText(t, fresh, *uncertain.RunID, "stdout"))
			require.Equal(t, original, recoveryAcceptanceResult(t, path, s.key, *uncertain.RunID))
			require.Equal(t, siblingOriginal, recoveryAcceptanceResult(t, path, s.key, *sibling.RunID))
			for _, marker := range []string{"sibling-once", "uncertain-once"} {
				data, err := os.ReadFile(filepath.Join(s.project, marker))
				require.NoError(t, err)
				require.Equal(t, "x", string(data), "recovery must not replay either command")
			}
			require.NoFileExists(t, filepath.Join(s.project, "forbidden"))
		})
	}
}

func recoveryAcceptanceResult(t *testing.T, path, key, id string) []byte {
	t.Helper()
	// The service holds its lifecycle lease throughout these snapshots.
	db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(path, key)+"&mode=ro")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var result []byte
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT result FROM runs WHERE id=?", id).Scan(&result))
	return result
}
