package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/serpro69/capy/internal/fileio"
	"github.com/serpro69/capy/internal/store"
)

func directoryFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, full, content)
	}
}

func sourceLabels(t *testing.T, st *store.ContentStore) []string {
	t.Helper()
	sources, err := st.ListSources()
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(sources))
	for _, source := range sources {
		if source.Kind != store.KindDurable {
			t.Fatalf("directory source not durable: %+v", source)
		}
		labels = append(labels, source.Label)
	}
	slices.Sort(labels)
	return labels
}

func TestIndexDirectoryLifecycle(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{"b.txt": "bravo", "a.md": "alpha", "sub/c.go": "charlie", "ignored.bin": "skip"})
	for _, prefix := range []string{"docs:v1", "docs:v1", "docs:v2"} {
		out, err := i.IndexDirectory(t.Context(), root, prefix, DirectoryOptions{})
		if err != nil || !out.Complete || out.Indexed+out.Unchanged != 3 || out.Skipped != 1 || out.Attempts != 3 || out.Chunks != 3 || out.BytesRead != 17 {
			t.Fatalf("scan = %+v, %v", out, err)
		}
		if prefix == "docs:v2" && out.Unchanged != 0 {
			t.Fatal("different prefix must create new sources")
		}
	}
	if got := sourceLabels(t, st); !slices.Equal(got, []string{"docs:v1:a.md", "docs:v1:b.txt", "docs:v1:sub/c.go", "docs:v2:a.md", "docs:v2:b.txt", "docs:v2:sub/c.go"}) {
		t.Fatalf("labels = %v", got)
	}
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	out, err := i.IndexDirectory(t.Context(), root, "docs:v1", DirectoryOptions{Exclude: []string{"sub/**"}})
	if err != nil || out.Unchanged != 1 || len(sourceLabels(t, st)) != 6 {
		t.Fatalf("removed/excluded files must remain stored: %+v, %v", out, err)
	}
	// The same prefix over another root intentionally replaces equal relpaths.
	external := t.TempDir()
	directoryFiles(t, external, map[string]string{"b.txt": "replacementmarker"})
	if _, err := i.IndexDirectory(t.Context(), external, "docs:v1", DirectoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(sourceLabels(t, st)) != 6 {
		t.Fatal("prefix identity changed across roots")
	}
}

func TestIndexDirectorySelection(t *testing.T) {
	i, st, project := testIndexer(t, 0, `{"permissions":{"deny":["Read(denied.md)","Read(docs/blocked/**)"]}}`)
	root := filepath.Join(project, "docs")
	directoryFiles(t, root, map[string]string{
		"ok.MD": "ok", "denied.md": "deny", "sub/keep.md": "keep", "sub/drop.md": "drop", "other.go": "go",
		"blocked/private.md": "deny", "node_modules/x.md": "skip", ".env.md": "skip", "secret.key": "skip", ".capy/state.md": "skip",
	})
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "broken.md")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := i.IndexDirectory(t.Context(), root, "picked", DirectoryOptions{Extensions: []string{" MD "}, Include: []string{"**/*.md", "*.MD"}, Exclude: []string{"sub/drop.md"}})
	if err != nil || !out.Complete || out.Indexed != 2 || out.Denied != 2 || out.Attempts != 2 || out.Skipped != 9 {
		t.Fatalf("selection = %+v, %v", out, err)
	}
	if got := sourceLabels(t, st); !slices.Equal(got, []string{"picked:ok.MD", "picked:sub/keep.md"}) {
		t.Fatalf("labels = %v", got)
	}
	// Explicit file inputs keep their original contract despite recursive controls.
	direct, err := i.IndexPath(t.Context(), filepath.Join(root, ".env.md"), "explicit", DirectoryOptions{Extensions: []string{"go"}})
	if err != nil || direct.File == nil || direct.File.Result.Label != "explicit" {
		t.Fatalf("explicit file = %+v, %v", direct, err)
	}
}

