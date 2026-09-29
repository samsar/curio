// Package uitest checks that the dashboard's HTML is inert: that nothing a
// page renders from stored content can run script, style the page, or
// point a link or form somewhere curio doesn't mean it to. It is test
// support only: depguard keeps production code from importing it.
package uitest

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Problems lists what makes page, an HTML document or fragment, not
// inert, one line each; none means it is. It fails on:
//
//   - a script other than an empty one whose src is under /ui/static/;
//   - a style element or attribute;
//   - an on* event-handler attribute, or an hx-on* one (data- prefixed too);
//   - an iframe, frame, object, embed or base element;
//   - an href or src whose scheme is not http, https or mailto, other than
//     a path under /ui/ or, for an href, a fragment naming an element of
//     the page, and an action or formaction other than such a path;
//   - an http(s) link whose rel lacks noopener or noreferrer.
func Problems(page string) []string {
	root, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return []string{"parse: " + err.Error()}
	}
	ids := map[string]bool{}
	for n := range root.Descendants() {
		if n.Type == html.ElementNode && attr(n, "id") != "" {
			ids[attr(n, "id")] = true
		}
	}
	var problems []string
	for n := range root.Descendants() {
		if n.Type == html.ElementNode {
			problems = append(problems, elementProblems(n, ids)...)
		}
	}
	return problems
}

// AssertInert fails t for each problem Problems finds in page.
func AssertInert(t testing.TB, page string) {
	t.Helper()
	for _, p := range Problems(page) {
		t.Errorf("not inert: %s", p)
	}
}

// forbidden are the elements that load or run something whatever their
// attributes.
var forbidden = []string{"iframe", "frame", "object", "embed", "base"}

// elementProblems lists what makes element n not inert; ids are the ids
// of the page's elements, which a fragment link may name.
func elementProblems(n *html.Node, ids map[string]bool) []string {
	var problems []string
	switch {
	case n.Data == "script":
		src := attr(n, "src")
		if n.FirstChild != nil || !strings.HasPrefix(src, "/ui/static/") {
			problems = append(problems, "<script src="+strconv.Quote(src)+">: only an empty script from /ui/static/ may run")
		}
	case n.Data == "style":
		problems = append(problems, "<style> element")
	case slices.Contains(forbidden, n.Data):
		problems = append(problems, "<"+n.Data+"> element")
	}
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		name := strings.TrimPrefix(key, "data-")
		switch {
		case key == "style":
			problems = append(problems, "style attribute on <"+n.Data+">")
		case strings.HasPrefix(key, "on"), strings.HasPrefix(name, "hx-on"):
			problems = append(problems, key+" attribute on <"+n.Data+">")
		case key == "href" && pageFragment(a.Val, ids):
			// A link within the page, such as the skip link.
		case key == "href", key == "src":
			if !allowedURL(a.Val) {
				problems = append(problems, key+"="+strconv.Quote(a.Val)+" on <"+n.Data+">")
			}
		case key == "action", key == "formaction":
			// Stricter than the rule for links, as the CSP's form-action is.
			if !strings.HasPrefix(strings.TrimSpace(a.Val), "/ui/") {
				problems = append(problems, key+"="+strconv.Quote(a.Val)+" on <"+n.Data+">: forms submit to /ui/ only")
			}
		}
	}
	if n.Data == "a" && isOutbound(attr(n, "href")) {
		rel := strings.Fields(attr(n, "rel"))
		if !slices.Contains(rel, "noopener") || !slices.Contains(rel, "noreferrer") {
			problems = append(problems, "link to "+strconv.Quote(attr(n, "href"))+" without rel noopener noreferrer")
		}
	}
	return problems
}

// allowedURL reports whether a page may point at u: a path under /ui/, or
// an http, https or mailto URL.
func allowedURL(u string) bool {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "/ui/") {
		return true
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch parsed.Scheme {
	case "http", "https":
		return parsed.Host != ""
	case "mailto":
		return true
	}
	return false
}

// refusedURL is what html/template writes in place of a URL it won't
// put in a page.
const refusedURL = "ZgotmplZ"

// pageFragment reports whether href is a fragment naming an element of
// the page, ids, such as a skip link's "#main". html/template's refusal,
// "#ZgotmplZ", never is.
func pageFragment(href string, ids map[string]bool) bool {
	id, ok := strings.CutPrefix(href, "#")
	return ok && id != refusedURL && ids[id]
}

// isOutbound reports whether href leaves curio for an http(s) site.
func isOutbound(href string) bool {
	parsed, err := url.Parse(strings.TrimSpace(href))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}
