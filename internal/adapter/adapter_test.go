package adapter

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── ParsePreToolUse tests ─────────────────────────────────────────────────────

func TestParsePreToolUse_Basic(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	input := `{"tool_name":"Bash","tool_input":{"command":"echo hello"},"session_id":"abc-123"}`
	event, err := a.ParsePreToolUse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "Bash", event.ToolName)
	assert.Equal(t, "echo hello", event.ToolInput["command"])
	assert.Equal(t, "abc-123", event.SessionID)
}

func TestParsePreToolUse_TranscriptPath(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	input := `{"tool_name":"Read","tool_input":{},"transcript_path":"/home/user/.claude/sessions/a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"}`
	event, err := a.ParsePreToolUse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890", event.SessionID)
}

func TestParsePreToolUse_InvalidJSON(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	_, err := a.ParsePreToolUse([]byte("not json"))
	assert.Error(t, err)
}

func TestParsePreToolUse_EmptyInput(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	event, err := a.ParsePreToolUse([]byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "", event.ToolName)
	assert.Nil(t, event.ToolInput)
}

func TestParsePreToolUse_ProjectDirFromEnv(t *testing.T) {
	t.Setenv("CLAUDE_PROJECT_DIR", "/test/project")
	a := &ClaudeCodeAdapter{}
	event, err := a.ParsePreToolUse([]byte(`{"tool_name":"Bash","tool_input":{}}`))
	require.NoError(t, err)
	assert.Equal(t, "/test/project", event.ProjectDir)
}

// ─── Session ID extraction tests ───────────────────────────────────────────────

func TestExtractSessionID_TranscriptPathUUID(t *testing.T) {
	// Highest priority: UUID from transcript path
	id := extractSessionID("fallback-id", "/path/to/a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl")
	assert.Equal(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890", id)
}

func TestExtractSessionID_SessionIDField(t *testing.T) {
	// Second priority: session_id field
	id := extractSessionID("session-from-field", "")
	assert.Equal(t, "session-from-field", id)
}

func TestExtractSessionID_EnvVar(t *testing.T) {
	// Third priority: CLAUDE_SESSION_ID env
	t.Setenv("CLAUDE_SESSION_ID", "env-session-id")
	id := extractSessionID("", "")
	assert.Equal(t, "env-session-id", id)
}

func TestExtractSessionID_PpidFallback(t *testing.T) {
	// Lowest priority: ppid fallback
	os.Unsetenv("CLAUDE_SESSION_ID")
	id := extractSessionID("", "")
	assert.Contains(t, id, "pid-")
}

func TestExtractSessionID_TranscriptPathNoUUID(t *testing.T) {
	// Transcript path without UUID falls through to session_id
	id := extractSessionID("fallback", "/path/to/notuuid.jsonl")
	assert.Equal(t, "fallback", id)
}

func TestExtractSessionID_Priority(t *testing.T) {
	// Transcript path UUID takes priority over session_id
	id := extractSessionID("session-id", "/path/a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl")
	assert.Equal(t, "a1b2c3d4-e5f6-7890-abcd-ef1234567890", id)
}

// ─── Format tests ──────────────────────────────────────────────────────────────

func parseJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	return result
}

func getHookOutput(t *testing.T, data []byte) map[string]any {
	t.Helper()
	result := parseJSON(t, data)
	hso, ok := result["hookSpecificOutput"].(map[string]any)
	require.True(t, ok, "expected hookSpecificOutput wrapper")
	return hso
}

func TestFormatBlock(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	data, err := a.FormatBlock("command blocked")
	require.NoError(t, err)

	hso := getHookOutput(t, data)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "deny", hso["permissionDecision"])
	assert.Equal(t, "command blocked", hso["permissionDecisionReason"])
}

func TestFormatAllow_WithGuidance(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	data, err := a.FormatAllow("use sandbox instead")
	require.NoError(t, err)
	require.NotNil(t, data)

	hso := getHookOutput(t, data)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "use sandbox instead", hso["additionalContext"])
	// Should not have permissionDecision for context-only responses
	assert.Nil(t, hso["permissionDecision"])
}

func TestFormatAllow_EmptyGuidance(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	data, err := a.FormatAllow("")
	require.NoError(t, err)
	assert.Nil(t, data, "empty guidance should return nil")
}

func TestFormatModify(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	updated := map[string]any{"command": "echo redirected"}
	data, err := a.FormatModify(updated)
	require.NoError(t, err)

	hso := getHookOutput(t, data)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "allow", hso["permissionDecision"])
	assert.Equal(t, "Routed to capy sandbox", hso["permissionDecisionReason"])
	ui := hso["updatedInput"].(map[string]any)
	assert.Equal(t, "echo redirected", ui["command"])
}

func TestFormatAsk(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	data, err := a.FormatAsk()
	require.NoError(t, err)

	hso := getHookOutput(t, data)
	assert.Equal(t, "PreToolUse", hso["hookEventName"])
	assert.Equal(t, "ask", hso["permissionDecision"])
}

func TestFormatSessionStart(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	data, err := a.FormatSessionStart("<routing>instructions</routing>")
	require.NoError(t, err)

	hso := getHookOutput(t, data)
	assert.Equal(t, "SessionStart", hso["hookEventName"])
	assert.Equal(t, "<routing>instructions</routing>", hso["additionalContext"])
}

// ─── Capabilities test ─────────────────────────────────────────────────────────

func TestCapabilities(t *testing.T) {
	a := &ClaudeCodeAdapter{}
	caps := a.Capabilities()
	assert.True(t, caps.PreToolUse)
	assert.True(t, caps.SessionStart)
	assert.True(t, caps.CanModifyArgs)
	assert.True(t, caps.CanInjectSessionContext)
}
