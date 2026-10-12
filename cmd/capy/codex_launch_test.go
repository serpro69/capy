package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

func installCodexLaunch(t *testing.T, bin, dir string) ([]string, []string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	env := stdioEnv(t, "")
	for i := range env {
		if strings.HasPrefix(env[i], "PATH=") {
			env[i] = "PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	cmd := exec.Command(bin, "setup", "--platform", "codex", "--project-dir", dir)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	data, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
	require.NoError(t, err)
	var config struct {
		Servers map[string]struct {
			Args []string `toml:"args"`
		} `toml:"mcp_servers"`
	}
	require.NoError(t, toml.Unmarshal(data, &config))
	args := config.Servers["capy"].Args
	return args, env
}

func launchHookCommand(args []string, event string) string {
	hookArgs := append([]string(nil), args...)
	hookArgs[4], hookArgs[5] = "hook", event
	command := "bash"
	for _, arg := range hookArgs {
		command += " '" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
	}
	return command
}

func runLaunch(t *testing.T, cwd string, env, args []string, input string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Dir, cmd.Env, cmd.Stdin = cwd, env, strings.NewReader(input)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	require.NotEqual(t, context.DeadlineExceeded, ctx.Err(), "hung launch: %s", errOut.String())
	if err == nil {
		return 0, out.String(), errOut.String()
	}
	require.NotNil(t, cmd.ProcessState, "%v", err)
	return cmd.ProcessState.ExitCode(), out.String(), errOut.String()
}

func prepareLaunchLocation(t *testing.T, bin, location string) (string, string, []string, []string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "project with spaces")
	args, env := installCodexLaunch(t, bin, dir)
	git := func(cwd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = cwd, env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	cwd := dir
	switch location {
	case "subdirectory":
		cwd = filepath.Join(dir, "packages", "service")
		require.NoError(t, os.MkdirAll(cwd, 0o755))
		// A valid unrelated project layer must not become an owner.
		require.NoError(t, os.Mkdir(filepath.Join(cwd, ".codex"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(cwd, ".codex/config.toml"), []byte("model='test'\n"), 0o600))
	case "moved":
		moved := filepath.Join(base, "moved project")
		require.NoError(t, os.Rename(dir, moved))
		dir, cwd = moved, moved
	case "sibling worktree":
		git(dir, "init", "-q")
		git(dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
		worktree := filepath.Join(base, "linked tree")
		git(dir, "worktree", "add", "--detach", worktree, "HEAD")
		require.NoError(t, os.CopyFS(filepath.Join(worktree, ".codex"), os.DirFS(filepath.Join(dir, ".codex"))))
		dir, cwd = worktree, worktree
	case "root":
		git(dir, "init", "-q")
	case "non-Git": // Command resolution only, not host discovery.
	default:
		t.Fatal("unknown location")
	}
	return dir, cwd, args, env
}

func TestCodexLaunchLocations(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, location := range []string{"root", "subdirectory", "moved", "sibling worktree", "non-Git"} {
		t.Run(location, func(t *testing.T) {
			owner, cwd, args, env := prepareLaunchLocation(t, bin, location)
			require.NoError(t, os.MkdirAll(filepath.Join(owner, ".claude"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(owner, ".claude/settings.json"), []byte(`{"permissions":{"deny":["Bash(echo owner-denied)"]}}`), 0o600))
			input, err := json.Marshal(map[string]any{"session_id": "launch-fixture", "cwd": cwd, "tool_name": "Bash", "tool_input": map[string]string{"command": "echo owner-denied"}})
			require.NoError(t, err)
			code, out, stderr := runLaunch(t, cwd, env, []string{"-c", launchHookCommand(args, "pretooluse")}, string(input))
			require.Equal(t, 0, code, "%s", stderr)
			require.Contains(t, out, `"permissionDecision":"deny"`)
			require.Contains(t, out, filepath.Join(owner, ".claude/settings.json"))
			code, out, stderr = runLaunch(t, cwd, env, []string{"-c", launchHookCommand(args, "sessionstart")}, string(input))
			require.Equal(t, 0, code, "%s", stderr)
			require.Contains(t, out, "SessionStart")
			require.NoFileExists(t, filepath.Join(owner, ".capy/knowledge.db"))
		})
	}
}

func TestCodexMCPLaunchLocations(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, location := range []string{"root", "subdirectory", "moved", "sibling worktree", "non-Git"} {
		t.Run(location, func(t *testing.T) {
			owner, cwd, args, env := prepareLaunchLocation(t, bin, location)
			storage := filepath.Join(t.TempDir(), "knowledge.db")
			require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy.toml"), []byte("[store]\npath = '"+storage+"'\n"), 0o600))
			for i := range env {
				if strings.HasPrefix(env[i], "CAPY_DB_KEY=") {
					env[i] = "CAPY_DB_KEY=synthetic-codex-launch-key"
				}
			}
			p := startStdioProcess(t, stdioCommand{executable: "bash", args: args, dir: cwd, env: env})
			p.initialize(t)
			var result struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			require.NoError(t, p.request("tools/list", map[string]any{}, &result))
			require.Len(t, result.Tools, 10)
			installed := readInstalledCodexHooks(t, owner)
			matcher := regexp.MustCompile(installed.Hooks["PreToolUse"][0].Matcher)
			for _, tool := range result.Tools {
				require.True(t, matcher.MatchString("mcp__alternate_server__"+tool.Name), "unmatched registered tool: %s", tool.Name)
			}
			output := p.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "pwd"})
			require.Contains(t, output, owner)
			p.tool(t, "capy_index", map[string]any{"source": "launch-fixture", "content": "portable owner binding sentinel"})
			p.close(t)
			require.FileExists(t, storage)
			require.NoFileExists(t, filepath.Join(cwd, ".capy/knowledge.db"))
		})
	}
}

func TestCodexCopiedOwnerMissingMarker(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, retained := range []string{"both", "hook", "MCP", "escaped"} {
		t.Run(retained, func(t *testing.T) {
			root := t.TempDir()
			args, env := installCodexLaunch(t, bin, root)
			git := exec.Command("git", "init", "-q", root)
			require.NoError(t, git.Run())
			nested := filepath.Join(root, "packages/service")
			require.NoError(t, os.MkdirAll(nested, 0o755))
			require.NoError(t, os.CopyFS(filepath.Join(nested, ".codex"), os.DirFS(filepath.Join(root, ".codex"))))
			for _, dir := range []string{root, nested} {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex/scripts/capy.sh"), []byte("#!/bin/bash\necho invoked > '"+dir+"/wrapper-invoked'\nexit 99\n"), 0o755))
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ".capy"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy/tool-observations.json"), []byte("unchanged sentinel"), 0o600))
			}
			require.NoError(t, os.Remove(filepath.Join(root, ".codex/capy-owner")))
			if retained == "hook" {
				require.NoError(t, os.Remove(filepath.Join(root, ".codex/config.toml")))
			}
			if retained == "MCP" {
				require.NoError(t, os.Remove(filepath.Join(root, ".codex/hooks.json")))
			}
			if retained == "escaped" {
				for _, file := range []string{"hooks.json", "config.toml"} {
					path := filepath.Join(root, ".codex", file)
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "capy-codex-launch-v1", `\u0063apy-codex-launch-v1`)), 0o600))
				}
			}
			// Both copied commands have identical IDs/argv but distinct definitions.
			for _, origin := range []string{"outer", "nested"} {
				t.Run(origin, func(t *testing.T) {
					code, out, stderr := runLaunch(t, nested, env, []string{"-c", launchHookCommand(args, "pretooluse")}, `{"session_id":"fixture","tool_name":"Bash","tool_input":{"command":"echo sentinel"}}`)
					require.Equal(t, 2, code)
					require.Empty(t, out)
					require.Contains(t, stderr, root+"/.codex/")
					require.Contains(t, stderr, "restore")
					code, out, stderr = runLaunch(t, nested, env, args, "")
					require.NotZero(t, code)
					require.Empty(t, out)
					require.Contains(t, stderr, root+"/.codex/")
				})
			}
			for _, dir := range []string{root, nested} {
				require.NoFileExists(t, filepath.Join(dir, "wrapper-invoked"))
				require.NoFileExists(t, filepath.Join(dir, ".capy/knowledge.db"))
				data, err := os.ReadFile(filepath.Join(dir, ".capy/tool-observations.json"))
				require.NoError(t, err)
				require.Equal(t, "unchanged sentinel", string(data))
			}
		})
	}
}

