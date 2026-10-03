package tui

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/serpro69/capy/internal/vault"
)

// CAPY_CODEX_VIEWER_BENCH_INPUT selects one frozen, uncompressed rollout for
// before/after comparisons. Only its digest and size are reported, never its
// path or content. Copy this harness unchanged into the baseline checkout.
func BenchmarkCodexViewerArchive(b *testing.B) {
	path := os.Getenv("CAPY_CODEX_VIEWER_BENCH_INPUT")
	if path == "" {
		b.Skip("CAPY_CODEX_VIEWER_BENCH_INPUT not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		// Preserve the I/O cause without disclosing a private rollout path.
		if pathErr, ok := err.(*os.PathError); ok {
			err = pathErr.Err
		}
		b.Fatalf("cannot read benchmark input: %v", err)
	}
	benchmarkCodexViewer(b, raw)
}

// Synthetic stress is separate from TestFindLatency's fixed acceptance corpus.
func BenchmarkCodexViewerLargeEdit(b *testing.B) {
	const lines = 16384
	hunk := fmt.Sprintf("@@ -1,%d +1,%d @@\n", lines, lines) +
		strings.Repeat("-old recorded line\n+new recorded line\n", lines)
	var raw []byte
	for _, record := range []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"cwd": "/fixture"}},
		{"type": "event_msg", "payload": map[string]any{
			"type": "user_message", "message": "Review recorded edits",
		}},
		{"type": "response_item", "payload": map[string]any{
			"type": "custom_tool_call", "name": "exec", "call_id": "call-1",
			"input": strings.Repeat("// original code input\n", lines),
		}},
		{"type": "event_msg", "payload": map[string]any{
			"type": "item_completed", "item": map[string]any{
				"type": "FileChange", "id": "edit-1", "status": "completed",
				"changes": map[string]any{
					"/fixture/file.txt": map[string]any{"type": "update", "unified_diff": hunk},
				},
			},
		}},
	} {
		line, err := json.Marshal(record)
		if err != nil {
			b.Fatal(err)
		}
		raw = append(raw, line...)
		raw = append(raw, '\n')
	}
	benchmarkCodexViewer(b, raw)
}

func benchmarkCodexViewer(b *testing.B, raw []byte) {
	b.Helper()
	b.Logf("input bytes=%d sha256=%x width=99", len(raw), sha256.Sum256(raw))
	messages := vault.ParseTranscript(vault.PlatformCodex, raw, nil)
	if len(messages) == 0 {
		b.Fatal("benchmark input has no viewer messages")
	}
	styles := DefaultStyles()
	b.Run("parse", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			vault.ParseTranscript(vault.PlatformCodex, raw, nil)
		}
	})
	b.Run("render", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			renderTranscript(vault.PlatformCodex, messages, styles, 99)
		}
	})
	b.Run("find-corpus", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := buildFindCorpus(b.Context(), "benchmark", messages); err != nil {
				b.Fatal(err)
			}
		}
	})
}
