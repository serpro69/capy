package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func capturedCLI(t *testing.T, bin, dir string, env []string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out, diagnostic bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostic
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "%v %s", err, diagnostic.String())
		code = exit.ExitCode()
	}
	require.LessOrEqual(t, out.Len()+diagnostic.Len(), 2048, "unbounded CLI response")
	return out.Bytes(), diagnostic.Bytes(), code
}
func cliRun(t *testing.T, bin, project string, env []string, code string, extra ...string) runstore.Result {
	t.Helper()
	args := []string{"execute", "--json", "--project-dir", project, "--code", code}
	args = append(args, extra...)
	out, diag, _ := capturedCLI(t, bin, project, env, args...)
	require.Empty(t, diag)
	var result runstore.Result
	require.NoError(t, json.Unmarshal(out, &result), string(out))
	return result
}
func cliProject(t *testing.T, extra string) string {
	t.Helper()
	project := stdioProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath='knowledge.db'\n[runs]\nmax_response_bytes=2048\n"+extra), 0600))
	return project
}
func TestRunCLICrossInterfaceRecovery(t *testing.T) {
	wantProof := capturedCleanup(t)
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	env := stdioEnv(t, "synthetic-cli-capture-key")
	result := cliRun(t, bin, project, env, "printf 'CLI-out\n'; printf 'CLI-err\n' >&2; printf x >> executed-once; exit 7")
	require.NotNil(t, result.RunID)
	require.Equal(t, "cli", result.OwnerType)
	require.Equal(t, 7, *result.ExitCode)
	require.Equal(t, 2, result.Version)
	require.Equal(t, wantProof, result.Cleanup)
	p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: env})
	p.initialize(t)
	require.Equal(t, []byte("CLI-out\n"), p.rawRun(t, *result.RunID, "stdout"))
	mcpRun := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf 'MCP-out\n'"})
	out, diag, status := capturedCLI(t, bin, project, env, "runs", "read", "--json", "--project-dir", project, "--run-id", *mcpRun.Structured.RunID, "--raw")
	require.Zero(t, status)
	require.Empty(t, diag)
	var page runstore.ReadPage
	require.NoError(t, json.Unmarshal(out, &page))
	require.Equal(t, 2, page.Version)
	require.Equal(t, wantProof, page.Run.Cleanup)
	require.Equal(t, []byte("MCP-out\n"), page.Fragments[0].Data)
	out, _, status = capturedCLI(t, bin, project, env, "runs", "status", "--json", "--project-dir", project, "--run-id", *result.RunID)
	require.Zero(t, status)
	var query runQueryResult
	require.NoError(t, json.Unmarshal(out, &query))
	require.Equal(t, 2, query.Version)
	require.Equal(t, wantProof, query.Run.Cleanup)
	require.NotContains(t, string(out), `"cleanup":null`)
	require.Equal(t, 7, *query.Run.ExitCode)
	out, _, status = capturedCLI(t, bin, project, env, "runs", "list", "--json", "--project-dir", project, "--limit", "64")
	require.Zero(t, status)
	var list runstore.ListPage
	require.NoError(t, json.Unmarshal(out, &list))
	require.Len(t, list.Entries, 2)
	require.Equal(t, 2, list.Version)
	for _, entry := range list.Entries {
		require.Equal(t, wantProof, entry.Cleanup)
	}
	var mcpList struct {
		IsError    bool              `json:"isError"`
		Structured runstore.ListPage `json:"structuredContent"`
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "list"}}, &mcpList))
	require.False(t, mcpList.IsError)
	require.Len(t, mcpList.Structured.Entries, 2)
	p.close(t)
	marker, err := os.ReadFile(filepath.Join(project, "executed-once"))
	require.NoError(t, err)
	require.Equal(t, "x", string(marker))
	require.NoFileExists(t, filepath.Join(project, "knowledge.db"))
}

