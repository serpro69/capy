package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestBatchRecoveryMinimumResponsePreservesHistory(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	id, receipt := "11111111-2222-4333-8444-555555555555", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	now := time.Now().UTC()
	expiry := now.Add(24 * time.Hour)
	batch := runstore.BatchResult{Version: 2, Kind: "batch", BatchID: id, OwnerID: receipt, OwnerType: "mcp", State: "running", RequestedConcurrency: 8, EffectiveConcurrency: 8,
		Cleanup: executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupPending}, RecoveredCount: 2, Counts: map[string]int{"exited": 4, "running": 2, "interrupted": 1, "skipped": 1}, IncompleteCount: 2, FailedCount: 3,
		ChildrenAction: "capy_runs(action=list,batch_id=" + id + ")", SearchAction: &runstore.BatchSearchRequest{Action: "search", BatchID: id, QueryScope: "batch"},
		Recovery: &runstore.Recovery{State: "operator_resolved", ReceiptID: receipt, Basis: "groups_stopped", ResolvedAt: &now, ClosedAt: &now, RetentionExpiresAt: &expiry, OperationallyClosed: true, CaptureClosed: true}}
	for _, scope := range []string{"execute", "status"} {
		t.Run(scope, func(t *testing.T) {
			var response *mcp.CallToolResult
			var err error
			if scope == "execute" {
				response, err = s.buildBatchRunResult(batch)
			} else {
				response, err = execution.Budget(2048, func(n int) (*mcp.CallToolResult, error) {
					return mcp.NewToolResultStructured(map[string]any{"version": 2, "kind": "query", "batch": execution.BoundBatch(batch, n)}, "Batch status; list children by batch_id and read each run_id."), nil
				}, nil)
			}
			require.NoError(t, err)
			encoded, err := json.Marshal(response)
			require.NoError(t, err)
			require.LessOrEqual(t, len(encoded), 2048)
			for _, field := range []string{`"receipt_id":"` + receipt + `"`, `"recovered_count":2`, `"current_concurrency":0`, `"completed_at":null`, `"expires_at":null`, `"state":"pending"`, `"state":"running"`, `"operationally_closed":true`} {
				require.Contains(t, string(encoded), field)
			}
		})
	}
	require.Nil(t, batch.CompletedAt)
	require.Nil(t, batch.ExpiresAt)
}
