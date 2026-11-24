package ui

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// within reports whether n is inside an element with id.
func within(n *html.Node, id string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && attrValue(p, "id") == id {
			return true
		}
	}
	return false
}

// TestSearch_RankingRegion: in every sample of a search's results, the
// part a reordered head replaces, #ranking, holds the head's slot, the
// results and the pager, and nothing else: not the understood line, the
// callouts or the once-only star ask, which a swap would remove or read
// out again. Each element in it that can take focus has an id of its
// own on the page, so htmx gives focus back after the swap.
func TestSearch_RankingRegion(t *testing.T) {
	r := newRenderer(t)
	regions := 0
	for i, data := range allSamples(t, r)[PageSearch] {
		doc := parse(t, render(t, r, PageSearch, data))
		ranking := byID(doc, "ranking")
		if ranking == nil {
			continue
		}
		regions++
		for _, class := range []string{"star-ask", "understood", "understood-note", "callout"} {
			for _, n := range withClass(doc, class) {
				assert.False(t, within(n, "ranking"), "sample %d: .%s inside #ranking", i, class)
			}
		}
		if status := byID(doc, "rerank-status"); status != nil {
			assert.True(t, within(status, "ranking"), "sample %d: #rerank-status in #ranking", i)
		}
		ids := elementIDs(doc)
		for n := range ranking.Descendants() {
			if n.Type == html.ElementNode && focusable(n) {
				id := attrValue(n, "id")
				assert.NotEmpty(t, id, "sample %d: <%s> in #ranking without an id", i, n.Data)
				assert.Equal(t, 1, ids[id], "sample %d: id %q", i, id)
			}
		}
	}
	assert.GreaterOrEqual(t, regions, 10, "the samples with results")
}

// TestSearch_FollowUp: an understood page's follow-up is one element, the
// head's slot, that htmx loads once: its own page, its plan reordered,
// never understood again; it replaces #ranking with the answer's, and a
// keystroke, Enter or a page aborts it, or drops it while one is in
// flight. Its pending line names the model, and shows only while it waits.
func TestSearch_FollowUp(t *testing.T) {
	r := newRenderer(t)
	s := Search{Layout: Layout{Nav: NavSearch}, Query: "kafka", Type: "pdf", Plan: "tok", Page: 1,
		Results: &SearchResults{WordHits: 1, Total: 4, Hits: sampleHits()[:2]}, Reorder: &Reorder{Model: "m:1"}}
	out := render(t, r, PageSearch, s)
	assert.Equal(t, 1, strings.Count(out, `id="rerank-status"`))
	assert.Contains(t, out, `<div id="ranking"><p class="rerank-status js-only" id="rerank-status" `+
		`hx-get="/ui/?content_type=pdf&amp;plan=tok&amp;q=kafka&amp;rerank=1" hx-trigger="load" hx-target="#ranking" `+
		`hx-select="#ranking" hx-select-oob="#rerank-said:innerHTML" hx-swap="outerHTML" `+
		`hx-sync="closest .search-page:abort">`+
		`<span class="htmx-indicator spinner" aria-hidden="true"></span>`+
		`<span class="htmx-indicator">Reordering with m:1…</span></p>`)
	assert.NotContains(t, out, "understand=1")

	s.Reorder, s.Order, s.Reordered = nil, "ord", &Reordered{Status: ReorderedByModel, Model: "m:1"}
	out = render(t, r, PageSearch, s)
	assert.Contains(t, out, `<div id="ranking"><p class="rerank-status" id="rerank-status">Reordered by m:1</p>`)
	assert.NotContains(t, out, `hx-trigger="load"`, "the reordered page sends nothing more")
}

// TestSearch_FollowUpKeepsTheTime: the results head's time is how long
// the results took to show, Enter's page's, and stays so once the
// follow-up's answer swaps the reordered head in: that answer's time, the
// search run again and the rerank, is marked hx-preserve, so htmx keeps
// the element of the same id already on the page. Every other page's,
// a reload of the reordered address among them, is its own and preserves
// nothing.
func TestSearch_FollowUpKeepsTheTime(t *testing.T) {
	r := newRenderer(t)
	base := Search{Layout: Layout{Nav: NavSearch}, Query: "kafka", Plan: "tok", Page: 1,
		Results: &SearchResults{WordHits: 1, Total: 4, TookMS: 1234, Hits: sampleHits()[:2]}}
	for _, tc := range []struct {
		name     string
		edit     func(*Search)
		preserve bool
	}{
		{"Enter's page", func(s *Search) { s.Reorder = &Reorder{Model: "m:1"} }, false},
		{"the follow-up's reordered page", func(s *Search) {
			s.Order, s.FollowUp, s.Reordered = "ord", true, &Reordered{Status: ReorderedByModel, Model: "m:1"}
		}, true},
		{"a reload of the reordered address", func(s *Search) {
			s.Order, s.Reordered = "ord", &Reordered{Status: ReorderedAsFirst}
		}, false},
		{"a search of the words", func(s *Search) { s.Plan = "" }, false},
	} {
		s := base
		tc.edit(&s)
		doc := parse(t, render(t, r, PageSearch, s))
		took := byID(doc, "search-took")
		require.NotNil(t, took, tc.name)
		assert.True(t, within(took, "ranking"), "%s: in the head #ranking swaps", tc.name)
		assert.Equal(t, "1234 ms", textOf(took), tc.name)
		assert.Equal(t, tc.preserve, attrValue(took, "hx-preserve") == "true", tc.name)
	}
}

