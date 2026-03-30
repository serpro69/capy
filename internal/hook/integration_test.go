package hook

import (
	"encoding/json"
	"testing"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── 16.2 Hook integration tests ──────────────────────────────────────────────
//
// These tests use the real ClaudeCodeAdapter (not a test stub) to verify that
// hook JSON is correctly parsed, routed, and formatted through the full pipeline.

func ccAdapter() adapter.HookAdapter {
	return &adapter.ClaudeCodeAdapter{}
}

func ccInput(toolName string, toolInput map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"tool_name":  toolName,
		"tool_input": toolInput,
		"session_id": "test-session-123",
	})
	return b
}

func ccParse(t *testing.T, output []byte) map[string]any {
	t.Helper()
	var raw map[string]any
	require.NoError(t, json.Unmarshal(output, &raw))
	hso, ok := raw["hookSpecificOutput"].(map[string]any)
	require.True(t, ok, "expected hookSpecificOutput wrapper in Claude Code format")
	return hso
}

// ─── Bash curl → modify (redirect to sandbox) ─────────────────────────────────

func TestHookIntegration_BashCurl_Modify(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("Bash", map[string]any{"command": "curl https://api.github.com/repos/anthropics/capy"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "allow", hso["permissionDecision"])
	assert.Equal(t, "Routed to capy sandbox", hso["permissionDecisionReason"])

	updated := hso["updatedInput"].(map[string]any)
	assert.Contains(t, updated["command"], "fetch_and_index")
}

// ─── Normal bash → context guidance (first call only) ──────────────────────────

func TestHookIntegration_NormalBash_Guidance(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("Bash", map[string]any{"command": "ls -la"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Nil(t, hso["permissionDecision"], "guidance should not have permission decision")
	assert.NotEmpty(t, hso["additionalContext"])
	assert.Contains(t, hso["additionalContext"], "capy")
}

func TestHookIntegration_NormalBash_ThrottledAfterFirst(t *testing.T) {
	dir := t.TempDir()
	ResetGuidanceFile(dir, "test-session-123")
	a := ccAdapter()

	// First call: guidance
	input := ccInput("Bash", map[string]any{"command": "echo first"})
	output1, err := handlePreToolUse(input, a, nil, dir)
	require.NoError(t, err)
	require.NotNil(t, output1)

	// Second call: nil (throttled)
	input2 := ccInput("Bash", map[string]any{"command": "echo second"})
	output2, err := handlePreToolUse(input2, a, nil, dir)
	require.NoError(t, err)
	assert.Nil(t, output2)
}

// ─── WebFetch → deny ───────────────────────────────────────────────────────────

func TestHookIntegration_WebFetch_Denied(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("WebFetch", map[string]any{"url": "https://docs.anthropic.com/api"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "deny", hso["permissionDecision"])
	reason := hso["permissionDecisionReason"].(string)
	assert.Contains(t, reason, "capy_fetch_and_index")
}

// ─── Read → context guidance ───────────────────────────────────────────────────

func TestHookIntegration_Read_Guidance(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("Read", map[string]any{"file_path": "/var/log/syslog"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Nil(t, hso["permissionDecision"])
	ctx := hso["additionalContext"].(string)
	assert.Contains(t, ctx, "execute_file")
}

// ─── Agent → routing block injected ────────────────────────────────────────────

func TestHookIntegration_Agent_RoutingBlockInjected(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("Agent", map[string]any{
		"prompt":       "Investigate the build failure",
		"subagent_type": "general-purpose",
	})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "allow", hso["permissionDecision"])

	updated := hso["updatedInput"].(map[string]any)
	prompt := updated["prompt"].(string)
	assert.Contains(t, prompt, "Investigate the build failure")
	assert.Contains(t, prompt, "context_window_protection")
}

// ─── Bash subagent → upgraded to general-purpose ───────────────────────────────

func TestHookIntegration_BashSubagent_Upgraded(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("Agent", map[string]any{
		"prompt":       "Run the test suite",
		"subagent_type": "Bash",
	})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	updated := hso["updatedInput"].(map[string]any)
	assert.Equal(t, "general-purpose", updated["subagent_type"])
}

// ─── Security policy enforcement via Claude Code adapter ───────────────────────

func TestHookIntegration_SecurityDeny_BashSudo(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(sudo *)"}},
	}
	input := ccInput("Bash", map[string]any{"command": "sudo apt install nmap"})
	output, err := handlePreToolUse(input, a, policies, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "deny", hso["permissionDecision"])
	assert.Contains(t, hso["permissionDecisionReason"], "security policy")
}

func TestHookIntegration_SecurityDeny_CapyExecuteShell(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(rm -rf *)"}},
	}
	input := ccInput("capy_execute", map[string]any{"language": "shell", "code": "rm -rf /tmp/data"})
	output, err := handlePreToolUse(input, a, policies, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "deny", hso["permissionDecision"])
}

func TestHookIntegration_SecurityAllow_CapyExecuteNonShell(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(rm -rf *)"}},
	}
	input := ccInput("capy_execute", map[string]any{"language": "python", "code": "print('hello')"})
	output, err := handlePreToolUse(input, a, policies, "")
	require.NoError(t, err)
	assert.Nil(t, output, "non-shell capy_execute should pass through with no output")
}

// ─── Tool alias routing ────────────────────────────────────────────────────────

func TestHookIntegration_GeminiAlias_CurlBlocked(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("run_shell_command", map[string]any{"command": "curl https://example.com"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "allow", hso["permissionDecision"])
	updated := hso["updatedInput"].(map[string]any)
	assert.Contains(t, updated["command"], "fetch_and_index")
}

// ─── Build tool routing ────────────────────────────────────────────────────────

func TestHookIntegration_GradleBuild_Redirected(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("Bash", map[string]any{"command": "./gradlew build"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "allow", hso["permissionDecision"])
	updated := hso["updatedInput"].(map[string]any)
	assert.Contains(t, updated["command"], "sandbox")
}

// ─── Session start ─────────────────────────────────────────────────────────────

func TestHookIntegration_SessionStart_RoutingBlock(t *testing.T) {
	a := ccAdapter()
	output, err := handleSessionStart(nil, a)
	require.NoError(t, err)
	require.NotNil(t, output)

	hso := ccParse(t, output)
	assert.Equal(t, "SessionStart", hso["hookEventName"])
	ctx := hso["additionalContext"].(string)
	assert.Contains(t, ctx, "context_window_protection")
}

// ─── Unknown tool passthrough ──────────────────────────────────────────────────

func TestHookIntegration_UnknownTool_Passthrough(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	input := ccInput("SomeCustomTool", map[string]any{"param": "value"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output, "unknown tools should pass through")
}

// ─── Invalid JSON passthrough ──────────────────────────────────────────────────

func TestHookIntegration_InvalidJSON_Passthrough(t *testing.T) {
	ResetGuidanceThrottle()
	a := ccAdapter()
	output, err := handlePreToolUse([]byte("not valid json{{{"), a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output, "invalid JSON should not block the tool")
}
