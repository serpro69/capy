package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrapperCodexHookFailure(t *testing.T) {
	for _, tc := range []struct {
		name, body, event string
		want              int
	}{
		{"crash", "exit 1", "pretooluse", 2},
		{"exec failure", "exit 126", "pretooluse", 2},
		{"not found", "exit 127", "pretooluse", 2},
		{"structured denial", `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}'`, "pretooluse", 0},
		{"startup failure", "exit 42", "sessionstart", 42},
		{"missing binary", "", "pretooluse", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			wrapper := filepath.Join(dir, "wrapper.sh")
			require.NoError(t, os.WriteFile(wrapper, []byte(capyWrapperScript), 0o700))
			if tc.body != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "capy"), []byte("#!/bin/sh\n"+tc.body+"\n"), 0o700))
			} else if isExecutableFile("/opt/homebrew/bin/capy") || isExecutableFile("/usr/local/bin/capy") {
				t.Skip("capy exists in a fixed fallback location")
			}
			cmd := exec.Command("bash", wrapper, "hook", "--platform", "codex", tc.event)
			cmd.Dir, cmd.Env = dir, []string{"PATH=" + dir, "HOME=" + dir}
			out, _ := cmd.CombinedOutput()
			require.Equal(t, tc.want, cmd.ProcessState.ExitCode(), "%s", out)
			if tc.want != 0 {
				require.Contains(t, string(out), "install/upgrade capy")
			} else {
				require.Contains(t, string(out), "permissionDecision")
			}
		})
	}
}

func TestCodexOwnerInlineBindings(t *testing.T) {
	dir := t.TempDir()
	command := codexHookCommand(testCodexOwnerID, "pretooluse")
	// Literal multiline TOML decodes to the exact same command as hooks.json.
	config := "[[hooks.PreToolUse]]\nmatcher='Bash'\n[[hooks.PreToolUse.hooks]]\ntype='command'\ncommand=\"\"\"" + strings.ReplaceAll(strings.ReplaceAll(command, "\\", "\\\\"), "\"", "\\\"") + "\"\"\"\n"
	writeCodexFixture(t, dir, ".codex/config.toml", config)
	_, err := InspectCodexOwners(dir)
	require.ErrorContains(t, err, "restore the marker")
	writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
	owner, err := ResolveCodexOwner(dir, testCodexOwnerID, "hook", "pretooluse")
	require.NoError(t, err)
	require.Equal(t, dir, owner)
}

func TestMergeCodexMCPDecodedTableBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	unrelated := "# [mcp_servers.capy] in a comment\nmessage='''\n[mcp_servers.capy]\nnot a real table\n'''\n"
	stale := "['mcp_servers'.'capy']\ncommand='bash'\nargs=['.codex/scripts/capy.sh','serve']\n"
	suffix := "[mcp_servers.other]\ncommand='other'\n"
	require.NoError(t, os.WriteFile(path, []byte(unrelated+stale+suffix), 0o600))
	require.NoError(t, mergeCodexMCPServer(path, testCodexOwnerID))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), unrelated))
	require.True(t, strings.HasSuffix(string(data), suffix))
	require.Contains(t, string(data), codexMCPBlock(testCodexOwnerID))
}
