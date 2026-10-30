package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRecoveryResultMinimumBudgetPreservesEvidence(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	id, receipt := "11111111-2222-4333-8444-555555555555", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	cursor := strings.Repeat("c", 220)
	original := runstore.Result{Version: 2, Kind: "run", RunID: &id, OwnerID: receipt, OwnerType: "mcp", State: "running",
		Cleanup:   executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupPending},
		Capture:   runstore.Capture{State: "incomplete", LastCommittedCursor: &cursor},
		Redaction: runstore.Projection{Version: 1, State: "partial", CoverageCursor: &cursor}, Index: runstore.Index{State: "not_requested"},
		Preview: strings.Repeat("large preview", 1000), Recovery: &runstore.Recovery{State: "operator_resolved", ReceiptID: receipt, DispositionID: id, ResolvedAt: &now, Basis: "groups_stopped", RetentionExpiresAt: &expires, OperationallyClosed: true, CaptureClosed: true}}
	original.StartedAt = &now
	original.Index.CoverageCursor = &cursor
	original.Recovery.IncidentID = receipt
	for _, shape := range []string{"execute envelope", "status envelope", "list envelope"} {
		t.Run(shape, func(t *testing.T) {
			var response *mcp.CallToolResult
			var err error
			switch shape {
			case "execute envelope":
				response, err = s.buildRunResult(original)
			case "status envelope":
				response, err = execution.Budget(2048, func(n int) (*mcp.CallToolResult, error) {
					return mcp.NewToolResultStructured(map[string]any{"version": 2, "kind": "query", "run": execution.BoundResult(original, n)}, "Last recorded run state."), nil
				}, nil)
			case "list envelope":
				response, err = execution.Budget(2048, func(int) (*mcp.CallToolResult, error) {
					return mcp.NewToolResultStructured(runstore.ListPage{Version: 2, Kind: "query", Entries: []runstore.Summary{{RunID: id, OwnerID: receipt, State: original.State, Cleanup: original.Cleanup, CaptureState: "incomplete", OutputAvailable: true, Recovery: original.Recovery}}}, "Historical run summary."), nil
				}, nil)
			}
			require.NoError(t, err)
			wire, err := json.Marshal(response)
			require.NoError(t, err)
			require.LessOrEqual(t, len(wire), 2048)
			require.Contains(t, string(wire), `"receipt_id":"`+receipt+`"`)
			require.Contains(t, string(wire), `"disposition_id":"`+id+`"`)
			require.Contains(t, string(wire), `"state":"running"`)
			require.Contains(t, string(wire), `"exit_code":null`)
			require.Contains(t, string(wire), `"state":"pending"`)
			if shape != "list envelope" {
				require.Contains(t, string(wire), `"completed_at":null`)
				require.Contains(t, string(wire), `"terminal_persisted":false`)
			}
		})
	}
	require.Len(t, original.Preview, len("large preview")*1000)
	require.Nil(t, original.CompletedAt)
}

func TestRecoveryRecordedOutcomeMinimumBudget(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	id, receipt := "11111111-2222-4333-8444-555555555555", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	rawCursor, projectionCursor, indexCursor := strings.Repeat("r", 236), strings.Repeat("p", 291), strings.Repeat("i", 240)
	exit := 7
	r := runstore.Result{Version: 2, Kind: "run", RunID: &id, OwnerID: receipt, OwnerType: "mcp", State: "interrupted", ExitCode: &exit, StartedAt: &now, CompletedAt: &now, ExpiresAt: &expires,
		Cleanup: executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupPending}, Capture: runstore.Capture{State: "incomplete", LastCommittedCursor: &rawCursor, TerminalPersisted: true},
		Redaction: runstore.Projection{Version: 1, State: "partial", CoverageCursor: &projectionCursor}, Index: runstore.Index{State: "failed", CoverageCursor: &indexCursor, Error: &runstore.Error{Code: "index_unavailable", Message: "historical index failure"}},
		Recovery: &runstore.Recovery{State: "operator_resolved", IncidentID: id, ReceiptID: receipt, DispositionID: id, ResolvedAt: &now, Basis: "groups_stopped", RetentionExpiresAt: &expires, OperationallyClosed: true, CaptureClosed: true}}
	response, err := s.buildRunResult(r)
	require.NoError(t, err)
	wire, err := json.Marshal(response)
	require.NoError(t, err)
	require.LessOrEqual(t, len(wire), 2048)
	require.Contains(t, string(wire), `"receipt_id":"`+receipt+`"`)
	_, err = execution.Budget(2048, func(n int) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultStructured(map[string]any{"version": 2, "kind": "query", "run": execution.BoundResult(r, n)}, fmt.Sprintf("Run %s: last recorded %s; capture %s.", id, r.State, r.Capture.State)), nil
	}, nil)
	require.NoError(t, err)
	bounded := execution.BoundResult(r, 0)
	require.True(t, bounded.MetadataOmitted)
	require.Equal(t, r.Capture.LastCommittedCursor, bounded.Capture.LastCommittedCursor)
	require.Equal(t, r.CompletedAt, bounded.CompletedAt)
	require.Equal(t, r.ExpiresAt, bounded.ExpiresAt)
	require.Equal(t, r.Recovery, bounded.Recovery)
	require.Nil(t, bounded.Index.CoverageCursor)
	require.Nil(t, bounded.Redaction.CoverageCursor)
	require.NotNil(t, r.Index.CoverageCursor)
	require.NotNil(t, r.Redaction.CoverageCursor)
	for _, raw := range []bool{false, true} {
		readResponse, err := execution.Budget(2048, func(n int) (*mcp.CallToolResult, error) {
			page := runstore.ReadPage{Version: 2, Kind: "query", RunID: id, Run: r, NextCursor: &projectionCursor, Fragments: []runstore.Fragment{{Stream: "stdout", Phase: "run", Sequence: 1, ByteOffset: 0, RawEnd: 1, StartLine: 1, Text: "x"}}, Encoding: "text"}
			fallback := "Sanitized output from the persisted projection."
			if raw {
				page.Raw, page.Encoding, page.NextCursor = true, "base64", &rawCursor
				page.Fragments[0].Text = ""
				page.Fragments[0].Data = []byte("x")
				fallback = "Unsanitized raw output: base64 event fragments."
			}
			return mcp.NewToolResultStructured(execution.BoundRead(page, n), fallback), nil
		}, nil)
		require.NoError(t, err, "raw=%t", raw)
		page, ok := readResponse.StructuredContent.(runstore.ReadPage)
		require.True(t, ok)
		wantCursor := projectionCursor
		if raw {
			wantCursor = rawCursor
		}
		require.Equal(t, &wantCursor, page.NextCursor)
		require.Equal(t, r.Recovery, page.Run.Recovery)
		require.Equal(t, r.CompletedAt, page.Run.CompletedAt)
		require.Equal(t, r.Capture.TerminalPersisted, page.Run.Capture.TerminalPersisted)
		require.Len(t, page.Fragments, 1)
		require.EqualValues(t, 0, page.Fragments[0].ByteOffset)
		require.EqualValues(t, 1, page.Fragments[0].RawEnd)
	}
}

