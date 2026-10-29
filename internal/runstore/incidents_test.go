package runstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func incidentEpoch(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.db.QueryRow("SELECT recovery_revision FROM run_meta").Scan(&n))
	return n
}
func allIncidents(t *testing.T, s *Store) []incident {
	t.Helper()
	rows, err := s.db.Query("SELECT " + incidentColumns + " FROM run_incidents ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	got := []incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		require.NoError(t, err)
		got = append(got, i)
	}
	require.NoError(t, rows.Err())
	return got
}
func fixtureReceipt(t *testing.T, s *Store) (string, string) {
	t.Helper()
	id, hash := uuid.NewString(), strings.ReplaceAll(uuid.NewString(), "-", "")+strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := s.db.Exec(`INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at) VALUES(?,?,?,1,1,'operator','groups_stopped',0,'reviewed fixture',1,0,0,1,0,1)`, id, hash, s.id)
	require.NoError(t, err)
	return id, hash
}

func TestIncidentReconciliationDeduplicatesAndPreservesEvidence(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	r := retentionRun(t, s, o, "SECRET_PROGRAM_OUTPUT", false)
	var original []byte
	require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&original))
	before := accounted(t, s)
	require.NoError(t, o.Lease.Close())
	_, err := s.Reconcile(t.Context())
	require.NoError(t, err)
	first := allIncidents(t, s)
	require.Len(t, first, 1)
	i := first[0]
	require.Equal(t, r.ID, i.RunID)
	require.Equal(t, o.ID, i.Generation)
	require.Equal(t, incidentPending, i.Cause)
	require.Equal(t, attributionInferred, i.Attribution)
	require.NotNil(t, i.DetectedAt)
	require.Equal(t, "admitted", i.Phase)
	epoch := incidentEpoch(t, s)
	require.EqualValues(t, 1, epoch)
	require.Equal(t, before+recoveryIncidentReserve, accounted(t, s))
	for range 3 {
		_, err = s.Reconcile(t.Context())
		require.NoError(t, err)
	}
	require.Equal(t, first, allIncidents(t, s))
	require.Equal(t, epoch, incidentEpoch(t, s))
	require.Equal(t, before+recoveryIncidentReserve, accounted(t, s))
	var after []byte
	require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", r.ID).Scan(&after))
	require.Equal(t, original, after)
	view, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{RunID: r.ID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, view.Subjects, 1)
	require.Equal(t, i.ID, view.Subjects[0].IncidentID)
	require.Equal(t, stageReconciliation, incidentStage(view.Subjects[0].DetectionStage))
	require.True(t, view.Subjects[0].BlocksAdmission)
	data, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(data), "SECRET_PROGRAM_OUTPUT")
}

func TestIncidentLegacyMarkersAndNewEpisodes(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	_, err := s.db.Exec("UPDATE run_meta SET admission_blocked=1; UPDATE run_owners SET cleanup_unverified=1")
	require.NoError(t, err)
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	first := allIncidents(t, s)
	require.Len(t, first, 2)
	for _, i := range first {
		require.Equal(t, incidentLegacy, i.Cause)
		require.Equal(t, attributionUnknown, i.Attribution)
		require.Nil(t, i.DetectedAt)
		require.Empty(t, i.RunID)
	}
	before := incidentEpoch(t, s)
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, incidentEpoch(t, s))
	receipt, _ := fixtureReceipt(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE run_incidents SET receipt_id=?", receipt)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE run_meta SET admission_blocked=0; UPDATE run_owners SET cleanup_unverified=0")
	require.NoError(t, err)
	require.NoError(t, advanceRecoveryRevision(t.Context(), tx))
	require.NoError(t, tx.Commit())
	require.NoError(t, s.Quarantine(t.Context()))
	_, err = s.db.Exec("UPDATE run_owners SET cleanup_unverified=1 WHERE slot=?", o.Slot)
	require.NoError(t, err)
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	all := allIncidents(t, s)
	require.Len(t, all, 4)
	for _, old := range first {
		found := false
		for _, i := range all {
			if i.ID == old.ID {
				found = true
				require.Equal(t, receipt, i.ReceiptID)
				require.Equal(t, old.Revision, i.Revision)
				require.Equal(t, old.DetectedAt, i.DetectedAt)
			}
		}
		require.True(t, found)
	}
}