// TestSearch_ReorderSaid: the search page says what its follow-up did in
// one status of its own, #rerank-said: outside #results, whose live
// region would read the reordered list out again (search.js swaps the
// follow-up's answer in with it off), and kept in the page's
// accessibility tree, visually hidden, so it announces as its words
// change. Only the follow-up's answer fills it, swapped in out of band;
// the box's and the form's answers, a search of their own, clear it, and
// every other page leaves it empty.
func TestSearch_ReorderSaid(t *testing.T) {
	r := newRenderer(t)
	base := Search{Layout: Layout{Nav: NavSearch}, Query: "kafka", Plan: "tok", Page: 1,
		Results: &SearchResults{WordHits: 1, Total: 4, Hits: sampleHits()[:2]}}
	held := time.Date(2026, 11, 23, 15, 4, 0, 0, time.Local)
	for _, tc := range []struct {
		name string
		edit func(*Search)
		said string
	}{
		{"Enter's page", func(s *Search) { s.Reorder = &Reorder{Model: "m:1"} }, ""},
		{"the follow-up's reordered page", func(s *Search) {
			s.Order, s.FollowUp, s.Reordered = "ord", true, &Reordered{Status: ReorderedByModel, Model: "m:1"}
		}, "Reordered by m:1"},
		{"the follow-up's note", func(s *Search) {
			s.FollowUp, s.Reordered = true, &Reordered{Status: ReorderedHeld, HeldUntil: held}
		}, "Kept curio's order: reordering resumes at " + held.Format(time.Kitchen) + "; curio doctor says why."},
		{"the follow-up's 204", func(s *Search) { s.FollowUp = true }, ""},
		{"a reload of the reordered address", func(s *Search) {
			s.Order, s.Reordered = "ord", &Reordered{Status: ReorderedAsFirst}
		}, ""},
		{"a note on Enter's page", func(s *Search) {
			s.Reordered = &Reordered{Status: ReorderedUnfit, Model: "m:1"}
		}, ""},
	} {
		s := base
		tc.edit(&s)
		doc := parse(t, render(t, r, PageSearch, s))
		said := byID(doc, "rerank-said")
		require.NotNil(t, said, tc.name)
		assert.Equal(t, tc.said, textOf(said), tc.name)
		assert.Equal(t, "p", said.Data, tc.name)
		assert.Equal(t, "visually-hidden", attrValue(said, "class"), tc.name)
		assert.Equal(t, "status", attrValue(said, "role"), tc.name)
		assert.Equal(t, "polite", attrValue(said, "aria-live"), tc.name)
		for p := said.Parent; p != nil; p = p.Parent {
			assert.NotEqual(t, "results", attrValue(p, "id"), "%s: outside #results", tc.name)
		}
		assert.Equal(t, "polite", attrValue(byID(doc, "results"), "aria-live"), tc.name)
		ranking := byID(doc, "ranking")
		require.NotNil(t, ranking, tc.name)
		for n := range ranking.Descendants() {
			if n.Type == html.ElementNode {
				assert.False(t, hasAttr(n, "aria-live") || attrValue(n, "role") == "status", "%s: <%s> in #ranking announces",
					tc.name, n.Data)
			}
		}
		box := byID(doc, "q")
		assert.Equal(t, "#search-scope,#rerank-said:innerHTML", attrValue(box, "hx-select-oob"), "%s: the box", tc.name)
		assert.Equal(t, "#search-scope,#rerank-said:innerHTML", attrValue(box.Parent.Parent, "hx-select-oob"),
			"%s: the form", tc.name)
	}
}

