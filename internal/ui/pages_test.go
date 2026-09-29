package ui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// parse parses a rendered page.
func parse(t *testing.T, page string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	require.NoError(t, err)
	return doc
}

// byID is the element of doc with id, or nil.
func byID(doc *html.Node, id string) *html.Node {
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && attrValue(n, "id") == id {
			return n
		}
	}
	return nil
}

// children are n's element children named tag.
func children(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	for c := range n.ChildNodes() {
		if c.Type == html.ElementNode && c.Data == tag {
			out = append(out, c)
		}
	}
	return out
}

// TestLibrary_Table: the Library's columns take their widths from the
// colgroup, and every row has the four cells the columns name, the two a
// phone folds away last; a failed row says why under its name.
func TestLibrary_Table(t *testing.T) {
	r := newRenderer(t)
	at := time.Now().Add(-3 * time.Hour)
	untitled := "https://www.google.com/search?q=angular&ei=" + strings.Repeat("CgZwc3ktYWIQ", 50)
	lib := Library{Layout: Layout{Title: "Library", Nav: NavLibrary}, PageSize: 50, NextCursor: "c", Rows: []LibraryRow{
		{DocumentID: "a", Title: strings.Repeat("A long title ", 25), URL: "https://example.com/a", State: "fetched",
			ContentType: "article", UpdatedAt: at},
		{DocumentID: "b", URL: untitled, State: "fetched", ContentType: "unknown", UpdatedAt: at},
		{DocumentID: "c", URL: "https://twitter.com/x/status/1", State: "failed", ContentType: "unknown", UpdatedAt: at,
			FailureCause: "anti_bot", LastError: "permanent failure: native: origin blocked the request (likely anti-bot)"},
		{DocumentID: "d", URL: "https://gone.example/", State: "dead", ContentType: "unknown", UpdatedAt: at,
			FailureCause: "dead_link"},
	}}
	out := render(t, r, PageLibrary, lib)
	doc := parse(t, out)

	assert.Contains(t, out, `<colgroup><col><col class="c-state"><col class="c-type"><col class="c-when"></colgroup>`)
	rows := byID(doc, "rows")
	require.NotNil(t, rows)
	trs := children(rows, "tr")
	require.Len(t, trs, len(lib.Rows))
	for i, tr := range trs {
		tds := children(tr, "td")
		require.Len(t, tds, 4, "row %d", i)
		assert.Equal(t, "state-cell", attrValue(tds[1], "class"), "row %d", i)
		assert.Equal(t, "c-type", attrValue(tds[2], "class"), "row %d", i)
		assert.Equal(t, "c-when when", attrValue(tds[3], "class"), "row %d", i)
	}

	assert.Contains(t, out, `<a class="doc-title" href="/ui/documents/a" title="`+strings.Repeat("A long title ", 25)+`">`)
	assert.Contains(t, out, `<a class="doc-title untitled" href="/ui/documents/b" title="`+
		html.EscapeString(untitled)+`">`+html.EscapeString(strings.TrimPrefix(untitled, "https://"))+"</a>")
	assert.Contains(t, out, `<span class="host" title="`+html.EscapeString(untitled)+`">www.google.com</span>`,
		"an untitled row's host under its name")
	assert.Contains(t, out, `<span class="doc-error" title="permanent failure: native: origin blocked the request (likely anti-bot)">`+
		`<span class="err-cause">Blocked by bot protection</span> <span class="msg">origin blocked the request (likely anti-bot)</span></span>`)
	assert.Contains(t, out, `<span class="doc-error"><span class="err-cause">Dead link</span> </span>`, "a cause without an error")
	assert.Equal(t, 2, strings.Count(out, `class="doc-error"`), "only the failed and dead rows")
	assert.Contains(t, out, `<span class="sep narrow">·</span><span class="narrow">article</span>`, "the type, on a phone")
	assert.Contains(t, out, `<span class="narrow"><time datetime="`, "the time, on a phone")
	assert.Contains(t, out, `<span class="badge state-dead">dead</span>`)
	assert.Contains(t, out, `<span class="tag"><svg class="icon"`, "the type with its icon")
	assert.Contains(t, out, `>Load 50 more</a></div>`)

	lib.NextCursor = ""
	assert.Contains(t, render(t, r, PageLibrary, lib), `<div class="load-more" id="more"></div>`, "nothing to load")
}

