package ui

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui/uitest"
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
			ContentType: "article", When: at},
		{DocumentID: "b", URL: untitled, State: "fetched", ContentType: "unknown", When: at},
		{DocumentID: "c", URL: "https://twitter.com/x/status/1", State: "failed", ContentType: "unknown", When: at,
			FailureCause: "anti_bot", LastError: "permanent failure: native: origin blocked the request (likely anti-bot)"},
		{DocumentID: "d", URL: "https://gone.example/", State: "dead", ContentType: "unknown", When: at,
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

// TestDocRef: a list names a document by its title, then by its
// bookmark's title as a fallback, then by its short URL, each on one line
// with the whole name on hover, linked when it has an ID.
func TestDocRef(t *testing.T) {
	set := newRenderer(t).pages[PageLibrary]
	const url = "https://example.com/a/b?q=1"
	for _, tc := range []struct {
		ref  DocRef
		want string
	}{
		{DocRef{ID: "d1", Title: "A <title>", Fallback: "ignored", URL: url},
			`<a class="doc-title" href="/ui/documents/d1" title="A &lt;title&gt;">A &lt;title&gt;</a>`},
		{DocRef{ID: "d1", Fallback: "Saved as <this>", URL: url},
			`<a class="doc-title from-bookmark" href="/ui/documents/d1" title="Saved as &lt;this&gt;">Saved as &lt;this&gt;</a>`},
		{DocRef{ID: "d1", URL: url},
			`<a class="doc-title untitled" href="/ui/documents/d1" title="` + url + `">example.com/a/b?q=1</a>`},
		{DocRef{Fallback: "Saved as <this>", URL: url},
			`<span class="doc-title from-bookmark" title="Saved as &lt;this&gt;">Saved as &lt;this&gt;</span>`},
		{DocRef{URL: url},
			`<span class="doc-title untitled" title="` + url + `">example.com/a/b?q=1</span>`},
	} {
		var buf strings.Builder
		require.NoError(t, set.ExecuteTemplate(&buf, "doc-title", tc.ref))
		assert.Equal(t, tc.want, buf.String(), "%+v", tc.ref)
	}

	row := LibraryRow{DocumentID: "d1", URL: url, BookmarkTitle: "Saved"}
	assert.Equal(t, "example.com/a/b?q=1", row.Cell().Where, "a named row's short URL under its name")
	row.BookmarkTitle = ""
	assert.Equal(t, "example.com", row.Cell().Where, "an unnamed row's host under its address")
}

// TestLibrary_Saved: in the Date saved order the time column reads Saved
// and holds when each page was saved, a row names the browser it came
// from on a wide screen, with the folder on hover, a note explains the
// rows, and the Showing line counts saves: of the library's bookmarks with
// no filter at all, on their own otherwise.
func TestLibrary_Saved(t *testing.T) {
	r := newRenderer(t)
	saved := time.Now().Add(-26 * time.Hour)
	counts := &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"failed": 2150}}
	lib := Library{Layout: Layout{Title: "Library", Nav: NavLibrary}, Filters: LibraryFilters{Order: OrderSaved},
		Counts: counts, PageSize: 50, NextCursor: "c", Rows: []LibraryRow{
			{DocumentID: "a", Title: "A page", URL: "https://example.com/a", State: "fetched", ContentType: "article",
				When: saved, Source: "safari", Folder: "/Reading/Web"},
			{URL: "https://example.com/orphan", BookmarkTitle: "No document", When: saved, Source: "chrome"},
		}}
	out := render(t, r, PageLibrary, lib)
	assert.Contains(t, out, `<option value="saved" selected>Date saved</option>`)
	assert.Contains(t, out, `<th scope="col" class="c-when">Saved</th>`)
	assert.Contains(t, out, `<span class="sep wide">·</span><span class="wide" title="/Reading/Web">safari</span>`)
	assert.Contains(t, out, `<span class="sep wide">·</span><span class="wide">chrome</span>`, "no folder, no title")
	assert.Contains(t, out, `<time datetime="`+datetime(saved)+`"`)
	assert.Contains(t, out, `<span class="doc-title from-bookmark" title="No document">No document</span>`)
	assert.Contains(t, out, `<td class="state-cell"></td>`+"\n"+`<td class="c-type"></td>`, "no document, no state or type")
	assert.Contains(t, out, `<div class="load-more" id="more"><a class="btn" href="/ui/library?cursor=c&amp;order=saved"`)
	assert.Equal(t, 1, strings.Count(out, `<p class="table-note">Newest saves first, one row per bookmark: `+
		`a page saved in two browsers is listed twice. Safari keeps no save dates, so its bookmarks are dated when `+
		`curio imported them.</p>`))
	assert.Contains(t, out, `<p class="pager-summary mt-4" id="showing">Showing 2 of 7,497 saves.</p>`)
	assert.Contains(t, out, `<a href="/ui/library?order=saved&amp;state=failed">Failed <span class="count">2,150</span></a>`,
		"the tabs keep the order and count documents")

	for _, f := range []LibraryFilters{{State: "failed"}, {ContentType: "pdf"}, {Host: "example.com"},
		{Folder: "/Reading"}, {Cause: "anti_bot"}} {
		f.Order = OrderSaved
		lib.Filters = f
		assert.Contains(t, render(t, r, PageLibrary, lib), `<p class="pager-summary mt-4" id="showing">Showing 2 saves.</p>`,
			"%+v", f)
	}
	lib.Filters, lib.Rows = LibraryFilters{Order: OrderSaved, State: "failed"}, lib.Rows[:1]
	assert.Contains(t, render(t, r, PageLibrary, lib), `>Showing 1 save.</p>`)
	lib.Filters, lib.Counts = LibraryFilters{Order: OrderSaved}, nil
	assert.Contains(t, render(t, r, PageLibrary, lib), `>Showing 1 save.</p>`, "no counts read")
	lib.Counts, lib.Shown = counts, 7497
	assert.Contains(t, render(t, r, PageLibrary, lib), `>Showing 7,498 saves.</p>`, "more shown than the count")

	lib.Rows, lib.NextCursor = nil, ""
	out = render(t, r, PageLibrary, lib)
	assert.Contains(t, out, "<h2>Your library is empty</h2>")
	assert.NotContains(t, out, "table-note")
	lib.Filters.Host = "nothing.example"
	out = render(t, r, PageLibrary, lib)
	assert.Contains(t, out, "<h2>No saves match</h2>")
	assert.Contains(t, out, `<a class="btn" href="/ui/library?order=saved">Clear filters</a>`, "it keeps the order")

	lib.Filters.Order = ""
	out = render(t, r, PageLibrary, lib)
	assert.Contains(t, out, `<option value="updated">Last updated</option><option value="saved">Date saved</option>`)
	assert.Contains(t, out, "<h2>No documents match</h2>")
}

// interestsPage is page of a run of 222 interests, its cards as many as
// the page holds of them; the coverage bar's proportions are attributes.
func interestsPage(page int) Interests {
	v := Interests{Layout: Layout{Title: "Interests", Nav: NavInterests}, Page: page,
		Run: &InterestRun{ID: "run-1", ComputedAt: time.Now(), Kind: "warm", Shape: "flat", Documents: 3142,
			Loose: 191, Unsorted: 1000, Interests: 222, Total: 222}}
	for i := range min(InterestsPageSize, 222-PageOffset(page, InterestsPageSize)) {
		v.Interests = append(v.Interests, Interest{ID: strconv.Itoa(i), Label: "Topic " + strconv.Itoa(i),
			Size: 84 - i, Cohesion: 0.7})
	}
	return v
}

// TestInterests_Pages: the lede counts the run's interests, and past the
// first page says which page it is; the pager under the cards counts the
// cards shown of the run's, and every link between pages names the run.
func TestInterests_Pages(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, PageInterests, interestsPage(1))
	assert.Contains(t, out, `<p class="lede">222 interests curio found in your library, largest first.</p>`)
	assert.NotContains(t, out, "largest;")
	assert.Equal(t, InterestsPageSize, strings.Count(out, `<li class="card interest">`))
	assert.Contains(t, out, `<rect class="fill-accent" x="0.000" y="0" width="62.094" height="10"/>`+
		`<rect class="fill-accent-border" x="62.094" y="0" width="6.079" height="10"/></svg>`)
	assert.Contains(t, out, `<span class="item"><span class="swatch swatch-accent"></span><span class="n">1,951</span> in an interest <span class="pct">62%</span></span>`+
		`<span class="item"><span class="swatch swatch-accent-border"></span><span class="n">191</span> loose fits <span class="pct">6%</span></span>`+
		`<span class="item"><span class="swatch swatch-track"></span><span class="n">1,000</span> unsorted <span class="pct">32%</span></span>`)
	assert.Regexp(t, `<span class="item run">Run of \d{4}-\d\d-\d\d \d\d:\d\d · warm · 3,142 documents</span>`, out)
	assert.NotContains(t, out, "style=")
	assert.Contains(t, out, `<span class="pager-summary">Interests 1–24 of 222</span>`)
	assert.Contains(t, out, `<a href="/ui/interests?run=run-1" aria-label="Page 1" aria-current="page">1</a>`)
	assert.Contains(t, out, `<a href="/ui/interests?page=10&amp;run=run-1" aria-label="Page 10">10</a>`)
	assert.Contains(t, out, `<a class="step" id="pager-next" href="/ui/interests?page=2&amp;run=run-1" rel="next">`)
	assert.NotContains(t, out, `role="note"`, "no rebuild since")

	out = render(t, r, PageInterests, interestsPage(2))
	assert.Contains(t, out, `<p class="lede">222 interests curio found in your library, largest first. Page 2 of 10.</p>`)
	assert.Contains(t, out, `<span class="pager-summary">Interests 25–48 of 222</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-prev" href="/ui/interests?run=run-1" rel="prev">`)

	last := interestsPage(10)
	assert.Len(t, last.Interests, 6)
	out = render(t, r, PageInterests, last)
	assert.Contains(t, out, "Page 10 of 10.</p>")
	assert.Contains(t, out, `<span class="pager-summary">Interests 217–222 of 222</span>`)
	assert.Contains(t, out, `<span class="step" aria-disabled="true">Next`)
	assert.Nil(t, last.OutOfRange())

	all := interestsPage(1)
	all.Run.Interests, all.Run.Total = InterestsPageSize, InterestsPageSize
	out = render(t, r, PageInterests, all)
	assert.Contains(t, out, "24 interests curio found in your library, largest first.</p>")
	assert.NotContains(t, out, `class="pager"`, "one page needs no pager")
}

// TestInterests_RunChanged: a page asked for from another run's says the
// interests were rebuilt, leading back to the first page past it; the
// note sits outside the polled regions.
func TestInterests_RunChanged(t *testing.T) {
	r := newRenderer(t)
	page := interestsPage(3)
	page.RunChanged = true
	out := render(t, r, PageInterests, page)
	assert.Contains(t, out, `<div class="callout callout-info mb-4" role="note">`)
	assert.Contains(t, out, `The interests were rebuilt since the page you came from, so this page lists the new `+
		`run's. <a href="/ui/interests?run=run-1">Start again from page 1</a>.</p>`)
	note := strings.Index(out, `role="note"`)
	assert.Less(t, note, strings.Index(out, `aria-label="Coverage"`), "above the coverage")
	assert.Greater(t, note, strings.Index(out, `id="rebuild-control"`), "outside the head's live regions")

	page = interestsPage(1)
	page.RunChanged = true
	out = render(t, r, PageInterests, page)
	assert.Contains(t, out, "so this page lists the new run's.</p>", "the first page needs no way back to it")
	assert.NotContains(t, out, "Start again")

	page = interestsPage(11) // the new run is shorter: nothing to list
	page.RunChanged = true
	require.NotNil(t, page.OutOfRange())
	out = render(t, r, PageInterests, page)
	assert.Contains(t, out, "so this page is past the new run's last. <a")
	assert.NotContains(t, out, "lists the new run's")
}

// TestInterests_OutOfRange: a page past the last keeps the head, the
// coverage and the rebuild, and in place of the cards says how many pages
// there are, with the way to the first and last; a run without interests
// and no run at all have one page.
func TestInterests_OutOfRange(t *testing.T) {
	r := newRenderer(t)
	page := interestsPage(11)
	require.NotNil(t, page.OutOfRange())
	page.Rebuild = Rebuild{Enabled: true, Shown: "run-1"}
	out := render(t, r, PageInterests, page)
	assert.Contains(t, out, `<p class="lede">222 interests curio found in your library, largest first.</p>`)
	assert.Contains(t, out, `aria-label="Coverage"`)
	assert.Contains(t, out, `id="rebuild"`)
	assert.Contains(t, out, `id="rebuild-poll"`)
	assert.Contains(t, out, "<h2>No page 11</h2>\n<p>This list has 10 pages.</p>")
	assert.Contains(t, out, `<a class="btn" href="/ui/interests?run=run-1">First page</a> `+
		`<a class="btn" href="/ui/interests?page=10&amp;run=run-1">Last page</a>`)
	assert.NotContains(t, out, "interest-grid")
	assert.NotContains(t, out, `class="pager"`)

	empty := Interests{Layout: page.Layout, Page: 2, Run: &InterestRun{ID: "run-2", Documents: 5, Unsorted: 5}}
	out = render(t, r, PageInterests, empty)
	assert.Contains(t, out, "<h2>No page 2</h2>\n<p>This list has 1 page.</p>")
	assert.Contains(t, out, `<a class="btn" href="/ui/interests?run=run-2">First page</a></p>`)
	assert.NotContains(t, out, "No interests in this run", "the card never says the run found none")

	none := Interests{Layout: page.Layout, Page: 2}
	out = render(t, r, PageInterests, none)
	assert.Contains(t, out, "<h2>No page 2</h2>")
	assert.Contains(t, out, `<a class="btn" href="/ui/interests">First page</a></p>`)
	assert.NotContains(t, out, "No interests yet")
	assert.Nil(t, Interests{Page: 1}.OutOfRange())
}

// TestInterests_Areas: in the areas shape the lede counts areas and
// interests, each card is an area naming its largest interests and their
// sizes, with the way to all of them, and the pager counts areas.
func TestInterests_Areas(t *testing.T) {
	r := newRenderer(t)
	v := Interests{Layout: Layout{Title: "Interests", Nav: NavInterests}, Page: 1,
		Run: &InterestRun{ID: "run-1", Shape: "areas", Documents: 5254, Unsorted: 402, Areas: 30, Interests: 187,
			Total: 30},
		Interests: []Interest{{ID: "a1", Area: true, Label: "Tech <Skills>", Size: 628, NumChildren: 10, Cohesion: 0.35,
			Children: []Interest{{ID: "i1", Label: "AI Agent Engineering", Size: 86}, {ID: "i2", Size: 45}}}}}
	out := render(t, r, PageInterests, v)
	assert.Contains(t, out, `<p class="lede">30 areas holding 187 interests in your library, largest first.</p>`)
	assert.Regexp(t, `<h2><svg class="icon"[^>]*>.*?</svg><a href="/ui/interests/a1">Tech &lt;Skills&gt;</a></h2>`, out,
		"an area carries the layers icon")
	assert.Contains(t, out, `<div class="stats"><span><b>628</b> documents</span><span><b>10</b> interests</span></div>`,
		"no new document, no new count")
	assert.Contains(t, out, `<ul class="members children"><li><a href="/ui/interests/i1" title="AI Agent Engineering">`+
		`AI Agent Engineering</a><svg class="stackbar" viewBox="0 0 100 10" preserveAspectRatio="none" aria-hidden="true">`+
		`<rect class="fill-track" x="0" y="0" width="100" height="10"/>`+
		`<rect class="fill-accent" x="0.000" y="0" width="100.000" height="10"/></svg><span class="n">86</span></li>`)
	assert.Contains(t, out, `<li><a href="/ui/interests/i2" title="Unlabeled interest"><span class="unlabeled">Unlabeled interest</span></a>`+
		`<svg class="stackbar" viewBox="0 0 100 10" preserveAspectRatio="none" aria-hidden="true">`+
		`<rect class="fill-track" x="0" y="0" width="100" height="10"/>`+
		`<rect class="fill-accent" x="0.000" y="0" width="52.326" height="10"/></svg><span class="n">45</span></li></ul>`,
		"each bar scaled to the area's largest")
	assert.Contains(t, out, `<a class="all" href="/ui/interests/a1">+ 8 more interests →</a>`)
	assert.Contains(t, out, `<span class="pager-summary">Areas 1–1 of 30</span>`)
	assert.Contains(t, out, `<span class="n">4,852</span> in an interest`)
	assert.Contains(t, out, `<span class="n">402</span> unsorted`)
}

// TestInterests_FirstGrouping: before the first rebuild is done, a rebuild
// in flight is the library being grouped for the first time; without one,
// there are no interests yet.
func TestInterests_FirstGrouping(t *testing.T) {
	r := newRenderer(t)
	for _, b := range []Rebuild{{Enabled: true, Queued: true}, {Enabled: true, Running: true}} {
		v := Interests{Layout: Layout{Title: "Interests", Nav: NavInterests}, Rebuild: b}
		assert.True(t, v.FirstGrouping())
		out := render(t, r, PageInterests, v)
		assert.Contains(t, out, "<h2>Your library is being grouped for the first time</h2>")
		assert.NotContains(t, out, "No interests yet")
	}
	v := Interests{Layout: Layout{Title: "Interests", Nav: NavInterests}, Rebuild: Rebuild{Enabled: true}}
	assert.False(t, v.FirstGrouping())
	assert.Contains(t, render(t, r, PageInterests, v), "<h2>No interests yet</h2>")
	v.Run, v.Rebuild.Queued = &InterestRun{ID: "run-1"}, true
	assert.False(t, v.FirstGrouping(), "a rebuild of interests already found")
}