func TestRunCLIExitAndCaptureMatrix(t *testing.T) {
	wantProof := capturedCleanup(t)
	bin := buildStdioCapy(t)
	for _, tc := range []struct {
		name, code, config string
		extra              []string
		exit               int
		state, capture     string
	}{
		{"success", "printf ok", "", nil, 0, "exited", "complete"},
		{"exit one", "printf ok; exit 1", "", nil, 1, "exited", "complete"},
		{"child 74", "exit 74", "", nil, 74, "exited", "complete"},
		{"capture loss", "printf 0123456789; printf x > finished", "max_run_bytes=4\n", nil, 74, "exited", "incomplete"},
		{"failed child with capture loss", "printf 0123456789; exit 7", "max_run_bytes=4\n", nil, 7, "exited", "incomplete"},
		{"signal", "kill -TERM $$", "", nil, 143, "exited", "complete"},
		{"deadline", "sleep 10", "", []string{"--timeout-ms", "1500"}, 124, "timed_out", "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := cliProject(t, tc.config)
			env := stdioEnv(t, "synthetic-cli-exit-key")
			args := append([]string{"execute", "--json", "--project-dir", project, "--code", tc.code}, tc.extra...)
			out, diag, status := capturedCLI(t, bin, project, env, args...)
			require.Equal(t, tc.exit, status, string(out))
			require.Empty(t, diag)
			var result runstore.Result
			require.NoError(t, json.Unmarshal(out, &result))
			require.Equal(t, tc.state, result.State)
			require.Equal(t, tc.capture, result.Capture.State)
			require.Equal(t, 2, result.Version)
			require.Equal(t, wantProof, result.Cleanup)
			require.True(t, result.Capture.TerminalPersisted)
			if tc.name == "capture loss" {
				require.FileExists(t, filepath.Join(project, "finished"))
				require.Equal(t, 0, *result.ExitCode)
			}
		})
	}
}

func TestRunCLIPolicyFilesAndInvalidFlags(t *testing.T) {
	capturedCleanup(t)
	bin := buildStdioCapy(t)
	for _, mode := range []string{"deny shell", "ask shell", "ask python", "deny file", "ask file", "outside file", "fifo", "both inputs", "background", "wait", "zero timeout", "fractional timeout", "long flag"} {
		t.Run(mode, func(t *testing.T) {
			project := cliProject(t, "")
			env := stdioEnv(t, "synthetic-cli-policy-key")
			program := filepath.Join(project, "program.sh")
			require.NoError(t, os.WriteFile(program, []byte("touch must-not-run"), 0600))
			args := []string{"execute", "--json", "--project-dir", project, "--code", "touch must-not-run"}
			var policy string
			switch mode {
			case "deny shell":
				policy = `{"permissions":{"deny":["Bash(touch:*)"]}}`
			case "ask shell":
				policy = `{"permissions":{"ask":["Bash(touch:*)"]}}`
			case "ask python":
				policy = `{"permissions":{"ask":["Bash(touch:*)"]}}`
				args = []string{"execute", "--json", "--project-dir", project, "--language", "python", "--code", `import os;os.system("touch must-not-run")`}
			case "deny file":
				policy = `{"permissions":{"deny":["Read(program.sh)"]}}`
				args = []string{"execute", "--json", "--project-dir", project, "--code-file", "program.sh"}
			case "ask file":
				policy = `{"permissions":{"ask":["Read(program.sh)"]}}`
				args = []string{"execute", "--json", "--project-dir", project, "--code-file", "program.sh"}
			case "outside file":
				external := filepath.Join(t.TempDir(), "program.sh")
				require.NoError(t, os.WriteFile(external, []byte("touch must-not-run"), 0600))
				args = []string{"execute", "--json", "--project-dir", project, "--code-file", external}
			case "fifo":
				fifo := filepath.Join(project, "pipe")
				require.NoError(t, syscall.Mkfifo(fifo, 0600))
				args = []string{"execute", "--json", "--project-dir", project, "--code-file", fifo}
			case "both inputs":
				args = append(args, "--code-file", program)
			case "background":
				args = append(args, "--background")
			case "wait":
				args = append(args, "--wait-ms", "1")
			case "zero timeout":
				args = append(args, "--timeout-ms", "0")
			case "fractional timeout":
				args = append(args, "--timeout-ms", "1.5")
			case "long flag":
				args = append(args, "--"+strings.Repeat("x", 60000))
			}
			policyPath := filepath.Join(project, ".claude", "settings.json")
			if policy != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(policyPath), 0700))
				require.NoError(t, os.WriteFile(policyPath, []byte(policy), 0600))
			}
			out, diag, status := capturedCLI(t, bin, project, env, args...)
			require.NotZero(t, status)
			require.Empty(t, diag)
			var result runstore.Result
			require.NoError(t, json.Unmarshal(out, &result), string(out))
			require.Nil(t, result.RunID)
			require.NoFileExists(t, filepath.Join(project, "must-not-run"))
			require.NoDirExists(t, filepath.Join(project, ".capy", "runs"))
			if strings.HasPrefix(mode, "ask") {
				require.Contains(t, result.Error.Message, "ask")
				require.Contains(t, result.Error.Message, policyPath)
			}
		})
	}
}

