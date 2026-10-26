//go:build darwin && cgo && integration

package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDarwinObservedInventory(t *testing.T) {
	f := startReentryFixture(t)
	group := f.leader.Process.Pid
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	members, err := observeDarwinGroup(ctx, group)
	if err != nil || len(members) != 2 {
		t.Fatalf("native live inventory: %v, %v", members, err)
	}
	if partial, err := readDarwinGroup(group, 1); !errors.Is(err, syscall.ENOMEM) || partial != nil {
		t.Fatalf("partial table accepted: %v, %v", partial, err)
	}
	if _, err := readDarwinGroup(group, darwinGroupMemberLimit+1); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("oversized inventory: %v", err)
	}
	f.command(t, 'l')
	if err := syscall.Kill(-group, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := waitForExitContext(ctx, group); err != nil {
		t.Fatal(err)
	}
	members, err = observeDarwinGroup(ctx, group)
	if err != nil || len(members) != 1 || members[0].pid != group || !members[0].isZombie {
		t.Fatalf("native terminal inventory: %v, %v", members, err)
	}
	darwinObserveThenReap(t, f.leader, 0, syscall.SIGKILL)
	members, err = observeDarwinGroup(ctx, group)
	if err != nil || len(members) != 0 {
		t.Fatalf("empty group inventory: %v, %v", members, err)
	}
	cancel()
	if _, err := observeDarwinGroup(ctx, group); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inventory: %v", err)
	}
}

func TestDarwinObservedExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		code string
		exit int
	}{
		{name: "ordinary", code: "printf out; printf err >&2", exit: 0},
		{name: "nonzero", code: "printf out; printf err >&2; exit 7", exit: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			var sequence uint64
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := NewExecutor(t.TempDir(), 0).ExecuteStreamWithStart(ctx,
				ExecRequest{Language: Shell, Code: tc.code}, func(event OutputEvent) error {
					if event.Sequence != sequence+1 || event.Phase != "run" {
						return errors.New("invalid phase/sequence")
					}
					sequence = event.Sequence
					if event.Stream == "stdout" {
						stdout.Write(event.Data)
					} else {
						stderr.Write(event.Data)
					}
					return nil
				}, nil)
			if err != nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || !result.Started || result.Cleanup.State == CleanupRetired ||
				result.ExitCode == nil || *result.ExitCode != tc.exit || result.CaptureLost {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if stdout.String() != "out" || stderr.String() != "err" || result.ObservedBytes != 6 {
				t.Fatalf("output %q/%q bytes=%d", stdout.String(), stderr.String(), result.ObservedBytes)
			}
		})
	}
	for _, reason := range []string{"cancelled", "owner_lost", "control_failed"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			result, err := NewExecutor(t.TempDir(), 0).ExecuteStreamWithStart(ctx,
				ExecRequest{Language: Shell, Code: "printf ready; exec sleep 30"}, func(OutputEvent) error {
					cancel(errors.New(reason)) // command has started; no sleep race
					return nil
				}, nil)
			if err != nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || result.Cleanup.State == CleanupRetired ||
				result.StopReason != reason || result.Signal != int(syscall.SIGKILL) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	t.Run("capture failure keeps execution", func(t *testing.T) {
		root := t.TempDir()
		result, err := NewExecutor(root, 0).ExecuteStreamWithStart(context.Background(),
			ExecRequest{Language: Shell, Code: "printf data; printf error >&2; : > completed"},
			func(OutputEvent) error { return errors.New("synthetic capture failure") }, nil)
		if err != nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || !result.CaptureLost || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if _, err := os.Stat(filepath.Join(root, "completed")); err != nil {
			t.Fatalf("command did not complete after capture loss: %v", err)
		}
	})
	t.Run("private stream dispatcher is supported", func(t *testing.T) {
		if err := PrepareSupervisor(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDarwinObservedCompilerPhases(t *testing.T) {
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Fatalf("native compiler verification requires installed rustc: %v", err)
	}
	for _, tc := range []struct {
		name       string
		code       string
		wantPhases int
	}{
		{name: "compile and run", code: `fn main() { println!("compiled output"); }`, wantPhases: 2},
		{name: "failed compiler stops runtime", code: "not valid rust", wantPhases: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phases := []string{}
			var stdout strings.Builder
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result, err := NewExecutor(t.TempDir(), 0).ExecuteStreamWithStart(ctx,
				ExecRequest{Language: Rust, Code: tc.code}, func(event OutputEvent) error {
					if event.Stream == "stdout" {
						stdout.Write(event.Data)
					}
					return nil
				}, func(phase string) error { phases = append(phases, phase); return nil })
			if err != nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || result.Cleanup.State == CleanupRetired || len(phases) != tc.wantPhases || phases[0] != "compile" {
				t.Fatalf("phases=%v result=%+v err=%v", phases, result, err)
			}
			if tc.wantPhases == 2 && (phases[1] != "run" || stdout.String() != "compiled output\n") {
				t.Fatalf("runtime phase/output: %v %q", phases, stdout.String())
			}
		})
	}
	t.Run("runtime spawn failure preserves compiler observation", func(t *testing.T) {
		root := t.TempDir()
		compiler := filepath.Join(root, "compiler")
		// A successful compiler phase produces a deliberately non-executable
		// artifact. Exercise real runtime Start failure, not a synthetic result.
		if err := os.WriteFile(compiler, []byte("#!/bin/sh\nprintf artifact > \"$3\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		e := NewExecutor(root, 0)
		e.detectOnce.Do(func() { e.runtimes = map[Language]string{Rust: compiler} })
		result, err := e.ExecuteStreamWithStart(context.Background(), ExecRequest{Language: Rust, Code: "fixture"},
			func(OutputEvent) error { return nil }, nil)
		if err == nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || !result.Started || result.Cleanup.State == CleanupRetired || result.SpawnCode != 126 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

const observedFixtureEnv = "CAPY_DARWIN_OBSERVED_FIXTURE"

// The child has a cooperative stop file and a finite lifetime. The test never
// sends a terminating signal to an orphan's numeric PID after leader reap.
func TestDarwinObservedFixture(t *testing.T) {
	mode := os.Getenv(observedFixtureEnv)
	if mode == "" {
		return
	}
	if mode == "member" {
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(os.Getenv("CAPY_DARWIN_OBSERVED_STOP")); err == nil {
				os.Exit(0)
			}
			time.Sleep(5 * time.Millisecond)
		}
		os.Exit(91)
	}
	if mode != "closed" && mode != "escaped" {
		os.Exit(92)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^TestDarwinObservedFixture$")
	cmd.Env = append(os.Environ(), observedFixtureEnv+"=member")
	if mode == "escaped" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	} // closed mode gets /dev/null for both streams
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("CAPY_DARWIN_OBSERVED_PID"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		if cleanupErr := reapReentryChild(cmd, true); cleanupErr != nil {
			panic(cleanupErr)
		}
		t.Fatal(err)
	}
	os.Exit(0) // intentional orphan, published only into this synthetic fixture
}

func observedChildPhase(t *testing.T, mode string) darwinPhase {
	t.Helper()
	root := t.TempDir()
	stop := filepath.Join(root, "stop")
	pidFile := filepath.Join(root, "pid")
	t.Setenv(observedFixtureEnv, mode)
	t.Setenv("CAPY_DARWIN_OBSERVED_STOP", stop)
	t.Setenv("CAPY_DARWIN_OBSERVED_PID", pidFile)
	t.Cleanup(func() {
		if err := os.WriteFile(stop, nil, 0o600); err != nil {
			t.Errorf("request fixture stop: %v", err)
		}
		data, err := os.ReadFile(pidFile)
		if os.IsNotExist(err) {
			return // no child published; its leader owns failed-start cleanup
		}
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil || pid <= 1 {
			t.Fatalf("invalid fixture child pid: %q, %v", data, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := syscall.Kill(pid, 0)
			if errors.Is(err, syscall.ESRCH) {
				return
			}
			if err != nil || time.Now().After(deadline) {
				panic(fmt.Sprintf("fixture child disappearance unconfirmed: %d, %v", pid, err))
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return darwinPhase{bin: bin, args: []string{"-test.run=^TestDarwinObservedFixture$"}, cwd: root, tmpDir: root, name: "run"}
}

func TestDarwinObservedCleanupFailures(t *testing.T) {
	t.Run("natural status wins later cancellation", func(t *testing.T) {
		root := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ops := nativeDarwinPhaseOps()
		wait := ops.waitExit
		ops.waitExit = func(ctx context.Context, pid int) error {
			err := wait(ctx, pid)
			if err == nil {
				cancel() // deterministic cancellation after terminal observation
			}
			return err
		}
		result, err := runDarwinStreamPhase(ctx,
			darwinPhase{bin: "/bin/sh", args: []string{"-c", "exit 9"}, cwd: root, tmpDir: root, name: "run"},
			&eventWriter{sink: func(OutputEvent) error { return nil }}, ops)
		if err != nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || result.StopReason != "" ||
			result.ExitCode == nil || *result.ExitCode != 9 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("live child with closed output is cleaned up", func(t *testing.T) {
		phase := observedChildPhase(t, "closed")
		result, err := runDarwinStreamPhase(context.Background(), phase,
			&eventWriter{sink: func(OutputEvent) error { return nil }}, nativeDarwinPhaseOps())
		if err != nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupObserved}) || result.Cleanup.State == CleanupRetired || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("escaped pipe holder prevents completion", func(t *testing.T) {
		phase := observedChildPhase(t, "escaped")
		began := time.Now()
		result, err := runDarwinStreamPhase(context.Background(), phase,
			&eventWriter{sink: func(OutputEvent) error { return nil }}, nativeDarwinPhaseOps())
		if err == nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupPending}) || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if time.Since(began) > 6*time.Second {
			t.Fatal("escaped pipe holder exceeded cleanup budget and scheduling allowance")
		}
	})
	t.Run("visible survivor prevents completion despite EOF", func(t *testing.T) {
		phase := observedChildPhase(t, "closed")
		ops := nativeDarwinPhaseOps()
		ops.killGroup = func(int) error { return nil } // suppress termination to retain a real visible survivor
		ops.budget = 100 * time.Millisecond
		result, err := runDarwinStreamPhase(context.Background(), phase,
			&eventWriter{sink: func(OutputEvent) error { return nil }}, ops)
		if err == nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupPending}) || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("signal error prevents completion", func(t *testing.T) {
		phase := observedChildPhase(t, "closed")
		ops := nativeDarwinPhaseOps()
		kill := ops.killGroup
		ops.killGroup = func(pid int) error {
			// Retire the real fixture safely, then inject the failed-call result.
			return errors.Join(kill(pid), syscall.EPERM)
		}
		result, err := runDarwinStreamPhase(context.Background(), phase,
			&eventWriter{sink: func(OutputEvent) error { return nil }}, ops)
		if !errors.Is(err, syscall.EPERM) || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupPending}) || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	for _, tc := range []struct {
		name      string
		inventory func(context.Context, int) ([]darwinGroupMember, error)
	}{
		{name: "inventory error", inventory: func(context.Context, int) ([]darwinGroupMember, error) { return nil, syscall.EACCES }},
		{name: "missing leader", inventory: func(context.Context, int) ([]darwinGroupMember, error) { return nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			ops := nativeDarwinPhaseOps()
			ops.inventory = tc.inventory
			result, err := runDarwinStreamPhase(context.Background(),
				darwinPhase{bin: "/bin/sh", args: []string{"-c", "exit 9"}, cwd: root, tmpDir: root, name: "run"},
				&eventWriter{sink: func(OutputEvent) error { return nil }}, ops)
			if err == nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupPending}) || result.ExitCode == nil || *result.ExitCode != 9 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestDarwinObservedWaitFailures(t *testing.T) {
	for _, scenario := range []string{"observer unavailable", "cancelled but not terminal", "sole reap failed"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pid, waits, signals := 0, 0, 0
			reaped := false
			// Failure injection below makes the production phase relinquish its
			// Go Process handle. The fixture then takes the sole native reap,
			// after verifying child ownership. No competing waiter is left behind.
			t.Cleanup(func() {
				if pid == 0 || reaped {
					return
				}
				terminal, err := observeDarwinExit(pid)
				if err != nil {
					panic(fmt.Sprintf("fixture lost native child ownership: %v", err))
				}
				if !terminal {
					if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
						panic(err)
					}
				}
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := waitForExitContext(cleanupCtx, pid); err != nil {
					panic(err)
				}
				if got, err := syscall.Wait4(pid, nil, 0, nil); err != nil || got != pid {
					panic(fmt.Sprintf("fixture reap: got=%d err=%v", got, err))
				}
			})
			ops := nativeDarwinPhaseOps()
			ops.budget = 50 * time.Millisecond
			ops.killGroup = func(int) error {
				signals++
				return nil // preserve a genuinely stopped child in the timeout case
			}
			ops.waitExit = func(waitCtx context.Context, child int) error {
				pid = child
				waits++
				if scenario == "sole reap failed" {
					if err := waitForExitContext(waitCtx, child); err != nil {
						return err
					}
					// Deliberately violate the sole-reaper contract to obtain a real
					// ECHILD from the production cmd.Wait, not a fake wait result.
					if got, err := syscall.Wait4(child, nil, 0, nil); err != nil || got != child {
						t.Fatalf("competing fixture reap: %d %v", got, err)
					}
					reaped = true
					return nil
				}
				if waits > 1 {
					return waitForExitContext(waitCtx, child) // real bounded stopped-child timeout
				}
				deadline := time.Now().Add(5 * time.Second)
				for {
					stopped, err := darwinChildStopped(child)
					if err != nil || time.Now().After(deadline) {
						t.Fatalf("fixture stop synchronization: %v", err)
					}
					if stopped {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if scenario == "observer unavailable" {
					return syscall.EMFILE // e.g. native kqueue creation failure
				}
				cancel()
				return context.Canceled
			}
			code := "kill -STOP $$; exit 9"
			if scenario == "sole reap failed" {
				code = "printf data; exit 9"
				ops.inventory = func(context.Context, int) ([]darwinGroupMember, error) {
					// Stale observation drives the final native reap error path.
					return []darwinGroupMember{{pid: pid, isZombie: true}}, nil
				}
			}
			began := time.Now()
			result, err := runDarwinStreamPhase(ctx,
				darwinPhase{bin: "/bin/sh", args: []string{"-c", code}, cwd: root, tmpDir: root, name: "run"},
				&eventWriter{sink: func(OutputEvent) error { return nil }}, ops)
			if err == nil || result.Cleanup != (CleanupEvidence{Scope: CleanupDarwinObservation, State: CleanupPending}) || result.Cleanup.State == CleanupRetired || result.ExitCode != nil {
				t.Fatalf("unverified outcome: result=%+v err=%v", result, err)
			}
			if time.Since(began) > time.Second {
				t.Fatal("unconfirmed terminal state blocked the phase return")
			}
			switch scenario {
			case "observer unavailable":
				if !errors.Is(err, syscall.EMFILE) || waits != 1 || signals != 0 {
					t.Fatalf("unexpected observer-error operations: waits=%d signals=%d err=%v", waits, signals, err)
				}
			case "cancelled but not terminal":
				if !errors.Is(err, context.DeadlineExceeded) || waits != 2 || signals != 1 || result.StopReason != "cancelled" {
					t.Fatalf("unexpected timeout operations: waits=%d signals=%d result=%+v err=%v", waits, signals, result, err)
				}
			case "sole reap failed":
				if !errors.Is(err, syscall.ECHILD) || waits != 1 || signals != 0 {
					t.Fatalf("unexpected reap-error operations: waits=%d signals=%d err=%v", waits, signals, err)
				}
			}
			if !reaped {
				if stopped, err := darwinChildStopped(pid); err != nil || !stopped {
					t.Fatalf("phase consumed or lost the retained stopped child: stopped=%v err=%v", stopped, err)
				}
			}
		})
	}
}
