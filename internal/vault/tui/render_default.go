//go:build !glamour

package tui

// renderBody turns a message body into viewport display rows for a viewport of
// the given content width (width <= 0 disables wrapping).
//
// This is the default, lipgloss-only path: the raw body is word-wrapped with no
// markdown styling, keeping the binary lean (no glamour/chroma/goldmark
// dependency linked). The -tags glamour build replaces this with a glamour
// markdown renderer — see render_glamour.go. The role is unused here; the
// glamour path uses it to decide which messages are markdown.
func renderBody(_ string, body string, st Styles, width int) []string {
	return wrapBody(body, st.Body, width)
}
