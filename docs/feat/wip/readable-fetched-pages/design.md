# Index the readable article in a fetched page

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

When a developer indexes an API manual, search should find the article, its examples and working reference links. Capy already converts HTML to Markdown, including tables, and removes scripts and common page chrome. The conversion receives the whole document and no final URL. An aside can outweigh the actual article, relative references lose their origin after a redirect, and a successful HTTP response can contain only a client-rendered shell.

## Choosing and rendering the article

For HTML or XHTML, select the first visible nonempty `main` in document order, otherwise the visible body or fragment. Inside that root, a sole top-level visible nonempty `article` wins; several articles keep the outer root. Exclude hidden subtrees, navigation, executable/template content and form controls from selection and conversion. A semantic header *inside* an article can contain its title. Preserve its heading, meaningful links, tables, lists, and whitespace in code. Use the document title once if the selected article has no first-level heading. Select on visible, non-whitespace body text. A title alone cannot make a script shell a useful article.

Visibility comes from markup only: the `hidden` attribute, `aria-hidden="true"` and inline `display:none` or `visibility:hidden`. The `hidden` attribute does not hide content a reader reveals by interaction: `hidden="until-found"` and inactive tab panels (`role="tabpanel"`), which documentation sites use for per-tool and per-language examples. Navigation is `nav` or `role="navigation"`. Content a stylesheet hides is indexed. HTML is decoded from its BOM, declared Content-Type charset or `<meta>` charset, defaulting to UTF-8. A declared charset capy cannot decode is a conversion failure, as is `<private>` markup whose region boundary is ambiguous (no matching end tag, a stray end tag, or markup the HTML parser and the redaction pass read differently); both keep the last good copy. `<private>` regions and recognized secrets are redacted in the parsed HTML before rendering, because Markdown escaping would otherwise hide token prefixes from the sanitizer; the preview and the stored text show the same sanitized Markdown. The source size limit therefore measures that redacted Markdown ([ADR-022](../../../adr/022-source-size-guard-and-db-bloat-prevention.md)). Converted pages are indexed as Markdown, so a single heading remains a section title.

Resolve relative HTTP(S) destinations against the final response URL, retaining the requested URL for cache identity. Keep same-page fragment references meaningful. Render visible link text alone for unsupported schemes or credential-bearing destinations. Conversion never dereferences embedded resources or links. Send an explicit Accept preference for HTML, JSON and text with the existing User-Agent on the initial GET and redirects.

If no readable body exists after selection, report that outcome for the URL and keep the last good index and validation timestamp. Accept a legitimate one-sentence page. A page whose only visible text is a placeholder such as “Loading…” counts as a short page and replaces the source. Indexed HTML and its previews use the same sanitized readable representation. The cache-hit path continues to serve a previously validated source until its TTL expires or the caller forces a fetch. A successful conversion of changed readable content flows through normal source replacement and retention. [Fetch freshness](../../../reference/tools.md#fetch-freshness-and-statistics)

## Assumptions

Checking the HTML tree and converting its selected subtree within the existing response-body cap is sufficient for static reference pages. The requested label and URL identify the logical source even as the final URL changes.

## Not Doing

JavaScript rendering would require a browser and a different network policy. Crawling follows other URLs and would exceed the scope of a single fetch.

## Rejected alternatives

A minimum word-count rule would reject terse documentation. Indexing raw markup on an empty or failed conversion would replace useful content with the page shell. Keeping the whole `main` when it contains one article was rejected after the [design review](review-design-2026-11-23.md): repository and docs pages place file listings and breadcrumbs inside `main`. The accepted cost is that a page wrapping a single widget in `<article>` beside its real content indexes only the widget. Treating status regions as empty was rejected to keep the shell rule observable.
