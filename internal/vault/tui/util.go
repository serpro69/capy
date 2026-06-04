package tui

import (
	"fmt"
	"os"
	"strings"
)

// Shared display helpers for the tui package (mirroring the CLI's cmd/capy
// formatting so the TUI and `capy vault list` read consistently).

// displayPath shortens a home-relative absolute path to ~/… for compact display.
func displayPath(p string) string {
	if p == "" {
		return "-"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// fmtSize renders a byte count in B/KB/MB/GB with one decimal place above KB.
func fmtSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// truncate shortens s to max runes, appending an ellipsis when cut.
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// oneLine collapses internal whitespace (FTS snippets may contain newlines) for
// single-row display.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
