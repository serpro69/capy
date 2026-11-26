package server

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
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
	vaultSidecarIDMaxBytes = 256
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

// renderedHit is one hit's full block and the identity a later hit must match
// to cite it instead of repeating it.
type renderedHit struct {
	block string
	key   passageKey
	// citable is false when the hit's location is unknown; such a hit carries
	// no key and is always shown in full.
	citable bool
}

// passageKey identifies a shown passage by its corpus, its rendered block and
// a digest of its complete identity fields: location, the windows selected
// from it and its attribution. Location alone is insufficient — another query
// can select a different window of the same chunk — and display text alone is
// insufficient — distinct rows can share titles or clipped prefixes, and a
// renamed session can keep its clipped preview. The digest keeps the retained
// key bounded however long those fields are.
type passageKey struct {
	corpus   string
	identity [sha256.Size]byte
	block    string
}

// passageIdentity digests identity fields, each length-prefixed, followed by
// the count and values of the selected window offsets. Offsets are encoded
// straight into the hash, so a passage selected from many windows needs no
// input-sized intermediate text.
func passageIdentity(spans []int, fields ...string) [sha256.Size]byte {
	h := sha256.New()
	var buf [binary.MaxVarintLen64]byte
	for _, f := range fields {
		h.Write(binary.AppendUvarint(buf[:0], uint64(len(f))))
		io.WriteString(h, f)
	}
	h.Write(binary.AppendUvarint(buf[:0], uint64(len(spans))))
	for _, s := range spans {
		h.Write(binary.AppendUvarint(buf[:0], uint64(s)))
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum
}

// replyWriter accumulates query groups for one keyword search reply within
// the space left after the request-wide notice reserve. It never commits a
// partial group frame or hit block.
//
// Full hit blocks are numbered [n] from one in emission order. A citable hit
// whose passage this reply already showed is written as a reference to that
// number instead.
type replyWriter struct {
	limit int
	b     strings.Builder
	// entries counts committed full blocks; refs maps each committed citable
	// passage to its number.
	entries int
	refs    map[passageKey]int
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

// addHit commits h as a reference to an earlier identical passage or as the
// next numbered full block. It reports false, writing and registering nothing,
// when the entry does not fit.
func (w *replyWriter) addHit(h renderedHit) bool {
	if h.citable {
		if n, ok := w.refs[h.key]; ok {
			return w.write(referenceBlock(n))
		}
	}
	n := w.entries + 1
	if !w.write(fmt.Sprintf("[%d] %s", n, h.block)) {
		return false
	}
	// Only a committed block can be cited.
	w.entries = n
	if h.citable {
		if w.refs == nil {
			w.refs = make(map[passageKey]int)
		}
		w.refs[h.key] = n
	}
	return true
}

// referenceBlock cites the full block numbered n.
func referenceBlock(n int) string {
	return fmt.Sprintf("↑ same passage as [%d]", n)
}

// write commits one whole entry, or reports false without writing when it does
// not fit.
func (w *replyWriter) write(entry string) bool {
	if len(hitSeparator)+len(entry) > w.remaining() {
		return false
	}
	w.b.WriteString(hitSeparator)
	w.b.WriteString(entry)
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

// partialNotice reports a capy_search batch stopped inside query ordinal (of
// total), which showed shown of the returned hits at the effective limit.
func partialNotice(ordinal, total, shown, returned, limit int) string {
	notice := partialGroupNotice(ordinal, total, shown, returned, limit)
	if returned > 1 {
		// A retry on a tapered call can return fewer ranks than this call did.
		notice += fmt.Sprintf(" If your next call's effective limit is below %d, retry after the reset time below to see the omitted ranks.", returned)
	}
	return notice
}

// partialGroupNotice reports a batch stopped inside query ordinal (of total),
// which showed shown of the returned hits at the effective limit. It is the
// whole notice for capy_vault_search, whose limit the shared search budget
// never tapers; partialNotice extends it for capy_search.
func partialGroupNotice(ordinal, total, shown, returned, limit int) string {
	head := fmt.Sprintf("Query %d showed %d of %d hit(s) returned at limit %d/query", ordinal, shown, returned, limit)
	return completionNotice(head, ordinal, total)
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
