//go:build darwin && cgo && integration

package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func darwinBatch(t *testing.T, s *Service, concurrency int, commands ...string) runstore.BatchResult {
	t.Helper()
	input := runstore.BatchInput{Concurrency: concurrency}
	for _, command := range commands {
		input.Commands = append(input.Commands, runstore.BatchCommand{Command: command})
	}
	result, err := s.ExecuteBatchResponse(t.Context(), input, 30*time.Second, 0, func(runstore.BatchResult) error { return nil })
	require.NoError(t, err)
	require.Nil(t, result.Error)
	require.NotEmpty(t, result.BatchID)
	return result
}

func awaitDarwinBatch(t *testing.T, s *Service, id string) runstore.BatchResult {
	t.Helper()
	var result runstore.BatchResult
	require.Eventually(t, func() bool {
		var err error
		result, err = s.BatchStatus(t.Context(), id)
		require.NoError(t, err)
		active, pending := s.WorkCounts()
		return result.CompletedAt != nil && active == 0 && pending == 0
	}, 10*time.Second, 10*time.Millisecond)
	return result
}

func TestDarwinBatchStalledStartupPreservesSibling(t *testing.T) {
	s := newDarwinCaptureService(t, "batch phases")
	s.policy.MaxActive, s.policy.MaxPending = 3, 2
	launch := s.launch
	s.launch = func(ctx context.Context, binary string, start Startup, key string, refs *runstore.LeaseRefs) (*Supervisor, error) {
		row, err := s.store.Load(ctx, start.RunID)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(row.Program.Request.Code, "# queued exchange\n") {
			// Order the fixture deterministically: this helper must contend for
			// the gate already held by the paused startup, not win it first.
			if err := awaitLeaseFile(ctx, filepath.Join(s.project, "opening")); err != nil {
				return nil, err
			}
		}
		return launch(ctx, binary, start, key, refs)
	}
	standalone, err := s.ExecuteResponse(t.Context(), executor.ExecRequest{Language: executor.Shell,
		Code: "printf x >> once-running; touch running; while [ ! -e release-running ]; do sleep .01; done; printf running-tail"}, "", 30*time.Second, 0, func(runstore.Result) error { return nil })
	require.NoError(t, err)
	require.NotNil(t, standalone.RunID)
	require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(s.project, "running")))
	b := darwinBatch(t, s, 2, "# hold startup\ntouch forbidden", "# queued exchange\nprintf x >> once-sibling; printf sibling-tail")
	require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(s.project, "opening")))
	active, pending := s.WorkCounts()
	require.Equal(t, 3, active)
	require.Zero(t, pending)
	require.NoFileExists(t, filepath.Join(s.project, "once-sibling"))
	blocked := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch bypass"}, "", time.Second)
	require.Equal(t, "capacity_exhausted", blocked.Error.Code)
	children, err := s.List(t.Context(), runstore.ListOptions{BatchID: b.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, children.Entries, 2)
	_, cancelled, err := s.Cancel(t.Context(), children.Entries[0].RunID)
	require.NoError(t, err)
	require.True(t, cancelled)
	// The already-running sibling keeps capturing while startup cancellation
	// reaps only the unauthorized helper and releases its gate to the next one.
	require.NoError(t, os.WriteFile(filepath.Join(s.project, "release-running"), nil, 0600))
	final := awaitDarwinBatch(t, s, b.BatchID)
	require.Equal(t, executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupObserved}, final.Cleanup)
	children, err = s.List(t.Context(), runstore.ListOptions{BatchID: b.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, children.Entries, 2)
	require.Equal(t, executor.CleanupNotSpawned, children.Entries[0].Cleanup.State)
	require.Nil(t, children.Entries[0].ExitCode)
	require.Equal(t, executor.CleanupObserved, children.Entries[1].Cleanup.State)
	for _, child := range children.Entries {
		require.Equal(t, standalone.OwnerID, child.OwnerID)
		require.Equal(t, executor.CleanupDarwinObservation, child.Cleanup.Scope)
	}
	require.Equal(t, "running-tail", darwinRawText(t, s, *standalone.RunID, "stdout"))
	require.Equal(t, "sibling-tail", darwinRawText(t, s, children.Entries[1].RunID, "stdout"))
	for _, name := range []string{"once-running", "once-sibling"} {
		count, err := os.ReadFile(filepath.Join(s.project, name))
		require.NoError(t, err)
		require.Equal(t, "x", string(count))
	}
	require.NoFileExists(t, filepath.Join(s.project, "forbidden"))
	require.NoFileExists(t, filepath.Join(s.project, "bypass"))
	require.False(t, s.unverified)
}

func TestDarwinBatchShutdownJoinsFinalizer(t *testing.T) {
	s := newDarwinCaptureService(t, "batch phases")
	s.policy.MaxActive = 1
	// Register after service cleanup so failure paths release the fixture first.
	t.Cleanup(func() { require.NoError(t, os.WriteFile(filepath.Join(s.project, "release-close"), nil, 0600)) })
	b := darwinBatch(t, s, 1, "# hold close\nprintf first; printf x >> once", "touch forbidden")
	require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(s.project, "closing")))
	active, pending := s.WorkCounts()
	require.Equal(t, 1, active)
	require.Equal(t, 1, pending)
	status, err := s.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Nil(t, status.CompletedAt)
	require.Nil(t, status.ExpiresAt)
	require.Equal(t, executor.CleanupPending, status.Cleanup.State)
	children, err := s.List(t.Context(), runstore.ListOptions{BatchID: b.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, children.Entries, 2)
	require.Equal(t, executor.CleanupObserved, children.Entries[0].Cleanup.State)
	require.Equal(t, executor.CleanupNotSpawned, children.Entries[1].Cleanup.State)
	path := s.store.Path()
	require.ErrorContains(t, runstore.Reset(t.Context(), path), "reset refused")
	blocked := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "touch bypass"}, "", time.Second)
	require.Equal(t, "capacity_exhausted", blocked.Error.Code)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	// Close has cancelled all children, but cannot return while a helper owns
	// its backend and inherited leases, even with terminal proof persisted.
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closed
	}, time.Second, time.Millisecond)
	select {
	case err := <-closed:
		t.Fatalf("close returned before backend release: %v", err)
	default:
	}
	require.ErrorContains(t, runstore.Reset(t.Context(), path), "reset refused")
	require.NoError(t, os.WriteFile(filepath.Join(s.project, "release-close"), nil, 0600))
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown failed to join batch finalizer")
	}
	active, pending = s.WorkCounts()
	require.Zero(t, active)
	require.Zero(t, pending)
	st, err := runstore.Open(t.Context(), path, darwinCaptureKey, false)
	require.NoError(t, err)
	defer st.Close()
	final, err := st.BatchStatus(t.Context(), b.BatchID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", final.State)
	require.NotNil(t, final.ExpiresAt)
	require.Equal(t, executor.CleanupObserved, final.Cleanup.State)
	list, err := st.List(t.Context(), runstore.ListOptions{BatchID: b.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, list.Entries, 2)
	require.Equal(t, executor.CleanupObserved, list.Entries[0].Cleanup.State)
	require.Equal(t, executor.CleanupNotSpawned, list.Entries[1].Cleanup.State)
	require.NoFileExists(t, filepath.Join(s.project, "forbidden"))
	require.NoFileExists(t, filepath.Join(s.project, "bypass"))
	count, err := os.ReadFile(filepath.Join(s.project, "once"))
	require.NoError(t, err)
	require.Equal(t, "x", string(count))
}
