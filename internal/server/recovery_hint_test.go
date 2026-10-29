package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRecoveryHintMCPRejectsAllExecutionForms(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	path, err := config.ResolveRunDBPath(s.projectDir)
	require.NoError(t, err)
	st, err := runstore.OpenWithPolicy(t.Context(), path, s.knowledgeKey, true, runstore.PolicyConfig{TTLHours: s.config.Runs.TTLHours, MaxRunBytes: s.config.Runs.MaxRunBytes, MaxRetainedBytes: s.config.Runs.MaxRetainedBytes})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	owner, err := st.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	row, err := st.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{Request: executor.ExecRequest{Code: "PRIVATE_PROGRAM"}})
	require.NoError(t, err)
	require.NoError(t, st.Pending(t.Context(), row.ID, owner.ID, row.Cleanup.Scope))
	require.NoError(t, st.QuarantineRun(t.Context(), runstore.RuntimeIncident{RunID: row.ID, Generation: owner.ID, Slot: owner.Slot, Cause: runstore.IncidentFinalizationUnpersisted, Stage: runstore.StageFinalization}))
	require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "input.txt"), []byte("PRIVATE_INPUT"), 0600))
	for _, tool := range []string{"capy_execute", "capy_execute_file", "capy_batch_execute"} {
		t.Run(tool, func(t *testing.T) {
			args := map[string]any{"language": "shell", "code": "touch must-not-spawn"}
			if tool == "capy_execute_file" {
				args["path"] = "input.txt"
			}
			if tool == "capy_batch_execute" {
				args = map[string]any{"commands": []any{map[string]any{"label": "child", "command": "touch must-not-spawn"}}}
			}
			req := mcp.CallToolRequest{}
			req.Params.Arguments = args
			var response *mcp.CallToolResult
			var err error
			if tool == "capy_batch_execute" {
				response, err = s.handleBatchExecute(t.Context(), req)
			} else {
				response, err = s.handleCapturedExecute(t.Context(), req, tool)
			}
			require.NoError(t, err)
			require.True(t, response.IsError)
			data, err := json.Marshal(response)
			require.NoError(t, err)
			require.LessOrEqual(t, len(data), 2048)
			require.Contains(t, resultText(response), "No command was admitted")
			require.Contains(t, resultText(response), "capy runs inspect")
			require.NotContains(t, string(data), "PRIVATE_")
			encoded, err := json.Marshal(response.StructuredContent)
			require.NoError(t, err)
			var result struct {
				RunID   *string         `json:"run_id"`
				BatchID string          `json:"batch_id"`
				Error   *runstore.Error `json:"error"`
			}
			require.NoError(t, json.Unmarshal(encoded, &result))
			require.Nil(t, result.RunID)
			require.Empty(t, result.BatchID)
			require.Equal(t, "cleanup_unverified", result.Error.Code)
			require.True(t, result.Error.RecoveryHint.NoAdmission)
			require.Equal(t, "store", result.Error.RecoveryHint.Scope)
			require.EqualValues(t, 1, *result.Error.RecoveryHint.IncidentCount)
			require.NoFileExists(t, filepath.Join(s.projectDir, "must-not-spawn"))
		})
	}
	view, err := runstore.InspectRuns(t.Context(), path, s.knowledgeKey, runstore.InspectionOptions{Limit: 64})
	require.NoError(t, err)
	require.EqualValues(t, 1, view.Counts.UnresolvedIncidents)
	query := callRuns(t, s, map[string]any{"action": "search", "run_id": row.ID, "queries": []string{"prefix"}})
	require.True(t, query.IsError)
	assertRunBudget(t, query, 2048)
	require.Contains(t, resultText(query), "capy runs inspect")
	require.Equal(t, "cleanup_unverified", query.StructuredContent.(map[string]any)["error"].(*runstore.Error).Code)
}

func TestRecoveryHintKeepsReasonWithAmpleBudget(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 8192
	result := runstore.Rejection("cleanup_unverified", "")
	result.Error = runstore.AdmissionError(result.Error, s.projectDir)
	response, err := s.buildRunResult(result)
	require.NoError(t, err)
	require.Contains(t, resultText(response), "unverified cleanup")
	bounded := response.StructuredContent.(runstore.Result)
	require.False(t, bounded.MetadataOmitted)
}

func TestRecoveryHintMCPMinimumBudgetAndRepairs(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	for _, code := range []string{"wrong_key", "credential_unavailable", "storage_busy", "schema_upgrade_busy", "schema_upgrade_failed", "schema_upgrade_capacity", "owner_slots_exhausted", "incident_publication_failed", "schema_mismatch", "recovery_plan_stale", "recovery_inconsistent", "recovery_capacity"} {
		t.Run(code, func(t *testing.T) {
			problem := runstore.AdmissionError(&runstore.Error{Code: code, Message: "PRIVATE_ERROR " + strings.Repeat("x", 8192)}, "/tmp/"+strings.Repeat("q", 200)+"'quote")
			for _, batch := range []bool{false, true} {
				var response *mcp.CallToolResult
				var err error
				if batch {
					response, err = s.buildBatchRunResult(runstore.BatchResult{Kind: "rejection", Version: 2, State: "not_started", Error: problem})
				} else {
					result := runstore.Rejection(code, "")
					result.Error = problem
					response, err = s.buildRunResult(result)
				}
				require.NoError(t, err)
				data, err := json.Marshal(response)
				require.NoError(t, err)
				require.LessOrEqual(t, len(data), 2048)
				require.Contains(t, string(data), code)
				require.Contains(t, resultText(response), "No command was admitted")
				require.Contains(t, resultText(response), "capy runs inspect")
				require.NotContains(t, string(data), "PRIVATE_ERROR")
				require.NotContains(t, string(data), "--discard")
			}
		})
	}
}
