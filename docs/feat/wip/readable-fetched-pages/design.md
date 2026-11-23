# Index the readable article in a fetched page

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

When a developer indexes an API manual, search should find the article, its examples and working reference links. Capy already converts HTML to Markdown, including tables, and removes scripts and common page chrome. The conversion receives the whole document and no final URL. An aside can outweigh the actual article, relative references lose their origin after a redirect, and a successful HTTP response can contain only a client-rendered shell.

## Choosing and rendering the article

For HTML or XHTML, select the first visible nonempty `main` in document order, then a sole visible nonempty `article`, then the visible body or fragment. Exclude hidden subtrees, navigation, executable/template content and form controls from selection and conversion. A semantic header *inside* the selected article can contain its title. Preserve its heading, meaningful links, tables, lists, and whitespace in code. Use the document title once if the selected article has no first-level heading. Select on visible, non-whitespace body text. A title alone cannot make a script shell a useful article.

Resolve relative HTTP(S) destinations against the final response URL, retaining the requested URL for cache identity. Keep same-page fragment references meaningful. Render visible link text alone for unsupported schemes or credential-bearing destinations. Conversion never dereferences embedded resources or links. Send an explicit Accept preference for HTML, JSON and text with the existing User-Agent on the initial GET and redirects.

If no readable body exists after selection, report that outcome for the URL and keep the last good index and validation timestamp. Accept a legitimate one-sentence page. Indexed HTML and its previews use the same sanitized readable representation. The cache-hit path continues to serve a previously validated source until its TTL expires or the caller forces a fetch. A successful conversion of changed readable content flows through normal source replacement and retention. [Fetch freshness](../../../reference/tools.md#fetch-freshness-and-statistics)

## Assumptions

Checking the HTML tree and converting its selected subtree within the existing response-body cap is sufficient for static reference pages. The requested label and URL identify the logical source even as the final URL changes.

## Not Doing

JavaScript rendering would require a browser and a different network policy. Crawling follows other URLs and would exceed the scope of a single fetch.

## Rejected alternatives

A minimum word-count rule would reject terse documentation. Indexing raw markup on an empty or failed conversion would replace useful content with the page shell.
