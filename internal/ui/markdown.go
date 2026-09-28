package ui

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strings"
	"time"
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
// renders: 1 MiB. A typical article of that size formats in tens of
// milliseconds; whatever the text, the formatting budgets and limits
// (budget.go) stop a render within about two seconds and a gigabyte
// allocated. The whole text stays at GET /v1/documents/{id}/content.
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
	// Unformatted says which formatting budget or limit the text is over,
	// when it is (budget.go): the page then shows Source, the markdown as
	// stored.
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
// against an allow-list. Each render is held to the formatting budgets
// and limits (budget.go). It is safe for concurrent use.
type markdown struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
	// A render's limits: maxFormatTime and maxFormatAlloc, which only
	// tests change.
	timeLimit  time.Duration
	allocLimit uint64
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
			// GFM without Linkify, whose regexps scan from every space at a
			// cost the inline budget doesn't charge: an address written
			// out without link markup stays text.
			goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList),
			goldmark.WithParserOptions(
				parser.WithInlineParsers(util.Prioritized(stopParser{}, stopParserPriority)),
				parser.WithASTTransformers(util.Prioritized(linkTransformer{}, 100)),
			),
		),
		policy:     policy,
		timeLimit:  maxFormatTime,
		allocLimit: maxFormatAlloc,
	}
}

// render renders src, a document's stored markdown whose URL is base. With
// images, https images are shown; otherwise, and for any other image, the
// image is its alt text, linking to it when it is an http or https URL. A
// text over a formatting budget or limit comes back Unformatted.
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
// text over a formatting budget or limit is a *budgetError: the shape of
// its markdown is checked before goldmark parses it, its links' length
// before they are resolved, and the limits all along. A check of the
// limits that fails panics with its *budgetError to unwind goldmark, and
// is recovered here; any other panic goes on.
func (m *markdown) format(src []byte, base string, images bool) (sanitized template.HTML, remote int, err error) {
	if err := checkShape(src); err != nil {
		return "", 0, err
	}
	limits := startLimits(m.timeLimit, m.allocLimit)
	defer limits.stop()
	defer func() {
		if v := recover(); v != nil {
			over, ok := v.(*budgetError)
			if !ok {
				panic(v)
			}
			sanitized, remote, err = "", 0, over
		}
	}()
	st := &linkState{images: images, limits: limits}
	if u, err := url.Parse(base); err == nil && u.IsAbs() {
		st.base = u
	}
	pc := parser.NewContext()
	pc.Set(linkStateKey, st)
	doc := m.md.Parser().Parse(stopReader{text.NewReader(src), limits}, parser.WithContext(pc))
	if st.over != nil {
		return "", 0, st.over
	}
	w := &htmlWriter{limits: limits}
	if err := m.md.Renderer().Render(w, src, doc); err != nil {
		return "", 0, fmt.Errorf("render markdown: %w", err)
	}
	limits.check()
	// The one place curio trusts HTML it didn't write: the sanitizer's
	// output (TestTrustedHTMLOnlyFromTheSanitizer).
	return template.HTML(m.policy.SanitizeBytes(w.buf.Bytes())), st.remote, nil //nolint:gosec // G203: bluemonday's allow-list output, from goldmark without raw HTML
}

// stopParserPriority runs stopParser before every other inline parser:
// goldmark tries them in ascending priority, and the task list's checkbox
// parser is at 0.
const stopParserPriority = -1

// stopParser checks a render's limits at every character an inline parser
// starts at, where a scan to the end of the paragraph can begin. It parses
// nothing: goldmark goes on to the next parser.
type stopParser struct{}

func (stopParser) Trigger() []byte { return []byte("![]`<*_~") }

func (stopParser) Parse(_ ast.Node, _ text.Reader, pc parser.Context) ast.Node {
	if st, ok := pc.Get(linkStateKey).(*linkState); ok {
		st.limits.check()
	}
	return nil
}

