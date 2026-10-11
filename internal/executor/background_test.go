package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitForExecutorFile(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 5*time.Second, 5*time.Millisecond, "waiting for %s", path)
}

func waitForBackgroundCleanup(t *testing.T, e *PolyglotExecutor, tmpDir string) {
	t.Helper()
	require.Eventually(t, func() bool {
		e.bgMu.Lock()
		tracked := len(e.backgroundPids)
		e.bgMu.Unlock()
		_, err := os.Stat(tmpDir)
		return tracked == 0 && os.IsNotExist(err)
	}, 5*time.Second, 5*time.Millisecond, "background process and temporary files must be retired")
}

func TestBackgroundOutputDiscardAndCleanup(t *testing.T) {
	for _, finish := range []string{"exit", "shutdown"} {
		t.Run(finish, func(t *testing.T) {
			t.Parallel()
			e := newTestExecutor(t)
			t.Cleanup(e.CleanupBackgrounded)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result *ExecResult
				err    error
			}
			resultCh := make(chan outcome, 1)
			go func() {
				result, err := e.Execute(ctx, ExecRequest{
					Language: Shell, Background: true,
					Code: `set -e
printf 'partial stdout\n'
printf 'partial stderr\n' >&2
printf '%s' "$TMPDIR" > workspace
touch ready
while [ ! -e write ]; do sleep 0.01; done
dd if=/dev/zero bs=65536 count=64 2>/dev/null
dd if=/dev/zero bs=65536 count=64 1>&2 2>/dev/null
test -f "$TMPDIR/script.sh"
touch progressed
while [ ! -e finish ]; do sleep 0.01; done
`,
				})
				resultCh <- outcome{result: result, err: err}
			}()
			waitForExecutorFile(t, filepath.Join(e.projectDir, "ready"))
			workspace, err := os.ReadFile(filepath.Join(e.projectDir, "workspace"))
			require.NoError(t, err)
			tmpDir := string(workspace)
			cancel()
			var got outcome
			select {
			case got = <-resultCh:
			case <-time.After(5 * time.Second):
				t.Fatal("background request did not detach")
			}
			require.NoError(t, got.err)
			require.True(t, got.result.Backgrounded)
			assert.Equal(t, "partial stdout\n", got.result.Stdout)
			assert.Equal(t, "partial stderr\n", got.result.Stderr)
			e.bgMu.Lock()
			process := e.backgroundPids[got.result.PID]
			e.bgMu.Unlock()
			require.NotNil(t, process)
			require.NoError(t, os.WriteFile(filepath.Join(e.projectDir, "write"), nil, 0o600))
			waitForExecutorFile(t, filepath.Join(e.projectDir, "progressed"))
			for _, buf := range []*safeBuffer{process.stdout, process.stderr} {
				assert.Zero(t, buf.Size(), "post-detach output must be drained without retention")
				assert.Empty(t, buf.String())
				buf.mu.Lock()
				assert.Zero(t, cap(buf.data))
				buf.mu.Unlock()
			}
			if finish == "shutdown" {
				e.CleanupBackgrounded()
				e.CleanupBackgrounded() // repeated cleanup must be safe
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(e.projectDir, "finish"), nil, 0o600))
			}
			waitForBackgroundCleanup(t, e, tmpDir)
			e.CleanupBackgrounded() // no stale entry after natural or forced exit
		})
	}
}

func TestBackgroundExitCancellationRace(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	t.Cleanup(e.CleanupBackgrounded)
	for range 32 {
		workspace := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		var callers sync.WaitGroup
		callers.Add(1)
		go func() {
			defer callers.Done()
			cancel()
		}()
		result, err := e.runProcess(ctx, e.Runtimes()[Shell], []string{"-c", "exit 0"}, e.projectDir, workspace, ExecRequest{Background: true})
		callers.Wait()
		require.NoError(t, err)
		if result.Backgrounded {
			waitForBackgroundCleanup(t, e, workspace)
		} else {
			assert.Equal(t, 0, result.ExitCode)
		}
	}
}

