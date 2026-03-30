package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
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
	srv := newTestServer(t, nil)
	// Create a test file
	testFile := filepath.Join(t.TempDir(), "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hello from file"), 0o644))

	r := callExecuteFile(t, srv, map[string]any{
		"path":     testFile,
		"language": "shell",
		"code":     `echo "File has $(wc -c < "$FILE_CONTENT_PATH" 2>/dev/null || echo 'content:') $FILE_CONTENT"`,
	})
	// The shell code uses FILE_CONTENT which contains the file content
	assert.False(t, r.IsError)
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
	policies := []security.SecurityPolicy{
		{Deny: []string{"Bash(curl *)"}},
	}
	srv := newTestServer(t, policies)

	r := callExecuteFile(t, srv, map[string]any{
		"path":     "/tmp/safe.txt",
		"language": "python",
		"code":     `import os; os.system("curl http://evil.com")`,
	})
	assert.True(t, r.IsError)
	assert.Contains(t, resultText(r), "blocked by security policy")
}
