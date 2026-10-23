package server

import (
	"bytes"
	"context"
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

func capturedResult(t *testing.T, r *mcp.CallToolResult) runstore.Result {
	t.Helper()
	result, ok := r.StructuredContent.(runstore.Result)
	require.True(t, ok, "missing run envelope: %s", resultText(r))
	require.Equal(t, 2, result.Version)
	if result.RunID != nil {
		require.NoError(t, runstore.ValidateCleanup(result.Cleanup))
	}
	return result
}

func callRuns(t *testing.T, s *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	r, err := s.handleRuns(context.Background(), req)
	require.NoError(t, err)
	return r
}

func capturedOutput(t *testing.T, s *Server, r *mcp.CallToolResult, stream string) string {
	t.Helper()
	run := capturedResult(t, r)
	require.NotNil(t, run.RunID)
	var b strings.Builder
	cursor := ""
	for {
		args := map[string]any{"action": "read", "run_id": *run.RunID, "raw": true, "stream": stream, "max_bytes": 4096}
		if cursor != "" {
			args["cursor"] = cursor
		}
		r := callRuns(t, s, args)
		require.False(t, r.IsError, resultText(r))
		page, ok := r.StructuredContent.(runstore.ReadPage)
		require.True(t, ok)
		for _, fragment := range page.Fragments {
			b.Write(fragment.Data)
		}
		if page.EOF {
			break
		}
		require.NotNil(t, page.NextCursor)
		cursor = *page.NextCursor
	}
	return b.String()
}

func TestCapturedRunRecoveryAndSafeDefault(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callTool(t, srv, map[string]any{"language": "shell", "code": "printf stdout-marker; printf stderr-marker >&2; printf x >> executed-once; exit 7"})
	require.True(t, r.IsError)
	run := capturedResult(t, r)
	require.Equal(t, "exited", run.State)
	require.Equal(t, 7, *run.ExitCode)
	require.Equal(t, "complete", run.Capture.State)
	require.Equal(t, "ready", run.Redaction.State)
	require.NotContains(t, resultText(r), "stdout-marker")
	require.NotContains(t, resultText(r), "stderr-marker")
	status := callRuns(t, srv, map[string]any{"action": "status", "run_id": *run.RunID})
	require.False(t, status.IsError, resultText(status))
	for _, args := range []map[string]any{
		{"action": "read", "run_id": *run.RunID, "encoding": "base64"},
		{"action": "read", "run_id": *run.RunID, "raw": true, "encoding": "text"},
		{"action": "read", "run_id": *run.RunID, "raw": true, "max_bytes": 1.5},
		{"action": "status", "run_id": *run.RunID, "raw": true},
	} {
		result := callRuns(t, srv, args)
		require.True(t, result.IsError)
		require.NotContains(t, resultText(result), "stdout-marker")
	}
	require.Equal(t, "stdout-marker", capturedOutput(t, srv, r, "stdout"))
	require.Equal(t, "stderr-marker", capturedOutput(t, srv, r, "stderr"))
	b, err := os.ReadFile(filepath.Join(srv.projectDir, "executed-once"))
	require.NoError(t, err)
	require.Equal(t, "x", string(b))
	require.Nil(t, srv.store, "ordinary execution must not open knowledge storage")
	require.NoFileExists(t, srv.config.Store.Path)
	require.NoFileExists(t, os.Getenv("CAPY_VAULT_PATH"))
}

func TestCapturedAdmissionRejectsBeforeSpawn(t *testing.T) {
	for _, input := range []map[string]any{
		{"language": "shell", "code": "touch must-not-spawn", "timeout": 0},
		{"language": "shell", "code": "touch must-not-spawn", "timeout": 1.2},
		{"language": "shell", "code": "touch must-not-spawn", "timeout": "10"},
		{"language": "shell", "code": "touch must-not-spawn", "timeout": 1e30},
		{"language": "shell", "code": "touch must-not-spawn", "background": "not-a-boolean"},
	} {
		srv := newTestServer(t, nil)
		r := callTool(t, srv, input)
		require.True(t, r.IsError)
		run := capturedResult(t, r)
		require.Nil(t, run.RunID)
		require.Equal(t, "not_started", run.State)
		require.NoFileExists(t, filepath.Join(srv.projectDir, "must-not-spawn"))
		require.NoDirExists(t, filepath.Join(srv.projectDir, ".capy", "runs"))
	}
}

func TestCapturedCredentialSnapshotAndEmptyOverride(t *testing.T) {
	for _, mode := range []string{"empty", "long binary"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.Store.Path = filepath.Join(root, "knowledge.db")
			key := ""
			if mode == "long binary" {
				key = strings.Repeat("synthetic-key-", 6000) + string([]byte{255})
			}
			srv := NewServer(cfg, nil, executor.NewExecutor(root, 0), root, WithKnowledgeCredentials(key, config.KeySource{}))
			t.Cleanup(srv.shutdown)
			t.Setenv("CAPY_DB_KEY", "changed-inherited-key")
			r := callTool(t, srv, map[string]any{"language": "shell", "code": "printf retained; touch executed"})
			if mode == "empty" {
				require.True(t, r.IsError)
				require.Nil(t, capturedResult(t, r).RunID)
				require.NoDirExists(t, filepath.Join(root, ".capy"))
				require.NoFileExists(t, filepath.Join(root, "executed"))
				return
			}
			require.False(t, r.IsError, resultText(r))
			require.Equal(t, "retained", capturedOutput(t, srv, r, "stdout"))
			srv.shutdown()
			path, err := config.ResolveRunDBPath(root)
			require.NoError(t, err)
			st, err := runstore.Open(t.Context(), path, key, false)
			require.NoError(t, err)
			require.NoError(t, st.Close())
		})
	}
}

