//go:build darwin && cgo && integration

package executor

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

const reentryFixtureEnv = "CAPY_DARWIN_REENTRY_FIXTURE"

// Re-exec avoids forking Go runtime state without exec. The leader is a real
// session leader and the member is its real child, not a same-session sibling.
func TestDarwinGroupReentryFixture(t *testing.T) {
	var err error
	switch os.Getenv(reentryFixtureEnv) {
	case "leader":
		err = runReentryLeader()
	case "member":
		err = runReentryMember()
	default:
		return
	}
	if err != nil {
		// stderr may intentionally be closed. Exit status / missing handshake
		// still fail the parent; no error is interpreted as experimental success.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(90)
	}
	os.Exit(0)
}

func reentryCommand(mode string) (*exec.Cmd, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "-test.run=^TestDarwinGroupReentryFixture$")
	// No project credentials or unrelated fixture flags cross this boundary.
	cmd.Env = []string{reentryFixtureEnv + "=" + mode}
	return cmd, nil
}

func reentryInheritedPipe(fd int) (*os.File, error) {
	// NewFile requires a nonblocking descriptor for deadline support on Unix.
	if err := syscall.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "reentry-control")
	if err := f.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func runReentryLeader() (result error) {
	control, err := reentryInheritedPipe(3)
	if err != nil {
		return err
	}
	defer control.Close()
	report, err := reentryInheritedPipe(4)
	if err != nil {
		return err
	}
	defer report.Close()
	memberControl := os.NewFile(5, "member-control")
	defer memberControl.Close()
	memberReport := os.NewFile(6, "member-report")
	defer memberReport.Close()
	cmd, err := reentryCommand("member")
	if err != nil {
		return err
	}
	cmd.ExtraFiles = []*os.File{memberControl, memberReport}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	// Before intentional leader death, the leader owns and reaps its member on
	// every ordinary abort, including EOF/deadline or failed PID publication.
	defer func() { result = errors.Join(result, reapReentryChild(cmd, true)) }()
	memberControl.Close()
	memberReport.Close()
	if err := binary.Write(report, binary.BigEndian, uint32(cmd.Process.Pid)); err != nil {
		return err
	}
	var b [1]byte
	_, err = io.ReadFull(control, b[:])
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("leader control ended unexpectedly: byte=%d err=%v", b[0], err)
}

