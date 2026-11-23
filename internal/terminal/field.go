package terminal

import (
	"strconv"
	"strings"
	"unicode"
)

// EscapeField renders untrusted metadata on a single terminal line.
func EscapeField(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
