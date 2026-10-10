package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellPolicyLimitsBeforeSpawn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	marker := filepath.Join(t.TempDir(), "spawned")
	prefix := ": > " + marker + "; "
	for _, tc := range []struct{ name, at, over string }{
		{"input bytes", strings.Repeat("x", security.MaxShellInputBytes), strings.Repeat("x", security.MaxShellInputBytes+1)},
		{"frames", strings.Repeat("( ", 64) + ":" + strings.Repeat(")", 64), strings.Repeat("( ", 65) + ":" + strings.Repeat(")", 65)},
		{"elements", strings.Repeat(":;", 4096), strings.Repeat(":;", 4097)},
		{"visits", strings.Repeat("echo $(", 6) + strings.Repeat("x", 4096) + strings.Repeat(")", 6), strings.Repeat("echo $(", 16) + strings.Repeat("x", 4096) + strings.Repeat(")", 16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Nil(t, srv.checkDenyPolicy(tc.at))
			for _, mode := range []string{"shell", "javascript", "file", "batch serial", "batch parallel"} {
				t.Run(mode, func(t *testing.T) {
					command := prefix + tc.over
					req := mcp.CallToolRequest{}
					var result *mcp.CallToolResult
					var err error
					switch mode {
					case "file":
						req.Params.Arguments = map[string]any{"language": "shell", "code": command, "path": filepath.Join(t.TempDir(), "missing")}
						result, err = srv.handleExecuteFile(context.Background(), req)
					case "batch serial", "batch parallel":
						concurrency := 1
						if mode == "batch parallel" {
							concurrency = 2
						}
						req.Params.Arguments = map[string]any{"commands": []any{map[string]any{"label": "first", "command": prefix + ":"}, map[string]any{"label": "limited", "command": command}}, "queries": []any{"test"}, "concurrency": concurrency}
						result, err = srv.handleBatchExecute(context.Background(), req)
					default:
						code := command
						if mode == "javascript" {
							code = "require('child_process').execSync(`" + command + "`)"
						}
						req.Params.Arguments = map[string]any{"language": mode, "code": code}
						result, err = srv.handleExecute(context.Background(), req)
					}
					require.NoError(t, err)
					require.True(t, result.IsError, resultText(result))
					assert.Contains(t, resultText(result), tc.name+" limit exceeded")
					_, err = os.Stat(marker)
					require.ErrorIs(t, err, os.ErrNotExist, "rejected request spawned a child")
				})
			}
		})
	}
}

func TestShellPolicyNestedDenyBeforeSpawn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, []security.SecurityPolicy{{Deny: []string{"Bash(blocked:*)"}}})
	marker := filepath.Join(t.TempDir(), "spawned")
	for _, tc := range []struct{ name, command string }{
		{"newline", "echo ok\nblocked arg"},
		{"substitution", `echo "$(blocked arg)"`},
		{"heredoc", "cat <<EOF\n$(blocked arg)\nEOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := ": > " + marker + "; " + tc.command
			result := callTool(t, srv, map[string]any{"language": "shell", "code": command})
			require.True(t, result.IsError)
			assert.Contains(t, resultText(result), "Bash(blocked:*)")
			_, err := os.Stat(marker)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
	result := callTool(t, srv, map[string]any{"language": "shell", "code": "cat <<'EOF'\n$(blocked literal)\nEOF"})
	require.False(t, result.IsError, resultText(result))
	assert.Contains(t, resultText(result), "$(blocked literal)")
}
