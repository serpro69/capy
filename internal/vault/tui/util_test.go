package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFmtSize(t *testing.T) {
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"bytes", 512, "512B"},
		{"exact KB", 1024, "1.0KB"},
		{"KB", 2048, "2.0KB"},
		{"MB", 5 * 1024 * 1024, "5.0MB"},
		{"GB", 3 * 1024 * 1024 * 1024, "3.0GB"},
		{"zero", 0, "0B"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, fmtSize(tt.n))
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		max    int
		want   string
	}{
		{"shorter than max", "abc", 10, "abc"},
		{"exactly max", "abcde", 5, "abcde"},
		{"truncated", "abcdef", 5, "abcd…"},
		{"max one", "abcdef", 1, "…"},
		{"max zero", "abc", 0, ""},
		{"multibyte", "héllo wörld", 6, "héllo…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncate(tt.in, tt.max))
		})
	}
}

func TestOneLine(t *testing.T) {
	assert.Equal(t, "a b c", oneLine("a\n  b\tc"))
	assert.Equal(t, "", oneLine("   \n\t"))
}

func TestDisplayPath(t *testing.T) {
	assert.Equal(t, "-", displayPath(""))
	assert.Equal(t, "/abs/path", displayPath("/abs/path"))

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		p := filepath.Join(home, "proj", "x")
		got := displayPath(p)
		assert.Equal(t, "~"+string(filepath.Separator)+filepath.Join("proj", "x"), got)
	}
}

func TestRoleLabel(t *testing.T) {
	tests := map[string]string{
		"user":      "You",
		"assistant": "Claude",
		"tool":      "Tool result",
		"subagent":  "Subagent",
		"system":    "System",
		"weird":     "System", // default
	}
	for role, want := range tests {
		t.Run(role, func(t *testing.T) {
			assert.Equal(t, want, roleLabel(role))
		})
	}
}
