package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/sanitize"
	"github.com/serpro69/capy/internal/store"
)

// Escaping happens before the byte cap: even a preview consisting entirely of
// Markdown delimiters stays within 500 bytes and cannot create headings/fences.
var batchMarkdownEscape = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
	"<", `\<`, ">", `\>`, "#", `\#`, "!", `\!`, "|", `\|`,
)

func batchOneLine(text string) string {
	return strings.Join(strings.Fields(strings.ToValidUTF8(sanitize.StripSecrets(text), "?")), " ")
}

func batchDisplay(text string, limit int) string {
	text = batchMarkdownEscape.Replace(batchOneLine(text))
	if len(text) > limit {
		return truncateRunes(text, limit-len("…")) + "…"
	}
	return text
}

// batchTextBudget accounts for each fragment's escaped JSON bytes. UTF-8 text
// length alone misses quotes, backslashes, control characters and HTML escapes.
// String marshaling cannot fail; these values contain no custom marshalers.
type batchTextBudget struct {
	text      strings.Builder
	remaining int
}

func jsonTextBytes(text string) int {
	encoded, _ := json.Marshal(text)
	return len(encoded) - 2 // quotes belong to the enclosing text-content object
}

func (b *batchTextBudget) add(text string) bool {
	size := jsonTextBytes(text)
	if size > b.remaining {
		return false
	}
	b.text.WriteString(text)
	b.remaining -= size
	return true
}

func batchCommandInventory(commands []CommandInput, outputs []batchCommandOutput) string {
	// Reserve the omission count even if the final admitted item fills the cap.
	b := batchTextBudget{remaining: 4096 - 128}
	b.add("## Commands\n\n")
	shown := 0
	for i, command := range commands {
		state := ""
		if outputs[i].skipped {
			state = " (not executed)"
		}
		line := fmt.Sprintf("- %s%s: %s\n", batchDisplay(command.Label, 160), state, batchDisplay(command.Command, 500))
		if !b.add(line) {
			break
		}
		shown++
	}
	b.remaining += 128
	b.add(fmt.Sprintf("Commands omitted: %d.\n\n", len(commands)-shown))
	return b.text.String()
}

func batchSectionInventory(total int, sections []store.SectionMeta, err error) string {
	b := batchTextBudget{remaining: 8192 - 128}
	b.add("## Indexed Sections\n\n")
	shown := 0
	if err != nil {
		b.add(fmt.Sprintf("(inventory unavailable: %s)\n", batchDisplay(err.Error(), 512)))
	} else {
		for _, section := range sections {
			line := fmt.Sprintf("- %s (%.1fKB)\n", batchDisplay(section.Title, 160), float64(section.Bytes)/1024)
			if !b.add(line) {
				break
			}
			shown++
		}
	}
	b.remaining += 128
	b.add(fmt.Sprintf("Sections omitted: %d.\n\n", max(0, total-shown)))
	return b.text.String()
}

