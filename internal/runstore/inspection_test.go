package runstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/sqliteutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func inspectionFiles(t *testing.T, root string) map[string]fs.FileMode {
	t.Helper()
	files := map[string]fs.FileMode{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files[path] = info.Mode()
		return nil
	}))
	return files
}

func TestInspectionOldBlockersAndHeldOwner(t *testing.T) {
	s, owner := retentionStore(t, 8<<20)
	old := retentionRun(t, s, owner, "PRIVATE_OUTPUT", false)
	for range 10 {
		retentionRun(t, s, owner, "newer", true)
	}
	before := accounted(t, s)
	files := inspectionFiles(t, filepath.Dir(s.Path()))
	view, err := InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 8})
	require.NoError(t, err)
	require.Equal(t, "unverified", view.Admission)
	require.Zero(t, view.Counts.QuarantineMarkers)
	require.EqualValues(t, 1, view.Counts.UnattributedPending)
	require.Len(t, view.Subjects, 1)
	require.Equal(t, old.ID, view.Subjects[0].RunID)
	require.False(t, view.Subjects[0].BlocksAdmission, "held healthy pending owner is not an incident")
	require.NotNil(t, view.RecoveryRevision)
	require.Zero(t, *view.RecoveryRevision)
	require.Equal(t, files, inspectionFiles(t, filepath.Dir(s.Path())))
	require.Equal(t, before, accounted(t, s))
	// Only reconciliation after releasing the owner can attribute this marker.
	require.NoError(t, owner.Lease.Close())
	_, err = s.Reconcile(t.Context())
	require.NoError(t, err)
	report, err := Inspect(t.Context(), s.Path(), testKey)
	require.NoError(t, err)
	require.Equal(t, "recorded_blocked", report.Inspection.Admission)
	require.EqualValues(t, 2, report.Inspection.Counts.QuarantineMarkers)
	require.EqualValues(t, 1, report.Inspection.Counts.InferredIncidents)
	require.Equal(t, old.ID, report.Inspection.Subjects[0].RunID)
	for _, r := range report.Recent {
		require.NotEqual(t, old.ID, r.ID)
	}
	data, err := json.Marshal(report.Inspection)
	require.NoError(t, err)
	require.NotContains(t, string(data), "PRIVATE_OUTPUT")
}

func TestInspectionAdmittedQueuedAndSafeSiblingsStayUnattributed(t *testing.T) {
	s, owner := retentionStore(t, 8<<20)
	_, err := s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{})
	require.NoError(t, err)
	_, children, err := s.AdmitBatch(t.Context(), owner, executor.CleanupLinuxDescendants, BatchInput{
		Concurrency: 1, Commands: []BatchCommand{{Command: "one"}, {Command: "two"}},
	})
	require.NoError(t, err)
	require.Len(t, children, 2)
	view, err := InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 8})
	require.NoError(t, err)
	require.Equal(t, "inspect", view.Action)
	require.EqualValues(t, 3, view.Counts.UnattributedPending)
	for _, subject := range view.Subjects {
		require.Equal(t, "unattributed_pending", subject.Reason)
		require.False(t, subject.BlocksAdmission)
	}
	unsafe := retentionRun(t, s, owner, "", false)
	// A legacy owner marker does not turn proven-safe siblings into incidents.
	require.NoError(t, s.Quarantine(t.Context()))
	_, err = s.db.Exec("UPDATE run_owners SET cleanup_unverified=1")
	require.NoError(t, err)
	view, err = InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 8})
	require.NoError(t, err)
	require.EqualValues(t, 1, view.Counts.InferredCandidates)
	require.EqualValues(t, 3, view.Counts.UnattributedPending)
	for _, subject := range view.Subjects {
		if subject.Kind == "run" && subject.RunID != unsafe.ID {
			require.Equal(t, "unattributed_pending", subject.Reason)
			require.False(t, subject.BlocksAdmission)
		}
	}
}