func TestRecoveryIndexedListMinimumBudget(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	cursor := strings.Repeat("c", 240)
	ordinal := 0
	recovery := &runstore.Recovery{State: "operator_resolved", IncidentID: id, ReceiptID: id, DispositionID: id,
		ResolvedAt: &now, Basis: "groups_stopped", RetentionExpiresAt: &expires, OperationallyClosed: true, CaptureClosed: true}
	entry := runstore.Summary{RunID: id, BatchID: id, OwnerID: id, OwnerType: "mcp", Ordinal: &ordinal, Label: strings.Repeat("<", 80),
		State: "running", Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupPending},
		CaptureState: "incomplete", CapturedBytes: 1000, CreatedAt: now, OutputAvailable: true, Recovery: recovery}
	labelNeeded := false
	for size := 80; size <= 160; size++ {
		t.Run(fmt.Sprintf("escaped label %d", size), func(t *testing.T) {
			entry.Label = strings.Repeat("<", size)
			list := runstore.ListPage{Version: 2, Kind: "query", BatchID: id, Entries: []runstore.Summary{entry}, NextCursor: &cursor}
			baseline, err := json.Marshal(mcp.NewToolResultStructured(list, "Captured run summaries; use status or read by run_id."))
			require.NoError(t, err)
			list.Entries[0].Index = &runstore.Index{State: "failed", CoverageCursor: &cursor, Error: &runstore.Error{Code: "index_unavailable", Message: "index work was interrupted or unavailable; retry search to resume retained safe coverage"}}
			// Reproduce the initial correction's remaining boundary: index cursor
			// and prose removed, but the original label still fills the envelope.
			indexOnly := execution.BoundList(list, 0)
			indexOnly.Entries[0].Label = entry.Label
			unboundedLabel, err := json.Marshal(mcp.NewToolResultStructured(indexOnly, "Captured run summaries; use status or read by run_id."))
			require.NoError(t, err)
			if len(baseline) <= 2048 && len(unboundedLabel) > 2048 {
				if !labelNeeded {
					t.Logf("label %d: without index %d bytes; compact index with original label %d bytes", size, len(baseline), len(unboundedLabel))
				}
				labelNeeded = true
			}
			response, err := execution.Budget(2048, func(n int) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultStructured(execution.BoundList(list, n), "Captured run summaries; use status or read by run_id."), nil
			}, nil)
			require.NoError(t, err)
			wire, err := json.Marshal(response)
			require.NoError(t, err)
			require.LessOrEqual(t, len(wire), 2048)
			page := response.StructuredContent.(runstore.ListPage)
			require.Equal(t, list.NextCursor, page.NextCursor)
			require.Equal(t, entry.Recovery, page.Entries[0].Recovery)
			require.Equal(t, "failed", page.Entries[0].Index.State)
			require.Equal(t, "index_unavailable", page.Entries[0].Index.Error.Code)
			require.True(t, page.Entries[0].MetadataOmitted)
			require.NotNil(t, list.Entries[0].Index.CoverageCursor, "formatting must not mutate the original view")
			require.Equal(t, entry.Label, list.Entries[0].Label)
		})
	}
	require.True(t, labelNeeded, "the sweep must include a formerly fitting label that needs shortening")
}

