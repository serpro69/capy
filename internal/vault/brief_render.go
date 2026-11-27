package vault

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/serpro69/capy/internal/sanitize"
)

// BriefMaxBytes is the complete rendered brief's ceiling in valid UTF-8 bytes,
// including citations, labels, omission marks and the final newline.
const BriefMaxBytes = 4096

// Finished-byte allowances for metadata fields, including quotes and marks.
const (
	briefIDMax      = 128
	briefTitleMax   = 256
	briefProjectMax = 512
	briefOpeningMax = 512
)

const (
	briefClipMark    = "…"
	briefAbsentField = "(none)"
)

var briefOmittedField = fmt.Sprintf("[omitted: larger than %d bytes]", briefFieldReadLimit)

// RenderBrief renders b as UTF-8 text of at most BriefMaxBytes bytes ending in
// one newline. Every untrusted value is a single quoted, escaped field, so
// transcript content cannot start another line or emit terminal controls.
// Secrets are redacted on the complete value before escaping and clipping.
// CLI stdout and the MCP text item both use this rendering.
func RenderBrief(b *Brief) string {
	var head strings.Builder

	id, intact := finishField(b.ID, briefIDMax)
	if intact {
		fmt.Fprintf(&head, "Session %s · %s\n", id, b.Platform)
	} else {
		fmt.Fprintf(&head, "Session ID preview %s · %s\n", id, b.Platform)
	}
	project, _ := finishMetadata(b.Project, briefProjectMax)
	title, _ := finishMetadata(b.Title, briefTitleMax)
	fmt.Fprintf(&head, "Project %s · Title %s\n", project, title)

	var tail strings.Builder
	switch {
	case b.Opening == nil:
		tail.WriteString("No indexed user entry in this session.\n")
	case b.Assistant == nil:
		tail.WriteString("No indexed assistant entry in this turn.\n")
	}
	if b.StaleIndex() {
		fmt.Fprintf(&tail, "Index version %d predates %d; run \"capy vault reindex\" to refresh this session's rows.\n",
			b.IndexVersion, currentIndexVersion)
	}

	if b.Opening == nil {
		return head.String() + tail.String()
	}

	// Entry lines are a fixed label plus one finished field and a newline. The
	// excerpt allowance is what remains after every fixed byte is reserved.
	type line struct {
		label string
		entry *BriefEntry
		text  string
	}
	var opening, latest, assistant *line
	if b.LatestIsOpening {
		latest = &line{label: entryLabel("Opening and latest", b.Latest), entry: b.Latest}
	} else {
		opening = &line{label: entryLabel("Opening", b.Opening), entry: b.Opening}
		latest = &line{label: entryLabel("Latest", b.Latest), entry: b.Latest}
	}
	if b.Assistant != nil {
		assistant = &line{label: entryLabel("Latest", b.Assistant), entry: b.Assistant}
	}

	allowance := BriefMaxBytes - head.Len() - tail.Len()
	for _, l := range []*line{opening, latest, assistant} {
		if l != nil {
			allowance -= len(l.label) + len("\n")
		}
	}

	// The latest exchange has priority: a distinct opening entry gets at most a
	// fifth of the allowance, capped, and only what it uses is spent.
	if opening != nil {
		opening.text, _ = finishField(opening.entry.Text, min(allowance/5, briefOpeningMax))
		allowance -= len(opening.text)
	}
	if assistant == nil {
		latest.text, _ = finishField(latest.entry.Text, allowance)
	} else {
		half := allowance / 2
		userNeed := naturalSize(latest.entry.Text)
		assistantNeed := naturalSize(assistant.entry.Text)
		userBudget, assistantBudget := half, allowance-half
		switch {
		case userNeed <= half:
			userBudget, assistantBudget = userNeed, allowance-userNeed
		case assistantNeed <= allowance-half:
			userBudget, assistantBudget = allowance-assistantNeed, assistantNeed
		}
		latest.text, _ = finishField(latest.entry.Text, userBudget)
		assistant.text, _ = finishField(assistant.entry.Text, assistantBudget)
	}

	var out strings.Builder
	out.Grow(BriefMaxBytes)
	out.WriteString(head.String())
	for _, l := range []*line{opening, latest, assistant} {
		if l != nil {
			out.WriteString(l.label)
			out.WriteString(l.text)
			out.WriteString("\n")
		}
	}
	out.WriteString(tail.String())
	return out.String()
}

func entryLabel(position string, e *BriefEntry) string {
	return fmt.Sprintf("%s %s · message starts at line %d: ", position, e.Role, e.Line)
}

// finishMetadata renders an imported title or project, using a fixed
// placeholder when it is absent or empty.
func finishMetadata(f BriefField, budget int) (string, bool) {
	if !f.Omitted && f.Value == "" {
		return briefAbsentField, false
	}
	return finishField(f, budget)
}

// briefIntactID returns the stored session ID when it is displayable without
// redaction, escaping or clipping within the ID allowance — the only form a
// diagnostic or navigation hint may offer as a usable selector.
func briefIntactID(f BriefField) (string, bool) {
	if _, intact := finishField(f, briefIDMax); !intact {
		return "", false
	}
	return f.Value, true
}

// finishField renders f as one quoted field within budget bytes. Invalid UTF-8
// is replaced and secrets are redacted on the complete value, then the value
// is escaped and clipped on whole rune/escape boundaries, with the clip mark
// after the closing quote so quotes stay balanced. intact reports that the
// displayed field is exactly the stored value. Callers size budgets well above
// the quotes and clip mark; RenderBrief's fixed reservations guarantee that.
func finishField(f BriefField, budget int) (out string, intact bool) {
	if f.Omitted {
		return briefOmittedField, false
	}
	if !f.Present {
		return briefAbsentField, false
	}
	value := sanitize.StripSecrets(strings.ToValidUTF8(f.Value, "\uFFFD"))
	intact = value == f.Value

	units := make([]string, 0, len(value))
	total := len(`""`)
	for _, r := range value {
		unit := escapeRune(r)
		if unit != string(r) {
			intact = false
		}
		units = append(units, unit)
		total += len(unit)
	}

	var b strings.Builder
	b.WriteByte('"')
	if total <= budget {
		for _, unit := range units {
			b.WriteString(unit)
		}
		b.WriteByte('"')
		return b.String(), intact
	}
	room := budget - len(`""`) - len(briefClipMark)
	for _, unit := range units {
		if b.Len()-1+len(unit) > room {
			break
		}
		b.WriteString(unit)
	}
	b.WriteByte('"')
	b.WriteString(briefClipMark)
	return b.String(), false
}

// naturalSize is the finished size of f without clipping.
func naturalSize(f BriefField) int {
	out, _ := finishField(f, math.MaxInt)
	return len(out)
}

// escapeRune renders one rune for a quoted single-line field.
func escapeRune(r rune) string {
	switch {
	case r == '"':
		return `\"`
	case r == '\\':
		return `\\`
	case unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp):
		quoted := strconv.QuoteRune(r)
		return quoted[1 : len(quoted)-1]
	default:
		return string(r)
	}
}