// TestInterestPage_AreaAndLoose: an interest's page names its area, and
// tags its loose fits, which its pager counts; an area's page lists its
// interests as cards, unpaged.
func TestInterestPage_AreaAndLoose(t *testing.T) {
	r := newRenderer(t)
	page := interestPage(2, 60)
	page.Interest.ParentID, page.Interest.ParentLabel, page.Interest.Loose = "a1", "Cloud <and> AWS", 3
	page.Interest.Members = append(page.Interest.Members, Member{DocumentID: "l1", Title: "Close", Fit: "loose",
		URL: "https://example.com/l1"})
	out := render(t, r, PageInterest, page)
	assert.Contains(t, out, `<nav class="crumbs" aria-label="Breadcrumb"><a href="/ui/interests">Interests</a>`+
		`<span class="sep">›</span><a class="truncate" href="/ui/interests/a1" title="Cloud &lt;and&gt; AWS">Cloud &lt;and&gt; AWS</a>`+
		`<span class="sep">›</span><span class="truncate" aria-current="page" title="AWS Serverless Architecture">`+
		`AWS Serverless Architecture</span></nav>`)
	assert.Contains(t, out, `<span class="badge plain">3 loose fits</span>`)
	assert.Equal(t, 1, strings.Count(out, `<h2 id="loose-fits">Loose fits</h2>`))
	members, loose := strings.Index(out, `<tr class="fit-member">`), strings.Index(out, `id="loose-fits"`)
	assert.Less(t, members, loose, "the members, then the loose fits")
	assert.Equal(t, 1, strings.Count(out[loose:], `<tr class="fit-`), "the loose fit under its heading alone")
	assert.Contains(t, out[loose:], `<td class="num muted">61</td>`, "ranked after the members")
	assert.Contains(t, out, `<span class="pager-summary">Documents 51–61 of 63, most similar first</span>`)
	assert.Equal(t, 2, page.Pages())

	area := InterestPage{Layout: Layout{Title: "Cloud", Nav: NavInterests}, Page: 1, Interest: Interest{ID: "a1",
		Area: true, Size: 120, NumChildren: 2, Children: []Interest{{ID: "i1", Label: "AWS", Size: 70},
			{ID: "i2", Label: "GCP", Size: 50}}}}
	out = render(t, r, PageInterest, area)
	assert.Regexp(t, `<h1><svg class="icon"[^>]*>.*?</svg>Unlabeled area</h1>`, out)
	assert.Contains(t, out, `<span class="truncate" aria-current="page" title="Unlabeled area">Unlabeled area</span></nav>`)
	assert.Contains(t, out, `<span class="badge plain">2 interests</span>`)
	assert.Equal(t, 2, strings.Count(out, `<li class="card interest">`))
	assert.NotContains(t, out, "<table")
	assert.Nil(t, area.Pager())
	assert.Equal(t, 1, area.Pages())
	area.Page = 2
	assert.NotNil(t, area.OutOfRange(), "an area's interests are paged")
}

// TestInterests_MemberNames: a card names its members as the Library
// names documents: by title, then by a bookmark's title in italics, then
// by address, the whole of it on hover.
func TestInterests_MemberNames(t *testing.T) {
	r := newRenderer(t)
	page := interestsPage(1)
	page.Interests[0].Members = []Member{
		{DocumentID: "d1", Title: "A <title>", BookmarkTitle: "ignored", URL: "https://example.com/a"},
		{DocumentID: "d2", BookmarkTitle: "AWS Serverless Application Lens",
			URL: "https://d1.awsstatic.com/whitepapers/AWS-Serverless-Applications-Lens.pdf"},
		{DocumentID: "d3", URL: "https://example.com/b/?q=1"},
	}
	out := render(t, r, PageInterests, page)
	assert.Contains(t, out, `<ul class="members">`+
		`<li><a href="/ui/documents/d1" title="A &lt;title&gt;">A &lt;title&gt;</a></li>`+
		`<li><a class="from-bookmark" href="/ui/documents/d2" title="AWS Serverless Application Lens">`+
		`AWS Serverless Application Lens</a></li>`+
		`<li><a class="untitled" href="/ui/documents/d3" title="https://example.com/b/?q=1">example.com/b?q=1</a></li>`+
		`</ul>`)
}

// interestPage is page of an interest of size members, with the members
// the page holds, each named by its bookmark.
func interestPage(page, size int) InterestPage {
	in := Interest{ID: "i1", Label: "AWS Serverless Architecture", Size: size, Cohesion: 0.59}
	first := PageOffset(page, InterestMembersPageSize)
	for i := first; i < min(first+InterestMembersPageSize, size); i++ {
		in.Members = append(in.Members, Member{DocumentID: "d" + strconv.Itoa(i), BookmarkTitle: "Saved " + strconv.Itoa(i),
			URL: "https://example.com/" + strconv.Itoa(i), Similarity: 0.5, Fit: "member"})
	}
	return InterestPage{Layout: Layout{Title: in.Label, Nav: NavInterests}, Interest: in, Page: page}
}

// TestInterestPage_Pages: an interest's members are ranked across its
// pages, with a pager under them that counts the documents shown of all of
// them, and the head names the run the interest comes from.
func TestInterestPage_Pages(t *testing.T) {
	r := newRenderer(t)
	one := interestPage(1, 84)
	one.RunAt = time.Date(2026, 9, 28, 10, 34, 0, 0, time.Local)
	out := render(t, r, PageInterest, one)
	assert.Contains(t, out, `cohesion 0.59</span><span class="sep">·</span><span class="text">run of 2026-09-28 10:34</span>`+
		`<a class="map-link" href="/ui/interests/map?select=interest%3Ai1">`)
	assert.NotContains(t, out, "showing")
	assert.Contains(t, out, `<td class="num muted">1</td>`)
	assert.Contains(t, out, `<td class="num muted">50</td>`)
	assert.NotContains(t, out, `<td class="num muted">51</td>`)
	assert.Contains(t, out, `<span class="pager-summary">Documents 1–50 of 84, most similar first</span>`)
	assert.Contains(t, out, `<a href="/ui/interests/i1?page=2" aria-label="Page 2">2</a>`)
	assert.Contains(t, out, `<a class="step" id="pager-next" href="/ui/interests/i1?page=2" rel="next">`)

	two := interestPage(2, 84)
	out = render(t, r, PageInterest, two)
	assert.NotContains(t, out, "run of", "no run time, no run line")
	assert.Contains(t, out, `<td class="num muted">51</td>`)
	assert.Contains(t, out, `<td class="num muted">84</td>`)
	assert.Equal(t, 34, strings.Count(out, `<td class="num muted">`))
	assert.Contains(t, out, `<span class="pager-summary">Documents 51–84 of 84, most similar first</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-prev" href="/ui/interests/i1" rel="prev">`)
	assert.Equal(t, []int{51, 52}, []int{two.RankedMembers()[0].Rank, two.RankedMembers()[1].Rank})

	deep := interestPage(25, 1300)
	assert.Contains(t, render(t, r, PageInterest, deep), `<td class="num muted">1,201</td>`, "a rank grouped as a count")

	whole := interestPage(1, 50)
	assert.NotContains(t, render(t, r, PageInterest, whole), `class="pager"`, "one page needs no pager")
}

// TestInterestPage_OutOfRange: a page past the last keeps the interest's
// head, and in place of the table leads to the first and last pages.
func TestInterestPage_OutOfRange(t *testing.T) {
	r := newRenderer(t)
	page := interestPage(3, 84)
	require.NotNil(t, page.OutOfRange())
	page.Interest.Summary = "Guides to serverless AWS."
	out := render(t, r, PageInterest, page)
	assert.Contains(t, out, `<nav class="crumbs" aria-label="Breadcrumb"><a href="/ui/interests">Interests</a>`+
		`<span class="sep">›</span><span class="truncate" aria-current="page" title="AWS Serverless Architecture">`+
		`AWS Serverless Architecture</span></nav>`, "a flat interest: no area in its crumbs")
	assert.Contains(t, out, "<h1>AWS Serverless Architecture</h1>")
	assert.Contains(t, out, `<p class="lede">Guides to serverless AWS.</p>`)
	assert.Contains(t, out, `<span class="badge badge-accent plain">84 documents</span>`)
	assert.Contains(t, out, "<h2>No page 3</h2>\n<p>This list has 2 pages.</p>")
	assert.Contains(t, out, `<a class="btn" href="/ui/interests/i1">First page</a> `+
		`<a class="btn" href="/ui/interests/i1?page=2">Last page</a>`)
	assert.NotContains(t, out, "<table")
	assert.Nil(t, interestPage(2, 84).OutOfRange())
}

// TestMember_Cell: an interest's member is named as the Library names a
// document, with its short URL under a name and its host under an
// address.
func TestMember_Cell(t *testing.T) {
	const url = "https://example.com/a/b?q=1"
	named := Member{DocumentID: "d1", BookmarkTitle: "Saved", URL: url}.Cell()
	assert.Equal(t, DocRef{ID: "d1", Fallback: "Saved", URL: url}, named.Ref)
	assert.Equal(t, "from-bookmark", named.Ref.Class())
	assert.Equal(t, "example.com/a/b?q=1", named.Where)
	titled := Member{DocumentID: "d1", Title: "Title", URL: url}.Cell()
	assert.Equal(t, "example.com/a/b?q=1", titled.Where)
	address := Member{DocumentID: "d1", URL: url}.Cell()
	assert.Equal(t, "untitled", address.Ref.Class())
	assert.Equal(t, "example.com", address.Where)

	out := render(t, newRenderer(t), PageInterest, interestPage(1, 1))
	assert.Contains(t, out, `<a class="doc-title from-bookmark" href="/ui/documents/d0" title="Saved 0">Saved 0</a>`+
		"\n"+`<span class="doc-sub"><span class="host" title="https://example.com/0">example.com/0</span>`)
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
	doc.Meta.BookmarkTitle = "Saved as <this>"
	assert.Contains(t, render(t, r, PageDocument, doc), `<h1 class="from-bookmark">Saved as &lt;this&gt;</h1>`,
		"or by its bookmark's title, styled as a fallback")
	doc.Meta.Title = "Its own"
	assert.Contains(t, render(t, r, PageDocument, doc), "<h1>Its own</h1>", "a title of its own comes first")
	doc.Meta.Title, doc.Meta.BookmarkTitle = "", ""
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
// degraded upstream, each a note, since the region is polled. None shows
// when the health read failed, and a healthy daemon shows none.
func TestStatus_Attention(t *testing.T) {
	r := newRenderer(t)
	at := time.Now().Add(-5 * time.Minute)
	st := Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Health: &HealthPanel{
		OllamaDetail: "ollama unreachable (start it with `ollama serve`)",
		Drift: &Drift{Changes: []DriftChange{{What: "model digest", Recorded: "a", Current: "b"}}, Verified: true,
			Detail: "64 of 64 sampled chunks changed (worst cosine 0.9713)", Fix: "curio reindex --all"},
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
		{"callout-danger", "note", "Ollama isn't ready"},
		{"callout-warn", "note", "The embeddings drifted"},
		{"callout-warn", "note", "Jina Reader is degraded"},
		{"callout-danger", "note", "other is failing"},
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
	assert.Contains(t, out, "<p>Re-embedded sample: 64 of 64 sampled chunks changed (worst cosine 0.9713)</p>")

	st.Health.Err = &PanelError{Message: "health", RequestID: "r"}
	assert.NotContains(t, render(t, r, PageStatus, st), `class="callout`, "no health read, no callouts")

	healthy := Status{Layout: st.Layout, Health: &HealthPanel{OllamaReachable: true,
		Upstreams: []Upstream{{Name: "jina", Enabled: true, State: "ok"}}}}
	assert.NotContains(t, render(t, r, PageStatus, healthy), `class="callout`)
}

// TestStatus_UnverifiedDrift: a drift no sample could verify says it may
// have happened, in its callout and its Health row, with why.
func TestStatus_UnverifiedDrift(t *testing.T) {
	r := newRenderer(t)
	st := Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Health: &HealthPanel{OllamaReachable: true,
		Drift: &Drift{Changes: []DriftChange{{What: "Ollama", Recorded: "0.34.4", Current: "0.35.0"}},
			Detail: "not verified: after 3 attempts: ollama unreachable", Fix: "curio reindex --all"}}}
	out := render(t, r, PageStatus, st)

	callouts := calloutTitleRE.FindAllStringSubmatch(out, -1)
	require.Len(t, callouts, 1)
	assert.Equal(t, []string{"callout-warn", "note", "The embeddings may have drifted"}, callouts[0][1:])
	assert.NotContains(t, out, "The embeddings drifted")
	assert.Contains(t, out, `<span class="sub">0 dimensions, may have drifted</span>`)
	assert.Contains(t, out, "<p>Re-embedded sample: not verified: after 3 attempts: ollama unreachable</p>")
}

// TestStatus_Failures: the causes, most first, each leading to its card on
// the Failures tab, with a bar scaled to the most; the card leads to the
// tab; none read, no card.
func TestStatus_Failures(t *testing.T) {
	r := newRenderer(t)
	st := Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Health: &HealthPanel{OllamaReachable: true},
		Failures: &FailuresPanel{Total: 1745, Causes: []Count{{Name: "anti_bot", Count: 926}, {Name: "dead_link", Count: 819},
			{Name: "new_cause", Count: 1}}}}
	out := render(t, r, PageStatus, st)
	assert.Contains(t, out, `<a class="more" href="/ui/failures">All failures →</a>`)
	assert.Contains(t, out, `<li><a class="label" href="/ui/failures#cause-anti_bot" title="Blocked by bot protection">`+
		`Blocked by bot protection</a><svg class="stackbar" viewBox="0 0 100 10" preserveAspectRatio="none" aria-hidden="true">`+
		`<rect class="fill-track" x="0" y="0" width="100" height="10"/><rect class="fill-danger" x="0.000" y="0" width="100.000" height="10"/></svg>`+
		`<span class="n">926</span></li>`)
	assert.Contains(t, out, `<rect class="fill-neutral" x="0.000" y="0" width="88.445" height="10"/></svg><span class="n">819</span>`)
	assert.Contains(t, out, `<a class="label" href="/ui/failures#cause-dead_link" title="Dead link">`)
	assert.Contains(t, out, `<a class="label" href="/ui/failures#cause-new_cause" title="new_cause">`,
		"a cause this build doesn't know leads to its card too, which the Library would refuse")
	assert.NotContains(t, out, "style=")

	st.Failures = &FailuresPanel{}
	assert.NotContains(t, render(t, r, PageStatus, st), "Why documents failed", "nothing failed")
	st.Failures.Err = &PanelError{Message: "failures", RequestID: "r"}
	assert.Contains(t, render(t, r, PageStatus, st), "Couldn't read this: failures.")
	st.Failures = nil
	assert.NotContains(t, render(t, r, PageStatus, st), "Why documents failed", "not read")
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
	for _, tab := range (Search{Query: "kafka", Type: "video", Page: 3}).TypeTabs() {
		assert.NotContains(t, tab.Href, "page", "%s: another type starts at page 1", tab.Label)
	}

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
		assert.NotContains(t, render1(t, stateTabs(t, out)), `class="count"`, "%+v: the tabs count no filtered list", f)
		assert.Contains(t, out, `>Failures <span class="count">2,969</span></a>`,
			"%+v: the subnav counts the whole library's failures", f)
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

// stateTabs is the Library's state tabs in the page out.
func stateTabs(t *testing.T, out string) *html.Node {
	t.Helper()
	for n := range parse(t, out).Descendants() {
		if n.Type == html.ElementNode && attrValue(n, "role") == "group" && attrValue(n, "aria-label") == "State" {
			return n
		}
	}
	require.Fail(t, "no state tabs")
	return nil
}

// render1 is n's markup.
func render1(t *testing.T, n *html.Node) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, html.Render(&b, n))
	return b.String()
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

