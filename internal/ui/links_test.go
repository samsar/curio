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

	for href, want := range map[string]url.Values{
		stateHref("a&b=c #1"): {"state": {"a&b=c #1"}},
		causeHref("a&b=c #1"): {"cause": {"a&b=c #1"}},
	} {
		u := parseHref(t, href)
		assert.Equal(t, "/ui/library", u.Path, href)
		assert.Equal(t, want, u.Query(), "a code the page doesn't know round-trips: %s", href)
	}

	more := parseHref(t, libraryMoreHref(f, "cursor+/=", 150))
	assert.Equal(t, "/ui/library", more.Path)
	want := u.Query()
	want.Set("shown", "150")
	assert.Equal(t, want, more.Query(), "load more asks for the next page with the rows shown")
	assert.NotContains(t, href, "shown=", "the plain link doesn't")
}

// TestLibraryOrderLinks: the Date saved order round-trips through every
// Library link, and the default order is left out; clearing the filters
// keeps the order and drops the rest.
func TestLibraryOrderLinks(t *testing.T) {
	saved := LibraryFilters{Order: OrderSaved, State: "failed", Host: "a&b=c.example", Folder: "/100% Reading/#1",
		Limit: 7}
	for name, href := range map[string]string{
		"next page": libraryHref(saved, "cursor+/="),
		"load more": libraryMoreHref(saved, "cursor+/=", 150),
		"state tab": stateTabHref(saved, "dead"),
	} {
		assert.Equal(t, OrderSaved, parseHref(t, href).Query().Get("order"), name)
	}
	assert.Equal(t, "/ui/library?order=saved", clearFiltersHref(saved))
	assert.Equal(t, "/ui/library?order=saved", libraryHref(LibraryFilters{Order: OrderSaved}, ""))

	updated := saved
	updated.Order = ""
	for name, href := range map[string]string{
		"next page":     libraryHref(updated, "c"),
		"load more":     libraryMoreHref(updated, "c", 1),
		"state tab":     stateTabHref(updated, "dead"),
		"clear filters": clearFiltersHref(updated),
	} {
		assert.NotContains(t, parseHref(t, href).Query(), "order", "%s: the default order is left out", name)
	}
	assert.Equal(t, "/ui/library", clearFiltersHref(updated))
}

func parseHref(t *testing.T, href string) *url.URL {
	t.Helper()
	u, err := url.Parse(href)
	require.NoError(t, err)
	return u
}

// TestFailuresLinks: the Failures tab and its poll; a cause's card, which
// Status's rows lead to, by the id the card carries, its fragment escaped
// however odd the cause; and a host tag's Library of one cause on one
// host, both round-tripping.
func TestFailuresLinks(t *testing.T) {
	assert.Equal(t, "/ui/failures", failuresHref())
	assert.Equal(t, "/ui/failures?poll=causes", failuresPollHref())
	assert.Equal(t, "cause-anti_bot", causeCardID("anti_bot"))
	assert.Equal(t, failuresHref()+"#"+causeCardID("anti_bot"), failureCauseHref("anti_bot"))

	const odd = `a&b=c #1 <x>"`
	u := parseHref(t, failureCauseHref(odd))
	assert.Equal(t, "/ui/failures", u.Path)
	assert.Empty(t, u.RawQuery)
	assert.Equal(t, causeCardID(odd), u.Fragment, "the fragment round-trips")

	u = parseHref(t, causeHostHref("anti_bot", "a&b=c.example:8080"))
	assert.Equal(t, "/ui/library", u.Path)
	assert.Equal(t, url.Values{"cause": {"anti_bot"}, "host": {"a&b=c.example:8080"}}, u.Query())
	assert.Equal(t, "/ui/library?cause=dead_link&host=gone.example", causeHostHref("dead_link", "gone.example"),
		"no state: the list's cause filter takes dead documents")
}