func TestExecuteForegroundHardCap(t *testing.T) {
	t.Parallel()
	e := newTestExecutor(t)
	result, err := e.Execute(context.Background(), ExecRequest{
		Language: Shell, TimeoutSec: 10,
		Code: "dd if=/dev/zero bs=1048576 count=101 2>/dev/null; sleep 60",
	})
	require.NoError(t, err)
	assert.True(t, result.Killed)
	assert.False(t, result.Backgrounded)
	assert.Contains(t, result.Stderr, "output capped at 100MB")
}

func TestExitedLeaderRetainsGroupOwnership(t *testing.T) {
	for _, mode := range []string{"timeout", "shutdown", "hard cap"} {
		t.Run(mode, func(t *testing.T) {
			e := newTestExecutor(t)
			t.Cleanup(e.CleanupBackgrounded)
			workspace := t.TempDir()
			code := "sleep 3 & exit 0"
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if mode == "hard cap" {
				cancel()
				ctx = context.Background()
				code = "(dd if=/dev/zero bs=1048576 count=101 2>/dev/null; sleep 3) & exit 0"
			}
			start := time.Now()
			result, err := e.runProcess(ctx, e.Runtimes()[Shell], []string{"-c", code}, e.projectDir, workspace,
				ExecRequest{Background: mode == "shutdown"})
			require.NoError(t, err)
			if mode == "shutdown" {
				require.True(t, result.Backgrounded)
				e.bgMu.Lock()
				process := e.backgroundPids[result.PID]
				e.bgMu.Unlock()
				require.NotNil(t, process)
				// The leader has exited, but its unreaped process still owns
				// this identifier while the descendant holds its output pipes.
				require.NoError(t, waitForExit(result.PID))
				require.NoError(t, process.cmd.Process.Signal(syscall.Signal(0)))
				e.CleanupBackgrounded()
				waitForBackgroundCleanup(t, e, workspace)
				assert.ErrorIs(t, process.cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone)
			} else if mode == "hard cap" {
				assert.True(t, result.Killed)
			} else {
				assert.True(t, result.TimedOut)
			}
			assert.Less(t, time.Since(start), 2*time.Second, "descendant must be terminated instead of waiting for its 3-second sleep")
		})
	}
}

func TestProcessObserverFailure(t *testing.T) {
	for _, mode := range []string{"attached", "detached"} {
		t.Run(mode, func(t *testing.T) {
			var logs safeBuffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			e := newTestExecutor(t)
			t.Cleanup(e.CleanupBackgrounded)
			fail := make(chan struct{})
			var release sync.Once
			defer release.Do(func() { close(fail) })
			e.observeExit = func(int) error {
				<-fail
				return syscall.EPERM
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type outcome struct {
				result *ExecResult
				err    error
			}
			resultCh := make(chan outcome, 1)
			go func() {
				result, err := e.Execute(ctx, ExecRequest{
					Language: Shell, Background: mode == "detached",
					Code: `printf '%s' "$TMPDIR" > workspace; touch ready; sleep 60`,
				})
				resultCh <- outcome{result: result, err: err}
			}()
			waitForExecutorFile(t, filepath.Join(e.projectDir, "ready"))
			workspace, err := os.ReadFile(filepath.Join(e.projectDir, "workspace"))
			require.NoError(t, err)
			if mode == "detached" {
				cancel()
			} else {
				release.Do(func() { close(fail) })
			}
			select {
			case got := <-resultCh:
				if mode == "detached" {
					require.NoError(t, got.err)
					require.True(t, got.result.Backgrounded)
				} else {
					require.ErrorIs(t, got.err, syscall.EPERM)
					require.ErrorContains(t, got.err, "observing process exit")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("observer failure stranded execution")
			}
			release.Do(func() { close(fail) })
			waitForBackgroundCleanup(t, e, string(workspace))
			if mode == "detached" {
				assert.Contains(t, logs.String(), "background process capture failed")
				assert.Contains(t, logs.String(), "observing process exit")
			} else {
				assert.Empty(t, logs.String(), "attached errors are returned, not also logged")
			}
		})
	}
}
