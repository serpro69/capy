package server

import (
	"fmt"
	"strings"
)

const intentSearchThreshold = 5000 // bytes — trigger intent search above this

// intentSearch indexes output and searches for intent matches.
// Returns a formatted summary with section titles and previews.
func (s *Server) intentSearch(output, intent, source string, maxResults int) string {
	st := s.getStore()

	indexed, err := st.IndexPlainText(output, source)
	if err != nil {
		// Don't leak full output into context — truncate with error note
		preview := output
		if len(preview) > 3000 {
			preview = preview[:3000]
		}
		return fmt.Sprintf("%s\n\n(indexing failed: %v — showing truncated output)", preview, err)
	}

	results, err := st.SearchWithFallback(intent, maxResults, source)
	if err != nil {
		results = nil
	}

	terms, _ := st.GetDistinctiveTerms(indexed.SourceID, 40)

	totalLines := strings.Count(output, "\n") + 1
	totalKB := fmt.Sprintf("%.1f", float64(len(output))/1024)

	if len(results) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "Indexed %d sections from %q into knowledge base.\n", indexed.TotalChunks, source)
		fmt.Fprintf(&b, "No sections matched intent %q in %d-line output (%sKB).\n", intent, totalLines, totalKB)
		if len(terms) > 0 {
			fmt.Fprintf(&b, "\nSearchable terms: %s\n", strings.Join(terms, ", "))
		}
		b.WriteString("\nUse search() to explore the indexed content.")
		return b.String()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Indexed %d sections from %q into knowledge base.\n", indexed.TotalChunks, source)
	fmt.Fprintf(&b, "%d sections matched %q (%d lines, %sKB):\n\n", len(results), intent, totalLines, totalKB)

	for _, r := range results {
		preview := r.Content
		if nl := strings.IndexByte(preview, '\n'); nl != -1 {
			preview = preview[:nl]
		}
		if len(preview) > 120 {
			preview = preview[:120]
		}
		fmt.Fprintf(&b, "  - %s: %s\n", r.Title, preview)
	}

	if len(terms) > 0 {
		fmt.Fprintf(&b, "\nSearchable terms: %s\n", strings.Join(terms, ", "))
	}
	b.WriteString("\nUse search(queries: [...]) to retrieve full content of any section.")
	return b.String()
}
