package executor

import (
	"fmt"
	"strings"
)

const (
	MaxOutputBytes = 102400             // 100 KB
	HardCapBytes   = 100 * 1024 * 1024 // 100 MB
	headRatio      = 0.6
)

// SmartTruncate truncates output to maxBytes with a 60/40 head/tail split.
// Snaps to line boundaries to avoid UTF-8 corruption.
func SmartTruncate(output string, maxBytes int) string {
	if len(output) <= maxBytes {
		return output
	}

	lines := strings.Split(output, "\n")
	headBudget := int(float64(maxBytes) * headRatio)
	tailBudget := maxBytes - headBudget

	// Collect head lines.
	var headLines []string
	headBytes := 0
	for _, line := range lines {
		needed := len(line) + 1 // +1 for newline
		if headBytes+needed > headBudget && len(headLines) > 0 {
			break
		}
		headLines = append(headLines, line)
		headBytes += needed
	}

	// Collect tail lines (from end).
	var tailLines []string
	tailBytes := 0
	for i := len(lines) - 1; i >= len(headLines); i-- {
		needed := len(lines[i]) + 1
		if tailBytes+needed > tailBudget && len(tailLines) > 0 {
			break
		}
		tailLines = append(tailLines, lines[i])
		tailBytes += needed
	}

	// Reverse tail lines.
	for i, j := 0, len(tailLines)-1; i < j; i, j = i+1, j-1 {
		tailLines[i], tailLines[j] = tailLines[j], tailLines[i]
	}

	skippedLines := len(lines) - len(headLines) - len(tailLines)
	skippedBytes := len(output) - headBytes - tailBytes
	separator := fmt.Sprintf("\n\n... [%d lines / %.1fKB truncated — showing first %d + last %d lines] ...\n\n",
		skippedLines, float64(skippedBytes)/1024.0, len(headLines), len(tailLines))

	return strings.Join(headLines, "\n") + separator + strings.Join(tailLines, "\n")
}
