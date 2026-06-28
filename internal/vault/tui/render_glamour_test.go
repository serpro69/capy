//go:build glamour

package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleMarkdown = "# Heading\n\nSome **bold** text and a list:\n\n- one\n- two\n"

// currentGlamourRenderer reads the cached renderer under glamourMu, matching the
// production access discipline so the race detector stays clean even if these
// tests are ever parallelized.
func currentGlamourRenderer() *glamour.TermRenderer {
	glamourMu.Lock()
	defer glamourMu.Unlock()
	return glamourRenderer
}

// resetGlamourCache clears the cached renderer under the lock.
func resetGlamourCache() {
	glamourMu.Lock()
	defer glamourMu.Unlock()
	glamourRenderer = nil
	glamourWidth = 0
}

// In the glamour build, user/assistant markdown is rendered through glamour:
// the result differs from the raw lipgloss wrap (proving glamour transformed
// it) while the textual content survives.
func TestRenderBody_GlamourStylesMarkdown(t *testing.T) {
	st := DefaultStyles()

	for _, role := range []string{vault.RoleUser, vault.RoleAssistant} {
		t.Run(role, func(t *testing.T) {
			got := renderBody(role, sampleMarkdown, st, 80)
			raw := wrapBody(sampleMarkdown, st.Body, 80)

			assert.NotEqual(t, strings.Join(raw, "\n"), strings.Join(got, "\n"),
				"glamour output should differ from the plain lipgloss wrap")

			joined := strings.Join(got, "\n")
			for _, word := range []string{"Heading", "bold", "one", "two"} {
				assert.Contains(t, joined, word, "glamour output should preserve content %q", word)
			}
		})
	}
}

// Non-markdown roles (tool output, system text) are left raw-wrapped even in the
// glamour build, so code/log payloads aren't mangled by markdown rendering.
func TestRenderBody_GlamourFallsBackForNonMarkdownRoles(t *testing.T) {
	st := DefaultStyles()
	for _, role := range []string{vault.RoleTool, vault.RoleSystem} {
		t.Run(role, func(t *testing.T) {
			got := renderBody(role, sampleMarkdown, st, 80)
			want := wrapBody(sampleMarkdown, st.Body, 80)
			assert.Equal(t, want, got, "should fall back to wrapBody")
		})
	}
}

// A zero/negative width (the unwrapped contract, exercised before the first
// WindowSizeMsg) falls back to wrapBody rather than driving glamour with an
// invalid wrap width.
func TestRenderBody_GlamourFallsBackOnZeroWidth(t *testing.T) {
	st := DefaultStyles()
	got := renderBody(vault.RoleAssistant, sampleMarkdown, st, 0)
	want := wrapBody(sampleMarkdown, st.Body, 0)
	assert.Equal(t, want, got)
}

// Observable behavior: the wrap width is honored, so a long paragraph wraps to
// more rows at a narrow width than at a wide one. This also exercises the
// width-keyed renderer rebuild (a stale cache would reuse the wrong width).
func TestRenderBody_GlamourHonorsWidth(t *testing.T) {
	st := DefaultStyles()
	long := "This is a single long paragraph of plain prose that should wrap across " +
		"several lines once the available rendering width gets small enough to force it."

	wide := renderBody(vault.RoleAssistant, long, st, 100)
	narrow := renderBody(vault.RoleAssistant, long, st, 20)

	assert.Greater(t, len(narrow), len(wide),
		"a narrower width should wrap the paragraph into more rows")
}

// The renderer is cached per width: a same-width call reuses the renderer; a
// new width rebuilds it. Building a TermRenderer per message would be far too
// costly during a re-wrap pass over a full transcript. This is a guard for that
// optimization (white-box, hence same-package).
func TestGlamourRender_CachesByWidth(t *testing.T) {
	resetGlamourCache()

	_, err := glamourRender("hello", 80)
	require.NoError(t, err)
	first := currentGlamourRenderer()
	require.NotNil(t, first)

	_, err = glamourRender("world", 80)
	require.NoError(t, err)
	assert.Same(t, first, currentGlamourRenderer(), "same width should reuse the cached renderer")

	_, err = glamourRender("again", 40)
	require.NoError(t, err)
	assert.NotSame(t, first, currentGlamourRenderer(), "a new width should rebuild the renderer")
}
