package vault

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePlatform(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Platform
		wantErr bool
	}{
		{"claude-code", PlatformClaudeCode, false},
		{"codex", PlatformCodex, false},
		{"", "", true},
		{"Codex", "", true}, // case-sensitive: the stored form is canonical
		{"claude", "", true},
		{"codex ", "", true},
		{"bogus", "", true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParsePlatform(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrUnknownPlatform)
				assert.Contains(t, err.Error(), tc.in)
				assert.Equal(t, Platform(""), got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.in, got.String())
		})
	}
}

func TestPlatform_DisplayName(t *testing.T) {
	assert.Equal(t, "Claude", PlatformClaudeCode.DisplayName())
	assert.Equal(t, "Codex", PlatformCodex.DisplayName())
	assert.Equal(t, "bogus", Platform("bogus").DisplayName(), "an unrecognized value stays legible")
}

// Real first lines (redacted) from research.md Appendix A.1 and from the Claude
// first-line tally in design.md § Format Identification.
const (
	codexLegacyMeta    = `{"timestamp":"2026-05-13T19:11:47.906Z","type":"session_meta","payload":{"id":"019e22ba-4c15-7ae0-a903-255537a6a1b3","timestamp":"2026-05-13T19:04:55.061Z","cwd":"/home/user/Projects/proj","originator":"codex-tui","cli_version":"0.130.0","source":"cli","git":{"branch":"master"}}}`
	codexPaginatedMeta = `{"timestamp":"2026-09-02T12:43:08.280Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a06224-f7d9-71d3-b6bb-4ea07576fdb3","id":"01a06224-f7d9-71d3-b6bb-4ea07576fdb3","cwd":"/tmp/proj","source":"cli","history_mode":"paginated"}}`
	claudeSnapshot     = `{"type":"file-history-snapshot","messageId":"x","snapshot":{"messageId":"x","trackedFileBackups":{}},"isSnapshotUpdate":false}`
	claudeLastPrompt   = `{"type":"last-prompt","lastPrompt":"fix the bug","sessionId":"s"}`
	claudeUserLine     = `{"type":"user","uuid":"u1","timestamp":"2026-05-01T10:00:00Z","message":{"role":"user","content":"hi"}}`
)

func TestDetectFormat(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want Platform
	}{
		{"codex legacy session_meta", codexLegacyMeta, PlatformCodex},
		{"codex paginated session_meta", codexPaginatedMeta, PlatformCodex},
		{"codex whole blob, first line decides", codexLegacyMeta + "\n" + claudeUserLine + "\n", PlatformCodex},
		{"codex with CRLF", codexLegacyMeta + "\r\n", PlatformCodex},
		{"codex with leading whitespace", "  \t" + codexLegacyMeta, PlatformCodex},
		{"claude file-history-snapshot (no uuid/sessionId/message keys)", claudeSnapshot, PlatformClaudeCode},
		{"claude last-prompt", claudeLastPrompt, PlatformClaudeCode},
		{"claude user line", claudeUserLine, PlatformClaudeCode},
		{"claude whole blob", claudeSnapshot + "\n" + claudeUserLine + "\n", PlatformClaudeCode},
		{"empty object is claude", `{}`, PlatformClaudeCode},
		{"session_meta with non-object payload is not codex", `{"type":"session_meta","payload":"str"}`, PlatformClaudeCode},
		{"session_meta with array payload is not codex", `{"type":"session_meta","payload":[1]}`, PlatformClaudeCode},
		{"session_meta without payload is not codex", `{"type":"session_meta"}`, PlatformClaudeCode},
		{"session_meta with null payload is not codex", `{"type":"session_meta","payload":null}`, PlatformClaudeCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectFormat([]byte(tc.in))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDetectFormat_Undetectable(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace only", " \n\t"},
		{"newline first then object", "\n" + claudeUserLine},
		{"array", `[1,2]`},
		{"string", `"session_meta"`},
		{"null", `null`},
		{"number", `42`},
		{"garbage", `not json at all`},
		{"truncated object", `{"type":"session_meta","payload":{`},
		{"trailing garbage after object", `{"type":"user"} trailing`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DetectFormat([]byte(tc.in))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrUndetectableFormat)
			assert.Equal(t, Platform(""), got, "no silent default on an undetectable line")
		})
	}
}

