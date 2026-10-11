package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPStdioDirectoryIndex(t *testing.T) {
	bin := buildStdioCapy(t)
	project := stdioProject(t)
	const key = "synthetic-directory-stdio-passphrase-12345"
	env := stdioEnv(t, key)
	root := filepath.Join(project, "docs")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("directoryotter transport orchard"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("a.md", filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	launch := func() *stdioProcess {
		p := startStdioProcess(t, stdioCommand{executable: bin, args: []string{"serve", "--project-dir", project}, dir: t.TempDir(), env: env, secrets: []string{key}})
		p.initialize(t)
		return p
	}
	p := launch()
	var tools struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := p.request("tools/list", map[string]any{}, &tools); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name != "capy_index" {
			continue
		}
		found = true
		for _, field := range []string{"max_depth", "max_files", "extensions", "include", "exclude", "respect_gitignore", "follow_symlinks"} {
			if tool.InputSchema.Properties[field] == nil {
				t.Fatalf("missing schema field %s", field)
			}
		}
	}
	if !found {
		t.Fatal("capy_index missing")
	}
	for _, args := range []map[string]any{{"max_depth": 0.5}, {"respect_gitignore": "yes"}, {"follow_symlinks": nil}, {"follow_symlinks": 1}, {"include": []any{"*.md", 1}}} {
		args["path"] = "docs"
		isError, text := stdioBooleanCall(t, p, "capy_index", args)
		if !isError {
			t.Fatalf("invalid controls accepted over stdio: %s", text)
		}
	}
	if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !os.IsNotExist(err) {
		t.Fatalf("invalid requests opened DB: %v", err)
	}
	text := p.tool(t, "capy_index", map[string]any{"path": "docs", "source": "stdio:directory", "max_files": 1, "respect_gitignore": "false"})
	if !strings.Contains(text, "incomplete") || !strings.Contains(text, "max_files") {
		t.Fatalf("limit not visible: %s", text)
	}
	text = p.tool(t, "capy_index", map[string]any{"path": "docs", "source": "stdio:directory", "respect_gitignore": " FaLsE "})
	if !strings.Contains(text, "unchanged: 1") || !strings.Contains(text, "Indexed: 1") {
		t.Fatalf("dedup over stdio: %s", text)
	}
	if err := os.Symlink("missing", filepath.Join(root, "broken.md")); err != nil {
		t.Fatal(err)
	}
	text = p.tool(t, "capy_index", map[string]any{"path": "docs", "source": "stdio:directory", "follow_symlinks": " FaLsE "})
	if !strings.Contains(text, "unchanged: 2") || !strings.Contains(text, "failed: 0") {
		t.Fatalf("false must skip broken descendant: %s", text)
	}
	isError, text := stdioBooleanCall(t, p, "capy_index", map[string]any{"path": "docs", "source": "stdio:directory", "follow_symlinks": " TrUe "})
	if !isError || !strings.Contains(text, "partial error") || !strings.Contains(text, "unchanged: 2") || !strings.Contains(text, "failed: 1") {
		t.Fatalf("true must follow and report broken descendant: %s", text)
	}
	p.close(t)
	p = launch()
	text = p.tool(t, "capy_search", map[string]any{"queries": []string{"directoryotter"}, "source": "stdio:directory:"})
	if !strings.Contains(text, "directoryotter") {
		t.Fatalf("encrypted reopen lost directory sources: %s", text)
	}
	p.close(t)
}
