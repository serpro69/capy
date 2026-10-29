package execution

import (
	"context"
	"database/sql"
	"testing"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
)

func assertRuntimeIncident(t *testing.T, s *Service, id, batch, cause, stage, phase string) {
	t.Helper()
	view, err := runstore.InspectRuns(t.Context(), s.store.Path(), s.key, runstore.InspectionOptions{RunID: id, Limit: 1})
	require.NoError(t, err)
	require.Len(t, view.Subjects, 1)
	subject := view.Subjects[0]
	require.Equal(t, id, subject.RunID)
	require.Equal(t, s.owner.ID, subject.Generation)
	require.Equal(t, s.owner.Slot, *subject.Slot)
	require.Equal(t, "recorded", subject.Attribution)
	require.Equal(t, cause, subject.Reason)
	require.Equal(t, stage, subject.DetectionStage)
	require.Equal(t, phase, subject.Phase)
	require.NotEmpty(t, subject.IncidentID)
	require.NotNil(t, subject.DetectedAt)
	require.True(t, subject.BlocksAdmission)
	row, err := s.store.Load(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, batch, row.Result.BatchID)
}

func TestServiceQuarantineRetainsBoundedVolatileContext(t *testing.T) {
	s := NewService(t.TempDir(), "synthetic-runtime-key", config.DefaultConfig().Runs)
	st, owner, err := s.attached(t.Context(), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	first, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	second, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	// Close the authenticated handle to force publication failure. The still-held
	// owner lease and service latch are independent of database writability.
	require.NoError(t, st.Close())
	require.Error(t, s.quarantine(st, first, runstore.IncidentStartupUncertain, runstore.StageStartup))
	require.Error(t, s.quarantine(st, second, runstore.IncidentSupervisorLost, runstore.StageHelperWait))
	_, done, err := s.begin(t.Context(), true)
	require.Nil(t, done)
	require.ErrorContains(t, err, first.ID)
	require.ErrorContains(t, err, first.Generation)
	require.ErrorContains(t, err, "startup_uncertain at startup")
	require.ErrorContains(t, err, "volatile attribution; persistence unconfirmed")
	require.NotContains(t, err.Error(), second.ID, "only one bounded subject is cached")
	require.NotContains(t, err.Error(), "durable attribution")
	s.mu.Lock()
	batchErr := s.unverifiedErrorLocked()
	s.mu.Unlock()
	require.Equal(t, err.Error(), batchErr.Message)
	_, done, err = s.begin(t.Context(), false)
	require.NoError(t, err, "the latch must not disable queries")
	done()
}

func TestServiceQuarantineSurvivesAttributionTransactionLoss(t *testing.T) {
	for _, mode := range []string{"rollback", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s := NewService(t.TempDir(), "synthetic-runtime-key", config.DefaultConfig().Runs)
			st, owner, err := s.attached(t.Context(), true)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, s.Close()) })
			row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
			require.NoError(t, err)
			require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, row.Cleanup.Scope))
			db, err := sql.Open("sqlite3", sqliteutil.EncryptedDSN(st.Path(), s.key))
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			t.Cleanup(func() {
				_, err := db.Exec("DROP TRIGGER IF EXISTS runtime_loss")
				require.NoError(t, err)
				require.NoError(t, db.Close())
			})
			fault := "SELECT RAISE(ROLLBACK,'PRIVATE_TRANSACTION_ERROR');"
			if mode == "deadline" {
				// Bounded by the production write deadline. Force SQLite work after
				// the marker UPDATE, rather than sleeping or timing a racing writer.
				fault = "SELECT (WITH RECURSIVE slow(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM slow WHERE n<100000000) SELECT sum(n) FROM slow);"
			}
			_, err = db.Exec("CREATE TRIGGER runtime_loss BEFORE INSERT ON run_incidents BEGIN " + fault + " END")
			require.NoError(t, err)
			err = s.quarantine(st, row, runstore.IncidentFinalizationUnpersisted, runstore.StageFinalization)
			require.Error(t, err)
			if mode == "deadline" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.NotContains(t, err.Error(), "PRIVATE_TRANSACTION_ERROR")
			var blocked bool
			var epoch, incidents int
			require.NoError(t, db.QueryRow("SELECT admission_blocked,recovery_revision,(SELECT count(*) FROM run_incidents) FROM run_meta").Scan(&blocked, &epoch, &incidents))
			require.True(t, blocked, "marker-only fallback must survive lost attribution transaction")
			require.Equal(t, 1, epoch)
			require.Zero(t, incidents)
			require.ErrorContains(t, st.GuardAdmission(t.Context()), "unverified cleanup")
			_, done, err := s.begin(t.Context(), true)
			require.Nil(t, done)
			require.ErrorContains(t, err, "volatile attribution; persistence unconfirmed")
		})
	}
}