func TestIncidentMaterialRevisionAndMonotonicPhase(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	r := retentionRun(t, s, o, "", false)
	require.NoError(t, o.Lease.Close())
	_, err := s.Reconcile(t.Context())
	require.NoError(t, err)
	original := allIncidents(t, s)[0]
	next := original
	next.Cause = incidentFinalization
	next.Stage = stageFinalization
	next.Attribution = attributionRecorded
	next.Phase = "run"
	apply := func(i incident) {
		tx, err := s.db.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		changed, err := observeIncident(t.Context(), tx, i)
		require.NoError(t, err)
		require.NoError(t, finishIncidentChanges(t.Context(), tx, changed))
		require.NoError(t, tx.Commit())
	}
	apply(next)
	got := allIncidents(t, s)[0]
	require.Equal(t, original.ID, got.ID)
	require.Equal(t, original.DetectedAt, got.DetectedAt)
	require.EqualValues(t, 2, got.Revision)
	epoch := incidentEpoch(t, s)
	apply(next)
	apply(original)
	require.Equal(t, got, allIncidents(t, s)[0])
	require.Equal(t, epoch, incidentEpoch(t, s))
	require.Equal(t, r.ID, got.RunID)
}

func TestIncidentPublicationFailureAndEpochExhaustion(t *testing.T) {
	for _, mode := range []string{"write_failure", "capacity", "epoch"} {
		t.Run(mode, func(t *testing.T) {
			s, o := retentionStore(t, 8<<20)
			r := retentionRun(t, s, o, "evidence", false)
			before := accounted(t, s)
			require.NoError(t, o.Lease.Close())
			switch mode {
			case "write_failure":
				_, err := s.db.Exec("CREATE TRIGGER refuse_incident BEFORE INSERT ON run_incidents BEGIN SELECT RAISE(ABORT,'PRIVATE_BACKEND_ERROR'); END")
				require.NoError(t, err)
			case "capacity":
				_, err := s.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", before)
				require.NoError(t, err)
			case "epoch":
				_, err := s.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
				require.NoError(t, err)
			}
			_, err := s.Reconcile(t.Context())
			require.Error(t, err)
			require.NotContains(t, err.Error(), "PRIVATE_BACKEND_ERROR")
			require.Empty(t, allIncidents(t, s))
			require.Equal(t, before, accounted(t, s))
			var blocked bool
			require.NoError(t, s.db.QueryRow("SELECT admission_blocked FROM run_meta").Scan(&blocked))
			require.False(t, blocked, "incident and marker transaction must roll back together")
			fresh, err := s.ClaimOwner(t.Context())
			require.Error(t, err)
			require.Nil(t, fresh, "pending evidence still refuses admission when attribution cannot persist")
			var count int
			require.NoError(t, s.db.QueryRow("SELECT count(*) FROM runs WHERE id=? AND terminal=0", r.ID).Scan(&count))
			require.Equal(t, 1, count)
			_, err = s.db.Exec("DROP TRIGGER IF EXISTS refuse_incident; UPDATE run_meta SET recovery_revision=0; UPDATE run_policy SET max_run_bytes=1048576,max_retained_bytes=8388608")
			require.NoError(t, err)
			_, err = s.Reconcile(t.Context())
			require.NoError(t, err)
			require.Len(t, allIncidents(t, s), 1)
		})
	}
}