func assertRunBudget(t *testing.T, r *mcp.CallToolResult, limit int) {
	t.Helper()
	b, err := json.Marshal(r)
	require.NoError(t, err)
	require.LessOrEqual(t, len(b), limit, string(b))
	assertCleanupEnvelope(t, r.StructuredContent)
}

// Inspect the actual serialized response, including its smallest-budget form.
func assertCleanupEnvelope(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &object))
	require.JSONEq(t, "2", string(object["version"]))
	proof := func(subject map[string]json.RawMessage) {
		t.Helper()
		require.Contains(t, subject, "cleanup")
		var evidence executor.CleanupEvidence
		require.NoError(t, json.Unmarshal(subject["cleanup"], &evidence))
		require.NoError(t, runstore.ValidateCleanup(evidence))
	}
	for _, field := range []string{"run", "batch"} {
		if child, exists := object[field]; exists {
			require.NotContains(t, object, "cleanup")
			assertCleanupEnvelope(t, json.RawMessage(child))
			return
		}
	}
	if entries, exists := object["entries"]; exists {
		require.NotContains(t, object, "cleanup")
		var list []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(entries, &list))
		for _, entry := range list {
			proof(entry)
		}
		return
	}
	if string(object["kind"]) == `"rejection"` || (object["run_id"] == nil && object["batch_id"] == nil && object["error"] != nil) {
		require.Contains(t, object, "cleanup")
		require.JSONEq(t, "null", string(object["cleanup"]))
		return
	}
	proof(object)
	if children, exists := object["children"]; exists {
		var list []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(children, &list))
		for _, child := range list {
			proof(child)
		}
	}
}

