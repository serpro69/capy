package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/serpro69/capy/internal/sanitize"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"
)

// errNoReadableContent reports an HTML response with no visible text after
// pruning and article selection — typically a client-rendered application
// shell. Callers must not index anything for it, so the last good copy and its
// validation time survive.
var errNoReadableContent = errors.New("no readable content after article selection")

// errPrivateBoundary reports <private> markup whose region boundary the token
// pass and the HTML parser could disagree about: an unterminated region, a
// stray end tag, or a private element the parser built where the token pass
// saw none (raw-text, CDATA or foreign-content handling differs). Conversion
// fails closed rather than risk indexing private content or a truncated page.
var errPrivateBoundary = errors.New("ambiguous <private> region boundary")

// htmlCharsetPrescanLen matches the WHATWG prescan window used to find a
// <meta> charset declaration.
const htmlCharsetPrescanLen = 1024

// convertHTMLToMarkdown decodes an HTML body, removes content a reader cannot
// see, selects the readable root and renders it as Markdown.
//
// Root selection: the first visible nonempty <main>, otherwise <body>. Inside
// that root a sole top-level visible nonempty <article> wins; several articles
// keep the outer root. The document title is prepended once when the selected
// root has no <h1>, but a title alone never makes an empty page readable.
//
// Links resolve against finalURL, the response URL after redirects; see
// rewriteReferences for the destination rules and the maxSourceBytes budget.
func convertHTMLToMarkdown(body, contentType string, finalURL *url.URL, maxSourceBytes int) (string, error) {
	decoded, err := decodeHTML(body, contentType)
	if err != nil {
		return "", err
	}
	// Redact <private> regions on the token stream, before tree construction can
	// move their content out of the wrapper (table rows are foster-parented).
	// Secret patterns are never applied to raw markup — the generic key=value
	// pattern can consume an attribute's closing quote; redactTree applies them
	// to parsed text and attributes instead.
	decoded, err = redactPrivateRegions(decoded)
	if err != nil {
		return "", err
	}
	// The parser rejects nesting deeper than 512 elements with an error, which
	// bounds the recursive renderer below.
	doc, err := html.Parse(strings.NewReader(decoded))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}

	// The token pass removed every region it recognized; a private element the
	// parser still built means the two disagree about its boundary.
	if findElementNamed(doc, "private") != nil {
		return "", errPrivateBoundary
	}

	title := documentTitle(doc)
	pruneUnreadable(doc)
	// Private regions written as text only, before selection, so text that
	// redacts to the private placeholder cannot select a root or label a link.
	// Secret patterns wait for the final pass: an early partial match could
	// leave a suffix that later joins other text unrecognized.
	redactTree(doc, sanitize.StripPrivate)
	root := selectReadableRoot(doc)
	if root == nil {
		return "", errNoReadableContent
	}
	if err := rewriteReferences(root, finalURL, maxSourceBytes); err != nil {
		return "", err
	}
	// After the rewrite, so text that image replacement and link removal made
	// adjacent is redacted as the one string the renderer will emit.
	redactTree(root, sanitize.StripSecrets)
	// That join can complete a private region, leaving only its placeholder.
	if !hasVisibleText(root) {
		return "", errNoReadableContent
	}
	if title != "" && findElement(root, atom.H1) == nil {
		h1 := &html.Node{Type: html.ElementNode, Data: "h1", DataAtom: atom.H1}
		h1.AppendChild(&html.Node{Type: html.TextNode, Data: title})
		root.InsertBefore(h1, root.FirstChild)
	}
	if root.Parent != nil {
		root.Parent.RemoveChild(root)
	}

	// A new converter per call avoids hidden state; fetch is not a hot path.
	conv := converter.NewConverter(
		converter.WithPlugins(
			base.NewBasePlugin(),
			commonmark.NewCommonmarkPlugin(),
			table.NewTablePlugin(),
		),
	)
	md, err := conv.ConvertNode(root)
	if err != nil {
		return "", err
	}
	if len(bytes.TrimSpace(md)) == 0 {
		return "", errNoReadableContent
	}
	return string(md), nil
}

