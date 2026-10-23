//go:build linux && integration

package execution

import (
	"testing"
	"time"

	"github.com/serpro69/capy/internal/config"
	"github.com/serpro69/capy/internal/executor"
)

// Warm owner/storage; measure one real helper, capture and completion per run.
// This fixture also compiles at the immediate protocol-v1 baseline.
func BenchmarkStoredStartup(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("CAPY_STORED_FIXTURE", dir)
	b.Setenv("CAPY_STORED_MODE", "")
	s := NewService(dir, storedFixtureKey, config.DefaultConfig().Runs)
	defer s.Close()
	request := executor.ExecRequest{Language: executor.Shell, Code: "true"}
	if r := s.Execute(b.Context(), request, "", 30*time.Second); r.State != "exited" || r.Capture.State != "complete" {
		b.Fatalf("warm-up failed: %+v", r)
	}
	b.ReportAllocs()
	for b.Loop() {
		r := s.Execute(b.Context(), request, "", 30*time.Second)
		if r.State != "exited" || r.Capture.State != "complete" || !r.Capture.TerminalPersisted {
			b.Fatalf("captured execution failed: %+v", r)
		}
	}
}