func TestRecoverySearchMinimumBudget(t *testing.T) {
	id := "11111111-2222-4333-8444-555555555555"
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	cursor := strings.Repeat("c", 240)
	recovery := &runstore.Recovery{State: "operator_resolved", IncidentID: id, ReceiptID: id, DispositionID: id,
		ResolvedAt: &now, Basis: "groups_stopped", RetentionExpiresAt: &expires, OperationallyClosed: true, CaptureClosed: true}
	for _, batch := range []bool{false, true} {
		page := runstore.SearchPage{Version: 2, Kind: "query", RunID: id, Recovery: recovery,
			Cleanup: executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupPending},
			Index:   runstore.Index{State: "partial", CoverageCursor: &cursor}, Partial: true,
			Coverage: map[string]int64{"stdout": 1 << 30, "stderr": 1 << 30},
			Results:  []runstore.QueryHits{{Hits: []runstore.SearchHit{{RunID: id, ReadCursor: cursor, Text: strings.Repeat("hit ", 1000)}}}}}
		if batch {
			page.BatchID, page.RunID = id, ""
			batchRecovery := *recovery
			batchRecovery.DispositionID = ""
			page.Recovery = &batchRecovery
			page.CoverageOmitted = true
			page.Children = []runstore.SearchCoverage{{RunID: id, Recovery: recovery, Index: page.Index, OutputAvailable: true}}
			page.Error = runstore.AdmissionError(&runstore.Error{Code: "cleanup_unverified"}, "/tmp/"+strings.Repeat("<", 160))
			page.Error.RecoveryHint.Subject = &runstore.InspectionSubject{RunID: id}
			page.Index.State, page.Index.Error = "failed", page.Error
		}
		response, err := execution.Budget(2048, func(n int) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultStructured(execution.BoundSearch(page, n), "Search covers retained safe prefixes; closed capture cannot gain withheld output."), nil
		}, nil)
		require.NoError(t, err)
		wire, err := json.Marshal(response)
		require.NoError(t, err)
		require.LessOrEqual(t, len(wire), 2048)
		require.Contains(t, string(wire), `"receipt_id":"`+id+`"`)
		require.Contains(t, string(wire), `"capture_closed":true`)
		require.Contains(t, string(wire), `"partial":true`)
		if batch {
			require.Contains(t, string(wire), `"coverage_omitted":true`)
			require.Contains(t, string(wire), `"code":"cleanup_unverified"`)
			require.Contains(t, string(wire), `"next_step":`)
		}
	}
}

func TestUnresolvedHistoryActualMCPBudgets(t *testing.T) {
	s := newTestServer(t, nil)
	s.config.Runs.MaxResponseBytes = 2048
	path, err := config.ResolveRunDBPath(s.projectDir)
	require.NoError(t, err)
	store, err := runstore.Open(t.Context(), path, s.knowledgeKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	owner, err := store.ClaimOwner(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Lease.Close()) })
	r, err := store.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
	require.NoError(t, err)
	require.NoError(t, store.Pending(t.Context(), r.ID, owner.ID, r.Cleanup.Scope))
	require.NoError(t, store.Started(t.Context(), r.ID, "run"))
	require.NoError(t, store.Append(t.Context(), r.ID, executor.OutputEvent{Sequence: 1, Phase: "run", Stream: "stdout", Data: []byte(strings.Repeat("retained safe output\n", 100))}))
	_, err = store.Search(t.Context(), owner, runstore.SearchOptions{RunID: r.ID, Queries: []string{"retained"}, Limit: 1})
	require.NoError(t, err)
	before, err := store.Load(t.Context(), r.ID)
	require.NoError(t, err)
	require.NotNil(t, before.Result.Index.CoverageCursor)
	require.NoError(t, store.QuarantineRun(t.Context(), runstore.RuntimeIncident{RunID: r.ID, Generation: owner.ID, Slot: owner.Slot, Cause: runstore.IncidentSupervisorLost, Stage: runstore.StageHelperWait}))
	for _, args := range []map[string]any{
		{"action": "status", "run_id": r.ID},
		{"action": "list", "limit": 1},
		{"action": "read", "run_id": r.ID, "stream": "stdout", "max_bytes": 4096},
		{"action": "read", "run_id": r.ID, "stream": "stdout", "max_bytes": 4096, "raw": true},
	} {
		response := callRuns(t, s, args)
		require.False(t, response.IsError, resultText(response))
		wire, err := json.Marshal(response)
		require.NoError(t, err)
		require.LessOrEqual(t, len(wire), 2048)
		require.Contains(t, string(wire), `"recovery"`)
		require.Contains(t, string(wire), `"state":"unresolved"`)
		if args["action"] == "read" {
			page, ok := response.StructuredContent.(runstore.ReadPage)
			require.True(t, ok)
			require.NotNil(t, page.NextCursor)
			require.False(t, page.EOF)
			require.NotEmpty(t, page.Fragments)
		}
	}
}
