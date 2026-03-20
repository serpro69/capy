package store

import (
	"encoding/json"
	"strings"
)

// DetectContentType returns "json", "markdown", or "plaintext".
func DetectContentType(content string) string {
	trimmed := strings.TrimSpace(content)
	if json.Valid([]byte(trimmed)) && len(trimmed) > 0 &&
		(trimmed[0] == '{' || trimmed[0] == '[') {
		return "json"
	}
	if looksLikeMarkdown(trimmed) {
		return "markdown"
	}
	return "plaintext"
}

func looksLikeMarkdown(content string) bool {
	lines := strings.SplitN(content, "\n", 50)
	indicators := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			indicators++
		}
		if strings.HasPrefix(trimmed, "```") {
			indicators++
		}
		if strings.Contains(trimmed, "](") {
			indicators++
		}
		if indicators >= 2 {
			return true
		}
	}
	return false
}
