package store

import (
	"bytes"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/serpro69/capy/internal/fileio"
)

func TestStaleRefreshBoundedReads(t *testing.T) {
	for _, name := range []string{"growth-after-stat", "read-failure", "oversized-at-stat"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := NewContentStore(filepath.Join(dir, "test.db"), dir, 0, 64,
				WithEncryptionKey(testEncryptionKey, "test"))
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			path := writeAndIndexFile(t, s, dir, "document.txt", "cachedmarker original")
			modifyFile(t, path, "small changed content")
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			var readBytes int
			readCalled := false
			s.readFileContent = func(r io.Reader, limit int) ([]byte, error) {
				readCalled = true
				if name == "read-failure" {
					return []byte("partial"), syscall.EIO
				}
				if name == "oversized-at-stat" {
					t.Error("stat-rejected file was read")
				}
				// The old unbounded path sanitizes this below 64 bytes and
				// replaces the cache, so store admission cannot mask this bug.
				modifyFile(t, path, "replacementmarker <private>"+strings.Repeat("z", 1024)+"</private>")
				data, err := fileio.ReadAll(r, limit)
				readBytes = len(data)
				return data, err
			}
			if name == "oversized-at-stat" {
				modifyFile(t, path, strings.Repeat("x", 65))
			}
			before, err := s.GetSourceMeta("src:document.txt")
			if err != nil {
				t.Fatal(err)
			}
			if count := forceRefresh(s); count != 0 {
				t.Errorf("rejected refresh replaced %d sources", count)
			}
			if got := sourceContent(t, s, "src:document.txt"); !strings.Contains(got, "cachedmarker original") {
				t.Errorf("cached content replaced: %q", got)
			}
			after, err := s.GetSourceMeta("src:document.txt")
			if err != nil || !after.IndexedAt.Equal(before.IndexedAt) {
				t.Errorf("failed refresh advanced index time: %+v, %v", after, err)
			}
			if name == "growth-after-stat" && readBytes != 65 {
				t.Errorf("read %d bytes; want exactly limit + 1", readBytes)
			}
			if name == "oversized-at-stat" && readCalled {
				t.Error("oversized file reached reader")
			}
			if !strings.Contains(logs.String(), "stale refresh:") || !strings.Contains(logs.String(), path) {
				t.Errorf("skipped refresh must be logged with path: %q", logs.String())
			}
		})
	}
}
