package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3" // needed for FTS5 check

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckVersion(t *testing.T) {
	r := CheckVersion()
	assert.Equal(t, "Version", r.Name)
	assert.Equal(t, Pass, r.Status)
}

func TestCheckRuntimes(t *testing.T) {
	t.Run("no runtimes", func(t *testing.T) {
		r := CheckRuntimes(nil, 11)
		assert.Equal(t, Fail, r.Status)
		assert.Contains(t, r.Detail, "none detected")
	})

	t.Run("one runtime", func(t *testing.T) {
		r := CheckRuntimes(map[string]string{"shell": "/bin/bash"}, 11)
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "1/11")
	})

	t.Run("multiple runtimes", func(t *testing.T) {
		r := CheckRuntimes(map[string]string{
			"shell":  "/bin/bash",
			"python": "/usr/bin/python3",
			"go":     "/usr/local/go/bin/go",
		}, 11)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "3/11")
	})
}

func TestCheckFTS5(t *testing.T) {
	r := CheckFTS5()
	// Should pass when built with -tags fts5
	assert.Equal(t, Pass, r.Status)
	assert.Contains(t, r.Detail, "available")
}

func TestCheckConfig(t *testing.T) {
	t.Run("with db path", func(t *testing.T) {
		r := CheckConfig("/tmp/project", "/tmp/data/knowledge.db")
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "knowledge.db")
	})

	t.Run("defaults", func(t *testing.T) {
		r := CheckConfig("/tmp/project", "")
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "defaults")
	})
}

func TestCheckHookRegistration(t *testing.T) {
	t.Run("no settings file", func(t *testing.T) {
		dir := t.TempDir()
		r := CheckHookRegistration(dir)
		assert.Equal(t, Fail, r.Status)
		assert.Contains(t, r.Detail, "no capy hooks found")
	})

	t.Run("no hooks configured", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(claudeDir, "settings.json"),
			[]byte(`{"permissions": {}}`),
			0o644,
		))

		r := CheckHookRegistration(dir)
		assert.Equal(t, Fail, r.Status)
		assert.Contains(t, r.Detail, "no capy hooks found")
	})

	t.Run("all hooks registered in settings.json", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0o755))

		require.NoError(t, mergeHooks(filepath.Join(claudeDir, "settings.json")))

		r := CheckHookRegistration(dir)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "6/6")
		assert.Contains(t, r.Detail, "settings.json")
	})

	t.Run("all hooks registered in settings.local.json", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0o755))

		require.NoError(t, mergeHooks(filepath.Join(claudeDir, "settings.local.json")))

		r := CheckHookRegistration(dir)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "6/6")
		assert.Contains(t, r.Detail, "settings.local.json")
	})

	t.Run("detects old hardcoded-path hooks", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0o755))

		// Old-format hooks with hardcoded binary path
		oldHooks := make(map[string]any)
		for _, he := range hookEvents {
			oldHooks[he.Event] = []any{
				map[string]any{
					"matcher": he.Matcher,
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "/opt/homebrew/bin/capy hook " + he.CLIArg,
						},
					},
				},
			}
		}
		settings := map[string]any{"hooks": oldHooks}
		data, _ := json.MarshalIndent(settings, "", "  ")
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

		r := CheckHookRegistration(dir)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "6/6")
	})

	t.Run("detects new wrapper-format hooks", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0o755))

		// New-format hooks with portable wrapper script
		newHooks := make(map[string]any)
		for _, he := range hookEvents {
			newHooks[he.Event] = []any{
				map[string]any{
					"matcher": he.Matcher,
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "bash $CLAUDE_PROJECT_DIR/" + capyWrapperRelPath + " hook " + he.CLIArg,
						},
					},
				},
			}
		}
		settings := map[string]any{"hooks": newHooks}
		data, _ := json.MarshalIndent(settings, "", "  ")
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

		r := CheckHookRegistration(dir)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "6/6")
	})

	t.Run("partial hooks", func(t *testing.T) {
		dir := t.TempDir()
		claudeDir := filepath.Join(dir, ".claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0o755))

		// Write only PreToolUse hook
		settings := map[string]any{
			"hooks": map[string]any{
				"PreToolUse": []any{
					map[string]any{
						"matcher": PreToolUseMatcherPattern,
						"hooks": []any{
							map[string]any{
								"type":    "command",
								"command": "/usr/local/bin/capy hook pretooluse",
							},
						},
					},
				},
			},
		}
		data, _ := json.MarshalIndent(settings, "", "  ")
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

		r := CheckHookRegistration(dir)
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "1/6")
	})
}

func TestCheckMCPRegistration(t *testing.T) {
	t.Run("no mcp file", func(t *testing.T) {
		dir := t.TempDir()
		r := CheckMCPRegistration(dir)
		assert.Equal(t, Fail, r.Status)
		assert.Contains(t, r.Detail, "not found")
	})

	t.Run("capy not registered", func(t *testing.T) {
		dir := t.TempDir()
		mcp := map[string]any{
			"mcpServers": map[string]any{
				"other": map[string]any{"command": "/usr/bin/other"},
			},
		}
		data, _ := json.MarshalIndent(mcp, "", "  ")
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), data, 0o644))

		r := CheckMCPRegistration(dir)
		assert.Equal(t, Fail, r.Status)
		assert.Contains(t, r.Detail, "not registered")
	})

	t.Run("capy registered", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, mergeMCPServer(filepath.Join(dir, ".mcp.json")))

		r := CheckMCPRegistration(dir)
		// Status depends on whether the binary path exists on this system
		assert.NotEqual(t, Fail, r.Status)
	})
}

func TestCheckSecurity(t *testing.T) {
	t.Run("no policies", func(t *testing.T) {
		r := CheckSecurity(0, 0)
		assert.Equal(t, Warn, r.Status)
	})

	t.Run("with policies", func(t *testing.T) {
		r := CheckSecurity(5, 2)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "2 policy files")
		assert.Contains(t, r.Detail, "5 deny patterns")
	})
}

func TestCheckKnowledgeBase(t *testing.T) {
	t.Run("not exists", func(t *testing.T) {
		r := CheckKnowledgeBase("/nonexistent/path/kb.db")
		assert.Equal(t, Warn, r.Status)
		assert.Contains(t, r.Detail, "not initialized")
	})

	t.Run("exists", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "knowledge.db")
		require.NoError(t, os.WriteFile(dbPath, []byte("test"), 0o644))

		r := CheckKnowledgeBase(dbPath)
		assert.Equal(t, Pass, r.Status)
		assert.Contains(t, r.Detail, "exists")
	})
}

func TestCheckResultMarker(t *testing.T) {
	assert.Equal(t, "[x]", CheckResult{Status: Pass}.Marker())
	assert.Equal(t, "[-]", CheckResult{Status: Warn}.Marker())
	assert.Equal(t, "[ ]", CheckResult{Status: Fail}.Marker())
}

func TestFormatDiagnostics(t *testing.T) {
	results := []CheckResult{
		{Name: "Version", Status: Pass, Detail: "1.0.0"},
		{Name: "FTS5", Status: Fail, Detail: "unavailable"},
	}

	output := FormatDiagnostics(results)
	assert.Contains(t, output, "## capy doctor")
	assert.Contains(t, output, "- [x] Version: 1.0.0")
	assert.Contains(t, output, "- [ ] FTS5: unavailable")
}
