package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreparedReadPolicyHook(t *testing.T) {
	for _, policy := range []string{
		`{"permissions":{"deny":["Read(secret.txt)"]}}`,
		`{"permissions":{"deny":["Read"]}}`,
		`{"permissions":{"deny":["Read([abc])"]}}`,
		`{broken`,
	} {
		t.Run(policy, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			project := t.TempDir()
			require.NoError(t, os.MkdirAll(project+"/.claude", 0o755))
			require.NoError(t, os.WriteFile(project+"/.claude/settings.json", []byte(policy), 0o600))
			for _, tool := range []string{"mcp__capy__capy_execute_file", "mcp__capy__capy_index"} {
				for _, path := range []string{"secret.txt", filepath.Join(project, "secret.txt")} {
					output, err := handlePreToolUse(makeInput(tool, map[string]any{
						"path": path, "language": "shell", "code": "echo safe",
					}), &testAdapter{}, nil, project)
					require.NoError(t, err, "policy errors must produce structured blocks")
					result := parseResult(t, output)
					assert.Equal(t, "deny", result["action"])
					assert.Contains(t, result["reason"], "security policy")
				}
			}
		})
	}
}
