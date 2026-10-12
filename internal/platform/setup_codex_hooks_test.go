package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexHookMatchers(t *testing.T) {
	pre := regexp.MustCompile(codexHookEvents[1].Matcher)
	for _, tool := range []string{"execute", "execute_file", "batch_execute", "fetch_and_index", "index", "search", "vault_search", "stats", "doctor", "cleanup"} {
		for _, alias := range []string{"capy", "another_server", "my.server-v2"} {
			name := "mcp__" + alias + "__capy_" + tool
			t.Run(name, func(t *testing.T) { require.True(t, pre.MatchString(name)) })
		}
	}
	require.True(t, pre.MatchString("Bash"))
	for _, name := range []string{"BashExtra", "OtherBash", "mcp__capy__unrelated", "mcp__capy_server__list", "mcp____capy_execute", "mcp__capy__capy_execute_extra", "prefix_mcp__capy__capy_execute", "spawn_agent", "Agent", "WebFetch", "exec_command", "mcp__space alias__capy_execute"} {
		t.Run(name, func(t *testing.T) { require.False(t, pre.MatchString(name)) })
	}
	startup := regexp.MustCompile(codexHookEvents[0].Matcher)
	for _, source := range []string{"startup", "resume", "clear", "compact", "fork", "future-source", ""} {
		t.Run(source, func(t *testing.T) { require.True(t, startup.MatchString(source)) })
	}
	post := regexp.MustCompile(codexHookEvents[3].Matcher)
	for _, alias := range []string{"capy", "another_server", "my.server-v2"} {
		for _, tool := range CapyToolNames {
			require.Equal(t, tool == "capy_execute" || tool == "capy_fetch_and_index", post.MatchString("mcp__"+alias+"__"+tool), tool)
		}
	}
	for _, name := range []string{"capy_execute", "Bash", "mcp____capy_execute", "prefix_mcp__capy__capy_execute", "mcp__capy__capy_execute_extra"} {
		require.False(t, post.MatchString(name), name)
	}
	for _, index := range []int{2, 4} {
		matcher := regexp.MustCompile(codexHookEvents[index].Matcher)
		for _, value := range []string{"worker", "explorer", "custom", "other", "clear", ""} {
			require.True(t, matcher.MatchString(value))
		}
	}
}

func TestCodexHookContextConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	require.NoError(t, mergeCodexHooks(path, testCodexOwnerID))
	root := mustReadJSONMap(t, path)
	hooks := root["hooks"].(map[string]any)
	require.Len(t, hooks, 5)
	for _, event := range codexHookEvents {
		groups := hooks[event.Event].([]any)
		require.Len(t, groups, 1)
		group := groups[0].(map[string]any)
		require.Equal(t, event.Matcher, group["matcher"])
		handler := group["hooks"].([]any)[0].(map[string]any)
		require.NotContains(t, handler, "additionalContextLimit")
		binding, owned, err := recognizeCodexHook(handler["command"].(string))
		require.NoError(t, err)
		require.True(t, owned)
		require.Equal(t, event.CLIArg, binding.Event)
		require.Equal(t, testCodexOwnerID, binding.ID)
	}
}