// stopReader is the reader goldmark's block parse reads a text through: it
// checks the render's limits at every line the parse peeks at, once for
// every open block it visits there.
type stopReader struct {
	text.Reader
	limits *formatLimits
}

func (r stopReader) PeekLine() ([]byte, text.Segment) {
	r.limits.check()
	return r.Reader.PeekLine()
}

// htmlWriter holds the HTML goldmark writes, up to maxHTMLBytes, and
// checks the render's limits at every write, which goldmark buffers into
// 4 KiB. Past maxHTMLBytes it panics rather than failing the write:
// goldmark ignores a failed write and renders on, escaping every URL left
// for a writer that takes none of it. Its only method is Write, so bufio
// never writes around it.
type htmlWriter struct {
	buf    bytes.Buffer
	limits *formatLimits
}

func (w *htmlWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > maxHTMLBytes {
		panic(errHTMLBytes)
	}
	w.limits.check()
	return w.buf.Write(p)
}

// linkStateKey carries a render's linkState to linkTransformer.
var linkStateKey = parser.NewContextKey()

// linkState is one render's settings, limits and findings.
type linkState struct {
	base      *url.URL // the document's URL; nil leaves nothing to resolve against
	images    bool     // show https images
	limits    *formatLimits
	remote    int   // http and https images seen
	linkBytes int   // the links' and images' destinations and titles, as stored
	over      error // errLinkBytes once linkBytes passes maxLinkBytes
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

// charge counts a link's or image's destination and title, as stored,
// against maxLinkBytes, before anything resolves it, and reports whether
// the render is still within it. It checks the render's limits too.
func (s *linkState) charge(dest, title []byte) bool {
	s.limits.check()
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
	links, autoLinks, images := collectLinks(doc)
	source := reader.Source()
	for _, l := range links {
		if !st.charge(l.Destination, l.Title) {
			return
		}
		u, ok := st.resolve(l.Destination)
		if !ok {
			unwrap(l)
			continue
		}
		l.Destination = []byte(u.String())
	}
	for _, a := range autoLinks {
		st.limits.check()
		if !autoLinkable(a, source) {
			a.Parent().ReplaceChild(a.Parent(), a, ast.NewString(a.Label(source)))
		}
	}
	for _, img := range images {
		if !st.charge(img.Destination, img.Title) {
			return
		}
		st.replaceImage(img, source)
	}
}

// collectLinks returns the links, autolinks and images under doc, except
// those inside an image: all an image holds is written as its text, its
// alt or the label replacing it. The walk only collects: changing the
// tree under it would skip nodes. ast.Walk fails only with an error its
// callback returns, and this one returns none.
func collectLinks(doc ast.Node) (links []*ast.Link, autoLinks []*ast.AutoLink, images []*ast.Image) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			links = append(links, n)
		case *ast.AutoLink:
			autoLinks = append(autoLinks, n)
		case *ast.Image:
			images = append(images, n)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return links, autoLinks, images
}

// autoLinkable reports whether a page may keep autolink a, <https://...>
// or <someone@example.com>. Its URL is absolute by definition, and
// written as it is stored, escaped.
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
// otherwise.
func (s *linkState) replaceImage(img *ast.Image, source []byte) {
	u, ok := s.resolve(img.Destination)
	remote := ok && (u.Scheme == "http" || u.Scheme == "https")
	if remote {
		s.remote++
	}
	if remote && s.images && u.Scheme == "https" {
		img.Destination = []byte(u.String())
		img.SetAttributeString("loading", []byte("lazy"))
		return
	}
	label := ast.NewString([]byte(imageLabel(img, source)))
	if !remote || insideLink(img) {
		img.Parent().ReplaceChild(img.Parent(), img, label)
		return
	}
	link := ast.NewLink()
	link.Destination = []byte(u.String())
	link.AppendChild(link, label)
	img.Parent().ReplaceChild(img.Parent(), img, link)
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
