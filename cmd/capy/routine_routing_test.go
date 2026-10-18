package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func generatedRoutineHook(t *testing.T, owner, cwd string, env []string, event, agent, tool string, args map[string]any, response any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": event, "session_id": "routine-session", "agent_id": agent,
		"cwd": cwd, "tool_name": tool, "tool_input": args, "tool_response": response,
	})
	require.NoError(t, err)
	code, out, stderr := runLaunch(t, cwd, env, []string{"-c", launchHookCommand(t, owner, event)}, string(payload))
	require.Equal(t, 0, code, "%s", stderr)
	require.Empty(t, stderr)
	if out == "" {
		return nil
	}
	var result struct {
		Output map[string]any `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	return result.Output
}

func TestRoutineGeneratedHooksAndMCPRecovery(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make unavailable")
	}
	bin := buildStdioCapy(t)
	owner := t.TempDir()
	args, hookEnv := installCodexLaunch(t, bin, owner)
	require.NoError(t, os.WriteFile(filepath.Join(owner, "Makefile"), []byte("test:\n\t@printf once >> count\n\t@printf 'captured test result\\n'\n"), 0600))
	for _, agent := range []string{"", "child"} {
		out := generatedRoutineHook(t, owner, owner, hookEnv, "PreToolUse", agent, "Bash", map[string]any{"command": "make test"}, nil)
		require.Equal(t, "deny", out["permissionDecision"])
		require.Contains(t, out["permissionDecisionReason"], "Nothing in this request ran")
	}
	require.NoFileExists(t, filepath.Join(owner, "count"))
	require.NoDirExists(t, filepath.Join(owner, ".capy", "runs"))
	serveEnv := append([]string(nil), hookEnv...)
	for i, entry := range serveEnv {
		if strings.HasPrefix(entry, "CAPY_DB_KEY=") {
			serveEnv[i] = "CAPY_DB_KEY=task15-synthetic-mcp-capture-key-long-enough"
		}
	}
	p := startStdioProcess(t, stdioCommand{executable: bin, args: args, dir: owner, env: serveEnv})
	p.initialize(t)
	var tools map[string]any
	require.NoError(t, p.request("tools/list", map[string]any{}, &tools))
	require.Len(t, tools["tools"], 11)
	var result map[string]any
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_execute", "arguments": map[string]any{"language": "shell", "code": "make test", "wait_ms": 30000}}, &result))
	require.NotEqual(t, true, result["isError"], "%+v", result)
	run := result["structuredContent"].(map[string]any)
	require.Equal(t, "exited", run["state"])
	require.EqualValues(t, 0, run["exit_code"])
	count, err := os.ReadFile(filepath.Join(owner, "count"))
	require.NoError(t, err)
	require.Equal(t, "once", string(count))
	for _, agent := range []string{"", "child"} {
		require.Nil(t, generatedRoutineHook(t, owner, owner, hookEnv, "PostToolUse", agent, "mcp__alias__capy_execute", nil, result))
		mixed := generatedRoutineHook(t, owner, owner, hookEnv, "PreToolUse", agent, "Bash", map[string]any{"command": "curl https://example.invalid; make test"}, nil)
		require.Equal(t, "deny", mixed["permissionDecision"])
		require.Contains(t, mixed["permissionDecisionReason"], "Routine command capture is required")
		http := generatedRoutineHook(t, owner, owner, hookEnv, "PreToolUse", agent, "Bash", map[string]any{"command": "curl https://example.invalid"}, nil)
		require.Equal(t, "deny", http["permissionDecision"])
		require.Contains(t, http["permissionDecisionReason"], "retry the native tool")
		var recovered map[string]any
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "read", "run_id": run["run_id"]}}, &recovered))
		require.NotEqual(t, true, recovered["isError"])
		encoded, err := json.Marshal(recovered)
		require.NoError(t, err)
		require.Contains(t, string(encoded), "captured test result")
		require.Nil(t, generatedRoutineHook(t, owner, owner, hookEnv, "PostToolUse", agent, "mcp__alias__capy_runs", map[string]any{"action": "read"}, recovered))
		http = generatedRoutineHook(t, owner, owner, hookEnv, "PreToolUse", agent, "Bash", map[string]any{"command": "curl https://example.invalid"}, nil)
		require.NotContains(t, http, "permissionDecision", "polling must not refresh consumed evidence")
	}
	p.close(t)
	count, err = os.ReadFile(filepath.Join(owner, "count"))
	require.NoError(t, err)
	require.Equal(t, "once", string(count), "recovery must not execute again")
}

func TestRoutineNestedOwnerPolicyAndCodeFile(t *testing.T) {
	bin := buildStdioCapy(t)
	parent := t.TempDir()
	_, env := installCodexLaunch(t, bin, parent)
	child := filepath.Join(parent, "child")
	_, childEnv := installCodexLaunch(t, bin, child)
	work := filepath.Join(child, "work")
	require.NoError(t, os.Mkdir(work, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(parent, ".capy.toml"), []byte("[routing.routine]\nenabled=false\n"), 0600))
	makeArgs := map[string]any{"command": "make test"}
	require.Nil(t, generatedRoutineHook(t, parent, work, env, "PreToolUse", "", "Bash", makeArgs, nil), "validated ancestor hook is a no-op")
	nearest := generatedRoutineHook(t, child, work, childEnv, "PreToolUse", "", "Bash", makeArgs, nil)
	require.Equal(t, "deny", nearest["permissionDecision"])
	require.Contains(t, nearest["permissionDecisionReason"], "Routine command capture is required")
	require.NoError(t, os.WriteFile(filepath.Join(parent, "program.sh"), []byte("echo parent"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(child, "program.sh"), []byte("guarded PRIVATE"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(child, ".claude"), 0700))
	source := filepath.Join(child, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(source, []byte(`{"permissions":{"ask":["Bash(guarded:*)"]}}`), 0600))
	response := generatedRoutineHook(t, child, work, childEnv, "PreToolUse", "", "Bash", map[string]any{"command": "capy execute --code-file program.sh"}, nil)
	require.Equal(t, "deny", response["permissionDecision"])
	require.Contains(t, response["permissionDecisionReason"], source)
	require.Contains(t, response["permissionDecisionReason"], "Bash(guarded:*)")
	require.NotContains(t, response["permissionDecisionReason"], "PRIVATE")
	require.NoError(t, os.WriteFile(filepath.Join(child, ".capy.toml"), []byte("[routing.routine]\nnative_allow=['Bash(make:*)']\n"), 0600))
	response = generatedRoutineHook(t, child, work, childEnv, "PreToolUse", "", "Bash", makeArgs, nil)
	require.NotContains(t, response, "permissionDecision")
	require.NoDirExists(t, filepath.Join(parent, ".capy", "runs"))
	require.NoDirExists(t, filepath.Join(child, ".capy", "runs"))
}