func TestCodexBootstrapFailure(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, failure := range []string{"missing wrapper", "symlink wrapper", "wrapper crash", "outside tree", "invalid owner", "old binary", "binary crash", "missing binary"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			args, env := installCodexLaunch(t, bin, root)
			cwd := root
			wrapper := filepath.Join(root, ".codex/scripts/capy.sh")
			switch failure {
			case "missing wrapper":
				require.NoError(t, os.Remove(wrapper))
			case "symlink wrapper":
				require.NoError(t, os.Remove(wrapper))
				require.NoError(t, os.Symlink("missing", wrapper))
			case "wrapper crash":
				require.NoError(t, os.WriteFile(wrapper, []byte("exit 126\n"), 0o600))
			case "outside tree":
				cwd = t.TempDir()
			case "invalid owner":
				require.NoError(t, os.WriteFile(filepath.Join(root, ".codex/capy-owner"), []byte("bad"), 0o600))
			default:
				fakeBin := t.TempDir()
				body := "#!/bin/bash\nexit 1\n"
				if failure == "binary crash" {
					body = "#!/bin/bash\nkill -SEGV $$\n"
				}
				if failure != "missing binary" {
					require.NoError(t, os.WriteFile(filepath.Join(fakeBin, "capy"), []byte(body), 0o755))
				}
				bash, err := exec.LookPath("bash")
				require.NoError(t, err)
				require.NoError(t, os.Symlink(bash, filepath.Join(fakeBin, "bash")))
				for i := range env {
					if strings.HasPrefix(env[i], "PATH=") {
						env[i] = "PATH=" + fakeBin
					}
				}
			}
			code, out, stderr := runLaunch(t, cwd, env, []string{"-c", launchHookCommand(args, "pretooluse")}, "stdin sentinel")
			require.Equal(t, 2, code, "%s", stderr)
			require.Empty(t, out)
			require.Contains(t, stderr, "capy setup --platform codex")
		})
	}
}

