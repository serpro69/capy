//go:build darwin && cgo && integration

package executor

import (
	"context"
	"fmt"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDarwinConcurrentCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ready := make(chan struct{}, 8)
	done := make(chan error, 8)
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	for range 8 {
		executor := NewExecutor(t.TempDir(), 0)
		workers.Add(1)
		go func() {
			defer workers.Done()
			var once sync.Once
			result, err := executor.ExecuteStream(ctx, ExecRequest{Language: Shell, Code: "printf ready; sleep 60"}, func(event OutputEvent) error {
				once.Do(func() { ready <- struct{}{} })
				return nil
			})
			if err == nil && result.Cleanup.State != CleanupObserved {
				err = fmt.Errorf("incomplete cancellation: %+v", result)
			}
			done <- err
		}()
	}
	for range 8 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("command did not become ready")
		}
	}
	cancel()
	for range 8 {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("concurrent cancellation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation did not finish")
		}
	}
}

func TestDarwinCleanupDoesNotResignalExitingMembers(t *testing.T) {
	for _, mode := range []string{"cancelled", "natural exit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root := t.TempDir()
			phase := darwinPhase{bin: "/bin/sh", args: []string{"-c", "printf ready; exec sleep 60"}, cwd: root, tmpDir: root, name: "run"}
			if mode == "natural exit" {
				phase = observedChildPhase(t, "closed")
			}
			ops := nativeDarwinPhaseOps()
			kill, inventory := ops.killGroup, ops.inventory
			signals, afterSignal := 0, 0
			ops.killGroup = func(pid int) error {
				signals++
				if signals > 1 {
					return syscall.EPERM
				}
				return kill(pid)
			}
			ops.inventory = func(ctx context.Context, pid int) ([]darwinGroupMember, error) {
				members, err := inventory(ctx, pid)
				if err == nil && signals == 1 && afterSignal < 2 {
					afterSignal++
					// Model an inventory taken before a signalled member finished
					// exiting. Later reads must use the real kernel inventory.
					members = append(members, darwinGroupMember{pid: -1})
				}
				return members, err
			}
			result, err := runDarwinStreamPhase(ctx, phase, &eventWriter{sink: func(OutputEvent) error {
				if mode == "cancelled" {
					cancel()
				}
				return nil
			}}, ops)
			require.NoError(t, err)
			require.Equal(t, 1, signals)
			require.Equal(t, 2, afterSignal)
			require.Equal(t, CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}, result.Cleanup)
		})
	}
}
