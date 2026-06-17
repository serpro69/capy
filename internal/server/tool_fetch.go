package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/giturl"
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
	kindStr := req.GetString("kind", "")

	if url == "" {
		return errorResult("Missing required parameter: url"), nil
	}

	// Validate at the boundary before cache check and network fetch — gives a clean
	// error cheaply. The store also validates at write time as defense-in-depth.
	kind := store.KindEphemeral
	if kindStr != "" {
		k := store.SourceKind(kindStr)
		if k != store.KindDurable && k != store.KindEphemeral {
			return errorResult(fmt.Sprintf("Invalid kind %q: accepted values are \"durable\" and \"ephemeral\"", kindStr)), nil
		}
		kind = k
	}

	// SSRF protection (scheme gate): reject non-http(s) URLs cheaply before any
	// cache or network work. IP-level classification happens at connect time in
	// the SSRF-safe transport below, closing the DNS-rebinding TOCTOU window.
	if err := validateFetchScheme(url); err != nil {
		return errorResult(fmt.Sprintf("Blocked URL: %v", err)), nil
	}

	// Git platform issue/PR/MR URLs should use platform CLI for comprehension,
	// not BM25-fragmented indexing. Block at the server level so this works for
	// ALL MCP clients (Claude Code, Codex, Cursor, etc.), not just those running hooks.
	if info, ok := giturl.ParsePlatformURL(url); ok {
		return s.trackToolResponse("capy_fetch_and_index", textResult(gitPlatformRedirectMessage(info))), nil
	}

	label := source
	if label == "" {
		label = url
	}

	// Cache/storage key includes the URL so two distinct URLs sharing one label
	// don't collide — without it, the first URL's cached response would mask the
	// second's. Used as both the GetSourceMeta lookup key and the storage label.
	cacheKey := composeFetchCacheKey(label, url)

	// TTL cache check — skip re-fetch if content was recently indexed
	if !force {
		st := s.getStore()
		ttl := time.Duration(s.config.Store.Cache.FetchTTLHours) * time.Hour
		meta, err := st.GetSourceMeta(cacheKey)
		if err != nil {
			slog.Warn("cache check failed, proceeding with fetch", "label", label, "error", err)
		}
		if err == nil && meta != nil && time.Since(meta.IndexedAt) < ttl && meta.Kind == kind {
			s.stats.AddCacheHit(int64(meta.ChunkCount) * 1600) // ~1.6KB per chunk estimate
			kindInfo := string(meta.Kind)
			if meta.Kind == store.KindEphemeral {
				kindInfo += ", excluded from default search"
			}
			// Surface the friendly label (not the composite key): a search source
			// filter is LIKE-based, so the short label still matches the stored key.
			text := fmt.Sprintf(
				"**Cache hit** — source %q was indexed %s (%d chunks, %s).\nConfigured TTL: %dh. Use `force: true` to re-fetch.\nUse search(queries: [...], source: %q) for lookups.",
				label, formatAge(time.Since(meta.IndexedAt)), meta.ChunkCount, kindInfo,
				s.config.Store.Cache.FetchTTLHours, label,
			)
			return s.trackToolResponse("capy_fetch_and_index", textResult(text)), nil
		}
	}

	// Fetch with timeout, redirect limit, and SSRF-safe transport. The transport's
	// DialContext resolves DNS and classifies every IP at connect time, so each
	// redirect to a new host is re-validated (DNS-rebinding defense).
	client := &http.Client{
		Timeout:   fetchTimeout,
		Transport: newFetchTransport(),
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
		indexed, err = st.IndexJSON(content, cacheKey, kind)

	case strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml"):
		md, convErr := convertHTMLToMarkdown(content)
		if convErr != nil {
			indexed, err = st.IndexPlainText(content, cacheKey, kind)
		} else {
			content = md
			indexed, err = st.Index(md, cacheKey, "", kind)
		}

	default:
		indexed, err = st.IndexPlainText(content, cacheKey, kind)
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

	kindNote := fmt.Sprintf("Indexed as **%s**", kind)
	if kind == store.KindEphemeral {
		kindNote += fmt.Sprintf(
			" (24h TTL, excluded from default search — use `source: %q` or `include_kinds: [\"durable\",\"ephemeral\"]` for follow-up queries)",
			label,
		)
	}

	// Surface the friendly label rather than the composite cache key (label|url):
	// the search source filter is LIKE-based, so the short label still matches.
	text := fmt.Sprintf(
		"Fetched and indexed **%d sections** (%sKB) from: %s\n%s\nUse search(queries: [...], source: %q) for specific lookups.\n\n---\n\n%s",
		indexed.TotalChunks, totalKB, label, kindNote, label, preview,
	)

	return s.trackToolResponse("capy_fetch_and_index", textResult(text)), nil
}

// composeFetchCacheKey derives the cache/storage key for a fetched URL by joining
// the (user-chosen or URL-defaulted) label with the URL. Two distinct URLs sharing
// the same label therefore get distinct keys, so one URL's cached response can no
// longer mask another's.
//
// The composite is treated as an opaque key — nothing ever splits it back into its
// (label, url) parts — so a '|' inside a user-chosen label is harmless: distinct
// (label, url) pairs always yield distinct keys regardless of the separator.
//
// The composite is used as BOTH the GetSourceMeta cache-lookup key and the storage
// label, so a later fetch of the same label+url hits the same entry. Callers can
// still recover the source with capy_search(source: "<label>") because the search
// source filter is LIKE-based — the friendly label is a substring of the key.
func composeFetchCacheKey(label, url string) string {
	return label + "|" + url
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

// newFetchTransport returns the HTTP transport used to fetch remote content.
// It is a package-level var so tests can substitute a transport that permits
// loopback connections (httptest.NewServer binds to 127.0.0.1, which the
// SSRF-safe dialer blocks). Production resolves to getFetchTransport, which
// shares one SSRF-safe transport process-wide so connections pool correctly.
var newFetchTransport = getFetchTransport

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

// gitPlatformRedirectMessage returns the redirect text when capy_fetch_and_index
// is called with a git platform issue/PR/MR URL.
func gitPlatformRedirectMessage(info giturl.Info) string {
	alternative := "your platform's CLI or WebSearch"
	if ghCmd := info.GhCommand(); ghCmd != "" {
		alternative = "`" + ghCmd + "`"
	}

	return fmt.Sprintf(
		"**Not fetched** — this URL points to a %s (#%s).\n\n"+
			"For full comprehension, use %s instead.\n"+
			"capy_fetch_and_index BM25-fragments content, losing sequential context.",
		info.KindDisplay(), info.Number, alternative,
	)
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
