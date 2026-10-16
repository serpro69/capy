package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func registrationChecks(t *testing.T, dir string) map[string]CheckResult {
	t.Helper()
	results := make(map[string]CheckResult)
	for _, result := range CheckPlatformRegistrations(dir) {
		require.NotContains(t, results, result.Name)
		results[result.Name] = result
	}
	return results
}

func TestDoctorPlatformRegistrations(t *testing.T) {
	for _, tc := range []struct {
		name          string
		claude, codex bool
	}{
		{name: "neither"}, {name: "Claude only", claude: true},
		{name: "Codex only", codex: true}, {name: "both", claude: true, codex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Directories and unrelated host configuration are not capy registration.
			writeCodexFixture(t, dir, ".codex/config.toml", "model='fixture'\n")
			writeCodexFixture(t, dir, ".claude/settings.json", `{"permissions":{"deny":["Bash(secret:*)"]},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"other-tool hook pretooluse"}]}]}}`)
			writeCodexFixture(t, dir, ".mcp.json", `{"mcpServers":{"other":{"command":"fixture"}}}`)
			if tc.claude {
				require.NoError(t, SetupClaudeCode("capy", dir, SettingsProject))
			}
			if tc.codex {
				require.NoError(t, setupCodex(dir))
			}
			checks := registrationChecks(t, dir)
			for _, name := range []string{"Claude Code hooks", "Claude Code MCP"} {
				if tc.claude {
					require.Equal(t, Pass, checks[name].Status, checks)
				} else {
					require.NotContains(t, checks, name)
				}
			}
			for _, name := range []string{"Codex hooks", "Codex MCP"} {
				if tc.codex {
					require.Equal(t, Pass, checks[name].Status, checks)
				} else {
					require.NotContains(t, checks, name)
				}
			}
			if !tc.codex && !tc.claude {
				require.Equal(t, Fail, checks["Platform registration"].Status)
				require.Contains(t, checks["Platform registration"].Detail, "capy setup")
			}
			require.NoFileExists(t, filepath.Join(dir, ".capy/knowledge.db"))
			require.NoFileExists(t, filepath.Join(dir, ".capy/tool-observations.json"))
		})
	}

	for _, tc := range []struct {
		name, hookDetail, mcpDetail string
		hookStatus, mcpStatus       CheckStatus
		mutate                      func(*testing.T, string)
	}{
		{name: "missing hooks", hookStatus: Fail, mcpStatus: Pass, hookDetail: "missing", mutate: func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, codexHooksRelPath)))
		}},
		{name: "partial hooks", hookStatus: Warn, mcpStatus: Pass, hookDetail: "missing: SessionEnd", mutate: func(t *testing.T, dir string) {
			mutateDoctorHooks(t, dir, func(hooks map[string]any) { delete(hooks, "SessionEnd") })
		}},
		{name: "missing MCP", hookStatus: Pass, mcpStatus: Fail, mcpDetail: "missing owner-bound", mutate: func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, ".codex/config.toml")))
		}},
		{name: "absent wrapper", hookStatus: Pass, mcpStatus: Pass, mutate: func(t *testing.T, dir string) {
			require.NoFileExists(t, filepath.Join(dir, ".codex/scripts/capy.sh"))
		}},
		{name: "missing marker", hookStatus: Fail, mcpStatus: Fail, hookDetail: "no adjacent", mutate: func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, codexOwnerRelPath)))
		}},
		{name: "malformed marker", hookStatus: Fail, mcpStatus: Fail, hookDetail: "invalid", mutate: func(t *testing.T, dir string) {
			writeCodexFixture(t, dir, codexOwnerRelPath, "broken\n")
		}},
		{name: "wrong marker ID", hookStatus: Fail, mcpStatus: Fail, hookDetail: "disagrees", mutate: func(t *testing.T, dir string) {
			writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(strings.Repeat("f", 32)))
		}},
		{name: "malformed JSON", hookStatus: Fail, mcpStatus: Fail, hookDetail: "invalid", mutate: func(t *testing.T, dir string) {
			writeCodexFixture(t, dir, codexHooksRelPath, "{")
		}},
		{name: "wrong hook shape", hookStatus: Fail, mcpStatus: Fail, hookDetail: "groups must be an array", mutate: func(t *testing.T, dir string) {
			writeCodexFixture(t, dir, codexHooksRelPath, `{"hooks":{"PreToolUse":{}}}`)
		}},
		{name: "malformed TOML", hookStatus: Fail, mcpStatus: Fail, hookDetail: "invalid", mutate: func(t *testing.T, dir string) {
			writeCodexFixture(t, dir, ".codex/config.toml", "[")
		}},
		{name: "invalid bootstrap argv", hookStatus: Fail, mcpStatus: Fail, mcpDetail: "unsupported", mutate: func(t *testing.T, dir string) {
			replaceDoctorConfig(t, dir, `command = "capy"`, `command = "sh"`)
		}},
		{name: "wrong matcher", hookStatus: Fail, mcpStatus: Pass, hookDetail: "invalid matcher", mutate: func(t *testing.T, dir string) {
			mutateDoctorHooks(t, dir, func(hooks map[string]any) {
				hooks["PreToolUse"].([]any)[0].(map[string]any)["matcher"] = "Bash"
			})
		}},
		{name: "duplicate handler", hookStatus: Fail, mcpStatus: Pass, hookDetail: "duplicate owned handlers: SessionEnd", mutate: func(t *testing.T, dir string) {
			mutateDoctorHooks(t, dir, func(hooks map[string]any) {
				groups := hooks["SessionEnd"].([]any)
				hooks["SessionEnd"] = append(groups, groups[0])
			})
		}},
		{name: "aliased MCP", hookStatus: Pass, mcpStatus: Pass, mutate: func(t *testing.T, dir string) {
			replaceDoctorConfig(t, dir, "[mcp_servers.capy]", "[mcp_servers.alternate]")
		}},
		{name: "duplicate MCP", hookStatus: Pass, mcpStatus: Fail, mcpDetail: "duplicate owner-bound MCP", mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, ".codex/config.toml")
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			writeCodexFixture(t, dir, ".codex/config.toml", string(data)+"\n"+strings.ReplaceAll(string(data), "[mcp_servers.capy]", "[mcp_servers.copy]"))
		}},
		{name: "unused symlink wrapper", hookStatus: Pass, mcpStatus: Pass, mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, ".codex/scripts/capy.sh")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.Symlink(path+".real", path))
		}},
		{name: "symlink hook config", hookStatus: Fail, mcpStatus: Fail, hookDetail: "regular file", mutate: func(t *testing.T, dir string) {
			path := filepath.Join(dir, codexHooksRelPath)
			require.NoError(t, os.Rename(path, path+".real"))
			require.NoError(t, os.Symlink(path+".real", path))
		}},
		{name: "oversize config", hookStatus: Fail, mcpStatus: Fail, hookDetail: "exceeds", mutate: func(t *testing.T, dir string) {
			writeCodexFixture(t, dir, codexHooksRelPath, strings.Repeat(" ", codexConfigLimit+1))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, setupCodex(dir))
			tc.mutate(t, dir)
			checks := registrationChecks(t, dir)
			require.Equal(t, tc.hookStatus, checks["Codex hooks"].Status, checks)
			require.Equal(t, tc.mcpStatus, checks["Codex MCP"].Status, checks)
			require.Contains(t, checks["Codex hooks"].Detail, tc.hookDetail)
			require.Contains(t, checks["Codex MCP"].Detail, tc.mcpDetail)
		})
	}
}

