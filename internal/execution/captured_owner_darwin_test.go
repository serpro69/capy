//go:build darwin && cgo && integration

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type darwinOwnerIdentity struct {
	Run, Path, Generation string
	Slot, Helper          int
}

// The owner is the helper's sole parent here. Preserve the stop notification
// and validate it without consuming a terminal event or becoming a reaper.
func stopDarwinCaptureHelper(ctx context.Context, process *os.Process) error {
	if err := process.Signal(syscall.SIGSTOP); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		stopped, err := darwinCaptureHelperStopped(process.Pid)
		if err != nil {
			return err
		}
		if stopped {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runDarwinCaptureOwner() (retErr error) {
	root := os.Getenv("CAPY_DARWIN_CAPTURE_ROOT")
	mode := os.Getenv("CAPY_DARWIN_OWNER_MODE")
	path, err := config.ResolveRunDBPath(root)
	if err != nil {
		return err
	}
	st, err := runstore.Open(context.Background(), path, darwinCaptureKey, true)
	if err != nil {
		return err
	}
	defer st.Close()
	owner, err := st.ClaimOwner(context.Background())
	if err != nil {
		return err
	}
	defer owner.Lease.Close()
	deadline := time.Now().Add(30 * time.Second)
	row, err := st.Admit(context.Background(), owner, executor.CleanupDarwinObservation, runstore.Program{
		Owner: root, Deadline: deadline, Request: executor.ExecRequest{Language: executor.Shell,
			Code: "printf x >> once; printf ready; exec sleep 30"},
	})
	if err != nil {
		return err
	}
	refs, err := st.BorrowLeases(owner)
	if err != nil {
		return err
	}
	defer refs.Close()
	launcher := newLeaseLauncher()
	launcher.authorize = func(ctx context.Context, p *exchangeProcess, x exchangeIO, scope string) error {
		// Validated ready has returned this exchange's token. Prove that a later
		// borrower can obtain it while this helper remains alive.
		gate, err := refs.AcquireExchange(ctx)
		if err != nil {
			return err
		}
		if err := gate.Release(); err != nil {
			return err
		}
		if mode == "buffered" {
			if err := stopDarwinCaptureHelper(ctx, p.cmd.Process); err != nil {
				return err
			}
		}
		return p.authorize(ctx, x, scope)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	helper, err := launcher.launchSupervisor(context.Background(), exe, Startup{
		RunID: row.ID, Generation: owner.ID, StorePath: path, Deadline: deadline,
		CleanupScope: darwinExchangeScope,
	}, darwinCaptureKey, refs)
	if helper != nil {
		defer func() {
			if retErr != nil {
				// Parent still owns this child on a failed fixture setup.
				_ = helper.cmd.Process.Signal(syscall.SIGCONT)
				_ = helper.Cancel()
				_, _ = helper.Wait()
			}
		}()
	}
	if err != nil {
		return err
	}
	if mode == "running" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			current, err := st.Load(ctx, row.ID)
			if err != nil {
				return err
			}
			if current.Result.Capture.CapturedBytes == 5 {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
		if err := stopDarwinCaptureHelper(ctx, helper.cmd.Process); err != nil {
			return err
		}
	}
	identity, err := json.Marshal(darwinOwnerIdentity{row.ID, path, owner.ID, owner.Slot, helper.cmd.Process.Pid})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "identity"), identity, 0600); err != nil {
		return err
	}
	// The test intentionally kills this owner after publication. Bound setup
	// failure cleanup if the outer test never reaches that step.
	<-time.After(20 * time.Second)
	return errors.New("fixture owner was not terminated")
}

func requireDarwinLease(t *testing.T, path string, held bool) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	require.NoError(t, err)
	defer f.Close()
	err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if held {
		require.ErrorIs(t, err, unix.EWOULDBLOCK)
	} else {
		require.NoError(t, err)
	}
}

