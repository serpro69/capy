package runstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func TestRecoveryMetadataUsesRecoveryBudget(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		name := "within recovery budget"
		if cancelled {
			name = "earlier caller deadline"
		}
		t.Run(name, func(t *testing.T) {
			s, o := retentionStore(t, 16<<20)
			row, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, Program{Request: executor.ExecRequest{Language: executor.Shell, Code: "#" + strings.Repeat("<", (1<<20)-1)}})
			require.NoError(t, err)
			conn, err := s.db.Conn(t.Context())
			require.NoError(t, err)
			require.NoError(t, conn.Raw(func(driver any) error {
				return driver.(*sqlite3.SQLiteConn).RegisterFunc("slow_recovery_metadata", func() int {
					time.Sleep(350 * time.Millisecond)
					return 0
				}, false)
			}))
			require.NoError(t, conn.Close())
			_, err = s.db.ExecContext(t.Context(), "CREATE TRIGGER slow_recovery BEFORE UPDATE OF result ON runs WHEN OLD.terminal=0 BEGIN SELECT slow_recovery_metadata(); END")
			require.NoError(t, err)
			require.NoError(t, o.Lease.Close())
			ctx := t.Context()
			if cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			report, err := s.Reconcile(ctx)
			if cancelled {
				require.Error(t, err)
				var terminal bool
				var completed sql.NullInt64
				require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT terminal,completed_at FROM runs WHERE id=?", row.ID).Scan(&terminal, &completed))
				require.False(t, terminal)
				require.False(t, completed.Valid, "cancelled recovery must roll back")
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, report.RecoveredRuns)
				got, err := s.Load(t.Context(), row.ID)
				require.NoError(t, err)
				require.Equal(t, "interrupted", got.Result.State)
				require.Equal(t, executor.CleanupNotSpawned, got.Cleanup.State)
				require.True(t, got.Result.Capture.TerminalPersisted)
			}
			_, err = s.db.ExecContext(t.Context(), "DROP TRIGGER slow_recovery")
			require.NoError(t, err)
		})
	}
}

func TestAdmissionAccountingPreservesCallerBudgets(t *testing.T) {
	s, o := retentionStore(t, 16<<20)
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("slow_accounting", func() int {
			time.Sleep(350 * time.Millisecond)
			return 0
		}, false)
	}))
	require.NoError(t, conn.Close())
	_, err = s.db.ExecContext(t.Context(), "CREATE TRIGGER slow_charge BEFORE UPDATE OF accounted_bytes ON runs BEGIN SELECT slow_accounting(); END")
	require.NoError(t, err)
	program := Program{Request: executor.ExecRequest{Language: executor.Shell, Code: "#" + strings.Repeat("<", (1<<20)-1)}}
	row, err := s.Admit(t.Context(), o, executor.CleanupLinuxDescendants, program)
	require.NoError(t, err, "valid admission may use its bounded admission budget")
	before := accounted(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, err = s.Admit(ctx, o, executor.CleanupLinuxDescendants, Program{})
	cancel()
	require.Error(t, err, "earlier caller cancellation must still abort admission")
	require.Equal(t, before, accounted(t, s))
	// Cancellation can retire a driver connection. Install the function on the
	// current connection before exercising the separate capture-write boundary.
	conn, err = s.db.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("slow_accounting", func() int {
			time.Sleep(350 * time.Millisecond)
			return 0
		}, false)
	}))
	require.NoError(t, conn.Close())
	require.NoError(t, s.Pending(t.Context(), row.ID, o.ID, executor.CleanupLinuxDescendants))
	require.NoError(t, s.Started(t.Context(), row.ID, "run"))
	err = s.Append(t.Context(), row.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte("must roll back")})
	require.Error(t, err, "capture append must keep its shorter write deadline")
	var events int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_events WHERE run_id=?", row.ID).Scan(&events))
	require.Zero(t, events)
	require.Equal(t, before, accounted(t, s), "failed capture cannot charge uncommitted payload")
	_, err = s.db.ExecContext(t.Context(), "DROP TRIGGER slow_charge")
	require.NoError(t, err)
	require.NoError(t, s.Finish(t.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true, CaptureLost: true}))
}