// TestSearch_Paging: how many pages a search's results fill, when the page
// is past the last or says the ranking stops at the cap, and its pager,
// which counts the results the page shows and links pages of the same
// query and type.
func TestSearch_Paging(t *testing.T) {
	page := func(n, total int, capped bool) Search {
		hits := make([]SearchHit, max(0, min(SearchPageSize, total-PageOffset(n, SearchPageSize))))
		return Search{Query: "kafka", Type: "pdf", Page: n,
			Results: &SearchResults{Total: total, Capped: capped, Hits: hits}}
	}
	for _, tc := range []struct {
		search             Search
		pages              int
		outOfRange, capped bool
		summary, items     string // "" without a pager
	}{
		{page(1, 37, false), 4, false, false, "Results 1–10 of 37", "1* 2 3 4"},
		{page(4, 37, false), 4, false, false, "Results 31–37 of 37", "1 2 3 4*"},
		{page(5, 37, false), 4, true, false, "", ""},
		{page(1, 7, false), 1, false, false, "", ""},
		{page(2, 7, false), 1, true, false, "", ""},
		{page(5, 100, true), 10, false, false, "Results 41–50 of 100", "1 … 4 5* 6 … 10"},
		{page(9, 100, true), 10, false, false, "Results 81–90 of 100", "1 … 8 9* 10"},
		{page(10, 100, true), 10, false, true, "Results 91–100 of 100", "1 … 9 10*"},
		{page(10, 100, false), 10, false, false, "Results 91–100 of 100", "1 … 9 10*"},
		{page(1, 0, false), 0, false, false, "", ""},
		{page(3, 0, false), 0, false, false, "", ""},
	} {
		s := tc.search
		name := fmt.Sprintf("page %d of %d results", s.Page, s.Results.Total)
		assert.Equal(t, tc.pages, s.Pages(), name)
		assert.Equal(t, tc.capped, s.CappedNote(), name)
		if tc.outOfRange {
			assert.Equal(t, &PageOutOfRange{Page: s.Page, Pages: tc.pages, First: "/ui/?content_type=pdf&q=kafka",
				Last: searchHref("kafka", "pdf", tc.pages)}, s.OutOfRange(), name)
		} else {
			assert.Nil(t, s.OutOfRange(), "%s: nothing matched, or the page has results", name)
		}
		if tc.summary == "" {
			assert.Nil(t, s.Pager(), "%s: one page, or past the last", name)
			continue
		}
		p := s.Pager()
		require.NotNil(t, p, name)
		assert.Equal(t, tc.summary, p.Summary, name)
		assert.Equal(t, tc.items, pagerItems(p), name)
		for _, item := range p.Items {
			if !item.Gap() {
				assert.Equal(t, searchHref("kafka", "pdf", item.Page), item.Href, name)
			}
		}
	}
	assert.Equal(t, "Results 1–10 of 37", Search{Query: "kafka",
		Results: &SearchResults{Total: 37, Hits: make([]SearchHit, 10)}}.Pager().Summary, "page 0 is the first")
	assert.Zero(t, Search{}.Pages(), "no results, no pages")
	assert.Nil(t, Search{}.Pager())
	assert.Nil(t, Search{}.OutOfRange())
}

// TestSearchResults_Count: the results' head counts the documents that
// match, and past the cap says there are more.
func TestSearchResults_Count(t *testing.T) {
	for _, tc := range []struct {
		results     SearchResults
		count, verb string
	}{
		{SearchResults{Total: 1}, "1 document", "matches"},
		{SearchResults{Total: 37}, "37 documents", "match"},
		{SearchResults{Total: 100}, "100 documents", "match"},
		{SearchResults{Total: 100, Capped: true}, "100+ documents", "match"},
	} {
		assert.Equal(t, tc.count, tc.results.Count())
		assert.Equal(t, tc.verb, tc.results.Verb(), tc.count)
	}
}

// TestSearchHit_MatchKind: a result says which searches found its
// passages: its words, their meaning, or both.
func TestSearchHit_MatchKind(t *testing.T) {
	bm25, vector := new(22.5), new(0.71)
	for _, tc := range []struct {
		matches []Match
		want    string
	}{
		{nil, ""},
		{[]Match{{BM25: bm25}}, "keyword only"},
		{[]Match{{Vector: vector}, {Vector: vector}}, "meaning only"},
		{[]Match{{BM25: bm25, Vector: vector}}, "keyword + meaning"},
		{[]Match{{BM25: bm25}, {Vector: vector}}, "keyword + meaning"},
		{[]Match{{}}, ""},
	} {
		assert.Equal(t, tc.want, SearchHit{Matches: tc.matches}.MatchKind(), "%+v", tc.matches)
	}
}

// TestMatch_Scores: a passage's scores, for Show scores, name each
// retriever that returned it.
func TestMatch_Scores(t *testing.T) {
	assert.Equal(t, "bm25 22.523 · vector 0.710", Match{BM25: new(22.523), Vector: new(0.71)}.Scores())
	assert.Equal(t, "bm25 22.523", Match{BM25: new(22.523)}.Scores())
	assert.Equal(t, "vector 0.710", Match{Vector: new(0.71)}.Scores())
	assert.Empty(t, Match{}.Scores())
}

// TestSearch_ResultsPage: a page of results: the head counts the ranking,
// with the Show scores toggle; every score is in a .score span; a result
// says how it matched and is named as a list names a document; the pager,
// boosted to swap the results in, sits under them; the last page of a
// capped search says so, and a page past the end says how many there are.
func TestSearch_ResultsPage(t *testing.T) {
	r := newRenderer(t)
	layout := Layout{Title: "Search", Nav: NavSearch}
	hits := []SearchHit{
		{DocumentID: "titled", Title: "Kafka partitions", URL: "https://example.com/kafka", ContentType: "article",
			Score: 0.0328, Matches: []Match{{Segments: Passage("<em>kafka</em> partitions", ""), BM25: new(22.523),
				Vector: new(0.71)}}},
		{DocumentID: "saved", BookmarkTitle: "Saved as <this>", URL: "https://example.com/saved", Score: 0.03,
			Matches: []Match{{Segments: []Segment{{Text: "text"}}, Vector: new(0.5)}}},
		{DocumentID: "bare", URL: "https://example.com/some/where", Score: 0.02},
	}
	out := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Page: 2,
		Results: &SearchResults{Total: 37, TookMS: 12, Hits: hits}})
	assert.Contains(t, out, `<div class="results-head"><span><strong>37 documents</strong> match · 12 ms</span>`+
		`<label class="toggle"><input type="checkbox" id="show-scores" hx-preserve="true"> Show scores</label></div>`)
	assert.Contains(t, out, `<mark>kafka</mark> partitions <span class="score">bm25 22.523 · vector 0.710</span></p>`)
	assert.Contains(t, out, `<span class="badge badge-accent plain">keyword &#43; meaning</span><span class="score">score 0.0328</span>`)
	assert.Contains(t, out, `<span class="badge badge-accent plain">meaning only</span>`)
	assert.NotRegexp(t, `(bm25|vector|score) \d`, regexp.MustCompile(`<span class="score">[^<]*</span>`).ReplaceAllString(out, ""),
		"no score outside .score")
	assert.Contains(t, out, `<h2 class="result-title"><a href="/ui/documents/titled" title="Kafka partitions">Kafka partitions</a></h2>`)
	assert.Contains(t, out, `<a href="/ui/documents/saved" title="Saved as &lt;this&gt;" class="from-bookmark">Saved as &lt;this&gt;</a>`)
	assert.Contains(t, out, `<a href="/ui/documents/bare" title="https://example.com/some/where" class="untitled">example.com/some/where</a>`)
	assert.Contains(t, out, `<span class="path" title="https://example.com/saved"><b>example.com</b> › saved</span>`,
		"the address stays above a result named by its bookmark")

	assert.Contains(t, out, `<div hx-boost="true" hx-target="#results" hx-select="#results > *" hx-swap="innerHTML show:#results:top"`+
		` hx-sync="closest .search-page:drop"><nav class="pager" aria-label="Pages"><span class="pager-summary">Results 11–13 of 37</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-prev" href="/ui/?q=kafka" rel="prev">`)
	assert.Contains(t, out, `<a href="/ui/?q=kafka" aria-label="Page 1">1</a><a href="/ui/?page=2&amp;q=kafka" aria-label="Page 2" aria-current="page">2</a>`+
		`<a href="/ui/?page=3&amp;q=kafka" aria-label="Page 3">3</a><a href="/ui/?page=4&amp;q=kafka" aria-label="Page 4">4</a><span class="of">Page 2 of 4</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-next" href="/ui/?page=3&amp;q=kafka" rel="next">Next<svg`)
	assert.Less(t, strings.Index(out, `</ol>`), strings.Index(out, `<nav class="pager"`), "under the results")
	assert.NotContains(t, out, "results-note")
	assert.NotContains(t, out, `name="page"`, "typing starts again at page 1")
	assert.Contains(t, out, `hx-sync="closest .search-page:replace"`, "a keystroke aborts a page in flight")

	first := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Page: 1,
		Results: &SearchResults{Total: 37, Hits: hits}})
	assert.Contains(t, first, `<span class="step" aria-disabled="true"><svg`)
	assert.NotContains(t, first, `id="pager-prev"`, "no step back from the first page")
	last := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Page: 10,
		Results: &SearchResults{Total: 100, Capped: true, TookMS: 9, Hits: hits}})
	assert.Contains(t, last, `<strong>100&#43; documents</strong> match · showing the best 100 · 9 ms</span>`)
	assert.Contains(t, last, `<p class="results-note">curio ranks the best 100 matches; refine the query to see others.</p>`)
	assert.NotContains(t, last, `id="pager-next"`, "no step on from the last page")
	assert.Contains(t, last, `<span class="step" aria-disabled="true">Next<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">`+
		`<path d="m9 18 6-6-6-6"/></svg></span></div></nav>`)

	past := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Page: 5,
		Results: &SearchResults{Total: 37}})
	assert.Contains(t, past, `<strong>37 documents</strong> match`)
	assert.Contains(t, past, `<div class="scores-kept"><input type="checkbox" id="show-scores" hx-preserve="true"`,
		"no scores to show, but the checkbox stays, unseen, so its state carries to the next results")
	assert.NotContains(t, past, "Show scores")
	assert.Contains(t, past, `<h2>No page 5</h2>`+"\n"+`<p>This list has 4 pages.</p>`+"\n"+
		`<p class="mt-2"><a class="btn" href="/ui/?q=kafka">First page</a> <a class="btn" href="/ui/?page=4&amp;q=kafka">Last page</a></p>`)
	assert.NotContains(t, past, `class="pager"`, "the card leads to the first and last pages")
	assert.NotContains(t, past, "Nothing in your library matches")

	one := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Page: 1,
		Results: &SearchResults{Total: 1, Hits: hits[:1]}})
	assert.Contains(t, one, `<strong>1 document</strong> matches`)
	assert.NotContains(t, one, `class="pager"`, "one page, no pager")

	none := render(t, r, PageSearch, Search{Layout: layout, Query: "kafka", Page: 1, Results: &SearchResults{}})
	assert.Contains(t, none, "<h2>Nothing in your library matches</h2>")
	assert.NotContains(t, none, "results-head")
	assert.Contains(t, none, `<div class="scores-kept"><input type="checkbox" id="show-scores" hx-preserve="true"`,
		"a query that matched nothing keeps Show scores for the next one")
}

// TestQueuePanel_Controls: the controls follow the settings: Resume while
// paused, the throttle's button pressed, the schedule's times in the form,
// and a keep-awake hint that says, in the keeper's order, why it doesn't
// hold the Mac awake.
func TestQueuePanel_Controls(t *testing.T) {
	assert.Equal(t, ActionPause, QueuePanel{}.Toggle().Kind)
	assert.Equal(t, ActionResume, QueuePanel{Paused: true}.Toggle().Kind)

	choices := QueuePanel{Throttle: "gentle"}.Throttles()
	require.Len(t, choices, 2)
	assert.Equal(t, []string{"throttle-normal", "Normal"}, []string{choices[0].ID, choices[0].Label})
	assert.Equal(t, []string{"throttle-gentle", "Gentle"}, []string{choices[1].ID, choices[1].Label})
	assert.Equal(t, []bool{false, true}, []bool{choices[0].Pressed, choices[1].Pressed})
	for _, c := range choices {
		assert.Equal(t, ActionThrottle, c.Action.Kind)
	}
	assert.Equal(t, "Gentle: at most 4 fetches and 1 index at a time.", gentleHint(), "from the gentle caps")

	set := QueuePanel{Schedule: "22:00-07:00"}
	assert.Equal(t, []string{"22:00", "07:00"}, []string{set.ScheduleStart(), set.ScheduleEnd()})
	assert.Empty(t, QueuePanel{}.ScheduleStart()+QueuePanel{}.ScheduleEnd())

	busy := []KindLoad{{Kind: "fetch", Pending: 3, Running: 1}, {Kind: "index", Pending: 2}}
	assert.Equal(t, 5, QueuePanel{Kinds: busy}.Waiting())
	for _, tc := range []struct {
		panel QueuePanel
		want  string
	}{
		{QueuePanel{Kinds: busy}, "Off: jobs wait while the Mac sleeps."},
		{QueuePanel{KeepAwake: true, KeepAwakeActive: true, PowerSource: "ac", Kinds: busy},
			"Holding the Mac awake while jobs run (AC power)."},
		{QueuePanel{KeepAwake: true, Paused: true, PowerSource: "ac", Kinds: busy},
			"On, not holding the Mac awake: the queue is paused."},
		{QueuePanel{KeepAwake: true, PowerSource: "ac"}, "On, not holding the Mac awake: nothing is queued."},
		{QueuePanel{KeepAwake: true, PowerSource: "ac", Kinds: []KindLoad{{Kind: "index", Running: 1}}},
			"On, not holding the Mac awake yet."},
		{QueuePanel{KeepAwake: true, PowerSource: "battery", Kinds: busy},
			"On, not holding the Mac awake: it runs on battery."},
		{QueuePanel{KeepAwake: true, PowerSource: "unknown", Kinds: busy},
			"On, not holding the Mac awake: its power source is unknown."},
	} {
		assert.Equal(t, tc.want, tc.panel.KeepAwakeHint())
	}
}

// TestStatus_Pollers: the page polls what changes every 2 seconds and
// after every change, and health every 15 seconds; a poll's answer has no
// pollers of its own.
func TestStatus_Pollers(t *testing.T) {
	pollers := Status{}.Pollers()
	require.Len(t, pollers, 2)
	assert.Equal(t, "/ui/status?poll=live", pollers[0].Href)
	assert.Equal(t, "every 2s, curio:changed from:body", pollers[0].Trigger())
	assert.Equal(t, "#library-live,#queue-state,#queue-toggle,#queue-throttle,#queue-keep-awake,#keep-awake-hint,"+
		"#schedule-state,#queue-pools,#progress-live,#jobs-live", pollers[0].Select())
	assert.Equal(t, "/ui/status?poll=health", pollers[1].Href)
	assert.Equal(t, "every 15s", pollers[1].Trigger())
	assert.Equal(t, "#attention,#health-live", pollers[1].Select())
	assert.Empty(t, Status{Poll: PollLive}.Pollers())
	assert.Empty(t, Status{Poll: PollHealth}.Pollers())
}

// TestStatus_Controls: the queue card's controls say the settings, send
// their changes, and need JavaScript; their equivalents without it are
// the settings and the commands. No phase badge is left.
func TestStatus_Controls(t *testing.T) {
	r := newRenderer(t)
	queue := &QueuePanel{Open: true, Throttle: "normal", KeepAwake: false, Kinds: []KindLoad{{Kind: "fetch", Limit: 16}}}
	st := Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Queue: queue}
	doc := parse(t, render(t, r, PageStatus, st))
	pause := byID(doc, "queue-pause")
	require.NotNil(t, pause)
	assert.Equal(t, "pause", attrValue(pause, "data-kind"))
	assert.Contains(t, attrValue(byID(doc, "queue-toggle"), "class"), "js-only")
	assert.Equal(t, "true", attrValue(byID(doc, "throttle-normal"), "aria-pressed"))
	assert.Equal(t, "false", attrValue(byID(doc, "throttle-gentle"), "aria-pressed"))
	assert.Equal(t, `{"throttle":"gentle"}`, attrValue(byID(doc, "throttle-gentle"), "data-body"))
	awake := byID(doc, "keep-awake")
	assert.False(t, hasAttr(awake, "checked"))
	assert.Equal(t, "switch", attrValue(awake, "role"))
	assert.Equal(t, "keep_awake", attrValue(awake, "data-field"))
	assert.Empty(t, attrValue(byID(doc, "schedule-opens"), "value"), "no schedule")
	assert.Equal(t, "-", attrValue(byID(doc, "schedule-form"), "data-join"))
	for _, n := range withAttr(doc, "class") {
		if strings.Contains(attrValue(n, "class"), "controls") {
			assert.Contains(t, attrValue(n, "class"), "js-only")
		}
	}
	out := render(t, r, PageStatus, st)
	assert.Contains(t, out, `<dl class="facts mt-4 no-js">`)
	assert.Contains(t, out, `<div class="cli mt-4 no-js"><span class="muted">From the terminal</span><code>curio pause</code>`)
	assert.Contains(t, out, `<span class="why">Nothing waiting</span>`)
	assert.NotContains(t, out, "Phase 2")

	queue.Paused, queue.Open, queue.Reason, queue.Throttle, queue.KeepAwake = true, false, "paused", "gentle", true
	queue.Schedule = "22:00-07:00"
	doc = parse(t, render(t, r, PageStatus, st))
	assert.Equal(t, "resume", attrValue(byID(doc, "queue-pause"), "data-kind"))
	assert.Equal(t, `{"paused":false}`, attrValue(byID(doc, "queue-pause"), "data-body"))
	assert.Equal(t, "true", attrValue(byID(doc, "throttle-gentle"), "aria-pressed"))
	assert.True(t, hasAttr(byID(doc, "keep-awake"), "checked"))
	assert.Equal(t, "22:00", attrValue(byID(doc, "schedule-opens"), "value"))
	assert.Equal(t, "07:00", attrValue(byID(doc, "schedule-closes"), "value"))
	out = render(t, r, PageStatus, st)
	assert.Contains(t, out, `<span class="badge queue-closed">closed</span><span class="why">Paused</span>`)
	assert.Contains(t, out, `<p class="schedule-now" id="schedule-state">Jobs start from 22:00 to 07:00.</p>`)
}

