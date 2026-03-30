package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestServer(t *testing.T, policies []security.SecurityPolicy) *Server {
	t.Helper()
	return newTestServerWithProjectDir(t, policies, "")
}

func newTestServerWithProjectDir(t *testing.T, policies []security.SecurityPolicy, projectDir string) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	if projectDir == "" {
		projectDir = t.TempDir()
	}
	cfg.Store.Path = filepath.Join(projectDir, "test.db")
	exec := executor.NewExecutor(projectDir, 0)
	srv := NewServer(cfg, policies, exec, projectDir)
	t.Cleanup(func() { srv.shutdown() })
	return srv
}

func callTool(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleExecute(context.Background(), req)
	require.NoError(t, err)
	return result
}

func resultText(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func TestExecute_Shell_Success(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo hello world",
	})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "hello world")
}

func TestExecute_Python_Success(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callTool(t, srv, map[string]any{
		"language": "python",
		"code":     "print('hello from python')",
	})
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "hello from python")
}

func TestExecute_SecurityDeny_Shell(t *testing.T) {
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(sudo *)"}},
	}
	srv := newTestServer(t, policies)
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "sudo rm -rf /",
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "blocked by security policy")
}

func TestExecute_SecurityDeny_ShellEscape(t *testing.T) {
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(rm -rf *)"}},
	}
	srv := newTestServer(t, policies)
	r := callTool(t, srv, map[string]any{
		"language": "python",
		"code":     `import os; os.system("rm -rf /tmp")`,
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "blocked by security policy")
	assert.Contains(t, resultText(r), "embedded shell command")
}

func TestExecute_NonZeroExit(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "exit 2",
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "Exit code: 2")
}

func TestExecute_SoftFail_GrepNoMatch(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo 'some output'; exit 1",
	})
	// Shell exit 1 with stdout is soft fail
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "some output")
}

func TestExecute_MissingCode(t *testing.T) {
	srv := newTestServer(t, nil)
	r := callTool(t, srv, map[string]any{
		"language": "shell",
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "missing required parameter")
}

func TestExecute_IntentSearch(t *testing.T) {
	srv := newTestServer(t, nil)
	// Generate output > 5KB
	bigOutput := strings.Repeat("line of test output\n", 300) // ~6KB
	r := callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo '" + bigOutput + "'",
		"intent":   "test output",
	})
	assert.False(t, r.IsError)
	text := resultText(r)
	// Should have been indexed, not returned raw
	assert.Contains(t, text, "Indexed")
	assert.Contains(t, text, "knowledge base")
}

func TestIntentSearch_IndexErrorTruncates(t *testing.T) {
	// Create a server with a store path that will fail to init
	cfg := config.DefaultConfig()
	cfg.Store.Path = "/dev/null/impossible/path.db"
	srv := NewServer(cfg, nil, nil, t.TempDir())

	bigOutput := strings.Repeat("x", 10000)
	result := srv.intentSearch(bigOutput, "test", "source", 5)

	// Should NOT return full 10KB output — should truncate
	assert.Less(t, len(result), 4000, "should truncate output on index failure")
	assert.Contains(t, result, "indexing failed")
}

func TestExecute_StatsTracking(t *testing.T) {
	srv := newTestServer(t, nil)
	callTool(t, srv, map[string]any{
		"language": "shell",
		"code":     "echo stats_test",
	})
	snap := srv.stats.Snapshot()
	assert.Equal(t, 1, snap.Calls["capy_execute"])
	assert.Greater(t, snap.BytesReturned["capy_execute"], int64(0))
	assert.Greater(t, snap.BytesSandboxed, int64(0))
}