// TestSearch_TitleIDsFollowTheDocument: a result's ids are its document's,
// the same in the first ranking and the reordered one, wherever it moved.
func TestSearch_TitleIDsFollowTheDocument(t *testing.T) {
	r := newRenderer(t)
	hits := sampleHits()
	first := Search{Query: "kafka", Results: &SearchResults{WordHits: 1, Total: len(hits), Hits: hits}}
	again := first
	again.Results = &SearchResults{WordHits: 1, Total: len(hits), Hits: reversed(hits)}
	idsOf := func(s Search) map[string]string {
		doc := parse(t, render(t, r, PageSearch, s))
		out := map[string]string{}
		for _, a := range withAttr(doc, "id") {
			if id := attrValue(a, "id"); strings.HasPrefix(id, "result-") && a.Data == "a" &&
				!strings.HasSuffix(id, "-original") {
				out[attrValue(a, "href")] = id
			}
		}
		return out
	}
	before, after := idsOf(first), idsOf(again)
	require.Len(t, before, len(hits))
	assert.Equal(t, before, after)
}

// TestSearch_OnlyItsWindowsKeepTheOrder: the order a page was searched by
// is kept by its pager and its own address, windows of the same ranking,
// and by nothing that searches another: a type, the plan less a filter,
// its undated documents switched, or the words alone.
func TestSearch_OnlyItsWindowsKeepTheOrder(t *testing.T) {
	undated := 4
	s := Search{Query: "kafka", Plan: "plan", Order: "ord", Page: 2,
		Results: &SearchResults{WordHits: 1, Total: 37, Hits: sampleHits()},
		Understood: &Understood{Status: "planned", Undated: &undated, UndatedToken: "switched",
			Filters: []PlanFilter{{Field: "saved", After: "2024-01-01", Token: "less"}}}}
	keeps := []string{s.Address()}
	p := s.Pager()
	require.NotNil(t, p)
	keeps = append(keeps, p.Prev, p.Next)
	for _, item := range p.Items {
		if !item.Gap() {
			keeps = append(keeps, item.Href)
		}
	}
	for _, href := range keeps {
		q := parseHref(t, href).Query()
		assert.Equal(t, "ord", q.Get(OrderParam), href)
		assert.Equal(t, "plan", q.Get(PlanParam), href)
		assert.False(t, q.Has(RerankParam), href)
	}
	drops := make([]string, 0, len(searchTypes)+3)
	for _, tab := range s.TypeTabs() {
		drops = append(drops, tab.Href)
	}
	for _, chip := range s.Chips() {
		drops = append(drops, chip.Href)
	}
	line := s.UndatedLine()
	require.NotNil(t, line)
	drops = append(drops, line.Href, s.WordsHref())
	require.Len(t, drops, len(searchTypes)+3)
	for _, href := range drops {
		q := parseHref(t, href).Query()
		assert.False(t, q.Has(OrderParam), href)
		assert.False(t, q.Has(RerankParam), href)
	}
}

// TestReordered_Note: what a page says of its head, from the status
// alone: the model that reordered it, the first page's order kept and how
// much of it has left the top, and why curio's order was kept, with the
// fix or when it passes.
func TestReordered_Note(t *testing.T) {
	until := time.Date(2026, 11, 3, 14, 5, 0, 0, time.Local)
	for _, tc := range []struct {
		r    Reordered
		want string
	}{
		{Reordered{Status: ReorderedByModel, Model: "m:1"}, "Reordered by m:1"},
		{Reordered{Status: ReorderedAsFirst}, "Reordered as the first page was"},
		{Reordered{Status: ReorderedAsFirst, Stale: 1},
			"Reordered as the first page was · 1 document has left the top since"},
		{Reordered{Status: ReorderedAsFirst, Stale: 3},
			"Reordered as the first page was · 3 documents have left the top since"},
		{Reordered{Status: ReorderedUnfit, Model: "m:1"}, "Kept curio's order: m:1 didn't pass curio's check " +
			"for reordering results; set generation.roles.reranker.model to a model that passes, or " +
			"search.rerank: false."},
		{Reordered{Status: ReorderedHeld, HeldUntil: until},
			"Kept curio's order: reordering resumes at 2:05PM; curio doctor says why."},
		{Reordered{Status: "timeout"}, ""},
	} {
		assert.Equal(t, tc.want, tc.r.Note(), tc.r.Status)
	}
}

// TestRerankHref: the follow-up's request is its page's window, by its
// plan, asking the reranker, and never the planner.
func TestRerankHref(t *testing.T) {
	assert.Equal(t, "/ui/?plan=tok&q=kafka&rerank=1", rerankHref("kafka", "", 1, "tok"))
	u, err := url.Parse(rerankHref("kafka", "pdf", 3, "tok"))
	require.NoError(t, err)
	assert.Equal(t, url.Values{"q": {"kafka"}, "content_type": {"pdf"}, PageParam: {"3"}, PlanParam: {"tok"},
		RerankParam: {"1"}}, u.Query())
	assert.Equal(t, "/ui/?order=ord&plan=tok&q=kafka", searchHref("kafka", "", 1, "tok", "ord"))
	assert.NotContains(t, rerankHref("kafka", "", 1, "tok"), "understand")
}
