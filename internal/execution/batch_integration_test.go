//go:build linux && integration

package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestBatchCancellationDuringHelperStartup(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(name, dir)
	}
	t.Setenv("CAPY_STORED_FIXTURE", dir)
	t.Setenv("CAPY_STORED_MODE", "hold startup")
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	defer s.Close()
	launch := s.launch
	var helper *Supervisor
	s.launch = func(ctx context.Context, binary string, start Startup, key string, refs *runstore.LeaseRefs) (*Supervisor, error) {
		h, err := launch(ctx, binary, start, key, refs)
		helper = h
		return h, err
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	var result runstore.BatchResult
	var executeErr error
	go func() {
		defer close(done)
		result, executeErr = s.ExecuteBatchResponse(ctx, runstore.BatchInput{Concurrency: 2, Commands: []runstore.BatchCommand{{Command: "touch forbidden"}, {Command: "touch sibling"}}}, 30*time.Second, 30*time.Second, func(runstore.BatchResult) error { return nil })
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "opening")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	cancel()
	// Protocol v2 force-reaps a helper before authorization, even while its
	// backend is paused. Completion must follow that confirmed reap.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("startup cancellation did not finish")
	}
	require.NoError(t, executeErr)
	require.NotNil(t, helper)
	require.NotNil(t, helper.cmd.ProcessState)
	require.True(t, helper.knownNotSpawned())
	require.Equal(t, "cancelled", result.State)
	require.NotNil(t, result.CompletedAt)
	page, err := s.List(t.Context(), runstore.ListOptions{BatchID: result.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	for _, child := range page.Entries {
		require.Equal(t, "cancelled", child.State)
		require.Nil(t, child.ExitCode)
		require.Equal(t, executor.CleanupNotSpawned, child.Cleanup.State)
	}
	require.NoFileExists(t, filepath.Join(dir, "forbidden"))
	require.NoFileExists(t, filepath.Join(dir, "sibling"))
	require.False(t, s.unverified)
}
