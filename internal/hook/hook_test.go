package hook

import (
	"encoding/json"
	"testing"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAdapter is a minimal HookAdapter for testing that returns predictable JSON.
type testAdapter struct{}

func (a *testAdapter) ParsePreToolUse(input []byte) (*adapter.PreToolUseEvent, error) {
	var raw struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, err
	}
	return &adapter.PreToolUseEvent{
		ToolName:  raw.ToolName,
		ToolInput: raw.ToolInput,
	}, nil
}

func (a *testAdapter) FormatBlock(reason string) ([]byte, error) {
	return json.Marshal(map[string]any{"action": "deny", "reason": reason})
}

func (a *testAdapter) FormatAllow(guidance string) ([]byte, error) {
	if guidance == "" {
		return nil, nil
	}
	return json.Marshal(map[string]any{"action": "context", "additionalContext": guidance})
}

func (a *testAdapter) FormatModify(updatedInput map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{"action": "modify", "updatedInput": updatedInput})
}

func (a *testAdapter) FormatAsk() ([]byte, error) {
	return json.Marshal(map[string]any{"action": "ask"})
}

func (a *testAdapter) FormatSessionStart(ctx string) ([]byte, error) {
	return json.Marshal(map[string]any{"action": "sessionstart", "context": ctx})
}

func (a *testAdapter) Capabilities() adapter.PlatformCapabilities {
	return adapter.PlatformCapabilities{PreToolUse: true}
}

func makeInput(toolName string, toolInput map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"tool_name": toolName, "tool_input": toolInput})
	return b
}

func parseResult(t *testing.T, output []byte) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, json.Unmarshal(output, &result))
	return result
}

// ─── Bash routing tests ────────────────────────────────────────────────────────

func TestPreToolUse_CurlBlocked(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Bash", map[string]any{"command": "curl https://example.com"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "modify", result["action"])
	updated := result["updatedInput"].(map[string]any)
	assert.Contains(t, updated["command"], "fetch_and_index")
}

func TestPreToolUse_WgetBlocked(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Bash", map[string]any{"command": "wget -O - https://example.com"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "modify", result["action"])
}

func TestPreToolUse_CurlInQuotesAllowed(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Bash", map[string]any{"command": `gh issue edit --body "text with curl in it"`})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	// Should get guidance (first bash call), not a block
	if output != nil {
		result := parseResult(t, output)
		assert.NotEqual(t, "deny", result["action"])
	}
}

func TestPreToolUse_InlineHTTPBlocked(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Bash", map[string]any{"command": `python3 -c "import requests; requests.get('https://api.example.com')"`})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "modify", result["action"])
	updated := result["updatedInput"].(map[string]any)
	assert.Contains(t, updated["command"], "HTTP")
}

func TestPreToolUse_FetchBlocked(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Bash", map[string]any{"command": `node -e "fetch('https://api.example.com').then(r => r.json())"`})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "modify", result["action"])
}

func TestPreToolUse_BuildToolBlocked(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	for _, cmd := range []string{"./gradlew build", "gradle test", "mvn clean install", "./mvnw package"} {
		ResetGuidanceThrottle()
		input := makeInput("Bash", map[string]any{"command": cmd})
		output, err := handlePreToolUse(input, a, nil, "")
		require.NoError(t, err)
		require.NotNil(t, output, "expected modify for: %s", cmd)
		result := parseResult(t, output)
		assert.Equal(t, "modify", result["action"], "expected modify for: %s", cmd)
		updated := result["updatedInput"].(map[string]any)
		assert.Contains(t, updated["command"], "sandbox", "for: %s", cmd)
	}
}

func TestPreToolUse_BashGuidanceOnce(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}

	// First bash call: guidance
	input := makeInput("Bash", map[string]any{"command": "echo hello"})
	output1, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output1)
	result1 := parseResult(t, output1)
	assert.Equal(t, "context", result1["action"])

	// Second bash call: nil (throttled)
	output2, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output2)
}

// ─── WebFetch tests ────────────────────────────────────────────────────────────

func TestPreToolUse_WebFetchDenied(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("WebFetch", map[string]any{"url": "https://example.com"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "deny", result["action"])
	assert.Contains(t, result["reason"], "capy_fetch_and_index")
}

// ─── Read/Grep guidance tests ──────────────────────────────────────────────────

func TestPreToolUse_ReadGuidanceOnce(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}

	input := makeInput("Read", map[string]any{"file_path": "/tmp/test.txt"})
	output1, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output1)
	result := parseResult(t, output1)
	assert.Equal(t, "context", result["action"])
	assert.Contains(t, result["additionalContext"], "execute_file")

	// Second call: throttled
	output2, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output2)
}

func TestPreToolUse_GrepGuidanceOnce(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}

	input := makeInput("Grep", map[string]any{"pattern": "TODO"})
	output1, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output1)
	result := parseResult(t, output1)
	assert.Equal(t, "context", result["action"])
	assert.Contains(t, result["additionalContext"], "sandbox")

	output2, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output2)
}

// ─── Agent/Task routing tests ──────────────────────────────────────────────────

func TestPreToolUse_AgentRoutingBlockInjected(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Agent", map[string]any{"prompt": "Find bugs", "subagent_type": "general-purpose"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "modify", result["action"])
	updated := result["updatedInput"].(map[string]any)
	assert.Contains(t, updated["prompt"], "context_window_protection")
}

