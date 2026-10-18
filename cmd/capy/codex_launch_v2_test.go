package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestCodexDirectDispatch(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, state := range []string{"absent", "sentinel", "symlink", "unreadable"} {
		t.Run(state, func(t *testing.T) {
			owner := t.TempDir()
			args, env := installCodexLaunch(t, bin, owner)
			wrapper := filepath.Join(owner, ".codex/scripts/capy.sh")
			require.NoFileExists(t, wrapper)
			if state != "absent" {
				require.NoError(t, os.MkdirAll(filepath.Dir(wrapper), 0o755))
				if state == "symlink" {
					require.NoError(t, os.Symlink("does-not-exist", wrapper))
				} else {
					require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\necho invoked > '"+owner+"/wrapper-invoked'\nexit 99\n"), 0o755))
					if state == "unreadable" {
						require.NoError(t, os.Chmod(wrapper, 0))
					}
				}
			}
			storage := filepath.Join(t.TempDir(), "knowledge.db")
			require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy.toml"), []byte("[store]\npath='"+storage+"'\n"), 0o600))
			require.NoError(t, os.MkdirAll(filepath.Join(owner, ".claude"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(owner, ".claude/settings.json"), []byte(`{"permissions":{"deny":["Bash(echo direct-denied)"]}}`), 0o600))
			for _, cwd := range []string{owner, filepath.Join(owner, "subdir")} {
				require.NoError(t, os.MkdirAll(cwd, 0o755))
				input := fmt.Sprintf(`{"session_id":"direct","cwd":%q,"tool_name":"Bash","tool_input":{"command":"echo direct-denied"}}`, cwd)
				code, out, stderr := runLaunch(t, cwd, env, []string{"-c", launchHookCommand(t, owner, "PreToolUse")}, input)
				require.Equal(t, 0, code, "%s", stderr)
				require.Contains(t, out, `"permissionDecision":"deny"`)
				require.Contains(t, out, owner+"/.claude/settings.json")
				require.NoFileExists(t, storage)
				// Byte-identical transport and explicit owner produce the same handler response.
				direct := exec.Command(bin, "hook", "--platform", "codex", "pretooluse", "--project-dir", owner)
				direct.Dir, direct.Env, direct.Stdin = cwd, env, strings.NewReader(input)
				expected, err := direct.Output()
				require.NoError(t, err)
				require.Equal(t, string(expected), out)
				serveEnv := append([]string(nil), env...)
				for i, entry := range serveEnv {
					if strings.HasPrefix(entry, "CAPY_DB_KEY=") {
						serveEnv[i] = "CAPY_DB_KEY=synthetic-direct-key"
					}
				}
				p := startStdioProcess(t, stdioCommand{executable: bin, args: args, dir: cwd, env: serveEnv})
				p.initialize(t)
				var listed map[string]any
				require.NoError(t, p.request("tools/list", map[string]any{}, &listed))
				require.Len(t, listed["tools"], 11)
				require.Contains(t, p.executeOutput(t, map[string]any{"language": "shell", "code": "pwd"}), owner)
				p.close(t)
				require.NoFileExists(t, filepath.Join(owner, "wrapper-invoked"))
			}
		})
	}
}

func TestCodexProtocolProbe(t *testing.T) {
	bin := buildStdioCapy(t)
	owner := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy.toml"), []byte("[broken"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(owner, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(owner, ".codex/capy-owner"), []byte("broken"), 0o600))
	for _, protocol := range []string{platform.CodexLaunchProtocol, "capy-codex-launch-v1", ""} {
		t.Run(protocol, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "codex-launch", "--check-protocol", protocol)
			cmd.Dir, cmd.Env = owner, stdioEnv(t, "")
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close()
			defer writer.Close() // stays open: the metadata probe must not read stdin
			cmd.Stdin = reader
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			err = cmd.Run()
			require.NoError(t, ctx.Err())
			if protocol == platform.CodexLaunchProtocol {
				require.NoError(t, err, "%s", stderr.String())
				require.Equal(t, platform.CodexLaunchProtocol+"\n", out.String())
				require.Empty(t, stderr.String())
			} else {
				require.Error(t, err)
				require.Empty(t, out.String())
				require.Contains(t, stderr.String(), "unsupported")
			}
			require.NoDirExists(t, filepath.Join(owner, ".capy"))
		})
	}
}

func TestCodexSetupPathPrerequisiteCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, target := range []string{"codex", "both", "claude-code", "db-repo"} {
		t.Run(target, func(t *testing.T) {
			owner, emptyPath := t.TempDir(), t.TempDir()
			env := stdioEnv(t, "")
			env[0] = "PATH=" + emptyPath
			args := []string{"setup", "--project-dir", owner}
			if target == "db-repo" {
				args = append(args, "--db-repo")
			} else {
				args = append(args, "--project")
				if target != "both" {
					args = append(args, "--platform", target)
				}
			}
			cmd := exec.Command(bin, args...)
			cmd.Dir, cmd.Env = owner, env
			out, err := cmd.CombinedOutput()
			if target == "codex" || target == "both" {
				require.Error(t, err)
				require.Contains(t, string(out), "PATH")
				entries, err := os.ReadDir(owner)
				require.NoError(t, err)
				require.Empty(t, entries, "no partial setup on prerequisite failure")
			} else {
				require.NoError(t, err, "%s", out)
				require.FileExists(t, filepath.Join(owner, ".gitignore"))
				require.NoDirExists(t, filepath.Join(owner, ".codex"))
			}
		})
	}
}

func TestCodexLaunchFailureAndCancellation(t *testing.T) {
	bin := buildStdioCapy(t)
	t.Run("serve initialization failure has clean stdout", func(t *testing.T) {
		owner := t.TempDir()
		args, env := installCodexLaunch(t, bin, owner)
		code, out, stderr := runLaunch(t, owner, env, args, "")
		require.Equal(t, 1, code)
		require.Empty(t, out)
		require.Contains(t, stderr, "credential")
		require.NoFileExists(t, filepath.Join(owner, ".capy/knowledge.db"))
	})
	for _, outcome := range []string{"EOF", "TERM", "INT", "HUP"} {
		t.Run("MCP "+outcome, func(t *testing.T) {
			owner := t.TempDir()
			args, env := installCodexLaunch(t, bin, owner)
			for i, entry := range env {
				if strings.HasPrefix(entry, "CAPY_DB_KEY=") {
					env[i] = "CAPY_DB_KEY=synthetic-lifecycle-key"
				}
			}
			p := startStdioProcess(t, stdioCommand{executable: bin, args: args, dir: owner, env: env})
			p.initialize(t)
			// An actual executor group, with a sleeping descendant, survives the call.
			result := p.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "echo $$ > executor.pid; sleep 120 & echo $! > descendant.pid; echo ready; wait", "timeout": 1000, "background": true})
			require.Contains(t, result, "running")
			pids := []int{readFixturePID(t, filepath.Join(owner, "executor.pid")), readFixturePID(t, filepath.Join(owner, "descendant.pid"))}
			for _, pid := range pids {
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			}
			if outcome == "EOF" {
				p.close(t)
			} else {
				sig := map[string]syscall.Signal{"TERM": syscall.SIGTERM, "INT": syscall.SIGINT, "HUP": syscall.SIGHUP}[outcome]
				require.NoError(t, p.cmd.Process.Signal(sig))
				select {
				case <-p.waited:
				case <-time.After(5 * time.Second):
					t.Fatal("MCP did not stop after signal")
				}
				require.NoError(t, p.waitErr, "%s", p.diagnostics())
			}
			for _, pid := range pids {
				require.Eventually(t, func() bool { return processStopped(pid) }, 3*time.Second, 10*time.Millisecond, "executor descendant %d remains running", pid)
			}
		})
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run("hook "+sig.String(), func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skip("process topology and readiness use Linux /proc")
			}
			owner := t.TempDir()
			_, env := installCodexLaunch(t, bin, owner)
			cmd := exec.Command("bash", "-c", launchHookCommand(t, owner, "PreToolUse"))
			cmd.Dir, cmd.Env = owner, env
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close()
			defer writer.Close()
			cmd.Stdin = reader
			var stderr stdioLog
			cmd.Stderr = &stderr
			require.NoError(t, cmd.Start())
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			var pid int
			require.Eventually(t, func() bool {
				children, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
				if err != nil || len(strings.Fields(string(children))) != 1 {
					return false
				}
				pid, err = strconv.Atoi(strings.Fields(string(children))[0])
				if err != nil {
					return false
				}
				exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
				if err != nil || exe != bin {
					return false
				}
				status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
				if err != nil {
					return false
				}
				for _, line := range strings.Split(string(status), "\n") {
					if strings.HasPrefix(line, "SigCgt:") {
						mask, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "SigCgt:")), 16, 64)
						return err == nil && mask&(1<<(sig-1)) != 0
					}
				}
				return false
			}, 5*time.Second, 10*time.Millisecond, "expected exactly one ready Go child: %s", stderr.String())
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			require.NoError(t, cmd.Process.Signal(sig))
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("hook supervisor did not reap Go child")
			}
			require.Equal(t, 128+int(sig), cmd.ProcessState.ExitCode(), "%s", stderr.String())
			require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, time.Second, 10*time.Millisecond)
		})
	}
}