// TestClearCauseHref: clearing the cause keeps every other filter, the
// order and the page size, and drops the cause and the cursor, in either
// order.
func TestClearCauseHref(t *testing.T) {
	for _, order := range []string{"", OrderSaved} {
		f := LibraryFilters{Order: order, State: "failed", ContentType: "pdf", Host: "a&b=c.example",
			Folder: "/100% Reading/#1", Cause: "a&cause=b #1", Limit: 7}
		want := url.Values{"state": {"failed"}, "content_type": {"pdf"}, "host": {"a&b=c.example"},
			"folder": {"/100% Reading/#1"}, "limit": {"7"}}
		if order != "" {
			want.Set("order", order)
		}
		u := parseHref(t, clearCauseHref(f))
		assert.Equal(t, "/ui/library", u.Path, order)
		assert.Equal(t, want, u.Query(), order)
	}
	assert.Equal(t, "/ui/library", clearCauseHref(LibraryFilters{Cause: "anti_bot"}))
	assert.Equal(t, "/ui/library?order=saved", clearCauseHref(LibraryFilters{Order: OrderSaved, Cause: "anti_bot"}))
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

// TestPollHrefs: each poll asks its own page for its live regions; a
// document's carries the page's baseline, which round-trips exactly
// whatever its values, and an Interests poll the run the page shows.
func TestPollHrefs(t *testing.T) {
	assert.Equal(t, "/ui/status?poll=live", statusPollHref(PollLive))
	assert.Equal(t, "/ui/status?poll=health", statusPollHref(PollHealth))

	updated := time.Date(2026, 9, 29, 7, 2, 3, 456_000_000, time.FixedZone("PDT", -7*3600))
	for _, b := range []DocumentBaseline{
		{Updated: updated, Extraction: "e1"},
		{Updated: updated, Extraction: "a&b=c #1/%2F"},
		{Updated: updated},
	} {
		u := parseHref(t, documentPollHref("a/b?#", b))
		assert.Equal(t, "/ui/documents/a%2Fb%3F%23", u.EscapedPath(), "one path segment")
		q := u.Query()
		assert.Equal(t, PollJobs, q.Get(PollParam))
		assert.Equal(t, "2026-09-29T14:02:03.456Z", q.Get("updated"), "UTC, to the millisecond")
		got, err := ParseDocumentBaseline(q)
		require.NoError(t, err)
		assert.True(t, got.Updated.Equal(b.Updated), "the time round-trips")
		assert.Equal(t, b.Extraction, got.Extraction)
	}
	for _, bad := range []string{"", "yesterday", "2026-09-29", "2026-09-29 14:02:03"} {
		_, err := ParseDocumentBaseline(url.Values{"updated": {bad}, "extraction": {"e1"}})
		assert.Error(t, err, "updated %q", bad)
	}

	for _, run := range []string{"r1", "", "a&run=b #1"} {
		u := parseHref(t, interestsPollHref(run))
		assert.Equal(t, "/ui/interests", u.Path)
		assert.Equal(t, PollRebuild, u.Query().Get(PollParam))
		assert.Equal(t, run, ShownRun(u.Query()), "the run round-trips")
	}
}

// TestPageHrefs: a page of the Interests names the run it shows, and the
// first page no number; a page of an interest's members keeps the ID one
// path segment. Every value round-trips, however odd.
func TestPageHrefs(t *testing.T) {
	assert.Equal(t, "/ui/interests", interestsPageHref(1, ""))
	assert.Equal(t, "/ui/interests?run=r", interestsPageHref(1, "r"))
	assert.Equal(t, "/ui/interests?page=2&run=r", interestsPageHref(2, "r"))
	assert.Equal(t, "/ui/interests?page=2", interestsPageHref(2, ""))
	for _, run := range []string{"a&page=9&run=b #1", `"><script>`, "100%/x?"} {
		u := parseHref(t, interestsPageHref(3, run))
		assert.Equal(t, "/ui/interests", u.Path)
		assert.Equal(t, url.Values{PageParam: {"3"}, "run": {run}}, u.Query(), "the run round-trips")
		assert.Equal(t, run, ShownRun(u.Query()))
	}

	assert.Equal(t, "/ui/interests/i1", interestPageHref("i1", 1))
	assert.Equal(t, "/ui/interests/i1?page=2", interestPageHref("i1", 2))
	for _, id := range []string{"a/b?page=9#x", `"><script>`, "../status"} {
		u := parseHref(t, interestPageHref(id, 4))
		assert.Equal(t, "/ui/interests/"+url.PathEscape(id), u.EscapedPath(), "one path segment")
		assert.Equal(t, url.Values{PageParam: {"4"}}, u.Query())
	}
}