// TestStatus_Polls: a poll's answer holds the panels it read, and the
// frame, and nothing else: no health, callouts or failures in the 2-second
// poll's, and only those in the health poll's.
func TestStatus_Polls(t *testing.T) {
	r := newRenderer(t)
	layout := Layout{Title: "Status", Nav: NavStatus}
	live := render(t, r, PageStatus, Status{Layout: layout, Poll: PollLive, Counts: &CountsPanel{},
		Queue: &QueuePanel{Open: true}, Progress: &ProgressPanel{}})
	for _, gone := range []string{`id="health"`, `id="attention"`, "Why documents failed", "data-poll"} {
		assert.NotContains(t, live, gone)
	}
	for _, there := range []string{`id="library-live"`, `id="queue-state"`, `id="progress-live"`, `id="jobs-live"`} {
		assert.Contains(t, live, there)
	}
	health := render(t, r, PageStatus, Status{Layout: layout, Poll: PollHealth, Health: &HealthPanel{}})
	assert.Contains(t, health, `id="attention"`)
	assert.Contains(t, health, `id="health-live"`)
	for _, gone := range []string{`id="counts"`, `id="queue"`, `id="progress"`, `id="jobs"`, "Why documents failed"} {
		assert.NotContains(t, health, gone)
	}
}

// TestStatus_ProgressClosed: a closed queue's progress says why, as the
// queue's state line does.
func TestStatus_ProgressClosed(t *testing.T) {
	r := newRenderer(t)
	for reason, want := range map[string]string{"paused": "Paused", "outside_schedule": "Outside its schedule",
		evilScript: "Closed"} {
		st := Status{Layout: Layout{Title: "Status"}, Progress: &ProgressPanel{Reason: reason,
			Progress: EstimateProgress([]KindWork{{Kind: "fetch", Pending: 2}}, ProgressWindow, false)}}
		assert.Contains(t, render(t, r, PageStatus, st), "<p>2 jobs queued. "+want+": none start until the queue opens.</p>")
	}
}

// TestStatus_Progress: each state of the progress estimate, and the jobs
// due later beside the work due now; the queue's state line doesn't say it
// works while every job waits for a later time.
func TestStatus_Progress(t *testing.T) {
	r := newRenderer(t)
	soon := time.Now().Add(24*time.Minute + 30*time.Second)
	progressOf := func(open bool, work ...KindWork) string {
		st := Status{Layout: Layout{Title: "Status"}, Progress: &ProgressPanel{Reason: "paused",
			Progress: EstimateProgress(work, ProgressWindow, open)}}
		return textOf(byID(parse(t, render(t, r, PageStatus, st)), "progress-live"))
	}
	cases := []struct {
		name string
		open bool
		work []KindWork
		want string
	}{
		{"idle", true, nil, "Nothing queued."},
		{"closed", false, []KindWork{{Kind: "fetch", Pending: 5, DueLater: 3, NextDue: soon}},
			"5 jobs queued. Paused: none start until the queue opens."},
		{"waiting", true, []KindWork{{Kind: "fetch", Pending: 171, DueLater: 171, NextDue: soon}},
			"171 jobs due later, the first in 24 min: none can run before then."},
		{"stalled", true, []KindWork{{Kind: "fetch", Pending: 5, DueLater: 3, NextDue: soon}},
			"2 jobs queued, and none finished in the last 10m. 3 more jobs are due later, the first in 24 min."},
		{"running", true, []KindWork{{Kind: "fetch", Pending: 21, DueLater: 1, NextDue: soon, Finished: 10}},
			"20 jobs left≈ 20mAn estimate at the pace of the last 10m: 10 jobs finished, 1.0 a minute. " +
				"1 more job is due later, the first in 24 min."},
		{"running, nothing due later", true, []KindWork{{Kind: "fetch", Pending: 20, Finished: 10}},
			"20 jobs left≈ 20mAn estimate at the pace of the last 10m: 10 jobs finished, 1.0 a minute."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, strings.TrimSpace(progressOf(tc.open, tc.work...)))
		})
	}

	why := func(q QueuePanel) string {
		out := render(t, r, PageStatus, Status{Layout: Layout{Title: "Status"}, Queue: &q})
		return textOf(byID(parse(t, out), "queue-state"))
	}
	assert.Equal(t, "open171 jobs due later, the first in 24 min", why(QueuePanel{Open: true,
		Kinds: []KindLoad{{Kind: "fetch", Pending: 171, DueLater: 171, NextDue: soon}, {Kind: "index"}}}))
	assert.Equal(t, "openWorking: 172 jobs waiting", why(QueuePanel{Open: true,
		Kinds: []KindLoad{{Kind: "fetch", Pending: 171, DueLater: 171, NextDue: soon}, {Kind: "index", Pending: 1}}}),
		"one job can run")
	assert.Equal(t, "openWorking: 171 jobs waiting", why(QueuePanel{Open: true,
		Kinds: []KindLoad{{Kind: "fetch", Pending: 171, DueLater: 171, NextDue: soon, Running: 1}}}), "one runs")
}

// TestDocumentJobs: what came of a document's jobs against its page's
// baseline, and when its poller polls.
func TestDocumentJobs(t *testing.T) {
	at := time.Date(2026, 9, 29, 7, 0, 0, 0, time.UTC)
	page := DocumentBaseline{Updated: at, Extraction: "e1"}
	updated := DocumentBaseline{Updated: at.Add(time.Millisecond), Extraction: "e1"}
	newText := DocumentBaseline{Updated: at.Add(time.Second), Extraction: "e2"}
	fetching := []JobLine{{Kind: "fetch", Running: true, Attempts: 1}}
	for _, tc := range []struct {
		name string
		jobs DocumentJobs
		want DocumentOutcome
	}{
		{"unchanged", DocumentJobs{State: "fetched", Baseline: page, Current: page}, OutcomeNone},
		{"working", DocumentJobs{State: "pending", Baseline: page, Current: updated, Jobs: fetching}, OutcomeNone},
		{"a new text, still indexing", DocumentJobs{State: "fetched", Baseline: page, Current: newText,
			Jobs: []JobLine{{Kind: "index"}}}, OutcomeTextChanged},
		{"a first text", DocumentJobs{State: "fetched", Baseline: DocumentBaseline{Updated: at}, Current: newText},
			OutcomeTextChanged},
		{"failed", DocumentJobs{State: "failed", Baseline: page, Current: updated}, OutcomeFailed},
		{"dead", DocumentJobs{State: "dead", Baseline: page, Current: updated}, OutcomeFailed},
		{"updated", DocumentJobs{State: "fetched", Baseline: page, Current: updated}, OutcomeUpdated},
		{"jobs unread", DocumentJobs{State: "fetched", Baseline: page, Current: updated,
			Err: &PanelError{Message: "m"}}, OutcomeNone},
	} {
		assert.Equal(t, tc.want, tc.jobs.Outcome(), tc.name)
	}

	for _, tc := range []struct {
		jobs  DocumentJobs
		every bool
	}{
		{DocumentJobs{State: "fetched"}, false},
		{DocumentJobs{State: "fetched", Jobs: fetching}, true},
		{DocumentJobs{State: "pending"}, true},
		{DocumentJobs{State: "failed"}, false},
		{DocumentJobs{State: "fetched", Err: &PanelError{Message: "m"}}, true}, // a read that failed tries again
	} {
		p := tc.jobs.Poller()
		assert.Equal(t, tc.every, p.Every > 0, "%+v", tc.jobs)
		assert.Equal(t, []string{"doc-jobs", "doc-poll"}, p.Regions, "it lists itself")
		assert.True(t, p.OnChange)
	}
	p := DocumentJobs{DocumentID: "d1", Baseline: page, State: "pending"}.Poller()
	assert.Equal(t, documentPollHref("d1", page), p.Href, "it carries the page's baseline")
	assert.Equal(t, "every 2s, curio:changed from:body", p.Trigger())
	assert.Equal(t, "curio:changed from:body", DocumentJobs{State: "fetched"}.Poller().Trigger())

	assert.Equal(t, 3, JobLine{Kind: "fetch", Waiting: true, Attempts: 2}.Attempt(), "the attempt it waits to make")
	assert.Equal(t, 2, JobLine{Kind: "fetch", Running: true, Attempts: 2}.Attempt())
}

// TestDocument_Actions: each state's buttons: Refetch, the primary one for
// a failed document; for a dead link, Refetch anyway behind a confirm that
// forces it; Reindex only with a text. At rest the status says how the
// text was fetched.
func TestDocument_Actions(t *testing.T) {
	r := newRenderer(t)
	at := time.Now().Add(-13 * time.Hour)
	page := func(state string, ext *Extraction) *html.Node {
		return parse(t, render(t, r, PageDocument, Document{Layout: Layout{Title: "d"},
			Meta: DocumentMeta{ID: "d1", URL: "https://example.com/", State: state}, Extraction: ext,
			Text: TextPanel{State: TextNotFetched}, Jobs: DocumentJobs{DocumentID: "d1", State: state}}))
	}
	ext := &Extraction{Fetcher: "native", Via: "readability", FetchedAt: at}
	for _, state := range []string{"pending", "fetched", "failed"} {
		doc := page(state, ext)
		refetch := byID(doc, "refetch")
		require.NotNil(t, refetch, state)
		assert.Equal(t, "/v1/documents/d1/refetch", attrValue(refetch, "data-path"), state)
		assert.Equal(t, state == "failed", strings.Contains(attrValue(refetch, "class"), "btn-primary"), state)
		assert.Nil(t, byID(doc, "confirm-refetch"), state)
		assert.Equal(t, "/v1/documents/d1/reindex", attrValue(byID(doc, "reindex"), "data-path"), state)
	}
	fetched := render(t, r, PageDocument, Document{Layout: Layout{Title: "d"},
		Meta: DocumentMeta{ID: "d1", State: "fetched"}, Extraction: ext, Text: TextPanel{State: TextNotFetched}})
	assert.Regexp(t, `<div class="doc-actions js-only">`, fetched)
	assert.Regexp(t, `<span class="action-status" id="doc-status" role="status" aria-live="polite">Fetched <time[^>]*>13 h ago</time> by native via Readability</span>`, fetched)
	assert.NotContains(t, fetched, "Phase 2")

	dead := page("dead", nil)
	assert.Nil(t, byID(dead, "refetch"))
	anyway := byID(dead, "refetch-anyway")
	require.NotNil(t, anyway)
	assert.Equal(t, "confirm-refetch", attrValue(anyway, "popovertarget"))
	assert.False(t, hasAttr(anyway, "data-method"), "it only opens the confirm")
	confirm := byID(dead, "confirm-refetch")
	require.NotNil(t, confirm)
	assert.True(t, hasAttr(confirm, "popover"))
	assert.Equal(t, "alertdialog", attrValue(confirm, "role"))
	assert.Equal(t, "confirm-refetch-title", attrValue(confirm, "aria-labelledby"))
	assert.Equal(t, "confirm-refetch-text", attrValue(confirm, "aria-describedby"))
	cancel := byID(dead, "refetch-cancel")
	assert.True(t, hasAttr(cancel, "autofocus"))
	assert.Equal(t, "hide", attrValue(cancel, "popovertargetaction"))
	assert.False(t, hasAttr(cancel, "data-method"))
	forced := byID(dead, "refetch-forced")
	assert.Equal(t, "/v1/documents/d1/refetch?force=1", attrValue(forced, "data-path"))
	assert.Equal(t, "hide", attrValue(forced, "popovertargetaction"))
	reindex := byID(dead, "reindex")
	assert.Equal(t, "true", attrValue(reindex, "aria-disabled"), "nothing to reindex")
	assert.NotEmpty(t, attrValue(reindex, "title"), "says why")
	assert.False(t, hasAttr(reindex, "data-method"))
	assert.Empty(t, textOf(byID(dead, "doc-status")), "no text, nothing to say at rest")
}

// TestDocument_JobLines: the jobs in flight by kind, whether they run, are
// queued or wait for a time, their attempt, when a waiting job is due and
// why it waits, why the queue holds them, and each outcome's reload link.
// A pending job never reads as failed.
func TestDocument_JobLines(t *testing.T) {
	r := newRenderer(t)
	at := time.Now().Add(4 * time.Minute)
	jobsOf := func(j DocumentJobs) string {
		j.DocumentID, j.AttemptLimit = "d1", 5
		out := render(t, r, PageDocument, Document{Layout: Layout{Title: "d"}, Poll: PollJobs, Jobs: j})
		return regexp.MustCompile(`(?s)<div class="doc-jobs" id="doc-jobs">.*?</div>`).FindString(out)
	}
	assert.Contains(t, jobsOf(DocumentJobs{State: "pending", Jobs: []JobLine{{Kind: "fetch"}}}), "<li>Fetch queued</li>")
	assert.Contains(t, jobsOf(DocumentJobs{State: "pending", Jobs: []JobLine{{Kind: "fetch", Attempts: 2,
		LastError: "fetch failed: HTTP 503"}}}), "<li>Fetch queued · attempt 3 of 5</li>", "a retry that is due")
	assert.Contains(t, jobsOf(DocumentJobs{State: "fetched", Jobs: []JobLine{{Kind: "index", Running: true,
		Attempts: 2}}}), "<li>Index running · attempt 2 of 5</li>")

	retry := jobsOf(DocumentJobs{State: "pending", Jobs: []JobLine{{Kind: "fetch", Waiting: true, Attempts: 2,
		RunAfter: at, LastError: "fetch failed: native: HTTP 503 Service Unavailable"}}})
	assert.Regexp(t, `<li>Fetch waiting, due <time[^>]*>in 3 min</time> · attempt 3 of 5`+
		`<span class="visually-hidden">: </span><span class="job-why" title="fetch failed: native: HTTP 503 Service Unavailable">`+
		`HTTP 503 Service Unavailable</span></li>`,
		retry)
	deferred := jobsOf(DocumentJobs{State: "pending", Jobs: []JobLine{{Kind: "fetch", Waiting: true, RunAfter: at,
		LastError: "waiting for GitHub's API rate limit to reset"}}})
	assert.Regexp(t, `<li>Fetch waiting, due <time[^>]*>in 3 min</time><span class="visually-hidden">: </span><span class="job-why" `+
		`title="waiting for GitHub&#39;s API rate limit to reset">waiting for GitHub&#39;s API rate limit to reset</span></li>`,
		deferred)
	assert.NotContains(t, deferred, "attempt", "no attempt used yet")
	for _, out := range []string{retry, deferred} {
		shown := textOf(byID(parse(t, out), "doc-jobs"))
		assert.NotContains(t, shown, "failed", "the reason is shown without its wrappers: %s", shown)
	}
	assert.Equal(t, "Fetch waiting, due in 3 min · attempt 3 of 5: HTTP 503 Service Unavailable",
		textOf(byID(parse(t, retry), "doc-jobs")), "as text, the reason is set off from the line")
	assert.Equal(t, "Fetch waiting, due in 3 min: waiting for GitHub's API rate limit to reset",
		textOf(byID(parse(t, deferred), "doc-jobs")))
	held := jobsOf(DocumentJobs{State: "pending", Jobs: []JobLine{{Kind: "fetch"}}, Hold: "paused"})
	assert.Contains(t, held, `<a id="doc-jobs-hold" href="/ui/status">The queue is closed: Paused</a>`)
	assert.NotContains(t, jobsOf(DocumentJobs{State: "pending", Jobs: []JobLine{{Kind: "fetch", Running: true}},
		Hold: "paused"}), "closed", "a running job isn't held")

	base := DocumentBaseline{Updated: at, Extraction: "e1"}
	later := DocumentBaseline{Updated: at.Add(time.Second), Extraction: "e1"}
	assert.Contains(t, jobsOf(DocumentJobs{State: "fetched", Baseline: base, Current: DocumentBaseline{Extraction: "e2"}}),
		`The text changed: <a id="doc-reload" href="/ui/documents/d1">reload</a>`)
	assert.Contains(t, jobsOf(DocumentJobs{State: "failed", Baseline: base, Current: later}),
		`It failed: <a id="doc-reload" href="/ui/documents/d1">reload to see why</a>`)
	assert.Contains(t, jobsOf(DocumentJobs{State: "fetched", Baseline: base, Current: later}),
		`Updated: <a id="doc-reload" href="/ui/documents/d1">reload</a>`)
	assert.Equal(t, `<div class="doc-jobs" id="doc-jobs"></div>`, jobsOf(DocumentJobs{State: "fetched", Baseline: base,
		Current: base}), "nothing to say, nothing in it")
}

// TestRebuild: the rebuild's poller polls every 2 seconds while a rebuild
// is in flight, as the queue or the scheduler says, or a read failed;
// every 30 seconds otherwise with insight on, so that a rebuild the
// scheduler queues reaches an open page; and only after a change with
// insight off. The line says the scheduler's state only when nothing is
// in flight and it has something to say.
func TestRebuild(t *testing.T) {
	assert.Equal(t, ActionRebuild, Rebuild{}.Action().Kind)
	const fast, idle, onChange = "every 2s, curio:changed from:body", "every 30s, curio:changed from:body",
		"curio:changed from:body"
	state := func(s string) InterestsState { return InterestsState{State: s} }
	for _, tc := range []struct {
		name    string
		rebuild Rebuild
		trigger string
		shows   bool
	}{
		{"insight off", Rebuild{State: state("off")}, onChange, false},
		{"current", Rebuild{Enabled: true, State: state("current")}, idle, true},
		{"none", Rebuild{Enabled: true, State: state("none")}, idle, true},
		{"due", Rebuild{Enabled: true, State: state("due")}, idle, true},
		{"held", Rebuild{Enabled: true, State: state("held")}, idle, true},
		{"failing", Rebuild{Enabled: true, State: state("failing")}, idle, true},
		{"unknown", Rebuild{Enabled: true, State: state("unknown")}, idle, false},
		{"a state this build doesn't know", Rebuild{Enabled: true, State: state(evilAttr)}, idle, true},
		{"queued, as the queue says", Rebuild{Enabled: true, Queued: true, State: state("current")}, fast, false},
		{"running, as the queue says", Rebuild{Enabled: true, Running: true, State: state("held")}, fast, false},
		{"queued, as the scheduler says", Rebuild{Enabled: true, State: state("queued")}, fast, false},
		{"rebuilding, as the scheduler says", Rebuild{Enabled: true, State: state("rebuilding")}, fast, false},
		{"a read failed", Rebuild{Enabled: true, Err: &PanelError{Message: "m"}, State: state("current")}, fast, true},
	} {
		p := tc.rebuild.Poller()
		assert.Equal(t, tc.trigger, p.Trigger(), tc.name)
		assert.Equal(t, []string{"rebuild-state", "rebuild-control", "rebuild-poll"}, p.Regions, tc.name)
		assert.Equal(t, tc.shows, tc.rebuild.ShowsState(), tc.name)
	}
	assert.Equal(t, "/ui/interests?poll=rebuild&run=r1", Rebuild{Shown: "r1"}.Poller().Href)
}