// TestLibrary_Empty: an empty Library says whether the filters matched
// nothing, naming them, or the library has nothing yet; either way it
// holds an empty #more.
func TestLibrary_Empty(t *testing.T) {
	r := newRenderer(t)
	lib := Library{Layout: Layout{Title: "Library", Nav: NavLibrary}, Filters: LibraryFilters{State: "pending",
		ContentType: "pdf", Host: "arxiv.org", Folder: "/Reading", Limit: 5}}
	out := render(t, r, PageLibrary, lib)
	assert.Contains(t, out, "<h2>No documents match</h2>")
	assert.Contains(t, out, "Nothing in the library has state <code>pending</code>, type <code>pdf</code>, "+
		"host <code>arxiv.org</code>, folder <code>/Reading</code>.")
	assert.Contains(t, out, "A host matches exactly")
	assert.Contains(t, out, `<a class="btn" href="/ui/library">Clear filters</a>`)
	assert.Contains(t, out, `<div class="load-more" id="more"></div>`)

	lib.Filters = LibraryFilters{Limit: 5}
	out = render(t, r, PageLibrary, lib)
	assert.Contains(t, out, "<h2>Your library is empty</h2>")
	assert.Contains(t, out, "<code>curio import chrome</code>")
	assert.NotContains(t, out, "A host matches exactly")
	assert.Contains(t, out, `<div class="load-more" id="more"></div>`)
}

// TestInterests_Cut: the Interests page says how many interests the run
// found, and when it shows only the largest, how many; the coverage bar's
// proportions are attributes.
func TestInterests_Cut(t *testing.T) {
	r := newRenderer(t)
	page := Interests{Layout: Layout{Title: "Interests", Nav: NavInterests},
		Run: &InterestRun{ComputedAt: time.Now(), Algo: "knn-graph", Documents: 3142, Noise: 1191, Interests: 222}}
	for i := range 50 {
		page.Interests = append(page.Interests, Interest{ID: strconv.Itoa(i), Label: "Topic " + strconv.Itoa(i),
			Size: 84 - i, Cohesion: 0.7})
	}
	out := render(t, r, PageInterests, page)
	assert.Contains(t, out, "222 topics curio found in your library, largest first; these are the 50 largest.")
	assert.Contains(t, out, `<rect class="fill-accent" x="0.000" y="0" width="62.094" height="10"/>`)
	assert.Contains(t, out, `<span class="n">1,951</span> in an interest <span class="pct">62%</span>`)
	assert.Contains(t, out, `<span class="n">1,191</span> in none <span class="pct">38%</span>`)
	assert.NotContains(t, out, "style=")

	page.Run.Interests = 50
	assert.Contains(t, render(t, r, PageInterests, page), "50 topics curio found in your library, largest first.</p>",
		"all of them shown")
}

