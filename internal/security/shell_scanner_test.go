package security

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellElementsPolicy(t *testing.T) {
	policies := []SecurityPolicy{{Deny: []string{"Bash(blocked:*)"}, Allow: []string{"Bash(echo:*)", "Bash(cat:*)"}}}
	cases := []struct {
		name, command string
		denied        bool
	}{
		{"newline", "echo ok\nblocked arg", true},
		{"carriage return", "echo ok\rblocked arg", true},
		{"crlf", "echo ok\r\nblocked arg", true},
		{"background", "echo ok & blocked arg", true},
		{"double quoted substitution", `echo "$(blocked arg)"`, true},
		{"nested substitutions", `echo "$(echo $(blocked arg))"`, true},
		{"backtick", "echo `blocked arg`", true},
		{"double quoted backtick", "echo \"`blocked arg`\"", true},
		{"nested backtick", "echo `echo \\`blocked arg\\``", true},
		{"substitution in arithmetic", `echo $((1 + $(blocked arg)))`, true},
		{"arithmetic command substitution", `((1 + $(blocked arg)))`, true},
		{"subshell", `(echo ok; blocked arg)`, true},
		{"even backslashes", `echo \\; blocked arg`, true},
		{"even backslashes before quote", `echo "x\\"; blocked arg`, true},
		{"single quoted data", "echo '$(blocked arg); `blocked arg`'", false},
		{"double quoted separators", `echo "ok; blocked arg | blocked arg"`, false},
		{"escaped substitution", `echo "\$(blocked arg)"`, false},
		{"escaped pipe", `echo ok \| blocked arg`, false},
		{"odd backslashes", `echo \\\; blocked arg`, false},
		{"escaped quote", `echo "x\"; blocked arg"`, false},
		{"descriptor duplication", `echo ok 2>&1`, false},
		{"output redirection", `echo ok &>out`, false},
		{"quoted heredoc", "cat <<'END'\n$(blocked arg)\nblocked arg\nEND\necho ok", false},
		{"double quoted heredoc", "cat <<\"END\"\n$(blocked arg)\nEND", false},
		{"escaped delimiter", "cat <<E\\ND\n$(blocked arg)\nEND", false},
		{"partly quoted delimiter", "cat <<E'N'D\n$(blocked arg)\nEND", false},
		{"heredoc ordinary body", "cat <<END\nblocked arg\nEND", false},
		{"heredoc substitution", "cat <<END\n$(blocked arg)\nEND", true},
		{"heredoc quotes are data", "cat <<END\n'$(blocked arg)'\nEND", true},
		{"heredoc parameter quotes are data", "cat <<END\n${x:-'$(blocked arg)'}\nEND", true},
		{"heredoc escaped substitution", "cat <<END\n\\$(blocked arg)\nEND", false},
		{"heredoc nested substitution", "cat <<END\n$(echo $(blocked arg))\nEND", true},
		{"joined heredoc delimiter", "cat <<END\nEN\\\nD\nblocked arg", true},
		{"continued heredoc declaration", "cat <<E\\\nND\n$(blocked arg)\nEND", true},
		{"joined heredoc expansion", "cat <<END\n$\\\n(blocked arg)\nEND", true},
		{"tab stripped heredoc", "cat <<-END\n\ttext\n\tEND\nblocked arg", true},
		{"multiple heredocs", "cat <<'A' <<B\n$(blocked literal)\nA\n$(blocked arg)\nB", true},
		{"heredoc inside substitution", "echo $(cat <<'END'\n) $(blocked literal)\nEND\n)", false},
		{"arithmetic is data", `echo $((1 | 2 & 3))`, false},
		{"comment is data", "echo ok # $(blocked arg)\necho ok", false},
		{"after comment", "echo ok # comment\nblocked arg", true},
		{"escaped space before hash", `echo x\ # $(blocked arg)`, true},
		{"continuation before comment", "echo ok; \\\n# $(blocked arg)\necho ok", false},
		{"ansi escaped quote", `echo $'\'' $(blocked arg) # '`, true},
		{"parameter parenthesis", `echo "$(echo ${x:-)}; blocked arg)"`, true},
		{"parameter nested substitution", `echo ${x:-$(blocked arg)}`, true},
		{"parameter literal separators", `echo ${x:-; blocked arg}`, false},
		{"parameter quotes inside double quotes", `echo "${x:-'$(blocked arg)'}"`, true},
		{"parameter ansi escaped quote", `echo ${x:-$'\''} $(blocked arg) # '}`, true},
		{"case sensitive", `BLOCKED arg`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, evaluate := range []func(string, []SecurityPolicy) (CommandDecision, error){EvaluateCommand, EvaluateCommandDenyOnly} {
				decision, err := evaluate(tc.command, policies)
				require.NoError(t, err)
				assert.Equal(t, tc.denied, decision.Decision == "deny", decision)
			}
		})
	}
}

func TestShellPolicyEveryElementAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		policies      []SecurityPolicy
		want, pattern string
	}{
		{"unknown later command", "echo ok; unknown arg", []SecurityPolicy{{Allow: []string{"Bash(echo:*)"}}}, "ask", ""},
		{"unknown substitution", "echo $(unknown arg)", []SecurityPolicy{{Allow: []string{"Bash(echo:*)"}}}, "ask", ""},
		{"all elements allowed", "echo ok; cat file", []SecurityPolicy{{Allow: []string{"Bash(echo:*)", "Bash(cat:*)"}}}, "allow", "Bash(echo:*)"},
		{"ask later element", "echo ok; cat file", []SecurityPolicy{{Allow: []string{"Bash(*)"}, Ask: []string{"Bash(cat:*)"}}}, "ask", "Bash(cat:*)"},
		{"earlier allow wins later ask", "echo ok", []SecurityPolicy{{Allow: []string{"Bash(echo:*)"}}, {Ask: []string{"Bash(echo:*)"}}}, "allow", "Bash(echo:*)"},
		{"later deny wins", "echo $(blocked arg)", []SecurityPolicy{{Allow: []string{"Bash(*)"}}, {Deny: []string{"Bash(blocked:*)"}}}, "deny", "Bash(blocked:*)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := EvaluateCommand(tc.command, tc.policies)
			require.NoError(t, err)
			assert.Equal(t, CommandDecision{tc.want, tc.pattern}, decision)
		})
	}
}

func TestShellScannerLimits(t *testing.T) {
	for _, tc := range []struct{ name, at, over string }{
		{"input bytes", strings.Repeat("x", MaxShellInputBytes), strings.Repeat("x", MaxShellInputBytes+1)},
		{"frames", strings.Repeat("( ", MaxShellFrames) + ":" + strings.Repeat(")", MaxShellFrames), strings.Repeat("( ", MaxShellFrames+1) + ":" + strings.Repeat(")", MaxShellFrames+1)},
		{"elements", strings.Repeat(":;", MaxShellElements), strings.Repeat(":;", MaxShellElements+1)},
		{"heredoc frames", "cat" + strings.Repeat(" <<E", MaxShellFrames), "cat" + strings.Repeat(" <<E", MaxShellFrames+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SplitChainedCommands(tc.at)
			require.NoError(t, err)
			parts, err := SplitChainedCommands(tc.over)
			var limit *ShellScanLimitError
			require.ErrorAs(t, err, &limit)
			assert.Nil(t, parts)
			assert.Contains(t, tc.name, limit.Limit)
			for _, evaluate := range []func(string, []SecurityPolicy) (CommandDecision, error){EvaluateCommand, EvaluateCommandDenyOnly} {
				decision, err := evaluate(tc.over, nil)
				require.ErrorAs(t, err, &limit)
				assert.Empty(t, decision.Decision)
			}
		})
	}
	t.Run("visits exact boundary", func(t *testing.T) {
		input := "echo $(echo $(echo hello))"
		s := shellScanner{text: input, budget: 10000}
		_, err := s.commands(0, len(input), 0, 0)
		require.NoError(t, err)
		for _, allowance := range []int{s.visits, s.visits - 1} {
			bounded := shellScanner{text: input, budget: allowance}
			_, err := bounded.commands(0, len(input), 0, 0)
			if allowance == s.visits {
				require.NoError(t, err)
			} else {
				var limit *ShellScanLimitError
				require.ErrorAs(t, err, &limit)
				assert.Equal(t, "visits", limit.Limit)
			}
		}
	})
	t.Run("aggregate visits", func(t *testing.T) {
		input := strings.Repeat("echo $(", 16) + strings.Repeat("x", 4096) + strings.Repeat(")", 16)
		parts, err := SplitChainedCommands(input)
		var limit *ShellScanLimitError
		require.ErrorAs(t, err, &limit)
		assert.Equal(t, "visits", limit.Limit)
		assert.Nil(t, parts)
	})
}

func TestShellScannerUnsupportedHeredocQuotesFailClosed(t *testing.T) {
	for _, delimiter := range []string{`$'EOF'`, `$"EOF"`, `E$'OF'`, `$'\x45OF'`} {
		t.Run(delimiter, func(t *testing.T) {
			parts, err := SplitChainedCommands("cat <<" + delimiter + "\ntext\nEOF\nblocked arg")
			require.ErrorContains(t, err, "unsupported shell heredoc delimiter quoting")
			assert.Nil(t, parts)
		})
	}
}

func FuzzSplitChainedCommands(f *testing.F) {
	for _, seed := range []string{"echo $(date)", "cat <<E\n$(date)\nE", "echo `echo \\`date\\``", `echo $((1 + $(date)))`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		parts, err := SplitChainedCommands(command)
		if err != nil && parts != nil {
			t.Fatal("partial scanner result on error")
		}
		if len(parts) > MaxShellElements {
			t.Fatal("element bound exceeded")
		}
	})
}
