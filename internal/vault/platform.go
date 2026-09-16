package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Platform identifies the agent CLI that produced an archived session. It is
// stored verbatim in vault_sessions.platform (migration 0006) and selects the
// Decoder that turns the session's raw bytes into a Transcript (DecoderFor).
//
// The set is closed and validated in Go (ParsePlatform) rather than by a SQL
// CHECK, so a third platform is a new constant, not a migration. Adding a
// constant is a reader-version bump — see codec.go readerVersion* and
// TestPlatform_ReaderVersionPairing.
type Platform string

const (
	// PlatformClaudeCode is a Claude Code session: ~/.claude/projects/<mangled
	// dir>/<uuid>.jsonl with optional subagent sidecars.
	PlatformClaudeCode Platform = "claude-code"
	// PlatformCodex is a Codex CLI rollout: $CODEX_HOME/{sessions,
	// archived_sessions}/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl[.zst].
	PlatformCodex Platform = "codex"
)

// knownPlatforms is the closed set ParsePlatform accepts. Every constant above
// MUST be listed here, and every entry MUST be paired with a reader version in
// TestPlatform_ReaderVersionPairing (the "new platform ⇒ reader bump" rule).
var knownPlatforms = []Platform{PlatformClaudeCode, PlatformCodex}

// ErrUnknownPlatform is returned by ParsePlatform for a value outside the closed
// set, and by the Decoder DecoderFor returns for such a value. Because adding a
// platform constant bumps min_reader_version, a stored value this binary does not
// know can only be a corrupted or hand-edited row — never a legitimately newer
// vault (checkReaderVersion refuses that at open).
var ErrUnknownPlatform = errors.New("unknown vault platform")

// ErrUndetectableFormat is returned by DetectFormat when the first line is not a
// JSON object (or the blob is empty), so no platform can be inferred.
var ErrUndetectableFormat = errors.New("cannot detect session format: first line is not a JSON object")

// ParsePlatform validates s against the closed platform set.
func ParsePlatform(s string) (Platform, error) {
	p := Platform(s)
	for _, k := range knownPlatforms {
		if p == k {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownPlatform, s)
}

// String returns the stored/wire form ("claude-code", "codex").
func (p Platform) String() string { return string(p) }

// OrClaude returns p, or PlatformClaudeCode for the zero value. It is the read
// side of the convention the store's write contract already applies
// (writeRecord normalizes an empty Session.Platform to Claude): a Session or
// SessionFile built in memory without the field — tests, callers that predate
// it — is a Claude session. A STORED value is never empty (the column is NOT
// NULL DEFAULT 'claude-code'), so this never masks a corrupted row; DecoderFor
// itself stays strict and fails on "" like any other unknown value.
func (p Platform) OrClaude() Platform {
	if p == "" {
		return PlatformClaudeCode
	}
	return p
}

// DisplayName returns the human label used for the assistant heading in `vault
// show` and the TUI ("Claude", "Codex"). An unrecognized value falls back to the
// raw string so a corrupted row is still legible rather than blank.
func (p Platform) DisplayName() string {
	switch p {
	case PlatformClaudeCode:
		return "Claude"
	case PlatformCodex:
		return "Codex"
	}
	return string(p)
}

// codexSessionMetaType is the envelope `type` of the first line of every Codex
// rollout (168/168 local files; research.md § 3.2).
const codexSessionMetaType = "session_meta"

// DetectFormat infers the platform of a session blob from its first line. It is
// Codex-positive only:
//
//   - PlatformCodex when the line is a JSON object whose top-level "type" is
//     "session_meta" and whose "payload" is a JSON object;
//   - PlatformClaudeCode for ANY other JSON object;
//   - ErrUndetectableFormat when the line is not a JSON object (or firstLine is
//     empty). Callers record the session as an error rather than defaulting.
//
// There is deliberately no Claude-shaped key test: 41 of 467 real Claude
// sessions open with a `file-history-snapshot` line that carries none of uuid /
// sessionId / message, so a positive Claude rule would reject 8.8% of real
// sessions. The Claude decoder already skips any leading line it does not know.
//
// Precondition that makes "Claude for anything else" sound: every platform
// constant is a min_reader_version bump (codec.go), so a vault this binary opens
// holds only platforms it knows. DetectFormat is therefore consulted in exactly
// one situation — a vault_sessions.platform value that is PRESENT but
// UNRECOGNIZED, i.e. a corrupted or hand-edited column on a Claude or Codex blob
// — and the Codex-positive test resolves it exactly. An ABSENT column (a
// pre-0006 vault or merge source) is Claude by construction and is never
// sniffed. Import never calls this: the discoverer knows the platform from the
// root it walked.
//
// firstLine may be the whole blob; only the bytes before the first newline are
// inspected, so callers need not split it themselves.
func DetectFormat(firstLine []byte) (Platform, error) {
	if i := bytes.IndexByte(firstLine, '\n'); i >= 0 {
		firstLine = firstLine[:i]
	}
	line := bytes.TrimSpace(firstLine)
	if len(line) == 0 || line[0] != '{' {
		return "", ErrUndetectableFormat
	}
	var probe struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUndetectableFormat, err)
	}
	if probe.Type == codexSessionMetaType && isJSONObject(probe.Payload) {
		return PlatformCodex, nil
	}
	return PlatformClaudeCode, nil
}

// isJSONObject reports whether raw is a (non-empty) JSON object literal.
func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}
