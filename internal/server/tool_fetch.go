package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/serpro69/capy/internal/giturl"
	"github.com/serpro69/capy/internal/store"
	"golang.org/x/sync/errgroup"
)

const (
	fetchTimeout     = 30 * time.Second
	fetchMaxRedirect = 10
	fetchMaxBody     = 10 * 1024 * 1024 // 10MB
	fetchPreviewLen  = 3072
	fetchUserAgent   = "capy/1.0 (MCP knowledge indexer)"

	// fetchBatchPreviewLen caps each per-URL preview in batch mode so the
	// aggregate stays small (~3KB for a full 8-URL batch); the single-URL path
	// keeps the larger fetchPreviewLen.
	fetchBatchPreviewLen = 384

	// maxFetchBatchRequests bounds how many URLs a single batch may fetch. The
	// concurrency clamp limits in-flight fetches, but every body is buffered until
	// the serial index phase, so the request COUNT — not the worker count — is what
	// caps peak memory. Guards against an unbounded-batch OOM/DoS (see ADR-022).
	maxFetchBatchRequests = 20
)

// handleFetchAndIndex fetches a URL and indexes the content.
// Unlike the TS reference (which uses a Node subprocess to bypass executor stdout
// truncation), Go uses native net/http directly — no truncation constraint applies.
func (s *Server) handleFetchAndIndex(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	url := req.GetString("url", "")
	source := req.GetString("source", "")
	force, _ := args["force"].(bool)
	kindStr := req.GetString("kind", "")
	requests := coerceFetchRequests(args["requests"])

	// Validate kind once at the boundary — it applies to both the single-URL and
	// batch paths. The store also validates at write time as defense-in-depth.
	kind := store.KindEphemeral
	if kindStr != "" {
		k := store.SourceKind(kindStr)
		if k != store.KindDurable && k != store.KindEphemeral {
			return errorResult(fmt.Sprintf("Invalid kind %q: accepted values are \"durable\" and \"ephemeral\"", kindStr)), nil
		}
		kind = k
	}

	// Batch mode takes precedence when `requests` is provided; `url`+`source`
	// remain the single-URL path for backward compatibility.
	if len(requests) > 0 {
		concurrency := int(req.GetFloat("concurrency", 1))
		return s.handleFetchBatch(ctx, requests, kind, force, concurrency)
	}
	// `requests` was supplied but yielded no usable entries (not an array, or every
	// entry missing a url) — fail loud rather than emitting the confusing
	// "missing url" error below, which hides that the batch param was the problem.
	if args["requests"] != nil {
		return errorResult("Invalid requests: provide an array of {url, source?} objects, each with a non-empty url"), nil
	}

	if url == "" {
		return errorResult("Missing required parameter: url (or requests for batch mode)"), nil
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

	// Fetch the remote content (SSRF-safe transport, redirect cap, size limit).
	content, contentType, fetchErr := fetchRemoteContent(ctx, url)
	if fetchErr != "" {
		return errorResult(fetchErr), nil
	}

	// Track raw bytes
	s.stats.AddBytesIndexed(int64(len(content)))

	// Index via the content-type-aware strategy. content becomes the (possibly
	// HTML→markdown converted) text used for the preview below.
	st := s.getStore()
	content, indexed, err := indexFetchedContent(st, content, contentType, cacheKey, kind)
	if err != nil {
		return errorResult(fmt.Sprintf("Index error: %v", err)), nil
	}

	// Build preview (rune-safe — a raw byte cut can split a multibyte UTF-8 rune)
	preview := content
	if len(preview) > fetchPreviewLen {
		preview = truncateRunes(preview, fetchPreviewLen) + "\n\n…[truncated — use search() for full content]"
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

// truncateRunes returns s truncated to at most n bytes without splitting a
// multibyte UTF-8 rune — it backtracks to the nearest rune boundary at or before
// n. Slicing a Go string at a raw byte index can cut a rune in half and emit an
// invalid UTF-8 sequence (a garbled U+FFFD once JSON-serialized into the MCP
// response), so any byte-length cap on user/remote display text must go through
// here.
func truncateRunes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// fetchRemoteContent performs the HTTP GET for a single URL using the SSRF-safe
// transport, redirect cap, and body-size limit, returning the decoded text body
// and its Content-Type. The third return is a non-empty, user-facing error
// message on any failure (empty means success) — kept as a plain string rather
// than an error so the pre-formatted messages reach the caller verbatim. Binary
// and empty responses are rejected here. It performs no store I/O, so callers may
// invoke it concurrently. The request honors ctx, so a client cancellation aborts
// the in-flight fetch (in addition to the fetchTimeout ceiling) — important for
// the batch path, where up to maxFetchBatchRequests fetches may be in flight.
func fetchRemoteContent(ctx context.Context, url string) (content, contentType, errMsg string) {
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

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", "", fmt.Sprintf("Invalid URL: %v", err)
	}
	httpReq.Header.Set("User-Agent", fetchUserAgent)

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", "", fmt.Sprintf("Failed to fetch %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Sprintf("Failed to fetch %s: HTTP %d", url, resp.StatusCode)
	}

	// Read body with size limit
	limited := io.LimitReader(resp.Body, fetchMaxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", "", fmt.Sprintf("Failed to read response from %s: %v", url, err)
	}
	if len(body) > fetchMaxBody {
		return "", "", fmt.Sprintf("Response too large (>%dMB)", fetchMaxBody/(1024*1024))
	}
	if len(body) == 0 {
		return "", "", fmt.Sprintf("Fetched %s but got empty content", url)
	}

	ct := resp.Header.Get("Content-Type")
	if isBinaryContent(ct, body) {
		return "", "", fmt.Sprintf("Cannot index binary content from %s (Content-Type: %s)", url, ct)
	}

	return string(body), ct, ""
}

// indexFetchedContent routes fetched content to the correct index strategy based
// on its Content-Type and returns the content used for the preview (HTML is
// converted to markdown) alongside the index result. Callers MUST serialize these
// calls — SQLite writes must not run concurrently.
func indexFetchedContent(st *store.ContentStore, content, contentType, cacheKey string, kind store.SourceKind) (string, *store.IndexResult, error) {
	switch {
	case strings.Contains(contentType, "application/json") || strings.Contains(contentType, "+json"):
		indexed, err := st.IndexJSON(content, cacheKey, kind)
		return content, indexed, err

	case strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml"):
		md, convErr := convertHTMLToMarkdown(content)
		if convErr != nil {
			indexed, err := st.IndexPlainText(content, cacheKey, kind)
			return content, indexed, err
		}
		indexed, err := st.Index(md, cacheKey, "", kind)
		return md, indexed, err

	default:
		indexed, err := st.IndexPlainText(content, cacheKey, kind)
		return content, indexed, err
	}
}

// fetchItemResult is the per-URL outcome of the concurrent fetch phase in batch
// mode. Exactly one terminal state is set: errMsg (validation/fetch failure),
// redirect (git platform URL — not fetched), cacheMeta (TTL cache hit, already
// indexed), or content (freshly fetched, to be indexed in the serial phase).
type fetchItemResult struct {
	url         string
	label       string
	cacheKey    string
	errMsg      string
	redirect    bool
	cacheMeta   *store.SourceMeta
	content     string
	contentType string
}

// fetchOne validates, cache-checks, and fetches a single batch request. It only
// READS from the shared store (the TTL cache lookup), so it is safe to run
// concurrently; the SQLite write happens later in handleFetchBatch's serial
// phase. The store is passed in (resolved once by the caller) rather than fetched
// per goroutine. ctx is the parent request context — a client cancellation aborts
// every in-flight fetch.
func (s *Server) fetchOne(ctx context.Context, st *store.ContentStore, rq fetchRequest, kind store.SourceKind, force bool) fetchItemResult {
	label := rq.Source
	if label == "" {
		label = rq.URL
	}
	res := fetchItemResult{url: rq.URL, label: label, cacheKey: composeFetchCacheKey(label, rq.URL)}

	// SSRF scheme gate (per URL — never skipped in batch mode).
	if err := validateFetchScheme(rq.URL); err != nil {
		res.errMsg = fmt.Sprintf("Blocked URL: %v", err)
		return res
	}

	// Git platform issue/PR/MR URLs are not BM25-fragment-friendly; flag them for
	// a redirect note instead of fetching, matching the single-URL behavior.
	if _, ok := giturl.ParsePlatformURL(rq.URL); ok {
		res.redirect = true
		return res
	}

	// TTL cache check, keyed by the same composite as the single-URL path.
	if !force {
		ttl := time.Duration(s.config.Store.Cache.FetchTTLHours) * time.Hour
		meta, err := st.GetSourceMeta(res.cacheKey)
		if err != nil {
			slog.Warn("cache check failed, proceeding with fetch", "label", label, "error", err)
		}
		if err == nil && meta != nil && time.Since(meta.IndexedAt) < ttl && meta.Kind == kind {
			res.cacheMeta = meta
			return res
		}
	}

	content, contentType, errMsg := fetchRemoteContent(ctx, rq.URL)
	if errMsg != "" {
		res.errMsg = errMsg
		return res
	}
	res.content = content
	res.contentType = contentType
	return res
}

// handleFetchBatch fetches multiple URLs concurrently (bounded by concurrency)
// and indexes them serially. Per the design: parallel fetches, serial FTS5
// writes. Stats are also accumulated in the serial phase so SessionStats is never
// mutated concurrently. ctx is threaded into every fetch so a client cancellation
// aborts in-flight requests instead of leaving up to concurrency goroutines
// blocked on the network until fetchTimeout.
func (s *Server) handleFetchBatch(ctx context.Context, requests []fetchRequest, kind store.SourceKind, force bool, concurrency int) (*mcp.CallToolResult, error) {
	// Cap the batch SIZE, not just the worker count. The concurrency clamp below
	// bounds how many fetches are in-flight at once, but every fetched body (up to
	// fetchMaxBody = 10MB) is retained in `items` until the serial phase — so peak
	// memory grows with len(requests), not with concurrency. Fail loud before
	// buffering an unbounded amount of remote content (OOM/DoS guard; see ADR-022).
	if len(requests) > maxFetchBatchRequests {
		return errorResult(fmt.Sprintf(
			"Batch too large: %d URLs requested (max %d). Split into smaller batches.",
			len(requests), maxFetchBatchRequests,
		)), nil
	}

	// Clamp to [1, min(maxBatchConcurrency, len(requests))] — never more workers
	// than there are URLs.
	concurrency = max(1, min(concurrency, maxBatchConcurrency, len(requests)))

	// Resolve the store once and share it across the fetch goroutines (cache reads)
	// and the serial index phase, rather than re-entering getStore per goroutine.
	st := s.getStore()

	// Concurrent fetch phase — index-keyed slice preserves request order.
	items := make([]fetchItemResult, len(requests))
	var g errgroup.Group
	g.SetLimit(concurrency)
	for i, rq := range requests {
		g.Go(func() error {
			items[i] = s.fetchOne(ctx, st, rq, kind, force)
			return nil
		})
	}
	// Each closure handles its own outcome in-slot and returns nil, so Wait never
	// reports an error — it only blocks until all fetches finish. If a future edit
	// makes the closure return a non-nil error, this discard must be revisited (and
	// sibling-cancellation semantics reconsidered).
	_ = g.Wait()

	// Serial phase: index fresh fetches (SQLite writes must not run concurrently),
	// accumulate stats, and build the per-URL response.
	var (
		lines      []string
		previews   []string
		okCount    int // URLs whose content is in the index (freshly indexed or cache hit)
		cachedHits int
		freshBytes int
	)
	for _, it := range items {
		switch {
		case it.errMsg != "":
			lines = append(lines, fmt.Sprintf("✗ %s — %s", it.url, it.errMsg))

		case it.redirect:
			lines = append(lines, fmt.Sprintf("↪ %s — not fetched (git platform issue/PR/MR; use platform CLI or WebSearch)", it.url))

		case it.cacheMeta != nil:
			s.stats.AddCacheHit(int64(it.cacheMeta.ChunkCount) * 1600) // ~1.6KB per chunk estimate
			okCount++
			cachedHits++
			lines = append(lines, fmt.Sprintf("✓ %s — cache hit (%d chunks)", it.label, it.cacheMeta.ChunkCount))

		default:
			s.stats.AddBytesIndexed(int64(len(it.content)))
			finalContent, indexed, err := indexFetchedContent(st, it.content, it.contentType, it.cacheKey, kind)
			if err != nil {
				lines = append(lines, fmt.Sprintf("✗ %s — index error: %v", it.url, err))
				continue
			}
			okCount++
			freshBytes += len(finalContent)
			lines = append(lines, fmt.Sprintf("✓ %s — indexed %d sections", it.label, indexed.TotalChunks))

			// Rune-safe truncation — a raw byte cut can split a multibyte UTF-8 rune.
			preview := finalContent
			if len(preview) > fetchBatchPreviewLen {
				preview = truncateRunes(preview, fetchBatchPreviewLen) + "…"
			}
			previews = append(previews, fmt.Sprintf("## %s\n%s", it.label, preview))
		}
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Processed %d/%d URLs — %d newly indexed (%.1fKB), %d from cache (kind=%s).\n\n",
		okCount, len(requests), okCount-cachedHits, float64(freshBytes)/1024, cachedHits, kind)
	out.WriteString(strings.Join(lines, "\n"))
	if len(previews) > 0 {
		out.WriteString("\n\n---\n\n")
		out.WriteString(strings.Join(previews, "\n\n"))
	}
	out.WriteString("\n\nUse search(queries: [...], source: \"<label>\") for follow-up lookups.")

	return s.trackToolResponse("capy_fetch_and_index", textResult(out.String())), nil
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
