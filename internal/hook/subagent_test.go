package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildRoutingUnknownCapabilities(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	// Local definitions are not authoritative: both a fixed-tool and an
	// inherit-all definition remain unknown at this boundary.
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", "agents", "fixed.md"), []byte("---\ntools: Bash, WebFetch\n---\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", "agents", "inherited.md"), []byte("---\nname: inherited\n---\n"), 0o600))
	for _, identity := range []struct {
		name, id, kind string
	}{
		{name: "main"},
		{name: "type only main", kind: "fixed"},
		{name: "fixed child", id: "child-1", kind: "fixed"},
		{name: "inherited child", id: "child-2", kind: "inherited"},
		{name: "unknown plugin child", id: "child-3", kind: "plugin:custom"},
		{name: "untyped child", id: "child-4"},
	} {
		t.Run(identity.name, func(t *testing.T) {
			for _, call := range []struct {
				name, tool string
				input      map[string]any
			}{
				{name: "curl", tool: "Bash", input: map[string]any{"command": "curl https://example.com"}},
				{name: "wget alias", tool: "run_shell_command", input: map[string]any{"command": "wget https://example.com"}},
				{name: "inline HTTP", tool: "Bash", input: map[string]any{"command": `node -e "fetch('https://example.com')"`}},
				{name: "web page", tool: "WebFetch", input: map[string]any{"url": "https://example.com"}},
				{name: "git issue", tool: "WebFetch", input: map[string]any{"url": "https://github.com/owner/repo/issues/1"}},
			} {
				t.Run(call.name, func(t *testing.T) {
					input := hookInput(t, call.tool, call.input, map[string]any{"agent_id": identity.id, "agent_type": identity.kind})
					output, err := handleEvent("pretooluse", input, ccAdapter(), &project)
					require.NoError(t, err)
					hso := ccParse(t, output)
					assert.NotContains(t, hso, "updatedInput")
					if identity.id == "" {
						assert.Equal(t, "deny", hso["permissionDecision"])
						if call.name == "git issue" {
							assert.Contains(t, hso["permissionDecisionReason"], "gh issue view")
						}
					} else {
						assert.NotContains(t, hso, "permissionDecision", "advice must not override native permissions")
						assert.Contains(t, hso["additionalContext"], "unverified")
						assert.Contains(t, hso["additionalContext"], "once")
						assert.Contains(t, hso["additionalContext"], "native tools")
						assert.Contains(t, hso["additionalContext"], "platform CLI")
					}
				})
			}
		})
	}
}

func TestChildRoutingPreservesSecurity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".claude", "settings.json"),
		[]byte(`{"permissions":{"deny":["Bash(curl *)"],"ask":["Bash(wget *)"]}}`), 0o600))
	for _, tool := range []string{"Bash", "capy_execute"} {
		for _, tt := range []struct{ command, decision string }{
			{command: "curl https://example.com", decision: "deny"},
			{command: "wget https://example.com", decision: "ask"},
		} {
			t.Run(tool+"/"+tt.decision, func(t *testing.T) {
				args := map[string]any{"command": tt.command, "language": "shell", "code": tt.command}
				input := hookInput(t, tool, args, map[string]any{"agent_id": "child", "agent_type": "fixed", "cwd": 42})
				output, err := handleEvent("pretooluse", input, ccAdapter(), &project)
				require.NoError(t, err)
				hso := ccParse(t, output)
				assert.Equal(t, tt.decision, hso["permissionDecision"])
				assert.NotContains(t, hso, "additionalContext")
				if tt.decision == "deny" {
					assert.Contains(t, hso["permissionDecisionReason"], "security policy")
				}
			})
		}
	}
}