// TestDocument_Head: the head's facts, a published date as its UTC day,
// and the commands that work for the document's state.
func TestDocument_Head(t *testing.T) {
	r := newRenderer(t)
	doc := Document{Layout: Layout{Title: "t", Nav: NavLibrary}, Meta: DocumentMeta{ID: "d1", Title: "A post",
		URL: "https://site.example/post", ContentType: "article", State: "fetched", Author: "Ann", Language: "en",
		PublishedAt: time.Date(2014, 3, 14, 0, 0, 0, 0, time.UTC), WordCount: 5210},
		Extraction: &Extraction{Fetcher: "native", Status: "ok"}, Text: TextPanel{State: TextNotFetched}}
	out := render(t, r, PageDocument, doc)
	assert.Contains(t, out, `<nav class="crumbs" aria-label="Breadcrumb"><a href="/ui/library">Library</a>`+
		`<span class="sep">/</span><span>site.example</span></nav>`)
	assert.Contains(t, out, "<h1>A post</h1>")
	assert.Contains(t, out, `<a href="https://site.example/post" rel="noopener noreferrer" target="_blank">`)
	assert.Contains(t, out, `<span class="text">5,210 words</span>`)
	assert.Contains(t, out, `<span class="text">by Ann</span>`)
	assert.Contains(t, out, `<span class="text">published Mar 14, 2014</span>`)
	assert.Contains(t, out, "<code>curio refetch d1</code>")
	assert.Contains(t, out, "<code>curio reindex d1</code>")
	assert.NotContains(t, out, `class="callout`, "a fetched document has no failure")

	doc.Meta.State, doc.Extraction, doc.FailureCause = "dead", nil, "dead_link"
	doc.Meta.Title, doc.Meta.URL = "", "ftp://gone.example/a/"
	out = render(t, r, PageDocument, doc)
	assert.Contains(t, out, "<h1>gone.example/a</h1>", "an untitled document by its short URL")
	assert.Contains(t, out, `<span class="truncate">ftp://gone.example/a/</span>`, "not a link")
	assert.Contains(t, out, `<p class="callout-title">This page is gone</p>`)
	assert.Contains(t, out, "<code>curio refetch --force d1</code>")
	assert.NotContains(t, out, "curio reindex", "nothing to reindex")
	assert.NotContains(t, out, "<details", "no error to show")
}

// TestUpstream: an upstream's dot follows its state, off is neutral, and
// Jina is named for people. Only failing and degraded call for a callout.
func TestUpstream(t *testing.T) {
	for state, tone := range map[string]string{"ok": "ok", "idle": "ok", "degraded": "warn", "paused": "warn",
		"failing": "danger", "disabled": "neutral", evilScript: "neutral"} {
		assert.Equal(t, tone, Upstream{Name: "jina", Enabled: true, State: state}.Tone(), state)
	}
	assert.Equal(t, "neutral", Upstream{State: "failing"}.Tone(), "off")
	assert.Equal(t, "Jina Reader", Upstream{Name: "jina"}.Label())
	assert.Equal(t, "other", Upstream{Name: "other"}.Label())

	for state, alert := range map[string]string{"failing": "danger", "degraded": "warn", "paused": "", "idle": "",
		"ok": "", "disabled": "", evilScript: ""} {
		assert.Equal(t, alert, Upstream{Name: "jina", Enabled: true, State: state}.Alert(), state)
	}
	assert.Empty(t, Upstream{State: "failing"}.Alert(), "off")
}

// calloutTitleRE finds the callouts' titles, in page order.
var calloutTitleRE = regexp.MustCompile(`<div class="callout (callout-[a-z]+) mb-4" role="([a-z]+)">` +
	`<svg[^>]*>.*?</svg>\n<p class="callout-title">([^<]*)</p>`)