func TestInspectionConcurrentWriterAndProcessRead(t *testing.T) {
	if path := os.Getenv("CAPY_INSPECTION_TEST_PATH"); path != "" {
		for range 4 {
			view, err := InspectRuns(t.Context(), path, testKey, InspectionOptions{Limit: 8})
			require.NoError(t, err)
			require.EqualValues(t, 1, view.Counts.UnattributedPending)
		}
		return
	}
	s, owner := retentionStore(t, 8<<20)
	run := retentionRun(t, s, owner, "", false)
	stop := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for sequence := uint64(1); ; sequence++ {
			select {
			case <-stop:
				finished <- nil
				return
			case <-ticker.C:
				if err := s.Append(t.Context(), run.ID, executor.OutputEvent{Sequence: sequence, Phase: "run", Stream: "stdout", Data: []byte("retained\n")}); err != nil {
					finished <- err
					return
				}
			}
		}
	}()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestInspectionConcurrentWriterAndProcessRead$")
	cmd.Env = append(os.Environ(), "CAPY_INSPECTION_TEST_PATH="+s.Path())
	output, err := cmd.CombinedOutput()
	close(stop)
	require.NoError(t, <-finished)
	require.NoError(t, err, "%s", output)
}

func TestInspectionLegacyNoMutation(t *testing.T) {
	f := newLegacyFixture(t)
	old := f.run(t, executor.CleanupPending, false, "", "HISTORICAL_SECRET")
	f.run(t, executor.CleanupRetired, true, "", "sibling")
	_, err := f.db.Exec("UPDATE run_meta SET admission_blocked=1; UPDATE run_owners SET cleanup_unverified=1")
	require.NoError(t, err)
	var original []byte
	require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", *old.RunID).Scan(&original))
	files := inspectionFiles(t, filepath.Dir(f.path))
	report, err := Inspect(t.Context(), f.path, testKey)
	require.NoError(t, err)
	require.Equal(t, 8, report.SchemaVersion)
	require.Equal(t, "upgrade_required", report.Inspection.Action)
	require.Equal(t, executor.CleanupLinuxDescendants, report.Inspection.Subjects[2].Cleanup.Scope)
	require.Nil(t, report.Inspection.RecoveryRevision)
	require.Equal(t, files, inspectionFiles(t, filepath.Dir(f.path)))
	var after []byte
	require.NoError(t, f.db.QueryRow("SELECT result FROM runs WHERE id=?", *old.RunID).Scan(&after))
	require.Equal(t, original, after)
	migrationVersions(t, f.db, 8)
	var owners, claims int
	require.NoError(t, f.db.QueryRow("SELECT count(*) FROM run_owners").Scan(&owners))
	require.NoError(t, f.db.QueryRow("SELECT count(*) FROM run_work_claims").Scan(&claims))
	require.Equal(t, 1, owners)
	require.Zero(t, claims)
}

func TestInspectionPaginationAndStaleMarkers(t *testing.T) {
	s, owner := retentionStore(t, 8<<20)
	ids := []string{}
	for range 5 {
		ids = append(ids, retentionRun(t, s, owner, "", false).ID)
	}
	require.NoError(t, s.Quarantine(t.Context()))
	options := InspectionOptions{Limit: 2}
	first, err := InspectRuns(t.Context(), s.Path(), testKey, options)
	require.NoError(t, err)
	require.True(t, first.Omitted)
	short := BoundInspection(first, 1)
	require.NotEqual(t, first.NextCursor, short.NextCursor)
	options.Cursor = short.NextCursor
	seen := []string{}
	for range 6 {
		page, err := InspectRuns(t.Context(), s.Path(), testKey, options)
		require.NoError(t, err)
		for _, subject := range page.Subjects {
			if subject.RunID != "" {
				seen = append(seen, subject.RunID)
			}
		}
		options.Cursor = page.NextCursor
		if options.Cursor == "" {
			break
		}
	}
	require.Equal(t, ids, seen, "budget shortening must not skip an entry")
	// Ordinary capture state is per-page, not a reason to invent an epoch.
	require.NoError(t, s.Finish(t.Context(), ids[1], Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true}))
	_, err = InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 2, Cursor: first.NextCursor})
	require.NoError(t, err)
	tx, err := s.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	_, err = tx.Exec("UPDATE run_owners SET cleanup_unverified=1")
	require.NoError(t, err)
	require.NoError(t, advanceRecoveryRevision(t.Context(), tx))
	require.NoError(t, tx.Commit())
	_, err = InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 2, Cursor: first.NextCursor})
	requireErrorCode(t, err, "inspection_stale")
	_, err = InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{RunID: ids[0], Limit: 2, Cursor: short.NextCursor})
	requireErrorCode(t, err, "inspection_stale")
}

