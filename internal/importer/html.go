package importer

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ParseHTML reads a Netscape Bookmark File Format document and returns
// the bookmarks it contains. This is the format every browser and most
// read-later tools export to.
//
// Shape:
//
//	<DL>
//	  <DT><H3 ADD_DATE="...">Folder</H3>
//	  <DL>
//	    <DT><A HREF="..." ADD_DATE="..." TAGS="a,b">Title</A>
//	    ...
//	  </DL>
//	  <DT><A HREF="...">Top-level bookmark</A>
//	</DL>
//
// The H1 page heading and any META tags in the HEADER are ignored.
// HR separators and arbitrary whitespace are tolerated. Unparseable
// dates are dropped (SavedAt stays zero); unparseable HREFs are
// filtered out at insert time via Indexable.
func ParseHTML(r io.Reader) ([]ParsedBookmark, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("html: parse: %w", err)
	}

	var out []ParsedBookmark
	walkNode(doc, []string{}, &out)
	if len(out) == 0 {
		return nil, ErrEmpty
	}
	return out, nil
}

// walkNode is a recursive pre-order walk.
//
// HTML5 doesn't auto-close <DT> or <H3> on a following <DL> the way the
// Netscape format implicitly assumes, so the parsed tree nests aggressively:
// a <DL> that visually belongs after an <H3>+</H3> ends up as a *sibling*
// of that <H3>, both children of the <DT> that contained them. We handle
// this by walking generically and looking inside each <DT> for an <H3> +
// any nested <DL>.
//
// Rules:
//   - <a>:  emit one bookmark using the current folderStack.
//   - <dt>: if it contains an <h3>, the H3 text is the folder name for
//     any nested <dl>; recurse into all children, pushing that name
//     onto the stack only for the <dl> path.
//   - default: recurse into children unchanged.
func walkNode(n *html.Node, folderStack []string, out *[]ParsedBookmark) {
	if n == nil {
		return
	}
	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNode(c, folderStack, out)
		}
		return
	}

	switch n.Data {
	case "a":
		emitAnchor(n, folderStack, out)
		// Don't recurse into A children; the title text was captured.
		return

	case "dt":
		// Look for an H3 sibling-child to act as the folder name for any DL.
		var folderName string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "h3" {
				folderName = strings.TrimSpace(textContent(c))
				break
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				walkNode(c, folderStack, out)
				continue
			}
			switch c.Data {
			case "h3":
				// Already consumed for folderName; nothing to do.
			case "dl":
				walkNode(c, pushFolder(folderStack, folderName), out)
			default:
				walkNode(c, folderStack, out)
			}
		}
		return

	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkNode(c, folderStack, out)
		}
	}
}

func emitAnchor(n *html.Node, folderStack []string, out *[]ParsedBookmark) {
	href := attr(n, "href")
	if href == "" {
		return
	}
	*out = append(*out, ParsedBookmark{
		URL:        canonicalURL(href),
		Title:      strings.TrimSpace(textContent(n)),
		FolderPath: joinFolderPath(folderStack),
		SavedAt:    parseDateAttr(attr(n, "add_date")),
		Tags:       splitTagsAttr(attr(n, "tags")),
	})
}

// textContent returns the concatenated text under n.
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// epochUnits infers a Unix timestamp's unit from its magnitude, finest unit
// first: nanoseconds, microseconds (Firefox), milliseconds (several
// read-later exporters), seconds. Each threshold is about 1973 in the finer
// unit and about year 5138 in the coarser one, so no plausible bookmark date
// is ambiguous.
var epochUnits = []struct {
	min       float64
	perSecond int64
}{
	{1e17, 1e9},
	{1e14, 1e6},
	{1e11, 1e3},
	{0, 1},
}

func epochPerSecond(v float64) int64 {
	for _, u := range epochUnits {
		if v >= u.min {
			return u.perSecond
		}
	}
	return 1
}

// parseDateAttr reads an ADD_DATE attribute: a Unix timestamp in the unit
// epochUnits infers, written as an integer (parsed exactly) or a decimal,
// possibly in scientific notation (sub-second precision kept). Returns the
// zero time for anything unparseable, non-positive, or non-finite, so
// callers can omit a SavedAt downstream.
func parseDateAttr(s string) time.Time {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n <= 0 {
			return time.Time{}
		}
		per := epochPerSecond(float64(n))
		return time.Unix(n/per, n%per*(1e9/per)).UTC()
	}
	f, err := strconv.ParseFloat(s, 64)
	// Written so NaN, which fails every comparison, is rejected; the upper
	// bound keeps the seconds within int64.
	if err != nil || !(f > 0 && f < math.MaxInt64) {
		return time.Time{}
	}
	sec, frac := math.Modf(f / float64(epochPerSecond(f)))
	return time.Unix(int64(sec), int64(math.Round(frac*1e9))).UTC()
}

func splitTagsAttr(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// joinFolderPath builds "/A/B/C" from a stack of folder names. Slashes
// in folder names are replaced with hyphens so they don't break path
// semantics downstream.
func joinFolderPath(stack []string) string {
	if len(stack) == 0 {
		return ""
	}
	parts := make([]string, len(stack))
	for i, s := range stack {
		parts[i] = strings.ReplaceAll(strings.TrimSpace(s), "/", "-")
	}
	return "/" + strings.Join(parts, "/")
}
