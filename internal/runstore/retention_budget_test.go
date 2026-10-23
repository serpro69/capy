package runstore

import (
	"context"
	"strings"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

func slowRetention(t *testing.T, s *Store) {
	t.Helper()
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, conn.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("slow_retention", func() int {
			time.Sleep(350 * time.Millisecond)
			return 0
		}, false)
	}))
	require.NoError(t, conn.Close())
	_, err = s.db.ExecContext(t.Context(), "CREATE TRIGGER slow_retention_work BEFORE UPDATE OF evicting ON runs BEGIN SELECT slow_retention(); END")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), recoveryTimeout)
		defer cancel()
		_, err := s.db.ExecContext(ctx, "DROP TRIGGER slow_retention_work")
		require.NoError(t, err)
	})
}

func TestRetentionWorkUsesOperationBudgets(t *testing.T) {
	for _, operation := range []string{"admission", "maintenance"} {
		for _, cancelled := range []bool{false, true} {
			name := operation + "/within metadata budget"
			if cancelled {
				name = operation + "/earlier caller deadline"
			}
			t.Run(name, func(t *testing.T) {
				s, owner := retentionStore(t, 2*runReserve+10000)
				victim := retentionRun(t, s, owner, "retained bytes", true)
				if operation == "maintenance" {
					expireRun(t, s, victim.ID)
				}
				before := accounted(t, s)
				slowRetention(t, s)
				ctx := t.Context()
				if cancelled {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
					defer cancel()
				}
				var err error
				if operation == "admission" {
					_, err = s.Admit(ctx, owner, executor.CleanupLinuxDescendants, Program{Request: executor.ExecRequest{Language: executor.Shell, Code: "#" + strings.Repeat("x", 65536)}})
				} else {
					var report RetentionReport
					report, err = s.Maintain(ctx)
					if !cancelled && err == nil {
						require.Equal(t, 1, report.Expired)
					}
				}
				if cancelled {
					require.Error(t, err)
					require.Equal(t, before, accounted(t, s), "cancelled retention must roll back its debit")
					var count, evicting int
					require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*),coalesce(sum(evicting),0) FROM runs").Scan(&count, &evicting))
					require.Equal(t, 1, count)
					require.Zero(t, evicting)
				} else {
					require.NoError(t, err, "metadata retention may use its bounded operation budget")
					var count int
					require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM runs WHERE id=?", victim.ID).Scan(&count))
					require.Zero(t, count)
				}
			})
		}
	}
}

func TestCaptureRetentionKeepsWriteDeadline(t *testing.T) {
	s, owner := retentionStore(t, 2*runReserve+10000)
	retentionRun(t, s, owner, "retained", true)
	active := retentionRun(t, s, owner, "", false)
	before := accounted(t, s)
	slowRetention(t, s)
	err := s.Append(t.Context(), active.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte(strings.Repeat("x", 12000))})
	require.Error(t, err, "streaming capture still uses the shorter write deadline")
	require.Equal(t, before, accounted(t, s))
	var count int
	require.NoError(t, s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM run_events WHERE run_id=?", active.ID).Scan(&count))
	require.Zero(t, count, "failed capture must not retain uncommitted output")
}
