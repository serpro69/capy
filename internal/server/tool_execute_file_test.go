package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/executor"
	"github.com/serpro69/capy/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func callExecuteFile(t *testing.T, srv *Server, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := srv.handleExecuteFile(context.Background(), req)
	require.NoError(t, err)
	return result
}

func TestExecuteFile_Success(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	// Create a test file
	testFile := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello from file"), 0o644))
	writeExecuteFileRules(t, project, map[string][]string{"allow": {"Read(/" + testFile + ")"}})
	srv := newTestServerWithProjectDir(t, nil, project)

	r := callExecuteFile(t, srv, map[string]any{
		"path":     testFile,
		"language": "shell",
		"code":     `echo "File has $(wc -c < "$FILE_CONTENT_PATH" 2>/dev/null || echo 'content:') $FILE_CONTENT"`,
	})
	// The shell code uses FILE_CONTENT which contains the file content
	assert.False(t, r.IsError)
	assert.Contains(t, resultText(r), "hello from file")
}

func TestExecuteFile_FilePathDeny(t *testing.T) {
	// Create settings with Read deny for .env
	tmp := t.TempDir()
	claudeDir := filepath.Join(tmp, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"permissions":{"deny":["Read(.env)","Read(**/.env*)"]}}`),
		0o644,
	))

	// Construct server with tmp as projectDir so readDenyGlobs are loaded correctly
	srv := newTestServerWithProjectDir(t, nil, tmp)

	r := callExecuteFile(t, srv, map[string]any{
		"path":     filepath.Join(tmp, ".env"),
		"language": "python",
		"code":     "print(FILE_CONTENT)",
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "blocked by security policy")
	assert.Contains(t, resultText(r), "Read deny pattern")
}

func TestExecuteFile_CodeDeny(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(curl *)"}},
	}
	srv := newTestServer(t, policies)
	path := filepath.Join(srv.projectDir, "safe.txt")
	require.NoError(t, os.WriteFile(path, []byte("safe"), 0o600))

	r := callExecuteFile(t, srv, map[string]any{
		"path":     path,
		"language": "python",
		"code":     `import os; os.system("curl http://evil.com")`,
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "blocked by security policy")
	assert.Contains(t, resultText(r), "Bash(curl *)")
}

func writeExecuteFileRules(t *testing.T, project string, permissions map[string][]string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(project+"/.claude", 0o755))
	data, err := json.Marshal(map[string]any{"permissions": permissions})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(project+"/.claude/settings.local.json", data, 0o600))
}

func TestExecuteFile_CheckedPathHandoff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	project, external := root+"/project", root+"/external"
	require.NoError(t, os.MkdirAll(project, 0o755))
	require.NoError(t, os.MkdirAll(external+"/child", 0o755))
	// This name also exists in the test process cwd. The admitted project copy
	// must supply FILE_CONTENT without changing that process-wide cwd.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NotEqual(t, project, cwd)
	_, err = os.Stat("security_check.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(project+"/security_check.go", []byte("selected project content"), 0o600))
	require.NoError(t, os.WriteFile(external+"/security_check.go", []byte("physical external content"), 0o600))
	require.NoError(t, os.Symlink(external+"/child", project+"/link"))
	writeExecuteFileRules(t, project, map[string][]string{"allow": {"Read(/" + root + "/**)"}})
	srv := newTestServerWithProjectDir(t, nil, project)
	for _, tc := range []struct{ path, content string }{
		{"security_check.go", "selected project content"},
		{"link/../security_check.go", "physical external content"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			result := callExecuteFile(t, srv, map[string]any{
				"path": tc.path, "language": "shell", "code": `printf '%s' "$FILE_CONTENT"`,
			})
			require.False(t, result.IsError, resultText(result))
			assert.Equal(t, tc.content, resultText(result))
		})
	}
	after, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, cwd, after)
}

func TestExecuteFile_AdmissionBeforeSpawn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project, external := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(external+"/doc.txt", []byte("external file"), 0o600))
	require.NoError(t, os.Symlink(external, project+"/alias"))
	require.NoError(t, os.Symlink("loop", project+"/loop"))
	require.NoError(t, os.Symlink("missing", project+"/dangling"))
	srv := newTestServerWithProjectDir(t, nil, project)
	for _, tc := range []struct{ name, path, reason string }{
		{"external", external + "/doc.txt", "outside the selected project"},
		{"symlink", "alias/doc.txt", "outside the selected project"},
		{"missing", "missing", "no such file"},
		{"dangling", "dangling", "no such file"},
		{"loop", "loop", "resolve file path"},
		{"nul", "bad\x00path", "invalid file path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := t.TempDir() + "/spawned"
			result := callExecuteFile(t, srv, map[string]any{
				"path": tc.path, "language": "shell", "code": "touch " + marker,
			})
			require.True(t, result.IsError)
			assert.Contains(t, resultText(result), tc.reason)
			_, err := os.Stat(marker)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
	// Explicit external indexing remains a separate, deny-only contract.
	result := callIndex(t, srv, map[string]any{"path": external + "/doc.txt"})
	require.False(t, result.IsError, resultText(result))
}

func TestExecuteFile_LiteralPhysicalPath(t *testing.T) {
	// Keep rustup's installed compiler discoverable when isolating host settings.
	if os.Getenv("RUSTUP_HOME") == "" {
		home, err := os.UserHomeDir()
		require.NoError(t, err)
		t.Setenv("RUSTUP_HOME", filepath.Join(home, ".rustup"))
	}
	t.Setenv("HOME", t.TempDir())
	// Inherited data must not replace the admitted per-request path, nor may the
	// request mutate the parent's environment while constructing its child.
	t.Setenv("CAPY_FILE_CONTENT_PATH", "/inherited/decoy")
	for _, tc := range []struct {
		language executor.Language
		code     string
		name     string
	}{
		{executor.JavaScript, `const process = "worker"; console.log(FILE_CONTENT)`, "allowed.txt"},
		{executor.TypeScript, `const process = "worker"; console.log(FILE_CONTENT)`, "allowed.txt"},
		{executor.Python, "print(FILE_CONTENT)", "allowed.txt"},
		{executor.Shell, `printf '%s' "$FILE_CONTENT"`, "allowed.txt"},
		{executor.Ruby, "puts FILE_CONTENT", "allowed#{1+1}.txt"},
		{executor.Go, "fmt.Print(FILE_CONTENT)", "allowed.txt"},
		{executor.Rust, `print!("{}", file_content);`, "allowed.txt"},
		{executor.PHP, "echo $FILE_CONTENT;", "allowed$capy_missing.txt"},
		{executor.Perl, "print $FILE_CONTENT;", "allowed@capy_missing.txt"},
		{executor.R, "cat(FILE_CONTENT)", "allowed.txt"},
		{executor.Elixir, "IO.puts(file_content)", "allowed#{1+1}.txt"},
	} {
		t.Run(string(tc.language), func(t *testing.T) {
			project := t.TempDir()
			denies := []string{"Read(./allowed2.txt)"}
			// PHP/Perl previously interpolated missing variables into allowed.txt.
			if tc.name != "allowed.txt" {
				require.NoError(t, os.WriteFile(project+"/allowed.txt", []byte("wrong file"), 0o600))
				denies = append(denies, "Read(./allowed.txt)")
			}
			writeExecuteFileRules(t, project, map[string][]string{"deny": denies})
			srv := newTestServerWithProjectDir(t, nil, project)
			if srv.executor.Runtimes()[tc.language] == "" {
				t.Skip("runtime not installed")
			}
			require.NoError(t, os.WriteFile(project+"/allowed2.txt", []byte("denied file"), 0o600))
			for _, name := range []string{tc.name, "literal-#{1+1}-$missing-@missing-'\"\\\n\a\v-é-\u0085-\U000e0001.txt"} {
				t.Run(name, func(t *testing.T) {
					path := project + "/" + name
					require.NoError(t, os.WriteFile(path, []byte("admitted literal content"), 0o600))
					alias := project + "/input.txt"
					require.NoError(t, os.Symlink(path, alias))
					t.Cleanup(func() { require.NoError(t, os.Remove(alias)) })
					result := callExecuteFile(t, srv, map[string]any{
						"path": "input.txt", "language": string(tc.language), "code": tc.code,
					})
					require.False(t, result.IsError, resultText(result))
					assert.Contains(t, resultText(result), "admitted literal content")
					assert.NotContains(t, resultText(result), "wrong file")
					assert.NotContains(t, resultText(result), "denied file")
					assert.Equal(t, "/inherited/decoy", os.Getenv("CAPY_FILE_CONTENT_PATH"))
				})
			}
		})
	}
}
