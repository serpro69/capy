package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
			denied, pattern := EvaluateFilePath(tt.path, denyGlobs)
			assert.Equal(t, tt.denied, denied)
			assert.Equal(t, tt.pattern, pattern)
		})
	}
}

func TestEvaluateFilePath_EmptyGlobs(t *testing.T) {
	denied, _ := EvaluateFilePath("/any/path", nil)
	assert.False(t, denied)
}