// TestInterestsState: each state of automatic rebuilds in the sentence
// curio status prints for it, its times as <time>s and an error cut with
// the whole of it on hover; a state this build doesn't know reads as
// current. Each sentence is escaped and inert.
func TestInterestsState(t *testing.T) {
	set := newRenderer(t).pages[PageStatus]
	now := time.Now()
	long := strings.Repeat("E", 290) + evilScript
	for _, tc := range []struct {
		name  string
		state InterestsState
		want  string // a regexp
	}{
		{"unknown", InterestsState{State: "unknown"}, `^the daemon hasn't checked them yet$`},
		{"off", InterestsState{State: "off"}, `^off \(insight\.enabled: false\)$`},
		{"none", InterestsState{State: "none", Changed: 7, RebuildAt: 20},
			`^waiting for 20 indexed documents \(7 so far\)$`},
		{"the first due", InterestsState{State: "due", Changed: 25, RebuildAt: 20},
			`^the first grouping is due \(25 documents indexed\): waiting for the library to settle$`},
		{"due after a reindex", InterestsState{State: "due", LastRebuildAt: now, FreshOwed: "reindex"},
			`^a fresh rebuild is due: waiting for the re-embedding to finish$`},
		{"due, asked for", InterestsState{State: "due", LastRebuildAt: now, FreshOwed: "manual"},
			`^a fresh rebuild is due \(asked for\): waiting for the library to settle$`},
		{"due, the params changed", InterestsState{State: "due", LastRebuildAt: now, FreshOwed: "params"},
			`^a fresh rebuild is due \(the grouping&#39;s parameters changed\): waiting for the library to settle$`},
		{"due", InterestsState{State: "due", LastRebuildAt: now, Changed: 300, RebuildAt: 263},
			`^a rebuild is due \(300 changed, threshold 263\): waiting for the library to settle$`},
		{"queued", InterestsState{State: "queued"}, `^a rebuild is queued$`},
		{"rebuilding", InterestsState{State: "rebuilding"}, `^rebuilding$`},
		{"held", InterestsState{State: "held", HeldReason: "the embedding model's digest " + evilScript},
			`^rebuilds held: the embedding model&#39;s digest &lt;script&gt;alert\(1\)&lt;/script&gt;; run <code>curio reindex --all</code>$`},
		{"failing, retrying later", InterestsState{State: "failing", LastError: long, RetryAt: now.Add(15 * time.Minute)},
			`^the last rebuild failed: <span class="state-error" title="E{290}&lt;script&gt;alert\(1\)&lt;/script&gt;">E{290}&lt;script&gt;al…</span>; retrying <time datetime="[^"]+" title="[^"]+">in 1[45] min</time>$`},
		{"failing, its backoff past", InterestsState{State: "failing", LastError: "boom", RetryAt: now.Add(-time.Minute)},
			`; retrying once the library settles$`},
		{"failing, no backoff", InterestsState{State: "failing", LastError: "boom"}, `; retrying once the library settles$`},
		{"current", InterestsState{State: "current", LastRebuildAt: now.Add(-2 * time.Hour), LastKind: "warm", Changed: 37,
			RebuildAt: 276},
			`^rebuilt <time datetime="[^"]+" title="[^"]+">2 h ago</time> \(warm\) · 37 documents changed, next at 276$`},
		{"current, one change", InterestsState{State: "current", Changed: 1, RebuildAt: 263},
			`^rebuilt · 1 document changed, next at 263$`},
		{"a state it doesn't know", InterestsState{State: evilScript, Changed: 2, RebuildAt: 5},
			`^rebuilt · 2 documents changed, next at 5$`},
	} {
		var buf bytes.Buffer
		require.NoError(t, set.ExecuteTemplate(&buf, "interests-state", tc.state), tc.name)
		uitest.AssertInert(t, buf.String())
		assert.Regexp(t, tc.want, buf.String(), tc.name)
	}
	for state, tone := range map[string]string{"current": "ok", "none": "ok", "due": "ok", "queued": "ok",
		"rebuilding": "ok", "held": "warn", "failing": "warn", "off": "neutral", "unknown": "neutral", evilAttr: "neutral"} {
		assert.Equal(t, tone, InterestsState{State: state}.Tone(), state)
	}
}

// TestInterests_Rebuild: the Rebuild button while insight is on, disabled
// while the queue holds a rebuild, enabled while rebuilds are held or
// failing; the line: the queue's state of one, else the scheduler's, else
// nothing; newer interests offered; and, with insight off, an empty page
// that says how to turn it on.
func TestInterests_Rebuild(t *testing.T) {
	r := newRenderer(t)
	layout := Layout{Title: "Interests", Nav: NavInterests}
	run := &InterestRun{ID: "r1", Documents: 3}
	page := func(b Rebuild) (string, *html.Node) {
		out := render(t, r, PageInterests, Interests{Layout: layout, Run: run, Rebuild: b})
		return out, parse(t, out)
	}
	current := InterestsState{State: "current", LastRebuildAt: time.Now().Add(-2 * time.Hour), LastKind: "warm",
		Changed: 3, RebuildAt: 20}
	out, doc := page(Rebuild{Enabled: true, Shown: "r1", State: current})
	rebuild := byID(doc, "rebuild")
	require.NotNil(t, rebuild)
	assert.Equal(t, "/v1/interests/rebuild", attrValue(rebuild, "data-path"))
	assert.False(t, hasAttr(rebuild, "aria-disabled"))
	assert.Contains(t, out, `<span class="action-status" id="rebuild-status" role="status" aria-live="polite"></span>`)
	assert.Contains(t, out, `<span class="no-js muted">Rebuild them with <code>curio interests rebuild</code>.</span>`)
	assert.Regexp(t, `<p class="interests-state tone-ok">rebuilt <time[^>]*>2 h ago</time> \(warm\) · 3 documents changed, next at 20</p>`, out)
	assert.Contains(t, out, `hx-get="/ui/interests?poll=rebuild&amp;run=r1" hx-trigger="every 30s, curio:changed from:body"`)

	out, doc = page(Rebuild{Enabled: true, Queued: true, Hold: "outside_schedule", Shown: "r1", State: current})
	assert.Equal(t, "true", attrValue(byID(doc, "rebuild"), "aria-disabled"))
	assert.Contains(t, out, `<div class="rebuild-state" id="rebuild-state"><p>Rebuild queued · <a id="rebuild-hold" href="/ui/status">`+
		`the queue is closed: Outside its schedule</a></p></div>`, "the queue's, not the snapshot's")
	assert.Contains(t, out, `hx-trigger="every 2s, curio:changed from:body"`)
	out, _ = page(Rebuild{Enabled: true, Running: true, StartedAt: time.Now().Add(-2 * time.Minute)})
	assert.Regexp(t, `<p>Rebuilding · started <time[^>]*>2 min ago</time></p>`, out)
	out, _ = page(Rebuild{Enabled: true, Ready: true, State: current})
	assert.Contains(t, out, `<p>New interests are ready: <a id="rebuild-reload" href="/ui/interests">reload</a></p>`)

	for _, state := range []InterestsState{{State: "held", HeldReason: "the embeddings drifted"},
		{State: "failing", LastError: "cluster: <boom>"}} {
		out, doc = page(Rebuild{Enabled: true, State: state})
		assert.False(t, hasAttr(byID(doc, "rebuild"), "aria-disabled"), "%s: a rebuild asked for still runs", state.State)
		assert.Contains(t, out, `<p class="interests-state tone-warn">`, state.State)
	}
	assert.Contains(t, out, `the last rebuild failed: <span class="state-error" title="cluster: &lt;boom&gt;">cluster: &lt;boom&gt;</span>`)

	out, doc = page(Rebuild{Enabled: true, State: InterestsState{State: "queued"}})
	assert.Contains(t, out, `<div class="rebuild-state" id="rebuild-state"></div>`,
		"a snapshot of a rebuild the queue no longer holds is behind it")
	assert.False(t, hasAttr(byID(doc, "rebuild"), "aria-disabled"))
	assert.Contains(t, out, `hx-trigger="every 2s, curio:changed from:body"`, "until the scheduler catches up")

	off := render(t, r, PageInterests, Interests{Layout: layout, Rebuild: Rebuild{State: InterestsState{State: "off"}}})
	assert.Nil(t, byID(parse(t, off), "rebuild"))
	assert.Contains(t, off, "set <code>insight.enabled: true</code> in config.yaml")
	assert.NotContains(t, off, "curio interests rebuild")
	assert.Contains(t, off, `<div class="rebuild-state" id="rebuild-state"></div>`)
	assert.Contains(t, off, `hx-trigger="curio:changed from:body"`)
}

// TestInterests_Empty: before the first rebuild is done, and none is in
// flight, the page says why, as the scheduler last saw it.
func TestInterests_Empty(t *testing.T) {
	r := newRenderer(t)
	for _, tc := range []struct {
		state InterestsState
		want  string
	}{
		{InterestsState{State: "due", Changed: 25, RebuildAt: 20},
			"<p>The first grouping is due (25 documents indexed), and starts once the library settles.</p>"},
		{InterestsState{State: "none", Changed: 7, RebuildAt: 20},
			"<p>The library is grouped into them on its own once 20 documents are indexed; 7 are so far.</p>"},
		{InterestsState{State: "none", Changed: 1, RebuildAt: 20}, "once 20 documents are indexed; 1 is so far.</p>"},
		{InterestsState{State: "held", HeldReason: "the embeddings " + evilScript},
			"<p>Grouping is held: the embeddings &lt;script&gt;alert(1)&lt;/script&gt;. Run <code>curio reindex --all</code>; " +
				"the library is grouped once the re-embedding finishes.</p>"},
		{InterestsState{State: "failing", LastError: evilAttr}, `<p>The first grouping failed: <span class="state-error" ` +
			`title="&#34; onerror=&#34;alert(1)">&#34; onerror=&#34;alert(1)</span>. It is tried again once the library settles.</p>`},
		{InterestsState{State: "unknown"},
			"<p>The library is grouped into them on its own once enough of it is indexed and it settles.</p>"},
	} {
		out := render(t, r, PageInterests, Interests{Layout: Layout{Title: "Interests", Nav: NavInterests},
			Rebuild: Rebuild{Enabled: true, State: tc.state}})
		assert.Contains(t, out, "<h2>No interests yet</h2>", tc.state.State)
		assert.Contains(t, out, tc.want, tc.state.State)
		assert.Contains(t, out, "<p><code>curio interests rebuild</code> groups it now.</p>", tc.state.State)
	}
}

// textOf is n's text.
func textOf(n *html.Node) string {
	var b strings.Builder
	for d := range n.Descendants() {
		if d.Type == html.TextNode {
			b.WriteString(d.Data)
		}
	}
	return b.String()
}

// sampleFailures is a Failures tab over the author's library's causes, as
// GET /v1/failures orders them: by count, most first, then by cause. None
// failed as too large or while indexing.
func sampleFailures() Failures {
	return Failures{Layout: Layout{Title: "Failures", Nav: NavLibrary},
		Counts: &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"failed": 2151, "dead": 820}},
		Total:  2971,
		Groups: []FailureGroup{
			{Cause: "anti_bot", Count: 927, Hosts: []Count{{Name: "stackoverflow.com", Count: 169},
				{Name: "medium.com", Count: 134}}},
			{Cause: "dead_link", Count: 820, Hosts: []Count{{Name: "www.thebookoflife.org", Count: 51}}},
			{Cause: "unreachable", Count: 244}, {Cause: "rate_limited", Count: 222}, {Cause: "login_wall", Count: 188},
			{Cause: "jina_refused", Count: 185}, {Cause: "http_error", Count: 143}, {Cause: "timeout", Count: 130},
			{Cause: "tls", Count: 43}, {Cause: "unsupported", Count: 28}, {Cause: "network", Count: 26},
			{Cause: "other", Count: 15},
		}}
}

// TestFailures: the totals split failed from dead links, and say so only
// when there are both; the footnote names the causes without documents in
// their order; the subnav counts the summary's documents while it was read;
// and the page polls once per change, never on a timer.
func TestFailures(t *testing.T) {
	f := sampleFailures()
	assert.Equal(t, 820, f.Dead())
	assert.Equal(t, 2151, f.Failed())
	assert.Equal(t, "2,971 documents couldn't be fetched: 2,151 failed and 820 dead links, grouped by why. "+
		"Refetching a group queues a fresh fetch for each of its documents.", f.Totals())
	assert.Equal(t, []string{"too_large", "index"}, f.Absent())

	onlyFailed := Failures{Total: 3, Groups: []FailureGroup{{Cause: "timeout", Count: 2}, {Cause: "tls", Count: 1}}}
	assert.Zero(t, onlyFailed.Dead())
	assert.Equal(t, "3 documents couldn't be fetched, grouped by why. "+
		"Refetching a group queues a fresh fetch for each of its documents.", onlyFailed.Totals())
	onlyDead := Failures{Total: 1, Groups: []FailureGroup{{Cause: "dead_link", Count: 1}}}
	assert.Equal(t, 1, onlyDead.Dead())
	assert.Zero(t, onlyDead.Failed())
	assert.Equal(t, "1 document couldn't be fetched, grouped by why. "+
		"Refetching a group queues a fresh fetch for each of its documents.", onlyDead.Totals())
	absent := onlyDead.Absent()
	assert.Len(t, absent, len(store.FailureCauses())-1)
	assert.Equal(t, "anti_bot", absent[0], "in store.FailureCauses' order")
	unknown := Failures{Total: 1, Groups: []FailureGroup{{Cause: "new_cause", Count: 1}}}
	assert.Len(t, unknown.Absent(), len(store.FailureCauses()), "a cause this build doesn't know is none of them")

	assert.Equal(t, LibraryViews{OnFailures: true, Failed: 2971, Counted: true}, f.Views())
	assert.Equal(t, LibraryHead{Counts: f.Counts, Views: f.Views()}, f.Head())
	f.Err = &PanelError{Message: "m", RequestID: "r"}
	assert.Equal(t, LibraryViews{OnFailures: true}, f.Views(), "no count without the summary")

	pollers := sampleFailures().Pollers()
	require.Len(t, pollers, 1)
	p := pollers[0]
	assert.Equal(t, "failures-poll", p.ID)
	assert.Equal(t, "/ui/failures?poll=causes", p.Href)
	assert.Equal(t, "curio:changed from:body", p.Trigger(), "after a change, never on a timer")
	assert.Equal(t, "#library-subnav,#failures-live", p.Select())
	assert.Empty(t, Failures{Poll: PollCauses}.Pollers())
}

// TestFailureGroup: a group's card id, whether its cause is one the
// Library and refetch-all take, its refetch (the dead links' names their
// state) and the command that does it; a cause this build doesn't know has
// neither.
func TestFailureGroup(t *testing.T) {
	blocked := FailureGroup{Cause: "anti_bot", Count: 927}
	assert.Equal(t, "cause-anti_bot", blocked.ID())
	assert.True(t, blocked.Known())
	assert.False(t, blocked.DeadLinks())
	assert.Equal(t, refetchCauseAction("anti_bot", 927), blocked.Refetch())
	assert.Equal(t, "curio refetch --all --cause anti_bot", blocked.Command())

	dead := FailureGroup{Cause: "dead_link", Count: 820}
	assert.True(t, dead.DeadLinks())
	assert.Equal(t, refetchDeadLinksAction(820), dead.Refetch())
	assert.Equal(t, "curio refetch --all --state dead --cause dead_link", dead.Command())

	unknown := FailureGroup{Cause: evilScript, Count: 3}
	assert.False(t, unknown.Known())
	assert.Empty(t, unknown.Command())
	assert.Equal(t, "cause-"+evilScript, unknown.ID())

	lib := Library{Counts: &LibraryCounts{ByState: map[string]int{"fetched": 9, "failed": 2150, "dead": 819}}}
	assert.Equal(t, LibraryViews{Failed: 2969, Counted: true}, lib.Head().Views, "the Library counts from its stats")
	assert.Equal(t, LibraryViews{}, Library{}.Head().Views, "and without them, not at all")
}

// cardsOf are the ids of the Failures tab's cards, in page order.
func cardsOf(doc *html.Node) []string {
	var ids []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "li" && attrValue(n, "class") == "card cause" {
			ids = append(ids, attrValue(n, "id"))
		}
	}
	return ids
}