func TestCodexLauncherStdinAndSignals(t *testing.T) {
	bin := buildStdioCapy(t)
	root := t.TempDir()
	args, env := installCodexLaunch(t, bin, root)
	wrapper := filepath.Join(root, ".codex/scripts/capy.sh")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/bash\ncat\nexit 42\n"), 0o600))
	code, out, _ := runLaunch(t, root, env, args, "event bytes\n\x00sentinel\n")
	require.Equal(t, 42, code)
	require.Equal(t, "event bytes\n\x00sentinel\n", out)
	// Long-running final process records its PID. Terminate the outer bootstrap,
	// then prove the final process has been reaped (not simply stdout closed).
	pidFile := filepath.Join(root, "server.pid")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/bash\necho $$ > '"+pidFile+"'\nexec sleep 120\n"), 0o600))
	cmd := exec.Command("bash", args...)
	cmd.Dir, cmd.Env = root, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { cmd.Process.Kill() })
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		_, err = fmt.Sscan(string(data), &pid)
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond)
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("launcher failed to stop")
	}
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond, "%s", stderr.String())
}

func TestCodexWrapperOpenFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrapper.sh")
	require.NoError(t, os.WriteFile(path, []byte("exit 0\n"), 0o600))
	err := inspectCodexWrapper(path, func(path string) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	})
	require.ErrorIs(t, err, os.ErrPermission)
	// Controlled disappearance after the readable-regular check: the supervisor
	// must observe bash's launch failure rather than replacing itself with exec.
	require.NoError(t, checkCodexWrapper(path))
	require.NoError(t, os.Remove(path))
	cmd := exec.Command("bash", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = superviseCodexProcess(context.Background(), cmd)
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit))
	require.Equal(t, 127, exit.ExitCode())
	require.NotEmpty(t, stderr.String())
}

