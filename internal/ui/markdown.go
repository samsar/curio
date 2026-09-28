package ui

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MaxRenderedMarkdown is how much of a document's markdown its page
// renders: 1 MiB, which the formatting budgets (budget.go) keep to about a
// second and a few hundred megabytes allocated at worst; a typical article
// of that size formats in tens of milliseconds. The whole text stays at
// GET /v1/documents/{id}/content.
const MaxRenderedMarkdown = 1 << 20

// CutMarkdown keeps the first MaxRenderedMarkdown bytes of src, cut after
// the last whole line in them, and reports whether it cut anything. A
// first line longer than that is cut before the character the limit falls
// in.
func CutMarkdown(src []byte) ([]byte, bool) {
	if len(src) <= MaxRenderedMarkdown {
		return src, false
	}
	if i := bytes.LastIndexByte(src[:MaxRenderedMarkdown], '\n'); i >= 0 {
		return src[:i+1], true
	}
	cut := MaxRenderedMarkdown
	for cut > MaxRenderedMarkdown-utf8.UTFMax && !utf8.RuneStart(src[cut]) {
		cut--
	}
	return src[:cut], true
}

// Text is a document's markdown, rendered for its page.
type Text struct {
	// HTML is the formatted text, empty when it is Unformatted.
	HTML template.HTML
	// Unformatted says which formatting budget the text is over, when it
	// is (budget.go): the page then shows Source, the markdown as stored.
	Unformatted string
	Source      string
	// RemoteImages counts the text's http and https images, shown or not:
	// the page offers to load them when there are any.
	RemoteImages int
}

// markdown renders stored markdown into HTML that is safe to put in a
// page, in three steps. goldmark parses it without raw HTML (no
// html.WithUnsafe: raw HTML is left out). A transformer resolves every
// link and image against the document's URL, keeps only links to http and
// https URLs with a host and to mailto ones, and turns images into links,
// or into https images when asked. bluemonday then sanitizes the HTML
// against an allow-list. It is safe for concurrent use.
type markdown struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

func newMarkdown() *markdown {
	policy := bluemonday.UGCPolicy()
	policy.AllowRelativeURLs(false)
	policy.AllowURLSchemes("mailto")
	// A second net under the transformer: an http URL without a host
	// resolves against the page, which is the daemon.
	hasHost := func(u *url.URL) bool { return u.Host != "" }
	policy.AllowURLSchemeWithCustomPolicy("http", hasHost)
	policy.AllowURLSchemeWithCustomPolicy("https", hasHost)
	policy.RequireNoReferrerOnLinks(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)
	policy.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
	return &markdown{
		md: goldmark.New(
			goldmark.WithExtensions(extension.GFM),
			goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(linkTransformer{}, 100))),
		),
		policy: policy,
	}
}

// render renders src, a document's stored markdown whose URL is base. With
// images, https images are shown; otherwise, and for any other image, the
// image is its alt text, linking to it when it is an http or https URL. A
// text over a formatting budget comes back Unformatted.
func (m *markdown) render(src []byte, base string, images bool) (Text, error) {
	html, remote, err := m.format(src, base, images)
	if over, ok := errors.AsType[*budgetError](err); ok {
		return Text{Unformatted: over.reason, Source: string(src)}, nil
	}
	if err != nil {
		return Text{}, err
	}
	return Text{HTML: html, RemoteImages: remote}, nil
}

// format formats src into sanitized HTML and counts its remote images. A
// text over a formatting budget is a *budgetError: the shape of its
// markdown is checked before goldmark parses it, its links' length once
// they are resolved, before goldmark writes them.
func (m *markdown) format(src []byte, base string, images bool) (template.HTML, int, error) {
	if err := checkShape(src); err != nil {
		return "", 0, err
	}
	st := &linkState{images: images}
	if u, err := url.Parse(base); err == nil && u.IsAbs() {
		st.base = u
	}
	pc := parser.NewContext()
	pc.Set(linkStateKey, st)
	doc := m.md.Parser().Parse(text.NewReader(src), parser.WithContext(pc))
	if st.over != nil {
		return "", 0, st.over
	}
	var buf bytes.Buffer
	if err := m.md.Renderer().Render(&buf, src, doc); err != nil {
		return "", 0, fmt.Errorf("render markdown: %w", err)
	}
	// The one place curio trusts HTML it didn't write: the sanitizer's
	// output (TestTrustedHTMLOnlyFromTheSanitizer).
	return template.HTML(m.policy.SanitizeBytes(buf.Bytes())), st.remote, nil //nolint:gosec // G203: bluemonday's allow-list output, from goldmark without raw HTML
}

// linkStateKey carries a render's linkState to linkTransformer.
var linkStateKey = parser.NewContextKey()

// linkState is one render's settings and findings.
type linkState struct {
	base      *url.URL // the document's URL; nil leaves nothing to resolve against
	images    bool     // show https images
	remote    int      // http and https images seen
	linkBytes int      // the kept links' and images' destinations and titles
	over      error    // errLinkBytes once linkBytes passes maxLinkBytes
}

