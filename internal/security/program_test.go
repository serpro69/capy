package security

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapturedProgramRetainsOuterPolicy(t *testing.T) {
	policies := []SecurityPolicy{{
		Deny: []string{"Bash(blocked:*)"}, Ask: []string{"Bash(confirm:*)"}, SourcePath: "/outer/settings.json",
	}}
	for _, tc := range []struct {
		name, language, code, decision string
	}{
		{"shell", "shell", `capy --project-dir /other execute --code 'blocked arg'`, "deny"},
		{"nested", "shell", `capy execute --code "capy execute --code 'blocked arg'"`, "deny"},
		{"embedded", "python", `subprocess.run(["capy","execute","--code","blocked arg"])`, "deny"},
		{"ask", "shell", `capy execute --code 'confirm arg'`, "ask"},
		{"quoted data", "shell", `printf '%s' "capy execute --code 'blocked arg'"`, "ask"},
		{"argv data", "python", `subprocess.run(["printf","%s","blocked arg"])`, "ask"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := EvaluateProgram(tc.code, tc.language, policies, false)
			require.NoError(t, err)
			require.Equal(t, tc.decision, decision.Decision)
			if tc.name == "ask" || tc.decision == "deny" {
				require.Equal(t, "/outer/settings.json", decision.SourcePath)
			} else {
				require.Empty(t, decision.MatchedPattern)
			}
			denyOnly, err := EvaluateProgram(tc.code, tc.language, policies, true)
			require.NoError(t, err)
			if tc.decision == "deny" {
				require.Equal(t, "deny", denyOnly.Decision)
			} else {
				require.Equal(t, "allow", denyOnly.Decision)
			}
		})
	}
	_, err := EvaluateProgram(`capy execute --code-file program.sh`, "shell", policies, true)
	require.ErrorContains(t, err, "standalone invocation")
}
