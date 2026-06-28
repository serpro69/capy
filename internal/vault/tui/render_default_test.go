//go:build !glamour

package tui

import (
	"testing"

	"github.com/serpro69/capy/internal/vault"
	"github.com/stretchr/testify/assert"
)

// In the default build, renderBody must be byte-for-byte the lipgloss word-wrap
// (wrapBody) for every role — markdown is NOT rendered. This guards the design
// goal that the default build's behavior is unchanged by the glamour seam.
func TestRenderBody_DefaultIsLipglossOnly(t *testing.T) {
	st := DefaultStyles()
	md := "# Heading\n\nSome **bold** text\n\n- one\n- two\n"

	for _, role := range []string{vault.RoleUser, vault.RoleAssistant, vault.RoleTool, vault.RoleSystem} {
		t.Run(role, func(t *testing.T) {
			got := renderBody(role, md, st, 80)
			want := wrapBody(md, st.Body, 80)
			assert.Equal(t, want, got, "default renderBody must equal wrapBody (no markdown styling)")
		})
	}
}
