package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Keep these installed-command probes in the normal suite, like the existing
// Codex launch fixtures. All input limits here are the production defaults.
func TestCodexHookInputExitCodes(t *testing.T) {
	bin := buildStdioCapy(t)
	owner := t.TempDir()
	_, env := installCodexLaunch(t, bin, owner)
	// Race-instrumented subprocesses must not add the default one-second
	// exit sleep to the transport timing assertions.
	env = append(env, "GORACE=atexit_sleep_ms=0")
	cwd := filepath.Join(owner, "subdirectory")
	require.NoError(t, os.Mkdir(cwd, 0o755))
	for _, generated := range []bool{false, true} {
		path := "direct"
		if generated {
			path = "generated"
		}
		for _, event := range []string{"PreToolUse", "PostToolUse", "SessionStart", "SubagentStart", "SessionEnd"} {
			for _, outcome := range []string{"overflow", "unsupported", "empty EOF", "malformed EOF", "valid EOF"} {
				t.Run(path+"/"+event+"/"+outcome, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					cmd := hookInputCommand(t, ctx, bin, owner, event, generated)
					cmd.Dir, cmd.Env = cwd, env
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					payload := ""
					switch outcome {
					case "overflow":
						limit := 8 * 1024 * 1024
						if event == "SessionEnd" {
							limit = 64 * 1024
						}
						payload = strings.Repeat("x", limit+1)
					case "malformed EOF":
						payload = "{"
					case "valid EOF":
						payload = `{"tool_name":"mcp__capy__capy_stats","tool_input":{}}`
					}
					if outcome != "unsupported" {
						// Regular redirected files must retain support as well as
						// the pipe probes below. nil stdin supplies /dev/null.
						f, err := os.CreateTemp(t.TempDir(), "stdin")
						require.NoError(t, err)
						defer f.Close()
						_, err = io.WriteString(f, payload)
						require.NoError(t, err)
						_, err = f.Seek(0, io.SeekStart)
						require.NoError(t, err)
						cmd.Stdin = f
					}
					err := cmd.Run()
					require.NoError(t, ctx.Err(), "hung hook: %s", stderr.String())
					if outcome == "overflow" || outcome == "unsupported" {
						require.Error(t, err)
						want := 1
						if event == "PreToolUse" {
							want = 2
						}
						require.Equal(t, want, cmd.ProcessState.ExitCode(), stderr.String())
						require.Empty(t, stdout.String())
						require.Contains(t, stderr.String(), "Codex hook stdin")
						require.NotContains(t, stderr.String(), "Usage:")
						return
					}
					if outcome != "valid EOF" {
						if event == "PreToolUse" {
							require.NoError(t, err, stderr.String())
							require.Empty(t, stderr.String())
							var response struct {
								Output struct {
									Decision string `json:"permissionDecision"`
								} `json:"hookSpecificOutput"`
							}
							require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
							require.Equal(t, "deny", response.Output.Decision)
						} else {
							require.Error(t, err)
							require.NotEmpty(t, stderr.String())
							require.Empty(t, stdout.String())
						}
						return
					}
					require.NoError(t, err, stderr.String())
					require.Empty(t, stderr.String())
					if event == "SessionStart" || event == "SubagentStart" {
						require.True(t, json.Valid(stdout.Bytes()))
					} else {
						require.Empty(t, stdout.String())
					}
				})
			}
		}
	}
}

func hookInputCommand(t *testing.T, ctx context.Context, bin, owner, event string, generated bool) *exec.Cmd {
	t.Helper()
	var cmd *exec.Cmd
	if generated {
		cmd = exec.CommandContext(ctx, "bash", "-c", launchHookCommand(t, owner, event))
	} else {
		cmd = exec.CommandContext(ctx, bin, "hook", strings.ToLower(event), "--platform", "codex", "--project-dir", owner)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd
}

func TestCodexHookInputProductionDeadlines(t *testing.T) {
	bin := buildStdioCapy(t)
	owner := t.TempDir()
	_, env := installCodexLaunch(t, bin, owner)
	env = append(env, "GORACE=atexit_sleep_ms=0")
	cwd := filepath.Join(owner, "subdirectory")
	require.NoError(t, os.Mkdir(cwd, 0o755))
	for _, tc := range []struct {
		name, event, payload, reason string
		generated, trickle, eof      bool
		minimum, maximum             time.Duration
	}{
		{"direct empty idle", "PreToolUse", "", "idle", false, false, false, 1900 * time.Millisecond, 4 * time.Second},
		{"direct partial idle", "PreToolUse", "{", "idle", false, false, false, 1900 * time.Millisecond, 4 * time.Second},
		{"generated empty idle", "PreToolUse", "", "idle", true, false, false, 1900 * time.Millisecond, 4 * time.Second},
		{"generated valid open", "PreToolUse", `{}`, "idle", true, false, false, 1900 * time.Millisecond, 4 * time.Second},
		{"generated trickle", "PreToolUse", "{", "absolute", true, true, false, 9900 * time.Millisecond, 13 * time.Second},
		{"direct end idle", "SessionEnd", "", "idle", false, false, false, 90 * time.Millisecond, 2 * time.Second},
		{"generated end idle", "SessionEnd", "{", "idle", true, false, false, 90 * time.Millisecond, 2 * time.Second},
		{"generated end trickle", "SessionEnd", "{", "absolute", true, true, false, 240 * time.Millisecond, 2 * time.Second},
		{"generated prompt EOF", "PreToolUse", `{"tool_name":"mcp__capy__capy_stats","tool_input":{}}`, "", true, false, true, 0, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := hookInputCommand(t, ctx, bin, owner, tc.event, tc.generated)
			cmd.Dir, cmd.Env = cwd, env
			r, w, err := os.Pipe()
			require.NoError(t, err)
			defer r.Close()
			defer w.Close()
			cmd.Stdin = r
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			start := time.Now()
			require.NoError(t, cmd.Start())
			require.NoError(t, r.Close())
			done := make(chan struct{})
			var waitErr error
			go func() { waitErr = cmd.Wait(); close(done) }()
			defer func() { cancel(); <-done }()
			_, err = io.WriteString(w, tc.payload)
			require.NoError(t, err)
			if tc.eof {
				require.NoError(t, w.Close())
			}
			writerDone := make(chan struct{})
			go func() {
				defer close(writerDone)
				if !tc.trickle {
					return
				}
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-done:
						return
					case <-ticker.C:
						if _, err := w.Write([]byte(" ")); err != nil {
							return
						}
					}
				}
			}()
			<-done
			<-writerDone
			elapsed := time.Since(start)
			require.NoError(t, ctx.Err(), "hook needed harness kill: %s", stderr.String())
			require.GreaterOrEqual(t, elapsed, tc.minimum)
			require.Less(t, elapsed, tc.maximum)
			require.Empty(t, stdout.String())
			if tc.eof {
				require.NoError(t, waitErr, stderr.String())
				require.Empty(t, stderr.String())
			} else {
				require.Error(t, waitErr)
				code := 1
				if tc.event == "PreToolUse" {
					code = 2
				}
				require.Equal(t, code, cmd.ProcessState.ExitCode(), stderr.String())
				require.Contains(t, stderr.String(), tc.reason)
				require.NotContains(t, stderr.String(), "Usage:")
				// The writer was never closed to make the hook complete.
				_, err = w.Stat()
				require.NoError(t, err)
				_, err = w.Write([]byte("x"))
				require.ErrorIs(t, err, syscall.EPIPE, "a descendant still owns stdin")
			}
			t.Logf("%s: %s", tc.name, elapsed)
		})
	}
}
