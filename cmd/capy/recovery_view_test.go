package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRecoveryViewCLIHistoricalTextAndBudget(t *testing.T) {
	id, receipt := "11111111-2222-4333-8444-555555555555", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	r := runstore.Result{Version: 2, Kind: "run", RunID: &id, State: "running", Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}, Capture: runstore.Capture{State: "incomplete"}, Index: runstore.Index{State: "not_requested"}, Recovery: &runstore.Recovery{State: "operator_resolved", ReceiptID: receipt, DispositionID: id, ResolvedAt: &now, Basis: "groups_stopped", RetentionExpiresAt: &expiry, OperationallyClosed: true, CaptureClosed: true}}
	for _, value := range []any{runQueryResult{Version: 2, Kind: "query", Run: &r}, runstore.ListPage{Version: 2, Kind: "query", Entries: []runstore.Summary{{RunID: id, State: r.State, Cleanup: r.Cleanup, CaptureState: r.Capture.State, Recovery: r.Recovery}}}} {
		encoded, err := execution.Budget(2048, func(int) (any, error) { return value, nil }, encodeRunText)
		require.NoError(t, err)
		text, err := encodeRunText(encoded)
		require.NoError(t, err)
		require.Contains(t, string(text), "last recorded running")
		require.Contains(t, string(text), receipt)
		require.Contains(t, string(text), "Original execution evidence is unchanged")
		require.Contains(t, string(text), "quota may evict sooner")
		wire, err := json.Marshal(value)
		require.NoError(t, err)
		require.Contains(t, string(wire), `"disposition_id":"`+id+`"`)
	}
}