// decodeHTML returns body as UTF-8. The encoding comes from a byte-order mark,
// then the Content-Type charset, then a <meta> prescan, defaulting to UTF-8
// (bytes passed through unchanged, as before). A declared label capy cannot
// decode fails rather than producing garbled text that would look readable.
func decodeHTML(body, contentType string) (string, error) {
	label, offset, source := "", 0, "byte-order mark"
	switch {
	case strings.HasPrefix(body, "\xEF\xBB\xBF"):
		label, offset = "utf-8", 3
	case strings.HasPrefix(body, "\xFE\xFF"):
		label, offset = "utf-16be", 2
	case strings.HasPrefix(body, "\xFF\xFE"):
		label, offset = "utf-16le", 2
	}
	if label == "" {
		if _, params, err := mime.ParseMediaType(contentType); err == nil {
			label, source = params["charset"], "Content-Type"
		}
	}
	fromMeta := false
	if label == "" {
		label = metaCharsetLabel(body[:min(len(body), htmlCharsetPrescanLen)])
		fromMeta, source = label != "", "<meta>"
	}
	if label == "" {
		return body, nil
	}

	enc, name := charset.Lookup(label)
	// The WHATWG "replacement" encoding decodes any input to a single U+FFFD,
	// which would pass the visible-text check.
	if enc == nil || name == "replacement" {
		// The label is remote input, possibly from inside a private region the
		// prescan cannot see; name only where it was declared.
		return "", fmt.Errorf("unsupported HTML charset declared in %s", source)
	}
	// A <meta> declaration readable as ASCII cannot be UTF-16 (WHATWG prescan).
	if fromMeta && strings.HasPrefix(name, "utf-16") {
		name = "utf-8"
	}
	if name == "utf-8" {
		return body[offset:], nil
	}

	// Single-byte encodings can triple in size as UTF-8; keep the decoded
	// document within the same bound as the admitted response body.
	r := transform.NewReader(strings.NewReader(body[offset:]), enc.NewDecoder())
	out, err := io.ReadAll(io.LimitReader(r, fetchMaxBody+1))
	if err != nil {
		return "", fmt.Errorf("decoding HTML from %s: %w", name, err)
	}
	if len(out) > fetchMaxBody {
		return "", fmt.Errorf("decoded HTML exceeds %d bytes", fetchMaxBody)
	}
	return string(out), nil
}

// metaCharsetLabel returns the charset declared by the first <meta charset> or
// <meta http-equiv="content-type" content="...; charset=..."> in head.
func metaCharsetLabel(head string) string {
	z := html.NewTokenizer(strings.NewReader(head))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			if tok.DataAtom != atom.Meta {
				continue
			}
			var httpEquiv, content string
			for _, a := range tok.Attr {
				switch a.Key {
				case "charset":
					if v := strings.TrimSpace(a.Val); v != "" {
						return v
					}
				case "http-equiv":
					httpEquiv = strings.ToLower(strings.TrimSpace(a.Val))
				case "content":
					content = a.Val
				}
			}
			if httpEquiv == "content-type" {
				if label := charsetFromMetaContent(content); label != "" {
					return label
				}
			}
		}
	}
}

// charsetFromMetaContent extracts the label from a http-equiv content value
// following the WHATWG algorithm, which, unlike MIME parameter parsing, accepts
// single-quoted and unterminated values.
func charsetFromMetaContent(content string) string {
	// ASCII-only folding keeps byte offsets aligned with content.
	lower := []byte(content)
	for k, b := range lower {
		if 'A' <= b && b <= 'Z' {
			lower[k] = b + ('a' - 'A')
		}
	}
	for i := 0; ; {
		j := bytes.Index(lower[i:], []byte("charset"))
		if j < 0 {
			return ""
		}
		rest := strings.TrimLeft(content[i+j+len("charset"):], "\t\n\f\r ")
		if !strings.HasPrefix(rest, "=") {
			i += j + len("charset")
			continue
		}
		rest = strings.TrimLeft(rest[1:], "\t\n\f\r ")
		if rest == "" {
			return ""
		}
		if q := rest[0]; q == '"' || q == '\'' {
			end := strings.IndexByte(rest[1:], q)
			if end < 0 {
				return ""
			}
			return rest[1 : 1+end]
		}
		if end := strings.IndexAny(rest, "\t\n\f\r ;"); end >= 0 {
			return rest[:end]
		}
		return rest
	}
}