func TestCodexMCPClosedOutputCleanup(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, trigger := range []string{"EOF", "signal", "response write"} {
		t.Run(trigger, func(t *testing.T) {
			owner := t.TempDir()
			args, env := installCodexLaunch(t, bin, owner)
			env = replaceWrapperEnv(env, "CAPY_DB_KEY", "synthetic-closed-output-key")
			p := startStdioProcess(t, stdioCommand{executable: bin, args: args, dir: owner, env: env})
			p.initialize(t)
			p.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "echo $$ > detached.pid; echo detached; sleep 120", "timeout": 1000, "background": true})
			detached := readFixturePID(t, filepath.Join(owner, "detached.pid"))
			t.Cleanup(func() { _ = syscall.Kill(-detached, syscall.SIGKILL) })
			require.NoError(t, p.write(map[string]any{
				"jsonrpc": "2.0", "id": 100, "method": "tools/call",
				"params": map[string]any{"name": "capy_execute", "arguments": map[string]any{"language": "shell", "code": "echo $$ > active.pid; sleep 120", "timeout": 60000}},
			}, time.Now().Add(time.Second)))
			require.Eventually(t, func() bool {
				info, err := os.Stat(filepath.Join(owner, "active.pid"))
				return err == nil && info.Size() > 0
			}, 3*time.Second, 10*time.Millisecond)
			active := readFixturePID(t, filepath.Join(owner, "active.pid"))
			t.Cleanup(func() { _ = syscall.Kill(-active, syscall.SIGKILL) })
			require.NoError(t, p.stdout.Close())
			switch trigger {
			case "EOF":
				require.NoError(t, p.stdin.Close())
			case "signal":
				require.NoError(t, p.cmd.Process.Signal(syscall.SIGTERM))
			case "response write":
				require.NoError(t, p.write(map[string]any{"jsonrpc": "2.0", "id": 101, "method": "tools/list", "params": map[string]any{}}, time.Now().Add(time.Second)))
			}
			select {
			case <-p.waited:
			case <-time.After(5 * time.Second):
				t.Fatal("closed output did not cancel active workers")
			}
			for _, pid := range []int{detached, active} {
				require.Eventually(t, func() bool { return processStopped(pid) }, 3*time.Second, 10*time.Millisecond, "process %d remains after exit %v", pid, p.waitErr)
			}
			require.Equal(t, 1, p.cmd.ProcessState.ExitCode(), "output failure must be diagnostic, not fatal SIGPIPE: %s", p.diagnostics())
			require.Contains(t, p.diagnostics(), "broken pipe")
		})
	}
}

func readFixturePID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	require.Positive(t, pid)
	return pid
}

func processStopped(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	// An exited reparented descendant may await init's reap; it holds no live FDs.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && strings.Contains(string(data), ") Z ")
}
