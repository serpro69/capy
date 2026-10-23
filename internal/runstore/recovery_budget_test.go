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