func TestIndexDirectoryRootAdmission(t *testing.T) {
	for _, name := range []string{"link", "external", "parent-after-link", "denied-target", "denied-alias", "denied-alias-children", "missing", "file", "nil-policy"} {
		t.Run(name, func(t *testing.T) {
			settings := ""
			if name == "denied-target" {
				settings = `{"permissions":{"deny":["Read(real)"]}}`
			}
			if name == "denied-alias" {
				settings = `{"permissions":{"deny":["Read(alias)"]}}`
			}
			if name == "denied-alias-children" {
				settings = `{"permissions":{"deny":["Read(alias/**)"]}}`
			}
			i, st, project := testIndexer(t, 0, settings)
			root := filepath.Join(project, "real")
			directoryFiles(t, root, map[string]string{"note.md": "content", "sub/deep.txt": "deeper"})
			if err := os.Symlink(root, filepath.Join(project, "alias")); err != nil {
				t.Fatal(err)
			}
			input := "alias"
			wantErr := false
			switch name {
			case "external":
				root = t.TempDir()
				directoryFiles(t, root, map[string]string{"note.md": "outside"})
				input = root
			case "parent-after-link":
				if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(project, "deep")); err != nil {
					t.Fatal(err)
				}
				input = "deep/.."
			case "denied-target", "denied-alias":
				wantErr = true
			case "missing":
				input, wantErr = "missing", true
			case "file":
				input, wantErr = "real/note.md", true
			case "nil-policy":
				i.policy, wantErr = nil, true
			}
			out, err := i.IndexDirectory(t.Context(), input, "", DirectoryOptions{})
			if wantErr {
				if err == nil {
					t.Fatalf("invalid root accepted: %+v", out)
				}
				return
			}
			if err != nil || !out.Complete || out.Root != root {
				t.Fatalf("root = %+v, %v", out, err)
			}
			if name == "denied-alias-children" && (out.Indexed != 0 || out.Denied != 2) {
				t.Fatalf("root alias bypassed child denies: %+v", out)
			}
			for _, label := range sourceLabels(t, st) {
				if !strings.HasPrefix(label, root+"/") {
					t.Fatalf("not canonical: %s", label)
				}
			}
		})
	}
}

func TestIndexDirectoryFileAndDepthLimits(t *testing.T) {
	for _, maxFiles := range []int{1, 200, 1000} {
		t.Run(fmt.Sprintf("files-%d", maxFiles), func(t *testing.T) {
			i, st, project := testIndexer(t, 0, "")
			root := filepath.Join(project, "docs")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			for n := maxFiles; n >= 0; n-- {
				writeFile(t, filepath.Join(root, fmt.Sprintf("%04d.txt", n)), "content")
			}
			options := DirectoryOptions{}
			if maxFiles != 200 {
				options.MaxFiles = &maxFiles
			}
			out, err := i.IndexDirectory(t.Context(), root, "limit", options)
			if err != nil || out.Complete || out.Attempts != maxFiles || out.Indexed != maxFiles || !slices.Equal(out.Limits, []string{"max_files"}) {
				t.Fatalf("limit = %+v, %v", out, err)
			}
			labels := sourceLabels(t, st)
			if labels[0] != "limit:0000.txt" || labels[len(labels)-1] != fmt.Sprintf("limit:%04d.txt", maxFiles-1) {
				t.Fatalf("nonlexical prefix: %v", labels)
			}
			if maxFiles == 1000 {
				checks := 0
				st.SetDenyChecker(func(string) bool { checks++; return false })
				if _, err := st.SearchWithFallbackContext(t.Context(), "content", 1, store.SearchOptions{}); err != nil {
					t.Fatal(err)
				}
				if checks < 1 || checks > 32 {
					t.Fatalf("directory ingestion caused unbounded stale checks: %d", checks)
				}
			}
		})
	}
	for _, depth := range []int{0, 5, 20} {
		t.Run(fmt.Sprintf("depth-%d", depth), func(t *testing.T) {
			i, _, project := testIndexer(t, 0, "")
			root := filepath.Join(project, "docs")
			for n := 0; n <= depth+1; n++ {
				directoryFiles(t, root, map[string]string{strings.Repeat("sub/", n) + "a.md": "content"})
			}
			out, err := i.IndexDirectory(t.Context(), root, "depth", DirectoryOptions{MaxDepth: &depth})
			if err != nil || out.Complete || out.Indexed != depth+1 || !slices.Equal(out.Limits, []string{"max_depth"}) {
				t.Fatalf("depth = %+v, %v", out, err)
			}
		})
	}
}

func TestIndexDirectoryEntryLimit(t *testing.T) {
	i, _, project := testIndexer(t, 0, "")
	root := filepath.Join(project, "huge")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < maxDirectoryEntries+1; n++ {
		writeFile(t, filepath.Join(root, fmt.Sprintf("%05d.txt", n)), "")
	}
	out, err := i.IndexDirectory(t.Context(), root, "huge", DirectoryOptions{})
	if err != nil || out.Complete || out.Visited != maxDirectoryEntries || out.Attempts != 0 || !slices.Equal(out.Limits, []string{"max_entries"}) {
		t.Fatalf("entry cap = %+v, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial directory was indexed: %v", err)
	}
}

