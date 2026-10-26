package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/serpro69/capy/internal/execution"
	"github.com/serpro69/capy/internal/executor"
	"github.com/stretchr/testify/require"
)

// Keep the expected public proof independent of admission's scope selector.
func capturedCleanup(t *testing.T) executor.CleanupEvidence {
	t.Helper()
	if err := execution.Supported(); err != nil {
		t.Skipf("captured execution unavailable on this host: %v", err)
	}
	switch runtime.GOOS {
	case "linux":
		return executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}
	case "darwin":
		return executor.CleanupEvidence{Scope: executor.CleanupDarwinObservation, State: executor.CleanupObserved}
	default:
		t.Fatal("unexpected supported host")
		return executor.CleanupEvidence{}
	}
}

func TestMCPOrdinaryCaptureOutcomes(t *testing.T) {
	wantProof := capturedCleanup(t)
	bin := buildStdioCapy(t)
	for _, tc := range []struct {
		name, code, config string
		timeout            int
		failed             bool
		state, capture     string
		exit, signal       int
	}{
		{"success", "printf ok", "", 10000, false, "exited", "complete", 0, 0},
		{"nonzero", "printf diagnostic >&2; exit 7", "", 10000, true, "exited", "complete", 7, 0},
		{"signal", "kill -TERM $$", "", 10000, true, "exited", "complete", 0, 15},
		{"deadline", "sleep 10", "", 1500, true, "timed_out", "complete", 0, 9},
		{"capture loss", "printf 0123456789; printf x > finished", "max_run_bytes=4\n", 10000, true, "exited", "incomplete", 0, 0},
		{"failed with capture loss", "printf 0123456789; exit 7", "max_run_bytes=4\n", 10000, true, "exited", "incomplete", 7, 0},
		{"bounded large output", "head -c 10000 /dev/zero", "", 10000, false, "exited", "complete", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := cliProject(t, tc.config)
			p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: project, env: stdioEnv(t, "synthetic-public-outcomes-key")})
			p.initialize(t)
			// Inspect the actual wire result, including optional metadata omitted
			// by the typed fixture decoder, against the configured 2-KiB budget.
			var wire json.RawMessage
			require.NoError(t, p.request("tools/call", map[string]any{"name": "capy_execute", "arguments": map[string]any{"language": "shell", "code": tc.code, "timeout": tc.timeout, "wait_ms": 30000}}, &wire))
			require.LessOrEqual(t, len(wire), 2048)
			var result stdioRunResponse
			require.NoError(t, json.Unmarshal(wire, &result))
			require.Equal(t, tc.failed, result.IsError, string(wire))
			run := result.Structured
			require.NotNil(t, run.RunID)
			require.Equal(t, 2, run.Version)
			require.Equal(t, tc.state, run.State)
			require.Equal(t, tc.capture, run.Capture.State)
			require.True(t, run.Capture.TerminalPersisted)
			require.Equal(t, wantProof, run.Cleanup)
			if tc.signal != 0 {
				require.Equal(t, &tc.signal, run.Signal)
				require.Nil(t, run.ExitCode)
			} else {
				require.Equal(t, &tc.exit, run.ExitCode)
				require.Nil(t, run.Signal)
			}
			if tc.name == "capture loss" {
				require.FileExists(t, filepath.Join(project, "finished"))
			}
			p.close(t)
		})
	}
}

