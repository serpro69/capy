package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestInspectMissingCreatesNothing(t *testing.T) {
	root := t.TempDir()
	path, err := config.ResolveRunDBPath(root)
	require.NoError(t, err)
	for _, key := range []string{"", testKey} {
		report, err := Inspect(t.Context(), path, key)
		require.NoError(t, err)
		require.False(t, report.Exists)
		require.NoDirExists(t, filepath.Join(root, ".capy"))
	}
}

func TestInspectReadOnlySnapshot(t *testing.T) {
	s, owner := retentionStore(t, 8<<20)
	const secret = "RAW_PROGRAM_OUTPUT_MUST_NOT_APPEAR"
	expired := retentionRun(t, s, owner, secret, true)
	for range 9 {
		retentionRun(t, s, owner, secret, true)
	}
	active := retentionRun(t, s, owner, secret, false)
	expireRun(t, s, expired.ID)
	// Unknown persisted state strings cannot become diagnostic output.
	row, err := s.Load(t.Context(), active.ID)
	require.NoError(t, err)
	row.Result.Index.State = secret
	data, err := json.Marshal(row.Result)
	require.NoError(t, err)
	_, err = s.db.ExecContext(t.Context(), "UPDATE runs SET result=? WHERE id=?", data, active.ID)
	require.NoError(t, err)
	before := accounted(t, s)
	report, err := Inspect(t.Context(), s.Path(), testKey)
	require.NoError(t, err)
	require.Equal(t, s.Policy(), report.Policy)
	require.Equal(t, before, report.AccountedBytes)
	require.EqualValues(t, 10, report.RetainedRuns)
	require.EqualValues(t, 1, report.UnfinishedRuns)
	require.Len(t, report.Recent, diagnosticRunLimit)
	require.Equal(t, active.ID, report.Recent[0].ID)
	require.Equal(t, "running", report.Recent[0].State)
	require.Equal(t, "unknown", report.Recent[0].Index)
	require.EqualValues(t, len(secret), report.Recent[0].CapturedBytes)
	require.Nil(t, report.Recent[0].ExpiresAt)
	require.NotNil(t, report.Recent[1].ExpiresAt)
	require.Positive(t, report.DBBytes)
	require.Positive(t, report.WALBytes, "inspection must include the active WAL")
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.Less(t, len(encoded), 6000)
	require.Equal(t, before, accounted(t, s), "inspection must not maintain expired rows")
	var exists int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM runs WHERE id=?", expired.ID).Scan(&exists))
	require.Equal(t, 1, exists)
	require.NoError(t, s.Finish(t.Context(), active.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true}))
}

func TestInspectFailurePreservesStore(t *testing.T) {
	s, owner := retentionStore(t, 1<<20)
	run := retentionRun(t, s, owner, "kept", true)
	for _, tt := range []struct{ name, key, code string }{
		{"missing credential", "", "credential_unavailable"},
		{"wrong credential", "wrong-synthetic-key", "wrong_key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := accounted(t, s)
			_, err := Inspect(t.Context(), s.Path(), tt.key)
			requireErrorCode(t, err, tt.code)
			require.Equal(t, before, accounted(t, s))
			_, err = s.Load(t.Context(), run.ID)
			require.NoError(t, err)
		})
	}
	require.NoError(t, s.Quarantine(t.Context()))
	report, err := Inspect(t.Context(), s.Path(), testKey)
	require.NoError(t, err)
	require.True(t, report.AdmissionBlocked)
	// Diagnostics must not reconcile or clear the quarantine.
	_, err = s.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, Program{Request: executor.ExecRequest{Code: "forbidden"}})
	requireErrorCode(t, err, "cleanup_unverified")
}

func TestInspectRejectsUnsafePathsAndBusyLifecycle(t *testing.T) {
	for _, mode := range []string{"symlink", "fifo", "missing lock", "busy lock", "oversized metadata"} {
		t.Run(mode, func(t *testing.T) {
			s, owner := retentionStore(t, 1<<20)
			path := s.Path()
			run := retentionRun(t, s, owner, "", true)
			require.NoError(t, owner.Lease.Close())
			require.NoError(t, s.Close())
			switch mode {
			case "symlink", "fifo":
				require.NoError(t, os.Remove(path))
				if mode == "symlink" {
					require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), path))
				} else {
					require.NoError(t, unix.Mkfifo(path, 0600))
				}
			case "missing lock":
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(path), "lifecycle.lock")))
			case "busy lock":
				lock, err := openRegular(filepath.Join(filepath.Dir(path), "lifecycle.lock"), false)
				require.NoError(t, err)
				defer lock.Close()
				require.NoError(t, unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB))
			case "oversized metadata":
				db, err := openAttached(t.Context(), path, testKey, s.owner, unix.LOCK_SH)
				require.NoError(t, err)
				_, err = db.db.Exec("UPDATE runs SET result=zeroblob(65537) WHERE id=?", run.ID)
				require.NoError(t, err)
				require.NoError(t, db.closeHandles())
			}
			start := time.Now()
			_, err := Inspect(t.Context(), path, testKey)
			require.Error(t, err)
			require.Less(t, time.Since(start), 3*time.Second)
		})
	}
}
