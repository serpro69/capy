package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookObservationsAcrossProcesses(t *testing.T) {
	bin := buildStdioCapy(t)
	project, home, dataHome := t.TempDir(), t.TempDir(), t.TempDir()
	run := func(event, tool, session, agent string, args map[string]any, result any) map[string]any {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"tool_name": tool, "tool_input": args, "tool_response": result,
			"session_id": session, "agent_id": agent,
		})
		require.NoError(t, err)
		cmd := exec.Command(bin, "hook", event, "--project-dir", project)
		cmd.Dir = project
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+home,
			"XDG_DATA_HOME="+dataHome, "CLAUDE_PROJECT_DIR=", "CLAUDE_SESSION_ID=", "CAPY_DB_KEY=", "CAPY_VAULT_KEY=")
		cmd.Stdin = bytes.NewReader(payload)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		require.NoError(t, cmd.Run(), stderr.String())
		assert.Empty(t, stderr.String())
		if stdout.Len() == 0 {
			return nil
		}
		var response struct {
			Output map[string]any `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
		return response.Output
	}
	args := map[string]any{"command": "curl https://example.com"}
	ok := map[string]any{"content": []any{map[string]any{"type": "text", "text": "success marker not retained"}}}
	run("posttooluse", "capy_execute", "", "child", nil, ok)
	run("posttooluse", "capy_execute", "session", "", nil, ok)
	assert.NoDirExists(t, filepath.Join(project, ".capy"), "PID fallback and main-agent calls do not persist observations")
	assert.NotContains(t, run("pretooluse", "Bash", "session", "child", args, nil), "permissionDecision")
	run("posttooluse", "mcp__capy__capy_execute", "session", "child", nil, ok)
	assert.Equal(t, "deny", run("pretooluse", "Bash", "session", "child", args, nil)["permissionDecision"])
	run("posttooluse", "mcp__capy__capy_execute", "session", "child", nil, map[string]any{"isError": true})
	assert.NotContains(t, run("pretooluse", "Bash", "session", "child", args, nil), "permissionDecision", "failed retry leaves native fallback")

	run("posttooluse", "capy_execute", "session", "child", nil, ok)
	run("posttooluse", "capy_execute", "live-session", "sibling", nil, ok)
	run("sessionend", "", "session", "", nil, nil)
	assert.NotContains(t, run("pretooluse", "Bash", "session", "child", args, nil), "permissionDecision")
	assert.Equal(t, "deny", run("pretooluse", "Bash", "live-session", "sibling", args, nil)["permissionDecision"])
	files, err := os.ReadDir(filepath.Join(project, ".capy"))
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, "tool-observations.json", files[0].Name())
	assert.Equal(t, "tool-observations.lock", files[1].Name())
	data, err := os.ReadFile(filepath.Join(project, ".capy", "tool-observations.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "success marker not retained")
	assert.NoDirExists(t, filepath.Join(dataHome, "capy"), "hooks must not open knowledge/vault databases")
}