// resolve resolves dest against the document's URL. It reports false for
// a destination that doesn't parse or resolve, or that a page may not
// link to (linkable). The destination's character references are
// resolved first, as the renderer resolves them when it writes the href:
// &#106;avascript: is a javascript: URL.
func (s *linkState) resolve(dest []byte) (*url.URL, bool) {
	u, err := url.Parse(string(util.ResolveEntityNames(util.ResolveNumericReferences(dest))))
	if err != nil {
		return nil, false
	}
	if s.base != nil {
		u = s.base.ResolveReference(u)
	}
	return u, linkable(u)
}

// keep counts a kept link's or image's destination and title against
// maxLinkBytes, and reports whether the render is still within it.
func (s *linkState) keep(dest, title []byte) bool {
	s.linkBytes += len(dest) + len(title)
	if s.linkBytes > maxLinkBytes {
		s.over = errLinkBytes
	}
	return s.over == nil
}

// linkable reports whether a stored page's link may point at u: an http
// or https URL with a host, or a mailto one. A browser resolves an http
// URL without a host, http:/ui/ say, against the page, which is the
// daemon.
func linkable(u *url.URL) bool {
	switch u.Scheme {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return true
	}
	return false
}

// linkTransformer points every link and image at the original site, so no
// relative URL resolves against the daemon, drops links it can't allow,
// and replaces images a page doesn't show. It stops once the links are
// over maxLinkBytes: the render is then shown unformatted.
type linkTransformer struct{}

func (linkTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	st, ok := pc.Get(linkStateKey).(*linkState)
	if !ok {
		return // a parse without format's context: nothing to resolve against
	}
	var links []*ast.Link
	var autoLinks []*ast.AutoLink
	var images []*ast.Image
	// The walk only collects: changing the tree under it would skip nodes.
	walk(doc, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.Link:
			links = append(links, n)
		case *ast.AutoLink:
			autoLinks = append(autoLinks, n)
		case *ast.Image:
			images = append(images, n)
		}
	})
	source := reader.Source()
	for _, l := range links {
		u, ok := st.resolve(l.Destination)
		if !ok {
			unwrap(l)
			continue
		}
		if l.Destination = []byte(u.String()); !st.keep(l.Destination, l.Title) {
			return
		}
	}
	for _, a := range autoLinks {
		if !autoLinkable(a, source) {
			a.Parent().ReplaceChild(a.Parent(), a, ast.NewString(a.Label(source)))
		}
	}
	for _, img := range images {
		if !st.replaceImage(img, source) {
			return
		}
	}
}

// autoLinkable reports whether a page may keep autolink a: <https://...>,
// or a bare URL or address the linkify extension found. Its URL is
// absolute by definition, and written as it is stored, escaped.
func autoLinkable(a *ast.AutoLink, source []byte) bool {
	if a.AutoLinkType == ast.AutoLinkEmail {
		return true // written as a mailto: link
	}
	u, err := url.Parse(string(util.URLEscape(a.URL(source), false)))
	return err == nil && linkable(u)
}

// replaceImage leaves img an https image when the render shows images, and
// otherwise replaces it with its alt text: a link to the image when its
// URL is http or https and it isn't inside a link already, plain text
// otherwise. It reports whether the render is still within maxLinkBytes.
func (s *linkState) replaceImage(img *ast.Image, source []byte) bool {
	u, ok := s.resolve(img.Destination)
	remote := ok && (u.Scheme == "http" || u.Scheme == "https")
	if remote {
		s.remote++
	}
	if remote && s.images && u.Scheme == "https" {
		img.Destination = []byte(u.String())
		img.SetAttributeString("loading", []byte("lazy"))
		return s.keep(img.Destination, img.Title)
	}
	label := ast.NewString([]byte(imageLabel(img, source)))
	if !remote || insideLink(img) {
		img.Parent().ReplaceChild(img.Parent(), img, label)
		return true
	}
	link := ast.NewLink()
	link.Destination = []byte(u.String())
	link.AppendChild(link, label)
	img.Parent().ReplaceChild(img.Parent(), img, link)
	return s.keep(link.Destination, nil)
}

// imageLabel is the text an image is replaced with: its alt text, or a
// placeholder when it has none.
func imageLabel(img *ast.Image, source []byte) string {
	var alt strings.Builder
	walk(img, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.Text:
			alt.Write(n.Segment.Value(source))
			if n.SoftLineBreak() {
				alt.WriteByte(' ')
			}
		case *ast.String:
			alt.Write(n.Value)
		}
	})
	if s := strings.TrimSpace(alt.String()); s != "" {
		return "[image: " + s + "]"
	}
	return "[image]"
}

// walk calls fn on root and every node under it, parents first. ast.Walk
// fails only with an error its callback returns, and this one returns none.
func walk(root ast.Node, fn func(ast.Node)) {
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			fn(n)
		}
		return ast.WalkContinue, nil
	})
}

// insideLink reports whether n has a link among its ancestors.
func insideLink(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.(type) {
		case *ast.Link, *ast.AutoLink:
			return true
		}
	}
	return false
}

// unwrap replaces n with its children.
func unwrap(n ast.Node) {
	parent := n.Parent()
	for c := n.FirstChild(); c != nil; {
		next := c.NextSibling()
		parent.InsertBefore(parent, n, c)
		c = next
	}
	parent.RemoveChild(parent, n)
}