func TestRunCLICodeFileAndSpawnFailures(t *testing.T) {
	capturedCleanup(t)
	bin := buildStdioCapy(t)
	t.Run("accepted checked file", func(t *testing.T) {
		project := cliProject(t, "")
		env := stdioEnv(t, "synthetic-cli-file-key")
		require.NoError(t, os.WriteFile(filepath.Join(project, "program.sh"), []byte("printf 'file-output!\n'"), 0600))
		out, diag, status := capturedCLI(t, bin, project, env, "execute", "--json", "--project-dir", project, "--code-file", "program.sh")
		require.Zero(t, status, string(out))
		require.Empty(t, diag)
		var result runstore.Result
		require.NoError(t, json.Unmarshal(out, &result))
		require.Contains(t, result.Preview, "file-output!")
	})
	for _, mode := range []string{"missing runtime", "bad executable"} {
		t.Run(mode, func(t *testing.T) {
			project := cliProject(t, "")
			env := stdioEnv(t, "synthetic-cli-spawn-key")
			path := t.TempDir()
			expected := 127
			if mode == "bad executable" {
				expected = 126
				require.NoError(t, os.WriteFile(filepath.Join(path, "rustc"), []byte("invalid executable format"), 0700))
			}
			for i, v := range env {
				if strings.HasPrefix(v, "PATH=") {
					env[i] = "PATH=" + path
				}
			}
			out, diag, status := capturedCLI(t, bin, project, env, "execute", "--json", "--project-dir", project, "--language", "rust", "--code", "synthetic source")
			require.Equal(t, expected, status, string(out))
			require.Empty(t, diag)
			var result runstore.Result
			require.NoError(t, json.Unmarshal(out, &result))
			require.Equal(t, "start_failed", result.State)
			require.Nil(t, result.ExitCode)
		})
	}
}

func TestRunCLIPreservesProgramAndCwdBytes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux supervisor required")
	}
	bin := buildStdioCapy(t)
	project := filepath.Join(cliProject(t, ""), "owner-\xff")
	require.NoError(t, os.Mkdir(project, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[runs]\nmax_response_bytes=2048\n"), 0600))
	env := stdioEnv(t, "synthetic-cli-byte-request-key-long-enough")
	dir := filepath.Join(project, "work-\xff")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "identity"), []byte("correct-directory"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(project, "program.sh"), []byte("cat identity; printf '\xff'"), 0600))
	out, diag, status := capturedCLI(t, bin, project, env, "execute", "--json",
		"--project-dir", project, "--code-file", "program.sh", "--cwd", dir)
	require.Zero(t, status, string(out))
	require.Empty(t, diag)
	var result runstore.Result
	require.NoError(t, json.Unmarshal(out, &result))
	require.NotNil(t, result.RunID)
	out, diag, status = capturedCLI(t, bin, project, env, "runs", "read", "--json",
		"--project-dir", project, "--run-id", *result.RunID, "--stream", "stdout", "--raw")
	require.Zero(t, status, string(out))
	require.Empty(t, diag)
	var page runstore.ReadPage
	require.NoError(t, json.Unmarshal(out, &page))
	var raw []byte
	for _, f := range page.Fragments {
		raw = append(raw, f.Data...)
	}
	require.Equal(t, []byte("correct-directory\xff"), raw)
}