func TestInspectionReceiptSelectors(t *testing.T) {
	s, _ := retentionStore(t, 1<<20)
	plan := inspectionPlan{Version: 1, Store: s.id, Hash: strings.Repeat("a", 64)}
	data, err := json.Marshal(plan)
	require.NoError(t, err)
	token := base64.RawURLEncoding.EncodeToString(data)
	for _, tc := range []struct {
		name    string
		options InspectionOptions
		code    string
	}{
		{"receipt absent", InspectionOptions{Limit: 1, ReceiptID: uuid.NewString()}, "receipt_unavailable"},
		{"plan absent", InspectionOptions{Limit: 1, Plan: token}, "receipt_unavailable"},
		{"malformed plan", InspectionOptions{Limit: 1, Plan: "bad"}, "invalid_argument"},
		{"multiple selectors", InspectionOptions{Limit: 1, RunID: uuid.NewString(), Plan: token}, "invalid_argument"},
		{"invalid receipt", InspectionOptions{Limit: 1, ReceiptID: "secret-invalid-id"}, "invalid_argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := InspectRuns(t.Context(), s.Path(), testKey, tc.options)
			requireErrorCode(t, err, tc.code)
			if tc.code == "receipt_unavailable" {
				require.Contains(t, err.Error(), "does not prove nonapplication")
			}
			require.NotContains(t, err.Error(), "secret-invalid-id")
		})
	}
}

func TestInspectionClosedStoreCreatesNoPaths(t *testing.T) {
	f := newLegacyFixture(t)
	f.run(t, executor.CleanupRetired, true, "", "retained")
	f.close(t)
	files := inspectionFiles(t, filepath.Dir(f.path))
	_, err := InspectRuns(t.Context(), f.path, testKey, InspectionOptions{Limit: 8})
	require.NoError(t, err)
	require.Equal(t, files, inspectionFiles(t, filepath.Dir(f.path)))
}

func TestInspectionSQLiteGuardCreatesNoSidecars(t *testing.T) {
	f := newLegacyFixture(t)
	f.run(t, executor.CleanupRetired, true, "", "retained")
	f.close(t)
	before := inspectionFiles(t, filepath.Dir(f.path))
	options, err := inspectionSQLiteOptions()
	require.NoError(t, err)
	// Model a close racing the shared branch after its filesystem check. The
	// dedicated VFS refuses absent WAL/SHM rather than creating either file.
	db, err := sqliteutil.OpenWithCanary(t.Context(), sqliteutil.EncryptedDSN(f.path, testKey)+options, f.path, "fixture")
	if db != nil {
		require.NoError(t, db.Close())
	}
	require.Error(t, err)
	require.Equal(t, before, inspectionFiles(t, filepath.Dir(f.path)))
}