// documentTitle returns the whitespace-collapsed text of the head's <title>.
// Secrets and private blocks are redacted here because the title is inserted
// as text, which the renderer escapes beyond the store sanitizer's reach.
func documentTitle(doc *html.Node) string {
	head := findElement(doc, atom.Head)
	if head == nil {
		return ""
	}
	// Only the head's own <title>; one inside inert <template> content is not
	// the document title.
	var t *html.Node
	for c := head.FirstChild; c != nil && t == nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.Title {
			t = c
		}
	}
	if t == nil {
		return ""
	}
	var sb strings.Builder
	for c := t.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return sanitize.StripSecrets(strings.Join(strings.Fields(sb.String()), " "))
}

// unreadableElements are removed wherever they occur: document metadata (the
// title is captured before pruning), executable, template and embedded content,
// navigation chrome and form controls.
var unreadableElements = map[atom.Atom]bool{
	atom.Head: true,
	// The renderer drops body <meta>/<link> and joins their neighbors; remove
	// them before redactTree coalesces text so it sees the joined string.
	atom.Meta: true, atom.Link: true,
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true,
	atom.Nav: true, atom.Footer: true,
	atom.Button: true, atom.Input: true, atom.Select: true, atom.Textarea: true, atom.Datalist: true,
}

// privateTagRe finds a <private> start, self-closing or end tag. The required
// delimiter keeps custom elements such as <private-note> out of the pass.
var privateTagRe = regexp.MustCompile(`(?i)</?private[\t\n\f\r />]`)

// renderedAttrs are attributes whose values can reach the Markdown output.
// Link destinations are absent: rewriteReferences validates each original href
// and redacts the resolved destination it keeps.
var renderedAttrs = map[string]bool{"src": true, "alt": true, "title": true}

// codeElements are rendered as their text only, so links inside them never
// reach the output and are left alone.
var codeElements = map[atom.Atom]bool{
	atom.Pre: true, atom.Code: true, atom.Var: true, atom.Samp: true, atom.Kbd: true, atom.Tt: true,
}

// svgMetadata are SVG children that describe a graphic or define content shown
// only by reference: an icon's title or sprite definitions must not make an
// empty page readable. Labels displayed through <use> are dropped with them.
var svgMetadata = map[string]bool{"title": true, "desc": true, "metadata": true, "defs": true, "symbol": true}

// redactPrivateRegions replaces each outermost <private> element (any
// attributes, nested regions included) with the sanitizer's placeholder. It
// works on the token stream, so raw-text elements such as <script> keep their
// literal contents and the parser cannot move private content out of its
// wrapper. A self-closing <private/> opens a region, as in HTML, which ignores
// the slash on non-void elements. A region still open at the end of input or
// a stray end tag fails conversion with errPrivateBoundary: the standalone
// tokenizer can stay in raw-text mode where the parser would not, and indexing
// a truncated page or leaked private text would replace a good copy.
func redactPrivateRegions(src string) (string, error) {
	if !privateTagRe.MatchString(src) {
		return src, nil
	}
	// The standalone tokenizer cannot follow the parser's SVG CDATA handling
	// (it may read a section as a bogus comment or as raw text), so with
	// private markup present a CDATA section makes every boundary uncertain.
	// Check the original input, before any boundary evidence is rewritten.
	if strings.Contains(strings.ToLower(src), "<![cdata[") {
		return "", errPrivateBoundary
	}
	z := html.NewTokenizer(strings.NewReader(src))
	var out strings.Builder
	out.Grow(len(src))
	depth := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if depth > 0 {
				return "", errPrivateBoundary
			}
			return out.String(), nil
		}
		isTag := tt == html.StartTagToken || tt == html.EndTagToken || tt == html.SelfClosingTagToken
		if !isTag {
			if depth == 0 {
				out.Write(z.Raw())
			}
			continue
		}
		// TagName may rewrite the buffer Raw returns; copy the original first.
		raw := append([]byte(nil), z.Raw()...)
		if name, _ := z.TagName(); string(name) == "private" {
			switch {
			case tt != html.EndTagToken:
				if depth == 0 {
					out.WriteString(sanitize.RedactedPrivate)
				}
				depth++
			case depth > 0:
				depth--
			default:
				return "", errPrivateBoundary
			}
			continue
		}
		if depth == 0 {
			out.Write(raw)
		}
	}
}