func TestIncidentUnsupportedEvidenceRemainsBlocked(t *testing.T) {
	for _, mode := range []string{"generation", "result", "manifest_identity", "missing_child", "claim"} {
		t.Run(mode, func(t *testing.T) {
			s, o := retentionStore(t, 8<<20)
			batch, children, err := s.AdmitBatch(t.Context(), o, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "never replay"}}})
			require.NoError(t, err)
			id := children[0].ID
			want := incidentManifest
			switch mode {
			case "generation":
				_, err = s.db.Exec("UPDATE runs SET generation=? WHERE id=?", uuid.NewString(), id)
				want = incidentGeneration
			case "result":
				_, err = s.db.Exec("UPDATE runs SET result=x'00' WHERE id=?", id)
				want = incidentProof
			case "manifest_identity":
				_, err = s.db.Exec("UPDATE run_batch_children SET result=json_set(CAST(result AS TEXT),'$.run_id',?) WHERE run_id=?", uuid.NewString(), id)
			case "missing_child":
				_, err = s.db.Exec("DELETE FROM runs WHERE id=?", id)
			case "claim":
				_, err = s.db.Exec("INSERT INTO run_work_claims(run_id,slot,generation) VALUES(?,?,?)", id, o.Slot, uuid.NewString())
				want = incidentGeneration
			}
			require.NoError(t, err)
			require.NoError(t, o.Lease.Close())
			_, err = s.Reconcile(t.Context())
			require.NoError(t, err)
			requireErrorCode(t, s.GuardAdmission(t.Context()), "cleanup_unverified")
			incidents := allIncidents(t, s)
			require.NotEmpty(t, incidents)
			found := false
			for _, i := range incidents {
				found = found || i.Cause == want
			}
			require.True(t, found, "%s: %v", batch.BatchID, incidents)
			if mode == "result" {
				_, err = s.db.Exec("UPDATE runs SET result=? WHERE id=?", mustResult(t, children[0].Result), id)
				require.NoError(t, err)
			}
		})
	}
}
func mustResult(t *testing.T, r Result) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	require.NoError(t, err)
	return b
}

func TestIncidentBoundedPublicationAndEpochCursors(t *testing.T) {
	s, o := retentionStore(t, 32<<20)
	for range 70 {
		retentionRun(t, s, o, "", false)
	}
	require.NoError(t, o.Lease.Close())
	_, err := s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Len(t, allIncidents(t, s), 64)
	page, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, page.NextCursor)
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Len(t, allIncidents(t, s), 70)
	_, err = InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1, Cursor: page.NextCursor})
	requireErrorCode(t, err, "inspection_stale")
	seen := map[string]bool{}
	cursor := ""
	epoch := incidentEpoch(t, s)
	for range 40 {
		page, err = InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 3, Cursor: cursor})
		require.NoError(t, err)
		require.Equal(t, epoch, *page.RecoveryRevision)
		for _, i := range page.Subjects {
			if i.IncidentID != "" {
				require.False(t, seen[i.IncidentID])
				seen[i.IncidentID] = true
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	require.Empty(t, cursor)
	require.Len(t, seen, 70)
}

func TestIncidentReceiptInspectionRetainedAndPruning(t *testing.T) {
	s, _ := retentionStore(t, 8<<20)
	require.NoError(t, s.Quarantine(t.Context()))
	_, err0 := s.Reconcile(t.Context())
	require.NoError(t, err0)
	i := allIncidents(t, s)[0]
	receipt, hash := fixtureReceipt(t, s)
	_, err := s.db.Exec("UPDATE run_incidents SET receipt_id=? WHERE id=?", receipt, i.ID)
	require.NoError(t, err)
	data, err := json.Marshal(inspectionPlan{Version: 1, Store: s.id, Hash: hash})
	require.NoError(t, err)
	for _, options := range []InspectionOptions{{ReceiptID: receipt, Limit: 4}, {Plan: base64.RawURLEncoding.EncodeToString(data), Limit: 4}} {
		before := incidentEpoch(t, s)
		view, err := InspectRuns(t.Context(), s.path, testKey, options)
		require.NoError(t, err)
		require.NotNil(t, view.Receipt)
		require.Equal(t, receipt, view.Receipt.ID)
		require.Equal(t, i.ID, view.Subjects[0].IncidentID)
		require.False(t, view.Subjects[0].BlocksAdmission)
		require.EqualValues(t, 1, view.Receipt.AuditExpiresAt, "expiry alone cannot hide retained audit")
		require.Equal(t, before, incidentEpoch(t, s))
	}
	_, err = InspectRuns(t.Context(), s.path, testKey, InspectionOptions{ReceiptID: i.ID, Limit: 1})
	requireErrorCode(t, err, "receipt_unavailable")
	_, err = s.db.Exec("UPDATE run_recovery_receipts SET pruning=1 WHERE id=?", receipt)
	require.NoError(t, err)
	view, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{ReceiptID: receipt, Limit: 1})
	require.NoError(t, err)
	require.True(t, view.Receipt.Pruning)
	require.Equal(t, "partial", view.Details)
}

