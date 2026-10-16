package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type installedCodexHooks struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command                string `json:"command"`
			AdditionalContextLimit *int   `json:"additionalContextLimit"`
		} `json:"hooks"`
	} `json:"hooks"`
}

func readInstalledCodexHooks(t *testing.T, dir string) installedCodexHooks {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".codex/hooks.json"))
	require.NoError(t, err)
	var config installedCodexHooks
	require.NoError(t, json.Unmarshal(data, &config))
	return config
}

func TestSetupCodexHooksCLI(t *testing.T) {
	bin := buildStdioCapy(t)
	for _, target := range []string{"codex", "default both", "default both local"} {
		t.Run(target, func(t *testing.T) {
			ancestor := t.TempDir()
			dir := filepath.Join(ancestor, "project")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(ancestor, ".codex"), 0o755))
			trustPath := filepath.Join(ancestor, ".codex/config.toml")
			trustState := []byte("[hooks.state.\"/fixture/project/.codex/hooks.json:pre_tool_use:0:0\"]\nenabled=true\ntrusted_hash='fixture-hash'\n")
			require.NoError(t, os.WriteFile(trustPath, trustState, 0o600))
			env := stdioEnv(t, "")
			env[0] = "PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH")
			args := []string{"setup", "--project-dir", dir}
			if target == "codex" {
				args = append(args, "--platform", "codex")
			} else if target == "default both local" {
				args = append(args, "--local")
			} else {
				args = append(args, "--project")
			}
			cmd := exec.Command(bin, args...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			require.Contains(t, string(output), "project-layer trust")
			require.Contains(t, string(output), "exact hook definitions in /hooks")
			require.Contains(t, string(output), "feature overrides and trust records unchanged")
			hooks := readInstalledCodexHooks(t, dir)
			require.Len(t, hooks.Hooks, 5)
			for _, event := range []string{"SessionStart", "PreToolUse", "SubagentStart", "PostToolUse", "SessionEnd"} {
				require.Len(t, hooks.Hooks[event], 1)
			}
			if target == "codex" {
				require.NoFileExists(t, filepath.Join(dir, ".mcp.json"))
			} else {
				require.FileExists(t, filepath.Join(dir, ".mcp.json"))
				settings := "settings.json"
				if target == "default both local" {
					settings = "settings.local.json"
					require.NoFileExists(t, filepath.Join(dir, ".claude/settings.json"))
				}
				require.FileExists(t, filepath.Join(dir, ".claude", settings))
			}
			after, err := os.ReadFile(trustPath)
			require.NoError(t, err)
			require.Equal(t, trustState, after, "ancestor trust state must remain untouched")
		})
	}
}

func TestCodexMixedStartupContext(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq required by the external toolbox startup script")
	}
	bin := buildStdioCapy(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex/scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
	// The real committed external script only reads these fixture paths.
	script, err := os.ReadFile("../../.codex/scripts/session-start.sh")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex/scripts/session-start.sh"), script, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude/CLAUDE.extra.md"), []byte("# Extra fixture\nPreserve extra instructions.\n"), 0o600))
	externalCommand := `bash "$(git rev-parse --show-toplevel)/.codex/scripts/session-start.sh"`
	externalHandlers := []any{map[string]any{
		"matcher": "startup|resume", "hooks": []any{map[string]any{"type": "command", "command": externalCommand, "additionalContextLimit": 10000}},
	}}
	config := map[string]any{"hooks": map[string]any{"SessionStart": externalHandlers}}
	data, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex/hooks.json"), data, 0o600))
	_, env := installCodexLaunch(t, bin, dir)
	git := exec.Command("git", "init", "-q", dir)
	git.Env = env
	require.NoError(t, git.Run())
	hooks := readInstalledCodexHooks(t, dir)
	require.Len(t, hooks.Hooks["SessionStart"], 2)
	static, err := os.ReadFile(filepath.Join(dir, ".capy/AGENTS.md"))
	require.NoError(t, err)
	contextFrom := func(t *testing.T, command, source string) string {
		t.Helper()
		input, err := json.Marshal(map[string]string{"session_id": "mixed-startup", "source": source, "cwd": dir})
		require.NoError(t, err)
		code, output, stderr := runLaunch(t, dir, env, []string{"-c", command}, string(input))
		require.Equal(t, 0, code, "%s", stderr)
		var result struct {
			Output struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		require.Equal(t, "SessionStart", result.Output.Event)
		return result.Output.Context
	}
	for _, source := range []string{"startup", "resume", "fork"} {
		t.Run(source, func(t *testing.T) {
			externalBytes, capyBytes := 0, 0
			for _, group := range hooks.Hooks["SessionStart"] {
				if !regexp.MustCompile(group.Matcher).MatchString(source) {
					continue
				}
				for _, handler := range group.Hooks {
					text := contextFrom(t, handler.Command, source)
					if handler.Command == externalCommand {
						require.Contains(t, text, "Provider: Codex (OpenAI).")
						require.Contains(t, text, "Preserve extra instructions.")
						// The toolbox defers routing guidance to the registered capy hook.
						require.Contains(t, text, "Capy guidance and HTTP routing are delivered by the capy hooks registered in .codex/hooks.json.")
						require.NotContains(t, text, "# capy — context-window routing")
						require.NotContains(t, text, strings.TrimRight(string(static), "\n"))
						require.NotNil(t, handler.AdditionalContextLimit)
						require.Equal(t, 10000, *handler.AdditionalContextLimit)
						externalBytes = len([]byte(text))
					} else {
						require.Nil(t, handler.AdditionalContextLimit)
						require.Contains(t, text, "capy_execute")
						require.LessOrEqual(t, len([]byte(text)), 4096)
						capyBytes = len([]byte(text))
					}
				}
			}
			require.NotZero(t, capyBytes)
			if source == "fork" {
				require.Zero(t, externalBytes)
			} else {
				require.NotZero(t, externalBytes)
			}
			t.Logf("UTF-8 context bytes: static file=%d, toolbox payload=%d, capy payload=%d, combined=%d", len(static), externalBytes, capyBytes, externalBytes+capyBytes)
		})
	}
	t.Run("legacy fallback without capy SessionStart", func(t *testing.T) {
		// Keep the other capy events registered: delegation is per event.
		path := filepath.Join(dir, ".codex/hooks.json")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var config struct {
			Hooks map[string]json.RawMessage `json:"hooks"`
		}
		require.NoError(t, json.Unmarshal(data, &config))
		config.Hooks["SessionStart"], err = json.Marshal(externalHandlers)
		require.NoError(t, err)
		data, err = json.Marshal(config)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		for _, source := range []string{"startup", "resume"} {
			t.Run(source, func(t *testing.T) {
				text := contextFrom(t, externalCommand, source)
				require.Contains(t, text, "Provider: Codex (OpenAI).")
				require.Contains(t, text, "Preserve extra instructions.")
				require.Contains(t, text, strings.TrimRight(string(static), "\n"))
				require.NotContains(t, text, "This hook does not inject .capy/AGENTS.md.")
			})
		}
	})
}
