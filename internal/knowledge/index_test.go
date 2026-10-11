package knowledge

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/fileio"
	"github.com/serpro69/capy/internal/security"
	"github.com/serpro69/capy/internal/store"
)

func testIndexer(t *testing.T, limit int, settings string) (*Indexer, *store.ContentStore, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	if settings != "" {
		if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(project, ".claude", "settings.json"), settings)
	}
	policy, err := security.LoadReadPolicy(security.FilePolicyContext{ProjectDir: project})
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewContentStore(filepath.Join(project, "knowledge.db"), project, 0, limit,
		store.WithEncryptionKey("synthetic-task12-key-at-least-32-characters", "test"))
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return NewIndexer(project, limit, policy, st), st, project
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIndexFileLifecycle(t *testing.T) {
	i, st, project := testIndexer(t, 0, "")
	path := filepath.Join(project, "notes.txt")
	const original = "originalmarker with useful notes"
	writeFile(t, path, original)
	var firstID int64
	for n, spelling := range []string{"notes.txt", "./notes.txt", path} {
		out, err := i.IndexFile(t.Context(), spelling, "")
		if err != nil {
			t.Fatal(err)
		}
		if out.Path != path || out.Result.Label != path || out.Result.Kind != store.KindDurable {
			t.Fatalf("unexpected path/label/kind: %+v, %+v", out, out.Result)
		}
		if out.BytesRead != int64(len(original)) || out.BytesIndexed != out.BytesRead {
			t.Fatalf("incorrect byte accounting: %+v", out)
		}
		if out.Result.AlreadyIndexed != (n > 0) {
			t.Fatalf("dedup = %v on attempt %d", out.Result.AlreadyIndexed, n)
		}
		if n == 0 {
			firstID = out.Result.SourceID
		} else if out.Result.SourceID != firstID {
			t.Fatal("equivalent spellings created a new source")
		}
	}
	writeFile(t, path, "replacementmarker for refreshed content")
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	results, err := st.SearchWithFallback("replacementmarker", 3, store.SearchOptions{Source: path})
	if err != nil || len(results) == 0 || !strings.Contains(results[0].Content, "replacementmarker") {
		t.Fatalf("file-backed refresh failed: %+v, %v", results, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	sources, err := st.ListSources()
	if err != nil || len(sources) != 1 || sources[0].Label != path {
		t.Fatalf("encrypted reopen lost source: %+v, %v", sources, err)
	}
}

func TestIndexFileAdmission(t *testing.T) {
	for _, name := range []string{"empty", "limit", "oversize", "missing", "directory", "fifo", "device", "denied", "alias-denied", "nil-policy", "relative-escape", "absolute-external"} {
		t.Run(name, func(t *testing.T) {
			i, st, project := testIndexer(t, 64, `{"permissions":{"deny":["Read(secret.txt)"]}}`)
			path, content, wantErr := filepath.Join(project, "file.txt"), "hello", ""
			switch name {
			case "empty":
				content = ""
			case "limit":
				content = strings.Repeat("x", 64)
			case "oversize":
				content, wantErr = strings.Repeat("x", 65), "file too large"
			case "missing":
				wantErr = "read file"
			case "directory":
				path, wantErr = project, "not a regular file"
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				wantErr = "not a regular file"
			case "device":
				path, wantErr = "/dev/null", "not a regular file"
			case "denied", "alias-denied":
				path, wantErr = filepath.Join(project, "secret.txt"), "Read deny pattern"
				writeFile(t, path, content)
				if name == "alias-denied" {
					alias := filepath.Join(project, "innocent.txt")
					if err := os.Symlink(path, alias); err != nil {
						t.Fatal(err)
					}
					path = alias
				}
			case "nil-policy":
				i.policy, wantErr = nil, "Read policy is unavailable"
			case "relative-escape":
				path, wantErr = "../outside.txt", "path escapes project directory"
			case "absolute-external":
				path = filepath.Join(t.TempDir(), "external.txt")
			}
			if wantErr == "" || name == "oversize" || name == "nil-policy" {
				writeFile(t, path, content)
			}
			out, err := i.IndexFile(t.Context(), path, "custom-label")
			if wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), wantErr) || out.BytesRead != 0 || out.BytesIndexed != 0 {
					t.Fatalf("admission = %+v, %v; want %q without content reads", out, err, wantErr)
				}
				if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !os.IsNotExist(err) {
					t.Fatalf("rejected input opened database: %v", err)
				}
				return
			}
			if err != nil || out.Result.Label != "custom-label" || out.BytesRead != int64(len(content)) {
				t.Fatalf("index = %+v, %v", out, err)
			}
			sources, err := st.ListSources()
			if err != nil || len(sources) != 1 {
				t.Fatalf("sources = %+v, %v", sources, err)
			}
		})
	}
}

func TestIndexFileGrowthAfterStat(t *testing.T) {
	i, st, project := testIndexer(t, 64, "")
	path := filepath.Join(project, "growing.txt")
	writeFile(t, path, "cachedmarker")
	if _, err := i.IndexFile(t.Context(), path, "retained"); err != nil {
		t.Fatal(err)
	}
	i.readContent = func(r io.Reader, limit int) ([]byte, error) {
		writeFile(t, path, strings.Repeat("x", 1024))
		return fileio.ReadAll(r, limit)
	}
	out, err := i.IndexFile(t.Context(), path, "retained")
	if err == nil || out.BytesRead != 65 || out.BytesIndexed != 0 || out.Result != nil {
		t.Fatalf("growth must stop after detection byte without indexing: %+v, %v", out, err)
	}
	sources, err := st.ListSources()
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources: %+v, %v", sources, err)
	}
	chunks, err := st.GetChunksBySource(sources[0].ID)
	if err != nil || len(chunks) != 1 || chunks[0].Content != "cachedmarker" {
		t.Fatalf("growth replaced cached content: %+v, %v", chunks, err)
	}
}

func TestIndexFileReadFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"read-failure", "canceled-before-read", "canceled-after-read"} {
		t.Run(name, func(t *testing.T) {
			i, _, project := testIndexer(t, 64, "")
			path := filepath.Join(project, "file.txt")
			writeFile(t, path, "small")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := error(context.Canceled)
			if name == "canceled-before-read" {
				cancel()
			}
			if name == "read-failure" {
				want = syscall.EIO
			}
			i.readContent = func(r io.Reader, limit int) ([]byte, error) {
				if name == "read-failure" {
					return []byte("partial"), syscall.EIO
				}
				if name == "canceled-before-read" {
					t.Error("canceled input was read")
				}
				cancel()
				return fileio.ReadAll(r, limit)
			}
			out, err := i.IndexFile(ctx, path, "")
			if !errors.Is(err, want) || out.BytesIndexed != 0 || out.Result != nil {
				t.Fatalf("outcome = %+v, %v; want %v without indexing", out, err, want)
			}
			if _, err := os.Stat(filepath.Join(project, "knowledge.db")); !os.IsNotExist(err) {
				t.Fatalf("failed input opened database: %v", err)
			}
		})
	}
}