// TestFailures_Page: the Library's head with Failures current and counted,
// the totals, a card per cause in the summary's order, each with its icon,
// label and code, why, hosts linking to the Library of that cause and host,
// count and share, and View in Library; the causes without documents; a
// cause this build doesn't know without a link or a refetch.
func TestFailures_Page(t *testing.T) {
	r := newRenderer(t)
	f := sampleFailures()
	f.Groups = append(f.Groups, FailureGroup{Cause: "new_cause", Count: 1,
		Hosts: []Count{{Name: "new.example", Count: 1}}})
	out := render(t, r, PageFailures, f)
	doc := parse(t, out)

	assert.Contains(t, out, "<title>Failures · curio</title>")
	assert.Contains(t, out, "<h1>Library</h1>\n"+`<p class="lede">7,467 documents from 7,497 bookmarks.</p>`)
	assert.Contains(t, out, `<nav class="subnav" id="library-subnav" aria-label="Library views">`+
		`<a id="subnav-documents" href="/ui/library">Documents</a>`+
		`<a id="subnav-failures" href="/ui/failures" aria-current="page">Failures <span class="count">2,971</span></a></nav>`)
	assert.NotContains(t, out, "Phase 2")
	assert.Contains(t, out, `<p class="failures-totals">2,971 documents couldn&#39;t be fetched: 2,151 failed and 820 dead links,`)

	cards := cardsOf(doc)
	require.Len(t, cards, 13)
	assert.Equal(t, []string{"cause-anti_bot", "cause-dead_link", "cause-unreachable"}, cards[:3], "the summary's order")
	assert.Equal(t, "cause-new_cause", cards[12])

	assert.Contains(t, out, `<li class="card cause" id="cause-anti_bot">`+"\n"+
		`<span class="cause-icon danger"><svg class="icon"`)
	assert.Contains(t, out, `<h2>Blocked by bot protection <code>anti_bot</code></h2>`+"\n"+
		`<p class="why">The site blocked curio&#39;s request:`)
	assert.Contains(t, out, `<div class="hosts" role="group" aria-label="Top hosts">`+
		`<a class="tag" id="host-anti_bot-0" href="/ui/library?cause=anti_bot&amp;host=stackoverflow.com" title="stackoverflow.com">`+
		`<span class="name">stackoverflow.com</span> <span class="n">169</span></a>`+
		`<a class="tag" id="host-anti_bot-1" href="/ui/library?cause=anti_bot&amp;host=medium.com" title="medium.com">`)
	assert.Contains(t, out, `<div class="count"><div class="value">927<span class="visually-hidden"> documents,</span></div>`+
		`<div class="pct">31%<span class="visually-hidden"> of failures</span></div></div>`)
	assert.Contains(t, out, `<a class="btn btn-sm btn-ghost" id="view-anti_bot" href="/ui/library?cause=anti_bot">View in Library →</a>`)
	assert.Contains(t, out, `<a class="btn btn-sm btn-ghost" id="view-dead_link" href="/ui/library?cause=dead_link">`,
		"the list's cause filter takes dead documents; state=dead is refetch-all's rule alone")
	assert.Contains(t, out, `<li class="card cause" id="cause-dead_link">`+"\n"+`<span class="cause-icon neutral">`)
	assert.Contains(t, out, `<li class="card cause" id="cause-timeout">`+"\n"+`<span class="cause-icon warn">`)
	assert.Contains(t, out, `<div class="value">15<span class="visually-hidden"> documents,</span></div><div class="pct">1%`)
	assert.NotContains(t, render1(t, byID(doc, "cause-unreachable")), `class="hosts"`, "no hosts, no group")

	unknown := byID(doc, "cause-new_cause")
	markup := render1(t, unknown)
	assert.Contains(t, markup, `<h2>new_cause <code>new_cause</code></h2>`)
	assert.NotContains(t, markup, `class="why"`)
	assert.Contains(t, out, `<span class="tag" title="new.example"><span class="name">new.example</span> <span class="n">1</span></span>`)
	assert.Contains(t, markup, `<div class="value">1<span class="visually-hidden"> document,</span></div>`)
	for n := range unknown.Descendants() {
		assert.False(t, n.Type == html.ElementNode && (focusable(n) || n.Data == "code" && hasAttr(n, "class")),
			"no link, refetch or command: %s", render1(t, n))
	}

	assert.Contains(t, out, `<p class="empty-inline mt-4">Causes without documents: Too large, Index failed.</p>`)
	f.Groups = append(f.Groups, FailureGroup{Cause: "too_large", Count: 1}, FailureGroup{Cause: "index", Count: 1})
	assert.NotContains(t, render(t, r, PageFailures, f), "Causes without documents", "every cause has some")
}

// TestFailures_States: nothing failed is an empty state and nothing else;
// a summary that couldn't be read is its error inside the region, and the
// head renders, its subnav without a count; the library's counts unread
// are the lede's fallback; a poll's answer is the subnav and the region
// alone.
func TestFailures_States(t *testing.T) {
	r := newRenderer(t)
	layout := Layout{Title: "Failures", Nav: NavLibrary}
	counts := &LibraryCounts{Documents: 3, Bookmarks: 3}

	empty := render(t, r, PageFailures, Failures{Layout: layout, Counts: counts})
	live := render1(t, byID(parse(t, empty), "failures-live"))
	assert.Contains(t, live, `<div class="empty">`)
	assert.Contains(t, live, "<h2>Nothing failed</h2>")
	for _, gone := range []string{"failures-totals", `class="causes"`, "Causes without documents"} {
		assert.NotContains(t, live, gone)
	}
	assert.Contains(t, empty, `Failures <span class="count">0</span></a>`, "0 is a count")

	failed := render(t, r, PageFailures, Failures{Layout: layout, Counts: counts,
		Err: &PanelError{Message: "summarize failures: boom", RequestID: "req-1"}})
	doc := parse(t, failed)
	assert.Contains(t, render1(t, byID(doc, "failures-live")),
		`<div class="panel-error">`, "the error sits in the region, which a refresh can clear")
	assert.Contains(t, failed, "Couldn't read this: summarize failures: boom.")
	assert.NotContains(t, render1(t, byID(doc, "library-subnav")), "count")
	assert.Contains(t, failed, "<h1>Library</h1>")

	unread := render(t, r, PageFailures, Failures{Layout: layout, Total: 1,
		Groups: []FailureGroup{{Cause: "tls", Count: 1}}})
	assert.Contains(t, unread, `<p class="lede">Every page curio saved for you.</p>`)
	assert.Contains(t, unread, `Failures <span class="count">1</span></a>`, "the summary counts, stats or not")

	poll := render(t, r, PageFailures, Failures{Layout: layout, Poll: PollCauses, Total: 1,
		Groups: []FailureGroup{{Cause: "tls", Count: 1}}})
	doc = parse(t, poll)
	assert.NotNil(t, byID(doc, "library-subnav"))
	assert.NotNil(t, byID(doc, "failures-live"))
	for _, gone := range []string{"<h1>", `class="lede"`, `id="failures-status"`, "data-poll"} {
		assert.NotContains(t, poll, gone)
	}
}

// TestFailures_Actions: a group's Refetch sends refetch-all by its cause
// and says what it queued in the page's one status, outside the live
// regions; the dead links' opens a confirm in their card, whose button
// sends their state too; each needs JavaScript, and its command stands in
// without it. The status holds nothing at rest.
func TestFailures_Actions(t *testing.T) {
	r := newRenderer(t)
	out := render(t, r, PageFailures, sampleFailures())
	doc := parse(t, out)

	refetch := byID(doc, "refetch-anti_bot")
	require.NotNil(t, refetch)
	assert.Equal(t, "btn btn-sm js-only", attrValue(refetch, "class"))
	assert.Equal(t, "POST", attrValue(refetch, "data-method"))
	assert.Equal(t, "/v1/documents/refetch-all?cause=anti_bot", attrValue(refetch, "data-path"))
	assert.Equal(t, "failures-status", attrValue(refetch, "data-status"))
	assert.Equal(t, "Blocked by bot protection: 927 refetches queued", attrValue(refetch, "data-done"))
	assert.Equal(t, "Refetch 927", textOf(refetch))
	assert.False(t, hasAttr(refetch, "hidden"))

	opener := byID(doc, "refetch-dead_link")
	require.NotNil(t, opener)
	assert.Equal(t, "btn btn-sm js-only", attrValue(opener, "class"))
	assert.Equal(t, "confirm-dead_link", attrValue(opener, "popovertarget"))
	assert.False(t, hasAttr(opener, "data-method"), "it only opens the confirm")
	assert.Equal(t, "Refetch 820 anyway…", textOf(opener))

	confirm := byID(doc, "confirm-dead_link")
	require.NotNil(t, confirm)
	assert.True(t, hasAttr(confirm, "popover"))
	assert.Equal(t, "alertdialog", attrValue(confirm, "role"))
	assert.Equal(t, "confirm-dead_link-title", attrValue(confirm, "aria-labelledby"))
	assert.Equal(t, "confirm-dead_link-text", attrValue(confirm, "aria-describedby"))
	assert.Equal(t, "Refetch 820 dead links?", textOf(byID(doc, "confirm-dead_link-title")))
	assert.NotEmpty(t, textOf(byID(doc, "confirm-dead_link-text")))
	assert.Contains(t, render1(t, byID(doc, "cause-dead_link")), `id="confirm-dead_link"`,
		"in the card, refreshed with the count it names")
	cancel := byID(doc, "refetch-dead_link-cancel")
	assert.True(t, hasAttr(cancel, "autofocus"))
	assert.Equal(t, "confirm-dead_link", attrValue(cancel, "popovertarget"))
	assert.Equal(t, "hide", attrValue(cancel, "popovertargetaction"))
	assert.False(t, hasAttr(cancel, "data-method"))
	forced := byID(doc, "refetch-dead_link-confirm")
	assert.Equal(t, "btn btn-primary", attrValue(forced, "class"))
	assert.Equal(t, "/v1/documents/refetch-all?cause=dead_link&state=dead", attrValue(forced, "data-path"))
	assert.Equal(t, "hide", attrValue(forced, "popovertargetaction"))
	assert.Equal(t, "Dead link: 820 refetches queued", attrValue(forced, "data-done"))
	assert.Equal(t, "Refetch 820", textOf(forced))

	status := byID(doc, "failures-status")
	require.NotNil(t, status)
	assert.Equal(t, "action-status", attrValue(status, "class"))
	assert.Equal(t, "status", attrValue(status, "role"))
	assert.Equal(t, "polite", attrValue(status, "aria-live"))
	assert.Nil(t, status.FirstChild, "empty at rest")
	for n := status; n != nil; n = n.Parent {
		assert.NotContains(t, []string{"library-subnav", "failures-live"}, attrValue(n, "id"), "outside the regions")
	}
	assert.Less(t, strings.Index(out, `id="library-subnav"`), strings.Index(out, `id="failures-status"`))
	assert.Less(t, strings.Index(out, `id="failures-status"`), strings.Index(out, `id="failures-live"`))

	assert.Contains(t, out, `<code class="no-js">curio refetch --all --cause anti_bot</code>`)
	assert.Contains(t, out, `<code class="no-js">curio refetch --all --state dead --cause dead_link</code>`)
	assert.Equal(t, 12, len(withAttr(doc, "data-method")), "a refetch for every group, the dead links' in its confirm")
}

// TestLibrary_Head: the Library's head, in either order, has its subnav
// with Documents current and the Failures tab counting the failed and dead
// documents from the stats it read, whatever the filters, and no count
// without them.
func TestLibrary_Head(t *testing.T) {
	r := newRenderer(t)
	counts := &LibraryCounts{Documents: 7467, Bookmarks: 7497, ByState: map[string]int{"failed": 2150, "dead": 819}}
	for _, f := range []LibraryFilters{{}, {Order: OrderSaved}, {Host: "a.example", Cause: "tls"}} {
		out := render(t, r, PageLibrary, Library{Layout: Layout{Title: "Library", Nav: NavLibrary}, Filters: f,
			Counts: counts})
		assert.Contains(t, out, `<nav class="subnav" id="library-subnav" aria-label="Library views">`+
			`<a id="subnav-documents" href="/ui/library" aria-current="page">Documents</a>`+
			`<a id="subnav-failures" href="/ui/failures">Failures <span class="count">2,969</span></a></nav>`, "%+v", f)
	}
	out := render(t, r, PageLibrary, Library{Layout: Layout{Title: "Library", Nav: NavLibrary}})
	assert.Contains(t, out, `<a id="subnav-failures" href="/ui/failures">Failures</a>`)
}

// TestLibrary_CauseLine: a cause filter, in either order, gets a line
// under the toolbar naming it, with a link that clears it alone; the form
// keeps it for Apply; no cause, no line.
func TestLibrary_CauseLine(t *testing.T) {
	r := newRenderer(t)
	for _, order := range []string{"", OrderSaved} {
		f := LibraryFilters{Order: order, Host: "blocked.example", Cause: "anti_bot"}
		for _, rows := range [][]LibraryRow{nil, {{DocumentID: "a", URL: "https://blocked.example/a", State: "failed"}}} {
			out := render(t, r, PageLibrary, Library{Layout: Layout{Title: "Library", Nav: NavLibrary}, Filters: f,
				Rows: rows, PageSize: 50})
			doc := parse(t, out)
			link := byID(doc, "clear-cause")
			require.NotNil(t, link, order)
			assert.Equal(t, clearCauseHref(f), attrValue(link, "href"), order)
			assert.Contains(t, attrValue(link, "aria-label"), "Clear", order)
			assert.Equal(t, "Clear", textOf(link))
			assert.Contains(t, out, `<p class="cause-line"><span class="muted">Why they failed:</span> `+
				`<strong>Blocked by bot protection</strong> <code>anti_bot</code> `, order)
			assert.Contains(t, out, `<input type="hidden" name="cause" value="anti_bot">`, order)
			assert.Less(t, strings.Index(out, `</form>`), strings.Index(out, `class="cause-line"`), "under the toolbar")
		}
	}
	out := render(t, r, PageLibrary, Library{Layout: Layout{Title: "Library", Nav: NavLibrary},
		Filters: LibraryFilters{Host: "blocked.example"}})
	assert.NotContains(t, out, "cause-line")
	assert.NotContains(t, out, "clear-cause")
}

// TestInterests_Changes: for a week after a rebuild that split, merged or
// dissolved interests, a note counts what it changed, dates it, and leads
// to the changes; never for one that only kept, moved or created them,
// past the week, or without a run. The note sits outside the polled
// regions.
func TestInterests_Changes(t *testing.T) {
	r := newRenderer(t)
	at := time.Date(2026, 10, 9, 14, 3, 0, 0, time.UTC)
	withChanges := func(recent bool, c RunChanges) Interests {
		v := interestsPage(1)
		v.Run.ComputedAt, v.Run.Recent, v.Run.Changes = at, recent, c
		return v
	}
	out := render(t, r, PageInterests, withChanges(true, RunChanges{Kept: 180, Split: 5, Merged: 1, Moved: 2, Created: 9}))
	assert.Contains(t, out, `<div class="callout callout-info mb-4" role="note">`)
	assert.Contains(t, out, `<p>Rebuilt on <time datetime="2026-10-09T14:03:00Z" title="`+when(at)+`">`+localDay(at)+
		`</time>: 5 interests split, 1 merged, 2 moved, 9 new. <a href="/ui/interests/changes">What changed →</a></p>`)
	note := strings.Index(out, `What changed →`)
	assert.Less(t, note, strings.Index(out, `aria-label="Coverage"`), "above the coverage")
	assert.Greater(t, note, strings.Index(out, `id="rebuild-control"`), "outside the head's live regions")
	assert.Contains(t, render(t, r, PageInterests, withChanges(true, RunChanges{Dissolved: 1})),
		"Rebuilt on", "a dissolved interest alone")

	for name, v := range map[string]Interests{
		"older than a week":    withChanges(false, RunChanges{Split: 5}),
		"kept, moved, created": withChanges(true, RunChanges{Kept: 10, Moved: 3, Created: 4}),
		"no run":               {Layout: Layout{Title: "Interests", Nav: NavInterests}},
	} {
		assert.NotContains(t, render(t, r, PageInterests, v), "What changed", name)
	}
	assert.Equal(t, "1 interest merged, 2 dissolved", RunChanges{Merged: 1, Dissolved: 2}.Summary())
	assert.Empty(t, RunChanges{Kept: 3}.Summary())
}

// TestInterests_UnsortedCard: Unsorted's card is the last card on the last
// page of the groups, and on a run without any; never on another page,
// past the last, or when nothing is unsorted or placed there since. It
// leads to Unsorted's first page of the run shown.
func TestInterests_UnsortedCard(t *testing.T) {
	r := newRenderer(t)
	last := interestsPage(10)
	last.Run.NewUnsorted = 2
	out := render(t, r, PageInterests, last)
	card := strings.Index(out, `<li class="card interest unsorted">`)
	require.Positive(t, card)
	assert.Greater(t, card, strings.LastIndex(out, `<li class="card interest">`), "after the groups")
	assert.Less(t, card, strings.LastIndex(out, `</ul>`), "inside their list")
	assert.Less(t, strings.LastIndex(out, `</ul>`), strings.Index(out, `<nav class="pager"`), "over the pager")
	assert.Contains(t, out, `<a href="/ui/interests/unsorted?run=run-1">Unsorted</a></h2>`+"\n"+
		`<div class="stats"><span><b>1,000</b> documents</span><span class="new"><b>2</b> new</span></div>`+"\n"+
		`<p class="summary">Not close enough to any interest yet. Listed nearest first, each with the interest it is closest to.</p>`+"\n"+
		`<a class="all" href="/ui/interests/unsorted?run=run-1">See them →</a>`)

	assert.NotContains(t, render(t, r, PageInterests, interestsPage(1)), "card interest unsorted", "not the last page")
	assert.NotContains(t, render(t, r, PageInterests, interestsPage(11)), "card interest unsorted", "past the last")
	none := interestsPage(10)
	none.Run.Unsorted = 0
	assert.Nil(t, none.Unsorted(), "nothing unsorted or placed there")
	none.Run.NewUnsorted = 1
	assert.NotNil(t, none.Unsorted(), "placed there since")

	empty := Interests{Layout: last.Layout, Run: &InterestRun{ID: "run-2", Documents: 5, Unsorted: 5}}
	out = render(t, r, PageInterests, empty)
	assert.Contains(t, out, "<h2>No interests in this run</h2>")
	assert.Contains(t, out, `<a class="all" href="/ui/interests/unsorted?run=run-2">See them →</a>`, "a run without groups")
}

