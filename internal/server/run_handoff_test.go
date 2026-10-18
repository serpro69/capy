package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func newHandoffServer(t *testing.T, project string) *Server {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("captured supervision requires Linux")
	}
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, root)
	}
	return newTestServerWithProjectDir(t, nil, project)
}

func waitRunTerminal(t *testing.T, s *Server, id string) runstore.Result {
	t.Helper()
	var result runstore.Result
	require.Eventually(t, func() bool {
		var err error
		result, err = s.executionService().Status(t.Context(), id)
		return err == nil && result.CompletedAt != nil
	}, 10*time.Second, 10*time.Millisecond)
	return result
}

func TestRunHandoffPreparationAndCancellation(t *testing.T) {
	for _, mode := range []string{"cancel before handoff", "serialization failure", "natural completion"} {
		t.Run(mode, func(t *testing.T) {
			s := newHandoffServer(t, "")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			prepared := false
			result, err := s.executionService().ExecuteResponse(ctx, executor.ExecRequest{Language: executor.Shell,
				Code: "printf early; touch started; while [ ! -e release ]; do sleep .01; done; printf late; touch completed"}, "", 10*time.Second, 0,
				func(result runstore.Result) error {
					if prepared {
						return nil
					}
					prepared = true
					require.NotNil(t, result.RunID)
					require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.projectDir, "started")); return err == nil }, 5*time.Second, time.Millisecond)
					switch mode {
					case "cancel before handoff":
						cancel()
					case "serialization failure":
						_, err := json.Marshal(make(chan int))
						return err
					case "natural completion":
						require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "release"), nil, 0600))
						waitRunTerminal(t, s, *result.RunID)
					}
					return nil
				})
			if mode == "serialization failure" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result.RunID)
			terminal := waitRunTerminal(t, s, *result.RunID)
			require.Equal(t, "complete", terminal.Capture.State)
			if mode == "natural completion" {
				require.Equal(t, "exited", terminal.State)
				require.Equal(t, 0, *terminal.ExitCode)
			} else {
				require.Equal(t, "cancelled", terminal.State)
				require.NoFileExists(t, filepath.Join(s.projectDir, "completed"))
			}
		})
	}
}

func TestRunHandoffCapacityAndOwnCancellation(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxActive = 1
	s.config.Runs.MaxResponseBytes = 2048
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run, err := s.executionService().ExecuteResponse(ctx, executor.ExecRequest{Language: executor.Shell,
		Code: "printf early; touch started; while [ ! -e release ]; do sleep .01; done; printf late"}, "", 10*time.Second, 0, func(result runstore.Result) error {
		_, err := s.buildRunResult(result)
		return err
	})
	require.NoError(t, err)
	require.NotNil(t, run.RunID)
	cancel() // ordinary handler cleanup after the committed handoff
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(s.projectDir, "started")); return err == nil }, 5*time.Second, time.Millisecond)
	blocked := callTool(t, s, map[string]any{"language": "shell", "code": "touch must-not-run", "wait_ms": 0})
	assertRunBudget(t, blocked, 2048)
	rejection := capturedResult(t, blocked)
	require.Equal(t, "capacity_exhausted", rejection.Error.Code)
	require.NotNil(t, rejection.Capacity)
	require.Equal(t, 1, rejection.Capacity.ActiveCount)
	require.Contains(t, rejection.Capacity.CancelAction, "cancel")
	require.NoFileExists(t, filepath.Join(s.projectDir, "must-not-run"))
	for _, args := range []map[string]any{
		{"action": "status", "run_id": *run.RunID},
		{"action": "list"},
		{"action": "read", "run_id": *run.RunID},
	} {
		r := callRuns(t, s, args)
		require.False(t, r.IsError)
		assertRunBudget(t, r, 2048)
	}
	foreign := newHandoffServer(t, s.projectDir)
	r := callRuns(t, foreign, map[string]any{"action": "cancel", "run_id": *run.RunID})
	require.True(t, r.IsError)
	require.Contains(t, resultText(r), "Run operation failed")
	b, err := json.Marshal(r)
	require.NoError(t, err)
	require.Contains(t, string(b), "different_owner")
	r = callRuns(t, s, map[string]any{"action": "cancel", "run_id": *run.RunID})
	require.False(t, r.IsError)
	require.Equal(t, true, r.StructuredContent.(map[string]any)["cancel_requested"])
	terminal := waitRunTerminal(t, s, *run.RunID)
	require.Equal(t, "cancelled", terminal.State)
	r = callRuns(t, s, map[string]any{"action": "cancel", "run_id": *run.RunID})
	require.False(t, r.IsError)
	require.Equal(t, false, r.StructuredContent.(map[string]any)["cancel_requested"])
	require.NoError(t, s.executionService().Close())
}

func TestRunWaitAndBackgroundValidation(t *testing.T) {
	for _, extra := range []map[string]any{
		{"wait_ms": -1}, {"wait_ms": 30001}, {"wait_ms": .5}, {"wait_ms": "0"},
		{"background": true, "wait_ms": 0}, {"background": true, "timeout": 0},
	} {
		s := newHandoffServer(t, "")
		extra["language"], extra["code"] = "shell", "touch must-not-run"
		r := callTool(t, s, extra)
		require.True(t, r.IsError)
		require.Nil(t, capturedResult(t, r).RunID)
		require.NoFileExists(t, filepath.Join(s.projectDir, "must-not-run"))
	}
}
