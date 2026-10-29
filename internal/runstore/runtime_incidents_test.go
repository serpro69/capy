package runstore

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func runtimeSubject(row Admitted) RuntimeIncident {
	return RuntimeIncident{RunID: row.ID, Generation: row.Generation, Slot: row.Slot, BatchID: row.Result.BatchID,
		Cause: IncidentFinalizationUnpersisted, Stage: StageFinalization}
}

func TestRuntimeIncidentPreservesEvidenceAndDeduplicates(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	r := retentionRun(t, s, o, "PRIVATE_OUTPUT", false)
	var original []byte
	require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&original))
	before := accounted(t, s)
	require.NoError(t, s.QuarantineRun(t.Context(), runtimeSubject(r)))
	first := allIncidents(t, s)
	require.Len(t, first, 1)
	i := first[0]
	require.Equal(t, r.ID, i.RunID)
	require.Equal(t, o.ID, i.Generation)
	require.Equal(t, o.Slot, *i.Slot)
	require.Equal(t, incidentFinalization, i.Cause)
	require.Equal(t, stageFinalization, i.Stage)
	require.Equal(t, attributionRecorded, i.Attribution)
	require.Equal(t, "run", i.Phase, "phase comes from durable storage, not the admission snapshot")
	require.NotNil(t, i.DetectedAt)
	require.EqualValues(t, 1, incidentEpoch(t, s))
	require.Equal(t, before+recoveryIncidentReserve, accounted(t, s))
	for range 2 {
		require.NoError(t, s.QuarantineRun(t.Context(), runtimeSubject(r)))
	}
	require.Equal(t, first, allIncidents(t, s))
	require.EqualValues(t, 1, incidentEpoch(t, s))
	var after []byte
	require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&after))
	require.Equal(t, original, after)
	requireErrorCode(t, s.GuardAdmission(t.Context()), "cleanup_unverified")
	view, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{RunID: r.ID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, view.Subjects, 1)
	require.Equal(t, "recorded", view.Subjects[0].Attribution)
	require.Equal(t, "finalization", view.Subjects[0].DetectionStage)
	require.Equal(t, "run", view.Subjects[0].Phase)
	data, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(data), "PRIVATE_OUTPUT")
}

func TestRuntimeIncidentFailurePreservesSafetyMarker(t *testing.T) {
	for _, mode := range []string{"quota", "insert", "recount", "marker", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			s, o := retentionStore(t, 8<<20)
			r := retentionRun(t, s, o, "prefix", false)
			peer, err := s.ClaimOwner(t.Context())
			require.NoError(t, err)
			defer peer.Lease.Close()
			before := accounted(t, s)
			var original []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&original))
			switch mode {
			case "quota":
				_, err = s.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", before)
			case "insert":
				_, err = s.db.Exec("CREATE TRIGGER runtime_fault BEFORE INSERT ON run_incidents BEGIN SELECT RAISE(ABORT,'PRIVATE_ERROR'); END")
			case "recount":
				_, err = s.db.Exec("UPDATE run_meta SET index_enabled=1; CREATE TRIGGER runtime_fault BEFORE UPDATE OF index_bytes ON run_meta BEGIN SELECT RAISE(ABORT,'PRIVATE_ERROR'); END")
			case "marker":
				_, err = s.db.Exec("CREATE TRIGGER runtime_fault BEFORE UPDATE OF admission_blocked ON run_meta BEGIN SELECT RAISE(ABORT,'PRIVATE_ERROR'); END")
			case "epoch":
				_, err = s.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
			}
			require.NoError(t, err)
			err = s.QuarantineRun(t.Context(), runtimeSubject(r))
			require.Error(t, err)
			require.NotContains(t, err.Error(), "PRIVATE_ERROR")
			require.Empty(t, allIncidents(t, s), "failed attribution cannot leave partial audit or debit")
			require.Equal(t, before, accounted(t, s))
			var blocked bool
			require.NoError(t, s.db.QueryRow("SELECT admission_blocked FROM run_meta").Scan(&blocked))
			require.Equal(t, mode != "marker" && mode != "epoch", blocked)
			if blocked {
				require.EqualValues(t, 1, incidentEpoch(t, s), "marker alone invalidates inspection membership")
				_, err := s.Admit(t.Context(), peer, executor.CleanupLinuxDescendants, Program{})
				requireErrorCode(t, err, "cleanup_unverified")
			}
			var after []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&after))
			require.Equal(t, original, after)
			_, err = s.db.Exec("DROP TRIGGER IF EXISTS runtime_fault; UPDATE run_meta SET index_enabled=0,recovery_revision=1; UPDATE run_policy SET max_run_bytes=1048576,max_retained_bytes=8388608")
			require.NoError(t, err)
			require.NoError(t, s.QuarantineRun(t.Context(), runtimeSubject(r)))
			require.Len(t, allIncidents(t, s), 1)
		})
	}
}

