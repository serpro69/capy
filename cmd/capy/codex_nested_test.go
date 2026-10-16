package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexNestedLaunches(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, layout := range []string{"same repository", "separate repositories"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			nested := filepath.Join(root, "child project")
			require.NoError(t, os.Mkdir(nested, 0o755))
			require.NoError(t, exec.Command("git", "init", "-q", root).Run())
			if layout == "separate repositories" {
				require.NoError(t, exec.Command("git", "init", "-q", nested).Run())
			}
			outerArgs, env := installCodexLaunch(t, bin, root)
			childArgs, _ := installCodexLaunch(t, bin, nested)
			for i, dir := range []string{root, nested} {
				label := []string{"parent", "child"}[i]
				require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
				policy := fmt.Sprintf(`{"permissions":{"deny":["Bash(echo %s-only)"]}}`, label)
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude/settings.json"), []byte(policy), 0o600))
				cfg := "[store]\npath = '.capy/knowledge.db'\nkey_file = '.capy/db.key'\n"
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy.toml"), []byte(cfg), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".capy/db.key"), []byte("synthetic-nested-"+label), 0o600))
			}
			// Every ancestor hook can be loaded by Codex, but only the child's
			// handler may emit guidance, apply rules or alter session observations.
			for _, rel := range []string{"tool-observations.json", "codex-main-observations.json"} {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".capy", rel), []byte("untouched parent state"), 0o600))
			}
			input := fmt.Sprintf(`{"session_id":"nested-fixture","cwd":%q,"source":"startup","tool_name":"Bash","tool_input":{"command":"echo parent-only"}}`, nested)
			for _, event := range []string{"SessionStart", "SubagentStart", "PreToolUse", "PostToolUse", "SessionEnd"} {
				code, out, stderr := runLaunch(t, nested, env, []string{"-c", launchHookCommand(t, root, event)}, input)
				require.Zero(t, code, "%s: %s", event, stderr)
				require.Empty(t, out, event)
				require.Empty(t, stderr, event)
			}
			for _, rel := range []string{"tool-observations.json", "codex-main-observations.json"} {
				data, err := os.ReadFile(filepath.Join(root, ".capy", rel))
				require.NoError(t, err)
				require.Equal(t, "untouched parent state", string(data))
			}
			code, out, stderr := runLaunch(t, nested, env, []string{"-c", launchHookCommand(t, nested, "PreToolUse")}, input)
			require.Zero(t, code, "%s", stderr)
			require.NotContains(t, out, `"permissionDecision":"deny"`)
			for i, dir := range []string{root, nested} {
				label := []string{"parent", "child"}[i]
				input := fmt.Sprintf(`{"session_id":"nested-fixture","cwd":%q,"tool_name":"Bash","tool_input":{"command":"echo %s-only"}}`, dir, label)
				code, out, stderr := runLaunch(t, dir, env, []string{"-c", launchHookCommand(t, dir, "PreToolUse")}, input)
				require.Zero(t, code, "%s", stderr)
				require.Contains(t, out, `"permissionDecision":"deny"`)
				require.Contains(t, out, filepath.Join(dir, ".claude/settings.json"))
				require.NoFileExists(t, filepath.Join(dir, ".capy/knowledge.db"))
			}
			// A stale/inherited parent MCP command cannot expose the parent database
			// or silently substitute the child's identity, even with valid credentials.
			code, out, stderr = runLaunch(t, nested, env, outerArgs, "")
			require.NotZero(t, code)
			require.Empty(t, out)
			require.Contains(t, stderr, "nearer installation")
			require.NoFileExists(t, filepath.Join(root, ".capy/knowledge.db"))
			require.NoFileExists(t, filepath.Join(nested, ".capy/knowledge.db"))

			parent := startStdioProcess(t, stdioCommand{executable: bin, args: outerArgs, dir: root, env: env})
			parent.initialize(t)
			child := startStdioProcess(t, stdioCommand{executable: bin, args: childArgs, dir: nested, env: env})
			child.initialize(t)
			parent.tool(t, "capy_index", map[string]any{"source": "nested-identity", "content": "nestedownership parentquartz"})
			child.tool(t, "capy_index", map[string]any{"source": "nested-identity", "content": "nestedownership childtangerine"})
			parentResult := parent.tool(t, "capy_search", map[string]any{"queries": []string{"nestedownership"}})
			childResult := child.tool(t, "capy_search", map[string]any{"queries": []string{"nestedownership"}})
			require.Contains(t, parentResult, "parentquartz")
			require.NotContains(t, parentResult, "childtangerine")
			require.Contains(t, childResult, "childtangerine")
			require.NotContains(t, childResult, "parentquartz")
			require.Contains(t, parent.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "pwd"}), root+"\n")
			require.Contains(t, child.tool(t, "capy_execute", map[string]any{"language": "shell", "code": "pwd"}), nested+"\n")
			child.close(t)
			parent.close(t)
			for _, dir := range []string{root, nested} {
				require.FileExists(t, filepath.Join(dir, ".capy/knowledge.db"))
				markerPath := filepath.Join(dir, ".codex/capy-owner")
				before, err := os.ReadFile(markerPath)
				require.NoError(t, err)
				// Exercise the original combined --local setup command as a rerun.
				args := []string{"setup", "--local"}
				if layout == "same repository" && dir == nested {
					args = append(args, "--project-dir", ".")
				}
				cmd := exec.Command(bin, args...)
				cmd.Dir, cmd.Env = dir, env
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
				require.Contains(t, string(output), "Claude Code setup complete")
				require.Contains(t, string(output), "Codex setup complete")
				after, err := os.ReadFile(markerPath)
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
		})
	}
}

func TestCodexShadowedHookInput(t *testing.T) {
	bin := buildStdioCapy(t)
	root := t.TempDir()
	nested := filepath.Join(root, "child")
	_, env := installCodexLaunch(t, bin, root)
	installCodexLaunch(t, bin, nested)
	for _, tc := range []struct {
		name, input string
		leaveOpen   bool
	}{
		{name: "large complete payload", input: `{"padding":"` + strings.Repeat("x", 256*1024) + `"}`},
		{name: "complete malformed payload", input: "{"},
		{name: "writer stays open", input: "{}", leaveOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", launchHookCommand(t, root, "PreToolUse"))
			cmd.Dir, cmd.Env = nested, env
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err)
			defer stdin.Close()
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			require.NoError(t, cmd.Start())
			n, err := stdin.Write([]byte(tc.input))
			require.NoError(t, err, "host must be able to deliver the whole event")
			require.Equal(t, len(tc.input), n)
			if !tc.leaveOpen {
				require.NoError(t, stdin.Close())
			}
			err = cmd.Wait()
			require.NoError(t, ctx.Err())
			require.Empty(t, out.String())
			if tc.leaveOpen {
				require.Error(t, err)
				require.Equal(t, 2, cmd.ProcessState.ExitCode())
				require.Contains(t, stderr.String(), "idle deadline exceeded")
			} else {
				require.NoError(t, err, "%s", stderr.String())
				require.Empty(t, stderr.String())
			}
		})
	}
}
