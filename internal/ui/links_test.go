package ui

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinks(t *testing.T) {
	assert.Equal(t, "/ui/documents/a%2Fb%3F%23", documentHref("a/b?#"), "one path segment")
	assert.Equal(t, "/ui/documents/x?images=1", documentImagesHref("x"))
	assert.Equal(t, "/ui/interests/%20x", interestHref(" x"))
	assert.Equal(t, "/ui/library?state=failed", stateHref("failed"))
	assert.Equal(t, "/ui/library?cause=anti_bot", causeHref("anti_bot"))
	assert.Equal(t, "/ui/library", libraryHref(LibraryFilters{}, ""))
	assert.Equal(t, "/ui/", navHref(NavSearch))
	assert.Equal(t, "/ui/status", navHref(NavStatus))
	assert.Empty(t, navHref("elsewhere"))

	// Every value round-trips through the query, however odd.
	f := LibraryFilters{State: "failed", ContentType: "pdf", Host: "a&b=c.example", Folder: "/100% Reading/#1",
		Cause: "anti_bot", Limit: 7}
	href := libraryHref(f, "cursor+/=")
	u, err := url.Parse(href)
	require.NoError(t, err)
	assert.Equal(t, "/ui/library", u.Path)
	assert.Equal(t, url.Values{"state": {"failed"}, "content_type": {"pdf"}, "host": {"a&b=c.example"},
		"folder": {"/100% Reading/#1"}, "cause": {"anti_bot"}, "cursor": {"cursor+/="}, "limit": {"7"}}, u.Query(),
		"the next page keeps every filter")

	more := parseHref(t, libraryMoreHref(f, "cursor+/=", 150))
	assert.Equal(t, "/ui/library", more.Path)
	want := u.Query()
	want.Set("shown", "150")
	assert.Equal(t, want, more.Query(), "load more asks for the next page with the rows shown")
	assert.NotContains(t, href, "shown=", "the plain link doesn't")
}

func parseHref(t *testing.T, href string) *url.URL {
	t.Helper()
	u, err := url.Parse(href)
	require.NoError(t, err)
	return u
}

// TestSearchHref: a search's query and type round-trip, however odd; a
// blank query is left out, and the home without a type is /ui/.
func TestSearchHref(t *testing.T) {
	const q = "a&b=c #1 <x>"
	for _, contentType := range []string{"", "article", "repo", "video", "pdf"} {
		u := parseHref(t, searchHref(q, contentType))
		assert.Equal(t, "/ui/", u.Path, contentType)
		want := url.Values{"q": {q}}
		if contentType != "" {
			want.Set("content_type", contentType)
		}
		assert.Equal(t, want, u.Query(), contentType)
	}
	assert.Equal(t, "/ui/", searchHref("", ""))
	assert.Equal(t, "/ui/", searchHref("  ", ""), "a blank query is the home")
	assert.Equal(t, "/ui/?content_type=pdf", searchHref(" ", "pdf"))
	assert.Equal(t, "/ui/?content_type=pdf&q=kafka", searchHref("kafka", "pdf"))
}

// TestStateTabHref: a state tab keeps the page's other filters and its
// size, drops the cursor, and sets or drops the state.
func TestStateTabHref(t *testing.T) {
	f := LibraryFilters{State: "failed", ContentType: "pdf", Host: "a&b=c.example", Folder: "/100% Reading/#1",
		Cause: "anti_bot", Limit: 7}
	u := parseHref(t, stateTabHref(f, "dead"))
	assert.Equal(t, url.Values{"state": {"dead"}, "content_type": {"pdf"}, "host": {"a&b=c.example"},
		"folder": {"/100% Reading/#1"}, "cause": {"anti_bot"}, "limit": {"7"}}, u.Query())
	u = parseHref(t, stateTabHref(f, ""))
	assert.Equal(t, url.Values{"content_type": {"pdf"}, "host": {"a&b=c.example"},
		"folder": {"/100% Reading/#1"}, "cause": {"anti_bot"}, "limit": {"7"}}, u.Query(), "All drops the state")
	assert.Equal(t, "/ui/library", stateTabHref(LibraryFilters{State: "failed"}, ""))
}

// TestNavItems: the navigation in its order, each item linking where an
// error page's Retry does.
func TestNavItems(t *testing.T) {
	items := navItems()
	navs := make([]Nav, 0, len(items))
	for _, item := range items {
		navs = append(navs, item.Nav)
		assert.Equal(t, navHref(item.Nav), item.Href, item.Nav)
		assert.NotEmpty(t, item.Label, item.Nav)
	}
	assert.Equal(t, []Nav{NavSearch, NavLibrary, NavInterests, NavStatus}, navs)
	assert.Equal(t, []string{"/ui/", "/ui/library", "/ui/interests", "/ui/status"},
		[]string{items[0].Href, items[1].Href, items[2].Href, items[3].Href})
	assert.Equal(t, []string{"search", "library", "sparkles", "activity"},
		[]string{items[0].Icon, items[1].Icon, items[2].Icon, items[3].Icon})
	assert.Len(t, navHrefs, len(items), "every page with an address is in the navigation")
}

func TestOutbound(t *testing.T) {
	for _, u := range []string{"https://example.com/a", "http://example.com", "HTTPS://Example.com/x"} {
		assert.Equal(t, u, outbound(u))
	}
	for _, u := range []string{"javascript:alert(1)", "data:text/html,x", "/ui/documents/x", "//example.com/a",
		"mailto:a@example.com", "https://", "%zz"} {
		assert.Empty(t, outbound(u), u)
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second:                "under a minute",
		12*time.Minute + 20*time.Second: "12m",
		3*time.Hour + 20*time.Minute:    "3h 20m",
		50 * time.Hour:                  "2d 2h",
	} {
		assert.Equal(t, want, duration(d), d)
	}
	assert.Equal(t, "never", when(time.Time{}))
}