// TestStatus_Attention: what needs attention opens the page, before the
// board, in a fixed order: Ollama, the drift, then each failing or
// degraded upstream. None shows when the health read failed, and a
// healthy daemon shows none.
func TestStatus_Attention(t *testing.T) {
	r := newRenderer(t)
	at := time.Now().Add(-5 * time.Minute)
	st := Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Health: HealthPanel{
		OllamaDetail: "ollama unreachable (start it with `ollama serve`)",
		Drift:        &Drift{Changes: []DriftChange{{What: "model digest", Recorded: "a", Current: "b"}}, Fix: "curio reindex --all"},
		Upstreams: []Upstream{
			{Name: "jina", Enabled: true, State: "degraded", LastSuccess: at, LastFailure: at,
				LastFailureClass: "rate_limited", Window: 15 * time.Minute, Calls: 20, Failed: 6},
			{Name: "other", Enabled: true, State: "failing", LastFailure: at, LastFailureClass: "network"},
			{Name: "paused", Enabled: true, State: "paused"}, {Name: "off", State: "failing"},
		}}}
	out := render(t, r, PageStatus, st)
	callouts := calloutTitleRE.FindAllStringSubmatch(out, -1)
	got := make([][]string, 0, len(callouts))
	for _, m := range callouts {
		got = append(got, m[1:])
	}
	assert.Equal(t, [][]string{
		{"callout-danger", "alert", "Ollama isn't ready"},
		{"callout-warn", "status", "The embeddings drifted"},
		{"callout-warn", "status", "Jina Reader is degraded"},
		{"callout-danger", "alert", "other is failing"},
	}, got)
	assert.Less(t, strings.LastIndex(out, `class="callout`), strings.Index(out, `<div class="board">`),
		"the callouts come before the board")
	assert.Contains(t, out, "<p>6 of its 20 calls in the last 15m failed.")
	assert.Contains(t, out, "The last failure was rate_limited, <time")
	assert.Contains(t, out, "Its calls have failed, for more than one site, since the daemon started.")
	assert.Contains(t, out, "<code>curio doctor</code> says why.")
	assert.NotContains(t, out, "refus", "a refusal is a healthy answer")
	assert.Equal(t, 1, strings.Count(out, "The embeddings drifted"), "the Health card keeps its row, not a callout")
	assert.Contains(t, out, `<span class="sub">0 dimensions, drifted</span>`)

	st.Health.Err = &PanelError{Message: "health", RequestID: "r"}
	assert.NotContains(t, render(t, r, PageStatus, st), `class="callout`, "no health read, no callouts")

	healthy := Status{Layout: st.Layout, Health: HealthPanel{OllamaReachable: true,
		Upstreams: []Upstream{{Name: "jina", Enabled: true, State: "ok"}}}}
	assert.NotContains(t, render(t, r, PageStatus, healthy), `class="callout`)
}

// TestStatus_Failures: the causes, most first, each linking to the Library
// of its documents, with a bar scaled to the most; none read, no card.
func TestStatus_Failures(t *testing.T) {
	r := newRenderer(t)
	st := Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Health: HealthPanel{OllamaReachable: true},
		Failures: FailuresPanel{Total: 1745, Causes: []Count{{Name: "anti_bot", Count: 926}, {Name: "dead_link", Count: 819}}}}
	out := render(t, r, PageStatus, st)
	assert.Contains(t, out, `<a class="more" href="/ui/library?state=failed">Failed documents →</a>`)
	assert.Contains(t, out, `<li><a class="label" href="/ui/library?cause=anti_bot" title="Blocked by bot protection">`+
		`Blocked by bot protection</a><svg class="hbar" viewBox="0 0 100 10" preserveAspectRatio="none" aria-hidden="true">`+
		`<rect class="fill-track" x="0" y="0" width="100" height="10"/><rect class="fill-danger" x="0.000" y="0" width="100.000" height="10"/></svg>`+
		`<span class="n">926</span></li>`)
	assert.Contains(t, out, `<rect class="fill-neutral" x="0.000" y="0" width="88.445" height="10"/></svg><span class="n">819</span>`)
	assert.NotContains(t, out, "style=")

	st.Failures = FailuresPanel{}
	assert.NotContains(t, render(t, r, PageStatus, st), "Why documents failed", "nothing failed")
	st.Failures.Err = &PanelError{Message: "failures", RequestID: "r"}
	assert.Contains(t, render(t, r, PageStatus, st), "Couldn't read this: failures.")
}

// TestSearch_Placeholder: the box says how many documents there are to
// search when the home knows, and "your library" otherwise.
func TestSearch_Placeholder(t *testing.T) {
	for _, tc := range []struct {
		search Search
		want   string
	}{
		{Search{Home: &SearchHome{Searchable: 4498}}, "Search your 4,498 documents"},
		{Search{Home: &SearchHome{Searchable: 1}}, "Search your 1 document"},
		{Search{Home: &SearchHome{}}, "Search your library"},
		{Search{Query: "kafka"}, "Search your library"},
	} {
		assert.Equal(t, tc.want, tc.search.Placeholder())
	}
}

