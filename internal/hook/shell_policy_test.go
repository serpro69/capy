package hook

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellPolicyHookLimits(t *testing.T) {
	for _, tc := range []struct{ name, at, over string }{
		{"input bytes", strings.Repeat("x", security.MaxShellInputBytes), strings.Repeat("x", security.MaxShellInputBytes+1)},
		{"frames", strings.Repeat("( ", 64) + ":" + strings.Repeat(")", 64), strings.Repeat("( ", 65) + ":" + strings.Repeat(")", 65)},
		{"elements", strings.Repeat(":;", 4096), strings.Repeat(":;", 4097)},
		{"visits", strings.Repeat("echo $(", 6) + strings.Repeat("x", 4096) + strings.Repeat(")", 6), strings.Repeat("echo $(", 16) + strings.Repeat("x", 4096) + strings.Repeat(")", 16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range []string{"Bash", "capy_execute", "capy_execute_file", "capy_batch_execute", "embedded"} {
				t.Run(tool, func(t *testing.T) {
					for _, over := range []bool{false, true} {
						command := tc.at
						if over {
							command = tc.over
						}
						name := tool
						args := map[string]any{"language": "shell", "code": command}
						switch tool {
						case "Bash":
							args = map[string]any{"command": command}
						case "capy_batch_execute":
							args = map[string]any{"commands": []any{map[string]any{"command": command}}}
						case "embedded":
							name = "capy_execute"
							args = map[string]any{"language": "javascript", "code": "execSync(`" + command + "`)"}
						}
						output, err := handlePreToolUse(makeInput(name, args), &testAdapter{}, nil, "")
						require.NoError(t, err)
						if over {
							require.NotNil(t, output)
							result := parseResult(t, output)
							assert.Equal(t, "deny", result["action"])
							assert.Contains(t, result["reason"], tc.name+" limit exceeded")
						} else if output != nil {
							assert.NotEqual(t, "deny", parseResult(t, output)["action"])
						}
					}
				})
			}
		})
	}
}

func TestShellPolicyHookDecisions(t *testing.T) {
	policies := []security.SecurityPolicy{{Deny: []string{"Bash(blocked:*)"}, Ask: []string{"Bash(confirm:*)"}, Allow: []string{"Bash(echo:*)"}}}
	for _, tc := range []struct{ name, command, action string }{
		{"nested deny", `echo "$(blocked arg)"`, "deny"},
		{"heredoc expansion deny", "cat <<EOF\n$(blocked arg)\nEOF", "deny"},
		{"matched ask", "echo ok; confirm arg", "ask"},
		{"unmatched ask passes", "echo ok; unknown arg", ""},
		{"literal passes", "cat <<'EOF'\n$(blocked literal)\nEOF", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := handlePreToolUse(makeInput("capy_execute", map[string]any{"language": "shell", "code": tc.command}), &testAdapter{}, policies, "")
			require.NoError(t, err)
			if tc.action == "" {
				assert.Nil(t, output)
			} else {
				assert.Equal(t, tc.action, parseResult(t, output)["action"])
			}
		})
	}
}

func TestShellPolicyHookChecksAllCommandsBeforeAsk(t *testing.T) {
	policies := []security.SecurityPolicy{{Ask: []string{"Bash(confirm:*)"}, Deny: []string{"Bash(blocked:*)"}}}
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
	}{
		{"batch later deny", "capy_batch_execute", map[string]any{"commands": []any{"confirm arg", "blocked arg"}}},
		{"batch later limit", "capy_batch_execute", map[string]any{"commands": []any{"confirm arg", strings.Repeat(":;", 4097)}}},
		{"embedded later deny", "capy_execute", map[string]any{"language": "javascript", "code": "execSync('confirm arg'); execSync('blocked arg')"}},
		{"malformed command", "capy_batch_execute", map[string]any{"commands": []any{map[string]any{"command": 42}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := handlePreToolUse(makeInput(tc.tool, tc.args), &testAdapter{}, policies, "")
			require.NoError(t, err)
			assert.Equal(t, "deny", parseResult(t, output)["action"])
		})
	}
	t.Run("serialized commands", func(t *testing.T) {
		encoded, err := json.Marshal([]any{map[string]any{"command": strings.Repeat(":;", 4097)}})
		require.NoError(t, err)
		output, err := handlePreToolUse(makeInput("capy_batch_execute", map[string]any{"commands": string(encoded)}), &testAdapter{}, nil, "")
		require.NoError(t, err)
		assert.Contains(t, parseResult(t, output)["reason"], "elements limit exceeded")
	})
}
