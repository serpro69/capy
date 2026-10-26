package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/runstore"
	"github.com/stretchr/testify/require"
)

func TestExecuteFileCapturedBytesAndStorageIsolation(t *testing.T) {
	s := newHandoffServer(t, "")
	s.config.Runs.MaxResponseBytes = 2048
	content := strings.Repeat("ordinary diagnostic line\n", 6000) + "middlefailure diagnostic\n<private>private-fixture</private>\n\x00\xfflast\n"
	require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "input.txt"), []byte(strings.TrimSuffix(content, "\x00\xfflast\n")), 0600))
	for _, intent := range []string{"", "middlefailure"} {
		t.Run("intent="+intent, func(t *testing.T) {
			r := callExecuteFile(t, s, map[string]any{"path": "input.txt", "language": "shell",
				"code": `cat "$FILE_CONTENT_PATH"; printf '\000\377last\n'; printf 'stderr-only diagnostic' >&2; printf x >> once; exit 1`, "intent": intent})
			require.True(t, r.IsError)
			assertRunBudget(t, r, 2048)
			run := capturedResult(t, r)
			require.Equal(t, "exited", run.State)
			require.Equal(t, 1, *run.ExitCode)
			require.Equal(t, "complete", run.Capture.State)
			require.True(t, run.Capture.TerminalPersisted)
			require.EqualValues(t, len(content)+len("stderr-only diagnostic"), run.Capture.CapturedBytes)
			if intent == "" {
				require.Equal(t, "not_requested", run.Index.State)
			} else {
				require.NotEqual(t, "not_requested", run.Index.State)
			}
			require.Equal(t, content, capturedOutput(t, s, r, "stdout"))
			require.Equal(t, "stderr-only diagnostic", capturedOutput(t, s, r, "stderr"))
			cursor := ""
			var sanitized strings.Builder
			for {
				pageResult := callRuns(t, s, map[string]any{"action": "read", "run_id": *run.RunID, "stream": "stdout", "cursor": cursor, "max_bytes": 4096})
				require.False(t, pageResult.IsError)
				assertRunBudget(t, pageResult, 2048)
				page := pageResult.StructuredContent.(runstore.ReadPage)
				for _, fragment := range page.Fragments {
					sanitized.WriteString(fragment.Text)
				}
				if page.EOF {
					break
				}
				require.NotNil(t, page.NextCursor)
				require.NotEqual(t, cursor, *page.NextCursor)
				cursor = *page.NextCursor
			}
			require.Contains(t, sanitized.String(), "middlefailure")
			require.NotContains(t, sanitized.String(), "private-fixture")
			require.Nil(t, s.store)
			require.Nil(t, s.vault)
			require.NoFileExists(t, s.config.Store.Path)
		})
	}
	count, err := os.ReadFile(filepath.Join(s.projectDir, "once"))
	require.NoError(t, err)
	require.Equal(t, "xx", string(count))
	require.Equal(t, 2, s.stats.Snapshot().Calls["capy_execute_file"])
	require.Zero(t, s.stats.Snapshot().Calls["capy_execute"])

	// Old file labels remain available and are neither replaced nor migrated.
	seed := callIndex(t, s, map[string]any{"source": "file:input.txt", "content": "legacyfiletoken retained output", "kind": "ephemeral"})
	require.False(t, seed.IsError)
	before, err := s.getStore().ListSources()
	require.NoError(t, err)
	r := callExecuteFile(t, s, map[string]any{"path": "input.txt", "language": "shell", "code": "printf newfiletoken", "intent": "newfiletoken"})
	require.False(t, r.IsError)
	after, err := s.getStore().ListSources()
	require.NoError(t, err)
	require.Equal(t, before, after)
	legacy := callSearch(t, s, map[string]any{"queries": []any{"legacyfiletoken"}, "source": "file:input.txt"})
	require.False(t, legacy.IsError)
	require.Contains(t, resultText(legacy), "legacyfiletoken")
}

func TestExecuteFileCapturedValidationBeforeAdmission(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"missing path": {"path": nil}, "empty path": {"path": ""}, "wrong path type": {"path": 5},
		"missing language": {"language": nil}, "unknown language": {"language": "other"}, "empty code": {"code": ""},
		"fractional wait": {"wait_ms": 0.5}, "negative wait": {"wait_ms": -1}, "oversized wait": {"wait_ms": 30001}, "string wait": {"wait_ms": "0"},
		"zero timeout": {"timeout": 0}, "fractional timeout": {"timeout": 0.5}, "overflow timeout": {"timeout": 1e30},
		"background true": {"background": true}, "background false": {"background": false}, "background string": {"background": "false"},
		"wrong intent": {"intent": 1}, "oversized intent": {"intent": strings.Repeat("x", 513)},
	} {
		t.Run(name, func(t *testing.T) {
			s := newHandoffServer(t, "")
			s.config.Runs.MaxResponseBytes = 2048
			require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "input.txt"), []byte("input"), 0600))
			args := map[string]any{"path": "input.txt", "language": "shell", "code": "touch must-not-run"}
			for key, value := range extra {
				args[key] = value
			}
			r := callExecuteFile(t, s, args)
			require.True(t, r.IsError)
			assertRunBudget(t, r, 2048)
			run := capturedResult(t, r)
			require.Nil(t, run.RunID)
			require.Equal(t, "invalid_argument", run.Error.Code)
			require.NoFileExists(t, filepath.Join(s.projectDir, "must-not-run"))
			require.NoDirExists(t, filepath.Join(s.projectDir, ".capy", "runs"))
		})
	}
	for _, mode := range []string{"missing credential", "unwritable store"} {
		t.Run(mode, func(t *testing.T) {
			s := newHandoffServer(t, "")
			require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "input.txt"), []byte("input"), 0600))
			if mode == "missing credential" {
				s.knowledgeKey = ""
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, ".capy"), nil, 0600))
			}
			r := callExecuteFile(t, s, map[string]any{"path": "input.txt", "language": "shell", "code": "touch must-not-run"})
			require.True(t, r.IsError)
			require.Nil(t, capturedResult(t, r).RunID)
			require.NoFileExists(t, filepath.Join(s.projectDir, "must-not-run"))
		})
	}
}