// pruneUnreadable removes hidden and boilerplate subtrees from doc. The walk is
// iterative so document depth never grows the Go stack.
func pruneUnreadable(doc *html.Node) {
	type frame struct {
		n         *html.Node
		inArticle bool
	}
	stack := []frame{{doc, false}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for c := f.n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode {
				drop := unreadableElements[c.DataAtom] ||
					(c.DataAtom == atom.Header && !f.inArticle) ||
					(c.Namespace == "svg" && svgMetadata[c.Data]) ||
					hiddenElement(c)
				if drop {
					f.n.RemoveChild(c)
				} else {
					stack = append(stack, frame{c, f.inArticle || c.DataAtom == atom.Article})
				}
			}
			c = next
		}
	}
}

// redactTree applies redact to the text and rendered attributes under root,
// before Markdown escaping (which turns "ghp_..." into "ghp\_...") hides
// tokens from the store's sanitizer. Its secret-pattern run follows all
// removals and link/image rewriting, so text they made adjacent is coalesced
// and redacted as one string — the renderer would join it anyway.
func redactTree(root *html.Node, redact func(string) string) {
	walkElements(root, func(n *html.Node) bool {
		for i, a := range n.Attr {
			if renderedAttrs[a.Key] {
				n.Attr[i].Val = redact(a.Val)
			}
		}
		coalesceText(n)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				c.Data = redact(c.Data)
			}
		}
		return true
	})
}

// coalesceText removes n's comment children and merges each resulting run of
// adjacent text nodes in one linear pass, so a secret split by a comment or a
// pruned element is redacted as one string, and the renderer never re-merges
// many small nodes.
func coalesceText(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.CommentNode {
			n.RemoveChild(c)
		}
		c = next
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.TextNode || c.NextSibling == nil || c.NextSibling.Type != html.TextNode {
			continue
		}
		var sb strings.Builder
		sb.WriteString(c.Data)
		for c.NextSibling != nil && c.NextSibling.Type == html.TextNode {
			sb.WriteString(c.NextSibling.Data)
			n.RemoveChild(c.NextSibling)
		}
		c.Data = sb.String()
	}
}

// hiddenElement reports markup-level visibility signals. Stylesheet rules are
// out of reach without a CSS engine. The hidden attribute does not hide
// content a reader reveals by interaction: hidden="until-found" (find-in-page)
// and inactive tab panels, which documentation sites use for per-tool or
// per-language examples.
func hiddenElement(n *html.Node) bool {
	tabPanel := false
	for _, a := range n.Attr {
		if a.Key == "role" && slices.Contains(strings.Fields(strings.ToLower(a.Val)), "tabpanel") {
			tabPanel = true
		}
	}
	for _, a := range n.Attr {
		val := strings.ToLower(strings.TrimSpace(a.Val))
		switch a.Key {
		case "hidden":
			if val != "until-found" && !tabPanel {
				return true
			}
		case "aria-hidden":
			if val == "true" {
				return true
			}
		case "role":
			for _, r := range strings.Fields(val) {
				if r == "navigation" {
					return true
				}
			}
		case "style":
			if inlineStyleHides(val) {
				return true
			}
		}
	}
	return false
}

func inlineStyleHides(style string) bool {
	for decl := range strings.SplitSeq(style, ";") {
		prop, value, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "!important"))
		switch strings.TrimSpace(prop) {
		case "display":
			if value == "none" {
				return true
			}
		case "visibility":
			if value == "hidden" || value == "collapse" {
				return true
			}
		}
	}
	return false
}

// selectReadableRoot returns the pruned subtree to render, or nil when the
// document has no visible text.
func selectReadableRoot(doc *html.Node) *html.Node {
	var root *html.Node
	walkElements(doc, func(n *html.Node) bool {
		if root != nil {
			return false
		}
		if n.DataAtom == atom.Main {
			// An empty main cannot contain a visible one; do not rescan it.
			if hasVisibleText(n) {
				root = n
			}
			return false
		}
		return true
	})
	if root == nil {
		// The parser always creates <body>; its absence means pruning removed it
		// (or a frameset document), leaving nothing readable.
		if root = findElement(doc, atom.Body); root == nil || !hasVisibleText(root) {
			return nil
		}
	}

	var articles []*html.Node
	walkElements(root, func(n *html.Node) bool {
		if n == root || n.DataAtom != atom.Article {
			return true
		}
		if hasVisibleText(n) {
			articles = append(articles, n)
		}
		return false // nested articles (e.g. comments) belong to this one
	})
	if len(articles) == 1 {
		return articles[0]
	}
	return root
}