func runReentryMember() error {
	control, err := reentryInheritedPipe(3)
	if err != nil {
		return err
	}
	defer control.Close()
	report, err := reentryInheritedPipe(4)
	if err != nil {
		return err
	}
	defer report.Close()
	group := syscall.Getpgrp()
	if group != os.Getppid() || group == os.Getpid() {
		return errors.New("member did not inherit its session leader's group")
	}
	if _, err := io.WriteString(os.Stdout, "member stdout\n"); err != nil {
		return err
	}
	if _, err := io.WriteString(os.Stderr, "member stderr\n"); err != nil {
		return err
	}
	if err := errors.Join(os.Stdout.Close(), os.Stderr.Close()); err != nil {
		return err
	}
	if _, err := report.Write([]byte{'r'}); err != nil {
		return err
	}
	for {
		var b [1]byte
		if _, err := io.ReadFull(control, b[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch b[0] {
		case 'l':
			err = syscall.Setpgid(0, 0) // leave the group, retain the session
		case 'j':
			err = syscall.Setpgid(0, group)
		case 'p':
			if syscall.Getpgrp() != group {
				return errors.New("member is no longer in the original group")
			}
		default:
			return fmt.Errorf("unexpected member command %d", b[0])
		}
		if err != nil {
			return err
		}
		if _, err := report.Write(b[:]); err != nil {
			return err
		}
	}
}

func reapReentryChild(cmd *exec.Cmd, kill bool) error {
	if cmd.ProcessState != nil {
		return nil
	}
	if kill {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill owned fixture child: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForExitContext(ctx, cmd.Process.Pid); err != nil {
		return fmt.Errorf("observe owned fixture child: %w", err)
	}
	err := cmd.Wait() // one reaper, observed terminal, no os/exec pipe copiers
	var exitErr *exec.ExitError
	if err != nil && (!kill || !errors.As(err, &exitErr)) {
		return fmt.Errorf("reap owned fixture child: %w", err)
	}
	return nil
}

type reentryFixture struct {
	leader                    *exec.Cmd
	member                    int
	memberControl, memberRead *os.File
	stdout, stderr            *os.File
}

func startReentryFixture(t *testing.T) *reentryFixture {
	t.Helper()
	pipe := func() (*os.File, *os.File) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		return r, w
	}
	leaderControlR, leaderControlW := pipe()
	leaderReportR, leaderReportW := pipe()
	memberControlR, memberControlW := pipe()
	memberReportR, memberReportW := pipe()
	outR, outW := pipe()
	errR, errW := pipe()
	cmd, err := reentryCommand("leader")
	if err != nil {
		t.Fatal(err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.ExtraFiles = []*os.File{leaderControlR, leaderReportW, memberControlR, memberReportW}
	cmd.Stdout, cmd.Stderr = outW, errW
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &reentryFixture{leader: cmd, memberControl: memberControlW, memberRead: memberReportR, stdout: outR, stderr: errR}
	t.Cleanup(func() {
		// No numeric signal to the orphaned member, and no group signal after
		// leader reap. EOF asks the controlled member to exit; its own deadline
		// also bounds life if a control endpoint is accidentally retained.
		memberControlW.Close()
		leaderControlW.Close()
		if err := reapReentryChild(cmd, false); err != nil {
			t.Errorf("graceful fixture cleanup: %v", err)
			if err := reapReentryChild(cmd, true); err != nil {
				panic(fmt.Sprintf("cannot retire fixture leader: %v", err))
			}
		}
		if f.member != 0 {
			// The member is reparented after deliberate leader death. Wait for
			// PID absence, including init's reap, rather than claiming EOF proves
			// death. PID reuse only causes a conservative timeout; never signal it.
			deadline := time.Now().Add(10 * time.Second)
			for {
				err := syscall.Kill(f.member, 0)
				if errors.Is(err, syscall.ESRCH) {
					t.Log("fixture cleanup: member PID absent; leader reaped")
					break
				}
				if err != nil || time.Now().After(deadline) {
					panic(fmt.Sprintf("cannot confirm fixture member cleanup: pid=%d err=%v", f.member, err))
				}
				time.Sleep(5 * time.Millisecond) // cleanup polling, not race synchronization
			}
		}
	})
	for _, file := range []*os.File{leaderControlR, leaderReportW, memberControlR, memberReportW, outW, errW} {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []*os.File{leaderReportR, memberControlW, memberReportR, outR, errR} {
		if err := file.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var pid uint32
	if err := binary.Read(leaderReportR, binary.BigEndian, &pid); err != nil {
		t.Fatalf("member PID publication: %v", err)
	}
	if pid <= 1 || int(pid) == cmd.Process.Pid {
		t.Fatalf("invalid member PID %d", pid)
	}
	f.member = int(pid)
	reentryReadByte(t, memberReportR, 'r')
	assertReentryGroup(t, cmd.Process.Pid, cmd.Process.Pid, f.member)
	return f
}

func reentryReadByte(t *testing.T, f *os.File, want byte) {
	t.Helper()
	var b [1]byte
	if _, err := io.ReadFull(f, b[:]); err != nil || b[0] != want {
		t.Fatalf("fixture handshake: got %q err=%v, want %q", b[0], err, want)
	}
}

func (f *reentryFixture) command(t *testing.T, command byte) {
	t.Helper()
	if _, err := f.memberControl.Write([]byte{command}); err != nil {
		t.Fatal(err)
	}
	reentryReadByte(t, f.memberRead, command)
}

func assertReentryGroup(t *testing.T, group int, want ...int) {
	t.Helper()
	got, err := darwinFixtureGroup(group)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("group %d: got %v, want %v", group, got, want)
	}
}

func TestDarwinGroupReentry(t *testing.T) {
	t.Run("abort before escape", func(t *testing.T) {
		startReentryFixture(t) // cleanup must also handle a live leader/member
	})
	t.Run("rejoin after terminal leader observation", func(t *testing.T) {
		f := startReentryFixture(t)
		group := f.leader.Process.Pid
		f.command(t, 'l')
		assertReentryGroup(t, group, group)
		// This is the cancellation variant: a successful final group signal
		// kills the live leader while the escaped member remains outside G.
		if err := syscall.Kill(-group, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := waitForExitContext(ctx, group); err != nil {
			t.Fatal(err)
		}
		for _, stream := range []struct {
			file *os.File
			want string
		}{{f.stdout, "member stdout\n"}, {f.stderr, "member stderr\n"}} {
			data, err := io.ReadAll(io.LimitReader(stream.file, 256))
			if err != nil || string(data) != stream.want {
				t.Fatalf("natural EOF: got %q err=%v, want %q", data, err, stream.want)
			}
		}
		for range 3 {
			assertReentryGroup(t, group, group)
		}
		t.Logf("before re-entry: successful group SIGKILL; terminal unreaped leader=%d; three snapshots=[%d]; stdout/stderr EOF", group, group)
		f.command(t, 'j') // explicit barrier: no timing window is left to chance
		assertReentryGroup(t, group, group, f.member)
		darwinObserveThenReap(t, f.leader, 0, syscall.SIGKILL)
		f.command(t, 'p') // fresh response proves user code runs after leader reap
		assertReentryGroup(t, group, f.member)
		t.Logf("after leader reap: original group=%d; live member=%d responded to ping", group, f.member)
	})
}
