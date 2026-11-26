package server

import (
	"fmt"
	"strings"

	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/terminal"
)

// Keyword search reply budget (docs/feat/wip/compact-search-replies,
// implementation "Bound the complete reply"). Every limit applies to finished
// display text in UTF-8 bytes, including markers, line breaks and separators.
const (
	// searchReplyMaxBytes bounds the final MCP text item.
	searchReplyMaxBytes = 40 * 1024
	// searchNoticeReserve is set aside before any query group for the
	// request-wide notices: availability, miss guidance, the completion notice
	// and the admission footer.
	searchNoticeReserve = 2048
	// searchFrameMaxBytes bounds one query's separator, heading and status. The
	// writer requires this much room before it dispatches a query.
	searchFrameMaxBytes = 1024
	// searchHitMaxBytes bounds one hit block and its separator.
	searchHitMaxBytes = 3072

	searchPassageMaxBytes = 1500
	searchQueryPreviewMax = 256
	searchCauseMaxBytes   = 192

	knowledgeLabelMaxBytes = 512
	knowledgeTitleMaxBytes = 256
	vaultSessionIDMaxBytes = 128
	vaultTitleMaxBytes     = 256
	vaultProjectMaxBytes   = 512
	// vaultMetaFieldMaxBytes bounds the stored platform token and the short
	// parent ID, which are short in practice but read from storage.
	vaultMetaFieldMaxBytes = 64

	// searchArgPreviewMaxBytes bounds a caller value quoted in a validation error.
	searchArgPreviewMaxBytes = 128

	omissionMarker  = "…"
	groupSeparator  = "\n\n---\n\n"
	hitSeparator    = "\n\n"
	noticeSeparator = "\n\n"
)

// displayField prepares one metadata field or query echo: known secrets are
// redacted on the complete value, then control, format and line/paragraph
// separator runes (and invalid UTF-8, as U+FFFD) are escaped so the field stays
// on one line, and the escaped text is clipped on a rune boundary with the
// omission marker inside maxBytes.
func displayField(s string, maxBytes int) string {
	return clipBytes(terminal.EscapeField(sanitize.StripSecrets(s)), maxBytes, omissionMarker)
}

// displayPassage bounds an already-selected passage, keeping its line breaks
// and indentation.
func displayPassage(s string) string {
	return clipBytes(strings.ToValidUTF8(s, "\uFFFD"), searchPassageMaxBytes, omissionMarker)
}

// argPreview quotes a caller-supplied value for a validation error: the full
// value is redacted before it is quoted, and the quoted form is bounded.
func argPreview(s string) string {
	quoted := fmt.Sprintf("%q", strings.ToValidUTF8(sanitize.StripSecrets(s), "\uFFFD"))
	return clipBytes(quoted, searchArgPreviewMaxBytes, omissionMarker)
}

// replyWriter accumulates query groups for one keyword search reply within
// the space left after the request-wide notice reserve. It never commits a
// partial group frame or hit block.
type replyWriter struct {
	limit int
	b     strings.Builder
}

func newReplyWriter(total int) *replyWriter {
	return &replyWriter{limit: total - searchNoticeReserve}
}

func (w *replyWriter) remaining() int { return w.limit - w.b.Len() }

// canStartGroup reports whether a whole query frame of maximum size still
// fits. The check precedes dispatch, so a query is never searched without room
// to report its outcome.
func (w *replyWriter) canStartGroup() bool {
	return w.remaining() >= searchFrameMaxBytes
}

// startGroup commits a frame produced by queryFrame (which bounds it within
// searchFrameMaxBytes, separator included). Only actual bytes are charged.
func (w *replyWriter) startGroup(frame string) {
	if w.b.Len() > 0 {
		w.b.WriteString(groupSeparator)
	}
	w.b.WriteString(frame)
}

// addHit commits one whole hit block, or reports false without writing when it
// does not fit.
func (w *replyWriter) addHit(block string) bool {
	if len(hitSeparator)+len(block) > w.remaining() {
		return false
	}
	w.b.WriteString(hitSeparator)
	w.b.WriteString(block)
	return true
}

func (w *replyWriter) String() string { return w.b.String() }

// queryFrame renders one group heading and status. ordinal counts from one in
// the caller's input order. causes are the per-corpus failures; status is
// "Error:" when no hit was returned, a partial-results warning otherwise, and
// "No results found." for a clean miss.
func queryFrame(ordinal int, query string, causes []string, hits int) string {
	heading := fmt.Sprintf("## %d. %s", ordinal, displayField(query, searchQueryPreviewMax))
	if len(causes) > 2 {
		causes = causes[:2]
	}
	bounded := make([]string, len(causes))
	for i, c := range causes {
		bounded[i] = displayField(c, searchCauseMaxBytes)
	}
	cause := strings.Join(bounded, " | ")
	switch {
	case len(causes) > 0 && hits == 0:
		return heading + "\nError: " + cause
	case len(causes) > 0:
		return heading + "\n\n⚠ partial results (" + cause + ")"
	case hits == 0:
		return heading + "\nNo results found."
	default:
		return heading
	}
}

// partialNotice reports a batch stopped inside query ordinal (of total), which
// showed shown of the returned hits at the effective limit.
func partialNotice(ordinal, total, shown, returned, limit int) string {
	head := fmt.Sprintf("Query %d showed %d of %d hit(s) returned at limit %d/query", ordinal, shown, returned, limit)
	notice := completionNotice(head, ordinal, total)
	if returned > 1 {
		// A retry on a tapered call can return fewer ranks than this call did.
		notice += fmt.Sprintf(" If your next call's effective limit is below %d, retry after the reset time below to see the omitted ranks.", returned)
	}
	return notice
}

// unsearchedNotice reports a batch stopped before query ordinal (of total) was
// searched.
func unsearchedNotice(ordinal, total int) string {
	return completionNotice(fmt.Sprintf("Query %d was not searched", ordinal), ordinal, total)
}

// completionNotice frames the boundary query's outcome with the count of
// following queries never run and the inclusive retry range.
func completionNotice(boundary string, ordinal, total int) string {
	var b strings.Builder
	b.WriteString("⚠ Reply budget reached. ")
	b.WriteString(boundary)
	following := total - ordinal
	switch following {
	case 0:
		b.WriteString(".")
	case 1:
		b.WriteString("; 1 following query was not searched.")
	default:
		fmt.Fprintf(&b, "; %d following queries were not searched.", following)
	}
	if following == 0 {
		fmt.Fprintf(&b, " To continue, retry query %d", ordinal)
	} else {
		fmt.Fprintf(&b, " To continue, retry queries %d–%d", ordinal, total)
	}
	b.WriteString(" (inclusive) with the same selectors and requested limit; numbering restarts at 1 and the retry searches current data from the first rank.")
	return b.String()
}