func TestRunCLINestedCaptureRetainsOuterPolicy(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, kind := range []string{"deny", "ask"} {
		t.Run(kind, func(t *testing.T) {
			project := cliProject(t, "")
			other := cliProject(t, "")
			env := stdioEnv(t, "synthetic-cli-nested-policy-key-long-enough")
			require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0700))
			source := filepath.Join(project, ".claude", "settings.json")
			require.NoError(t, os.WriteFile(source, []byte(`{"permissions":{"`+kind+`":["Bash(touch:*)"]}}`), 0600))
			code := "capy --project-dir '" + other + "' execute --code 'touch forbidden'"
			out, _, status := capturedCLI(t, bin, project, env, "execute", "--json", "--code", code)
			require.NotZero(t, status)
			var result runstore.Result
			require.NoError(t, json.Unmarshal(out, &result))
			require.Nil(t, result.RunID)
			require.Contains(t, result.Error.Message, source)
			require.Contains(t, result.Error.Message, "Bash(touch:*)")
			require.NoDirExists(t, filepath.Join(project, ".capy", "runs"))
			require.NoFileExists(t, filepath.Join(other, "forbidden"))
		})
	}
}

func TestRunCLINestedAndWorktreeOwners(t *testing.T) {
	capturedCleanup(t)
	bin := buildStdioCapy(t)
	root := cliProject(t, "")
	child := filepath.Join(root, "child")
	require.NoError(t, os.Mkdir(child, 0700))
	require.NoError(t, exec.Command("git", "init", "-q", root).Run())
	_, env := installCodexLaunch(t, bin, root)
	installCodexLaunch(t, bin, child)
	// Reuse isolated fixture credentials while retaining setup's PATH binding.
	for i, v := range env {
		if strings.HasPrefix(v, "CAPY_DB_KEY=") {
			env[i] = "CAPY_DB_KEY=synthetic-cli-owners-key"
		}
	}
	sub := filepath.Join(child, "sub")
	require.NoError(t, os.Mkdir(sub, 0700))
	out, _, status := capturedCLI(t, bin, sub, env, "execute", "--json", "--code", "printf nested!")
	require.Zero(t, status, string(out))
	require.FileExists(t, filepath.Join(child, ".capy", "runs", "runs.db"))
	require.NoDirExists(t, filepath.Join(root, ".capy", "runs"))
	commit := exec.Command("git", "-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture")
	// Use the fixture's HOME/config roots so host signing and hooks do not
	// affect the synthetic commit required to create a linked worktree.
	commit.Env = env
	output, err := commit.CombinedOutput()
	require.NoError(t, err, string(output))
	linked := filepath.Join(t.TempDir(), "linked")
	require.NoError(t, exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "linked-fixture", linked).Run())
	out, _, status = capturedCLI(t, bin, linked, env, "execute", "--json", "--code", "printf linked!")
	require.Zero(t, status, string(out))
	require.FileExists(t, filepath.Join(linked, ".capy", "runs", "runs.db"))
	require.NoDirExists(t, filepath.Join(root, ".capy", "runs"))
}