func TestDarwinCapturedOwnerLoss(t *testing.T) {
	for _, mode := range []string{"buffered", "running"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			exe, err := os.Executable()
			require.NoError(t, err)
			owner := exec.Command(exe, "darwin-capture-owner")
			owner.Env = append(os.Environ(), "CAPY_DARWIN_CAPTURE_ROOT="+root,
				"CAPY_DARWIN_CAPTURE_MODE=hold close", "CAPY_DARWIN_OWNER_MODE="+mode)
			owner.Stdout, owner.Stderr = os.Stdout, os.Stderr
			require.NoError(t, owner.Start())
			joined := false
			t.Cleanup(func() {
				if !joined {
					owner.Process.Kill()
					owner.Wait()
				}
			})
			require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(root, "identity")))
			data, err := os.ReadFile(filepath.Join(root, "identity"))
			require.NoError(t, err)
			var identity darwinOwnerIdentity
			require.NoError(t, json.Unmarshal(data, &identity))
			resumed := false
			t.Cleanup(func() {
				if !resumed {
					if err := unix.Kill(identity.Helper, unix.SIGCONT); err != nil && !errors.Is(err, unix.ESRCH) {
						t.Errorf("resume fixture helper: %v", err)
					}
				}
				require.NoError(t, os.WriteFile(filepath.Join(root, "release-close"), nil, 0600))
				require.Eventually(t, func() bool { return errors.Is(unix.Kill(identity.Helper, 0), unix.ESRCH) }, 8*time.Second, 5*time.Millisecond)
			})
			lockRoot := filepath.Dir(identity.Path)
			ownerLock := filepath.Join(lockRoot, "locks", "owner-00.lock")
			require.Equal(t, 0, identity.Slot)
			lifecycle := filepath.Join(lockRoot, "lifecycle.lock")
			requireDarwinLease(t, ownerLock, true)
			requireDarwinLease(t, lifecycle, true)
			require.ErrorContains(t, runstore.Reset(t.Context(), identity.Path), "reset refused")
			st, err := runstore.Open(t.Context(), identity.Path, darwinCaptureKey, false)
			require.NoError(t, err)
			defer st.Close()
			row, err := st.Load(t.Context(), identity.Run)
			require.NoError(t, err)
			if mode == "buffered" {
				require.Equal(t, executor.CleanupNotSpawned, row.Cleanup.State)
				require.NoFileExists(t, filepath.Join(root, "once"))
			} else {
				require.Equal(t, executor.CleanupPending, row.Cleanup.State)
			}
			// Inspect the buffered/running state while the parent and stopped
			// helper still exist. Parent death can send SIGCONT on Darwin, so the
			// helper may already be cleaning up when later lease probes run.
			require.NoError(t, owner.Process.Kill())
			require.Error(t, owner.Wait())
			joined = true
			replacement, err := st.ClaimOwner(t.Context())
			require.NoError(t, err)
			require.NotEqual(t, identity.Slot, replacement.Slot)
			require.NoError(t, replacement.Lease.Close())
			require.NoError(t, st.Close())
			require.NoError(t, unix.Kill(identity.Helper, unix.SIGCONT))
			resumed = true
			require.NoError(t, awaitLeaseFile(t.Context(), filepath.Join(root, "closing")))
			requireDarwinLease(t, ownerLock, true)
			requireDarwinLease(t, lifecycle, true)
			require.ErrorContains(t, runstore.Reset(t.Context(), identity.Path), "reset refused", "backend close must precede lease release")
			require.NoError(t, os.WriteFile(filepath.Join(root, "release-close"), nil, 0600))
			require.Eventually(t, func() bool { return errors.Is(unix.Kill(identity.Helper, 0), unix.ESRCH) }, 8*time.Second, 5*time.Millisecond)
			requireDarwinLease(t, ownerLock, false)
			requireDarwinLease(t, lifecycle, false)
			final, err := runstore.Open(t.Context(), identity.Path, darwinCaptureKey, false)
			require.NoError(t, err)
			defer final.Close()
			row, err = final.Load(t.Context(), identity.Run)
			require.NoError(t, err)
			require.Equal(t, executor.CleanupDarwinObservation, row.Cleanup.Scope)
			require.True(t, runstore.CleanupAccepted(row.Cleanup))
			count, readErr := os.ReadFile(filepath.Join(root, "once"))
			if mode == "running" {
				require.NoError(t, readErr)
				require.Equal(t, "x", string(count))
				require.Equal(t, executor.CleanupObserved, row.Cleanup.State)
				require.Equal(t, "interrupted", row.Result.State)
			} else {
				// Buffered authorization can race EOF processing. Both zero and one
				// execution are legal; repeated execution or false proof is not.
				require.True(t, readErr == nil || os.IsNotExist(readErr))
				require.LessOrEqual(t, len(count), 1)
				if len(count) == 1 {
					require.Equal(t, "x", string(count))
					require.Equal(t, executor.CleanupObserved, row.Cleanup.State)
				}
			}
			last, err := final.ClaimOwner(t.Context())
			require.NoError(t, err)
			require.Equal(t, identity.Slot, last.Slot)
			require.NoError(t, last.Lease.Close())
		})
	}
}