func TestExecuteFileCapturedCompilerAndRuntimeStatus(t *testing.T) {
	if os.Getenv("RUSTUP_HOME") == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("RUSTUP_HOME", filepath.Join(home, ".rustup"))
	}
	for _, tc := range []struct{ name, language, code, phase, diagnostic string }{
		{"go compiler", "go", "this is invalid go", "run", "syntax error"},
		{"rust compiler", "rust", `compile_error!("filecompilefixture");`, "compile", "filecompilefixture"},
		{"rust runtime", "rust", `eprintln!("{}", file_content); std::process::exit(7);`, "run", "file-runtime-diagnostic"},
		{"python runtime", "python", "import sys\nprint(FILE_CONTENT, file=sys.stderr)\nsys.exit(7)", "run", "file-runtime-diagnostic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHandoffServer(t, "")
			if s.executor.Runtimes()[executor.Language(tc.language)] == "" {
				t.Skip("runtime unavailable")
			}
			require.NoError(t, os.WriteFile(filepath.Join(s.projectDir, "input.txt"), []byte("file-runtime-diagnostic"), 0600))
			r := callExecuteFile(t, s, map[string]any{"path": "input.txt", "language": tc.language, "code": tc.code})
			require.True(t, r.IsError)
			run := capturedResult(t, r)
			require.Equal(t, "exited", run.State)
			require.NotNil(t, run.ExitCode)
			require.NotZero(t, *run.ExitCode)
			page, err := s.executionService().ReadRaw(t.Context(), *run.RunID, "stderr", "", 4096)
			require.NoError(t, err)
			require.NotEmpty(t, page.Fragments)
			require.Equal(t, tc.phase, page.Fragments[0].Phase)
			require.Equal(t, "complete", run.Capture.State)
			require.Contains(t, capturedOutput(t, s, r, "stderr"), tc.diagnostic)
		})
	}
}

func TestExecuteFileCapturedSchema(t *testing.T) {
	tool := toolExecuteFile("shell")
	data, err := json.Marshal(tool)
	require.NoError(t, err)
	var descriptor struct {
		InputSchema struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"inputSchema"`
	}
	require.NoError(t, json.Unmarshal(data, &descriptor))
	wait := descriptor.InputSchema.Properties["wait_ms"]
	require.Equal(t, float64(0), wait["minimum"])
	require.Equal(t, float64(30000), wait["maximum"])
	require.NotContains(t, descriptor.InputSchema.Properties, "background")
	require.Contains(t, string(data), "capy_runs")
	require.NotContains(t, string(data), "default: 30000")
}

func TestExecuteFileCapturedPhysicalPathBytes(t *testing.T) {
	// Darwin filesystems reject invalid UTF-8 names. Keep the byte-preserving
	// pathname regression on Linux; valid literal paths run on both hosts.
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux byte-transparent filenames")
	}
	s := newHandoffServer(t, "")
	external := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(external, "secret.txt"), []byte("denied-external-fixture"), 0600))
	// The only public path is valid UTF-8. Its checked physical target is not.
	// JSON replacement of the target would select the separate symlink below
	// and escape the project without any concurrent filesystem changes.
	rawPath := filepath.Join(s.projectDir, "safe-\xff.txt")
	require.NoError(t, os.WriteFile(rawPath, []byte("admitted-byte-path"), 0600))
	require.NoError(t, os.Symlink(rawPath, filepath.Join(s.projectDir, "input.txt")))
	require.NoError(t, os.Symlink(filepath.Join(external, "secret.txt"), filepath.Join(s.projectDir, "safe-\ufffd.txt")))
	denied := callExecuteFile(t, s, map[string]any{"path": "safe-\ufffd.txt", "language": "shell", "code": `printf '%s' "$FILE_CONTENT"`})
	require.True(t, denied.IsError)
	r := callExecuteFile(t, s, map[string]any{"path": "input.txt", "language": "shell", "code": `printf '%s' "$FILE_CONTENT"`})
	require.False(t, r.IsError, resultText(r))
	require.Equal(t, "complete", capturedResult(t, r).Capture.State)
	require.Equal(t, "admitted-byte-path", capturedOutput(t, s, r, "stdout"))
}
