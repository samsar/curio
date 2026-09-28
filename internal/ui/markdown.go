package ui

import (
	"bytes"
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
// renders: 1 MiB. A 2 MiB article takes about 90 ms to render and sanitize,
// but a pathological 2 MiB table about half a second, growing to 11 MiB of
// HTML. The whole text stays at GET /v1/documents/{id}/content.
const MaxRenderedMarkdown = 1 << 20

// CutMarkdown keeps the first MaxRenderedMarkdown bytes of src, cut after
// the last whole line in them, and reports whether it cut anything. A
// first line longer than that is cut at a character boundary instead.
func CutMarkdown(src []byte) ([]byte, bool) {
	if len(src) <= MaxRenderedMarkdown {
		return src, false
	}
	src = src[:MaxRenderedMarkdown]
	if i := bytes.LastIndexByte(src, '\n'); i >= 0 {
		return src[:i+1], true
	}
	for len(src) > 0 && !utf8.Valid(src) {
		src = src[:len(src)-1]
	}
	return src, true
}

// Text is a document's markdown, rendered for its page.
type Text struct {
	HTML template.HTML
	// RemoteImages counts the text's http and https images, shown or not:
	// the page offers to load them when there are any.
	RemoteImages int
}

// markdown renders stored markdown into HTML that is safe to put in a
// page, in three steps. goldmark parses it without raw HTML (no
// html.WithUnsafe: raw HTML is left out). A transformer resolves every
// link and image against the document's URL, keeps only http, https and
// mailto links, and turns images into links, or into https images when
// asked. bluemonday then sanitizes the HTML against an allow-list. It is
// safe for concurrent use.
type markdown struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

func newMarkdown() *markdown {
	policy := bluemonday.UGCPolicy()
	policy.AllowURLSchemes("http", "https", "mailto")
	policy.AllowRelativeURLs(false)
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
// image is its alt text, linking to it when it is an http or https URL.
func (m *markdown) render(src []byte, base string, images bool) (Text, error) {
	st := &linkState{images: images}
	if u, err := url.Parse(base); err == nil && u.IsAbs() {
		st.base = u
	}
	pc := parser.NewContext()
	pc.Set(linkStateKey, st)
	var buf bytes.Buffer
	if err := m.md.Convert(src, &buf, parser.WithContext(pc)); err != nil {
		return Text{}, fmt.Errorf("render markdown: %w", err)
	}
	// The one place curio trusts HTML it didn't write: the sanitizer's
	// output (TestTrustedHTMLOnlyFromTheSanitizer).
	return Text{
		HTML:         template.HTML(m.policy.SanitizeBytes(buf.Bytes())), //nolint:gosec // G203: bluemonday's allow-list output, from goldmark without raw HTML
		RemoteImages: st.remote,
	}, nil
}

// linkStateKey carries a render's linkState to linkTransformer.
var linkStateKey = parser.NewContextKey()

// linkState is one render's settings and findings.
type linkState struct {
	base   *url.URL // the document's URL; nil leaves nothing to resolve against
	images bool     // show https images
	remote int      // http and https images seen
}

// resolve resolves dest against the document's URL. It reports false for
// a destination that doesn't parse, can't be resolved, or has a scheme
// other than http, https and mailto. The destination's character
// references are resolved first, as the renderer resolves them when it
// writes the href: &#106;avascript: is a javascript: URL.
func (s *linkState) resolve(dest []byte) (*url.URL, bool) {
	u, err := url.Parse(string(util.ResolveEntityNames(util.ResolveNumericReferences(dest))))
	if err != nil {
		return nil, false
	}
	if s.base != nil {
		u = s.base.ResolveReference(u)
	}
	switch u.Scheme {
	case "http", "https", "mailto":
		return u, true
	}
	return nil, false
}

// linkTransformer points every link and image at the original site, so no
// relative URL resolves against the daemon, drops links it can't allow,
// and replaces images a page doesn't show.
type linkTransformer struct{}

func (linkTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	st, ok := pc.Get(linkStateKey).(*linkState)
	if !ok {
		return // a Convert without render's context: nothing to resolve against
	}
	var links []*ast.Link
	var images []*ast.Image
	// The walk only collects: changing the tree under it would skip nodes.
	walk(doc, func(n ast.Node) {
		switch n := n.(type) {
		case *ast.Link:
			links = append(links, n)
		case *ast.Image:
			images = append(images, n)
		}
	})
	for _, l := range links {
		if u, ok := st.resolve(l.Destination); ok {
			l.Destination = []byte(u.String())
		} else {
			unwrap(l)
		}
	}
	for _, img := range images {
		st.replaceImage(img, reader.Source())
	}
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
	var repl ast.Node = label
	if remote && !insideLink(img) {
		link := ast.NewLink()
		link.Destination = []byte(u.String())
		link.AppendChild(link, label)
		repl = link
	}
	img.Parent().ReplaceChild(img.Parent(), img, repl)
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
