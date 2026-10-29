package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func stdioInspection(t *testing.T, p *stdioProcess, args map[string]any) runstore.Inspection {
	t.Helper()
	var wire json.RawMessage
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": args}, &wire))
	require.LessOrEqual(t, len(wire), 2048, "complete MCP tool result must fit the configured minimum")
	var result struct {
		IsError bool                `json:"isError"`
		View    runstore.Inspection `json:"structuredContent"`
	}
	require.NoError(t, json.Unmarshal(wire, &result))
	require.False(t, result.IsError, "%s", wire)
	require.Equal(t, "inspection", result.View.Kind)
	return result.View
}

func TestMCPRunInspectionLegacyPreservesStore(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, version := range []int{8, 9} {
		t.Run(fmt.Sprintf("schema-%d", version), func(t *testing.T) {
			project, path, key := upgradeCLIFixture(t, version, true)
			env := stdioEnv(t, key)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			out, diag, status := capturedCLI(t, bin, project, env, "runs", "inspect", "--json", "--project-dir", project)
			require.Zero(t, status, "%s", out)
			require.Empty(t, diag)
			var cli runstore.Inspection
			require.NoError(t, json.Unmarshal(out, &cli))
			p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
			p.initialize(t)
			var tools struct {
				Tools []struct {
					Name        string `json:"name"`
					InputSchema struct {
						Properties map[string]map[string]any `json:"properties"`
					} `json:"inputSchema"`
				} `json:"tools"`
			}
			require.NoError(t, p.request("tools/list", map[string]any{}, &tools))
			found := false
			for _, tool := range tools.Tools {
				if tool.Name == "capy_runs" {
					found = true
					require.Contains(t, tool.InputSchema.Properties["action"]["enum"], "inspect")
					require.Contains(t, tool.InputSchema.Properties, "receipt_id")
					require.Contains(t, tool.InputSchema.Properties, "plan")
					require.NotContains(t, tool.InputSchema.Properties, "apply")
				}
			}
			require.True(t, found)
			view := stdioInspection(t, p, map[string]any{"action": "inspect"})
			require.Equal(t, version, view.SchemaVersion)
			require.Nil(t, view.RecoveryRevision)
			require.Equal(t, "upgrade_required", view.Action)
			require.Contains(t, view.NextStep, "runs upgrade")
			require.Equal(t, cli.StoreID, view.StoreID)
			require.Equal(t, cli.Admission, view.Admission)
			require.Equal(t, cli.Counts, view.Counts)
			require.Equal(t, cli.Subjects, view.Subjects)
			p.close(t)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after, "MCP inspection must not migrate, reconcile, claim an owner or publish policy")
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				require.NoFileExists(t, path+suffix)
			}
		})
	}
}

func TestMCPRunInspectionReceiptMatchesCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	key := "synthetic-inspection-receipt-key"
	path, err := config.ResolveRunDBPath(project)
	require.NoError(t, err)
	st, err := runstore.Open(t.Context(), path, key, true)
	require.NoError(t, err)
	storeID := st.ID()
	require.NoError(t, st.Close())
	db := upgradeFixtureDB(t, path, key)
	receipt, incident := uuid.NewString(), uuid.NewString()
	// Private audit fixture; no public recovery application is exercised.
	_, err = db.Exec(`INSERT INTO run_recovery_receipts(id,plan_hash,store_id,version,applied_at,method,basis,uid,explanation,incident_count,run_count,owner_count,store_count,charged_bytes,audit_expires_at)
 VALUES(?,?,?,1,1,'operator','groups_stopped',0,'reviewed fixture',1,0,0,1,0,1)`, receipt, strings.Repeat("c", 64), storeID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO run_incidents(id,version,subject_kind,subject_key,reason,attribution,stage,last_phase,revision,receipt_id,charged_bytes)
 VALUES(?,1,'store','store','legacy_unattributed','unattributed','legacy_inspection','unknown',1,?,0)`, incident, receipt)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	env := stdioEnv(t, key)
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "inspect", "--json", "--receipt-id", receipt, "--project-dir", project)
	require.Zero(t, status, "%s", out)
	require.Empty(t, diag)
	var cli runstore.Inspection
	require.NoError(t, json.Unmarshal(out, &cli))
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	view := stdioInspection(t, p, map[string]any{"action": "inspect", "receipt_id": receipt})
	require.Equal(t, cli.StoreID, view.StoreID)
	require.Equal(t, cli.Receipt, view.Receipt)
	require.Equal(t, cli.Counts, view.Counts)
	require.Equal(t, cli.Subjects, view.Subjects)
	out, _, status = capturedCLI(t, bin, project, env, "runs", "inspect", "--json", "--receipt-id", incident, "--project-dir", project)
	require.Equal(t, 1, status)
	require.Contains(t, string(out), "receipt_unavailable")
	var failure struct {
		IsError bool `json:"isError"`
		Body    struct {
			Error runstore.Error `json:"error"`
		} `json:"structuredContent"`
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "inspect", "receipt_id": incident}}, &failure))
	require.True(t, failure.IsError)
	require.Equal(t, "receipt_unavailable", failure.Body.Error.Code)
	p.close(t)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