func mutateDoctorHooks(t *testing.T, dir string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, codexHooksRelPath)
	root := mustReadJSONMap(t, path)
	mutate(root["hooks"].(map[string]any))
	require.NoError(t, writeJSONFile(path, root))
}

func replaceDoctorConfig(t *testing.T, dir, from, to string) {
	t.Helper()
	path := filepath.Join(dir, ".codex/config.toml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), from)
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(data), from, to)), 0o600))
}

func TestDoctorCodexCopiedRegistrations(t *testing.T) {
	for _, outer := range []string{"complete", "missing marker", "only hooks without marker", "only MCP without marker"} {
		t.Run(outer, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, setupCodex(root))
			nested := filepath.Join(root, "packages/service")
			require.NoError(t, os.MkdirAll(nested, 0o755))
			require.NoError(t, os.CopyFS(filepath.Join(nested, ".codex"), os.DirFS(filepath.Join(root, ".codex"))))
			if outer != "complete" {
				require.NoError(t, os.Remove(filepath.Join(root, codexOwnerRelPath)))
			}
			if outer == "only hooks without marker" {
				require.NoError(t, os.Remove(filepath.Join(root, ".codex/config.toml")))
			}
			if outer == "only MCP without marker" {
				require.NoError(t, os.Remove(filepath.Join(root, codexHooksRelPath)))
			}
			checks := registrationChecks(t, nested)
			for _, name := range []string{"Codex hooks", "Codex MCP"} {
				require.Equal(t, Fail, checks[name].Status)
				require.Contains(t, checks[name].Detail, root)
			}
			marker, err := os.ReadFile(filepath.Join(nested, codexOwnerRelPath))
			require.NoError(t, err)
			id, err := parseCodexOwner(marker)
			require.NoError(t, err)
			_, runtimeErr := ResolveCodexOwner(nested, id, "hook", "pretooluse")
			require.Error(t, runtimeErr)
			require.Contains(t, runtimeErr.Error(), root)
			require.Error(t, setupCodex(nested))
		})
	}
}

func TestDoctorCodexNestedRegistrations(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))
	require.NoError(t, setupCodex(root))
	require.NoError(t, setupCodex(nested))
	for _, dir := range []string{root, nested} {
		checks := registrationChecks(t, dir)
		require.Equal(t, Pass, checks["Codex hooks"].Status, checks)
		require.Equal(t, Pass, checks["Codex MCP"].Status, checks)
		require.Contains(t, checks["Codex hooks"].Detail, filepath.Join(dir, codexHooksRelPath))
	}
	// An incomplete child must not be reported healthy using the parent's MCP.
	require.NoError(t, os.Remove(filepath.Join(nested, ".codex/config.toml")))
	checks := registrationChecks(t, nested)
	require.Equal(t, Fail, checks["Codex MCP"].Status)
	require.Contains(t, checks["Codex MCP"].Detail, "missing owner-bound")
}

