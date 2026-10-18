package server

import (
	"fmt"
	"os"
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

func callCwdTool(t *testing.T, srv *Server, tool string, cwd any, code string) *mcp.CallToolResult {
	t.Helper()
	args := map[string]any{"cwd": cwd, "language": "shell", "code": code}
	switch tool {
	case "execute":
		return callTool(t, srv, args)
	case "execute-file":
		args["path"] = "input.txt"
		return callExecuteFile(t, srv, args)
	default:
		concurrency := 1
		if tool == "parallel batch" {
			concurrency = 2
		}
		args["commands"] = []any{
			map[string]any{"label": "first " + code, "command": code},
			map[string]any{"label": "second " + code, "command": code},
		}
		args["queries"] = []any{"cwdmarker"}
		args["concurrency"] = concurrency
		return callBatch(t, srv, args)
	}
}

func TestExecutionCwdInvalidBeforeSideEffects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tool := range []string{"execute", "execute-file", "serial batch", "parallel batch"} {
		t.Run(tool, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				value any
			}{
				{name: "empty", value: ""},
				{name: "null", value: nil},
				{name: "number", value: 1},
				{name: "boolean", value: true},
				{name: "array", value: []any{"."}},
				{name: "object", value: map[string]any{}},
				{name: "missing", value: "missing"},
				{name: "file", value: "input.txt"},
				{name: "nul", value: "bad\x00path"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					srv := newTestServer(t, nil)
					require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "input.txt"), []byte("input"), 0o600))
					result := callCwdTool(t, srv, tool, tc.value, "printf cwdmarker > spawned")
					require.True(t, result.IsError, resultText(result))
					assert.Contains(t, resultText(result), "cwd")
					assert.NoFileExists(t, filepath.Join(srv.projectDir, "spawned"))
					assert.Nil(t, srv.store)
					assert.Nil(t, srv.vault)
					assert.Empty(t, srv.stats.Snapshot().Calls)
				})
			}
		})
	}
}

func TestExecutionCwdLocationsAndFileAnchor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tool := range []string{"execute", "execute-file", "serial batch", "parallel batch"} {
		t.Run(tool, func(t *testing.T) {
			srv := newTestServer(t, nil)
			require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "input.txt"), []byte("project-input"), 0o600))
			external := t.TempDir()
			relative := filepath.Join(srv.projectDir, "with spaces")
			require.NoError(t, os.MkdirAll(relative, 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(external, "child"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(external, "child"), filepath.Join(srv.projectDir, "alias")))
			for _, tc := range []struct{ name, cwd, dir string }{
				{name: "relative", cwd: "with spaces", dir: relative},
				{name: "absolute", cwd: external, dir: external},
				{name: "symlink parent", cwd: "alias/..", dir: external},
			} {
				t.Run(tc.name, func(t *testing.T) {
					require.NoError(t, os.WriteFile(filepath.Join(tc.dir, "input.txt"), []byte("wrong-cwd-input"), 0o600))
					code := "printf cwdmarker; pwd"
					if tool == "execute-file" {
						code += `; printf '\n%s\n' "$FILE_CONTENT"`
					}
					result := callCwdTool(t, srv, tool, tc.cwd, code)
					require.False(t, result.IsError, resultText(result))
					want, err := filepath.EvalSymlinks(tc.dir)
					require.NoError(t, err)
					output := resultText(result)
					if tool == "execute" || tool == "execute-file" {
						output = capturedOutput(t, srv, result, "stdout")
					}
					assert.Contains(t, output, "cwdmarker"+want)
					if tool == "execute-file" {
						assert.Contains(t, output, "project-input")
						assert.NotContains(t, output, "wrong-cwd-input")
					}
					if strings.Contains(tool, "batch") {
						sources, err := srv.getStore().ListSources()
						require.NoError(t, err)
						require.Len(t, sources, 1)
						chunks, err := srv.getStore().GetChunksBySource(sources[0].ID)
						require.NoError(t, err)
						require.Len(t, chunks, 2)
						for _, chunk := range chunks {
							assert.Contains(t, chunk.Content, "cwdmarker"+want)
						}
					}
				})
			}
		})
	}
}

