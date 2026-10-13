package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/adapter"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexMalformedPreToolUse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, input := range []string{
		`{bad`, `null`, `[]`, `{}`, `{"tool_name":"Bash"}`,
		`{"tool_name":"Bash","tool_input":null}`, `{"tool_name":"Bash","tool_input":[]}`,
		`{"tool_name":"Bash","tool_input":{}}`, `{"tool_name":"Bash","tool_input":{"cmd":"printf marker"}}`,
		`{"tool_name":"Bash","tool_input":{"command":42}}`, `{"tool_name":"Bash","tool_input":{"command":null}}`,
		`{"tool_name":"Bash","tool_input":{"command":" "}}`, `{"tool_name":42,"tool_input":{}}`,
	} {
		t.Run(input, func(t *testing.T) {
			project := t.TempDir()
			output, err := handleEvent("pretooluse", []byte(input), &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			response := ccParse(t, output)
			assert.Equal(t, "deny", response["permissionDecision"])
			assert.Contains(t, response["permissionDecisionReason"], "invalid Codex")
			assert.NoDirExists(t, filepath.Join(project, ".capy"))
		})
	}
	project := t.TempDir()
	output, err := handleEvent("pretooluse", []byte(`{bad`), ccAdapter(), &project)
	require.NoError(t, err)
	assert.Nil(t, output, "retain Claude malformed-input passthrough")
}

func TestCodexPolicyResponses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(project, ".claude"), 0o755))
	source := filepath.Join(project, ".claude", "settings.json")
	require.NoError(t, os.WriteFile(source, []byte(`{"permissions":{"ask":["Bash(confirm:*)","Bash(second:*)"],"deny":["Bash(blocked:*)"]}}`), 0o600))
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		pattern    string
		ask        bool
	}{
		{"Bash ask", "Bash", map[string]any{"command": "confirm PRIVATE"}, "Bash(confirm:*)", true},
		{"compound ask", "Bash", map[string]any{"command": "printf marker; confirm PRIVATE"}, "Bash(confirm:*)", true},
		{"MCP shell", "mcp__another__capy_execute", map[string]any{"language": "shell", "code": "confirm PRIVATE"}, "Bash(confirm:*)", true},
		{"MCP file", "mcp__capy__capy_execute_file", map[string]any{"language": "shell", "code": "confirm PRIVATE"}, "Bash(confirm:*)", true},
		{"batch first ask", "mcp__capy__capy_batch_execute", map[string]any{"commands": []any{"confirm PRIVATE", "second later"}}, "Bash(confirm:*)", true},
		{"embedded first ask", "mcp__capy__capy_execute", map[string]any{"language": "python", "code": `subprocess.run(["confirm", "PRIVATE"]); subprocess.run(["second", "later"])`}, "Bash(confirm:*)", true},
		{"compound later deny", "Bash", map[string]any{"command": "confirm PRIVATE; blocked later"}, "Bash(blocked:*)", false},
		{"batch later deny", "mcp__capy__capy_batch_execute", map[string]any{"commands": []any{"confirm PRIVATE", "blocked later"}}, "Bash(blocked:*)", false},
		{"embedded later deny", "mcp__capy__capy_execute", map[string]any{"language": "python", "code": `subprocess.run(["confirm", "PRIVATE"]); subprocess.run(["blocked", "later"])`}, "Bash(blocked:*)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := handleEvent("pretooluse", hookInput(t, tc.tool, tc.args, nil), &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			response := ccParse(t, output)
			assert.Equal(t, "deny", response["permissionDecision"])
			reason, ok := response["permissionDecisionReason"].(string)
			require.True(t, ok)
			assert.Contains(t, reason, tc.pattern)
			assert.Contains(t, reason, source)
			assert.NotContains(t, reason, "PRIVATE")
			assert.Equal(t, tc.ask, strings.Contains(reason, "cannot request"))
			assert.NotContains(t, response, "updatedInput")
		})
	}
	for _, language := range []string{"shell", "python"} {
		t.Run("unmatched "+language, func(t *testing.T) {
			code := "unknown argument"
			if language == "python" {
				code = `subprocess.run(["unknown", "argument"])`
			}
			output, err := handleEvent("pretooluse", hookInput(t, "mcp__capy__capy_execute", map[string]any{"language": language, "code": code}, nil), &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			assert.Nil(t, output)
		})
	}
	t.Run("scanner failure after ask", func(t *testing.T) {
		output, err := checkCommandsSecurity([]string{"confirm PRIVATE", strings.Repeat(":;", 4097)}, security.ReadBashPolicies(project, ""), &adapter.CodexAdapter{})
		require.NoError(t, err)
		response := ccParse(t, output)
		assert.Equal(t, "deny", response["permissionDecision"])
		assert.Contains(t, response["permissionDecisionReason"], "limit exceeded")
		assert.NotContains(t, response["permissionDecisionReason"], "cannot request")
	})
	assert.NoDirExists(t, filepath.Join(project, ".capy"), "policy-only handling must not write guidance or observations")
}