func TestMergeCodexHooks(t *testing.T) {
	t.Run("preserves external siblings and metadata while collapsing duplicates", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "hooks.json")
		owned := map[string]any{"type": "command", "command": codexHookCommand(testCodexOwnerID, "pretooluse"), "additionalContextLimit": 10000}
		external := map[string]any{"type": "command", "command": "echo independent capy hook mention", "timeout": 12, "custom": "keep"}
		startup := map[string]any{"matcher": "startup|resume", "hooks": []any{map[string]any{"type": "command", "command": "bash toolbox-session-start.sh", "additionalContextLimit": 10000}}}
		future := []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "prompt", "prompt": "external future handler"}}}}
		root := map[string]any{
			"description": "external metadata", "large": json.Number("9007199254740993"),
			"mcp_servers": "unrelated hooks-file metadata",
			"hooks": map[string]any{
				"SessionStart": []any{startup}, "FutureEvent": future,
				"PreToolUse": []any{
					map[string]any{"matcher": "Bash", "group-note": "keep mixed", "hooks": []any{owned, external}},
					map[string]any{"matcher": "old", "hooks": []any{owned, owned}},
					map[string]any{"note": "keep duplicate metadata", "hooks": []any{owned}},
				},
			},
		}
		require.NoError(t, writeJSONFile(path, root))
		require.NoError(t, mergeCodexHooks(path, testCodexOwnerID))
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		merged, err := decodeCodexJSON(data)
		require.NoError(t, err)
		require.Equal(t, root["description"], merged["description"])
		require.Equal(t, root["large"], merged["large"])
		require.Equal(t, root["mcp_servers"], merged["mcp_servers"])
		hooks := merged["hooks"].(map[string]any)
		// Compare through normal JSON decoding to ignore test-side int/Number types.
		plain := mustReadJSONMap(t, path)["hooks"].(map[string]any)
		expected, _ := json.Marshal(future)
		actual, _ := json.Marshal(plain["FutureEvent"])
		require.JSONEq(t, string(expected), string(actual))
		expected, _ = json.Marshal(startup)
		actual, _ = json.Marshal(plain["SessionStart"].([]any)[0])
		require.JSONEq(t, string(expected), string(actual))
		groups := hooks["PreToolUse"].([]any)
		require.Len(t, groups, 3)
		mixed := groups[0].(map[string]any)
		require.Equal(t, "Bash", mixed["matcher"])
		require.Equal(t, "keep mixed", mixed["group-note"])
		require.Len(t, mixed["hooks"], 1)
		require.Equal(t, "echo independent capy hook mention", mixed["hooks"].([]any)[0].(map[string]any)["command"])
		require.Equal(t, "keep duplicate metadata", groups[1].(map[string]any)["note"])
		bindings, err := codexHookBindings(merged, path)
		require.NoError(t, err)
		require.Len(t, bindings, 5)
		last := groups[2].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		require.NotContains(t, last, "additionalContextLimit")
		require.NoError(t, mergeCodexHooks(path, testCodexOwnerID))
		again, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, data, again)
	})
	for _, tc := range []struct{ name, content string }{
		{"invalid JSON", "{"}, {"null", "null"}, {"wrong hooks", `{"hooks":false}`},
		{"wrong groups", `{"hooks":{"PreToolUse":{}}}`},
		{"wrong handlers", `{"hooks":{"PreToolUse":[{"hooks":null}]}}`},
		{"missing handler type", `{"hooks":{"PreToolUse":[{"hooks":[{"command":"echo external"}]}]}}`},
		{"empty handler type", `{"hooks":{"PreToolUse":[{"hooks":[{"type":""}]}]}}`},
		{"wrong matcher", `{"hooks":{"PreToolUse":[{"matcher":true,"hooks":[]}]}}`},
		{"duplicate JSON", `{"hooks":{},"hooks":{}}`},
		{"mismatched owner", codexHookFixture(t, strings.Repeat("f", 32))},
		{"unsupported version", strings.ReplaceAll(codexHookFixture(t, testCodexOwnerID), codexLaunchVersion, "capy-codex-launch-v99")},
		{"wrong event", strings.ReplaceAll(codexHookFixture(t, testCodexOwnerID), "PreToolUse", "SessionStart")},
		{"merge would exceed read limit", `{"description":"` + strings.Repeat("x", codexConfigLimit-100) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
			require.Error(t, mergeCodexHooks(path, testCodexOwnerID))
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.content, string(after))
		})
	}
}

func TestCodexMCPMergePreservesOversizeInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "#" + strings.Repeat("x", codexConfigLimit-100) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	require.ErrorContains(t, mergeCodexMCPServer(path, testCodexOwnerID), "would exceed")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, string(got))
}

func TestSetupCodexHooks(t *testing.T) {
	for _, partial := range []string{"fresh", "marker only", "MCP only", "hooks only"} {
		t.Run(partial, func(t *testing.T) {
			dir := t.TempDir()
			if partial != "fresh" {
				writeCodexFixture(t, dir, codexOwnerRelPath, codexOwnerContent(testCodexOwnerID))
			}
			if partial == "MCP only" {
				writeCodexFixture(t, dir, ".codex/config.toml", codexMCPBlock(testCodexOwnerID))
			}
			if partial == "hooks only" {
				writeCodexFixture(t, dir, codexHooksRelPath, codexHookFixture(t, testCodexOwnerID))
			}
			require.NoError(t, SetupCodex("capy", dir))
			owners, err := InspectCodexOwners(dir)
			require.NoError(t, err)
			require.Len(t, owners, 1)
			require.Len(t, owners[0].Bindings, 6)
			before, err := os.ReadFile(filepath.Join(dir, codexHooksRelPath))
			require.NoError(t, err)
			require.NoError(t, SetupCodex("capy", dir))
			after, err := os.ReadFile(filepath.Join(dir, codexHooksRelPath))
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.NoDirExists(t, filepath.Join(dir, ".claude"))
		})
	}
	t.Run("preserves disabled feature inline hooks and external scripts", func(t *testing.T) {
		dir := t.TempDir()
		prefix := "[features]\nhooks=false\n[[hooks.SessionStart]]\nmatcher='.*'\n[[hooks.SessionStart.hooks]]\ntype='command'\ncommand='echo independent inline'\n"
		writeCodexFixture(t, dir, ".codex/config.toml", prefix)
		writeCodexFixture(t, dir, ".codex/scripts/session-start.sh", "echo keep external\n")
		require.NoError(t, SetupCodex("capy", dir))
		config, err := os.ReadFile(filepath.Join(dir, ".codex/config.toml"))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(config), prefix))
		script, err := os.ReadFile(filepath.Join(dir, ".codex/scripts/session-start.sh"))
		require.NoError(t, err)
		require.Equal(t, "echo keep external\n", string(script))
	})
	t.Run("invalid existing hook file remains intact", func(t *testing.T) {
		dir := t.TempDir()
		writeCodexFixture(t, dir, codexHooksRelPath, `{"hooks":false}`)
		require.Error(t, SetupCodex("capy", dir))
		data, err := os.ReadFile(filepath.Join(dir, codexHooksRelPath))
		require.NoError(t, err)
		require.Equal(t, `{"hooks":false}`, string(data))
		require.NoFileExists(t, filepath.Join(dir, codexOwnerRelPath))
	})
}
