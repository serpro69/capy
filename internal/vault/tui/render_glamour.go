//go:build glamour

package tui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/serpro69/capy/internal/vault"
)

// renderBody turns a message body into viewport display rows for a viewport of
// the given content width.
//
// This is the -tags glamour path: user/assistant bodies (which are markdown)
// are rendered through glamour for rich styling and syntax highlighting; every
// other role (tool output, system text) falls back to the lipgloss word-wrap,
// because tool results are frequently code/log payloads that markdown rendering
// would mangle. A zero/negative width (the unwrapped contract, exercised before
// the first WindowSizeMsg) and any glamour error also fall back to wrapBody, so
// the viewer never loses content to a styling failure — it just shows it raw.
func renderBody(role string, body string, st Styles, width int) []string {
	if width <= 0 || (role != vault.RoleUser && role != vault.RoleAssistant) {
		return wrapBody(body, st.Body, width)
	}
	out, err := glamourRender(body, width)
	if err != nil {
		return wrapBody(body, st.Body, width)
	}
	// glamour frames its output with a leading blank line (document margin) and a
	// trailing newline. Trim both ends: markdown rendering already normalizes away
	// any author-intended leading/trailing blank lines, so this strips only
	// glamour's own padding, keeping message spacing identical to the lipgloss
	// path (renderTranscript adds its own single blank separator between messages).
	out = strings.Trim(out, "\n")
	return strings.Split(out, "\n")
}

// glamourRenderer caches a single width-bound *glamour.TermRenderer.
// renderTranscript styles every message at one width per pass, so one renderer
// serves the whole pass; a terminal resize (a new width) rebuilds it. Building a
// TermRenderer compiles a chroma style and is far too costly to do per message.
//
// The cache is package-level and guarded by a mutex. In practice renderTranscript
// runs only on the single bubbletea goroutine, so contention is nil — the lock
// exists so the lazy initialization is data-race-free regardless of caller.
var (
	glamourMu       sync.Mutex
	glamourRenderer *glamour.TermRenderer
	glamourWidth    int
)

// glamourRender renders markdown to ANSI-styled text wrapped at width, reusing
// (or lazily building) the width-bound renderer.
func glamourRender(body string, width int) (string, error) {
	glamourMu.Lock()
	defer glamourMu.Unlock()
	if glamourRenderer == nil || glamourWidth != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return "", fmt.Errorf("build glamour renderer: %w", err)
		}
		glamourRenderer = r
		glamourWidth = width
	}
	out, err := glamourRenderer.Render(body)
	if err != nil {
		return "", fmt.Errorf("glamour render: %w", err)
	}
	return out, nil
}
