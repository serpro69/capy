package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func observationHook(t *testing.T, project, hook, tool string, args, metadata map[string]any) map[string]any {
	t.Helper()
	input := hookInput(t, tool, args, metadata)
	output, err := handleEvent(hook, input, ccAdapter(), &project)
	require.NoError(t, err)
	if output == nil {
		return nil
	}
	return ccParse(t, output)
}

func observeSuccess(t *testing.T, project, tool string) {
	t.Helper()
	result := observationHook(t, project, "posttooluse", tool, nil, map[string]any{
		"agent_id": "child", "hook_event_name": "PostToolUse",
		"tool_response": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}, "isError": false},
	})
	assert.Nil(t, result)
}

func TestObservedChildRoutingLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	native := func(tool string, args map[string]any) map[string]any {
		return observationHook(t, project, "pretooluse", tool, args, map[string]any{"agent_id": "child"})
	}
	bash := map[string]any{"command": "curl -X POST https://example.com"}
	assert.NotContains(t, native("Bash", bash), "permissionDecision", "first child call stays advisory")
	assert.NoDirExists(t, filepath.Join(project, ".capy"))
	observeSuccess(t, project, "mcp__capy__capy_fetch_and_index")
	assert.NotContains(t, native("Bash", bash), "permissionDecision", "fetch is not suitable for arbitrary HTTP")
	assert.NotContains(t, native("WebFetch", map[string]any{"url": "https://github.com/owner/repo/issues/1"}), "permissionDecision", "git comprehension must keep native advice")
	for _, url := range []string{"not a URL", "file:///tmp/file", "http://"} {
		assert.NotContains(t, native("WebFetch", map[string]any{"url": url}), "permissionDecision")
	}
	response := native("WebFetch", map[string]any{"url": "https://example.com"})
	assert.Equal(t, "deny", response["permissionDecision"])
	assert.Contains(t, response["permissionDecisionReason"], observedFetch)
	assert.NotContains(t, native("WebFetch", map[string]any{"url": "https://example.com"}), "permissionDecision")

	observeSuccess(t, project, "capy_execute")
	observeSuccess(t, project, "server/capy_fetch_and_index")
	response = native("Bash", bash)
	assert.Equal(t, "deny", response["permissionDecision"])
	assert.Contains(t, response["permissionDecisionReason"], observedExecute)
	assert.NotContains(t, response["permissionDecisionReason"], observedFetch)
	// The failed retry cannot renew either alternative consumed by the block.
	observationHook(t, project, "posttooluse", "capy_execute", nil, map[string]any{
		"agent_id": "child", "tool_response": map[string]any{"isError": true},
	})
	assert.NotContains(t, native("Bash", bash), "permissionDecision")
	assert.NotContains(t, native("WebFetch", map[string]any{"url": "https://example.com"}), "permissionDecision")

	observeSuccess(t, project, "capy_execute")
	response = native("Bash", map[string]any{"command": `node -e "fetch('https://example.com')"`})
	assert.Equal(t, "deny", response["permissionDecision"])
	observeSuccess(t, project, "capy_execute")
	response = native("WebFetch", map[string]any{"url": "https://example.com"})
	assert.Equal(t, "deny", response["permissionDecision"])
	assert.Contains(t, response["permissionDecisionReason"], observedExecute)
}