func TestDoctorCodexActivationLimits(t *testing.T) {
	for _, setting := range []string{"", "[features]\nhooks=false\n", "[features]\ncodex_hooks=false\n", "[features]\nhooks=true\ncodex_hooks=false\n"} {
		t.Run(setting, func(t *testing.T) {
			dir := t.TempDir()
			inline := "[[hooks.PreToolUse]]\nmatcher='^Bash$'\n[[hooks.PreToolUse.hooks]]\ntype='command'\ncommand='echo external'\n"
			writeCodexFixture(t, dir, ".codex/config.toml", setting+inline)
			require.NoError(t, setupCodex(dir))
			before, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
			require.NoError(t, err)
			checks := registrationChecks(t, dir)
			require.Equal(t, Pass, checks["Codex hooks"].Status)
			require.Equal(t, Pass, checks["Codex MCP"].Status)
			activation := checks["Codex activation"]
			require.Equal(t, Warn, activation.Status)
			require.Contains(t, activation.Detail, "project trust, exact hook trust and execution are unverified")
			require.Contains(t, activation.Detail, "inline PreToolUse hooks also configured")
			require.Contains(t, activation.Detail, "feature precedence and host trust records are not inspected")
			if setting != "" {
				require.Contains(t, activation.Detail, "= false")
			}
			after, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
			require.NoError(t, err)
			require.Equal(t, before, after, "diagnostics must not change features or hooks")
		})
	}
	t.Run("owned inline duplicate", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, setupCodex(dir))
		owners, err := InspectCodexOwners(dir)
		require.NoError(t, err)
		command, err := json.Marshal(codexHookCommand(owners[0].ID, "pretooluse"))
		require.NoError(t, err)
		path := filepath.Join(dir, ".codex/config.toml")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		writeCodexFixture(t, dir, ".codex/config.toml", string(data)+"\n[[hooks.PreToolUse]]\nmatcher='.*'\n[[hooks.PreToolUse.hooks]]\ntype='command'\ncommand="+string(command)+"\n")
		checks := registrationChecks(t, dir)
		require.Equal(t, Fail, checks["Codex hooks"].Status)
		require.Contains(t, checks["Codex hooks"].Detail, "duplicate owned handlers: PreToolUse")
	})
}

func TestDoctorPartialPlatformArtifacts(t *testing.T) {
	t.Run("unused wrapper is not registration", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, ".codex/scripts/capy.sh", "#!/bin/sh\nexit 99\n")
		checks := registrationChecks(t, dir)
		require.NotContains(t, checks, "Codex hooks")
		require.NotContains(t, checks, "Codex MCP")
		require.Contains(t, checks, "Platform registration")
	})
	for _, tc := range []struct {
		name, path, content, platform string
	}{
		{"marker only", codexOwnerRelPath, codexOwnerContent(testCodexOwnerID), "Codex"},
		{"old MCP only", ".codex/config.toml", "[mcp_servers.capy]\ncommand='bash'\nargs=['.codex/scripts/capy.sh','serve']\n", "Codex"},
		{"Claude MCP only", ".mcp.json", `{"mcpServers":{"capy":{"command":"bash"}}}`, "Claude Code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCodexFixture(t, dir, tc.path, tc.content)
			checks := registrationChecks(t, dir)
			require.Equal(t, Fail, checks[tc.platform+" hooks"].Status)
			require.Contains(t, checks, tc.platform+" MCP")
			require.NotContains(t, checks, "Platform registration")
		})
	}
	t.Run("ordinary subdirectory finds owner", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, setupCodex(root))
		nested := filepath.Join(root, "packages/service")
		writeCodexFixture(t, nested, ".codex/config.toml", "model='unrelated'\n")
		checks := registrationChecks(t, nested)
		require.Equal(t, Pass, checks["Codex hooks"].Status)
		require.Equal(t, Pass, checks["Codex MCP"].Status)
		require.Contains(t, checks["Codex hooks"].Detail, filepath.Join(root, codexHooksRelPath))
	})
}

func TestDoctorClaudeInvocationRecognition(t *testing.T) {
	for _, tc := range []struct {
		command string
		owned   bool
	}{
		{"capy hook pretooluse", true},
		{"/usr/local/bin/capy hook pretooluse", true},
		{"'/path with spaces/capy' hook pretooluse", true},
		{"bash $CLAUDE_PROJECT_DIR/.claude/scripts/capy.sh hook pretooluse", true},
		{"other-tool hook pretooluse", false},
		{"echo /usr/bin/capy hook pretooluse", false},
		{"capy-extra hook pretooluse", false},
		{"capy hook pretooluse-other", false},
	} {
		t.Run(tc.command, func(t *testing.T) {
			entries := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": tc.command}}}}
			require.Equal(t, tc.owned, hasClaudeCapyHook(entries, "pretooluse"))
		})
	}
}