func TestCapturedResponseBudgetsAndLargeRawRecovery(t *testing.T) {
	srv := newTestServer(t, nil)
	srv.config.Runs.MaxResponseBytes = 2048
	// Over the old 100 KiB boundary: binary, split multibyte sequences and one
	// giant line. Pace blocks so this fixture tests complete recovery, not overload.
	code := `import os,time
for i in range(24):
 os.write(1,bytes(range(256))*32)
 time.sleep(.01)
os.write(1,b'MIDDLE_MARKER')
os.write(2,'stderr-only-\u20ac'.encode())
for i in range(24):
 os.write(1,bytes(range(256))*32)
 time.sleep(.01)
`
	r := callTool(t, srv, map[string]any{"language": "python", "code": code})
	assertRunBudget(t, r, 2048)
	run := capturedResult(t, r)
	require.False(t, r.IsError, "capture=%+v error=%+v", run.Capture, run.Error)
	block := make([]byte, 256)
	for i := range block {
		block[i] = byte(i)
	}
	want := bytes.Repeat(block, 32*24)
	want = append(want, []byte("MIDDLE_MARKER")...)
	want = append(want, bytes.Repeat(block, 32*24)...)
	var got []byte
	cursor := ""
	for pages := 0; pages < 4000; pages++ {
		pageResult := callRuns(t, srv, map[string]any{"action": "read", "run_id": *run.RunID, "raw": true, "stream": "stdout", "cursor": cursor, "max_bytes": 65536})
		assertRunBudget(t, pageResult, 2048)
		require.False(t, pageResult.IsError, resultText(pageResult))
		page := pageResult.StructuredContent.(runstore.ReadPage)
		for _, f := range page.Fragments {
			got = append(got, f.Data...)
		}
		if page.EOF {
			break
		}
		require.NotNil(t, page.NextCursor)
		require.NotEqual(t, cursor, *page.NextCursor)
		cursor = *page.NextCursor
	}
	require.Equal(t, want, got)
	require.Equal(t, "stderr-only-€", capturedOutput(t, srv, r, "stderr"))
	for _, tc := range []struct {
		name, code, state string
		timeout           int
		exit              int
	}{
		{"exit one", "printf stdout; printf stderr >&2; exit 1", "exited", 1000, 1},
		{"deadline", "sleep 10", "timed_out", 1500, -1},
		{"signal", "kill -TERM $$", "exited", 1000, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := callTool(t, srv, map[string]any{"language": "shell", "code": tc.code, "timeout": tc.timeout})
			assertRunBudget(t, r, 2048)
			require.True(t, r.IsError)
			run := capturedResult(t, r)
			require.Equal(t, tc.state, run.State)
			if tc.exit >= 0 {
				require.Equal(t, tc.exit, *run.ExitCode)
			} else {
				require.NotNil(t, run.Signal)
			}
			status := callRuns(t, srv, map[string]any{"action": "status", "run_id": *run.RunID})
			assertRunBudget(t, status, 2048)
			require.False(t, status.IsError)
		})
	}
	rejection := srv.runResult(runstore.Rejection("invalid_argument", strings.Repeat("\"\n€", 10000)))
	assertRunBudget(t, rejection, 2048)
	require.Nil(t, capturedResult(t, rejection).RunID)
}

func TestCapturedSpawnAndCompilerFailuresAreBounded(t *testing.T) {
	for _, mode := range []string{"missing runtime", "compiler failure"} {
		t.Run(mode, func(t *testing.T) {
			srv := newTestServer(t, nil)
			srv.config.Runs.MaxResponseBytes = 2048
			bin := t.TempDir()
			if mode == "compiler failure" {
				require.NoError(t, os.WriteFile(filepath.Join(bin, "rustc"), []byte("#!/bin/sh\nprintf compiler-failed >&2\nexit 23\n"), 0700))
			}
			t.Setenv("PATH", bin)
			r := callTool(t, srv, map[string]any{"language": "rust", "code": "synthetic compiler input"})
			assertRunBudget(t, r, 2048)
			require.True(t, r.IsError)
			run := capturedResult(t, r)
			require.NotNil(t, run.RunID)
			if mode == "missing runtime" {
				require.Equal(t, "start_failed", run.State)
				require.Nil(t, run.ExitCode)
				require.Equal(t, "spawn_failed", *run.Reason)
				require.Equal(t, "not_started", run.Capture.State)
			} else {
				require.Equal(t, "exited", run.State)
				require.Equal(t, 23, *run.ExitCode)
				require.Equal(t, "compiler-failed", capturedOutput(t, srv, r, "stderr"))
			}
		})
	}
}

func TestCapturedDefaultProjectionSuppressesSecrets(t *testing.T) {
	srv := newTestServer(t, nil)
	srv.config.Runs.MaxResponseBytes = 2048
	secret := "sk-" + strings.Repeat("A", 30)
	code := "printf 'safe!\n" + secret + "\n<private>hidden\nprivate tail'"
	r := callTool(t, srv, map[string]any{"language": "shell", "code": code})
	assertRunBudget(t, r, 2048)
	run := capturedResult(t, r)
	require.Contains(t, run.Preview, "safe!")
	encoded, err := json.Marshal(r)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), secret)
	require.NotContains(t, string(encoded), "private tail")
	result := callRuns(t, srv, map[string]any{"action": "read", "run_id": *run.RunID, "stream": "stdout", "start_line": 4, "max_bytes": 65536})
	require.False(t, result.IsError, resultText(result))
	assertRunBudget(t, result, 2048)
	page := result.StructuredContent.(runstore.ReadPage)
	for _, f := range page.Fragments {
		require.NotContains(t, f.Text, "private tail")
		require.Empty(t, f.Data)
	}
	require.Contains(t, capturedOutput(t, srv, r, "stdout"), secret)
}
