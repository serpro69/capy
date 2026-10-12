package security

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandDecisionProvenance(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	sources := []string{
		filepath.Join(project, ".claude", "settings.local.json"),
		filepath.Join(project, ".claude", "settings.json"),
		filepath.Join(t.TempDir(), "settings.json"),
	}
	for _, path := range sources {
		writeSettingsFile(t, path, `{"permissions":{"ask":["Bash(confirm:*)"],"deny":["Bash(blocked:*)"],"allow":["Bash(printf:*)"]}}`)
	}
	policies := ReadBashPolicies(project, sources[2])
	require.Len(t, policies, 3)
	for i, path := range sources {
		t.Run(path, func(t *testing.T) {
			assert.Equal(t, path, policies[i].SourcePath)
			// Identical patterns in later sources must not replace this source.
			for _, tc := range []struct{ command, decision, pattern string }{
				{"confirm secret-argument", "ask", "Bash(confirm:*)"},
				{"confirm arg; blocked later", "deny", "Bash(blocked:*)"},
				{"printf marker", "allow", "Bash(printf:*)"},
			} {
				t.Run(tc.decision, func(t *testing.T) {
					decision, err := EvaluateCommand(tc.command, policies[i:])
					require.NoError(t, err)
					assert.Equal(t, CommandDecision{Decision: tc.decision, MatchedPattern: tc.pattern, SourcePath: path}, decision)
				})
			}
			commands := ExtractEmbeddedCommands(`subprocess.run(["confirm", "secret-argument"])`, "python")
			require.Len(t, commands, 1)
			decision, err := EvaluateEmbeddedCommand(commands[0], policies[i:])
			require.NoError(t, err)
			assert.Equal(t, path, decision.SourcePath)
		})
	}
	t.Run("later deny wins earlier ask", func(t *testing.T) {
		decision, err := EvaluateCommand("confirm arg", []SecurityPolicy{
			{Ask: []string{"Bash(confirm:*)"}, SourcePath: sources[0]},
			{Deny: []string{"Bash(confirm:*)"}, SourcePath: sources[2]},
		})
		require.NoError(t, err)
		assert.Equal(t, "deny", decision.Decision)
		assert.Equal(t, sources[2], decision.SourcePath)
	})
	t.Run("unmatched default ask", func(t *testing.T) {
		decision, err := EvaluateCommand("unknown argument", policies)
		require.NoError(t, err)
		assert.Equal(t, CommandDecision{Decision: "ask"}, decision)
	})
	t.Run("in-memory rule", func(t *testing.T) {
		decision, err := EvaluateCommand("confirm arg", []SecurityPolicy{{Ask: []string{"Bash(confirm:*)"}}})
		require.NoError(t, err)
		assert.Equal(t, "<in-memory>", decision.SourcePath)
	})
}
