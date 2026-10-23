//go:build darwin && cgo && integration

package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// The fixture has no shell descendants or os/exec copy goroutines. Its terminal
// status belongs exclusively to the parent test's cmd.Wait.
func TestDarwinWaitFixture(t *testing.T) {
	switch os.Getenv("CAPY_DARWIN_WAIT_FIXTURE") {
	case "exit":
		os.Exit(17)
	case "stop":
		if err := syscall.Kill(os.Getpid(), syscall.SIGSTOP); err != nil {
			os.Exit(90)
		}
		os.Exit(23)
	}
}

func darwinWaitChild(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestDarwinWaitFixture$")
	cmd.Env = append(os.Environ(), "CAPY_DARWIN_WAIT_FIXTURE="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState != nil {
			return
		}
		// Cleanup also uses terminal observation before the sole reap. On a
		// failure, stop the test process rather than abandon a wait goroutine.
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("fixture kill: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := waitForExitContext(ctx, cmd.Process.Pid); err != nil {
			t.Errorf("fixture cleanup could not observe terminal state: %v", err)
			panic("cannot safely retire Darwin wait fixture")
		}
		_ = cmd.Wait() // Killed fixtures have an expected nonzero exit status.
	})
	return cmd
}

func darwinWaitStopped(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stopped, err := darwinChildStopped(cmd.Process.Pid)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if stopped {
			// WNOWAIT preserves the pending stop, including for the first
			// WEXITED probe performed by the production observer.
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("fixture did not reach SIGSTOP")
}

func darwinObserveThenReap(t *testing.T, cmd *exec.Cmd, wantExit int, wantSignal syscall.Signal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Repeated successful observations must preserve the status for cmd.Wait,
	// including when observation starts after the child has already exited.
	for range 3 {
		if err := waitForExitContext(ctx, cmd.Process.Pid); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected fixture exit status, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Error("terminal observation did not permit prompt reaping")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("unexpected wait status: %T", cmd.ProcessState.Sys())
	}
	if wantSignal != 0 {
		if !status.Signaled() || status.Signal() != wantSignal {
			t.Fatalf("signal status = %v, want %v", status, wantSignal)
		}
	} else if !status.Exited() || status.ExitStatus() != wantExit {
		t.Fatalf("exit status = %v, want %d", status, wantExit)
	}
	// No terminal status remains after the one reap. Ownership loss must be
	// reported, never interpreted as a successful observation (ESRCH shortcut).
	if err := waitForExitContext(ctx, cmd.Process.Pid); !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("observation after reap = %v, want ECHILD", err)
	}
}

func TestDarwinWaitForExit(t *testing.T) {
	t.Run("exiting and already terminal", func(t *testing.T) {
		cmd := darwinWaitChild(t, "exit")
		darwinObserveThenReap(t, cmd, 17, 0)
	})
	for _, finish := range []string{"continue", "kill"} {
		t.Run("stopped then "+finish, func(t *testing.T) {
			cmd := darwinWaitChild(t, "stop")
			darwinWaitStopped(t, cmd)
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			started := time.Now()
			if err := waitForExitContext(ctx, cmd.Process.Pid); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stopped child observation = %v, want deadline", err)
			}
			if time.Since(started) > time.Second {
				t.Error("stopped child exceeded observation budget")
			}
			if stopped, err := darwinChildStopped(cmd.Process.Pid); err != nil || !stopped {
				t.Fatalf("observation consumed or changed stopped state: stopped=%v err=%v", stopped, err)
			}
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("deadline lost child ownership: %v", err)
			}
			if finish == "continue" {
				if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
					t.Fatal(err)
				}
				darwinObserveThenReap(t, cmd, 23, 0)
			} else {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				darwinObserveThenReap(t, cmd, 0, syscall.SIGKILL)
			}
		})
	}
	t.Run("cancel observation without killing child", func(t *testing.T) {
		cmd := darwinWaitChild(t, "stop")
		darwinWaitStopped(t, cmd)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := time.AfterFunc(25*time.Millisecond, cancel)
		defer timer.Stop()
		started := time.Now()
		if err := waitForExitContext(ctx, cmd.Process.Pid); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled observation = %v", err)
		}
		if time.Since(started) > time.Second {
			t.Error("cancellation did not bound observation")
		}
		terminal, err := observeDarwinExit(cmd.Process.Pid)
		if err != nil || terminal {
			t.Fatalf("cancelled observer changed stopped child: terminal=%v err=%v", terminal, err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		darwinObserveThenReap(t, cmd, 0, syscall.SIGKILL)
	})
	t.Run("invalid and non-child identifiers", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		for _, pid := range []int{-1, 0, 1 << 32} {
			if err := waitForExitContext(ctx, pid); !errors.Is(err, syscall.EINVAL) {
				t.Errorf("pid %d: got %v, want EINVAL", pid, err)
			}
		}
		if err := waitForExitContext(ctx, os.Getpid()); !errors.Is(err, syscall.ECHILD) {
			t.Fatalf("non-child: got %v, want ECHILD", err)
		}
	})
}
