package runstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRecoveredRunViewsPreserveOriginalEvidence(t *testing.T) {
	for _, recordedCompletion := range []bool{false, true} {
		name := "unknown completion"
		if recordedCompletion {
			name = "recorded completion with pending cleanup"
		}
		t.Run(name, func(t *testing.T) {
			s, o, closeStore := planFixture(t)
			r := planPending(t, s, o)
			if recordedCompletion {
				exit := 7
				require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupPending}, Started: true, ExitCode: &exit}))
			}
			before := applyHistory(t, s.db, r.ID)
			var encoded []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&encoded))
			var original Result
			require.NoError(t, decodeRunResult(encoded, executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupPending}, &original))
			closeStore()
			plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
			require.NoError(t, err)
			applied, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
			require.NoError(t, err)
			fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, fresh.closeHandles()) })
			owner, err := fresh.ClaimOwner(t.Context())
			require.NoError(t, err)
			require.Equal(t, o.Slot, owner.Slot)
			require.NotEqual(t, o.ID, owner.ID)
			t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
			healthy, err := fresh.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
			require.NoError(t, err)
			row, err := fresh.Load(t.Context(), r.ID)
			require.NoError(t, err)
			got := row.Result
			require.Equal(t, original.State, got.State)
			require.Equal(t, original.ExitCode, got.ExitCode)
			require.Equal(t, original.CompletedAt, got.CompletedAt)
			require.Equal(t, original.ExpiresAt, got.ExpiresAt)
			require.Equal(t, original.Capture.TerminalPersisted, got.Capture.TerminalPersisted)
			require.Equal(t, original.Cleanup, got.Cleanup)
			require.Equal(t, "incomplete", got.Capture.State)
			require.Equal(t, "operator_resolved", got.Recovery.State)
			require.Equal(t, applied.Receipt.ID, got.Recovery.ReceiptID)
			require.Equal(t, r.ID, got.Recovery.DispositionID)
			require.True(t, got.Recovery.OperationallyClosed)
			require.True(t, got.Recovery.CaptureClosed)
			require.False(t, got.Recovery.BlocksAdmission)
			var deadline int64
			require.NoError(t, fresh.db.QueryRow("SELECT retention_expires_at FROM run_resolutions WHERE run_id=?", r.ID).Scan(&deadline))
			require.Equal(t, time.UnixMilli(deadline).UTC(), *got.Recovery.RetentionExpiresAt)
			require.Equal(t, o.ID, got.OwnerID)
			page, err := fresh.List(t.Context(), ListOptions{Limit: 64})
			require.NoError(t, err)
			require.Len(t, page.Entries, 2)
			require.Equal(t, got.Recovery, page.Entries[0].Recovery)
			require.Equal(t, got.State, page.Entries[0].State)
			require.Equal(t, got.ExitCode, page.Entries[0].ExitCode)
			control, err := fresh.Load(t.Context(), healthy.ID)
			require.NoError(t, err)
			require.Nil(t, control.Result.Recovery)
			wire, err := json.Marshal(control.Result)
			require.NoError(t, err)
			require.NotContains(t, string(wire), `"recovery"`)
			require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
			// Effective availability is separate from the original execution TTL.
			view := original
			available, err := projectRunHistory(t.Context(), fresh.db, r.ID, o.ID, &view, false, false, got.Recovery.RetentionExpiresAt.Add(time.Nanosecond))
			require.NoError(t, err)
			require.False(t, available)
			require.Equal(t, original.ExpiresAt, view.ExpiresAt)
		})
	}
}

