package runstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/executor"
)

// Separate capture-only, first-search and repeat-search costs. Opening the
// encrypted store and fixture preparation are outside the timed search region.
func BenchmarkRunSearch(b *testing.B) {
	data := strings.Repeat("passing fixture output\n", 6000) + "middle diagnostic failure\n"
	for _, mode := range []string{"capture_only", "first_search", "repeat_search", "fragmented_search"} {
		b.Run(mode, func(b *testing.B) {
			fixture := data
			if mode == "fragmented_search" {
				fixture = strings.Repeat(string([]byte{255, 'x'}), 4096) + "\nfragmented diagnostic marker\n"
			}
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				root, err := os.MkdirTemp(b.TempDir(), "run-")
				if err != nil {
					b.Fatal(err)
				}
				s, err := Open(b.Context(), filepath.Join(root, ".capy", "runs", "runs.db"), testKey, true)
				if err != nil {
					b.Fatal(err)
				}
				o, err := s.ClaimOwner(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				row, err := s.Admit(b.Context(), o, executor.CleanupLinuxDescendants, Program{})
				if err != nil {
					b.Fatal(err)
				}
				if err := s.Pending(b.Context(), row.ID, o.ID, executor.CleanupLinuxDescendants); err != nil {
					b.Fatal(err)
				}
				if err := s.Started(b.Context(), row.ID, "run"); err != nil {
					b.Fatal(err)
				}
				if mode == "capture_only" {
					b.StartTimer()
				}
				var seq uint64
				for offset := 0; offset < len(fixture); offset += executor.EventBlockBytes {
					seq++
					end := min(len(fixture), offset+executor.EventBlockBytes)
					if err := s.Append(b.Context(), row.ID, executor.OutputEvent{Sequence: seq, Phase: "run", Stream: "stdout", Data: []byte(fixture[offset:end])}); err != nil {
						b.Fatal(err)
					}
				}
				code := 0
				if err := s.Finish(b.Context(), row.ID, Completion{Cleanup: executor.CleanupEvidence{Scope: executor.CleanupLinuxDescendants, State: executor.CleanupRetired}, Started: true, ExitCode: &code, ObservedBytes: int64(len(fixture))}); err != nil {
					b.Fatal(err)
				}
				if mode == "capture_only" {
					b.StopTimer()
				}
				search := func() {
					for range 100 {
						page, err := s.Search(b.Context(), o, SearchOptions{RunID: row.ID, Queries: []string{"diagnostic"}, Limit: 5})
						if err != nil {
							b.Fatal(err)
						}
						if page.Error != nil {
							b.Fatal(page.Error)
						}
						if !page.Partial {
							return
						}
					}
					b.Fatal("index did not finish")
				}
				if mode == "repeat_search" {
					search()
				}
				if mode != "capture_only" {
					b.StartTimer()
					search()
					b.StopTimer()
				}
				var used, debit int64
				if err := s.db.QueryRow("SELECT accounted_bytes,index_bytes FROM run_meta").Scan(&used, &debit); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(used), "accounted_bytes")
				b.ReportMetric(float64(debit), "index_bytes")
				if info, err := os.Stat(s.path + "-wal"); err == nil {
					b.ReportMetric(float64(info.Size()), "wal_bytes")
				}
				if err := o.Lease.Close(); err != nil {
					b.Fatal(err)
				}
				if err := s.Close(); err != nil {
					b.Fatal(err)
				}
				b.StartTimer() // B.Loop requires a running timer at its next boundary.
			}
		})
	}
}