func TestIncidentEpochUnchangedAtExhaustion(t *testing.T) {
	s, _ := retentionStore(t, 8<<20)
	require.NoError(t, s.Quarantine(t.Context()))
	_, err := s.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
	require.NoError(t, err)
	require.NoError(t, s.Quarantine(t.Context()), "unchanged observation need not advance exhausted epoch")
	require.Equal(t, int64(math.MaxInt64), incidentEpoch(t, s))
	_, err = s.db.Exec("UPDATE run_meta SET recovery_revision=1")
	require.NoError(t, err)
}

func TestIncidentEarlyCancellationDoesNotPublish(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	retentionRun(t, s, o, "", false)
	require.NoError(t, o.Lease.Close())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Reconcile(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, allIncidents(t, s))
	require.Zero(t, incidentEpoch(t, s))
}

func TestIncidentInspectionDoesNotPublishLegacyAttribution(t *testing.T) {
	s, _ := retentionStore(t, 8<<20)
	_, err := s.db.Exec("UPDATE run_meta SET admission_blocked=1")
	require.NoError(t, err)
	before := accounted(t, s)
	view, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 4})
	require.NoError(t, err)
	require.Equal(t, "recorded_blocked", view.Admission)
	require.Zero(t, view.Counts.UnattributedIncidents)
	require.Empty(t, allIncidents(t, s))
	require.Zero(t, incidentEpoch(t, s))
	require.Equal(t, before, accounted(t, s))
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	got := allIncidents(t, s)
	require.Len(t, got, 1)
	require.Equal(t, "store", got[0].Kind)
	require.Empty(t, got[0].RunID)
	require.Nil(t, got[0].DetectedAt)
}

func TestIncidentMalformedOptionalIdentityCannotDisappear(t *testing.T) {
	s, _ := retentionStore(t, 8<<20)
	require.NoError(t, s.Quarantine(t.Context()))
	_, err0 := s.Reconcile(t.Context())
	require.NoError(t, err0)
	_, err := s.db.Exec("PRAGMA foreign_keys=OFF; PRAGMA ignore_check_constraints=ON; UPDATE run_incidents SET receipt_id='PRIVATE_INVALID_ID'; PRAGMA ignore_check_constraints=OFF; PRAGMA foreign_keys=ON")
	require.NoError(t, err)
	_, err = InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 4})
	requireErrorCode(t, err, "recovery_inconsistent")
	require.NotContains(t, err.Error(), "PRIVATE_INVALID_ID")
	_, err = s.db.Exec("UPDATE run_incidents SET receipt_id=NULL")
	require.NoError(t, err)
}