func TestPreToolUse_BashSubagentUpgraded(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("Agent", map[string]any{"prompt": "Run tests", "subagent_type": "Bash"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	updated := result["updatedInput"].(map[string]any)
	assert.Equal(t, "general-purpose", updated["subagent_type"])
}

// ─── Security check tests ──────────────────────────────────────────────────────

func TestPreToolUse_BashSecurityDeny(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(sudo *)"}},
	}
	input := makeInput("Bash", map[string]any{"command": "sudo rm -rf /"})
	output, err := handlePreToolUse(input, a, policies, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "deny", result["action"])
	assert.Contains(t, result["reason"], "security policy")
}

func TestPreToolUse_CapyExecuteSecurityDeny(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(rm -rf *)"}},
	}
	input := makeInput("capy_execute", map[string]any{"language": "shell", "code": "rm -rf /tmp"})
	output, err := handlePreToolUse(input, a, policies, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "deny", result["action"])
}

func TestPreToolUse_CapyExecuteNonShellAllowed(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(rm -rf *)"}},
	}
	input := makeInput("capy_execute", map[string]any{"language": "python", "code": "print('hello')"})
	output, err := handlePreToolUse(input, a, policies, "")
	require.NoError(t, err)
	assert.Nil(t, output)
}

// ─── Parse error / passthrough tests ───────────────────────────────────────────

func TestPreToolUse_ParseError_PassThrough(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	output, err := handlePreToolUse([]byte("invalid json{{{"), a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output, "parse error should pass through")
}

func TestPreToolUse_UnknownTool_PassThrough(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("SomeUnknownTool", map[string]any{})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	assert.Nil(t, output)
}

// ─── Tool alias tests ──────────────────────────────────────────────────────────

func TestPreToolUse_GeminiAlias(t *testing.T) {
	ResetGuidanceThrottle()
	a := &testAdapter{}
	input := makeInput("run_shell_command", map[string]any{"command": "curl https://example.com"})
	output, err := handlePreToolUse(input, a, nil, "")
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "modify", result["action"])
}

// ─── Helper tests ──────────────────────────────────────────────────────────────

func TestStripHeredocs(t *testing.T) {
	cmd := `cat <<EOF
some content with curl in it
EOF
echo done`
	stripped := stripHeredocs(cmd)
	assert.NotContains(t, stripped, "some content")
	assert.Contains(t, stripped, "echo done")
}

func TestStripQuotedContent(t *testing.T) {
	cmd := `gh issue edit --body "text with curl in it" --title 'another curl'`
	stripped := stripQuotedContent(cmd)
	assert.NotContains(t, stripped, "text with curl")
	assert.NotContains(t, stripped, "another curl")
}

func TestIsCurlOrWget(t *testing.T) {
	assert.True(t, isCurlOrWget("curl https://example.com"))
	assert.True(t, isCurlOrWget("wget -O file.txt https://example.com"))
	assert.True(t, isCurlOrWget("echo test && curl https://example.com"))
	assert.False(t, isCurlOrWget("echo curling_iron")) // substring, not command
	assert.False(t, isCurlOrWget("git log --oneline"))
}

func TestHasInlineHTTP(t *testing.T) {
	assert.True(t, hasInlineHTTP(`fetch('https://api.example.com')`))
	assert.True(t, hasInlineHTTP(`requests.get('https://api.com')`))
	assert.True(t, hasInlineHTTP(`http.get('http://localhost:3000')`))
	assert.False(t, hasInlineHTTP("echo hello world"))
}

func TestIsBuildTool(t *testing.T) {
	assert.True(t, isBuildTool("./gradlew build"))
	assert.True(t, isBuildTool("gradle test"))
	assert.True(t, isBuildTool("mvn clean install"))
	assert.True(t, isBuildTool("./mvnw package"))
	assert.False(t, isBuildTool("go build ./..."))
	assert.False(t, isBuildTool("npm run build"))
}

func TestIsCapyTool(t *testing.T) {
	assert.True(t, isCapyTool("capy_execute"))
	assert.True(t, isCapyTool("capy_batch_execute"))
	assert.True(t, isCapyTool("mcp__plugin__capy_execute"))
	assert.False(t, isCapyTool("Bash"))
	assert.False(t, isCapyTool("Read"))
}

func TestCanonicalToolName(t *testing.T) {
	assert.Equal(t, "Bash", canonicalToolName("run_shell_command"))
	assert.Equal(t, "Read", canonicalToolName("read_file"))
	assert.Equal(t, "Grep", canonicalToolName("grep_search"))
	assert.Equal(t, "WebFetch", canonicalToolName("web_fetch"))
	assert.Equal(t, "Bash", canonicalToolName("Bash")) // already canonical
	assert.Equal(t, "SomeOther", canonicalToolName("SomeOther"))
}

// ─── Session start test ────────────────────────────────────────────────────────

func TestSessionStart_RoutingBlock(t *testing.T) {
	a := &testAdapter{}
	output, err := handleSessionStart(nil, a)
	require.NoError(t, err)
	require.NotNil(t, output)
	result := parseResult(t, output)
	assert.Equal(t, "sessionstart", result["action"])
	assert.Contains(t, result["context"], "context_window_protection")
}