func TestRunCLIWorkspaceWriteSandbox(t *testing.T) {
	if os.Getenv("CAPY_CLI_SANDBOX") != "1" {
		t.Skip("set CAPY_CLI_SANDBOX=1 to run actual Linux mount confinement")
	}
	if runtime.GOOS != "linux" {
		t.Skip("Linux sandbox required")
	}
	bwrap, err := exec.LookPath("bwrap")
	require.NoError(t, err)
	bin := buildStdioCapy(t)
	owner := cliProject(t, "")
	excluded := cliProject(t, "")
	temp := filepath.Join(owner, "tmp")
	require.NoError(t, os.Mkdir(temp, 0700))
	env := stdioEnv(t, "synthetic-sandbox-cli-key")
	env = append(env, "TMPDIR="+temp, "XDG_CACHE_HOME="+filepath.Join(t.TempDir(), "unwritable-cache"))
	for _, selected := range []string{owner, excluded} {
		marker := filepath.Join(owner, "marker")
		if selected == excluded {
			marker = filepath.Join(owner, "must-not-spawn")
		}
		args := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--bind", owner, owner, "--unshare-net", "--die-with-parent", "--", bin, "execute", "--json", "--project-dir", selected, "--cwd", owner, "--code", "printf captured!; printf x > '" + marker + "'"}
		out, diag, status := capturedCLI(t, bwrap, owner, env, args...)
		require.Empty(t, diag, string(diag))
		var result runstore.Result
		require.NoError(t, json.Unmarshal(out, &result), string(out))
		if selected == owner {
			require.Zero(t, status, string(out))
			require.FileExists(t, marker)
			require.FileExists(t, filepath.Join(owner, ".capy", "runs", "runs.db"))
		} else {
			require.NotZero(t, status)
			require.Nil(t, result.RunID)
			require.NoFileExists(t, marker)
			require.NoDirExists(t, filepath.Join(excluded, ".capy", "runs"))
		}
	}
	// The same confinement really rejects home/cache writes; success above is not
	// an ordinary unrestricted subprocess or a chmod-only permissions simulation.
	probe := exec.Command(bwrap, "--ro-bind", "/", "/", "--dev", "/dev", "--bind", owner, owner, "--unshare-net", "--", "/bin/sh", "-c", `touch "$HOME/forbidden-home-write"`)
	probe.Env = env
	require.Error(t, probe.Run())
}

func TestRunCLIRemainsAttachedThroughSignal(t *testing.T) {
	capturedCleanup(t)
	bin := buildStdioCapy(t)
	project := cliProject(t, "")
	env := stdioEnv(t, "synthetic-cli-owner-signal-key")
	cmd := exec.Command(bin, "execute", "--json", "--project-dir", project, "--code", "printf ready > started; sleep 30; printf late > must-not-finish")
	cmd.Dir = project
	cmd.Env = env
	var out, diag bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diag
	require.NoError(t, cmd.Start())
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(project, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CLI child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		waited = true
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("CLI did not join cancelled supervisor")
	}
	require.Equal(t, 137, cmd.ProcessState.ExitCode())
	require.Empty(t, diag.String())
	require.LessOrEqual(t, out.Len(), 2048)
	var result runstore.Result
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	require.Equal(t, "cancelled", result.State)
	require.Equal(t, "cli", result.OwnerType)
	require.True(t, result.Capture.TerminalPersisted)
	require.NoFileExists(t, filepath.Join(project, "must-not-finish"))
}

func TestRunCLITextBudgetIncludesTerminalEscaping(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	zero := 0
	result := runstore.Result{Version: 1, Kind: "run", RunID: &id, State: "exited", ExitCode: &zero, Capture: runstore.Capture{State: "complete"}, Preview: strings.Repeat("\x00€", 20000)}
	cmd := &cobra.Command{}
	var out, diag bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diag)
	require.NoError(t, writeRunResponse(cmd, 2048, false, 0, func(n int) (any, error) { return execution.BoundResult(result, n), nil }))
	require.LessOrEqual(t, out.Len()+diag.Len(), 2048)
	require.True(t, utf8.Valid(out.Bytes()))
	require.NotContains(t, out.String(), "\x00")
	require.Contains(t, out.String(), id)
}
