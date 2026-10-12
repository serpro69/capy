package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/serpro69/capy/internal/platform"
	"github.com/spf13/cobra"
)

func newCodexLaunchCmd() *cobra.Command {
	return &cobra.Command{
		Use: "codex-launch <owner-id> <mode> <event>", Hidden: true,
		Args: cobra.ExactArgs(3), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runCodexLaunch(cmd.Context(), args[0], args[1], args[2])
			if err != nil && args[1] == "hook" && args[2] == "pretooluse" {
				return &exitError{code: 2, err: err}
			}
			return err
		},
	}
}

func runCodexLaunch(ctx context.Context, id, mode, event string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Codex launch cwd: %w; restore the working directory", err)
	}
	owner, err := platform.ResolveCodexOwner(cwd, id, mode, event)
	if err != nil {
		return err
	}
	wrapper := filepath.Join(owner, ".codex", "scripts", "capy.sh")
	if err := checkCodexWrapper(wrapper); err != nil {
		return fmt.Errorf("Codex wrapper %s: %w; repair permissions and rerun capy setup --platform codex", wrapper, err)
	}
	args := []string{wrapper, "serve", "--project-dir", owner}
	if mode == "hook" {
		args = []string{wrapper, "hook", "--platform", "codex", event, "--project-dir", owner}
	}
	// Do not chdir: event cwd and project ownership have different policy roles.
	child := exec.Command("bash", args...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := superviseCodexProcess(ctx, child); err != nil {
		code := 1
		var requested *exitError
		var exit *exec.ExitError
		if errors.As(err, &requested) {
			code = requested.code
		} else if errors.As(err, &exit) && exit.ExitCode() > 0 {
			code = exit.ExitCode()
		}
		return &exitError{code: code, err: fmt.Errorf("Codex wrapper launch failed: %w; repair installation and rerun capy setup --platform codex", err)}
	}
	return nil
}

func checkCodexWrapper(path string) error {
	return inspectCodexWrapper(path, os.Open)
}

func inspectCodexWrapper(path string, open func(string) (*os.File, error)) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("expected a regular wrapper, not a symlink or special file")
	}
	f, err := open(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// Give the wrapper/server their own process group. Forward signals/cancellation,
// reap the child, and bound shutdown even if a descendant ignores the signal.
func superviseCodexProcess(ctx context.Context, child *exec.Cmd) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := child.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	var sig syscall.Signal
	select {
	case err := <-done:
		// A wrapper can fail after spawning a descendant. Clean the process group
		// on every completion path, not only when the host signals the launcher.
		return errors.Join(err, killCodexGroup(child.Process.Pid))
	case received := <-signals:
		sig = received.(syscall.Signal)
	case <-ctx.Done():
		sig = syscall.SIGTERM
	}
	if err := syscall.Kill(-child.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		child.Process.Kill()
		return errors.Join(err, <-done)
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		// Kill any remaining descendants even if the immediate shell exited.
		syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	case <-timer.C:
		syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		<-done
	}
	return &exitError{code: 128 + int(sig), err: fmt.Errorf("Codex launch interrupted by %s", sig)}
}

func killCodexGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop Codex wrapper descendants: %w", err)
	}
	return nil
}
