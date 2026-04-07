package sanitize

import (
	"regexp"
	"strings"
)

// Replacement placeholders.
const (
	RedactedSecret  = "[REDACTED_SECRET]"
	RedactedPrivate = "[REDACTED]"
)

// privateTagRe matches <private>...</private> blocks (case-insensitive, dotall).
var privateTagRe = regexp.MustCompile(`(?is)<private>.*?</private>`)

// secretPatterns lists compiled regexes for known secret formats.
// Order matters: more specific patterns (e.g. Anthropic keys) must precede
// generic ones (e.g. generic prefixed tokens) to avoid partial matches.
var secretPatterns = []*regexp.Regexp{
	// Anthropic keys: sk-ant-...
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
	// GitHub fine-grained PATs: github_pat_...
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),
	// GitHub PATs: ghp_...
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
	// Slack tokens: xoxb-... (real tokens are 50+ chars; floor at 10 to avoid false positives)
	regexp.MustCompile(`xoxb-[A-Za-z0-9-]{10,}`),
	// AWS access keys: AKIA...
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// Google API keys: AIza...
	regexp.MustCompile(`AIza[A-Za-z0-9_-]{35}`),
	// npm tokens: npm_...
	regexp.MustCompile(`npm_[A-Za-z0-9]{36}`),
	// GitLab tokens: glpat-...
	regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`),
	// DigitalOcean tokens: dop_v1_...
	regexp.MustCompile(`dop_v1_[A-Za-z0-9]{64}`),
	// JWTs: three base64url segments
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	// Generic prefixed tokens: sk-, pk-, rk-, ak-
	regexp.MustCompile(`(?:sk|pk|rk|ak)-[A-Za-z0-9]{20,}`),
	// NOTE: This pattern consumes the key name and operator (e.g. "api_key=" becomes
	// "[REDACTED_SECRET]"). Acceptable for FTS5 content; revisit with capture groups
	// if structured-format preservation becomes necessary.
	// Generic key=value secrets (case-insensitive key names)
	regexp.MustCompile(`(?i)(?:api[_-]?key|secret|token|password|credential|auth[_-]?(?:token|key|secret|credential))\s*[=:]\s*["']?[A-Za-z0-9_\-/.+]{20,}["']?`),
}

// StripSecrets replaces detected secret patterns and private tags in content
// with redaction placeholders. Returns the sanitized string.
func StripSecrets(content string) string {
	// Fast path: empty content.
	if content == "" {
		return content
	}

	// Strip private tags first (only if the marker is present).
	if strings.Contains(strings.ToLower(content), "<private>") {
		content = privateTagRe.ReplaceAllString(content, RedactedPrivate)
	}

	// NOTE: Each pattern runs a separate scan over the content. For the expected
	// content sizes (command output, docs) this is fine. If profiling shows this
	// as a hot path on large documents, consider combining into a single OR'd regex.
	for _, pat := range secretPatterns {
		content = pat.ReplaceAllString(content, RedactedSecret)
	}

	return content
}