// TestInterests_Cards: an interest's card counts its loose fits and its
// new documents when it has any; an area's leads to all its interests
// when its card lists them all, and counts its new documents.
func TestInterests_Cards(t *testing.T) {
	r := newRenderer(t)
	v := interestsPage(1)
	v.Interests[0].Loose, v.Interests[0].New = 2, 3
	v.Interests[1].Loose = 1
	out := render(t, r, PageInterests, v)
	assert.Contains(t, out, `<div class="stats"><span><b>84</b> documents</span><span><b>2</b> loose fits</span>`+
		`<span class="new"><b>3</b> new</span><span class="meter-row"`)
	assert.Contains(t, out, `<div class="stats"><span><b>83</b> documents</span><span><b>1</b> loose fit</span><span class="meter-row"`)
	assert.Contains(t, out, `<div class="stats"><span><b>82</b> documents</span><span class="meter-row"`, "none of either")

	area := Interest{ID: "a1", Area: true, Label: "Cloud", Size: 120, New: 4, NumChildren: 2,
		Children: []Interest{{ID: "i1", Label: "AWS", Size: 70}, {ID: "i2", Label: "GCP", Size: 50}}}
	out = render(t, r, PageInterests, Interests{Layout: v.Layout, Run: &InterestRun{ID: "r", Shape: "areas", Total: 1},
		Interests: []Interest{area}})
	assert.Contains(t, out, `<span><b>2</b> interests</span><span class="new"><b>4</b> new</span></div>`)
	assert.Contains(t, out, `<a class="all" href="/ui/interests/a1">All 2 interests →</a>`, "every one listed")
	area.NumChildren = 6
	assert.Equal(t, 4, area.MoreChildren())
	assert.Equal(t, 70, area.Largest())
}

// TestInterestPage_Lineage: an area's or interest's page says what the
// latest rebuild did to it, a line an event, dated by the run's finish,
// each identity linking to its page, a retired one marked; one that was
// only kept says nothing, the run before it pruned.
func TestInterestPage_Lineage(t *testing.T) {
	r := newRenderer(t)
	at := time.Date(2026, 10, 9, 14, 3, 0, 0, time.UTC)
	self := &InterestRef{ID: "i1", Label: "Kafka"}
	page := interestPage(1, 3)
	page.RunAt = at
	page.Interest.Events = []InterestEvent{
		{Event: "kept", From: self, To: self, Shared: 3},
		{Event: "split", From: &InterestRef{ID: "old", Label: "Streams <old>", Retired: true}, To: self, Shared: 4},
		{Event: "split", From: self, To: &InterestRef{ID: "i6", Label: "Kafka Connect"}, Shared: 1},
		{Event: "merged", From: &InterestRef{ID: "i2", Retired: true}, To: self, Shared: 2},
		{Event: "moved", From: self, To: self},
		{Event: "new", To: self},
	}
	page.Interest.ID, page.Interest.Label = "i1", "Kafka"
	out := render(t, r, PageInterest, page)
	assert.Contains(t, out, `<p class="callout-title">In the rebuild of <time datetime="2026-10-09T14:03:00Z" title="`+
		when(at)+`">`+localDay(at)+`</time></p>`)
	assert.Contains(t, out, `<ul class="lineage">`+"\n"+
		`<li>Split off from <a class="ref" href="/ui/interests/old" title="Streams &lt;old&gt;">Streams &lt;old&gt;</a> <span class="tag">retired</span></li>`+"\n"+
		`<li><a class="ref" href="/ui/interests/i6" title="Kafka Connect">Kafka Connect</a> split off from it (1 document)</li>`+"\n"+
		`<li>Took in <a class="ref" href="/ui/interests/i2" title="Unlabeled interest"><span class="unlabeled">Unlabeled interest</span></a> <span class="tag">retired</span> (2 documents)</li>`+"\n"+
		`<li>Moved here from another area</li>`+"\n"+
		`<li>New in this rebuild</li>`+"\n"+
		`</ul>`)
	note := strings.Index(out, `role="note"`)
	assert.Less(t, note, strings.Index(out, `<table`), "under the head, over the members")

	page.Interest.Events = page.Interest.Events[:1]
	assert.NotContains(t, render(t, r, PageInterest, page), `role="note"`, "kept alone says nothing")
	page.Interest.Events, page.RunAt = []InterestEvent{{Event: "new", To: self}}, time.Time{}
	assert.Contains(t, render(t, r, PageInterest, page), `<p class="callout-title">In the latest rebuild</p>`,
		"a run that can't be read isn't dated")

	area := InterestPage{Layout: page.Layout, Interest: Interest{ID: "a1", Area: true, Label: "Engineering",
		NumChildren: 1, Children: []Interest{{ID: "i1", Label: "Kafka", Size: 3}},
		Events: []InterestEvent{{Event: "split", Area: true, From: &InterestRef{ID: "a1", Area: true},
			To: &InterestRef{ID: "a9", Area: true}, Shared: 40}}}}
	assert.Contains(t, render(t, r, PageInterest, area), `<li><a class="ref" href="/ui/interests/a9" title="Unlabeled area">`+
		`<span class="unlabeled">Unlabeled area</span></a> split off from it (40 documents)</li>`, "an area's note, of areas")
}

// TestInterestPage_NewBand: an interest's first page lists the documents
// placed into it since, newest first, each tagged new and unranked, over
// the ranked members, and how many more there are; later pages don't.
func TestInterestPage_NewBand(t *testing.T) {
	r := newRenderer(t)
	page := interestPage(1, 60)
	page.Interest.New = 23
	page.Interest.NewMembers = []Member{{DocumentID: "n1", BookmarkTitle: "Placed <here>", URL: "https://example.com/n1",
		Similarity: 0.61, Fit: "new"}, {DocumentID: "n2", URL: "https://example.com/n2", Similarity: 0.52, Fit: "new"}}
	out := render(t, r, PageInterest, page)
	assert.Contains(t, out, `<h2 id="new-band">New since the last rebuild</h2><p>Placed by similarity; the next rebuild decides.</p>`)
	assert.Contains(t, out, `<tr class="fit-new"><td><a class="doc-title from-bookmark" href="/ui/documents/n1" title="Placed &lt;here&gt;">`+
		`Placed &lt;here&gt;</a>`)
	assert.Contains(t, out, `<span class="badge plain fit-new">new</span></td><td><span class="meter-row">`+
		`<meter class="meter" min="0" max="1" value="0.61">0.61</meter>0.61</span></td></tr>`)
	assert.Contains(t, out, `<p class="table-note">and 21 more</p>`)
	band, members := strings.Index(out, `id="new-band"`), strings.Index(out, `<tr class="fit-member">`)
	assert.Less(t, band, members, "above the ranked members")
	assert.Equal(t, 50, strings.Count(out, `<td class="num muted">`), "the band is unranked")
	assert.Contains(t, out, `<span class="badge plain fit-new">23 new</span>`, "the head counts them all")

	page.Interest.New = 2
	assert.NotContains(t, render(t, r, PageInterest, page), "more</p>", "all of them listed")
	page.Page = 2
	assert.NotContains(t, render(t, r, PageInterest, page), "New since the last rebuild", "the first page alone")
}

// TestAreaPage_Pages: an area's page lists its interests 24 a page under
// the shared pager, its head counting all of them; a page past the last
// keeps the head.
func TestAreaPage_Pages(t *testing.T) {
	r := newRenderer(t)
	children := make([]Interest, 0, 6)
	for i := range 6 {
		children = append(children, Interest{ID: "i" + strconv.Itoa(i), Label: "Topic " + strconv.Itoa(i), Size: 30 - i})
	}
	page := InterestPage{Layout: Layout{Title: "Engineering", Nav: NavInterests}, Page: 2, Interest: Interest{ID: "a1",
		Area: true, Label: "Engineering", Summary: "Building things.", Size: 400, Loose: 2, New: 5, NumChildren: 30,
		Cohesion: 0.4, Children: children}}
	out := render(t, r, PageInterest, page)
	assert.Contains(t, out, `<span class="badge badge-accent plain">400 documents</span><span class="badge plain">30 interests</span>`+
		`<span class="badge plain">2 loose fits</span><span class="badge plain fit-new">5 new</span>`)
	assert.Contains(t, out, `<p class="lede">Building things.</p>`)
	assert.Equal(t, 6, strings.Count(out, `<li class="card interest">`))
	assert.Contains(t, out, `<span class="pager-summary">Interests 25–30 of 30</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-prev" href="/ui/interests/a1" rel="prev">`)
	assert.Equal(t, 2, page.Pages())

	page.Page, page.Interest.Children = 3, nil
	require.NotNil(t, page.OutOfRange())
	out = render(t, r, PageInterest, page)
	assert.Contains(t, out, `<span class="badge plain">30 interests</span>`, "the head kept")
	assert.Contains(t, out, "<h2>No page 3</h2>\n<p>This list has 2 pages.</p>")
	assert.NotContains(t, out, "interest-grid")
}

// TestUnsorted_Page: the documents in no interest, nearest first, each
// named as the Library names it with its nearest interest in a column of
// its own, cut on one line, which a phone folds under the name; 50 a page
// under the shared pager, whose links name the run; the documents placed
// there since on the first page.
func TestUnsorted_Page(t *testing.T) {
	r := newRenderer(t)
	docs := make([]UnsortedDoc, 0, 50)
	for i := range 50 {
		docs = append(docs, UnsortedDoc{Member: Member{DocumentID: "d" + strconv.Itoa(i), URL: "https://example.com/" +
			strconv.Itoa(i), Similarity: 0.3, Fit: "unsorted"}, Nearest: InterestRef{ID: "k", Label: "Kafka <streams>"}})
	}
	docs[1].Nearest, docs[2].Nearest = InterestRef{ID: "u"}, InterestRef{}
	page := Unsorted{Layout: Layout{Title: "Unsorted", Nav: NavInterests}, Page: 1, Run: "run-1", Total: 60, Documents: docs,
		NumNew: 1, New: []Member{{DocumentID: "n1", Title: "Placed", URL: "https://example.com/n1", Fit: "new"}}}
	out := render(t, r, PageUnsorted, page)
	assert.Contains(t, out, `<span class="sep">›</span><span aria-current="page">Unsorted</span></nav>`)
	assert.Contains(t, out, `<p class="lede">60 documents are in no interest: not close enough to any yet. `+
		`Listed nearest first, each with the interest it is closest to.</p>`)
	assert.Contains(t, out, `<span class="badge plain fit-new">1 new since the rebuild</span>`)
	assert.Contains(t, out, `<colgroup><col><col class="c-nearest"><col class="c-sim"></colgroup>`)
	assert.Contains(t, out, `<span class="nearest-sub">nearest <a class="ref" href="/ui/interests/k" title="Kafka &lt;streams&gt;">`+
		`Kafka &lt;streams&gt;</a></span></td><td class="c-nearest"><a class="ref" href="/ui/interests/k" `+
		`title="Kafka &lt;streams&gt;">Kafka &lt;streams&gt;</a></td>`)
	assert.Contains(t, out, `<td class="c-nearest"><a class="ref" href="/ui/interests/u" title="Unlabeled interest">`+
		`<span class="unlabeled">Unlabeled interest</span></a></td>`)
	assert.Contains(t, out, `<td class="c-nearest"><span class="muted">none</span></td>`)
	assert.Equal(t, 50, strings.Count(out, `<tr class="fit-unsorted">`))
	assert.Contains(t, out, `<span class="pager-summary">Documents 1–50 of 60, nearest first</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-next" href="/ui/interests/unsorted?page=2&amp;run=run-1" rel="next">`)
	assert.Contains(t, out, `<h2 id="new-band">New since the last rebuild</h2>`)

	page.Page, page.Documents = 2, docs[:10]
	out = render(t, r, PageUnsorted, page)
	assert.NotContains(t, out, "New since the last rebuild", "the first page alone")
	assert.Contains(t, out, "Page 2 of 2.</p>")
	page.Page, page.Documents = 3, nil
	require.NotNil(t, page.OutOfRange())
	out = render(t, r, PageUnsorted, page)
	assert.Contains(t, out, "<h2>No page 3</h2>")
	assert.Contains(t, out, `<a class="btn" href="/ui/interests/unsorted?run=run-1">First page</a>`)

	changed := Unsorted{Layout: page.Layout, Page: 2, RunChanged: true, Run: "run-2", Total: 60, Documents: docs[:10]}
	assert.Contains(t, render(t, r, PageUnsorted, changed), `this page lists the new run's. `+
		`<a href="/ui/interests/unsorted?run=run-2">Start again from page 1</a>.</p>`)

	out = render(t, r, PageUnsorted, Unsorted{Layout: page.Layout, Run: "run-1", NumNew: 1, New: page.New})
	assert.Contains(t, out, "<h2>Nothing unsorted</h2>")
	assert.Contains(t, out, "New since the last rebuild", "the documents placed there since, still")
	out = render(t, r, PageUnsorted, Unsorted{Layout: page.Layout})
	assert.Contains(t, out, "<h2>No interests yet</h2>")
	assert.Nil(t, Unsorted{Page: 4}.OutOfRange(), "no run, no pages to be past")
}

// TestChanges_Page: the latest rebuild's events under a heading a kind, in
// the API's order, each naming its identities as links, retired ones
// marked, with the documents a split or merge shares and the area a moved
// interest is in now; a first grouping and a rebuild that changed nothing
// in a line.
func TestChanges_Page(t *testing.T) {
	r := newRenderer(t)
	at := time.Date(2026, 10, 9, 14, 3, 0, 0, time.UTC)
	kafka := &InterestRef{ID: "k", Label: "Kafka"}
	run := &ChangesRun{ComputedAt: at, Trigger: "manual", Kind: "warm",
		Changes: RunChanges{Kept: 180, Split: 1, Merged: 1, Moved: 1, Dissolved: 1, Created: 1},
		Events: []InterestEvent{
			{Event: "split", From: kafka, To: &InterestRef{ID: "c", Label: "Kafka Connect"}, Shared: 1},
			{Event: "merged", From: &InterestRef{ID: "j", Label: "Joins", Retired: true}, To: &InterestRef{ID: "s",
				Label: "Streams"}, Shared: 2},
			{Event: "moved", From: kafka, To: kafka, In: &InterestRef{ID: "e", Label: "Engineering", Area: true}},
			{Event: "dissolved", From: &InterestRef{ID: "b", Label: "Bonds", Retired: true}},
			{Event: "new", Area: true, To: &InterestRef{ID: "a", Label: "Cooking", Area: true}},
		}}
	out := render(t, r, PageChanges, Changes{Layout: Layout{Title: "What changed", Nav: NavInterests}, Run: run})
	assert.Contains(t, out, `<p class="lede">The rebuild of <time datetime="2026-10-09T14:03:00Z" title="`+when(at)+`">`+
		localDay(at)+`</time>: asked for, warm.</p>`)
	assert.Contains(t, out, `<span class="text">1 interest split, 1 merged, 1 dissolved, 1 moved, 1 new</span>`+
		`<span class="sep">·</span><span class="text">180 interests kept</span>`)
	heads := make([]string, 0, 5)
	for _, m := range regexp.MustCompile(`<h2 id="events-\d+">([^<]+)</h2>`).FindAllStringSubmatch(out, -1) {
		heads = append(heads, m[1])
	}
	assert.Equal(t, []string{"Split", "Merged", "Moved", "Dissolved", "New"}, heads)
	for _, line := range []string{
		`<a class="ref" href="/ui/interests/c" title="Kafka Connect">Kafka Connect</a> split off from ` +
			`<a class="ref" href="/ui/interests/k" title="Kafka">Kafka</a> (1 document)`,
		`<a class="ref" href="/ui/interests/j" title="Joins">Joins</a> <span class="tag">retired</span> merged into ` +
			`<a class="ref" href="/ui/interests/s" title="Streams">Streams</a> (2 documents)`,
		`<a class="ref" href="/ui/interests/k" title="Kafka">Kafka</a> moved to ` +
			`<a class="ref" href="/ui/interests/e" title="Engineering">Engineering</a>`,
		`<a class="ref" href="/ui/interests/b" title="Bonds">Bonds</a> <span class="tag">retired</span> dissolved`,
		`<li><span class="tag">area</span> <a class="ref" href="/ui/interests/a" title="Cooking">Cooking</a></li>`,
	} {
		assert.Contains(t, out, line)
	}

	first := &ChangesRun{ComputedAt: at, Trigger: "first", Kind: "fresh", Changes: RunChanges{Created: 1},
		Events: []InterestEvent{{Event: "new", To: kafka}}}
	assert.True(t, first.FirstGrouping())
	out = render(t, r, PageChanges, Changes{Layout: Layout{Title: "What changed", Nav: NavInterests}, Run: first})
	assert.Contains(t, out, "<h2>The library's first grouping</h2>")
	assert.NotContains(t, out, `class="event-lines"`)
	out = render(t, r, PageChanges, Changes{Layout: Layout{Title: "What changed", Nav: NavInterests},
		Run: &ChangesRun{ComputedAt: at, Trigger: "auto", Kind: "warm", Changes: RunChanges{Kept: 3}}})
	assert.Contains(t, out, "<h2>Nothing changed</h2>")
	assert.Contains(t, render(t, r, PageChanges, Changes{Layout: Layout{Title: "What changed", Nav: NavInterests}}),
		"<h2>No rebuild yet</h2>")
	assert.False(t, ChangesRun{Changes: RunChanges{Kept: 1}, Events: []InterestEvent{{Event: "new"}}}.FirstGrouping(),
		"a rebuild that kept some")
}

