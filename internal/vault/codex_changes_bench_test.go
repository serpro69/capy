package vault

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Keep valid and early-rejected inputs together: eager builder allocation can
// improve the former while wasting memory on the latter.
func BenchmarkCodexUpdateDiff(b *testing.B) {
	for _, lines := range []int{32, 16384} {
		valid := fmt.Sprintf("@@ -1,%d +1,%d @@\n", lines, lines) +
			strings.Repeat("-old recorded line\n+new recorded line\n", lines)
		for _, malformed := range []bool{false, true} {
			text := valid
			if malformed {
				text = "invalid\n" + valid
			}
			b.Run(fmt.Sprintf("lines=%d/malformed=%v", lines, malformed), func(b *testing.B) {
				diff, reason := codexUpdateDiff(text)
				if malformed {
					if diff != nil || reason == "" {
						b.Fatal("expected malformed fixture rejection")
					}
				} else if diff == nil || reason != "" || diff.Added != lines || diff.Removed != lines {
					b.Fatal("unexpected fixture conversion")
				}
				b.ReportAllocs()
				for b.Loop() {
					codexUpdateDiff(text)
				}
			})
		}
	}
}

func BenchmarkCodexChangeOutput(b *testing.B) {
	for _, size := range []int{64, 1 << 20} {
		body := strings.Repeat("x", size)
		raw, err := json.Marshal(body)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			var diagnostics []FileChangeDiagnostic
			if codexChangeOutput(raw, "stdout", &diagnostics) != body || len(diagnostics) != 0 {
				b.Fatal("unexpected fixture output")
			}
			b.ReportAllocs()
			for b.Loop() {
				var loopDiagnostics []FileChangeDiagnostic
				codexChangeOutput(raw, "stdout", &loopDiagnostics)
			}
		})
	}
}