// rewriteReferences prepares the selected root's links and images for
// rendering without dereferencing either. Each image becomes its alt text,
// never a URL or data URI. Images go first, so a link around an image keeps
// the alt text as its label. A link without visible text is removed. Any
// other link stays a link only when linkResolver accepts its destination;
// otherwise it is replaced by its children. Destinations count against
// maxSourceBytes: links are the only step that can make the output larger
// than the body, so exceeding the limit fails before rendering, like any
// conversion failure. Links inside code elements are left alone because the
// renderer emits only their text, and a link inside a link is unwrapped, as
// the renderer would.
func rewriteReferences(root *html.Node, finalURL *url.URL, maxSourceBytes int) error {
	var images, links, nested []*html.Node
	type frame struct {
		n              *html.Node
		inLink, inCode bool
	}
	stack := []frame{{n: root}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for c := f.n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			next := frame{c, f.inLink, f.inCode || codeElements[c.DataAtom]}
			switch {
			case c.DataAtom == atom.Img:
				images = append(images, c)
			case c.DataAtom != atom.A || next.inCode:
				// Nothing to rewrite: not a link, or a link rendered as code text.
			case f.inLink:
				// Only foreign content such as SVG nests links.
				nested = append(nested, c)
			default:
				links = append(links, c)
				next.inLink = true
			}
			stack = append(stack, next)
		}
	}
	// Unwrapping nested links first keeps the remaining link subtrees disjoint,
	// so removing textless links below stays linear.
	for _, a := range nested {
		unwrapElement(a)
	}
	for _, img := range images {
		if alt := attrValue(img, "alt"); strings.TrimSpace(alt) != "" {
			img.Parent.InsertBefore(&html.Node{Type: html.TextNode, Data: alt}, img)
		}
		img.Parent.RemoveChild(img)
	}

	visible := visibleElements(root)
	resolver := newLinkResolver(finalURL)
	total := 0
	for _, a := range links {
		if !visible[a] {
			removeTextless(a)
			continue
		}
		dest, cost, ok := resolver.resolve(attrValue(a, "href"))
		total += cost
		if total > maxSourceBytes {
			// total stops at the first destination over the limit, so name the
			// limit rather than a misleading partial size.
			return fmt.Errorf("resolved link destinations exceed the %d-byte source limit (configure store.max_source_bytes to adjust)", maxSourceBytes)
		}
		if !ok {
			unwrapElement(a)
			continue
		}
		a.Attr = slices.DeleteFunc(a.Attr, func(attr html.Attribute) bool { return attr.Key == "href" })
		a.Attr = append(a.Attr, html.Attribute{Key: "href", Val: dest})
	}
	return nil
}

// linkResolver resolves a page's link destinations against its final URL (a
// document <base> is ignored). A destination renders as a link only when it
// is http(s) with a host, no userinfo — inherited from the final URL included
// — and no recognized secret in its path or fragment; query fields holding a
// secret are redacted (see redactQuery), because fragment links inherit the
// final URL's query.
//
// The final URL is untrusted and may be long, so each link's work stays
// within what it is charged: fragment links extend the page destination
// computed once, root-relative and absolute references resolve against the
// origin without reading the base path, path-relative references against the
// base directory, and query-only references against the full base. The last
// two are charged at least the base path they read, kept or dropped; that
// path is part of their destination unless dot segments shorten it.
type linkResolver struct {
	base     *url.URL
	origin   *url.URL // scheme, userinfo and host of base
	dir      *url.URL // origin plus the base path up to its last "/"
	pathCost int      // escaped length of the base path
	queries  map[string]redactedQuery
	page     *redactedDestination // base without its fragment, computed once
}

type redactedQuery struct {
	query string
	ok    bool
}

type redactedDestination struct {
	dest string
	ok   bool
	// openPrivate: the decoded original ends inside an unclosed private
	// region, which a fragment could close after query redaction erased the
	// opener.
	openPrivate bool
}

func newLinkResolver(base *url.URL) *linkResolver {
	origin := &url.URL{Scheme: base.Scheme, User: base.User, Host: base.Host}
	escaped := base.EscapedPath()
	dir := *origin
	dir.RawPath = escaped[:strings.LastIndex(escaped, "/")+1]
	// EscapedPath output always unescapes.
	dir.Path, _ = url.PathUnescape(dir.RawPath)
	return &linkResolver{
		base:     base,
		origin:   origin,
		dir:      &dir,
		pathCost: len(escaped),
		queries:  map[string]redactedQuery{},
	}
}

