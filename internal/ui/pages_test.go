package ui

import (
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
// Jina is named for people.
func TestUpstream(t *testing.T) {
	for state, tone := range map[string]string{"ok": "ok", "idle": "ok", "degraded": "warn", "paused": "warn",
		"failing": "danger", "disabled": "neutral", evilScript: "neutral"} {
		assert.Equal(t, tone, Upstream{Name: "jina", Enabled: true, State: state}.Tone(), state)
	}
	assert.Equal(t, "neutral", Upstream{State: "failing"}.Tone(), "off")
	assert.Equal(t, "Jina Reader", Upstream{Name: "jina"}.Label())
	assert.Equal(t, "other", Upstream{Name: "other"}.Label())
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