func TestObservationErrorsAndOtherToolsDoNotRenew(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	s := newObservationStore(project)
	old := time.Now().Add(-30 * time.Second)
	s.now = func() time.Time { return old }
	require.NoError(t, s.record("context-session", "child", observedExecute))
	before, err := os.ReadFile(observationFile(s))
	require.NoError(t, err)
	for _, metadata := range []map[string]any{
		{"is_error": true, "tool_response": "error"},
		{"is_interrupt": true, "tool_response": "interrupted"},
		{"hook_event_name": "PostToolUseFailure", "tool_response": "failure"},
		{"tool_response": map[string]any{"isError": true}},
		{"tool_response": map[string]any{"is_error": true}},
		{"tool_response": map[string]any{"canceled": true}},
		{"tool_response": map[string]any{"cancelled": true}},
		{"tool_response": map[string]any{"success": false}},
		{"tool_response": map[string]any{"error": "failed"}},
		{"tool_response": map[string]any{"isError": "false"}},
		{"tool_response": `{"isError":true,"content":[]}`},
		{"tool_response": []any{map[string]any{"is_error": true}}},
		{},
	} {
		metadata["agent_id"] = "child"
		observationHook(t, project, "posttooluse", "capy_execute", nil, metadata)
		after, err := os.ReadFile(observationFile(s))
		require.NoError(t, err)
		assert.Equal(t, before, after)
	}
	for _, tool := range []string{"capy_search", "capy_execute_file", "capy_batch_execute", "mcp__capy__capy_search", "notcapy_execute"} {
		observeSuccess(t, project, tool)
		after, err := os.ReadFile(observationFile(s))
		require.NoError(t, err)
		assert.Equal(t, before, after, "other tools must not renew execute")
	}
	observeSuccess(t, project, observedFetch)
	state, err := readObservations(observationFile(s), time.Now().UnixMilli())
	require.NoError(t, err)
	require.Len(t, state.Entries, 1)
	assert.Equal(t, old.UnixMilli(), state.Entries[0].Tools[observedExecute])
	assert.Greater(t, state.Entries[0].Tools[observedFetch], old.UnixMilli())
}

func TestObservationStableIdentityAndSecurity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_SESSION_ID", "")
	project := t.TempDir()
	for _, identity := range []map[string]any{
		{"session_id": "", "agent_id": "child"},
		{"session_id": "session", "agent_id": ""},
		{"session_id": "", "agent_type": "general-purpose"},
	} {
		identity["tool_response"] = map[string]any{"content": []any{}}
		observationHook(t, project, "posttooluse", observedExecute, nil, identity)
		assert.NoDirExists(t, filepath.Join(project, ".capy"))
	}
	observeSuccess(t, project, observedExecute)
	for _, identity := range []map[string]any{
		{"agent_id": "other-child"},
		{"agent_id": "child", "session_id": "other-session"},
	} {
		response := observationHook(t, project, "pretooluse", "Bash", map[string]any{"command": "curl https://example.com"}, identity)
		assert.NotContains(t, response, "permissionDecision")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
	settings := filepath.Join(project, ".claude", "settings.json")
	for _, decision := range []string{"deny", "ask"} {
		policy, err := json.Marshal(map[string]any{"permissions": map[string]any{decision: []string{"Bash(curl *)"}}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(settings, policy, 0o600))
		response := observationHook(t, project, "pretooluse", "Bash", map[string]any{"command": "curl https://example.com"}, map[string]any{"agent_id": "child"})
		assert.Equal(t, decision, response["permissionDecision"])
	}
	require.NoError(t, os.WriteFile(settings, []byte(`{}`), 0o600))
	response := observationHook(t, project, "pretooluse", "Bash", map[string]any{"command": "curl https://example.com"}, map[string]any{"agent_id": "child"})
	assert.Equal(t, "deny", response["permissionDecision"], "policy decisions must leave redirect evidence intact")
	assert.Contains(t, response["permissionDecisionReason"], observedExecute)
}

func TestObservationRoutingFallsBackOnStateFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	observeSuccess(t, project, observedExecute)
	s := newObservationStore(project)
	lock, err := lockObservations(filepath.Join(s.dir, "tool-observations.lock"))
	require.NoError(t, err)
	response := observationHook(t, project, "pretooluse", "Bash", map[string]any{"command": "curl https://example.com"}, map[string]any{"agent_id": "child"})
	assert.NotContains(t, response, "permissionDecision")
	require.NoError(t, lock.Close())
	require.NoError(t, os.WriteFile(observationFile(s), []byte(`{broken`), 0o600))
	response = observationHook(t, project, "pretooluse", "WebFetch", map[string]any{"url": "https://example.com"}, map[string]any{"agent_id": "child"})
	assert.NotContains(t, response, "permissionDecision")
}