func TestCapturedPublicOwnerCwdAndRejections(t *testing.T) {
	wantProof := capturedCleanup(t)
	bin := buildStdioCapy(t)
	for _, transport := range []string{"cli", "mcp"} {
		t.Run(transport, func(t *testing.T) {
			for _, mode := range []string{"external cwd", "deny", "ask", "invalid selected key", "blocked store"} {
				t.Run(mode, func(t *testing.T) {
					owner := cliProject(t, "")
					cwd := t.TempDir()
					code := "printf x >> once; printf owner-output; pwd"
					env := stdioEnv(t, "synthetic-public-owner-key")
					switch mode {
					case "deny", "ask":
						require.NoError(t, os.MkdirAll(filepath.Join(owner, ".claude"), 0700))
						require.NoError(t, os.WriteFile(filepath.Join(owner, ".claude", "settings.json"), []byte(`{"permissions":{"`+mode+`":["Bash(printf:*)"]}}`), 0600))
					case "invalid selected key":
						// A nonempty inherited key must not replace the selected empty file.
						require.NoError(t, os.WriteFile(filepath.Join(owner, "empty.key"), nil, 0600))
						require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy.toml"), []byte("[store]\npath='knowledge.db'\nkey_file='empty.key'\n[runs]\nmax_response_bytes=2048\n"), 0600))
					case "blocked store":
						require.NoError(t, os.MkdirAll(filepath.Join(owner, ".capy"), 0700))
						require.NoError(t, os.WriteFile(filepath.Join(owner, ".capy", "runs"), []byte("blocked"), 0600))
					}
					var result stdioRunResponse
					if transport == "cli" {
						out, diag, exit := capturedCLI(t, bin, cwd, env, "execute", "--json", "--project-dir", owner, "--cwd", cwd, "--code", code)
						require.Empty(t, diag)
						require.NoError(t, json.Unmarshal(out, &result.Structured))
						result.IsError = exit != 0
					} else {
						p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", owner}, dir: cwd, env: env})
						if mode == "invalid selected key" {
							assertMCPStartupFailure(t, p, "empty")
							require.NoFileExists(t, filepath.Join(cwd, "once"))
							require.NoDirExists(t, filepath.Join(owner, ".capy", "runs"))
							require.NoDirExists(t, filepath.Join(cwd, ".capy", "runs"))
							require.NoFileExists(t, filepath.Join(owner, "knowledge.db"))
							return
						}
						p.initialize(t)
						result = p.capturedRun(t, map[string]any{"language": "shell", "code": code, "cwd": cwd})
						p.close(t)
					}
					// MCP retains its existing deny-only policy; the CLI rejects matched asks.
					if mode == "external cwd" || (mode == "ask" && transport == "mcp") {
						require.False(t, result.IsError, "%+v", result)
						require.Equal(t, wantProof, result.Structured.Cleanup)
						canonical, err := filepath.EvalSymlinks(cwd)
						require.NoError(t, err)
						require.Contains(t, result.Structured.Preview, canonical)
						count, err := os.ReadFile(filepath.Join(cwd, "once"))
						require.NoError(t, err)
						require.Equal(t, "x", string(count))
						require.FileExists(t, filepath.Join(owner, ".capy", "runs", "runs.db"))
					} else {
						require.True(t, result.IsError)
						require.Nil(t, result.Structured.RunID)
						require.NotNil(t, result.Structured.Error)
						require.NoFileExists(t, filepath.Join(cwd, "once"))
						if mode == "deny" || mode == "ask" {
							wantCode := "permission_denied"
							if mode == "ask" {
								wantCode = "approval_required"
							}
							require.Equal(t, wantCode, result.Structured.Error.Code)
							require.Contains(t, result.Structured.Error.Message, "Bash(printf:*)")
							if transport == "cli" {
								require.Contains(t, result.Structured.Error.Message, filepath.Join(owner, ".claude", "settings.json"))
							}
						}
						if mode == "blocked store" {
							data, err := os.ReadFile(filepath.Join(owner, ".capy", "runs"))
							require.NoError(t, err)
							require.Equal(t, "blocked", string(data))
						} else {
							require.NoDirExists(t, filepath.Join(owner, ".capy", "runs"))
						}
					}
					require.NoDirExists(t, filepath.Join(cwd, ".capy", "runs"))
					require.NoFileExists(t, filepath.Join(owner, "knowledge.db"))
				})
			}
		})
	}
}
