package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveSessionPlatform pins the contract of the one resolver a surface
// that ACTS on a row (restore) must use instead of Platform.OrClaude: the zero
// value is Claude with no sniff, a recognized token is itself, a corrupted
// token is sniffed from the blob with a warning, and an undetectable blob is an
// error — never a silent Claude default (the Task 14 review's P2: a corrupted
// Codex row restored under the Claude projects tree).
func TestResolveSessionPlatform(t *testing.T) {
	codexBlob := []byte(`{"timestamp":"2026-05-01T10:00:00Z","type":"session_meta","payload":{"id":"x","cwd":"/p"}}` + "\n")
	claudeBlob := []byte(`{"type":"last-prompt","uuid":"u"}` + "\n")

	t.Run("empty is Claude without sniffing", func(t *testing.T) {
		h := captureSlog(t)
		// A Codex-shaped blob must NOT flip an empty value: empty is the
		// documented zero-value convention, not a corrupted token.
		p, err := ResolveSessionPlatform("test", &Session{UUID: "u1", RawJSONL: codexBlob})
		require.NoError(t, err)
		assert.Equal(t, PlatformClaudeCode, p)
		assert.Empty(t, h.messagesWithPrefix("test:"), "no sniff, no warning")
	})

	t.Run("recognized tokens pass through", func(t *testing.T) {
		h := captureSlog(t)
		for _, want := range knownPlatforms {
			// The blob is deliberately the OTHER platform's shape: a recognized
			// token is trusted, never second-guessed against the bytes.
			blob := claudeBlob
			if want == PlatformClaudeCode {
				blob = codexBlob
			}
			p, err := ResolveSessionPlatform("test", &Session{UUID: "u2", Platform: want, RawJSONL: blob})
			require.NoError(t, err)
			assert.Equal(t, want, p)
		}
		assert.Empty(t, h.messagesWithPrefix("test:"))
	})

	t.Run("corrupted token is sniffed with one warning", func(t *testing.T) {
		h := captureSlog(t)
		p, err := ResolveSessionPlatform("test", &Session{UUID: "u3", Platform: "bogus", RawJSONL: codexBlob})
		require.NoError(t, err)
		assert.Equal(t, PlatformCodex, p, "a Codex blob under a corrupted token resolves to Codex, not Claude")
		warns := h.recordsWithMessage("test: unrecognized stored platform, using the format detected from the transcript")
		require.Len(t, warns, 1)
		attrs := recordAttrs(warns[0])
		assert.Equal(t, "u3", attrs["uuid"])
		assert.Equal(t, "bogus", attrs["platform"])
		assert.Equal(t, PlatformCodex, attrs["detected"])

		p, err = ResolveSessionPlatform("test", &Session{UUID: "u4", Platform: "bogus", RawJSONL: claudeBlob})
		require.NoError(t, err)
		assert.Equal(t, PlatformClaudeCode, p)
	})

	t.Run("corrupted token over an undetectable blob is an error", func(t *testing.T) {
		_, err := ResolveSessionPlatform("test", &Session{UUID: "u5", Platform: "bogus", RawJSONL: []byte("not json\n")})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrUndetectableFormat)
		assert.Contains(t, err.Error(), `"bogus"`, "the error names the corrupted value")
	})
}