func TestHistoricalIncidentViewSurvivesRegistryReuse(t *testing.T) {
	s, o, closeStore := planFixture(t)
	r := planPending(t, s, o)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupRetired}, Started: true}))
	before := applyHistory(t, s.db, r.ID)
	row, err := s.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, "unresolved", row.Result.Recovery.State)
	require.True(t, row.Result.Recovery.BlocksAdmission)
	incidentID := row.Result.Recovery.IncidentID
	closeStore()
	plan, err := PlanRecovery(t.Context(), s.path, testKey, planOptions())
	require.NoError(t, err)
	applied, err := applyRecovery(t.Context(), s.path, testKey, applyOptions(plan.Token), nil)
	require.NoError(t, err)
	fresh, err := openAttached(t.Context(), s.path, testKey, s.owner, unix.LOCK_SH)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fresh.closeHandles()) })
	owner, err := fresh.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	row, err = fresh.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Equal(t, incidentID, row.Result.Recovery.IncidentID)
	require.Equal(t, applied.Receipt.ID, row.Result.Recovery.ReceiptID)
	require.Empty(t, row.Result.Recovery.DispositionID)
	require.Nil(t, row.Result.Recovery.RetentionExpiresAt)
	require.False(t, row.Result.Recovery.BlocksAdmission)
	require.Equal(t, before, applyHistory(t, fresh.db, r.ID))
}

func TestStoredResultCannotSupplyRecoveryAuthority(t *testing.T) {
	s, o, _ := planFixture(t)
	r := planPending(t, s, o)
	_, err := s.db.Exec("UPDATE runs SET result=CAST(json_set(CAST(result AS TEXT),'$.recovery',json('{\"state\":\"operator_resolved\"}')) AS BLOB) WHERE id=?", r.ID)
	require.NoError(t, err)
	row, err := s.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.Nil(t, row.Result.Recovery)
}

func TestIndexWritesPreserveExecutionAfterIncidentPublication(t *testing.T) {
	for _, worker := range []string{"index batch", "index failure"} {
		t.Run(worker, func(t *testing.T) {
			s, o, _ := planFixture(t)
			r := planPending(t, s, o)
			claimed, err := s.ClaimIndex(t.Context(), o, r.ID)
			require.NoError(t, err)
			require.True(t, claimed)
			executionBytes := func() []byte {
				var data []byte
				require.NoError(t, s.db.QueryRow("SELECT CAST(json_remove(CAST(result AS TEXT),'$.index') AS BLOB) FROM runs WHERE id=?", r.ID).Scan(&data))
				return data
			}
			before := executionBytes()
			require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
			var slotMarked bool
			require.NoError(t, s.db.QueryRow("SELECT cleanup_unverified FROM run_owners WHERE slot=?", o.Slot).Scan(&slotMarked))
			require.False(t, slotMarked, "incident publication is the new projection trigger")
			if worker == "index batch" {
				require.NoError(t, s.indexBatch(t.Context(), o, r.ID))
			} else {
				require.NoError(t, s.failIndex(t.Context(), o, r.ID, &Error{Code: "index_failed", Message: "fixture"}))
			}
			require.Equal(t, before, executionBytes(), "only Index may change; returned capture/error/recovery fields cannot enter execution storage")
			require.NoError(t, s.ReleaseIndex(t.Context(), o, r.ID))
			exit := 0
			require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupRetired}, Started: true, ExitCode: &exit}))
			row, err := s.Load(t.Context(), r.ID)
			require.NoError(t, err)
			require.Nil(t, row.Result.Error)
			require.Nil(t, row.Result.Capture.Reason)
			require.Equal(t, "complete", row.Result.Capture.State)
			claimed, err = s.ClaimIndex(t.Context(), o, r.ID)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, s.ReleaseIndex(t.Context(), o, r.ID))
		})
	}
}

func TestHistoryRejectsResultGenerationMismatch(t *testing.T) {
	s, o, _ := planFixture(t)
	r := planPending(t, s, o)
	require.NoError(t, s.QuarantineRun(t.Context(), RuntimeIncident{RunID: r.ID, Generation: o.ID, Slot: o.Slot, Cause: IncidentSupervisorLost, Stage: StageHelperWait}))
	_, err := s.db.Exec("UPDATE runs SET result=CAST(json_set(CAST(result AS TEXT),'$.owner_id','00000000-0000-0000-0000-000000000001') AS BLOB) WHERE id=?", r.ID)
	require.NoError(t, err)
	_, err = s.Load(t.Context(), r.ID)
	applyErrorCode(t, err, "recovery_inconsistent")
	_, err = s.List(t.Context(), ListOptions{Limit: 1})
	applyErrorCode(t, err, "recovery_inconsistent")
}
