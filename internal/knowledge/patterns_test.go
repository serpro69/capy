package knowledge

import (
	"testing"
)

func TestDirectoryPatterns(t *testing.T) {
	for _, tt := range []struct {
		name, pattern, path string
		want                bool
	}{
		{"root", "*.md", "file.md", true}, {"whole path", "*.md", "sub/file.md", false},
		{"globstar root", "**/*.md", "file.md", true}, {"globstar nested", "**/*.md", "a/b/file.md", true},
		{"question", "docs/?.md", "docs/界.md", true}, {"literal unicode", "筆記/*.md", "筆記/文.md", true},
		{"newline", "**/*.md", "sub/new\nline.md", true}, {"regex is literal", "(foo)+.md", "(foo)+.md", true},
		{"case sensitive", "*.md", "file.MD", false}, {"nested globstar", "a/**/z.md", "a/z.md", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			patterns, err := compilePatterns([]string{tt.pattern})
			if err != nil {
				t.Fatal(err)
			}
			if got := matchesAny(patterns, tt.path); got != tt.want {
				t.Fatalf("match %q against %q = %v", tt.pattern, tt.path, got)
			}
		})
	}
	for _, pattern := range []string{"", "/absolute/**", "../outside", "a/../b", "[ab].md", "!secret", "{a,b}", `a\b`, "C:/a", "a//b", "a/./b", "a\x00b"} {
		t.Run("invalid-"+pattern, func(t *testing.T) {
			if _, err := compilePatterns([]string{pattern}); err == nil {
				t.Fatalf("accepted %q", pattern)
			}
		})
	}
}

func TestDirectoryOptionsValidation(t *testing.T) {
	for _, depth := range []int{-1, 21} {
		if err := (DirectoryOptions{MaxDepth: &depth}).Validate(); err == nil {
			t.Fatalf("accepted depth %d", depth)
		}
	}
	for _, files := range []int{0, 1001} {
		if err := (DirectoryOptions{MaxFiles: &files}).Validate(); err == nil {
			t.Fatalf("accepted files %d", files)
		}
	}
	for _, ext := range []string{"", ".", "*.md", "a/b", "[md]"} {
		if err := (DirectoryOptions{Extensions: []string{ext}}).Validate(); err == nil {
			t.Fatalf("accepted extension %q", ext)
		}
	}
	for _, name := range []string{".git", ".hg", ".svn", ".capy", ".aws", ".ssh", "node_modules", "vendor", "dist", "build", ".next", "coverage", ".venv", "venv", "__pycache__", "target", ".cache", ".env", ".env.local", "cert.pem", "secret.key", "id_rsa", "id_ed25519", "credentials"} {
		t.Run(name, func(t *testing.T) {
			if !mandatoryExcluded("a/" + name + "/file.md") {
				t.Fatalf("mandatory exclusion missing for %s", name)
			}
		})
	}
}