func TestCodexWrapperExitCleansDescendants(t *testing.T) {
	for _, status := range []string{"0", "42"} {
		t.Run(status, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "descendant.pid")
			cmd := exec.Command("bash", "-c", `sleep 120 & echo $! > "$1"; exit "$2"`, "wrapper", pidFile, status)
			err := superviseCodexProcess(context.Background(), cmd)
			if status == "0" {
				require.NoError(t, err)
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, 42, exit.ExitCode())
			}
			data, err := os.ReadFile(pidFile)
			require.NoError(t, err)
			var pid int
			_, err = fmt.Sscan(string(data), &pid)
			require.NoError(t, err)
			t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
			require.Eventually(t, func() bool {
				if syscall.Kill(pid, 0) == syscall.ESRCH {
					return true
				}
				// A reparented grandchild can await the host init's reap after exit;
				// a zombie is no longer a running server or holder of inherited FDs.
				state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				return err == nil && strings.Contains(string(state), ") Z ")
			}, 3*time.Second, 10*time.Millisecond, "wrapper descendant remains running")
		})
	}
}

func TestCodexOverlappingLaunches(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, copied := range []bool{false, true} {
		t.Run(fmt.Sprint("copied=", copied), func(t *testing.T) {
			root, nestedSource := t.TempDir(), t.TempDir()
			outerArgs, env := installCodexLaunch(t, bin, root)
			nestedArgs, _ := installCodexLaunch(t, bin, nestedSource)
			nested := filepath.Join(root, "nested")
			require.NoError(t, os.Mkdir(nested, 0o755))
			if copied {
				nestedSource, nestedArgs = root, outerArgs
			}
			require.NoError(t, os.CopyFS(filepath.Join(nested, ".codex"), os.DirFS(filepath.Join(nestedSource, ".codex"))))
			for _, owner := range []string{root, nested} {
				require.NoError(t, os.MkdirAll(filepath.Join(owner, ".claude"), 0o755))
				policy := `{"permissions":{"deny":["Bash(echo ` + filepath.Base(owner) + `)"]}}`
				require.NoError(t, os.WriteFile(filepath.Join(owner, ".claude/settings.json"), []byte(policy), 0o600))
				require.NoError(t, os.MkdirAll(filepath.Join(owner, ".capy"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy/tool-observations.json"), []byte("unchanged"), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(owner, ".codex/scripts/capy.sh"), []byte("echo invoked > '"+owner+"/invoked'\nexit 0\n"), 0o600))
			}
			for _, removed := range []string{"none", "wrapper", "marker"} {
				if removed == "wrapper" {
					require.NoError(t, os.Remove(filepath.Join(root, ".codex/scripts/capy.sh")))
				}
				if removed == "marker" {
					require.NoError(t, os.Remove(filepath.Join(root, ".codex/capy-owner")))
				}
				for _, args := range [][]string{outerArgs, nestedArgs} {
					code, out, stderr := runLaunch(t, nested, env, []string{"-c", launchHookCommand(args, "pretooluse")}, "{}")
					require.Equal(t, 2, code, "%s", stderr)
					require.Empty(t, out)
					require.Contains(t, stderr, root)
					code, out, stderr = runLaunch(t, nested, env, args, "")
					require.NotZero(t, code)
					require.Empty(t, out)
					require.NotEmpty(t, stderr)
				}
			}
			for _, owner := range []string{root, nested} {
				require.NoFileExists(t, filepath.Join(owner, "invoked"))
				data, err := os.ReadFile(filepath.Join(owner, ".capy/tool-observations.json"))
				require.NoError(t, err)
				require.Equal(t, "unchanged", string(data))
				require.NoFileExists(t, filepath.Join(owner, ".capy/knowledge.db"))
			}
		})
	}
}
