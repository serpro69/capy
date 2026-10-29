package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestAdmissionHintUsesOccupiedTransaction(t *testing.T) {
	s := recoveryStore(t)
	owner, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	run := admitRecovery(t, s, owner)
	require.NoError(t, s.Pending(t.Context(), run.ID, owner.ID, run.Cleanup.Scope))
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: run.ID, Generation: owner.ID, Slot: owner.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	err = s.checkQuarantineQuery(ctx, tx)
	var typed *Error
	require.ErrorAs(t, err, &typed)
	require.Equal(t, "cleanup_unverified", typed.Code)
	require.NoError(t, ctx.Err(), "a pool query would wait for this occupied transaction")
	require.True(t, typed.RecoveryHint.NoAdmission)
	require.EqualValues(t, 1, *typed.RecoveryHint.IncidentCount)
	require.Equal(t, run.ID, typed.RecoveryHint.Subject.RunID)
	require.Equal(t, owner.ID, typed.RecoveryHint.Subject.Generation)
	require.Equal(t, "supervisor_lost", typed.RecoveryHint.Subject.Reason)
	require.Equal(t, "store", typed.RecoveryHint.Scope)
	require.Contains(t, typed.RecoveryHint.InspectCommand, "runs inspect")
	// Optional attribution failure must not replace the original refusal or
	// require another connection, even while the same transaction remains open.
	_, err = tx.ExecContext(ctx, "DROP TABLE run_incidents")
	require.NoError(t, err)
	err = s.admissionFailure(ctx, tx, "cleanup_unverified", "PRIVATE_BACKEND_ERROR")
	require.ErrorAs(t, err, &typed)
	require.Equal(t, "cleanup_unverified", typed.Code)
	require.True(t, typed.RecoveryHint.DetailUnavailable)
	require.Nil(t, typed.RecoveryHint.Subject)
	require.Contains(t, typed.RecoveryHint.NextStep, "capy runs inspect")
	require.NotContains(t, typed.Message, "PRIVATE_BACKEND_ERROR")
}

func TestAdmissionHintHeldRegistryPreservesDeadline(t *testing.T) {
	s := recoveryStore(t)
	lease, err := s.recoveryLease(t.Context())
	require.NoError(t, err)
	defer lease.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err = s.GuardAdmission(ctx)
	var typed *Error
	require.ErrorAs(t, err, &typed)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, "storage_busy", typed.Code)
	require.Equal(t, "stop_store_users", typed.RecoveryHint.Action)
	// The original exclusive lease remains held; hints did not reopen or
	// convert it, and returning the error did not create an execution owner.
	other, err := openRegular(lease.Name(), false)
	require.NoError(t, err)
	defer other.Close()
	require.ErrorIs(t, unix.Flock(int(other.Fd()), unix.LOCK_EX|unix.LOCK_NB), unix.EWOULDBLOCK)
}

func TestAdmissionHintsRefuseSingleBatchAndOwnerWithoutIDs(t *testing.T) {
	s := recoveryStore(t)
	owner, err := s.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	require.NoError(t, s.Quarantine(t.Context()))
	run, err := s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
	require.Empty(t, run.ID)
	var typed *Error
	require.ErrorAs(t, err, &typed)
	require.True(t, typed.RecoveryHint.NoAdmission)
	batch, children, err := s.AdmitBatch(t.Context(), owner, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "PRIVATE_PROGRAM"}}})
	require.Empty(t, batch.BatchID)
	require.Empty(t, children)
	require.ErrorAs(t, err, &typed)
	require.Equal(t, "cleanup_unverified", typed.Code)
	other, err := s.ClaimOwner(t.Context())
	require.Nil(t, other)
	require.ErrorAs(t, err, &typed)
	require.True(t, typed.RecoveryHint.NoAdmission)
	var runs, batches, owners int
	require.NoError(t, s.db.QueryRow("SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM run_batches),(SELECT count(*) FROM run_owners)").Scan(&runs, &batches, &owners))
	require.Zero(t, runs)
	require.Zero(t, batches)
	require.Equal(t, 1, owners)
}

func TestAdmissionHintRepairsAndPathPrivacy(t *testing.T) {
	for _, tc := range []struct{ code, action string }{
		{"cleanup_unverified", "inspect"}, {"wrong_key", "restore_credential"}, {"credential_unavailable", "restore_credential"},
		{"storage_busy", "stop_store_users"}, {"schema_mismatch", "upgrade_required"}, {"recovery_plan_stale", "review_plan"},
		{"recovery_inconsistent", "repair_evidence"}, {"recovery_capacity", "increase_capacity"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			original := errors.Join(failure(tc.code, "PRIVATE_BACKEND_ERROR"), context.DeadlineExceeded)
			e := AdmissionError(original, "/tmp/owner'quote")
			require.ErrorIs(t, e, context.DeadlineExceeded)
			require.Equal(t, tc.action, e.RecoveryHint.Action)
			require.Contains(t, e.RecoveryHint.InspectCommand, "'\"'\"'")
			encoded, err := json.Marshal(e)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "PRIVATE_BACKEND_ERROR")
			require.NotContains(t, string(encoded), "--discard")
			require.Contains(t, string(encoded), "capy runs inspect")
		})
	}
	for _, path := range []string{"/tmp/owner\ncontrol", "/tmp/owner\u202econtrol", "/tmp/password=synthetic-secret-long-enough"} {
		e := AdmissionError(failure("cleanup_unverified", ""), path)
		require.Empty(t, e.RecoveryHint.InspectCommand)
	}
}

func TestAdmissionHintUpgradeFailuresKeepSpecificRepair(t *testing.T) {
	err := upgradeError(errors.New("PRIVATE_SQLITE_ERROR"))
	e := AdmissionError(err, "/tmp/owner")
	require.Equal(t, "schema_upgrade_failed", e.Code)
	require.Equal(t, "repair_upgrade", e.RecoveryHint.Action)
	require.Contains(t, e.RecoveryHint.NextStep, "capy runs upgrade")
	require.NotContains(t, e.Message, "inconsistent")
	require.NotContains(t, e.Detail, "PRIVATE_SQLITE_ERROR")
	e = AdmissionError(failure("schema_upgrade_capacity", "run schema upgrade needs at least 9000 bytes; 100 bytes available; free disk space and retry"), "/tmp/owner")
	require.Contains(t, e.Detail, "9000")
	require.Contains(t, e.Detail, "100 bytes available")
}
