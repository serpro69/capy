package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateCommandDenyOnly(t *testing.T) {
	policies := []SecurityPolicy{
		{
			Deny:  []string{"Bash(sudo *)", "Bash(rm -rf *)"},
			Allow: []string{"Bash(echo *)"},
			Ask:   []string{"Bash(git push *)"},
		},
	}

	t.Run("deny matches", func(t *testing.T) {
		d := EvaluateCommandDenyOnly("sudo rm -rf /", policies)
		assert.Equal(t, "deny", d.Decision)
		assert.Equal(t, "Bash(sudo *)", d.MatchedPattern)
	})

	t.Run("allow when no deny match", func(t *testing.T) {
		d := EvaluateCommandDenyOnly("echo hello", policies)
		assert.Equal(t, "allow", d.Decision)
		assert.Empty(t, d.MatchedPattern)
	})

	t.Run("ignores ask patterns", func(t *testing.T) {
		d := EvaluateCommandDenyOnly("git push origin main", policies)
		assert.Equal(t, "allow", d.Decision)
	})

	t.Run("chained command deny", func(t *testing.T) {
		d := EvaluateCommandDenyOnly("echo ok && sudo rm -rf /", policies)
		assert.Equal(t, "deny", d.Decision)
	})
}

func TestEvaluateCommand(t *testing.T) {
	policies := []SecurityPolicy{
		{
			Deny:  []string{"Bash(sudo *)"},
			Allow: []string{"Bash(echo *)"},
			Ask:   []string{"Bash(git push *)"},
		},
	}

	t.Run("deny wins", func(t *testing.T) {
		d := EvaluateCommand("sudo rm -rf /", policies)
		assert.Equal(t, "deny", d.Decision)
	})

	t.Run("ask matches", func(t *testing.T) {
		d := EvaluateCommand("git push origin main", policies)
		assert.Equal(t, "ask", d.Decision)
		assert.Equal(t, "Bash(git push *)", d.MatchedPattern)
	})

	t.Run("allow matches", func(t *testing.T) {
		d := EvaluateCommand("echo hello world", policies)
		assert.Equal(t, "allow", d.Decision)
		assert.Equal(t, "Bash(echo *)", d.MatchedPattern)
	})

	t.Run("default is ask", func(t *testing.T) {
		d := EvaluateCommand("unknown command", policies)
		assert.Equal(t, "ask", d.Decision)
		assert.Empty(t, d.MatchedPattern)
	})

	t.Run("deny wins over allow in chained", func(t *testing.T) {
		d := EvaluateCommand("echo ok && sudo rm -rf /", policies)
		assert.Equal(t, "deny", d.Decision)
	})
}

func TestEvaluateCommand_DenyWinsOverAllow(t *testing.T) {
	// Deny in a later policy should still win over allow in an earlier one
	policies := []SecurityPolicy{
		{
			Allow: []string{"Bash(rm *)"},
		},
		{
			Deny: []string{"Bash(rm -rf *)"},
		},
	}

	d := EvaluateCommand("rm -rf /", policies)
	assert.Equal(t, "deny", d.Decision)
}

func TestEvaluateFilePath(t *testing.T) {
	denyGlobs := [][]string{
		{".env", "**/.env*"},
		{"**/*.key"},
	}

	tests := []struct {
		name    string
		path    string
		denied  bool
		pattern string
	}{
		{"exact .env", ".env", true, ".env"},
		{".env.local", "project/.env.local", true, "**/.env*"},
		{"key file", "certs/server.key", true, "**/*.key"},
		{"safe file", "src/main.go", false, ""},
		{"backslash normalization", "project\\.env", true, "**/.env*"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty projectRoot preserves the original raw-input-only behavior.
			denied, pattern := EvaluateFilePath(tt.path, denyGlobs, "")
			assert.Equal(t, tt.denied, denied)
			assert.Equal(t, tt.pattern, pattern)
		})
	}
}

func TestEvaluateFilePath_EmptyGlobs(t *testing.T) {
	denied, _ := EvaluateFilePath("/any/path", nil, "")
	assert.False(t, denied)
}