func TestIndexDirectoryReadOutcomes(t *testing.T) {
	for _, name := range []string{"growth", "read-failure", "index-failure", "cancellation", "unchanged"} {
		t.Run(name, func(t *testing.T) {
			i, st, project := testIndexer(t, 64, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"a.txt": "initial", "b.txt": "next"})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "unchanged" {
				if _, err := i.IndexDirectory(ctx, root, "read", DirectoryOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if name == "index-failure" {
				i.store = store.NewContentStore(filepath.Join(project, "invalid.db"), project, 0, 64, store.WithEncryptionKey("", "test"))
				t.Cleanup(func() {
					if err := i.store.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			calls := 0
			i.readContent = func(r io.Reader, limit int) ([]byte, error) {
				calls++
				if calls == 1 {
					switch name {
					case "growth":
						writeFile(t, filepath.Join(root, "a.txt"), strings.Repeat("x", 1000))
					case "read-failure":
						return []byte("partial"), syscall.EIO
					case "cancellation":
						cancel()
					}
				}
				return fileio.ReadAll(r, limit)
			}
			out, err := i.IndexDirectory(ctx, root, "read", DirectoryOptions{})
			wantBytes, wantAttempts := int64(11), 2
			if name == "growth" {
				wantBytes = 69
			}
			if name == "cancellation" {
				wantBytes, wantAttempts = 7, 1
			}
			if out.BytesRead != wantBytes || out.Attempts != wantAttempts {
				t.Fatalf("raw accounting = %+v, %v", out, err)
			}
			if name == "unchanged" {
				if err != nil || !out.Complete || out.Unchanged != 2 || out.Chunks != 2 {
					t.Fatalf("unchanged = %+v, %v", out, err)
				}
			} else if err == nil || out.Complete || out.Failed == 0 {
				t.Fatalf("failure hidden: %+v, %v", out, err)
			}
			if name == "cancellation" && out.Failed != 1 {
				t.Fatalf("cancellation counted more than once: %+v", out)
			}
			if name == "growth" || name == "read-failure" {
				if got := sourceLabels(t, st); !slices.Equal(got, []string{"read:b.txt"}) {
					t.Fatalf("partial file indexed: %v", got)
				}
			}
		})
	}
}

func TestIndexDirectoryByteBudget(t *testing.T) {
	for _, name := range []string{"first-file-growth", "failed-index-counts"} {
		t.Run(name, func(t *testing.T) {
			limit := 2 << 20
			if name == "first-file-growth" {
				limit = maxDirectoryBytes + 100
			}
			i, _, project := testIndexer(t, limit, "")
			root := filepath.Join(project, "docs")
			directoryFiles(t, root, map[string]string{"a.txt": "small"})
			if name == "first-file-growth" {
				i.readContent = func(r io.Reader, limit int) ([]byte, error) {
					if err := os.Truncate(filepath.Join(root, "a.txt"), maxDirectoryBytes+1); err != nil {
						t.Fatal(err)
					}
					return fileio.ReadAll(r, limit)
				}
			} else {
				if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
					t.Fatal(err)
				}
				for n := 0; n < 17; n++ {
					file := filepath.Join(root, fmt.Sprintf("%02d.txt", n))
					writeFile(t, file, "")
					if err := os.Truncate(file, int64(limit)); err != nil {
						t.Fatal(err)
					}
				}
				i.store = store.NewContentStore(filepath.Join(project, "invalid.db"), project, 0, limit, store.WithEncryptionKey("", "test"))
				t.Cleanup(func() {
					if err := i.store.Close(); err != nil {
						t.Error(err)
					}
				})
			}
			out, err := i.IndexDirectory(t.Context(), root, "bytes", DirectoryOptions{})
			if out.Complete || out.BytesRead != maxDirectoryBytes || out.Indexed != 0 || !slices.Contains(out.Limits, "max_bytes") {
				t.Fatalf("byte cap = %+v, %v", out, err)
			}
			if name == "first-file-growth" && (err != nil || out.Failed != 0 || out.BytesIndexed != 0) {
				t.Fatalf("request cap treated as source failure: %+v, %v", out, err)
			}
			if name == "failed-index-counts" && (err == nil || out.Failed != 15 || len(out.Failures) != 10 || out.OmittedFailures != 5) {
				t.Fatalf("failed byte/failure counts: %+v, %v", out, err)
			}
		})
	}
}

func TestIndexDirectoryEmptyAndFiltered(t *testing.T) {
	for _, name := range []string{"empty", "filtered", "denied", "canceled"} {
		t.Run(name, func(t *testing.T) {
			i, _, project := testIndexer(t, 0, `{"permissions":{"deny":["Read(secret.md)"]}}`)
			root := filepath.Join(project, "docs")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if name == "filtered" {
				writeFile(t, filepath.Join(root, "skip.bin"), "skip")
			}
			if name == "denied" {
				writeFile(t, filepath.Join(root, "secret.md"), "skip")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			out, err := i.IndexDirectory(ctx, root, "", DirectoryOptions{})
			if name == "canceled" {
				if !errors.Is(err, context.Canceled) || out.Complete {
					t.Fatalf("cancel = %+v, %v", out, err)
				}
				return
			}
			if err != nil || !out.Complete || out.Attempts != 0 || out.BytesRead != 0 {
				t.Fatalf("empty scan = %+v, %v", out, err)
			}
			if name == "filtered" && out.Skipped != 1 || name == "denied" && out.Denied != 1 {
				t.Fatalf("filter counts: %+v", out)
			}
		})
	}
}

func TestDirectoryRemainingReadAllowance(t *testing.T) {
	for _, tc := range []struct {
		name      string
		content   string
		allowance int64
		wantCap   bool
	}{
		{name: "empty with one byte", allowance: 1},
		{name: "one byte needs probe", content: "a", allowance: 1, wantCap: true},
		{name: "complete with probe room", content: "a", allowance: 2},
		{name: "partial is never indexed", content: "abc", allowance: 2, wantCap: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i, _, project := testIndexer(t, 64, "")
			path := filepath.Join(project, "file.md")
			writeFile(t, path, tc.content)
			out, err := i.indexAdmittedFile(t.Context(), path, "allowance", tc.allowance, true)
			if errors.Is(err, errDirectoryByteLimit) != tc.wantCap || out.BytesRead > tc.allowance {
				t.Fatalf("allowance = %+v, %v", out, err)
			}
			if tc.wantCap && out.Result != nil {
				t.Fatal("partial content was indexed")
			}
			if !tc.wantCap && (err != nil || out.Result == nil) {
				t.Fatalf("complete file failed: %+v, %v", out, err)
			}
		})
	}
	i, _, project := testIndexer(t, 64, "")
	path := filepath.Join(project, "file.md")
	writeFile(t, path, "abc")
	i.readContent = func(io.Reader, int) ([]byte, error) { return []byte("ab"), syscall.EIO }
	out, err := i.indexAdmittedFile(t.Context(), path, "allowance", 2, true)
	if !errors.Is(err, syscall.EIO) || out.BytesRead != 2 || out.Result != nil {
		t.Fatalf("operational error hidden by cap: %+v, %v", out, err)
	}
}

func TestIndexPathPreservesExplicitFileCleaning(t *testing.T) {
	for _, name := range []string{"missing physical target", "different physical file", "physical directory"} {
		t.Run(name, func(t *testing.T) {
			i, st, project := testIndexer(t, 0, "")
			directoryFiles(t, project, map[string]string{"a.md": "lexicalmarker", "real/sub/child.txt": "child"})
			if err := os.Symlink(filepath.Join(project, "real/sub"), filepath.Join(project, "alias")); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "different physical file":
				writeFile(t, filepath.Join(project, "real/a.md"), "physicalmarker")
			case "physical directory":
				directoryFiles(t, project, map[string]string{"real/a.md/child.md": "physicalmarker"})
			}
			out, err := i.IndexPath(t.Context(), "alias/../a.md", "preserved", DirectoryOptions{})
			if err != nil || out.File == nil || out.Directory != nil || out.File.Path != filepath.Join(project, "a.md") {
				t.Fatalf("file dispatch = %+v, %v", out, err)
			}
			chunks, err := st.GetChunksBySource(out.File.Result.SourceID)
			if err != nil || len(chunks) != 1 || chunks[0].Content != "lexicalmarker" {
				t.Fatalf("file target changed: %+v, %v", chunks, err)
			}
		})
	}
}
