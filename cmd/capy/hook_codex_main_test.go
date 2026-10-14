package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Replay complete synthetic host payloads through the installed commands. No
// HTTP request or actual host tool-delivery claim is made by this fixture.
func TestCodexMainObservationLifecycle(t *testing.T) {
	bin := buildStdioCapy(t)
	owner := stdioProject(t)
	git := exec.Command("git", "init", "-q", owner)
	git.Env = stdioEnv(t, "")
	output, err := git.CombinedOutput()
	require.NoError(t, err, "%s", output)
	_, env := installCodexLaunch(t, bin, owner)
	env = append(env, "GORACE=atexit_sleep_ms=0")
	cwd := filepath.Join(owner, "packages", "service")
	require.NoError(t, os.MkdirAll(cwd, 0o755))
	invoke := func(event, session, child, tool, command string, response any) map[string]any {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"hook_event_name": event, "session_id": session, "agent_id": child, "agent_type": "worker",
			"tool_name": tool, "tool_input": map[string]any{"command": command}, "tool_response": response,
			"cwd": cwd, "transcript_path": nil, "source": "startup", "reason": "other",
		})
		require.NoError(t, err)
		code, stdout, stderr := runLaunch(t, cwd, env, []string{"-c", launchHookCommand(t, owner, event)}, string(payload))
		require.Equal(t, 0, code, stderr)
		require.Empty(t, stderr)
		if stdout == "" {
			return nil
		}
		var result struct {
			Output map[string]any `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal([]byte(stdout), &result))
		return result.Output
	}
	pre := func(session, child, command string) map[string]any {
		t.Helper()
		return invoke("PreToolUse", session, child, "Bash", command, nil)
	}
	post := func(session, child, tool string, success bool) {
		t.Helper()
		require.Nil(t, invoke("PostToolUse", session, child, "mcp__alias__"+tool, "", map[string]any{"isError": !success, "content": []any{}}))
	}
	advice := func(response map[string]any) {
		t.Helper()
		require.NotContains(t, response, "permissionDecision")
		require.Contains(t, response["additionalContext"], "availability is unverified")
		require.Contains(t, response["additionalContext"], "capy schemas once")
	}
	redirect := func(response map[string]any) {
		t.Helper()
		require.Equal(t, "deny", response["permissionDecision"])
		require.Contains(t, response["permissionDecisionReason"], "Use capy_execute(language, code)")
		require.Contains(t, response["permissionDecisionReason"], "retry the native tool")
	}
	readEntries := func(scope string) []json.RawMessage {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(owner, ".capy", scope+"-observations.json"))
		require.NoError(t, err)
		var state struct {
			Entries []json.RawMessage `json:"entries"`
		}
		require.NoError(t, json.Unmarshal(data, &state))
		return state.Entries
	}
	for _, event := range []string{"SessionStart", "SubagentStart"} {
		result := invoke(event, "main", "", "", "", nil)
		context, ok := result["additionalContext"].(string)
		require.True(t, ok)
		require.LessOrEqual(t, len(context), 4096)
		require.Contains(t, context, "Main and child HTTP redirects each require their own capy_execute success")
	}
	const httpCommand = "curl https://example.invalid"
	advice(pre("main", "", httpCommand))
	mainPath := filepath.Join(owner, ".capy", "codex-main-observations.json")
	require.NoFileExists(t, mainPath)
	for _, tool := range []string{"capy_fetch_and_index", "capy_batch_execute"} {
		post("main", "", tool, true)
		advice(pre("main", "", httpCommand))
		require.NoFileExists(t, mainPath)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(owner, ".claude"), 0o755))
	settings := filepath.Join(owner, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(settings, []byte(`{"permissions":{"deny":["Bash(deny-marker:*)"],"ask":["Bash(ask-marker:*)"]}}`), 0o600))
	policyControls := func() {
		t.Helper()
		// Keep the argument distinct from macOS's /private temporary paths,
		// which legitimately appear in the settings-file provenance.
		const privateArgument = "private-command-argument"
		for _, marker := range []string{"deny-marker", "ask-marker"} {
			response := pre("main", "", marker+" "+privateArgument+"; "+httpCommand)
			require.Equal(t, "deny", response["permissionDecision"])
			require.Contains(t, response["permissionDecisionReason"], "Bash("+marker+":*)")
			require.Contains(t, response["permissionDecisionReason"], settings)
			require.NotContains(t, response["permissionDecisionReason"], privateArgument)
		}
	}
	policyControls()
	require.NoFileExists(t, mainPath)
	post("main", "", "capy_execute", true)
	require.Len(t, readEntries("codex-main"), 1)
	policyControls()
	advice(pre("main", "child", httpCommand))
	advice(pre("sibling", "", httpCommand))
	require.NotContains(t, pre("main", "", "curl -s -o file https://example.invalid"), "permissionDecision")
	redirect(pre("main", "", httpCommand))
	require.Empty(t, readEntries("codex-main"))
	advice(pre("main", "", httpCommand))
	post("main", "", "capy_execute", false)
	advice(pre("main", "", httpCommand))
	post("main", "", "capy_execute", true)
	redirect(pre("main", "", `node -e "fetch('https://example.invalid')"`))
	advice(pre("main", "", httpCommand))
	for _, session := range []string{"main", "sibling"} {
		post(session, "", "capy_execute", true)
		post(session, "child", "capy_execute", true)
	}
	lockMain, err := os.Stat(filepath.Join(owner, ".capy", "codex-main-observations.lock"))
	require.NoError(t, err)
	lockChild, err := os.Stat(filepath.Join(owner, ".capy", "tool-observations.lock"))
	require.NoError(t, err)
	invoke("SessionEnd", "main", "child", "", "", nil)
	require.Len(t, readEntries("codex-main"), 2)
	require.Len(t, readEntries("tool"), 2)
	invoke("SessionEnd", "main", "", "", "", nil)
	require.Len(t, readEntries("codex-main"), 1)
	require.Len(t, readEntries("tool"), 1)
	advice(pre("main", "", httpCommand))
	advice(pre("main", "child", httpCommand))
	redirect(pre("sibling", "", httpCommand))
	redirect(pre("sibling", "child", httpCommand))
	afterMain, err := os.Stat(filepath.Join(owner, ".capy", "codex-main-observations.lock"))
	require.NoError(t, err)
	afterChild, err := os.Stat(filepath.Join(owner, ".capy", "tool-observations.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(lockMain, afterMain))
	require.True(t, os.SameFile(lockChild, afterChild))
	require.NoDirExists(t, filepath.Join(cwd, ".capy"))
	require.NoFileExists(t, filepath.Join(owner, "knowledge.db"))
}