// TestGone_Page: a retired identity's page says what became of it, in a
// sentence by its successors' events, dated by its retirement, each
// successor linking to its page with what it took; an ID nothing knows
// names what it may have been, and neither names a request or the logs.
func TestGone_Page(t *testing.T) {
	r := newRenderer(t)
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	date := `<time datetime="2026-10-03T09:30:00Z" title="` + when(at) + `">` + localDay(at) + `</time>`
	succ := func(event string, retired bool) Successor {
		return Successor{InterestRef: InterestRef{ID: "s-" + event, Label: "To <" + event + ">", Retired: retired},
			Event: event, Shared: 3}
	}
	layout := Layout{Title: "Kafka", Nav: NavInterests}
	for _, tc := range []struct {
		retired Retired
		want    string
	}{
		{Retired{Label: "Kafka", RetiredAt: at, Successors: []Successor{succ("split", false), succ("split", true)}},
			`<p class="lede">It split into these on ` + date + `.</p>`},
		{Retired{Label: "Kafka", RetiredAt: at, Successors: []Successor{succ("merged", false)}},
			`<p class="lede">It merged into this on ` + date + `.</p>`},
		{Retired{Label: "Kafka", RetiredAt: at, Successors: []Successor{succ("merged", false), succ("merged", false)}},
			`<p class="lede">It merged into these on ` + date + `.</p>`},
		{Retired{Label: "Kafka", RetiredAt: at, Successors: []Successor{succ("split", false), succ("merged", false)}},
			`<p class="lede">Its documents went to these on ` + date + `.</p>`},
		{Retired{Label: "Kafka", RetiredAt: at}, `<p class="lede">It dissolved on ` + date + `, its documents going to ` +
			`other interests or to <a href="/ui/interests/unsorted">Unsorted</a>.</p>`},
	} {
		out := render(t, r, PageRetired, Gone{Layout: layout, ID: "x", Retired: &tc.retired, RetentionDays: 180})
		assert.Contains(t, out, "<h1>Kafka</h1>")
		assert.Contains(t, out, tc.want)
		assert.Contains(t, out, `<p><a href="/ui/interests">All interests →</a></p>`)
		assert.NotContains(t, out, "daemon logs")
		assert.NotContains(t, out, "Request ")
	}
	out := render(t, r, PageRetired, Gone{Layout: layout, Retired: &Retired{RetiredAt: at,
		Successors: []Successor{succ("split", true), succ("merged", false)}}, RetentionDays: 180})
	assert.Contains(t, out, "<h1>Unlabeled interest</h1>")
	assert.Contains(t, out, `<li><a class="ref" href="/ui/interests/s-split" title="To &lt;split&gt;">To &lt;split&gt;</a> `+
		`<span class="tag">retired</span><span class="muted">split off from it · 3 documents</span></li>`)
	assert.Contains(t, out, `<span class="muted">took it in · 3 documents</span>`)
	assert.Regexp(t, `<h1><svg class="icon"[^>]*>.*?</svg>Unlabeled area</h1>`,
		render(t, r, PageRetired, Gone{Layout: layout, Retired: &Retired{Area: true, RetiredAt: at}}))

	out = render(t, r, PageRetired, Gone{Layout: layout, ID: "no-such <id>", RetentionDays: 180})
	assert.Contains(t, out, "<h1>No such interest</h1>")
	assert.Contains(t, out, "Nothing in your interests has the ID <code>no-such &lt;id&gt;</code>.")
	assert.Contains(t, out, "It may be from before curio's interests were regrouped by an upgrade, or name one that "+
		"a rebuild split, merged or dissolved more than 180 days ago, which curio no longer remembers.")
	assert.NotContains(t, out, "daemon logs")
}

// TestDocument_Place: a document's line says where the latest rebuild put
// it, every name linking to its page, cut on one line, and sits outside
// its live jobs.
func TestDocument_Place(t *testing.T) {
	r := newRenderer(t)
	kafka := InterestRef{ID: "k", Label: "Kafka"}
	area := InterestRef{ID: "e", Label: "Engineering", Area: true}
	ref := func(r InterestRef) string {
		return `<a class="ref" href="/ui/interests/` + r.ID + `" title="` + r.Name() + `">` + r.Label + `</a>`
	}
	for _, tc := range []struct {
		place DocumentPlace
		want  string
	}{
		{DocumentPlace{Fit: "member", Interest: kafka, Area: area}, `In ` + ref(area) + ` › ` + ref(kafka)},
		{DocumentPlace{Fit: "member", Interest: kafka}, `In ` + ref(kafka)},
		{DocumentPlace{Fit: "loose", Interest: kafka, Area: area}, `Loose fit of ` + ref(area) + ` › ` + ref(kafka)},
		{DocumentPlace{Fit: "unsorted", Nearest: kafka}, `In <a href="/ui/interests/unsorted">Unsorted</a> · nearest ` + ref(kafka)},
		{DocumentPlace{Fit: "unsorted"}, `In <a href="/ui/interests/unsorted">Unsorted</a></span>`},
		{DocumentPlace{Fit: "new", Interest: kafka, Area: area}, `New since the last rebuild: in ` + ref(area) + ` › ` + ref(kafka)},
		{DocumentPlace{Fit: "new"}, `New since the last rebuild: in <a href="/ui/interests/unsorted">Unsorted</a>`},
	} {
		doc := placedDocument(Layout{Title: "d", Nav: NavLibrary}, tc.place)
		out := render(t, r, PageDocument, doc)
		assert.Contains(t, out, `<p class="doc-place fit-`+tc.place.Fit+`">`, tc.place.Fit)
		assert.Contains(t, out, tc.want, tc.place.Fit)
		assert.Less(t, strings.Index(out, `class="doc-place`), strings.Index(out, `id="doc-jobs"`), "outside the jobs")
	}
	nowhere := placedDocument(Layout{Title: "d", Nav: NavLibrary}, DocumentPlace{})
	nowhere.Place = nil
	assert.NotContains(t, render(t, r, PageDocument, nowhere), "doc-place", "nowhere, no line")
}

// TestStatus_InterestsRow: Status's health card has a row for automatic
// rebuilds of the interests, its dot by state and the sentence curio
// status prints, refreshed with health.
func TestStatus_InterestsRow(t *testing.T) {
	r := newRenderer(t)
	for _, tc := range []struct {
		state InterestsState
		row   string
	}{
		{InterestsState{State: "held", HeldReason: "the embeddings drifted"}, `<li><span class="label"><span class="dot dot-warn">` +
			`</span>Interests</span><span class="value">rebuilds held: the embeddings drifted; run <code>curio reindex --all</code></span></li>`},
		{InterestsState{State: "none", Changed: 3, RebuildAt: 20}, `<span class="dot dot-ok"></span>Interests</span>` +
			`<span class="value">waiting for 20 indexed documents (3 so far)</span>`},
		{InterestsState{State: "off"}, `<span class="dot dot-neutral"></span>Interests</span><span class="value">off (insight.enabled: false)</span>`},
		{InterestsState{State: "unknown"}, `<span class="value">the daemon hasn't checked them yet</span>`},
	} {
		out := render(t, r, PageStatus, Status{Layout: Layout{Title: "Status", Nav: NavStatus}, Poll: PollHealth,
			Health: &HealthPanel{OllamaReachable: true, Interests: tc.state}})
		row := strings.Index(out, tc.row)
		require.Positive(t, row, "%s:\n%s", tc.state.State, out)
		assert.Greater(t, row, strings.Index(out, `id="health-live"`), "%s: in health's region", tc.state.State)
	}
}

// TestMapPage_Ready: with a map to draw, the page is the Interests' head,
// the subnav with Map current, and the shell map.js fills: its root
// carrying where to read the map and the pages it leads to, the view and
// the selection asked for; the tabs, the search, the stage and the panel,
// each part it fills empty; and a card standing in without JavaScript.
func TestMapPage_Ready(t *testing.T) {
	r := newRenderer(t)
	page := MapPage{Layout: Layout{Title: "Interest map", Nav: NavInterests}, State: MapReady,
		Run: &MapRun{Shape: "areas", Documents: 5237, Areas: 29, Interests: 182,
			FinishedAt: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)},
		Query: MapQuery{View: MapViewGroups, Select: MapSelection{Kind: MapSelectInterest, ID: `"><script>alert(1)</script>`}}}
	out := render(t, r, PageMap, page)
	uitest.AssertInert(t, out)
	assert.Contains(t, out, "<title>Interest map · curio</title>")
	assert.Contains(t, out, `<p class="lede">5,237 documents in 29 areas holding 182 interests, as the rebuild of `)
	assert.Contains(t, out, `<nav class="subnav" aria-label="Interests views"><a href="/ui/interests">List</a>`+
		`<a href="/ui/interests/map" aria-current="page">Map</a></nav>`)
	doc := parse(t, out)
	root := byID(doc, "interest-map")
	require.NotNil(t, root)
	assert.Equal(t, "interest-map js-only", attrValue(root, "class"))
	for attr, want := range map[string]string{"data-src": "/v1/interests/map", "data-page": "/ui/interests/map",
		"data-document-page": "/ui/documents/", "data-interest-page": "/ui/interests/",
		"data-unsorted-page": "/ui/interests/unsorted", "data-interests-page": "/ui/interests", "data-view": "groups",
		"data-select": `interest:"><script>alert(1)</script>`} {
		assert.Equal(t, want, attrValue(root, attr), attr)
	}
	assert.Equal(t, "false", attrValue(byID(doc, "map-tab-similarity"), "aria-selected"))
	assert.Equal(t, "true", attrValue(byID(doc, "map-tab-groups"), "aria-selected"))
	assert.Equal(t, "0", attrValue(byID(doc, "map-tab-groups"), "tabindex"))
	assert.Equal(t, "map-tab-groups", attrValue(byID(doc, "map-stage"), "aria-labelledby"))
	search := byID(doc, "map-search")
	for attr, want := range map[string]string{"role": "combobox", "aria-expanded": "false", "aria-controls": "map-hits"} {
		assert.Equal(t, want, attrValue(search, attr), attr)
	}
	assert.False(t, hasAttr(search, "name"), "the map's search sends nothing")
	for _, id := range []string{"map-hits", "map-crumbs", "map-legend", "map-panel-kicker", "map-panel-title",
		"map-panel-body"} {
		require.NotNil(t, byID(doc, id), id)
		assert.Nil(t, byID(doc, id).FirstChild, "%s starts empty", id)
	}
	status := byID(doc, "map-status")
	assert.Equal(t, "status", attrValue(status, "role"))
	assert.Equal(t, "polite", attrValue(status, "aria-live"))
	assert.Equal(t, "Loading the map…", textOf(status))
	assert.Equal(t, "-1", attrValue(byID(doc, "map-panel-title"), "tabindex"))
	assert.Equal(t, "map-panel-body", attrValue(byID(doc, "map-sheet-toggle"), "aria-controls"))
	assert.Contains(t, out, `<div class="card no-js"><div class="empty">`)
	assert.Contains(t, out, `<h2>The map needs JavaScript</h2>`)

	page.Query = MapQuery{View: MapViewSimilarity}
	root = byID(parse(t, render(t, r, PageMap, page)), "interest-map")
	assert.Equal(t, "similarity", attrValue(root, "data-view"))
	assert.False(t, hasAttr(root, "data-select"), "the library: no selection")
}

// TestMapPage_None: without a map, the page says why, in a card of its
// own for each reason, with what draws it, and loads none of the map's
// scripts.
func TestMapPage_None(t *testing.T) {
	r := newRenderer(t)
	page := func(state MapState) MapPage {
		return MapPage{Layout: Layout{Title: "Interest map", Nav: NavInterests}, State: state,
			Rebuilds: InterestsState{State: "none", Changed: 3, RebuildAt: 20}}
	}
	for _, tc := range []struct {
		page MapPage
		want []string
	}{
		{page(MapNoRun), []string{"<h2>No interests yet</h2>", `<p class="interests-state">waiting for 20 indexed ` +
			`documents (3 so far).</p>`, "The first rebuild of the interests draws the map."}},
		{func() MapPage { p := page(MapNoRun); p.Rebuilds = InterestsState{State: "off"}; return p }(),
			[]string{"set <code>insight.enabled: true</code> in config.yaml and restart the daemon"}},
		{page(MapNoMap), []string{"<h2>No map yet</h2>", "a rebuild to draw it is due",
			`<a href="/ui/interests">Interests</a> rebuilds them now`, "<code>curio interests rebuild</code>"}},
		{func() MapPage { p := page(MapFailed); p.Error = strings.Repeat("e", 300) + "<b>"; return p }(),
			[]string{"<h2>The map failed</h2>", `<span class="state-error" title="` + strings.Repeat("e", 300) +
				`&lt;b&gt;">`, "<code>curio daemon logs</code>", "<code>insight.map: false</code>"}},
		{page(MapOff), []string{"<h2>The map is off</h2>", "(<code>insight.map: false</code>). Remove the setting, " +
			"or set it to true, and restart the daemon"}},
	} {
		out := render(t, r, PageMap, tc.page)
		uitest.AssertInert(t, out)
		for _, want := range tc.want {
			assert.Contains(t, out, want, tc.page.State)
		}
		assert.NotContains(t, out, `id="interest-map"`, tc.page.State)
		assert.NotContains(t, out, "d3-", tc.page.State)
		assert.Contains(t, out, `<a href="/ui/interests/map" aria-current="page">Map</a>`, tc.page.State)
	}
	failed := page(MapFailed)
	failed.Error = strings.Repeat("the map took too long ", 30)
	out := render(t, r, PageMap, failed)
	assert.Contains(t, out, shortError(failed.Error)+"</span>", "the error cut, whole on hover")
	assert.NotEqual(t, failed.Error, shortError(failed.Error))
}

// TestInterests_Subnav: the Interests' head leads to their map, in the
// whole page and never in a poll's answer.
func TestInterests_Subnav(t *testing.T) {
	r := newRenderer(t)
	subnav := `<nav class="subnav" aria-label="Interests views"><a href="/ui/interests" aria-current="page">List</a>` +
		`<a href="/ui/interests/map">Map</a></nav>`
	page := interestsPage(1)
	assert.Contains(t, render(t, r, PageInterests, page), subnav)
	page.Poll = PollRebuild
	assert.NotContains(t, render(t, r, PageInterests, page), `aria-label="Interests views"`)
}

// TestShowOnMap: an area's, an interest's, Unsorted's and a document's
// pages link to them on the map, the selection one escaped query value;
// none with the map off, nor Unsorted's before the first rebuild.
func TestShowOnMap(t *testing.T) {
	r := newRenderer(t)
	links := func(out string) []string {
		var hrefs []string
		for n := range parse(t, out).Descendants() {
			if n.Type == html.ElementNode && attrValue(n, "class") == "map-link" {
				hrefs = append(hrefs, attrValue(n, "href"))
				assert.Equal(t, "Show on map", textOf(n))
			}
		}
		return hrefs
	}
	layout := Layout{Title: "t", Nav: NavInterests}
	area := InterestPage{Layout: layout, Interest: Interest{ID: "a/1", Area: true, Label: "Area"}}
	interest := InterestPage{Layout: layout, Interest: Interest{ID: "i 1", Label: "Kafka"}}
	unsorted := Unsorted{Layout: layout, Run: "r1"}
	doc := placedDocument(Layout{Title: "d", Nav: NavLibrary}, DocumentPlace{DocumentID: "d&1", Fit: "member",
		Interest: InterestRef{ID: "k", Label: "Kafka"}})
	for _, tc := range []struct {
		page string
		data any
		href string
	}{
		{PageInterest, area, "/ui/interests/map?select=area%3Aa%2F1"},
		{PageInterest, interest, "/ui/interests/map?select=interest%3Ai+1"},
		{PageUnsorted, unsorted, "/ui/interests/map?select=unsorted"},
		{PageDocument, doc, "/ui/interests/map?select=document%3Ad%261"},
	} {
		out := render(t, r, tc.page, tc.data)
		uitest.AssertInert(t, out)
		assert.Equal(t, []string{tc.href}, links(out))
	}
	area.MapOff, interest.MapOff, unsorted.MapOff = true, true, true
	off := *doc.Place
	off.MapOff = true
	doc.Place = &off
	for _, tc := range []struct {
		page string
		data any
	}{{PageInterest, area}, {PageInterest, interest}, {PageUnsorted, unsorted}, {PageDocument, doc},
		{PageUnsorted, Unsorted{Layout: layout}}} {
		assert.Empty(t, links(render(t, r, tc.page, tc.data)), tc.page)
	}
}