// resolve returns the destination to render for href, its cost against the
// budget and whether it may be a link. A missing or blank href is not a link.
func (r *linkResolver) resolve(href string) (dest string, cost int, ok bool) {
	// Browsers drop surrounding whitespace and embedded tabs and newlines.
	href = strings.Map(func(c rune) rune {
		if c == '\t' || c == '\n' || c == '\r' {
			return -1
		}
		return c
	}, strings.TrimSpace(href))
	if href == "" {
		return "", 0, false
	}
	ref, err := url.Parse(href)
	if err != nil {
		return "", 0, false
	}

	// A reference without scheme, authority, path or query inherits the page,
	// query included: "#frag", "#", or a broken "//".
	if ref.Scheme == "" && ref.Host == "" && ref.User == nil && ref.EscapedPath() == "" && ref.RawQuery == "" && !ref.ForceQuery {
		page := r.pageDestination()
		if !page.ok {
			return "", 0, false
		}
		// A bare "#" means the page itself.
		if ref.Fragment == "" {
			return page.dest, len(page.dest), true
		}
		// Secret patterns cannot cross "#"; a private region can, if the page
		// left one open.
		frag := ref.EscapedFragment()
		decoded := lenientUnescape(frag, false)
		closes := page.openPrivate && strings.Contains(asciiLower(decoded), "</private>")
		if hasSecret(frag) || hasSecret(decoded) || closes {
			return "", 0, false
		}
		dest = page.dest + "#" + frag
		return dest, len(dest), true
	}

	// Classify by the escaped path, as ResolveReference does: "%2Fx" is
	// path-relative.
	base := r.origin
	if ref.Scheme == "" && ref.Host == "" {
		switch escaped := ref.EscapedPath(); {
		case escaped == "":
			base, cost = r.base, r.pathCost
		case !strings.HasPrefix(escaped, "/"):
			base, cost = r.dir, len(r.dir.RawPath)
		}
	}
	dest, ok = r.check(base.ResolveReference(ref))
	if !ok {
		return "", cost, false
	}
	return dest, max(cost, len(dest)), true
}

// pageDestination returns the checked final URL without its fragment.
func (r *linkResolver) pageDestination() redactedDestination {
	if r.page == nil {
		u := *r.base
		u.Fragment, u.RawFragment = "", ""
		// ASCII folding, as the sanitizer's (?i) pattern matches these letters.
		lower := asciiLower(lenientUnescape(u.String(), false))
		open := strings.LastIndex(lower, "<private>") > strings.LastIndex(lower, "</private>")
		dest, ok := r.check(&u)
		r.page = &redactedDestination{dest: dest, ok: ok, openPrivate: open}
	}
	return *r.page
}

// check applies the link rules to a resolved URL, which it may modify, and
// returns the destination to render.
func (r *linkResolver) check(u *url.URL) (string, bool) {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	// A private region can span path, query and fragment; check before query
	// redaction can erase one of its delimiters.
	if hasPrivate(u.String()) {
		return "", false
	}
	// Paths and fragments rarely carry credentials; one there, raw or decoded,
	// drops the destination.
	rest := *u
	rest.RawQuery, rest.ForceQuery = "", false
	if s := rest.String(); hasSecret(s) || hasSecret(lenientUnescape(s, false)) {
		return "", false
	}
	if u.RawQuery != "" {
		q, cached := r.queries[u.RawQuery]
		if !cached {
			q.query, q.ok = redactQuery(u.RawQuery)
			r.queries[u.RawQuery] = q
		}
		if !q.ok {
			return "", false
		}
		u.RawQuery = q.query
	}
	return u.String(), true
}

// redactQuery replaces each query field holding a recognized secret, raw or
// decoded with query semantics, by the placeholder. Replacing the whole field
// leaves no tail behind a partial match, and decoding catches a credential
// whose escapes (%2F, '+') the renderer re-encodes rather than decodes. A
// private region anywhere in the decoded query — it can span fields, and a
// field replacement could remove one of its delimiters — rejects the query.
func redactQuery(rawQuery string) (string, bool) {
	if decoded := lenientUnescape(rawQuery, true); sanitize.StripPrivate(decoded) != decoded {
		return "", false
	}
	fields := strings.Split(rawQuery, "&")
	for i, f := range fields {
		if hasSecret(f) || hasSecret(lenientUnescape(f, true)) {
			fields[i] = sanitize.RedactedSecret
		}
	}
	return strings.Join(fields, "&"), true
}