func TestExecutionCwdConcurrentRequests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	processDir, err := os.Getwd()
	require.NoError(t, err)
	originalEnv := os.Environ()
	t.Run("overlapping requests", func(t *testing.T) {
		for i, tool := range []string{"execute", "execute-file", "serial batch", "parallel batch"} {
			require.NoError(t, os.WriteFile(filepath.Join(srv.projectDir, "input.txt"), []byte("input"), 0o600))
			for j := range 2 {
				t.Run(fmt.Sprintf("%s/%d", tool, j), func(t *testing.T) {
					t.Parallel()
					dir := t.TempDir()
					marker := fmt.Sprintf("cwdmarker%d%d", i, j)
					require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), []byte(marker), 0o600))
					// Distinct short labels avoid Task 18/D3's deferred batch-label
					// collision behavior while exercising one shared executor/store.
					result := callCwdTool(t, srv, tool, dir, "printf "+marker+"; cat marker; pwd")
					require.False(t, result.IsError, resultText(result))
					output := resultText(result)
					if tool == "execute" || tool == "execute-file" {
						output = capturedOutput(t, srv, result, "stdout")
					}
					assert.Contains(t, output, marker)
					want, err := filepath.EvalSymlinks(dir)
					require.NoError(t, err)
					assert.Contains(t, output, want)
				})
			}
		}
	})
	result := callTool(t, srv, map[string]any{"language": "shell", "code": "pwd"})
	require.False(t, result.IsError, resultText(result))
	want, err := filepath.EvalSymlinks(srv.projectDir)
	require.NoError(t, err)
	assert.Equal(t, want, strings.TrimSpace(capturedOutput(t, srv, result, "stdout")))
	after, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, processDir, after)
	assert.ElementsMatch(t, originalEnv, os.Environ())
}

func TestExecutionCwdPreservesProjectIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CAPY_VAULT_KEY", "")
	project, external := t.TempDir(), t.TempDir()
	const key = "synthetic-cwd-selected-key-123456789"
	const otherKey = "synthetic-cwd-external-key-987654321"
	writeExecuteFileRules(t, project, map[string][]string{"deny": {"Read(secret.txt)"}})
	writeExecuteFileRules(t, external, map[string][]string{"deny": {"Read(input.txt)"}})
	for _, dir := range []string{project, external} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "input.txt"), []byte("input"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(external, ".env"), []byte("CAPY_DB_KEY="+otherKey), 0o600))
	cfg := config.DefaultConfig()
	cfg.Store.Path = "knowledge.db"
	srv := NewServer(cfg, []security.SecurityPolicy{{Deny: []string{"Bash(touch *)"}}}, executor.NewExecutor(project, 0), project,
		WithKnowledgeCredentials(key, config.KeySource{Kind: config.KeySourceFile, Path: filepath.Join(project, "db.key")}))
	t.Cleanup(srv.shutdown)
	// Delay first storage access until after an override, with conflicting
	// credentials and settings in the execution directory.
	result := callCwdTool(t, srv, "parallel batch", external, "printf cwdmarker")
	require.False(t, result.IsError, resultText(result))
	assert.FileExists(t, filepath.Join(project, "knowledge.db"))
	assert.NoFileExists(t, filepath.Join(external, "knowledge.db"))
	assert.Equal(t, key, srv.knowledgeKey)
	assert.Equal(t, project, srv.projectDir)
	result = callCwdTool(t, srv, "execute-file", external, `printf '%s' "$FILE_CONTENT"`)
	require.False(t, result.IsError, resultText(result))
	for _, path := range []string{"secret.txt", filepath.Join(external, "input.txt")} {
		result = callExecuteFile(t, srv, map[string]any{"cwd": external, "path": path, "language": "shell", "code": "printf should-not-run"})
		require.True(t, result.IsError, resultText(result))
	}
	result = callCwdTool(t, srv, "execute", external, "touch blocked-marker")
	require.True(t, result.IsError, resultText(result))
	assert.NoFileExists(t, filepath.Join(external, "blocked-marker"))
	result = callIndex(t, srv, map[string]any{"path": "input.txt", "source": "selected-input"})
	require.False(t, result.IsError, resultText(result))
	result = callSearch(t, srv, map[string]any{"query": "input", "source": "selected-input"})
	require.False(t, result.IsError, resultText(result))
	assert.Contains(t, resultText(result), "selected-input")
	srv.shutdown()
	reopened := NewServer(cfg, nil, nil, project, WithKnowledgeCredentials(key, config.KeySource{}))
	t.Cleanup(reopened.shutdown)
	sources, err := reopened.getStore().ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 2)
}

func TestExecutionCwdPreservesVaultScope(t *testing.T) {
	projectA, uuidA, projectB, uuidB := setupVaultSweepMultiProject(t)
	t.Setenv("CAPY_VAULT_KEY", testVaultSweepKey)
	t.Setenv("CAPY_VAULT_SWEEP_ALL", "1")
	srv := newTestServerWithProjectDir(t, nil, projectA)
	srv.vaultSweep(t.Context())
	result := callCwdTool(t, srv, "execute", projectB, "pwd")
	require.False(t, result.IsError, resultText(result))
	result, err := srv.handleVaultSearch(t.Context(), vaultSearchReq(map[string]any{"queries": []any{"database", "scheduler"}}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(result))
	assert.Contains(t, resultText(result), uuidA)
	assert.NotContains(t, resultText(result), uuidB)
}
