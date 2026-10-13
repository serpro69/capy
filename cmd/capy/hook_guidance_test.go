package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type guidanceCLIRequest struct{ platform, session, tool string }

func TestCodexGuidanceConcurrentCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	owner := stdioProject(t)
	_, env := installCodexLaunch(t, bin, owner)
	env = append(env, "GORACE=atexit_sleep_ms=0")
	cwd := filepath.Join(owner, "subdirectory")
	require.NoError(t, os.Mkdir(cwd, 0o755))
	command := launchHookCommand(t, owner, "PreToolUse")
	for _, mixed := range []bool{false, true} {
		name := "32 same Codex session"
		if mixed {
			name = "independent types sessions and platforms"
		}
		t.Run(name, func(t *testing.T) {
			requests := make([]guidanceCLIRequest, 32)
			pairs := []guidanceCLIRequest{{"codex", "single", "Bash"}}
			if mixed {
				pairs = []guidanceCLIRequest{
					{"codex", "alpha", "Bash"}, {"codex", "beta", "Bash"},
					{"claude-code", "alpha", "Bash"}, {"claude-code", "alpha", "Read"},
					{"claude-code", "alpha", "Grep"}, {"claude-code", "beta", "Bash"},
					{"claude-code", "beta", "Read"}, {"claude-code", "beta", "Grep"},
				}
			}
			for i := range requests {
				requests[i] = pairs[i%len(pairs)]
			}
			runGuidanceContenders(t, bin, owner, cwd, command, env, requests, true)
			// A second synchronized wave must find all claims, including every
			// independent type: no JSON read/modify/write update may be lost.
			runGuidanceContenders(t, bin, owner, cwd, command, env, requests, false)
			for _, pair := range pairs {
				component := pair.session
				if pair.platform == "codex" {
					component = fmt.Sprintf("sha256-%x", sha256.Sum256([]byte("codex:"+pair.session)))
				}
				path := filepath.Join(owner, ".capy", "guidance-"+component+"-"+strings.ToLower(pair.tool)+".once")
				info, err := os.Lstat(path)
				require.NoError(t, err)
				require.True(t, info.Mode().IsRegular())
				require.EqualValues(t, 0, info.Size())
				require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
				require.NoFileExists(t, filepath.Join(owner, ".capy", "guidance-"+component+".json"))
			}
		})
	}
	require.NoFileExists(t, filepath.Join(owner, "knowledge.db"))
	entries, err := os.ReadDir(filepath.Join(owner, ".capy"))
	require.NoError(t, err)
	require.Len(t, entries, 10, "nine claims plus setup's AGENTS.md, without observation or DB state")
}

func runGuidanceContenders(t *testing.T, bin, owner, cwd, command string, env []string, requests []guidanceCLIRequest, first bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gateR, gateW, err := os.Pipe()
	require.NoError(t, err)
	defer gateR.Close()
	defer gateW.Close()
	readyR, readyW, err := os.Pipe()
	require.NoError(t, err)
	defer readyR.Close()
	defer readyW.Close()
	type contender struct {
		cmd             *exec.Cmd
		out, diagnostic bytes.Buffer
		err             error
	}
	workers := make([]*contender, 0, len(requests))
	var waits sync.WaitGroup
	defer func() { cancel(); waits.Wait() }()
	for _, request := range requests {
		// Gate outside the hook so starting 32 processes cannot consume the
		// production stdin idle budget. Every contender signals readiness.
		args := []string{"-c", `printf . >&4; IFS= read -r release <&3; exec 3<&- 4>&-; exec "$@"`, "guidance-gate"}
		if request.platform == "codex" {
			args = append(args, "bash", "-c", command)
		} else {
			args = append(args, bin, "hook", "pretooluse", "--platform", request.platform, "--project-dir", owner)
		}
		w := &contender{cmd: exec.CommandContext(ctx, "bash", args...)}
		w.cmd.Dir, w.cmd.Env = cwd, env
		w.cmd.ExtraFiles = []*os.File{gateR, readyW}
		w.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		w.cmd.Cancel = func() error { return syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL) }
		w.cmd.WaitDelay = time.Second
		payload, err := json.Marshal(map[string]any{
			"hook_event_name": "PreToolUse", "session_id": request.session,
			"tool_name": request.tool, "tool_input": map[string]any{"command": "true"}, "cwd": cwd,
		})
		require.NoError(t, err)
		w.cmd.Stdin = bytes.NewReader(payload)
		w.cmd.Stdout, w.cmd.Stderr = &w.out, &w.diagnostic
		require.NoError(t, w.cmd.Start())
		workers = append(workers, w)
		waits.Add(1)
		go func() { defer waits.Done(); w.err = w.cmd.Wait() }()
	}
	require.NoError(t, gateR.Close())
	require.NoError(t, readyW.Close())
	require.NoError(t, readyR.SetReadDeadline(time.Now().Add(10*time.Second)))
	_, err = io.ReadFull(readyR, make([]byte, len(requests)))
	require.NoError(t, err, "all contenders must reach the barrier before release")
	_, err = gateW.Write(bytes.Repeat([]byte{'\n'}, len(requests)))
	require.NoError(t, err)
	require.NoError(t, gateW.Close())
	waits.Wait()
	require.NoError(t, ctx.Err(), "contenders must finish without watchdog termination")
	counts := make(map[guidanceCLIRequest]int)
	for i, w := range workers {
		require.NoError(t, w.err, w.diagnostic.String())
		require.Empty(t, w.diagnostic.String())
		pair := requests[i]
		counts[pair] += 0
		if w.out.Len() == 0 {
			continue
		}
		var result struct {
			Output map[string]any `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal(w.out.Bytes(), &result))
		require.NotContains(t, result.Output, "permissionDecision")
		require.NotEmpty(t, result.Output["additionalContext"])
		counts[pair]++
	}
	for pair, count := range counts {
		want := 0
		if first {
			want = 1
		}
		require.Equal(t, want, count, "%+v", pair)
	}
}
