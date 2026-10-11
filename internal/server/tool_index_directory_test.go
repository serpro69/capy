package server

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
)

func indexDirectoryFixture(t *testing.T, project string) string {
	t.Helper()
	root := filepath.Join(project, "docs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.md", "b.md", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("directorymarker "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestIndexDirectoryControls(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	root := indexDirectoryFixture(t, srv.projectDir)
	r := callIndex(t, srv, map[string]any{"path": root, "source": "docs:api", "max_files": 1, "respect_gitignore": " FaLsE "})
	if r.IsError || !strings.Contains(resultText(r), "incomplete") || !strings.Contains(resultText(r), "max_files") || !strings.Contains(resultText(r), "Indexed: 1") {
		t.Fatalf("limited scan: %s", resultText(r))
	}
	r = callIndex(t, srv, map[string]any{"path": "docs", "source": "docs:api", "extensions": []any{"MD"}, "include": `["**/*.md"]`, "respect_gitignore": false})
	if r.IsError || !strings.Contains(resultText(r), "complete") || !strings.Contains(resultText(r), "unchanged: 1") || !strings.Contains(resultText(r), "retained sections: 2") {
		t.Fatalf("repeat scan: %s", resultText(r))
	}
	sources, err := srv.getStore().ListSources()
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources = %+v, %v", sources, err)
	}
	for _, source := range sources {
		if !strings.HasPrefix(source.Label, "docs:api:") {
			t.Fatalf("label lost colon prefix: %q", source.Label)
		}
	}
}

func TestIndexDirectoryInvalidInputsBeforeSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []any
	}{
		{"max_depth", []any{nil, "2", -1, 21, 0.5, math.NaN(), math.Inf(1), true}},
		{"max_files", []any{nil, "2", 0, 1001, 0.5, false}},
		{"include", []any{nil, "oops", "null", []any{1}, []any{"../secret"}, []string{"[ab]"}}},
		{"exclude", []any{nil, []any{false}, []any{"/absolute"}}},
		{"extensions", []any{nil, []any{"md", 1}, []string{"*.md"}}},
		{"respect_gitignore", []any{nil, 0, 1, "yes", []any{}}},
		{"follow_symlinks", []any{nil, 0, 1, "yes", []any{}}},
	} {
		for n, value := range tc.values {
			t.Run(fmt.Sprintf("%s-%d", tc.name, n), func(t *testing.T) {
				srv := newTestServer(t, nil)
				root := indexDirectoryFixture(t, srv.projectDir)
				for _, base := range []map[string]any{{"path": root}, {"content": "no side effects", "source": "invalid"}} {
					base[tc.name] = value
					r := callIndex(t, srv, base)
					if !r.IsError {
						t.Fatalf("accepted invalid value: %s", resultText(r))
					}
					if srv.store != nil {
						t.Fatal("invalid controls initialized store")
					}
				}
			})
		}
	}
}

func TestIndexDirectoryPartialFailureAndCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newTestServer(t, nil)
	root := indexDirectoryFixture(t, srv.projectDir)
	srv.config.Store.MaxSourceBytes = 32
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte(strings.Repeat("x", 33)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := callIndex(t, srv, map[string]any{"path": root, "respect_gitignore": false})
	if !r.IsError || !strings.Contains(resultText(r), "partial error") || !strings.Contains(resultText(r), "Indexed: 2") || !strings.Contains(resultText(r), "failed: 1") {
		t.Fatalf("partial failure hidden: %s", resultText(r))
	}
	sources, err := srv.getStore().ListSources()
	if err != nil || len(sources) != 2 {
		t.Fatalf("lost partial progress: %+v, %v", sources, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"path": root, "respect_gitignore": false}
	r, err = srv.handleIndex(ctx, req)
	if err != nil || !r.IsError || !strings.Contains(resultText(r), "canceled") {
		t.Fatalf("canceled response = %+v, %v", r, err)
	}
}

func TestIndexDirectoryReadPolicyAndRefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	root := indexDirectoryFixture(t, project)
	if err := os.Mkdir(filepath.Join(project, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(project, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"permissions":{"deny":["Read(docs/b.md)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := newTestServerWithProjectDir(t, nil, project)
	r := callIndex(t, srv, map[string]any{"path": root, "source": "rules", "respect_gitignore": false})
	if r.IsError || !strings.Contains(resultText(r), "denied: 1") {
		t.Fatalf("walker deny failed: %s", resultText(r))
	}
	r = callIndex(t, srv, map[string]any{"path": filepath.Join(root, "b.md")})
	if !r.IsError {
		t.Fatal("explicit absolute file bypassed relative deny")
	}
	// A new policy snapshot also protects file-backed refresh of walked sources.
	if err := os.WriteFile(settings, []byte(`{"permissions":{"deny":["Read(docs/a.md)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := security.LoadReadPolicy(security.FilePolicyContext{ProjectDir: project})
	if err != nil {
		t.Fatal(err)
	}
	srv.getStore().SetDenyChecker(func(path string) bool { return policy.Check(path) != nil })
	path := filepath.Join(root, "a.md")
	if err := os.WriteFile(path, []byte("changedprivate"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	results, err := srv.getStore().SearchWithFallbackContext(t.Context(), "directorymarker", 3, store.SearchOptions{Source: "rules:a.md"})
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Content, "directorymarker") {
		t.Fatalf("denied refresh discarded cached content: %+v, %v", results, err)
	}
}
