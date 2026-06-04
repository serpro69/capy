// Package tui implements the interactive bubbletea interface for `capy vault`,
// activated by the --tui flag on the list/search/show commands. It is pure
// presentation: it composes a session list, a content viewer, and an FTS search
// over data read from internal/vault (VaultStore + ParseTranscript) and never
// opens or closes the store itself (the CLI owns that lifecycle).
package tui

import "github.com/charmbracelet/lipgloss"

// Styles holds every lipgloss style the TUI uses. It is constructed once
// (DefaultStyles) and passed by value to the sub-models; lipgloss styles are
// immutable value types, so sharing a Styles is safe and cheap.
//
// v1 is lipgloss-only by design — glamour (rich markdown / syntax highlighting)
// is excluded to keep the binary lean (see docs/wip/vault/design.md §Dependencies).
// Styling is limited to role coloring, a dimmed subagent marker, and panel chrome.
type Styles struct {
	// Role styles for the viewer headers and search-result rows.
	RoleUser      lipgloss.Style
	RoleAssistant lipgloss.Style
	RoleTool      lipgloss.Style
	RoleSystem    lipgloss.Style

	// Subagent launch markers: dimmed when visible-only, accented when openable.
	Marker         lipgloss.Style
	MarkerOpenable lipgloss.Style
	MarkerFocused  lipgloss.Style

	// Body text and structural chrome.
	Body     lipgloss.Style
	Title    lipgloss.Style
	StatusBar lipgloss.Style
	Help     lipgloss.Style
	ErrorMsg lipgloss.Style

	// Search-result rows.
	ResultSelected lipgloss.Style
	ResultMeta     lipgloss.Style
	Snippet        lipgloss.Style
}

// Palette colors. ANSI 16-color codes (lipgloss.Color("1".."15")) so the UI
// adapts to the user's terminal theme rather than hard-coding hex values.
const (
	colCyan    = lipgloss.Color("6")
	colGreen   = lipgloss.Color("2")
	colYellow  = lipgloss.Color("3")
	colMagenta = lipgloss.Color("5")
	colBlue    = lipgloss.Color("4")
	colGray    = lipgloss.Color("8")
	colWhite   = lipgloss.Color("7")
	colRed     = lipgloss.Color("1")
)

// DefaultStyles returns the standard style set.
func DefaultStyles() Styles {
	return Styles{
		RoleUser:      lipgloss.NewStyle().Foreground(colCyan).Bold(true),
		RoleAssistant: lipgloss.NewStyle().Foreground(colGreen).Bold(true),
		RoleTool:      lipgloss.NewStyle().Foreground(colYellow),
		RoleSystem:    lipgloss.NewStyle().Foreground(colMagenta),

		Marker:         lipgloss.NewStyle().Foreground(colGray).Faint(true),
		MarkerOpenable: lipgloss.NewStyle().Foreground(colBlue),
		MarkerFocused:  lipgloss.NewStyle().Foreground(colBlue).Bold(true).Reverse(true),

		Body:      lipgloss.NewStyle().Foreground(colWhite),
		Title:     lipgloss.NewStyle().Foreground(colWhite).Bold(true),
		StatusBar: lipgloss.NewStyle().Foreground(colGray),
		Help:      lipgloss.NewStyle().Foreground(colGray).Faint(true),
		ErrorMsg:  lipgloss.NewStyle().Foreground(colRed).Bold(true),

		ResultSelected: lipgloss.NewStyle().Foreground(colWhite).Bold(true).Reverse(true),
		ResultMeta:     lipgloss.NewStyle().Foreground(colGray),
		Snippet:        lipgloss.NewStyle().Foreground(colWhite),
	}
}

// roleStyle returns the style for a transcript/search role string.
func (s Styles) roleStyle(role string) lipgloss.Style {
	switch role {
	case "user":
		return s.RoleUser
	case "assistant":
		return s.RoleAssistant
	case "tool":
		return s.RoleTool
	default:
		return s.RoleSystem
	}
}
