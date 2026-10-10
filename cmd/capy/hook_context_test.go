package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookContextCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, name := range []string{"explicit", "relative explicit", "environment", "payload", "process", "invalid payload", "invalid explicit", "empty explicit", "invalid environment", "invalid sessionstart"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			projects := make(map[string]string)
			for _, label := range []string{"explicit", "environment", "payload", "process"} {
				project := filepath.Join(root, label)
				projects[label] = project
				require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
				require.NoError(t, os.Mkdir(filepath.Join(project, "nested"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), nil, 0o600))
			}
			require.NoError(t, os.WriteFile(filepath.Join(projects["explicit"], ".claude", "settings.json"), []byte(`{"permissions":{"deny":["Bash(probe *)"]}}`), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(projects["environment"], ".claude", "settings.json"), []byte(`{"permissions":{"ask":["Bash(probe *)"]}}`), 0o600))
			metadata := map[string]any{
				"tool_name": "Bash", "tool_input": map[string]any{"command": "probe arg"},
				"session_id": "cli-context", "cwd": filepath.Join(projects["payload"], "nested"),
			}
			args := []string{"hook", "pretooluse"}
			envProject := projects["environment"]
			wantDecision := ""
			wantStateProject := ""
			wantDiagnostic := ""
			switch name {
			case "explicit", "relative explicit":
				selected := projects["explicit"]
				if name == "relative explicit" {
					selected = "../../explicit"
				}
				args = append(args, "--project-dir", selected)
				wantDecision = "deny"
			case "environment":
				wantDecision = "ask"
			case "payload":
				envProject = ""
				wantStateProject = "payload"
			case "process":
				envProject = ""
				delete(metadata, "cwd")
				wantStateProject = "process"
			case "invalid payload":
				metadata["cwd"] = 42
				wantDecision = "ask"
				wantDiagnostic = "ignoring invalid hook cwd"
			case "invalid explicit", "invalid sessionstart":
				args = append(args, "--project-dir", filepath.Join(root, "missing"))
				wantDecision = "deny"
				if name == "invalid sessionstart" {
					args[1] = "sessionstart"
				}
			case "empty explicit":
				args = append(args, "--project-dir", "")
				wantDecision = "deny"
			case "invalid environment":
				envProject = filepath.Join(root, "missing-env")
				wantDecision = "deny"
			}
			input, err := json.Marshal(metadata)
			require.NoError(t, err)
			cmd := exec.Command(bin, args...)
			cmd.Dir = filepath.Join(projects["process"], "nested")
			cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+envProject, "HOME="+t.TempDir(), "CAPY_DB_KEY=", "CAPY_VAULT_KEY=")
			cmd.Stdin = bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			if name == "invalid sessionstart" {
				require.Error(t, err)
				assert.Empty(t, stdout.String())
				assert.Contains(t, stderr.String(), "invalid hook project directory")
			} else {
				require.NoError(t, err, stderr.String())
				var response struct {
					HookSpecificOutput map[string]any `json:"hookSpecificOutput"`
				}
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
				if wantDecision == "" {
					assert.NotContains(t, response.HookSpecificOutput, "permissionDecision")
					assert.NotEmpty(t, response.HookSpecificOutput["additionalContext"])
				} else {
					assert.Equal(t, wantDecision, response.HookSpecificOutput["permissionDecision"])
				}
				if wantDiagnostic != "" {
					assert.Contains(t, stderr.String(), wantDiagnostic)
				}
			}
			for label, project := range projects {
				stateFile := filepath.Join(project, ".capy", "guidance-cli-context.json")
				if label == wantStateProject {
					assert.FileExists(t, stateFile)
				} else {
					assert.NoFileExists(t, stateFile)
				}
			}
		})
	}
}
