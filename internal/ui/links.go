package ui

import (
	"net/url"
	"strconv"
	"strings"
)

// Every link a page makes is built here, whole, from the values it names:
// html/template leaves a path segment unescaped, and escapes a query
// string built in the template a second time.

// documentHref is a document's page.
func documentHref(id string) string { return "/ui/documents/" + url.PathEscape(id) }

// documentImagesHref is a document's page with its remote images.
func documentImagesHref(id string) string { return documentHref(id) + "?images=1" }

// interestHref is an interest's page.
func interestHref(id string) string { return "/ui/interests/" + url.PathEscape(id) }

// searchHref is the search page for q, limited to contentType when it is
// set; a blank q is the search home.
func searchHref(q, contentType string) string {
	v := url.Values{}
	if strings.TrimSpace(q) != "" {
		v.Set("q", q)
	}
	if contentType != "" {
		v.Set("content_type", contentType)
	}
	if len(v) == 0 {
		return "/ui/"
	}
	return "/ui/?" + v.Encode()
}

// libraryHref is the Library under f, from the page after cursor; an empty
// cursor is the first page.
func libraryHref(f LibraryFilters, cursor string) string {
	q := libraryQuery(f, cursor)
	if len(q) == 0 {
		return "/ui/library"
	}
	return "/ui/library?" + q.Encode()
}

// libraryMoreHref is libraryHref(f, cursor) as htmx's "load more" asks for
// it: htmx appends the next page's rows to the shown rows the page holds,
// and shown lets that page's Showing line count them too. A plain link to
// the next page shows its own rows alone, and never carries shown.
func libraryMoreHref(f LibraryFilters, cursor string, shown int) string {
	q := libraryQuery(f, cursor)
	q.Set("shown", strconv.Itoa(shown))
	return "/ui/library?" + q.Encode()
}

// libraryQuery is the query of the Library under f from the page after
// cursor: the filters that filter something, and the page size when it
// isn't the default.
func libraryQuery(f LibraryFilters, cursor string) url.Values {
	q := url.Values{}
	for name, v := range map[string]string{"state": f.State, "content_type": f.ContentType, "host": f.Host,
		"folder": f.Folder, "cause": f.Cause, "cursor": cursor} {
		if v != "" {
			q.Set(name, v)
		}
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	return q
}

// stateHref is the Library of the documents in state.
func stateHref(state string) string { return libraryHref(LibraryFilters{State: state}, "") }

// stateTabHref is the first page of the Library under f with its state
// filter set to state, or dropped for "".
func stateTabHref(f LibraryFilters, state string) string {
	f.State = state
	return libraryHref(f, "")
}

// causeHref is the Library of the documents that failed for cause.
func causeHref(cause string) string { return libraryHref(LibraryFilters{Cause: cause}, "") }

// navHrefs are the navigation's pages.
var navHrefs = map[Nav]string{
	NavSearch:    "/ui/",
	NavLibrary:   "/ui/library",
	NavInterests: "/ui/interests",
	NavStatus:    "/ui/status",
}

// navHref is nav's page, or "" for none.
func navHref(nav Nav) string { return navHrefs[nav] }

// NavItem is one item of the pages' navigation: its label and icon, and
// the page it links to.
type NavItem struct {
	Nav   Nav
	Label string
	Icon  string
	Href  string
}

// navItems is the pages' navigation, in its order, each item linking to
// its navHrefs page, as an error page's Retry does.
func navItems() []NavItem {
	items := []NavItem{
		{Nav: NavSearch, Label: "Search", Icon: "search"},
		{Nav: NavLibrary, Label: "Library", Icon: "library"},
		{Nav: NavInterests, Label: "Interests", Icon: "sparkles"},
		{Nav: NavStatus, Label: "Status", Icon: "activity"},
	}
	for i := range items {
		items[i].Href = navHref(items[i].Nav)
	}
	return items
}

// outbound is u when it is an http or https URL, which a page may link
// to, and "" otherwise: a stored URL is never trusted to be one.
func outbound(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return u
}
