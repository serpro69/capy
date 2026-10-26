package execution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedCaptureRejectsBeforeStorageAndSpawn(t *testing.T) {
	if Supported() == nil {
		t.Skip("requires a host without a supported captured backend")
	}
	root := t.TempDir()
	s := NewService(root, "synthetic-unsupported-host-key", config.DefaultConfig().Runs)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	s.launch = func(context.Context, string, Startup, string, *runstore.LeaseRefs) (*Supervisor, error) {
		t.Error("unsupported host attempted to spawn a helper")
		return nil, Supported()
	}
	result := s.Execute(t.Context(), executor.ExecRequest{Language: executor.Shell, Code: "printf x > forbidden"}, "", time.Second)
	require.Nil(t, result.RunID)
	require.NotNil(t, result.Error)
	require.Equal(t, "storage_unavailable", result.Error.Code)
	_, err := s.ExecuteBatchResponse(t.Context(), runstore.BatchInput{
		Commands:    []runstore.BatchCommand{{Label: "forbidden", Command: "printf x > forbidden"}},
		Queries:     []string{"forbidden"},
		Concurrency: 1,
	}, time.Second, time.Second, func(runstore.BatchResult) error { return nil })
	var failure *runstore.Error
	require.ErrorAs(t, err, &failure)
	require.Equal(t, "storage_unavailable", failure.Code)
	require.NoFileExists(t, filepath.Join(root, "forbidden"))
	require.NoDirExists(t, filepath.Join(root, ".capy"))
}