func TestIncidentUnavailableAttributionKeepsRestartedQueriesReadable(t *testing.T) {
	for _, mode := range []string{"capacity", "epoch", "write"} {
		t.Run(mode, func(t *testing.T) {
			settings := PolicyConfig{TTLHours: 24, MaxRunBytes: 1 << 20, MaxRetainedBytes: 8 << 20}
			s, err := OpenWithPolicy(t.Context(), policyPath(t), testKey, true, settings)
			require.NoError(t, err)
			closed := false
			t.Cleanup(func() {
				if !closed {
					require.NoError(t, s.Close())
				}
			})
			o, err := s.ClaimOwner(t.Context())
			require.NoError(t, err)
			pending := retentionRun(t, s, o, "uncertain prefix", false)
			kept := retentionRun(t, s, o, "retained sibling bytes", true)
			var original []byte
			require.NoError(t, s.db.QueryRow("SELECT result FROM runs WHERE id=?", kept.ID).Scan(&original))
			switch mode {
			case "capacity":
				settings.MaxRunBytes = 1
				settings.MaxRetainedBytes = accounted(t, s)
				_, err = s.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", settings.MaxRetainedBytes)
			case "epoch":
				_, err = s.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
			case "write":
				_, err = s.db.Exec("CREATE TRIGGER attribution_fault BEFORE INSERT ON run_incidents BEGIN SELECT RAISE(ABORT,'private fault'); END")
			}
			require.NoError(t, err)
			require.NoError(t, o.Lease.Close())
			err = s.Close()
			closed = true
			require.Error(t, err)
			// Configured query attachment is a mode=ro handle with a permanent admission
			// refusal. Unconfigured helper attachment never receives that fallback.
			reader, err := OpenWithPolicy(t.Context(), s.path, testKey, false, settings)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reader.Close()) })
			page, err := reader.ReadRaw(t.Context(), kept.ID, "stdout", "", 4096)
			require.NoError(t, err)
			require.Equal(t, []byte("retained sibling bytes"), page.Fragments[0].Data)
			status, err := reader.Load(t.Context(), kept.ID)
			require.NoError(t, err)
			require.Equal(t, "exited", status.Result.State)
			list, err := reader.List(t.Context(), ListOptions{Limit: 64})
			require.NoError(t, err)
			require.Len(t, list.Entries, 2)
			fresh, err := reader.ClaimOwner(t.Context())
			require.Error(t, err)
			require.Nil(t, fresh)
			_, err = reader.db.Exec("UPDATE run_meta SET admission_blocked=0")
			require.Error(t, err, "pool must remain SQLite read-only")
			require.Error(t, reader.Started(t.Context(), pending.ID))
			require.Error(t, reader.GuardAdmission(t.Context()))
			helper, err := Open(t.Context(), s.path, testKey, false)
			require.Error(t, err)
			require.Nil(t, helper)
			var after []byte
			require.NoError(t, reader.db.QueryRow("SELECT result FROM runs WHERE id=?", kept.ID).Scan(&after))
			require.Equal(t, original, after)
			require.Empty(t, allIncidents(t, reader), "failed publication remains atomic even during reader reattachment")
			require.NoError(t, reader.Close(), "query close must not reattempt failed publication or maintain")
		})
	}
}

func TestIncidentLiveQuarantineDoesNotRequireAuditHeadroom(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	retentionRun(t, s, o, "pending", false)
	other, err := OpenWithPolicy(t.Context(), s.path, testKey, false, s.policy.PolicyConfig)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	before := accounted(t, s)
	_, err = s.db.Exec("UPDATE run_policy SET max_run_bytes=1,max_retained_bytes=?", before)
	require.NoError(t, err)
	require.NoError(t, s.Quarantine(t.Context()), "the held owner's safety marker must not require incident allocation")
	var blocked bool
	require.NoError(t, s.db.QueryRow("SELECT admission_blocked FROM run_meta").Scan(&blocked))
	require.True(t, blocked)
	require.Empty(t, allIncidents(t, s))
	require.Equal(t, before, accounted(t, s))
	fresh, err := other.ClaimOwner(t.Context())
	requireErrorCode(t, err, "cleanup_unverified")
	require.Nil(t, fresh)
	requireErrorCode(t, other.GuardAdmission(t.Context()), "cleanup_unverified")
	// Restore the fixture policy before its normal close-time attribution.
	_, err = s.db.Exec("UPDATE run_policy SET max_run_bytes=1048576,max_retained_bytes=8388608")
	require.NoError(t, err)
}

func TestIncidentExhaustedEpochRefusesOtherLiveOwners(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	retentionRun(t, s, o, "", false)
	other, err := OpenWithPolicy(t.Context(), s.path, testKey, false, s.policy.PolicyConfig)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	_, err = s.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
	require.NoError(t, err)
	requireErrorCode(t, s.Quarantine(t.Context()), "recovery_revision_exhausted")
	fresh, err := other.ClaimOwner(t.Context())
	requireErrorCode(t, err, "recovery_revision_exhausted")
	require.Nil(t, fresh)
	_, err = s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{})
	requireErrorCode(t, err, "recovery_revision_exhausted")
	require.Equal(t, int64(math.MaxInt64), incidentEpoch(t, s))
	require.Empty(t, allIncidents(t, s))
	_, err = s.db.Exec("UPDATE run_meta SET recovery_revision=0")
	require.NoError(t, err)
}

