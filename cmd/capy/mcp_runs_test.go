package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

type stdioRunResponse struct {
	IsError    bool            `json:"isError"`
	Structured runstore.Result `json:"structuredContent"`
	Content    []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func (p *stdioProcess) capturedRun(t *testing.T, args map[string]any) stdioRunResponse {
	t.Helper()
	var result stdioRunResponse
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_execute", "arguments": args}, &result))
	return result
}

func (p *stdioProcess) rawRun(t *testing.T, id, stream string) []byte {
	t.Helper()
	var output []byte
	cursor := ""
	for {
		args := map[string]any{"action": "read", "run_id": id, "raw": true, "stream": stream, "max_bytes": 1024}
		if cursor != "" {
			args["cursor"] = cursor
		}
		var response struct {
			IsError    bool              `json:"isError"`
			Structured runstore.ReadPage `json:"structuredContent"`
		}
		require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": args}, &response))
		require.False(t, response.IsError)
		for _, fragment := range response.Structured.Fragments {
			output = append(output, fragment.Data...)
		}
		if response.Structured.EOF {
			break
		}
		require.NotNil(t, response.Structured.NextCursor)
		cursor = *response.Structured.NextCursor
	}
	return output
}

// executeOutput explicitly opts into unsanitized stdout for fixtures whose
// assertion concerns execution/cwd. It does not change the normal tool helper.
func (p *stdioProcess) executeOutput(t *testing.T, args map[string]any) string {
	t.Helper()
	result := p.capturedRun(t, args)
	require.False(t, result.IsError, "%+v", result)
	require.NotNil(t, result.Structured.RunID)
	return string(p.rawRun(t, *result.Structured.RunID, "stdout"))
}

func TestMCPRunCaptureRecovery(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("captured supervision requires Linux")
	}
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	const key = "synthetic-captured-project-credential-12345678"
	const changed = "synthetic-changed-project-credential-87654321"
	keyPath := filepath.Join(project, "db.key")
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".capy.toml"), []byte("[store]\npath='knowledge.db'\nkey_file='db.key'\n"), 0o600))
	launch := func(t *testing.T) *stdioProcess {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: stdioEnv(t, "unrelated-inherited-key"), secrets: []string{key, changed}})
		p.initialize(t)
		return p
	}
	p := launch(t)
	// The already-started service must transfer its captured key, not reread
	// this changed file or inherit the unrelated environment credential.
	require.NoError(t, os.WriteFile(keyPath, []byte(changed), 0o600))
	result := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf 'first\\000\\377last\\n'; printf 'failure diagnostic' >&2; printf x >> executed-once; exit 7"})
	require.True(t, result.IsError)
	run := result.Structured
	require.NotNil(t, run.RunID)
	require.Equal(t, 7, *run.ExitCode)
	require.Equal(t, "complete", run.Capture.State)
	require.True(t, run.Capture.TerminalPersisted)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "first")
	require.Equal(t, "ready", run.Redaction.State)
	want := append([]byte("first"), 0, 255)
	want = append(want, []byte("last\n")...)
	require.Equal(t, want, p.rawRun(t, *run.RunID, "stdout"))
	require.Equal(t, []byte("failure diagnostic"), p.rawRun(t, *run.RunID, "stderr"))
	var defaultRead struct {
		IsError    bool              `json:"isError"`
		Structured runstore.ReadPage `json:"structuredContent"`
	}
	require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "read", "run_id": *run.RunID}}, &defaultRead))
	require.False(t, defaultRead.IsError)
	require.Equal(t, "text", defaultRead.Structured.Encoding)
	require.NotEmpty(t, defaultRead.Structured.Fragments)

	p.close(t)
	dbPath := filepath.Join(project, ".capy", "runs", "runs.db")
	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	beforeHash := sha256.Sum256(before)
	for _, secret := range []string{key, "failure diagnostic", "executed-once"} {
		require.NotContains(t, string(before), secret)
	}
	require.NoFileExists(t, filepath.Join(project, "knowledge.db"))
	wrong := launch(t)
	var rejected struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Error runstore.Error `json:"error"`
		} `json:"structuredContent"`
	}
	require.NoError(t, wrong.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "status", "run_id": *run.RunID}}, &rejected))
	require.True(t, rejected.IsError)
	require.Equal(t, "wrong_key", rejected.Structured.Error.Code)
	wrong.close(t)
	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	require.Equal(t, beforeHash, sha256.Sum256(after))
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0o600))
	reopened := launch(t)
	var status struct {
		IsError    bool `json:"isError"`
		Structured struct {
			Run runstore.Result `json:"run"`
		} `json:"structuredContent"`
	}
	require.NoError(t, reopened.request("tools/call", map[string]any{"name": "capy_runs", "arguments": map[string]any{"action": "status", "run_id": *run.RunID}}, &status))
	require.False(t, status.IsError, "query success is independent of child exit 7")
	require.Equal(t, 7, *status.Structured.Run.ExitCode)
	require.Equal(t, want, reopened.rawRun(t, *run.RunID, "stdout"))
	reopened.close(t)
	count, err := os.ReadFile(filepath.Join(project, "executed-once"))
	require.NoError(t, err)
	require.Equal(t, []byte("x"), count)
	require.NoFileExists(t, filepath.Join(project, "knowledge.db"))
}

func TestMCPRunStoreSandboxAdmission(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("captured supervision requires Linux")
	}
	bin := buildStdioCapy(t)
	for _, mode := range []string{"owner writable", "owner excluded"} {
		t.Run(mode, func(t *testing.T) {
			owner := t.TempDir()
			if mode == "owner excluded" {
				owner = os.Getenv("CAPY_RUNS_EXCLUDED_OWNER")
				if owner == "" {
					t.Skip("set CAPY_RUNS_EXCLUDED_OWNER to an existing directory excluded by the actual host sandbox")
				}
			}
			env := stdioEnv(t, "synthetic-sandbox-capture-key-123456789")
			for i, value := range env {
				if strings.HasPrefix(value, "HOME=") {
					env[i] = "HOME=/proc"
				}
			}
			env = append(env, "XDG_CACHE_HOME=/proc/capy-run-no-cache")
			p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", owner}, dir: t.TempDir(), env: env})
			p.initialize(t)
			marker := filepath.Join(t.TempDir(), "spawned")
			result := p.capturedRun(t, map[string]any{"language": "shell", "code": "printf ok; printf x > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'"})
			if mode == "owner excluded" {
				require.True(t, result.IsError)
				require.Nil(t, result.Structured.RunID)
				require.NoFileExists(t, marker)
			} else {
				require.False(t, result.IsError, "%+v", result)
				require.True(t, bytes.Equal([]byte("ok"), p.rawRun(t, *result.Structured.RunID, "stdout")))
				require.FileExists(t, marker)
			}
			p.close(t)
		})
	}
}
