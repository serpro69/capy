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

// Replay host payloads through setup's actual bootstrap, owner preflight,
// direct Go dispatch. No MCP execution, network request or host delivery is claimed.
func TestCodexChildObservationLifecycle(t *testing.T) {
	bin := buildStdioCapy(t)
	owner := stdioProject(t)
	_, env := installCodexLaunch(t, bin, owner) // deliberately credential-free
	cwd := filepath.Join(owner, "packages", "service")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	hooks := readInstalledCodexHooks(t, owner)
	invoke := func(event, session, agent, tool string, response any) map[string]any {
		t.Helper()
		payload := map[string]any{
			"hook_event_name": event, "session_id": session, "agent_id": agent,
			"agent_type": "worker", "cwd": cwd, "transcript_path": nil,
			"tool_name": tool, "tool_input": map[string]any{"command": "curl -X POST https://example.invalid"},
			"tool_response": response, "reason": "other", "source": "startup",
		}
		input, err := json.Marshal(payload)
		require.NoError(t, err)
		require.Len(t, hooks.Hooks[event], 1)
		command := hooks.Hooks[event][0].Hooks[0].Command
		code, output, stderr := runLaunch(t, cwd, env, []string{"-c", command}, string(input))
		require.Equal(t, 0, code, "%s", stderr)
		require.Empty(t, stderr)
		if output == "" {
			return nil
		}
		var parsed struct {
			Output map[string]any `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal([]byte(output), &parsed))
		return parsed.Output
	}
	startup := invoke("SubagentStart", "parent", "child", "", nil)
	require.Equal(t, "SubagentStart", startup["hookEventName"])
	require.LessOrEqual(t, len(startup["additionalContext"].(string)), 4096)
	pre := func(session, child string) map[string]any {
		return invoke("PreToolUse", session, child, "Bash", nil)
	}
	post := func(session, child, tool string, success bool) {
		require.Nil(t, invoke("PostToolUse", session, child, "mcp__another_server__"+tool, map[string]any{"isError": !success, "content": []any{}}))
	}
	require.NotContains(t, pre("parent", "child"), "permissionDecision")
	post("parent", "child", "capy_fetch_and_index", true)
	require.NotContains(t, pre("parent", "child"), "permissionDecision", "fetch alone cannot justify Bash HTTP")
	post("parent", "child", "capy_execute", true)
	require.NotContains(t, pre("parent", "sibling"), "permissionDecision")
	require.NotContains(t, pre("another-parent", "child"), "permissionDecision")
	redirect := pre("parent", "child")
	require.Equal(t, "deny", redirect["permissionDecision"])
	require.Contains(t, redirect["permissionDecisionReason"], "capy_execute")
	statePath := filepath.Join(owner, ".capy", "tool-observations.json")
	readEntries := func() []json.RawMessage {
		t.Helper()
		data, err := os.ReadFile(statePath)
		require.NoError(t, err)
		var state struct {
			Entries []json.RawMessage `json:"entries"`
		}
		require.NoError(t, json.Unmarshal(data, &state))
		return state.Entries
	}
	require.Empty(t, readEntries(), "both alternatives consumed before redirect")
	post("parent", "child", "capy_execute", false)
	require.NotContains(t, pre("parent", "child"), "permissionDecision", "failed retry allows native fallback")
	post("parent", "child", "capy_execute", true)
	post("parent", "sibling", "capy_execute", true)
	post("another-parent", "child", "capy_execute", true)
	// Same raw parent ID in Claude is a separate session, preserved by cleanup.
	claude := exec.Command(bin, "hook", "posttooluse", "--project-dir", owner)
	claude.Env, claude.Dir = env, cwd
	claude.Stdin = strings.NewReader(`{"session_id":"parent","agent_id":"child","tool_name":"mcp__capy__capy_execute","tool_response":{"isError":false}}`)
	out, err := claude.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.Len(t, readEntries(), 4)
	lockPath := filepath.Join(owner, ".capy", "tool-observations.lock")
	lockBefore, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.Nil(t, invoke("SessionEnd", "", "", "", nil))
	require.Len(t, readEntries(), 4, "unstable identity cannot clean up")
	require.Nil(t, invoke("SessionEnd", "parent", "", "", nil))
	require.Len(t, readEntries(), 2)
	require.NotContains(t, pre("parent", "sibling"), "permissionDecision")
	require.Equal(t, "deny", pre("another-parent", "child")["permissionDecision"])
	remaining := readEntries()
	require.Len(t, remaining, 1)
	require.Contains(t, string(remaining[0]), `"session":"parent"`, "Claude session remains")
	lockAfter, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(lockBefore, lockAfter), "permanent lock inode must survive cleanup")
	require.NoDirExists(t, filepath.Join(cwd, ".capy"))
	require.NoFileExists(t, filepath.Join(owner, "knowledge.db"))
	for _, entry := range env {
		if path, ok := strings.CutPrefix(entry, "CAPY_VAULT_PATH="); ok {
			require.NoFileExists(t, path)
		}
	}
}
