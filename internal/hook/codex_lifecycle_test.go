package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexSubagentStart(t *testing.T) {
	t.Parallel()
	for _, agentType := range []string{"worker", "explorer", "default", "custom", ""} {
		t.Run(agentType, func(t *testing.T) {
			project := t.TempDir()
			input, err := json.Marshal(map[string]any{
				"hook_event_name": "SubagentStart", "session_id": "parent", "agent_id": "child",
				"agent_type": agentType, "transcript_path": nil,
			})
			require.NoError(t, err)
			output, err := handleEvent("subagentstart", input, &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			response := ccParse(t, output)
			assert.Equal(t, "SubagentStart", response["hookEventName"])
			context, ok := response["additionalContext"].(string)
			require.True(t, ok)
			assert.LessOrEqual(t, len(context), 4096)
			for _, want := range []string{"Comprehension", "Extraction", "availability is unverified", "capy schemas once", "60 seconds", "native fallback", "Hosted web/search tools are not intercepted"} {
				assert.Contains(t, context, want)
			}
			assert.Len(t, response, 2, "only event and context; no argument/type/prompt mutation")
			assert.NoDirExists(t, filepath.Join(project, ".capy"))
		})
	}
	assert.Error(t, validateEvent("subagentstart", adapter.ClaudeCode), "Claude keeps Agent/Task injection")
}

func TestCodexObservationPayloads(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "must-not-use")
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		tool     string
		observed bool
		main     bool
	}{
		{name: "MCP success", observed: true},
		{name: "aliased fetch", tool: "mcp__another_server__capy_fetch_and_index", observed: true},
		{name: "serialized success", metadata: map[string]any{"tool_response": `{"isError":false,"content":[{"type":"text","text":"error is discussed here"}]}`}, observed: true},
		{name: "child transcript UUID is not parent", metadata: map[string]any{"transcript_path": "/rollout-a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"}, observed: true},
		{name: "root", metadata: map[string]any{"agent_id": ""}, main: true},
		{name: "type alone", metadata: map[string]any{"agent_id": "", "agent_type": "worker"}, main: true},
		{name: "no session", metadata: map[string]any{"session_id": "", "transcript_path": "/rollout-a1b2c3d4-e5f6-7890-abcd-ef1234567890.jsonl"}},
		{name: "MCP failure", metadata: map[string]any{"tool_response": map[string]any{"isError": true}}},
		{name: "serialized failure", metadata: map[string]any{"tool_response": `{"isError":true,"content":[]}`}},
		{name: "malformed serialized result", metadata: map[string]any{"tool_response": `{"isError":false`}},
		{name: "missing response", metadata: map[string]any{"tool_response": nil}},
		{name: "interrupted", metadata: map[string]any{"is_interrupt": true}},
		{name: "failure event", metadata: map[string]any{"hook_event_name": "PostToolUseFailure"}},
		{name: "execute file is not evidence", tool: "mcp__capy__capy_execute_file"},
		{name: "batch is not evidence", tool: "mcp__capy__capy_batch_execute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			metadata := map[string]any{
				"hook_event_name": "PostToolUse", "session_id": "parent", "agent_id": "child",
				"agent_type": "worker", "transcript_path": nil,
				"tool_response": map[string]any{"isError": false, "content": []any{}},
			}
			for key, value := range tc.metadata {
				metadata[key] = value
			}
			tool := tc.tool
			if tool == "" {
				tool = "mcp__capy__capy_execute"
			}
			input := hookInput(t, tool, map[string]any{}, metadata)
			output, err := handleEvent("posttooluse", input, &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			assert.Empty(t, output)
			if tc.main {
				assert.NoFileExists(t, filepath.Join(project, ".capy", "tool-observations.json"))
				assert.FileExists(t, filepath.Join(project, ".capy", "codex-main-observations.json"))
				return
			}
			if !tc.observed {
				assert.NoDirExists(t, filepath.Join(project, ".capy"))
				return
			}
			store := newObservationStore(project)
			state, err := readObservations(observationFile(store), time.Now().UnixMilli(), childObservationScope)
			require.NoError(t, err)
			require.Len(t, state.Entries, 1)
			assert.Equal(t, sessionIDComponent("codex:parent"), state.Entries[0].Session)
			assert.Equal(t, "child", state.Entries[0].Agent)
			assert.Contains(t, state.Entries[0].Tools, observationToolName(tool))
			// Failed retries must not renew existing evidence, either.
			before, err := os.ReadFile(observationFile(store))
			require.NoError(t, err)
			metadata["tool_response"] = `{"isError":true}`
			_, err = handleEvent("posttooluse", hookInput(t, tool, nil, metadata), &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			after, err := os.ReadFile(observationFile(store))
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestCodexNoAgentArgumentRewrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	for _, tool := range []string{"spawn_agent", "functions.spawn_agent", "collaboration.spawn_agent", "Agent", "Task"} {
		t.Run(tool, func(t *testing.T) {
			input := map[string]any{"prompt": "original child prompt", "message": "original message", "agent_type": "explorer", "subagent_type": "Bash"}
			before, err := json.Marshal(input)
			require.NoError(t, err)
			output, err := routePreToolUse(&adapter.PreToolUseEvent{ToolName: tool, ToolInput: input}, &adapter.CodexAdapter{}, nil, hookContext{projectDir: project})
			require.NoError(t, err)
			assert.Nil(t, output)
			after, err := json.Marshal(input)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
	assert.NoDirExists(t, filepath.Join(project, ".capy"))
}
