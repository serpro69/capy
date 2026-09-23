package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/serpro69/capy/internal/vault"
)

// Shared display helpers for the tui package (mirroring the CLI's cmd/capy
// formatting so the TUI and `capy vault list` read consistently).

// displaySessionProject shortens only fallback paths. A custom label remains
// literal even when it looks like, or equals, the imported path.
func displaySessionProject(s vault.Session) string {
	project := s.EffectiveProject()
	if s.ProjectOverride != nil && s.ProjectOverride.CustomProject != nil {
		return project
	}
	return displayPath(project)
}

// displaySearchProject uses the store's resolved value and label provenance.
func displaySearchProject(r vault.SearchResult) string {
	if r.CustomProject != nil {
		return r.Project
	}
	return displayPath(r.Project)
}

// fitRow bounds styled metadata to one terminal row, measured in display cells
// so wide Unicode labels cannot wrap. Width zero means size is not known yet.
func fitRow(s string, width int) string {
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(1).Render(s)
}

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
