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

func TestBatchRecoveryCLIReportsAdministrativeClosure(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	batch := runstore.BatchResult{Version: 2, Kind: "batch", BatchID: id, State: "running", RecoveredCount: 1, Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending}, Recovery: &runstore.Recovery{State: "operator_resolved", ReceiptID: id, Basis: "groups_stopped", ClosedAt: &now, RetentionExpiresAt: &expiry, OperationallyClosed: true, CaptureClosed: true}}
	value := runQueryResult{Version: 2, Kind: "query", Batch: &batch}
	_, err := execution.Budget(2048, func(int) (any, error) { return value, nil }, encodeRunText)
	require.NoError(t, err)
	text, err := encodeRunText(value)
	require.NoError(t, err)
	require.Contains(t, string(text), "last recorded running")
	require.Contains(t, string(text), "Operator-disposed children: 1; administratively closed: true")
	require.Contains(t, string(text), "Original batch completion is unchanged")
	require.Contains(t, string(text), "Batch history deadline:")
	require.NotContains(t, string(text), "Retained output deadline:")
	require.Equal(t, &expiry, batch.Recovery.RetentionExpiresAt)
	require.Nil(t, batch.CompletedAt)
}

func TestBatchRecoveryCLIIncidentOnlyKeepsExecutionCompletion(t *testing.T) {
	now := time.Now().UTC()
	batch := runstore.BatchResult{Version: 2, Kind: "batch", BatchID: "11111111-2222-4333-8444-555555555555", State: "completed", CompletedAt: &now, Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Recovery: &runstore.Recovery{State: "operator_resolved", ReceiptID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", Basis: "groups_stopped", OperationallyClosed: true}}
	text, err := encodeRunText(runQueryResult{Version: 2, Kind: "query", Batch: &batch})
	require.NoError(t, err)
	require.Contains(t, string(text), ": completed;")
	require.NotContains(t, string(text), "last recorded completed")
	require.NotContains(t, string(text), "administratively closed")
}
