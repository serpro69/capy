package store

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/serpro69/capy/internal/fileio"
)

// seedRefreshWorkload creates real backing files and bulk-seeds matching source
// and FTS rows. Bulk setup keeps the 10,000-source fixture practical; measured
// searches use the ordinary production read/write paths.
func seedRefreshWorkload(tb testing.TB, s *ContentStore, dir string, count, changed int) {
	tb.Helper()
	db, err := s.getDB()
	if err != nil {
		tb.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	ins, err := tx.Prepare(`INSERT INTO sources (label, content_type, chunk_count, content_hash, kind, file_path) VALUES (?, 'plaintext', 1, ?, 'durable', ?)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer ins.Close()
	chunk, err := tx.Prepare(`INSERT INTO chunks (title, content, source_id, content_type) VALUES ('fixture', ?, ?, 'prose')`)
	if err != nil {
		tb.Fatal(err)
	}
	defer chunk.Close()
	trigram, err := tx.Prepare(`INSERT INTO chunks_trigram (title, content, source_id, content_type) VALUES ('fixture', ?, ?, 'prose')`)
	if err != nil {
		tb.Fatal(err)
	}
	defer trigram.Close()
	const original = "refreshmarker original content"
	old, future := time.Unix(946684800, 0), time.Now().Add(time.Minute)
	for n := range count {
		path := filepath.Join(dir, fmt.Sprintf("backing-%05d.txt", n))
		content, mtime := original, old
		if n < changed {
			content, mtime = "refreshmarker changed content", future
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			tb.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			tb.Fatal(err)
		}
		res, err := ins.Exec(fmt.Sprintf("fixture-%05d", n), contentHash(original), path)
		if err != nil {
			tb.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := chunk.Exec(original, id); err != nil {
			tb.Fatal(err)
		}
		if _, err := trigram.Exec(original, id); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

func BenchmarkStaleRefreshWorkload(b *testing.B) {
	for _, changed := range []int{0, 128} {
		name := "unchanged"
		if changed != 0 {
			name = "128-changed"
		}
		b.Run(name, func(b *testing.B) {
			var attempts, readBytes, writes int64
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				dir := b.TempDir()
				s := NewContentStore(filepath.Join(dir, "test.db"), dir, 0, 0, WithEncryptionKey(testEncryptionKey, "test"))
				seedRefreshWorkload(b, s, dir, 10000, changed)
				db, err := s.getDB()
				if err != nil {
					b.Fatal(err)
				}
				// total_changes is connection-local: pin this serial measurement
				// to one pooled connection, without holding it across the search.
				db.SetMaxOpenConns(1)
				db.SetMaxIdleConns(1)
				s.SetDenyChecker(func(string) bool { attempts++; return false })
				s.readFileContent = func(r io.Reader, limit int) ([]byte, error) {
					data, err := fileio.ReadAll(r, limit)
					readBytes += int64(len(data))
					return data, err
				}
				var before, after int64
				if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				results, err := s.SearchWithFallback("refreshmarker", 1, SearchOptions{Source: "fixture-00000", SourceMatchMode: "exact"})
				b.StopTimer()
				if err != nil || len(results) != 1 {
					b.Fatalf("search: %v, %v", results, err)
				}
				if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
					b.Fatal(err)
				}
				writes += after - before
				if err := s.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(attempts)/float64(b.N), "files/op")
			b.ReportMetric(float64(readBytes)/float64(b.N), "read-B/op")
			b.ReportMetric(float64(writes)/float64(b.N), "sqlite-changes/op")
		})
	}
}
