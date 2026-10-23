package execution

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestHandoffAndRequestObserverHaveOneWinner(t *testing.T) {
	for range 1000 {
		var cancelled atomic.Bool
		call := &runCall{cancel: func() { cancelled.Store(true) }, stopRequest: func() bool { return false }}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var handedOff bool
		go func() { defer wg.Done(); <-start; call.cancelAttached() }()
		go func() { defer wg.Done(); <-start; handedOff = call.handoff(context.Background()) }()
		close(start)
		wg.Wait()
		require.NotEqual(t, handedOff, cancelled.Load(), "handoff and attached observer must have exactly one winner")
		if handedOff {
			call.cancelAttached() // even a previously scheduled callback is harmless
			require.False(t, cancelled.Load())
		}
	}
}

func TestAlreadyCancelledRequestCannotHandoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stopped bool
	call := &runCall{cancel: func() { stopped = true }, stopRequest: func() bool { return true }}
	require.False(t, call.handoff(ctx))
	require.True(t, stopped)
}

func TestCancellationBeforeSpawnKeepsNullChildStatus(t *testing.T) {
	result := runstore.Result{State: "admitted"}
	runstore.ApplyOutcome(&result, runstore.Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupNotSpawned}, Reason: "cancelled"})
	require.Equal(t, "cancelled", result.State)
	require.Nil(t, result.ExitCode)
	require.Nil(t, result.Signal)
	require.Nil(t, result.Error)
}