func TestInspectionOfflineFallbackGuards(t *testing.T) {
	for _, mode := range []string{"held lifecycle", "rollback journal"} {
		t.Run(mode, func(t *testing.T) {
			f := newLegacyFixture(t)
			f.close(t)
			code := "storage_busy"
			if mode == "held lifecycle" {
				lease, err := openRegular(filepath.Join(filepath.Dir(f.path), "lifecycle.lock"), false)
				require.NoError(t, err)
				defer lease.Close()
				require.NoError(t, unix.Flock(int(lease.Fd()), unix.LOCK_SH|unix.LOCK_NB))
			} else {
				code = "storage_unavailable"
				require.NoError(t, os.WriteFile(f.path+"-journal", []byte("unreviewed journal"), 0600))
			}
			before := inspectionFiles(t, filepath.Dir(f.path))
			_, err := InspectRuns(t.Context(), f.path, testKey, InspectionOptions{Limit: 8})
			requireErrorCode(t, err, code)
			require.Equal(t, before, inspectionFiles(t, filepath.Dir(f.path)))
		})
	}
}

func TestInspectionExclusiveLeaseTemporarilyExcludesAttachment(t *testing.T) {
	s, owner := retentionStore(t, 1<<20)
	require.NoError(t, owner.Lease.Close())
	require.NoError(t, s.Close())
	lease, err := openRegular(filepath.Join(filepath.Dir(s.Path()), "lifecycle.lock"), false)
	require.NoError(t, err)
	defer lease.Close()
	require.NoError(t, unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	attached, err := Open(t.Context(), s.Path(), testKey, false)
	require.Nil(t, attached)
	requireErrorCode(t, err, "storage_busy")
	require.Contains(t, err.Error(), "lifecycle is busy")
	require.NoError(t, unix.Flock(int(lease.Fd()), unix.LOCK_UN))
	attached, err = Open(t.Context(), s.Path(), testKey, false)
	require.NoError(t, err, "attachment succeeds again after the exclusive snapshot lease is released")
	require.NoError(t, attached.Close())
}

func TestInspectionMinimumBudgetProgressAndPathPrivacy(t *testing.T) {
	s, owner := retentionStore(t, 1<<20)
	for range 2 {
		retentionRun(t, s, owner, "", false)
	}
	view, err := InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 2})
	require.NoError(t, err)
	view.Owner, view.InspectCommand = inspectionOwner("/" + strings.Repeat("'", 240))
	bounded := BoundInspection(view, 0)
	encoded, err := json.Marshal(bounded)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded)+1, 2048)
	require.Len(t, bounded.Subjects, 1)
	require.NotEmpty(t, bounded.NextCursor)
	page, err := InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 1, Cursor: bounded.NextCursor})
	require.NoError(t, err)
	require.Len(t, page.Subjects, 1)
	require.NotEqual(t, bounded.Subjects[0].RunID, page.Subjects[0].RunID)
	_, command := inspectionOwner("/tmp/owner's $(touch sentinel)")
	require.Contains(t, command, `'"'"'`)
	require.Contains(t, command, "$(touch sentinel)") // wholly inside single quotes
	ownerText, command := inspectionOwner("/tmp/unsafe\npath")
	require.NotContains(t, ownerText, "\n")
	require.Empty(t, command)
}

func TestInspectionExpiredRunAndSensitiveFields(t *testing.T) {
	s, owner := retentionStore(t, 1<<20)
	run := retentionRun(t, s, owner, "SECRET_OUTPUT", true)
	expireRun(t, s, run.ID)
	_, err := s.Load(t.Context(), run.ID)
	// Load hides expired output; inspection still returns its retained metadata.
	require.Error(t, err)
	view, err := InspectRuns(t.Context(), s.Path(), testKey, InspectionOptions{Limit: 1, RunID: run.ID})
	require.NoError(t, err)
	require.Len(t, view.Subjects, 1)
	require.False(t, *view.Subjects[0].OutputAvailable)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = InspectRuns(ctx, s.Path(), testKey, InspectionOptions{Limit: 1})
	require.Error(t, err)
	_, err = os.Stat(s.Path())
	require.NoError(t, err)
}