func TestRuntimeIncidentRejectsIncorrectIdentityAndStage(t *testing.T) {
	for _, mode := range []string{"run", "owner", "slot", "batch", "stage", "cause"} {
		t.Run(mode, func(t *testing.T) {
			s, o := retentionStore(t, 8<<20)
			r := retentionRun(t, s, o, "", false)
			subject := runtimeSubject(r)
			switch mode {
			case "run":
				subject.RunID = uuid.NewString()
			case "owner":
				subject.Generation = uuid.NewString()
			case "slot":
				subject.Slot++
			case "batch":
				subject.BatchID = uuid.NewString()
			case "stage":
				subject.Stage = StageStartup
			case "cause":
				subject.Cause = "PRIVATE_ERROR"
			}
			requireErrorCode(t, s.QuarantineRun(t.Context(), subject), "recovery_inconsistent")
			require.Empty(t, allIncidents(t, s))
			requireErrorCode(t, s.GuardAdmission(t.Context()), "cleanup_unverified")
		})
	}
}

func TestRunDurablePhasesUseExistingTransitions(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	r, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	check := func(phase string) Admitted {
		t.Helper()
		row, err := s.Load(t.Context(), r.ID)
		require.NoError(t, err)
		require.Equal(t, phase, row.LastPhase)
		require.Zero(t, incidentEpoch(t, s), "execution progress does not change recovery membership")
		return row
	}
	check("admitted")
	require.NoError(t, s.Pending(t.Context(), r.ID, o.ID, r.Cleanup.Scope))
	check("authorized")
	for _, phase := range []string{"", "unknown", "authorized", "PRIVATE_PHASE"} {
		requireErrorCode(t, s.Started(t.Context(), r.ID, phase), "invalid_argument")
		check("authorized")
	}
	_, err = s.db.Exec("CREATE TRIGGER phase_fault BEFORE UPDATE OF last_phase ON runs BEGIN SELECT RAISE(ABORT,'phase failure'); END")
	require.NoError(t, err)
	require.Error(t, s.Started(t.Context(), r.ID, "compile"))
	require.Nil(t, check("authorized").Result.StartedAt)
	_, err = s.db.Exec("DROP TRIGGER phase_fault")
	require.NoError(t, err)
	require.NoError(t, s.Started(t.Context(), r.ID, "compile"))
	started := check("compile").Result.StartedAt
	require.NotNil(t, started)
	require.NoError(t, s.Started(t.Context(), r.ID, "run"))
	require.Equal(t, started, check("run").Result.StartedAt)
	requireErrorCode(t, s.Started(t.Context(), r.ID, "compile"), "invalid_argument")
	require.NoError(t, s.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("only event")}))
	require.NoError(t, s.Finish(t.Context(), r.ID, Completion{Started: true, Cleanup: executor.CleanupEvidence{Scope: r.Cleanup.Scope, State: executor.CleanupRetired}}))
	check("run")
	var events int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM run_events WHERE run_id=?", r.ID).Scan(&events))
	require.Equal(t, 1, events)
}
