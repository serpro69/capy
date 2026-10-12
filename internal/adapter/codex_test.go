package adapter

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexAdapterIdentity(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "wrong-environment-session")
	for _, tc := range []struct {
		name, session, agent, agentType string
		transcript                      any
	}{
		{name: "root", session: "parent", transcript: "/a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"},
		{name: "child", session: "parent", agent: "child", agentType: "worker", transcript: "/child.jsonl"},
		{name: "type alone", session: "parent", agentType: "worker"},
		{name: "nullable transcript", session: "parent"},
		{name: "no stable identity", agent: "child", transcript: "/a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{
				"session_id": tc.session, "agent_id": tc.agent, "agent_type": tc.agentType,
				"transcript_path": tc.transcript, "cwd": "/project",
				"tool_name": "Bash", "tool_input": map[string]any{"command": "printf marker"},
			})
			require.NoError(t, err)
			event, err := (&CodexAdapter{}).ParsePreToolUse(input)
			require.NoError(t, err)
			wantSession := ""
			if tc.session != "" {
				wantSession = "codex:" + tc.session
			}
			assert.Equal(t, wantSession, event.SessionID)
			assert.Equal(t, tc.session != "", event.SessionIDStable)
			assert.Equal(t, tc.agent, event.AgentID)
			assert.Equal(t, tc.agentType, event.AgentType)
			assert.Equal(t, "/project", event.Cwd)
			assert.False(t, event.InvalidCwd)
			assert.Equal(t, "printf marker", event.ToolInput["command"])
		})
	}
}

func TestCodexAdapterResponses(t *testing.T) {
	t.Parallel()
	a := &CodexAdapter{}
	for _, tc := range []struct {
		name string
		call func() ([]byte, error)
		want string
	}{
		{"deny", func() ([]byte, error) { return a.FormatBlock("blocked") }, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"blocked"}}`},
		{"ask denied", func() ([]byte, error) { return a.FormatAsk("Matched approval pattern") }, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Matched approval pattern; this rule requires approval, which capy's Codex PreToolUse hook cannot request"}}`},
		{"guidance", func() ([]byte, error) { return a.FormatAllow("guidance") }, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","additionalContext":"guidance"}}`},
		{"startup", func() ([]byte, error) { return a.FormatSessionStart("routing") }, `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"routing"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := tc.call()
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(output))
		})
	}
	output, err := a.FormatAllow("")
	require.NoError(t, err)
	assert.Nil(t, output)
	output, err = a.FormatModify(map[string]any{"command": "printf marker"})
	require.ErrorContains(t, err, "deliberately does not implement input rewriting")
	assert.Nil(t, output)
	assert.Equal(t, Codex, a.Platform())
	assert.False(t, a.Capabilities().CanModifyArgs)
	assert.False(t, a.Capabilities().CanModifyOutput)
}

func TestCodexAdapterMalformedMetadata(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`null`, `[]`, `42`, `{bad`, `{"session_id":42}`, `{"tool_input":[]}`, `{"agent_id":false}`} {
		t.Run(input, func(t *testing.T) {
			_, err := (&CodexAdapter{}).ParsePreToolUse([]byte(input))
			require.Error(t, err)
		})
	}
	for _, cwd := range []string{`null`, `42`, `false`, `[]`, `{}`, `""`} {
		t.Run("cwd="+cwd, func(t *testing.T) {
			event, err := (&CodexAdapter{}).ParsePreToolUse([]byte(`{"cwd":` + cwd + `}`))
			require.NoError(t, err)
			assert.True(t, event.InvalidCwd)
		})
	}
}
