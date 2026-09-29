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
	assert.Equal(t, "/ui/library", libraryHref(LibraryFilters{}, ""))
	assert.Equal(t, "/ui/", navHref(NavOverview))
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
	assert.Equal(t, []Nav{NavOverview, NavSearch, NavLibrary, NavInterests}, navs)
	assert.Equal(t, []string{"/ui/", "/ui/search", "/ui/library", "/ui/interests"},
		[]string{items[0].Href, items[1].Href, items[2].Href, items[3].Href})
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