func TestCodexSessionStartGuidance(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	for _, source := range []string{"startup", "resume", "clear", "compact", "fork"} {
		t.Run(source, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"source": source, "transcript_path": nil})
			require.NoError(t, err)
			output, err := handleEvent("sessionstart", input, &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			response := ccParse(t, output)
			assert.Equal(t, "SessionStart", response["hookEventName"])
			context, ok := response["additionalContext"].(string)
			require.True(t, ok)
			assert.LessOrEqual(t, len(context), 4096)
			assert.Contains(t, context, "Hosted web/search tools are not intercepted")
			assert.Contains(t, context, "child launch arguments are preserved")
			assert.Contains(t, context, "Comprehension")
			assert.Contains(t, context, "Extraction")
			assert.NotContains(t, context, "Bash-to-general-purpose upgrade")
			assert.NotContains(t, context, "WebFetch →")
		})
	}
	assert.NoDirExists(t, filepath.Join(project, ".capy"))
}

func TestCodexRoutingScopeAndUnstableIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_SESSION_ID", "must-not-persist")
	project := t.TempDir()
	for _, tool := range []string{"spawn_agent", "Agent", "Task", "WebFetch", "Read", "exec_command"} {
		t.Run(tool, func(t *testing.T) {
			input := hookInput(t, tool, map[string]any{"command": "curl https://example.com", "prompt": "original"}, nil)
			output, err := handleEvent("pretooluse", input, &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			assert.Nil(t, output, "only canonical Bash and capy calls are selected")
		})
	}
	for range 2 {
		input := hookInput(t, "Bash", map[string]any{"command": "printf marker"}, map[string]any{"session_id": ""})
		output, err := handleEvent("pretooluse", input, &adapter.CodexAdapter{}, &project)
		require.NoError(t, err)
		assert.NotEmpty(t, ccParse(t, output)["additionalContext"])
	}
	assert.NoDirExists(t, filepath.Join(project, ".capy"))
}

func TestHookContextCodexIgnoresClaudeEnvironment(t *testing.T) {
	process := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(process, ".capy.toml"), nil, 0o600))
	processChild := filepath.Join(process, "child")
	require.NoError(t, os.Mkdir(processChild, 0o755))
	t.Chdir(processChild)
	t.Setenv("CLAUDE_PROJECT_DIR", "/missing-claude-project")
	payload := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(payload, ".capy.toml"), nil, 0o600))
	payloadChild := filepath.Join(payload, "child")
	require.NoError(t, os.Mkdir(payloadChild, 0o755))
	explicit := t.TempDir()
	for _, tc := range []struct {
		name, cwd, wantProject, wantCwd string
		explicit                        *string
	}{
		{"payload is owner", payloadChild, payloadChild, payloadChild, nil},
		{"explicit wins", payloadChild, explicit, payloadChild, &explicit},
		{"process discovery", "", process, process, nil},
		{"invalid payload fallback", "/missing-payload", process, process, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := resolveHookContext(adapter.Codex, tc.explicit, &adapter.PreToolUseEvent{Cwd: tc.cwd})
			require.NoError(t, err)
			wantProject, err := filepath.EvalSymlinks(tc.wantProject)
			require.NoError(t, err)
			wantCwd, err := filepath.EvalSymlinks(tc.wantCwd)
			require.NoError(t, err)
			assert.Equal(t, wantProject, ctx.projectDir)
			assert.Equal(t, wantCwd, ctx.workingDir)
		})
	}
}

func TestCodexPolicyWorkingDirectoryAndGuidanceIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project, cwd := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", cwd)
	require.NoError(t, os.Mkdir(filepath.Join(project, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(`{"permissions":{"deny":["Read(./secret.txt)"]}}`), 0o600))
	for _, tc := range []struct {
		name, path string
		deny       bool
	}{
		{"payload-relative rule", filepath.Join(cwd, "secret.txt"), true},
		{"project-relative argument", "secret.txt", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := hookInput(t, "mcp__capy__capy_index", map[string]any{"path": tc.path}, map[string]any{"cwd": cwd})
			output, err := handleEvent("pretooluse", input, &adapter.CodexAdapter{}, &project)
			require.NoError(t, err)
			if tc.deny {
				assert.Equal(t, "deny", ccParse(t, output)["permissionDecision"])
			} else {
				assert.Nil(t, output)
			}
		})
	}
	input := hookInput(t, "Bash", map[string]any{"command": "printf marker"}, map[string]any{"cwd": cwd})
	for _, a := range []adapter.HookAdapter{&adapter.ClaudeCodeAdapter{}, &adapter.CodexAdapter{}} {
		output, err := handleEvent("pretooluse", input, a, &project)
		require.NoError(t, err)
		assert.NotEmpty(t, ccParse(t, output)["additionalContext"])
		output, err = handleEvent("pretooluse", input, a, &project)
		require.NoError(t, err)
		assert.Nil(t, output)
	}
	assert.FileExists(t, filepath.Join(project, ".capy", "guidance-context-session-bash.once"))
	assert.FileExists(t, filepath.Join(project, ".capy", "guidance-"+sessionIDComponent("codex:context-session")+"-bash.once"))
	assert.NoDirExists(t, filepath.Join(cwd, ".capy"))
}