func hasSecret(s string) bool {
	return sanitize.StripSecrets(s) != s
}

// asciiLower folds ASCII letters only. strings.ToLower would also fold
// characters such as U+0130 to "i", which the sanitizer's pattern does not.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// hasPrivate reports a private region in the percent-decoded URL.
func hasPrivate(rawURL string) bool {
	decoded := lenientUnescape(rawURL, false)
	return sanitize.StripPrivate(decoded) != decoded
}

// lenientUnescape decodes valid %XX escapes, and '+' as a space when
// plusSpace, leaving malformed escapes as they are, so one stray '%' cannot
// hide the rest of a string from the secret patterns.
func lenientUnescape(s string, plusSpace bool) string {
	if !strings.ContainsAny(s, "%+") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		if c == '+' && plusSpace {
			c = ' '
		}
		b.WriteByte(c)
	}
	return b.String()
}

// visibleElements returns the elements under root that contain visible text,
// in one pass: each text node marks its ancestors until one is already marked.
func visibleElements(root *html.Node) map[*html.Node]bool {
	visible := map[*html.Node]bool{}
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if isVisibleText(n) {
			for p := n.Parent; p != nil && !visible[p]; p = p.Parent {
				visible[p] = true
				if p == root {
					break
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, c)
		}
	}
	return visible
}

// attrValue returns the value of n's first attribute named key, or "".
func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// unwrapElement replaces n with its children.
func unwrapElement(n *html.Node) {
	for c := n.FirstChild; c != nil; c = n.FirstChild {
		n.RemoveChild(c)
		n.Parent.InsertBefore(c, n)
	}
	n.Parent.RemoveChild(n)
}

// removeTextless removes an element without visible text, including any
// private-region placeholder in it. A space stays where it held whitespace,
// so the words around it remain apart.
func removeTextless(n *html.Node) {
	hasSpace := false
	walkNodes(n, func(c *html.Node) {
		hasSpace = hasSpace || (c.Type == html.TextNode && strings.IndexFunc(c.Data, unicode.IsSpace) >= 0)
	})
	if hasSpace {
		n.Parent.InsertBefore(&html.Node{Type: html.TextNode, Data: " "}, n)
	}
	n.Parent.RemoveChild(n)
}

// walkElements visits element nodes under root (root included) in document
// order; visit returns false to skip a node's descendants.
func walkElements(root *html.Node, visit func(*html.Node) bool) {
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Type == html.ElementNode && !visit(n) {
			continue
		}
		// Push children in reverse so the first child is visited first.
		for c := n.LastChild; c != nil; c = c.PrevSibling {
			stack = append(stack, c)
		}
	}
}

// findElementNamed finds an element by tag name, for elements without an atom.
func findElementNamed(root *html.Node, name string) *html.Node {
	var found *html.Node
	walkElements(root, func(n *html.Node) bool {
		if found != nil {
			return false
		}
		if n.Data == name {
			found = n
			return false
		}
		return true
	})
	return found
}

func findElement(root *html.Node, a atom.Atom) *html.Node {
	var found *html.Node
	walkElements(root, func(n *html.Node) bool {
		if found != nil {
			return false
		}
		if n.DataAtom == a {
			found = n
			return false
		}
		return true
	})
	return found
}

// hasVisibleText reports whether the (already pruned) subtree contains any
// non-whitespace text. Private-region placeholders do not count: redacted
// content alone is nothing to index and must not win root selection.
func hasVisibleText(root *html.Node) bool {
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if isVisibleText(n) {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, c)
		}
	}
	return false
}

// isVisibleText reports a text node with non-whitespace content other than
// private-region placeholders.
func isVisibleText(n *html.Node) bool {
	return n.Type == html.TextNode && strings.TrimSpace(strings.ReplaceAll(n.Data, sanitize.RedactedPrivate, "")) != ""
}

// walkNodes visits every node under root (root included).
func walkNodes(root *html.Node, visit func(*html.Node)) {
	stack := []*html.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		visit(n)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, c)
		}
	}
}