// TestPlatform_ReaderVersionPairing enforces the "new platform constant ⇒
// reader-version bump" rule (design § Reader version, ADR-031): every Platform
// in knownPlatforms must be paired here with the min_reader_version a vault
// holding its rows requires, and every paired platform must be a known one.
// Adding a constant without a pairing fails this test; the pairing must name a
// readerVersion* constant from codec.go.
func TestPlatform_ReaderVersionPairing(t *testing.T) {
	// 0 = no requirement: a Claude row is readable by any vault reader (v1).
	required := map[Platform]int{
		PlatformClaudeCode: 0,
		PlatformCodex:      readerVersionPlatform,
	}
	for _, p := range knownPlatforms {
		v, ok := required[p]
		require.True(t, ok,
			"platform %q has no reader-version pairing — adding a platform is a reader-version bump: add a readerVersion* constant in codec.go and pair it here", p)
		if p != PlatformClaudeCode {
			assert.Greater(t, v, readerVersionZstd,
				"platform %q must require a reader version above the pre-platform milestone", p)
		}
	}
	for p := range required {
		_, err := ParsePlatform(string(p))
		require.NoError(t, err, "paired platform %q is not in knownPlatforms", p)
	}
	assert.Len(t, knownPlatforms, len(required))

	// The milestones themselves, pinned so a renumbering is deliberate.
	assert.Equal(t, 2, readerVersionZstd)
	assert.Equal(t, 3, readerVersionPlatform)
	// This binary knows every platform constant, so it supports the platform
	// milestone. A future milestone raises both together; never raise
	// supportedReaderVersion without a readerVersion* constant that names why.
	assert.Equal(t, readerVersionPlatform, supportedReaderVersion)
}

func TestDecoderFor(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		d := DecoderFor(PlatformClaudeCode)
		require.NotNil(t, d)
		assert.IsType(t, claudeDecoder{}, d)
		tr, err := d.Decode(strings.NewReader(claudeUserLine + "\n"))
		require.NoError(t, err)
		assert.Equal(t, PlatformClaudeCode, tr.Meta.Platform)
	})
	t.Run("codex is not implemented yet", func(t *testing.T) {
		d := DecoderFor(PlatformCodex)
		require.NotNil(t, d)
		tr, err := d.Decode(strings.NewReader(codexLegacyMeta + "\n"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDecoderUnavailable)
		assert.Contains(t, err.Error(), "codex")
		assert.Nil(t, tr)
	})
	t.Run("unknown value fails per session, never defaults to claude", func(t *testing.T) {
		d := DecoderFor(Platform("bogus"))
		require.NotNil(t, d)
		tr, err := d.Decode(strings.NewReader(claudeUserLine + "\n"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnknownPlatform))
		assert.Contains(t, err.Error(), `"bogus"`)
		assert.Nil(t, tr)
	})
}

// FuzzDetectFormat pins the contract: any input yields exactly one of (a known
// platform, nil) or ("", ErrUndetectableFormat) — never a panic, never a
// platform outside the closed set.
func FuzzDetectFormat(f *testing.F) {
	for _, seed := range []string{codexLegacyMeta, codexPaginatedMeta, claudeSnapshot, claudeLastPrompt, claudeUserLine,
		"", "{", "{}", "[]", "null", `{"type":"session_meta","payload":{}}`, "\n" + claudeUserLine} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		p, err := DetectFormat(line)
		if err != nil {
			assert.ErrorIs(t, err, ErrUndetectableFormat)
			assert.Equal(t, Platform(""), p)
			return
		}
		_, perr := ParsePlatform(string(p))
		assert.NoError(t, perr, "DetectFormat returned a platform outside the closed set")
	})
}