func TestIncidentMalformedRunRemainsInspectable(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	bad := retentionRun(t, s, o, "PRIVATE_OUTPUT", false)
	good := retentionRun(t, s, o, "", false)
	_, err := s.db.Exec("UPDATE runs SET result=x'00' WHERE id=?", bad.ID)
	require.NoError(t, err)
	require.NoError(t, o.Lease.Close())
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	seen := map[string]bool{}
	cursor := ""
	for range 8 {
		view, err := InspectRuns(t.Context(), s.path, testKey, InspectionOptions{Limit: 1, Cursor: cursor})
		require.NoError(t, err)
		for _, subject := range view.Subjects {
			if subject.IncidentID != "" {
				seen[subject.RunID] = true
				if subject.RunID == bad.ID {
					require.Equal(t, string(incidentProof), subject.Reason)
					require.True(t, subject.EvidenceInconsistent)
					require.Contains(t, subject.MissingEvidence, "valid_run_metadata")
				}
			}
		}
		data, err := json.Marshal(view)
		require.NoError(t, err)
		require.NotContains(t, string(data), "PRIVATE_OUTPUT")
		cursor = view.NextCursor
		if cursor == "" {
			break
		}
	}
	require.Empty(t, cursor)
	require.True(t, seen[bad.ID])
	require.True(t, seen[good.ID])
	report, err := Inspect(t.Context(), s.path, testKey)
	require.NoError(t, err)
	require.Equal(t, "partial", report.Inspection.Details)
	require.Equal(t, "recovery_inconsistent", report.Inspection.Action)
	require.EqualValues(t, 2, report.Inspection.Counts.InferredIncidents)
	require.Empty(t, report.Recent)
}

func TestIncidentMigratedMarkersUseIdentifiedRun(t *testing.T) {
	f := newSchema9Fixture(t)
	id := schema9Run(t, f, true)
	_, err := f.db.Exec("UPDATE run_meta SET admission_blocked=1; UPDATE run_owners SET cleanup_unverified=1")
	require.NoError(t, err)
	f.close(t)
	_, err = Upgrade(t.Context(), f.path, testKey, f.settings)
	require.NoError(t, err)
	s, err := OpenWithPolicy(t.Context(), f.path, testKey, true, f.settings)
	require.NoError(t, err)
	defer s.Close()
	got := allIncidents(t, s)
	require.Len(t, got, 1)
	require.Equal(t, "run", got[0].Kind)
	require.Equal(t, id, got[0].RunID)
	require.Equal(t, incidentPending, got[0].Cause)
}

func TestIncidentMixedWindowsCommitBeforeLaterMalformedSibling(t *testing.T) {
	s, o := retentionStore(t, 32<<20)
	for range 64 {
		retentionRun(t, s, o, "", false)
	}
	row, program, _, err := s.prepareRun(o, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	row.ID = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	row.Result.RunID = &row.ID
	encoded := mustResult(t, row.Result)
	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, insertRun(t.Context(), tx, row, program, encoded))
	require.NoError(t, charge(t.Context(), tx, row.ID, int64(len(program))+runReserve, s.policy.MaxRetainedBytes, true))
	require.NoError(t, tx.Commit())
	_, err = s.db.Exec("UPDATE runs SET result=x'00' WHERE id=?", row.ID)
	require.NoError(t, err)
	require.NoError(t, o.Lease.Close())
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Len(t, allIncidents(t, s), 64)
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	require.Len(t, allIncidents(t, s), 65)
	var reason string
	require.NoError(t, s.db.QueryRow("SELECT reason FROM run_incidents WHERE run_id=?", row.ID).Scan(&reason))
	require.Equal(t, string(incidentProof), reason)
}

