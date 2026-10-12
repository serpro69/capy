package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookCodexDispatch(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, name := range []string{
		"startup", "guidance", "local ask", "shared ask", "global ask", "deny",
		"malformed", "invalid platform", "invalid event", "child event pending",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			project, home, dataHome := filepath.Join(root, "project"), filepath.Join(root, "home"), filepath.Join(root, "data")
			require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath = 'knowledge.db'\n"), 0o600))
			args := []string{"hook", "pretooluse", "--platform", "codex"}
			metadata := map[string]any{
				"tool_name": "Bash", "tool_input": map[string]any{"command": "printf marker"},
				"session_id": "cli-codex", "cwd": project, "transcript_path": nil,
			}
			wantError, wantDecision, source := false, "", ""
			switch name {
			case "startup":
				args[1] = "sessionstart"
				delete(metadata, "tool_name")
				delete(metadata, "tool_input")
				metadata["source"] = "fork"
			case "local ask", "shared ask", "global ask", "deny":
				source = filepath.Join(project, ".claude", "settings.json")
				if name == "local ask" {
					source = filepath.Join(project, ".claude", "settings.local.json")
				} else if name == "global ask" {
					source = filepath.Join(home, ".claude", "settings.json")
				}
				action := "ask"
				if name == "deny" {
					action = "deny"
				}
				require.NoError(t, os.WriteFile(source, []byte(`{"permissions":{"`+action+`":["Bash(printf:*)"]}}`), 0o600))
				wantDecision = "deny"
			case "malformed":
				metadata["tool_input"] = map[string]any{"command": 42}
				wantDecision = "deny"
			case "invalid platform":
				args[3], wantError = "other", true
			case "invalid event":
				args[1], wantError = "unknown", true
			case "child event pending":
				args[1], wantError = "subagentstart", true
			}
			input, err := json.Marshal(metadata)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Dir = root // payload cwd must select the policy owner
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home,
				"XDG_DATA_HOME=" + dataHome, "CAPY_DB_KEY=", "CAPY_VAULT_KEY=",
				"CAPY_VAULT_PATH=" + filepath.Join(root, "vault.db"),
				"CLAUDE_PROJECT_DIR=" + filepath.Join(root, "wrong-project"), "CLAUDE_SESSION_ID=wrong-session",
			}
			cmd.Stdin = bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			if wantError {
				require.Error(t, err)
				assert.Empty(t, stdout.String())
				assert.NotEmpty(t, stderr.String())
			} else {
				require.NoError(t, err, stderr.String())
				assert.Empty(t, stderr.String())
				var response struct {
					Output map[string]any `json:"hookSpecificOutput"`
				}
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &response))
				if wantDecision == "" {
					assert.NotContains(t, response.Output, "permissionDecision")
					guidance, ok := response.Output["additionalContext"].(string)
					require.True(t, ok)
					assert.NotEmpty(t, guidance)
					if name == "startup" {
						assert.Equal(t, "SessionStart", response.Output["hookEventName"])
						assert.LessOrEqual(t, len(guidance), 4096)
					}
				} else {
					assert.Equal(t, wantDecision, response.Output["permissionDecision"])
					if source != "" {
						assert.Contains(t, response.Output["permissionDecisionReason"], source)
						assert.Contains(t, response.Output["permissionDecisionReason"], "Bash(printf:*)")
					}
				}
			}
			if name != "guidance" {
				assert.NoDirExists(t, filepath.Join(project, ".capy"))
			}
			assert.NoFileExists(t, filepath.Join(project, "knowledge.db"))
			assert.NoFileExists(t, filepath.Join(root, "vault.db"))
			assert.NoDirExists(t, filepath.Join(dataHome, "capy"))
			assert.NoDirExists(t, filepath.Join(root, ".capy"))
		})
	}
}