// TestSearch_TypeTabs: a tab for every type the page offers, each keeping
// the query, the chosen one marked; a type it doesn't offer marks none.
func TestSearch_TypeTabs(t *testing.T) {
	tabs := Search{Query: "kafka", Type: "video"}.TypeTabs()
	labels, hrefs := make([]string, 0, len(tabs)), make([]string, 0, len(tabs))
	var current []string
	for _, tab := range tabs {
		labels, hrefs = append(labels, tab.Label), append(hrefs, tab.Href)
		if tab.Current {
			current = append(current, tab.Label)
		}
	}
	assert.Equal(t, []string{"All", "Articles", "Repos", "Videos", "PDFs"}, labels)
	assert.Equal(t, []string{"/ui/?q=kafka", "/ui/?content_type=article&q=kafka", "/ui/?content_type=repo&q=kafka",
		"/ui/?content_type=video&q=kafka", "/ui/?content_type=pdf&q=kafka"}, hrefs)
	assert.Equal(t, []string{"Videos"}, current)

	assert.True(t, Search{}.TypeTabs()[0].Current, "All without a type")
	for _, tab := range (Search{Type: "thread"}).TypeTabs() {
		assert.False(t, tab.Current, tab.Label)
	}
}

// TestSearch_Results: #results has no class of its own whatever it holds,
// since htmx swaps only its children: the landing on the home, whatever
// the home could read, and the results column with a query.
func TestSearch_Results(t *testing.T) {
	r := newRenderer(t)
	layout := Layout{Title: "Search", Nav: NavSearch}
	interests := make([]Interest, 0, 6)
	for i := range 6 {
		interests = append(interests, Interest{ID: "i" + strconv.Itoa(i), Label: "Topic " + strconv.Itoa(i) + " and more",
			Size: 60 - i})
	}
	interests[3].Label = ""
	for name, tc := range map[string]struct {
		page    Search
		landing bool
	}{
		"home":                {Search{Layout: layout, Home: &SearchHome{Searchable: 3, Interests: interests, AllInterests: 222}}, true},
		"home without a read": {Search{Layout: layout, Home: &SearchHome{}}, true},
		"results":             {Search{Layout: layout, Query: "kafka", Results: &SearchResults{}}, false},
		"a failed search":     {Search{Layout: layout, Query: "kafka", Err: &PanelError{Message: "m", RequestID: "r"}}, false},
	} {
		out := render(t, r, PageSearch, tc.page)
		results := byID(parse(t, out), "results")
		require.NotNil(t, results, name)
		assert.Empty(t, attrValue(results, "class"), name)
		divs := children(results, "div")
		require.Len(t, divs, 1, name)
		if tc.landing {
			assert.Equal(t, "landing", attrValue(divs[0], "class"), name)
		} else {
			assert.Equal(t, "results", attrValue(divs[0], "class"), name)
			assert.NotContains(t, out, "explore-line", name)
		}
	}

	out := render(t, r, PageSearch, Search{Layout: layout, Home: &SearchHome{Interests: interests, AllInterests: 222}})
	assert.Contains(t, out, `<nav class="explore-line" aria-label="Your interests"><span class="label">Your interests</span><ul>`)
	assert.Contains(t, out, `<li><a href="/ui/interests/i0" title="Topic 0 and more">Topic 0</a></li>`,
		"a label cut at \" and \" only with two words before it")
	assert.Contains(t, out, `<li><a href="/ui/interests/i3"><span class="unlabeled">Unlabeled interest</span></a></li>`)
	assert.Equal(t, 6, strings.Count(out, `<li><a href="/ui/interests/`))
	assert.Contains(t, out, `<a class="all" href="/ui/interests">All 222 →</a>`)
	assert.Less(t, strings.Index(out, "/ui/interests/i0"), strings.Index(out, "/ui/interests/i5"), "largest first")
	assert.NotContains(t, render(t, r, PageSearch, Search{Layout: layout, Home: &SearchHome{}}), "Your interests")

	typed := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Type: "pdf", Results: &SearchResults{}})
	assert.Contains(t, typed, `<input type="hidden" name="content_type" value="pdf">`)
	assert.Contains(t, typed, `<a href="/ui/?content_type=pdf&amp;q=kafka" aria-current="page">PDFs</a>`)
	assert.NotContains(t, render(t, r, PageSearch, Search{Layout: layout, Home: &SearchHome{}}), `name="content_type"`)
}

