package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseBashPattern(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Bash(sudo *)", "sudo *"},
		{"Bash(git:*)", "git:*"},
		{"Bash(echo (foo))", "echo (foo)"},
		{"Bash(*)", "*"},
		{"Read(.env)", ""},
		{"NotBash(ls)", ""},
		{"Bash()", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, parseBashPattern(tt.input))
		})
	}
}

func TestParseToolPattern(t *testing.T) {
	tests := []struct {
		input    string
		wantTool string
		wantGlob string
	}{
		{"Bash(sudo *)", "Bash", "sudo *"},
		{"Read(.env)", "Read", ".env"},
		{"Read(some(path))", "Read", "some(path)"},
		{"Grep(**/*.key)", "Grep", "**/*.key"},
		{"nope", "", ""},
		{"(bad)", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			tool, glob := parseToolPattern(tt.input)
			assert.Equal(t, tt.wantTool, tool)
			assert.Equal(t, tt.wantGlob, glob)
		})
	}
}

func TestGlobToRegex(t *testing.T) {
	tests := []struct {
		name  string
		glob  string
		match string
		want  bool
	}{
		// Colon format
		{"colon exact", "git:*", "git", true},
		{"colon with args", "git:*", "git push origin main", true},
		{"colon no match", "git:*", "gitx", false},
		{"colon specific args", "git:push *", "git push origin", true},
		{"colon specific no match", "git:push *", "git pull", false},

		// Plain glob
		{"plain wildcard", "sudo *", "sudo rm -rf /", true},
		{"plain no match", "sudo *", "echo hello", false},
		{"exact match", "ls", "ls", true},
		{"exact no match", "ls", "ls -la", false},
		{"star glob", "*commit*", "git commit -m foo", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := globToRegex(tt.glob, false)
			assert.Equal(t, tt.want, re.MatchString(tt.match))
		})
	}
}

func TestGlobToRegexCaseInsensitive(t *testing.T) {
	re := globToRegex("sudo *", true)
	assert.True(t, re.MatchString("SUDO rm -rf /"))
	assert.True(t, re.MatchString("sudo RM"))
}

func TestFileGlobToRegex(t *testing.T) {
	tests := []struct {
		name  string
		glob  string
		path  string
		want  bool
	}{
		{"simple file", ".env", ".env", true},
		{"simple no match", ".env", ".envrc", false},
		{"star", "*.key", "server.key", true},
		{"star no slash", "*.key", "dir/server.key", false},
		{"double star prefix", "**/.env", "foo/bar/.env", true},
		{"double star prefix root", "**/.env", ".env", true},
		{"double star mid", "src/**/*.go", "src/pkg/main.go", true},
		{"double star deep", "src/**/*.go", "src/a/b/c/main.go", true},
		{"question mark", "?.txt", "a.txt", true},
		{"question mark no match", "?.txt", "ab.txt", false},
		{"env star", "**/.env*", "project/.env.local", true},
		{"env star root", "**/.env*", ".env.production", true},
		{"trailing doublestar", "secrets/**", "secrets/key.pem", true},
		{"trailing doublestar deep", "secrets/**", "secrets/a/b/c.pem", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := fileGlobToRegex(tt.glob, false)
			assert.Equal(t, tt.want, re.MatchString(tt.path), "glob=%q path=%q regex=%s", tt.glob, tt.path, re.String())
		})
	}
}

func TestMatchesAnyBashPattern(t *testing.T) {
	patterns := []string{"Bash(sudo *)", "Bash(rm -rf *)", "Read(.env)"}

	assert.Equal(t, "Bash(sudo *)", matchesAnyBashPattern("sudo rm -rf /", patterns, false))
	assert.Equal(t, "Bash(rm -rf *)", matchesAnyBashPattern("rm -rf /tmp", patterns, false))
	assert.Equal(t, "", matchesAnyBashPattern("echo hello", patterns, false))
	// Read patterns are not Bash patterns, should be skipped
	assert.Equal(t, "", matchesAnyBashPattern(".env", patterns, false))
}