func (s *Server) batchResponse(ctx context.Context, commands []CommandInput, outputs []batchCommandOutput, queries []string,
	scope, source string, indexed *store.IndexResult, rawBytes, skipped int) *mcp.CallToolResult {
	st := s.getStore()
	total, sections, inventoryErr := st.GetSectionInventory(ctx, indexed.SourceID)
	if inventoryErr != nil && total == 0 {
		// A failed count must not misreport unchanged content as zero sections.
		return errorResult(fmt.Sprintf("batch indexed but section metadata unavailable: %v", inventoryErr))
	}
	overhead, _ := json.Marshal(textResult("")) // fixed text-only result, cannot fail
	b := batchTextBudget{remaining: maxBatchOutput - len(overhead) - 512}
	outcome := fmt.Sprintf("Indexed %d sections.", total)
	if indexed.AlreadyIndexed {
		outcome = fmt.Sprintf("Unchanged; retained %d sections.", total)
	}
	b.add(fmt.Sprintf("Executed %d commands; skipped %d (%.1fKB captured output). %s\nQuery scope: %s (knowledge only).\n\n",
		len(commands)-skipped, skipped, float64(rawBytes)/1024, outcome, scope))
	// The exact bounded source selector is mandatory and precedes optional output.
	// Use JSON quoting: Go's %q can emit \x escapes for control-bearing labels,
	// which cannot be pasted into a JSON tool argument.
	quotedSource, _ := json.Marshal(source)
	b.add(fmt.Sprintf("Batch source: %s\nFollow-up:\n    capy_search(queries: [...], source: %s)\n\n", quotedSource, quotedSource))
	b.add(batchCommandInventory(commands, outputs))
	b.add(batchSectionInventory(total, sections, inventoryErr))
	opts := store.SearchOptions{Source: source, SourceMatchMode: "exact"}
	if scope == "global" {
		opts = store.SearchOptions{IncludeKinds: []store.SourceKind{store.KindDurable, store.KindEphemeral}}
	}
	searched, shown := 0, 0
	for _, query := range queries {
		if b.remaining < 1024 {
			break
		}
		results, searchErr := st.SearchWithFallbackContext(ctx, query, 3, opts)
		searched++
		var block strings.Builder
		fmt.Fprintf(&block, "## %s\n\n", batchDisplay(query, 512))
		switch {
		case searchErr != nil:
			fmt.Fprintf(&block, "(search error: %s)\n\n", batchDisplay(searchErr.Error(), 512))
		case len(results) == 0:
			block.WriteString("No matching sections found.\n\n")
		default:
			for _, result := range results {
				fmt.Fprintf(&block, "Source: %s\n### %s\n%s\n\n", batchDisplay(result.Label, 512),
					batchDisplay(result.Title, 160), strings.ToValidUTF8(ExtractSnippet(result.Content, query, 3000, result.Highlighted), "?"))
			}
		}
		if !b.add(block.String()) {
			break
		}
		shown++
	}
	// Terms are optional, bounded individually and collectively. A single long
	// identifier must not consume the response or hide the omission notice.
	if b.remaining > 256 {
		terms, err := st.GetDistinctiveTerms(indexed.SourceID, 40)
		if err == nil && len(terms) > 0 {
			t := batchTextBudget{remaining: min(b.remaining, 2048)}
			t.add("Searchable terms for follow-up:")
			for _, term := range terms {
				if !t.add(" " + batchDisplay(term, 80)) {
					break
				}
			}
			b.add(t.text.String())
		}
	}
	b.remaining += 512
	b.add(fmt.Sprintf("\nSearched %d queries. Query outputs omitted: %d. Use the source selector above for follow-up.\n", searched, len(queries)-shown))
	return textResult(b.text.String())
}

// boundBatchResult is the final serialized-result guard, including early error
// paths that can contain arbitrarily long policy/command text. Normal success
// output has already budgeted inventories, source selectors and omission counts.
func boundBatchResult(result *mcp.CallToolResult) *mcp.CallToolResult {
	encoded, err := json.Marshal(result)
	if err != nil {
		return errorResult("could not encode batch result")
	}
	if len(encoded) <= maxBatchOutput {
		return result
	}
	if len(result.Content) != 1 {
		return errorResult("batch result exceeds response limit")
	}
	content, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		return errorResult("batch result exceeds response limit")
	}
	const notice = "\n(batch response truncated at serialized output limit)"
	text := strings.ToValidUTF8(sanitize.StripSecrets(content.Text), "?")
	lo, hi := 0, min(len(text), maxBatchOutput)
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		content.Text = truncateRunes(text, mid) + notice
		result.Content[0] = content
		encoded, err = json.Marshal(result)
		if err != nil {
			return errorResult("could not encode batch result")
		}
		if len(encoded) > maxBatchOutput {
			hi = mid - 1
		} else {
			lo = mid
		}
	}
	content.Text = truncateRunes(text, lo) + notice
	result.Content[0] = content
	return result
}
