package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"regexp"
	"slices"
	"strings"

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
func convertHTMLToMarkdown(body, contentType string) (string, error) {
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
	redactTree(doc)
	root := selectReadableRoot(doc)
	if root == nil {
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
var renderedAttrs = map[string]bool{"href": true, "src": true, "alt": true, "title": true}

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

// redactTree applies the secret patterns to the pruned tree, before Markdown
// escaping (which turns "ghp_..." into "ghp\_...") hides tokens from the
// store's sanitizer. It runs after all removals, so text that pruning made
// adjacent is coalesced and redacted as one string — the renderer would join it
// anyway.
func redactTree(doc *html.Node) {
	walkElements(doc, func(n *html.Node) bool {
		for i, a := range n.Attr {
			if renderedAttrs[a.Key] {
				n.Attr[i].Val = sanitize.StripSecrets(a.Val)
			}
		}
		coalesceText(n)
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				c.Data = sanitize.StripSecrets(c.Data)
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
		if n.Type == html.TextNode && strings.TrimSpace(strings.ReplaceAll(n.Data, sanitize.RedactedPrivate, "")) != "" {
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			stack = append(stack, c)
		}
	}
	return false
}
