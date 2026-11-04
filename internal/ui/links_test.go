package ui

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinks(t *testing.T) {
	assert.Equal(t, "/ui/documents/a%2Fb%3F%23", documentHref("a/b?#"), "one path segment")
	assert.Equal(t, "/ui/documents/x?images=1", documentImagesHref("x"))
	assert.Equal(t, "/ui/interests/%20x", interestHref(" x"))
	assert.Equal(t, "/ui/library?order=updated&state=failed", stateHref("failed"), "documents, which it counts")
	assert.Equal(t, "/ui/library?cause=anti_bot&order=updated", causeHref("anti_bot"), "documents, which it counts")
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
		stateHref("a&b=c #1"): {"state": {"a&b=c #1"}, "order": {OrderUpdated}},
		causeHref("a&b=c #1"): {"cause": {"a&b=c #1"}, "order": {OrderUpdated}},
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

// TestLibraryOrderLinks: the Last updated order round-trips through every
// Library link, and the default, Recently saved, is left out, however it
// is named; clearing the filters keeps the order and drops the rest.
func TestLibraryOrderLinks(t *testing.T) {
	updated := LibraryFilters{Order: OrderUpdated, State: "failed", Host: "a&b=c.example",
		Folder: "/100% Reading/#1", Limit: 7}
	for name, href := range map[string]string{
		"next page": libraryHref(updated, "cursor+/="),
		"load more": libraryMoreHref(updated, "cursor+/=", 150),
		"state tab": stateTabHref(updated, "dead"),
	} {
		assert.Equal(t, OrderUpdated, parseHref(t, href).Query().Get("order"), name)
	}
	assert.Equal(t, "/ui/library?order=updated", clearFiltersHref(updated))
	assert.Equal(t, "/ui/library?order=updated", libraryHref(LibraryFilters{Order: OrderUpdated}, ""))

	for _, order := range []string{"", OrderSaved} {
		saved := updated
		saved.Order = order
		assert.True(t, saved.Saved(), order)
		for name, href := range map[string]string{
			"next page":     libraryHref(saved, "c"),
			"load more":     libraryMoreHref(saved, "c", 1),
			"state tab":     stateTabHref(saved, "dead"),
			"clear filters": clearFiltersHref(saved),
		} {
			assert.NotContains(t, parseHref(t, href).Query(), "order", "%s: the default order is left out", name)
		}
		assert.Equal(t, "/ui/library", clearFiltersHref(saved))
	}
	assert.False(t, updated.Saved())
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
	assert.Equal(t, url.Values{"cause": {"anti_bot"}, "host": {"a&b=c.example:8080"}, "order": {OrderUpdated}},
		u.Query())
	assert.Equal(t, "/ui/library?cause=dead_link&host=gone.example&order=updated",
		causeHostHref("dead_link", "gone.example"),
		"no state: the list's cause filter takes dead documents; documents, which the tag counts")
}

// TestWithoutFilterHref: dropping one filter keeps every other, the
// order and the page size, and drops the cursor, in either order; a name
// that isn't a filter's drops nothing.
func TestWithoutFilterHref(t *testing.T) {
	for _, order := range []string{"", OrderUpdated} {
		f := LibraryFilters{Order: order, State: "failed", ContentType: "pdf", Host: "a&b=c.example",
			Folder: "/100% Reading/#1", Cause: "a&cause=b #1", Limit: 7}
		all := url.Values{"state": {"failed"}, "content_type": {"pdf"}, "host": {"a&b=c.example"},
			"folder": {"/100% Reading/#1"}, "cause": {"a&cause=b #1"}, "limit": {"7"}}
		if order != "" {
			all.Set("order", order)
		}
		for name, param := range map[string]string{filterState: "state", filterType: "content_type",
			filterHost: "host", filterFolder: "folder", filterCause: "cause"} {
			want := url.Values{}
			for k, v := range all {
				if k != param {
					want[k] = v
				}
			}
			u := parseHref(t, withoutFilterHref(f, name))
			assert.Equal(t, "/ui/library", u.Path, name)
			assert.Equal(t, want, u.Query(), "%s, order %q", name, order)
		}
		assert.Equal(t, all, parseHref(t, withoutFilterHref(f, "bogus")).Query())
	}
	assert.Equal(t, "/ui/library", withoutFilterHref(LibraryFilters{Cause: "anti_bot"}, filterCause))
	assert.Equal(t, "/ui/library?order=updated",
		withoutFilterHref(LibraryFilters{Order: OrderUpdated, Cause: "anti_bot"}, filterCause))
}

// TestOtherOrderHref: the time column's head leads to the other order,
// the filters and the page size kept; the default names no order.
func TestOtherOrderHref(t *testing.T) {
	f := LibraryFilters{State: "pending", Host: "a&b=c.example", Limit: 7}
	assert.Equal(t, url.Values{"order": {OrderUpdated}, "state": {"pending"}, "host": {"a&b=c.example"},
		"limit": {"7"}}, parseHref(t, otherOrderHref(f)).Query())
	for _, order := range []string{"", OrderSaved} {
		f.Order = order
		assert.Equal(t, OrderUpdated, parseHref(t, otherOrderHref(f)).Query().Get("order"), order)
	}
	f.Order = OrderUpdated
	assert.Equal(t, url.Values{"state": {"pending"}, "host": {"a&b=c.example"}, "limit": {"7"}},
		parseHref(t, otherOrderHref(f)).Query(), "back to the default, which names no order")
	assert.Equal(t, "/ui/library", otherOrderHref(LibraryFilters{Order: OrderUpdated}))
	assert.Equal(t, "/ui/library?order=updated", otherOrderHref(LibraryFilters{}))
}

// TestSearchHref: a search's query, type, page and plan round-trip,
// however odd the query; a blank query is left out, and so are its page
// and its plan, the first page names none, and the home without a type is
// /ui/.
func TestSearchHref(t *testing.T) {
	for _, q := range []string{"a&b=c #1 <x>", "page=2&q=x", "100%", "ü ñ ☃"} {
		for _, contentType := range []string{"", "article", "repo", "video", "pdf", "a&b"} {
			for _, page := range []int{-1, 0, 1, 2, 10} {
				for _, plan := range []string{"", "eyJraW5kIjoib25lIn0", "a+b/c="} {
					u := parseHref(t, searchHref(q, contentType, page, plan))
					assert.Equal(t, "/ui/", u.Path, contentType)
					want := url.Values{"q": {q}}
					if contentType != "" {
						want.Set("content_type", contentType)
					}
					if page > 1 {
						want.Set(PageParam, strconv.Itoa(page))
					}
					if plan != "" {
						want.Set(PlanParam, plan)
					}
					assert.Equal(t, want, u.Query(), "%q, %q, page %d, plan %q", q, contentType, page, plan)
				}
			}
		}
	}
	assert.Equal(t, "/ui/", searchHref("", "", 1, ""))
	assert.Equal(t, "/ui/", searchHref("  ", "", 3, "tok"), "a blank query is the home, which has no pages or plan")
	assert.Equal(t, "/ui/?content_type=pdf", searchHref(" ", "pdf", 2, ""))
	assert.Equal(t, "/ui/?content_type=pdf&q=kafka", searchHref("kafka", "pdf", 1, ""))
	assert.Equal(t, "/ui/?content_type=pdf&page=2&q=kafka", searchHref("kafka", "pdf", 2, ""))
	assert.Equal(t, "/ui/?page=2&plan=tok&q=kafka", searchHref("kafka", "", 2, "tok"))
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
// error page's Retry does, but Interests, which opens on their map, unless
// the map is off.
func TestNavItems(t *testing.T) {
	items := navItems(false)
	navs := make([]Nav, 0, len(items))
	for _, item := range items {
		navs = append(navs, item.Nav)
		if item.Nav != NavInterests {
			assert.Equal(t, navHref(item.Nav), item.Href, item.Nav)
		}
		assert.NotEmpty(t, item.Label, item.Nav)
	}
	assert.Equal(t, []Nav{NavSearch, NavLibrary, NavInterests, NavStatus}, navs)
	assert.Equal(t, mapHref(MapQuery{}), items[2].Href, "the map's own address, By group")
	assert.Equal(t, []string{"/ui/", "/ui/library", "/ui/interests/map", "/ui/status"},
		[]string{items[0].Href, items[1].Href, items[2].Href, items[3].Href})
	assert.Equal(t, []string{"search", "library", "sparkles", "activity"},
		[]string{items[0].Icon, items[1].Icon, items[2].Icon, items[3].Icon})
	assert.Len(t, navHrefs, len(items), "every page with an address is in the navigation")
	assert.Equal(t, navHref(NavInterests), navItems(true)[2].Href, "the map off: the list")
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

	assert.Equal(t, "/ui/interests/unsorted", unsortedHref(1, ""))
	assert.Equal(t, "/ui/interests/unsorted?run=r", unsortedHref(1, "r"))
	assert.Equal(t, "/ui/interests/unsorted?page=2&run=r", unsortedHref(2, "r"))
	assert.Equal(t, "/ui/interests/unsorted?page=2", unsortedHref(2, ""))
	assert.Equal(t, "/ui/interests/unsorted", unsortedHref(-3, ""), "a page before the first is the first")
	for _, run := range []string{"a&page=9&run=b #1", `"><script>`, "100%/x?", "../changes"} {
		for _, page := range []int{1, 3, math.MaxInt} {
			u := parseHref(t, unsortedHref(page, run))
			assert.Equal(t, "/ui/interests/unsorted", u.Path, "the path is fixed")
			want := url.Values{"run": {run}}
			if page > 1 {
				want.Set(PageParam, strconv.Itoa(page))
			}
			assert.Equal(t, want, u.Query(), "the run and page round-trip: %q, %d", run, page)
			assert.Equal(t, run, ShownRun(u.Query()))
		}
	}
	assert.Equal(t, "/ui/interests/changes", changesHref())

	assert.Equal(t, "/ui/interests/i1", interestPageHref("i1", 1))
	assert.Equal(t, "/ui/interests/i1?page=2", interestPageHref("i1", 2))
	for _, id := range []string{"a/b?page=9#x", `"><script>`, "../status"} {
		u := parseHref(t, interestPageHref(id, 4))
		assert.Equal(t, "/ui/interests/"+url.PathEscape(id), u.EscapedPath(), "one path segment")
		assert.Equal(t, url.Values{PageParam: {"4"}}, u.Query())
	}
}

// TestParseMapQuery: the map's view and selection as the address writes
// them, an ID after a selection's first colon, colons and all; and every
// other form refused, naming the forms there are.
func TestParseMapQuery(t *testing.T) {
	for query, want := range map[string]MapQuery{
		"":                                   {View: MapViewGroups},
		"view=groups":                        {View: MapViewGroups},
		"view=similarity":                    {View: MapViewSimilarity},
		"select=unsorted":                    {View: MapViewGroups, Select: MapSelection{Kind: MapSelectUnsorted}},
		"select=area%3Aa1":                   {View: MapViewGroups, Select: MapSelection{Kind: MapSelectArea, ID: "a1"}},
		"select=interest:i1":                 {View: MapViewGroups, Select: MapSelection{Kind: MapSelectInterest, ID: "i1"}},
		"view=similarity&select=document:d1": {View: MapViewSimilarity, Select: MapSelection{Kind: MapSelectDocument, ID: "d1"}},
		"select=document:a:b:c":              {View: MapViewGroups, Select: MapSelection{Kind: MapSelectDocument, ID: "a:b:c"}},
	} {
		q, err := url.ParseQuery(query)
		require.NoError(t, err)
		got, err := ParseMapQuery(q)
		require.NoError(t, err, query)
		assert.Equal(t, want, got, query)
	}
	for _, query := range []string{"view=map", "select=nope", "select=area:", "select=:x", "select=planet:x",
		"select=unsorted:x", "select=area:" + strings.Repeat("a", maxMapSelectID+1)} {
		q, err := url.ParseQuery(query)
		require.NoError(t, err)
		_, err = ParseMapQuery(q)
		require.Error(t, err, query)
		if strings.HasPrefix(query, "view") {
			assert.Contains(t, err.Error(), "want similarity or groups", query)
		} else {
			assert.Contains(t, err.Error(), "want unsorted, area:<id>, interest:<id> or document:<id>", query)
		}
	}
	_, err := ParseMapQuery(url.Values{"select": {"area:" + strings.Repeat("a", maxMapSelectID)}})
	assert.NoError(t, err, "an ID of the longest length")
}

// TestMapHref: the map's address leaves out the default view and the
// library, and every selection round-trips through ParseMapQuery, however
// odd its ID.
func TestMapHref(t *testing.T) {
	assert.Equal(t, "/ui/interests/map", mapHref(MapQuery{}))
	assert.Equal(t, "/ui/interests/map", mapHref(MapQuery{View: MapViewGroups}), "By group is the default")
	assert.Equal(t, "/ui/interests/map?view=similarity", mapHref(MapQuery{View: MapViewSimilarity}))
	assert.Equal(t, "/ui/interests/map?select=area%3Aa1", mapHref(MapQuery{Select: MapSelection{Kind: MapSelectArea,
		ID: "a1"}}))
	assert.Equal(t, "/ui/interests/map?select=unsorted&view=similarity", mapHref(MapQuery{View: MapViewSimilarity,
		Select: MapSelection{Kind: MapSelectUnsorted}}))
	for _, kind := range []string{MapSelectArea, MapSelectInterest, MapSelectDocument} {
		for _, id := range []string{"i1", "a/b?#&=", evilAttr, evilScript, "x:y"} {
			for _, view := range []string{MapViewSimilarity, MapViewGroups} {
				want := MapQuery{View: view, Select: MapSelection{Kind: kind, ID: id}}
				u := parseHref(t, mapHref(want))
				assert.Equal(t, "/ui/interests/map", u.Path)
				got, err := ParseMapQuery(u.Query())
				require.NoError(t, err)
				assert.Equal(t, want, got, "%s %q %s", kind, id, view)
			}
		}
	}
	u := parseHref(t, mapHref(MapQuery{View: MapViewSimilarity, Select: MapSelection{Kind: MapSelectUnsorted}}))
	got, err := ParseMapQuery(u.Query())
	require.NoError(t, err)
	assert.Equal(t, MapQuery{View: MapViewSimilarity, Select: MapSelection{Kind: MapSelectUnsorted}}, got)
}
