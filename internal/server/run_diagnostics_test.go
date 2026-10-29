package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/platform"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestRunDiagnosticsOldBlockerWithoutCompletionAccounting(t *testing.T) {
	isolateRunDiagnostics(t)
	srv := newTestServer(t, nil)
	path, err := config.ResolveRunDBPath(srv.projectDir)
	require.NoError(t, err)
	store, err := runstore.Open(t.Context(), path, srv.knowledgeKey, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	owner, err := store.ClaimOwner(t.Context())
	require.NoError(t, err)
	var old string
	for i := range 11 {
		run, err := store.Admit(t.Context(), owner, executor.CleanupLinuxDescendants, runstore.Program{})
		require.NoError(t, err)
		if i == 0 {
			old = run.ID
			require.NoError(t, store.Pending(t.Context(), run.ID, owner.ID, executor.CleanupLinuxDescendants))
		} else {
			require.NoError(t, store.Finish(t.Context(), run.ID, runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupNotSpawned}}))
		}
	}
	require.NoError(t, owner.Lease.Close())
	_, err = store.Reconcile(t.Context())
	require.NoError(t, err)
	before := srv.stats.Snapshot()
	for _, call := range []func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){srv.handleDoctor, srv.handleStats} {
		result, err := call(t.Context(), mcp.CallToolRequest{})
		require.NoError(t, err)
		require.Contains(t, resultText(result), old)
		require.Contains(t, resultText(result), "recorded quarantine markers=2; recorded incidents=0; inferred incidents=1")
		require.Nil(t, srv.runs)
	}
	after := srv.stats.Snapshot()
	require.Equal(t, before.RunsCompleted, after.RunsCompleted)
	require.Equal(t, before.RunsIncomplete, after.RunsIncomplete)
	require.Equal(t, before.RunObservedBytes, after.RunObservedBytes)
	require.Equal(t, before.RunCapturedBytes, after.RunCapturedBytes)
	require.Greater(t, after.BytesReturned["capy_stats"], before.BytesReturned["capy_stats"], "ordinary response accounting still occurs")
	require.Greater(t, after.BytesReturned["capy_doctor"], before.BytesReturned["capy_doctor"])
}

func isolateRunDiagnostics(t *testing.T) {
	t.Helper()
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	t.Setenv("CAPY_VAULT_KEY", "")
}

func TestRunDiagnosticsNoCreationAndSharedOutput(t *testing.T) {
	isolateRunDiagnostics(t)
	srv := newTestServer(t, nil)
	for _, call := range []func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){srv.handleDoctor, srv.handleStats} {
		result, err := call(t.Context(), mcp.CallToolRequest{})
		require.NoError(t, err)
		require.Contains(t, resultText(result), "inspection created no run store")
		require.Nil(t, srv.runs, "diagnostics must not initialize the execution service")
		require.NoDirExists(t, filepath.Join(srv.projectDir, ".capy", "runs"))
	}
	callTool(t, srv, map[string]any{"language": "shell", "code": "printf SAFE_DIAGNOSTIC_SECRET"})
	snap := srv.stats.Snapshot()
	require.EqualValues(t, 22, snap.RunCapturedBytes)
	report := srv.runChecks(t.Context())
	shared := platform.CheckRuns(t.Context(), srv.projectDir, srv.knowledgeKey, srv.config.Runs)
	require.Equal(t, shared, report[:len(shared)])
	for _, item := range report {
		require.NotContains(t, item.String(), "SAFE_DIAGNOSTIC_SECRET")
		require.NotContains(t, item.String(), srv.knowledgeKey)
	}
	srv.config.Runs.TTLHours++
	report = srv.runChecks(t.Context())
	require.Contains(t, platform.FormatDiagnostics(report), "configured policy differs")
	require.Contains(t, platform.FormatDiagnostics(report), "inspection did not publish a revision")
}

func TestStatsYieldedRunCompletionOnce(t *testing.T) {
	isolateRunDiagnostics(t)
	for _, tool := range []string{"execute", "file", "batch"} {
		t.Run(tool, func(t *testing.T) {
			srv := newTestServer(t, nil)
			program := "printf early; touch ready; while [ ! -f release ]; do sleep .01; done; printf late >&2"
			args := map[string]any{"language": "shell", "code": program, "wait_ms": 0}
			var result *mcp.CallToolResult
			var err error
			switch tool {
			case "execute":
				result = callTool(t, srv, args)
			case "file":
				require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "input"), []byte("ignored"), 0600))
				args["path"] = "input"
				req := mcp.CallToolRequest{}
				req.Params.Arguments = args
				result, err = srv.handleExecuteFile(t.Context(), req)
			case "batch":
				req := mcp.CallToolRequest{}
				req.Params.Arguments = map[string]any{"commands": []any{program, "printf sibling"}, "wait_ms": 0, "concurrency": 2}
				result, err = srv.handleBatchExecute(t.Context(), req)
			}
			require.NoError(t, err)
			require.False(t, result.IsError, resultText(result))
			require.Eventually(t, func() bool {
				_, err := os.Stat(filepath.Join(srv.projectDir, "ready"))
				return err == nil
			}, 5*time.Second, 10*time.Millisecond)
			require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "release"), nil, 0600))
			want, count := int64(9), int64(1)
			if tool == "batch" {
				want, count = 16, 2
			}
			require.Eventually(t, func() bool { return srv.stats.Snapshot().RunsCompleted == count }, 10*time.Second, 20*time.Millisecond)
			snap := srv.stats.Snapshot()
			require.Equal(t, want, snap.BytesSandboxed)
			require.Equal(t, want, snap.RunObservedBytes)
			require.Equal(t, want, snap.RunCapturedBytes)
			page, err := srv.executionService().List(t.Context(), runstore.ListOptions{Limit: 16})
			require.NoError(t, err)
			for _, run := range page.Entries {
				_, err := srv.executionService().Status(t.Context(), run.RunID)
				require.NoError(t, err)
				_, err = srv.executionService().ReadRaw(t.Context(), run.RunID, "stdout", "", 4096)
				require.NoError(t, err)
			}
			_, err = srv.handleStats(t.Context(), mcp.CallToolRequest{})
			require.NoError(t, err)
			require.Equal(t, snap.BytesSandboxed, srv.stats.Snapshot().BytesSandboxed, "recovery and stats must not recount output")
		})
	}
}

func TestCapturedPreviewLimitPreservesOutput(t *testing.T) {
	isolateRunDiagnostics(t)
	for _, intent := range []string{"", "marker"} {
		t.Run("intent="+intent, func(t *testing.T) {
			srv := newTestServer(t, nil)
			srv.config.Executor.MaxOutputBytes = 12
			result := callTool(t, srv, map[string]any{"language": "shell", "code": "printf 'ééé marker and retained tail'", "intent": intent})
			require.False(t, result.IsError, resultText(result))
			run, ok := result.StructuredContent.(runstore.Result)
			require.True(t, ok)
			require.LessOrEqual(t, len(run.Preview), 12)
			require.True(t, utf8.ValidString(run.Preview))
			require.True(t, run.MetadataOmitted)
			require.Equal(t, 0, *run.ExitCode)
			raw, err := srv.executionService().ReadRaw(t.Context(), *run.RunID, "stdout", "", 4096)
			require.NoError(t, err)
			var output []byte
			for _, fragment := range raw.Fragments {
				output = append(output, fragment.Data...)
			}
			require.Equal(t, "ééé marker and retained tail", string(output))
			require.EqualValues(t, len(output), run.Capture.CapturedBytes)
		})
	}
}
