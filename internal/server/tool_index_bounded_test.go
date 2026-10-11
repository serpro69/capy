package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/store"
)

func TestIndexFileLimitAndAccounting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	srv := newTestServerWithProjectDir(t, nil, project)
	srv.config.Store.MaxSourceBytes = 64
	path := filepath.Join(project, "document.txt")
	for _, size := range []int{64, 65, 64} {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o600); err != nil {
			t.Fatal(err)
		}
		result := callIndex(t, srv, map[string]any{"path": path})
		if result.IsError != (size > 64) {
			t.Fatalf("size %d: %s", size, resultText(result))
		}
		if size > 64 && !strings.Contains(resultText(result), "64 byte limit") {
			t.Fatalf("missing actionable limit: %s", resultText(result))
		}
	}
	stats := srv.stats.Snapshot()
	if stats.BytesIndexed != 128 || stats.Calls["capy_index"] != 2 {
		t.Fatalf("rejected file was counted or unchanged input was lost: bytes=%d calls=%d", stats.BytesIndexed, stats.Calls["capy_index"])
	}
	sources, err := srv.getStore().ListSources()
	if err != nil || len(sources) != 1 || sources[0].Kind != store.KindDurable {
		t.Fatalf("durable dedup changed: %+v, %v", sources, err)
	}
}

func TestIndexInlineContentWithPath(t *testing.T) {
	for _, denied := range []bool{false, true} {
		name := "allowed"
		if denied {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			project := t.TempDir()
			srv := newDenyIndexServer(t, project, "Read(secret.txt)")
			path := "missing.txt"
			if denied {
				path = "secret.txt"
			}
			result := callIndex(t, srv, map[string]any{"path": path, "content": "inlinemarker"})
			if result.IsError != denied {
				t.Fatalf("unexpected inline/path outcome: %s", resultText(result))
			}
			if denied {
				if !strings.Contains(resultText(result), "Read deny pattern") || srv.store != nil {
					t.Fatalf("denied inline/path request reached store: %s", resultText(result))
				}
				return
			}
			// A path accompanying inline content never becomes its backing file.
			if err := os.WriteFile(filepath.Join(project, path), []byte("replacementmarker"), 0o600); err != nil {
				t.Fatal(err)
			}
			results, err := srv.getStore().SearchWithFallback("inlinemarker", 3, store.SearchOptions{})
			if err != nil || len(results) != 1 || results[0].Label != "indexed-content" {
				t.Fatalf("inline content acquired a backing file: %+v, %v", results, err)
			}
		})
	}
}