// TestLibrary_Tabs: the state tabs keep the other filters, mark the
// current state, and count only when state is the one filter; the form
// keeps the state for Apply, and the Showing line counts every row shown.
func TestLibrary_Tabs(t *testing.T) {
	r := newRenderer(t)
	counts := &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"fetched": 4498, "failed": 2150,
		"dead": 819}}
	lib := Library{Layout: Layout{Title: "Library", Nav: NavLibrary}, Filters: LibraryFilters{State: "failed"},
		Counts: counts, PageSize: 50, Shown: 50, NextCursor: "c",
		Rows: []LibraryRow{{DocumentID: "a", URL: "https://example.com/a", State: "failed"}}}
	out := render(t, r, PageLibrary, lib)
	assert.Contains(t, out, `<div class="segmented" role="group" aria-label="State">`+
		`<a href="/ui/library">All <span class="count">7,467</span></a>`+
		`<a href="/ui/library?state=fetched">Fetched <span class="count">4,498</span></a>`+
		`<a href="/ui/library?state=pending">Pending <span class="count">0</span></a>`+
		`<a href="/ui/library?state=failed" aria-current="page">Failed <span class="count">2,150</span></a>`+
		`<a href="/ui/library?state=dead">Dead <span class="count">819</span></a></div>`)
	assert.Contains(t, out, `<input type="hidden" name="state" value="failed">`)
	assert.NotContains(t, out, `<select class="select" name="state"`)
	assert.Contains(t, out, `<p class="lede">7,467 documents from 7,497 bookmarks.</p>`)
	assert.Contains(t, out, `<p class="pager-summary mt-4" id="showing">Showing 51 of 2,150 documents, most recently updated first.</p>`)
	assert.Contains(t, out, `href="/ui/library?cursor=c&amp;state=failed" hx-get="/ui/library?cursor=c&amp;shown=51&amp;state=failed"`)
	assert.Contains(t, out, `hx-select-oob="#more,#showing">Load 50 more</a>`)

	for _, f := range []LibraryFilters{{ContentType: "pdf"}, {Host: "arxiv.org"}, {Folder: "/Reading"}, {Cause: "anti_bot"}} {
		f.State = "failed"
		lib.Filters = f
		out := render(t, r, PageLibrary, lib)
		assert.NotContains(t, out, `class="count"`, "%+v", f)
		assert.Contains(t, out, `>Showing 51 documents, most recently updated first.</p>`, "%+v", f)
		assert.Contains(t, out, `aria-current="page">Failed</a>`, "%+v", f)
	}

	lib.Filters, lib.Counts = LibraryFilters{}, nil
	out = render(t, r, PageLibrary, lib)
	assert.NotContains(t, out, `class="count"`, "no counts read")
	assert.Contains(t, out, `<a href="/ui/library" aria-current="page">All</a>`)
	assert.Contains(t, out, `<p class="lede">Every page curio saved for you.</p>`)
	assert.NotContains(t, out, `name="state"`, "no state to keep")

	lib.Counts, lib.Filters = counts, LibraryFilters{State: "dead"}
	lib.Shown = 819
	assert.Contains(t, render(t, r, PageLibrary, lib), `>Showing 820 documents, most recently updated first.</p>`,
		"more rows shown than the count: the library changed between pages")
}

// TestSearchHit_Matches: a result shows its first two matches, and the
// rest a click away.
func TestSearchHit_Matches(t *testing.T) {
	m := func(s string) Match { return Match{Segments: []Segment{{Text: s}}} }
	hit := SearchHit{Matches: []Match{m("a"), m("b"), m("c")}}
	assert.Equal(t, []Match{m("a"), m("b")}, hit.ShownMatches())
	assert.Equal(t, []Match{m("c")}, hit.MoreMatches())
	hit.Matches = hit.Matches[:2]
	assert.Len(t, hit.ShownMatches(), 2)
	assert.Empty(t, hit.MoreMatches())
	hit.Matches = nil
	assert.Empty(t, hit.ShownMatches())
	assert.Empty(t, hit.MoreMatches())
}