// TestEvaluateFilePath_Traversal verifies that a relative path which escapes the
// project root via ".." is matched against its resolved absolute form. Without
// the projectRoot argument the raw "../../.ssh/id_rsa" sidesteps an
// absolute-anchored deny glob — that is the bypass this fix closes.
func TestEvaluateFilePath_Traversal(t *testing.T) {
	denyGlobs := [][]string{{"/home/user/.ssh/**"}}

	t.Run("relative traversal caught when projectRoot provided", func(t *testing.T) {
		denied, pattern := EvaluateFilePath("../../.ssh/id_rsa", denyGlobs, "/home/user/project/nested")
		assert.True(t, denied)
		assert.Equal(t, "/home/user/.ssh/**", pattern)
	})

	t.Run("relative traversal bypasses absolute glob without projectRoot", func(t *testing.T) {
		// Documents the original vulnerability: raw relative path does not match
		// the absolute deny glob, so an empty projectRoot leaves the hole open.
		denied, _ := EvaluateFilePath("../../.ssh/id_rsa", denyGlobs, "")
		assert.False(t, denied)
	})

	t.Run("absolute path matches directly", func(t *testing.T) {
		denied, pattern := EvaluateFilePath("/home/user/.ssh/id_rsa", denyGlobs, "/home/user/project")
		assert.True(t, denied)
		assert.Equal(t, "/home/user/.ssh/**", pattern)
	})
}

// TestEvaluateFilePath_SymlinkEscape verifies that a symlink pointing at a
// denied target is caught by resolving the canonical realpath, even when the
// symlink's own path does not match any deny glob.
func TestEvaluateFilePath_SymlinkEscape(t *testing.T) {
	// t.TempDir() can sit under a symlinked prefix (e.g. /var -> /private/var on
	// macOS); resolve it first so the deny glob is anchored to the canonical base.
	realTmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	secretDir := filepath.Join(realTmp, "secrets")
	projectDir := filepath.Join(realTmp, "project")
	require.NoError(t, os.MkdirAll(secretDir, 0o755))
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	target := filepath.Join(secretDir, "id_rsa")
	require.NoError(t, os.WriteFile(target, []byte("PRIVATE KEY"), 0o600))

	link := filepath.Join(projectDir, "safe-link")
	require.NoError(t, os.Symlink(target, link))

	// A symlinked *directory* under the project, used to exercise the
	// symlink-then-".." physical traversal: "dir-link/../secrets/id_rsa".
	safeDir := filepath.Join(realTmp, "safe-dir")
	require.NoError(t, os.MkdirAll(safeDir, 0o755))
	dirLink := filepath.Join(projectDir, "dir-link")
	require.NoError(t, os.Symlink(safeDir, dirLink))

	denyGlobs := [][]string{{secretDir + "/**"}}

	t.Run("symlink path alone is not denied", func(t *testing.T) {
		// The symlink lives under project/, which the secret glob does not cover.
		denied, _ := EvaluateFilePath(link, denyGlobs, "")
		assert.False(t, denied)
	})

	t.Run("symlink escape caught via realpath", func(t *testing.T) {
		denied, pattern := EvaluateFilePath(link, denyGlobs, projectDir)
		assert.True(t, denied)
		assert.Equal(t, secretDir+"/**", pattern)
	})

	t.Run("physical traversal via symlinked dir caught", func(t *testing.T) {
		// Build the input as a literal (NOT filepath.Join, which would Clean
		// "dir-link/.." away). Lexically this resolves to project/secrets/id_rsa
		// (non-existent), but the OS resolves dir-link -> safe-dir first, then
		// ".." -> realTmp, reaching the denied realTmp/secrets/id_rsa. Only the
		// uncleaned EvalSymlinks candidate catches it.
		sep := string(filepath.Separator)
		escapePath := "dir-link" + sep + ".." + sep + "secrets" + sep + "id_rsa"
		denied, pattern := EvaluateFilePath(escapePath, denyGlobs, projectDir)
		assert.True(t, denied)
		assert.Equal(t, secretDir+"/**", pattern)
	})
}
