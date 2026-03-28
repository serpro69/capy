package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/store"
)

const (
	fetchTimeout     = 30 * time.Second
	fetchMaxRedirect = 10
	fetchMaxBody     = 10 * 1024 * 1024 // 10MB
	fetchPreviewLen  = 3072
	fetchUserAgent   = "capy/1.0 (MCP knowledge indexer)"
)

func (s *Server) handleFetchAndIndex(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	url := req.GetString("url", "")
	source := req.GetString("source", "")

	if url == "" {
		return errorResult("Missing required parameter: url"), nil
	}
	if source == "" {
		source = url
	}

	// Fetch with timeout and redirect limit
	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= fetchMaxRedirect {
				return fmt.Errorf("too many redirects (%d)", fetchMaxRedirect)
			}
			return nil
		},
	}

	httpReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return errorResult(fmt.Sprintf("Invalid URL: %v", err)), nil
	}
	httpReq.Header.Set("User-Agent", fetchUserAgent)

	resp, err := client.Do(httpReq)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to fetch %s: %v", url, err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorResult(fmt.Sprintf("Failed to fetch %s: HTTP %d", url, resp.StatusCode)), nil
	}

	// Read body with size limit
	limited := io.LimitReader(resp.Body, fetchMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to read response from %s: %v", url, err)), nil
	}
	if len(body) > fetchMaxBody {
		return errorResult(fmt.Sprintf("Response too large (>%dMB)", fetchMaxBody/(1024*1024))), nil
	}
	if len(body) == 0 {
		return errorResult(fmt.Sprintf("Fetched %s but got empty content", url)), nil
	}

	content := string(body)
	contentType := resp.Header.Get("Content-Type")

	// Track raw bytes
	s.stats.AddBytesIndexed(int64(len(body)))

	// Route to appropriate indexing strategy
	st := s.getStore()
	var indexed *store.IndexResult

	switch {
	case strings.Contains(contentType, "application/json") || strings.Contains(contentType, "+json"):
		indexed, err = st.IndexJSON(content, source)

	case strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml"):
		md, convErr := convertHTMLToMarkdown(content)
		if convErr != nil {
			// Fall back to plain text if conversion fails
			indexed, err = st.IndexPlainText(content, source)
		} else {
			content = md
			indexed, err = st.Index(md, source, "")
		}

	default:
		indexed, err = st.IndexPlainText(content, source)
	}

	if err != nil {
		return errorResult(fmt.Sprintf("Index error: %v", err)), nil
	}

	// Build preview
	preview := content
	if len(preview) > fetchPreviewLen {
		preview = preview[:fetchPreviewLen] + "\n\n…[truncated — use search() for full content]"
	}
	totalKB := fmt.Sprintf("%.1f", float64(len(content))/1024)

	text := fmt.Sprintf(
		"Fetched and indexed **%d sections** (%sKB) from: %s\nFull content indexed in sandbox — use search(queries: [...], source: %q) for specific lookups.\n\n---\n\n%s",
		indexed.TotalChunks, totalKB, indexed.Label, indexed.Label, preview,
	)

	return s.trackToolResponse("capy_fetch_and_index", textResult(text)), nil
}

// convertHTMLToMarkdown converts HTML to markdown, stripping non-content elements.
// Removes script, style, noscript (base defaults) plus nav, header, footer.
func convertHTMLToMarkdown(html string) (string, error) {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
		),
	)

	// Strip navigation/chrome elements (script/style/noscript already removed by base)
	conv.Register.TagType("nav", converter.TagTypeRemove, converter.PriorityStandard)
	conv.Register.TagType("header", converter.TagTypeRemove, converter.PriorityStandard)
	conv.Register.TagType("footer", converter.TagTypeRemove, converter.PriorityStandard)

	return conv.ConvertString(html)
}
