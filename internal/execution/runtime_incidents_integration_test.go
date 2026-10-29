//go:build (linux || darwin) && integration

package execution

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func newRuntimeIncidentService(t *testing.T, mode string) *Service {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS == "darwin" {
		t.Setenv("CAPY_DARWIN_CAPTURE_ROOT", root)
		t.Setenv("CAPY_DARWIN_CAPTURE_MODE", mode)
	} else {
		t.Setenv("CAPY_STORED_FIXTURE", root)
		if mode == "finish failure" {
			mode = "failed finish"
		}
		t.Setenv("CAPY_STORED_MODE", mode)
	}
	s := NewService(root, "synthetic-runtime-incident-key", config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func runtimeRawText(t *testing.T, s *Service, id, stream string) string {
	t.Helper()
	page, err := s.ReadRaw(t.Context(), id, stream, "", 65536)
	require.NoError(t, err)
	require.True(t, page.EndOfAvailable)
	var text strings.Builder
	for _, fragment := range page.Fragments {
		text.Write(fragment.Data)
	}
	return text.String()
}

func TestYieldedRuntimeIncidentCompletionOnce(t *testing.T) {
	for _, mode := range []string{"finish failure", "helper loss"} {
		t.Run(mode, func(t *testing.T) {
			s := newRuntimeIncidentService(t, mode)
			observed := make(chan runstore.Result, 2)
			s.onCompletion = func(r runstore.Result) { observed <- r }
			t.Cleanup(func() { require.NoError(t, os.WriteFile(filepath.Join(s.project, "release-run"), nil, 0600)) })
			r, err := s.ExecuteResponse(t.Context(), executor.ExecRequest{Language: executor.Shell,
				Code: "while [ ! -e release-run ]; do sleep .01; done; printf prefix; exit 7"}, "", 30*time.Second, 0, func(runstore.Result) error { return nil })
			require.NoError(t, err)
			require.NotNil(t, r.RunID)
			require.False(t, r.Capture.TerminalPersisted)
			require.NoError(t, os.WriteFile(filepath.Join(s.project, "release-run"), nil, 0600))
			var completion runstore.Result
			select {
			case completion = <-observed:
			case <-time.After(10 * time.Second):
				t.Fatal("yielded completion did not arrive")
			}
			require.Eventually(t, func() bool { active, _ := s.WorkCounts(); return active == 0 }, 5*time.Second, time.Millisecond)
			require.Equal(t, executor.CleanupPending, completion.Cleanup.State)
			require.False(t, completion.Capture.TerminalPersisted)
			require.EqualValues(t, 6, completion.Capture.CapturedBytes)
			cause, stage := "finalization_unpersisted", "finalization"
			if mode == "helper loss" {
				cause, stage = "supervisor_lost", "helper_wait"
				require.Nil(t, completion.ExitCode, "lost helper cannot publish its private exit status")
			} else {
				require.Equal(t, 7, *completion.ExitCode)
				require.EqualValues(t, 6, completion.Capture.ObservedBytes)
			}
			assertRuntimeIncident(t, s, *r.RunID, "", cause, stage, "run")
			require.Equal(t, "prefix", runtimeRawText(t, s, *r.RunID, "stdout"))
			_, err = s.Status(t.Context(), *r.RunID)
			require.NoError(t, err)
			require.Empty(t, observed, "reads, status and incident publication must not count completion again")
			next := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch forbidden"}, "", time.Second)
			require.Nil(t, next.RunID)
			require.Equal(t, *r.RunID, next.Error.RecoveryHint.Subject.RunID)
			require.Equal(t, "recorded", next.Error.RecoveryHint.Subject.Attribution)
			require.Equal(t, "owner", next.Error.RecoveryHint.Scope)
			require.NoFileExists(t, filepath.Join(s.project, "forbidden"))
		})
	}
}

func TestBatchRuntimeIncidentCompletionOnce(t *testing.T) {
	s := newRuntimeIncidentService(t, "finish failure")
	s.policy.MaxActive = 1
	observed := make(chan runstore.Result, 3)
	s.onCompletion = func(r runstore.Result) { observed <- r }
	b, err := s.ExecuteBatchResponse(t.Context(), runstore.BatchInput{Concurrency: 1, Commands: []runstore.BatchCommand{{Command: "printf prefix; printf x >> once; exit 7"}, {Command: "touch forbidden"}}}, 30*time.Second, 0, func(runstore.BatchResult) error { return nil })
	require.NoError(t, err)
	require.Nil(t, b.Error)
	require.NotEmpty(t, b.BatchID)
	require.Eventually(t, func() bool {
		active, pending := s.WorkCounts()
		return active == 0 && pending == 0
	}, 10*time.Second, 10*time.Millisecond)
	status, err := s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, executor.CleanupPending, status.Cleanup.State)
	require.Nil(t, status.ExpiresAt, "unverified child proof cannot expire as completed history")
	children, err := s.List(t.Context(), runstore.ListOptions{BatchID: b.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, children.Entries, 2)
	require.Equal(t, executor.CleanupPending, children.Entries[0].Cleanup.State)
	require.Equal(t, executor.CleanupNotSpawned, children.Entries[1].Cleanup.State)
	assertRuntimeIncident(t, s, children.Entries[0].RunID, b.BatchID, "finalization_unpersisted", "finalization", "run")
	require.Equal(t, "prefix", runtimeRawText(t, s, children.Entries[0].RunID, "stdout"))
	rejected := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch bypass"}, "", time.Second)
	require.Equal(t, "cleanup_unverified", rejected.Error.Code)
	blocked, err := s.ExecuteBatchResponse(t.Context(), runstore.BatchInput{Concurrency: 1, Commands: []runstore.BatchCommand{{Command: "touch bypass"}}}, time.Second, 0, func(runstore.BatchResult) error { return nil })
	require.NoError(t, err)
	require.Equal(t, "cleanup_unverified", blocked.Error.Code)
	require.Equal(t, children.Entries[0].RunID, blocked.Error.RecoveryHint.Subject.RunID)
	require.Equal(t, "recorded", blocked.Error.RecoveryHint.Subject.Attribution)
	require.Equal(t, "owner", blocked.Error.RecoveryHint.Scope)
	require.NoFileExists(t, filepath.Join(s.project, "forbidden"))
	require.NoFileExists(t, filepath.Join(s.project, "bypass"))
	count, err := os.ReadFile(filepath.Join(s.project, "once"))
	require.NoError(t, err)
	require.Equal(t, "x", string(count))
	require.Len(t, observed, 2, "each child is observed once, including the skipped sibling")
	seen := make(map[string]bool)
	var captured, bytes int64
	for range 2 {
		r := <-observed
		require.False(t, seen[*r.RunID])
		seen[*r.RunID] = true
		captured += r.Capture.CapturedBytes
		bytes += r.Capture.ObservedBytes
	}
	require.EqualValues(t, 6, captured)
	require.EqualValues(t, 6, bytes)
}
