package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
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

// handleFetchAndIndex fetches a URL and indexes the content.
// Unlike the TS reference (which uses a Node subprocess to bypass executor stdout
// truncation), Go uses native net/http directly — no truncation constraint applies.
func (s *Server) handleFetchAndIndex(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	url := req.GetString("url", "")
	source := req.GetString("source", "")
	force, _ := req.GetArguments()["force"].(bool)

	if url == "" {
		return errorResult("Missing required parameter: url"), nil
	}

	// SSRF protection: block requests to local/private networks
	if err := validateFetchURLFunc(url); err != nil {
		return errorResult(fmt.Sprintf("Blocked URL: %v", err)), nil
	}

	label := source
	if label == "" {
		label = url
	}

	// TTL cache check — skip re-fetch if content was recently indexed
	if !force {
		st := s.getStore()
		ttl := time.Duration(s.config.Store.Cache.FetchTTLHours) * time.Hour
		meta, err := st.GetSourceMeta(label)
		if err != nil {
			slog.Warn("cache check failed, proceeding with fetch", "label", label, "error", err)
		}
		if err == nil && meta != nil && time.Since(meta.IndexedAt) < ttl {
			s.stats.AddCacheHit(int64(meta.ChunkCount) * 1600) // ~1.6KB per chunk estimate
			text := fmt.Sprintf(
				"**Cache hit** — source %q was indexed %s (%d chunks).\nConfigured TTL: %dh. Use `force: true` to re-fetch.\nUse search(queries: [...], source: %q) for lookups.",
				meta.Label, formatAge(time.Since(meta.IndexedAt)), meta.ChunkCount,
				s.config.Store.Cache.FetchTTLHours, meta.Label,
			)
			return s.trackToolResponse("capy_fetch_and_index", textResult(text)), nil
		}
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

	// Reject binary content
	if isBinaryContent(contentType, body) {
		return errorResult(fmt.Sprintf("Cannot index binary content from %s (Content-Type: %s)", url, contentType)), nil
	}

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
// Creates a new converter per call rather than using a singleton — this avoids
// hidden state and is acceptable since fetch_and_index is not a hot path.
func convertHTMLToMarkdown(html string) (string, error) {
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
			table.NewTablePlugin(),
		),
	)

	// Strip navigation/chrome elements (script/style/noscript already removed by base)
	conv.Register.TagType("nav", converter.TagTypeRemove, converter.PriorityStandard)
	conv.Register.TagType("header", converter.TagTypeRemove, converter.PriorityStandard)
	conv.Register.TagType("footer", converter.TagTypeRemove, converter.PriorityStandard)

	return conv.ConvertString(html)
}

// validateFetchURLFunc is the URL validation function. Package-level var to
// allow tests to bypass SSRF checks when using httptest.NewServer (localhost).
var validateFetchURLFunc = validateFetchURL

// validateFetchURL blocks requests to local/private networks to prevent SSRF.
func validateFetchURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	hostname := parsed.Hostname()

	// Resolve hostname to check the actual IP
	ips, err := net.LookupIP(hostname)
	if err != nil {
		// If we can't resolve, allow — the HTTP client will fail with a better error
		return nil
	}

	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("access to local/private network address %s (%s) is forbidden", hostname, ip)
		}
	}

	return nil
}

// isBinaryContent returns true if the content appears to be binary (images, PDFs, etc.)
// based on Content-Type header and byte content inspection.
func isBinaryContent(contentType string, body []byte) bool {
	// Extract media type (strip parameters like charset, boundary, name)
	ct := strings.ToLower(contentType)
	mediaType := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])

	binaryTypes := []string{
		"image/", "audio/", "video/", "application/pdf",
		"application/zip", "application/gzip", "application/octet-stream",
		"application/x-tar", "application/x-bzip",
	}
	for _, prefix := range binaryTypes {
		if strings.HasPrefix(mediaType, prefix) {
			return true
		}
	}

	// Skip null-byte heuristic when Content-Type explicitly declares a text type —
	// the header already passed the binary-type check above, so we trust it.
	// This avoids false positives on UTF-16 encoded text which contains null bytes.
	if strings.HasPrefix(mediaType, "text/") {
		return false
	}

	// Heuristic: check for null bytes in the first 512 bytes
	checkLen := min(512, len(body))
	for _, b := range body[:checkLen] {
		if b == 0 {
			return true
		}
	}

	return false
}

// formatAge formats a duration as a human-readable age string.
func formatAge(d time.Duration) string {
	if days := int(d.Hours() / 24); days > 0 {
		return fmt.Sprintf("%dd ago", days)
	}
	if hours := int(d.Hours()); hours > 0 {
		return fmt.Sprintf("%dh ago", hours)
	}
	if minutes := int(d.Minutes()); minutes > 0 {
		return fmt.Sprintf("%dm ago", minutes)
	}
	return "just now"
}
