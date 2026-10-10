package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stdioBooleanCall keeps tool-level errors observable without weakening the
// success-only assertion in stdioProcess.tool. JSON-RPC errors still fail here.
func stdioBooleanCall(t *testing.T, p *stdioProcess, name string, args map[string]any) (bool, string) {
	t.Helper()
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": name, "arguments": args}, &result))
	var text strings.Builder
	for _, content := range result.Content {
		if content.Type == "text" {
			text.WriteString(content.Text)
		}
	}
	require.False(t, p.containsSecret(text.String()), "credential appeared in tool content")
	return result.IsError, text.String()
}

func TestMCPStdioBooleanArgs(t *testing.T) {
	bin := buildStdioCapy(t)
	const key = "synthetic-stdio-boolean-passphrase-12345"
	launch := func(t *testing.T, project string, env []string) *stdioProcess {
		t.Helper()
		p := startStdioProcess(t, stdioCommand{
			executable: bin, args: []string{"serve", "--project-dir", project},
			dir: project, env: env, secrets: []string{key},
		})
		p.initialize(t)
		return p
	}

	t.Run("invalid inputs and destructive strings", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project, stdioEnv(t, key))
		stdioIndex(t, p, "booleanbadger")
		type toolCase struct {
			tool string
			flag string
			args map[string]any
		}
		cases := []toolCase{
			{tool: "capy_execute", flag: "background", args: map[string]any{"language": "shell", "code": "printf spawned > boolean-spawn"}},
			{tool: "capy_fetch_and_index", flag: "force", args: map[string]any{"url": "http://127.0.0.1:1/boolean"}},
			{tool: "capy_fetch_and_index", flag: "force", args: map[string]any{"requests": []any{map[string]any{"url": "http://127.0.0.1:1/boolean"}}}},
			{tool: "capy_search", flag: "all_projects", args: map[string]any{"query": "transport orchard", "source": "stdio-fixture", "project": "*"}},
			{tool: "capy_vault_search", flag: "all_projects", args: map[string]any{"query": "transport orchard", "project": "*"}},
		}
		for _, flag := range []string{"dry_run", "purge_ephemeral", "purge_session", "purge_all", "optimize", "vacuum"} {
			cases = append(cases, toolCase{
				tool: "capy_cleanup", flag: flag,
				args: map[string]any{"dry_run": false, "purge_all": true, "optimize": true, "vacuum": true},
			})
		}
		for _, tc := range cases {
			t.Run(tc.tool+"/"+tc.flag, func(t *testing.T) {
				for _, value := range []any{nil, 0, 1, 0.5, "", "yes", "1", "t", "F", []any{true}, map[string]any{}} {
					args := map[string]any{}
					for k, v := range tc.args {
						args[k] = v
					}
					args[tc.flag] = value
					isError, text := stdioBooleanCall(t, p, tc.tool, args)
					require.True(t, isError, text)
					require.Contains(t, text, `invalid parameter "`+tc.flag+`"`)
				}
			})
		}
		assert.NoFileExists(t, filepath.Join(project, "boolean-spawn"))
		// This search also proves invalid all_projects calls did not consume the
		// shared budget. Its query does not contain the retained marker.
		stdioSearch(t, p, "booleanbadger", "absentotter")
		for _, args := range []map[string]any{
			{"purge_all": " TrUe "},
			{"purge_all": " TrUe ", "dry_run": " TrUe "},
		} {
			text := p.tool(t, "capy_cleanup", args)
			assert.Contains(t, text, "Would purge 1 sources")
		}
		text := p.tool(t, "capy_cleanup", map[string]any{"purge_all": " TrUe ", "dry_run": " FaLsE "})
		assert.Contains(t, text, "Purged 1 sources")
		text = p.tool(t, "capy_doctor", map[string]any{})
		assert.Contains(t, text, "Knowledge base: 0 sources")
		p.close(t)
		stdioCheckpointed(t, project)
	})

	t.Run("background and force strings", func(t *testing.T) {
		project := stdioProject(t)
		p := launch(t, project, stdioEnv(t, key))
		for _, tc := range []struct {
			value string
			want  string
		}{
			{value: " TrUe ", want: "process backgrounded"},
			{value: " FaLsE ", want: "timed out after"},
		} {
			text := p.tool(t, "capy_execute", map[string]any{
				"language": "shell", "code": "printf started; sleep 30", "timeout": 1000, "background": tc.value,
			})
			assert.Contains(t, text, tc.want)
		}

		// Seed a durable cache entry for a loopback URL. False must use it;
		// true must bypass it and reach the real SSRF guard, with no HTTP server
		// or production-policy override in this stdio fixture.
		const url = "http://127.0.0.1:1/boolean"
		p.tool(t, "capy_index", map[string]any{"content": "Retained fetch marker", "source": "boolean-cache|" + url})
		for _, mode := range []string{"single", "batch"} {
			t.Run(mode, func(t *testing.T) {
				args := map[string]any{"url": url, "source": "boolean-cache", "kind": "durable", "force": " FaLsE "}
				if mode == "batch" {
					args = map[string]any{
						"requests": []any{map[string]any{"url": url, "source": "boolean-cache"}},
						"kind":     "durable", "force": " FaLsE ",
					}
				}
				text := p.tool(t, "capy_fetch_and_index", args)
				assert.Contains(t, strings.ToLower(text), "cache hit")
				args["force"] = " TrUe "
				_, text = stdioBooleanCall(t, p, "capy_fetch_and_index", args)
				assert.NotContains(t, strings.ToLower(text), "cache hit")
				assert.Contains(t, text, "loopback")
			})
		}
		p.close(t)
	})

	t.Run("search scope strings", func(t *testing.T) {
		project := stdioProject(t)
		const vaultKey = "synthetic-stdio-boolean-vault-passphrase-12345"
		env := append(stdioEnv(t, key), "CAPY_VAULT_KEY="+vaultKey, "CAPY_MACHINE_ID=boolean-test")
		root := t.TempDir()
		const uuid = "abcdefab-1111-2222-3333-444444444444"
		line, err := json.Marshal(map[string]any{
			"type": "user", "uuid": "boolean-message", "sessionId": uuid,
			"cwd": t.TempDir(), "timestamp": "2026-10-10T12:00:00Z",
			"message": map[string]any{
				"role": "user", "content": "The distant orchard holds a silvercapybara. " + strings.Repeat("Orchard context. ", 30),
			},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, uuid+".jsonl"), append(line, '\n'), 0o600))
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		for _, args := range [][]string{{"vault", "import", "--source", root}, {"vault", "reindex"}} {
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env, cmd.Dir = env, project
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
		}
		p := launch(t, project, env)
		// Keep knowledge populated so a properly scoped miss is a successful
		// no-result response, not the empty-knowledge onboarding error.
		stdioIndex(t, p, "localbadger")
		for _, tool := range []string{"capy_search", "capy_vault_search"} {
			t.Run(tool, func(t *testing.T) {
				for _, invalid := range []any{nil, 0, 1, "yes", "t"} {
					isError, text := stdioBooleanCall(t, p, tool, map[string]any{
						"query": "distant orchard", "all_projects": invalid, "project": "*",
					})
					require.True(t, isError, text)
					assert.Contains(t, text, `invalid parameter "all_projects"`)
					assert.NotContains(t, text, uuid)
				}
				for _, tc := range []struct {
					value string
					wide  bool
				}{
					{value: " FaLsE "},
					{value: " TrUe ", wide: true},
				} {
					text := p.tool(t, tool, map[string]any{"query": "distant orchard", "all_projects": tc.value})
					assert.Equal(t, tc.wide, strings.Contains(text, "session:"+uuid), text)
					assert.Equal(t, tc.wide, strings.Contains(text, "silvercapybara"), text)
				}
			})
		}
		p.close(t)
	})
}
