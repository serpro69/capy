package vault

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OrClaude is the read-side twin of the store's write contract: only the zero
// value is normalized; every other value — a known constant or a corrupted
// string — passes through unchanged, so a bad stored value still reaches the
// strict dispatch (DecoderFor) and fails loudly there.
func TestPlatform_OrClaude(t *testing.T) {
	assert.Equal(t, PlatformClaudeCode, Platform("").OrClaude())
	assert.Equal(t, PlatformClaudeCode, PlatformClaudeCode.OrClaude())
	assert.Equal(t, PlatformCodex, PlatformCodex.OrClaude())
	assert.Equal(t, Platform("bogus"), Platform("bogus").OrClaude(), "an unrecognized value is never rewritten")

	// DecoderFor itself stays strict: "" is not Claude at the seam.
	_, err := DecoderFor("").Decode(bytes.NewReader(nil))
	require.ErrorIs(t, err, ErrUnknownPlatform)
}