func TestIncidentLiveQuarantineAvoidsIndexRecount(t *testing.T) {
	s, o := retentionStore(t, 8<<20)
	retentionRun(t, s, o, "", false)
	_, err := s.db.Exec("UPDATE run_meta SET index_enabled=1; CREATE TRIGGER reject_quarantine_recount BEFORE UPDATE OF index_bytes ON run_meta BEGIN SELECT RAISE(ABORT,'unexpected recount'); END")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), WriteTimeout)
	defer cancel()
	require.NoError(t, s.Quarantine(ctx), "live quarantine must not perform a size-dependent index recount")
	_, err = s.db.Exec("DROP TRIGGER reject_quarantine_recount")
	require.NoError(t, err)
}

func TestIncidentExistingPeerAdmissionReconcilesBeforeEviction(t *testing.T) {
	for _, mode := range []string{"capacity", "epoch", "write"} {
		t.Run(mode, func(t *testing.T) {
			wantCode := map[string]string{"capacity": "recovery_capacity", "epoch": "recovery_revision_exhausted", "write": "incident_publication_failed"}[mode]
			program, err := json.Marshal(Program{PolicyRevision: 1, TTLHours: 24, MaxRunBytes: 1})
			require.NoError(t, err)
			limit := 2 * (runReserve + int64(len(program)))
			if mode != "capacity" {
				limit += 4 * recoveryIncidentReserve
			}
			settings := PolicyConfig{TTLHours: 24, MaxRunBytes: 1, MaxRetainedBytes: limit}
			s, err := OpenWithPolicy(t.Context(), policyPath(t), testKey, true, settings)
			require.NoError(t, err)
			a, err := s.ClaimOwner(t.Context())
			require.NoError(t, err)
			b, err := s.ClaimOwner(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, a.Lease.Close()); require.NoError(t, s.Close()) })
			retained := retentionRun(t, s, a, "", true)
			retentionRun(t, s, b, "", false)
			if mode == "capacity" {
				require.Equal(t, limit, accounted(t, s), "fixture has exactly zero incident headroom and an evictable sibling")
			}
			require.NoError(t, b.Lease.Close())
			if mode == "epoch" {
				_, err = s.db.Exec("UPDATE run_meta SET recovery_revision=?", int64(math.MaxInt64))
				require.NoError(t, err)
			}
			if mode == "write" {
				_, err = s.db.Exec("CREATE TRIGGER peer_incident_fault BEFORE INSERT ON run_incidents BEGIN SELECT RAISE(ABORT,'private fault'); END")
				require.NoError(t, err)
			}
			_, err = s.Reconcile(t.Context())
			requireErrorCode(t, err, wantCode)
			// A is an already-claimed live owner, not a fresh attachment. Both APIs call
			// reconcile under recovery.lock before any quota eviction or admission write.
			one, err := s.Admit(t.Context(), a, executor.CleanupLinuxDescendants, Program{})
			requireErrorCode(t, err, wantCode)
			require.Empty(t, one.ID)
			batch, children, err := s.AdmitBatch(t.Context(), a, executor.CleanupLinuxDescendants, BatchInput{Concurrency: 1, Commands: []BatchCommand{{Command: "must not run"}}})
			requireErrorCode(t, err, wantCode)
			require.Empty(t, batch.BatchID)
			require.Empty(t, children)
			var runs, batches int
			require.NoError(t, s.db.QueryRow("SELECT count(*) FROM runs").Scan(&runs))
			require.Equal(t, 2, runs)
			require.NoError(t, s.db.QueryRow("SELECT count(*) FROM run_batches").Scan(&batches))
			require.Zero(t, batches)
			_, err = s.Load(t.Context(), retained.ID)
			require.NoError(t, err, "refused admission cannot evict the completed sibling")
			_, err = s.db.Exec("DROP TRIGGER IF EXISTS peer_incident_fault; UPDATE run_meta SET recovery_revision=0; UPDATE run_policy SET max_retained_bytes=8388608")
			require.NoError(t, err)
			s.policy.MaxRetainedBytes = 8 << 20
		})
	}
}
