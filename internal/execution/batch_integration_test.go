//go:build linux && integration

package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
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
	// Keep the helper paused long enough to demonstrate cancellation cannot
	// release its active slot, owner lease, or certify terminal cleanup early.
	select {
	case <-done:
		t.Fatal("batch returned before helper cleanup")
	case <-time.After(25 * time.Millisecond):
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "release-open"), nil, 0600))
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("startup cancellation did not finish")
	}
	require.NoError(t, executeErr)
	require.Equal(t, "cancelled", result.State)
	require.NotNil(t, result.CompletedAt)
	page, err := s.List(t.Context(), runstore.ListOptions{BatchID: result.BatchID, Limit: 8})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
	for _, child := range page.Entries {
		require.Equal(t, "cancelled", child.State)
		require.Nil(t, child.ExitCode)
	}
	require.NoFileExists(t, filepath.Join(dir, "forbidden"))
	require.NoFileExists(t, filepath.Join(dir, "sibling"))
	require.False(t, s.unverified)
}
