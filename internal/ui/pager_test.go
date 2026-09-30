package ui

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagerItems writes a pager's items as a line: each page's number, the
// current one starred, and a gap as "…".
func pagerItems(p *Pager) string {
	parts := make([]string, 0, len(p.Items))
	for _, item := range p.Items {
		switch {
		case item.Gap():
			parts = append(parts, "…")
		case item.Current:
			parts = append(parts, strconv.Itoa(item.Page)+"*")
		default:
			parts = append(parts, strconv.Itoa(item.Page))
		}
	}
	return strings.Join(parts, " ")
}

// testPageHref is a page's link in the tests' pagers.
func testPageHref(page int) string { return "/list?page=" + strconv.Itoa(page) }

// TestPager_Items: the first and last pages, the current one and its
// neighbours are linked; the pages between them are one gap, or the page
// itself where only one is left out.
func TestPager_Items(t *testing.T) {
	for _, tc := range []struct {
		page, pages int
		want        string
	}{
		{1, 2, "1* 2"},
		{2, 3, "1 2* 3"},
		{1, 10, "1* 2 … 10"},
		{3, 10, "1 2 3* 4 … 10"},
		{4, 10, "1 2 3 4* 5 … 10"},
		{5, 10, "1 … 4 5* 6 … 10"},
		{8, 10, "1 … 7 8* 9 10"},
		{10, 10, "1 … 9 10*"},
	} {
		p := newPager(pageSpan{Page: tc.page, Size: 10, Shown: 10, Total: tc.pages * 10, Noun: "Rows",
			Href: testPageHref})
		require.NotNil(t, p, "%d of %d", tc.page, tc.pages)
		assert.Equal(t, tc.want, pagerItems(p), "%d of %d", tc.page, tc.pages)
		assert.Equal(t, tc.page, p.Page)
		assert.Equal(t, tc.pages, p.Pages)
		for _, item := range p.Items {
			if !item.Gap() {
				assert.Equal(t, testPageHref(item.Page), item.Href)
			}
		}
	}
}

// TestPager_Steps: Previous and Next lead to the pages either side, and
// are empty at the ends; a list that fits on one page, or a page that
// shows nothing, has no pager.
func TestPager_Steps(t *testing.T) {
	first := newPager(pageSpan{Page: 1, Size: 24, Shown: 24, Total: 222, Href: testPageHref})
	assert.Empty(t, first.Prev)
	assert.Equal(t, testPageHref(2), first.Next)
	middle := newPager(pageSpan{Page: 5, Size: 24, Shown: 24, Total: 222, Href: testPageHref})
	assert.Equal(t, testPageHref(4), middle.Prev)
	assert.Equal(t, testPageHref(6), middle.Next)
	last := newPager(pageSpan{Page: 10, Size: 24, Shown: 6, Total: 222, Href: testPageHref})
	assert.Equal(t, testPageHref(9), last.Prev)
	assert.Empty(t, last.Next)

	assert.Nil(t, newPager(pageSpan{Page: 1, Size: 24, Shown: 24, Total: 24, Href: testPageHref}), "one page")
	assert.Nil(t, newPager(pageSpan{Page: 1, Size: 24, Total: 0, Href: testPageHref}), "no rows")
	assert.Nil(t, newPager(pageSpan{Page: 11, Size: 24, Total: 222, Href: testPageHref}), "past the last")
}

// TestPager_Summary: which rows the page shows, counted from the page's
// offset, its numbers grouped, then the suffix.
func TestPager_Summary(t *testing.T) {
	for _, tc := range []struct {
		span pageSpan
		want string
	}{
		{pageSpan{Page: 1, Size: 24, Shown: 24, Total: 222, Noun: "Interests"}, "Interests 1–24 of 222"},
		{pageSpan{Page: 10, Size: 24, Shown: 6, Total: 222, Noun: "Interests"}, "Interests 217–222 of 222"},
		{pageSpan{Page: 2, Size: 50, Shown: 34, Total: 84, Noun: "Documents", Suffix: ", most similar first"},
			"Documents 51–84 of 84, most similar first"},
		{pageSpan{Page: 51, Size: 24, Shown: 24, Total: 1951, Noun: "Interests"}, "Interests 1,201–1,224 of 1,951"},
	} {
		tc.span.Href = testPageHref
		assert.Equal(t, tc.want, newPager(tc.span).Summary)
	}
}

// TestPageOffset: the rows before a page, and past any list when that
// overflows.
func TestPageOffset(t *testing.T) {
	assert.Equal(t, 0, PageOffset(1, 24))
	assert.Equal(t, 0, PageOffset(0, 24), "no page is the first")
	assert.Equal(t, 24, PageOffset(2, 24))
	assert.Equal(t, 216, PageOffset(10, 24))
	assert.Equal(t, math.MaxInt, PageOffset(math.MaxInt, 24))
	assert.Equal(t, math.MaxInt, PageOffset(math.MaxInt/24+2, 24))
	assert.Equal(t, (math.MaxInt/24)*24, PageOffset(math.MaxInt/24+1, 24), "the last page that fits")
}

// TestOutOfRange: only a page past the last is out of range, and a list of
// no rows has one page, empty.
func TestOutOfRange(t *testing.T) {
	assert.Nil(t, outOfRange(10, 10, testPageHref))
	assert.Nil(t, outOfRange(1, 0, testPageHref), "the empty list's one page")
	assert.Equal(t, &PageOutOfRange{Page: 11, Pages: 10, First: testPageHref(1), Last: testPageHref(10)},
		outOfRange(11, 10, testPageHref))
	assert.Equal(t, &PageOutOfRange{Page: math.MaxInt, Pages: 1, First: testPageHref(1), Last: testPageHref(1)},
		outOfRange(math.MaxInt, 0, testPageHref))
}

// TestPager_Markup: the pager's links are built in Go and named for a
// screen reader; the current page is marked; Previous on the first page
// and Next on the last are not links, and the steps that are have the ids
// htmx gives focus back by; a phone's "Page N of M" is there.
func TestPager_Markup(t *testing.T) {
	set := newRenderer(t).pages[PageInterests]
	pager := func(page int) string {
		var buf strings.Builder
		require.NoError(t, set.ExecuteTemplate(&buf, "pager",
			newPager(pageSpan{Page: page, Size: 24, Shown: 24, Total: 222, Noun: "Interests", Href: testPageHref})))
		return buf.String()
	}
	out := pager(1)
	assert.Contains(t, out, `<nav class="pager" aria-label="Pages"><span class="pager-summary">Interests 1–24 of 222</span>`)
	assert.Contains(t, out, `<span class="step" aria-disabled="true"><svg class="icon" viewBox="0 0 24 24" aria-hidden="true">`+
		`<path d="m15 18-6-6 6-6"/></svg>Previous</span>`)
	assert.NotContains(t, out, `rel="prev"`)
	assert.Contains(t, out, `<a href="/list?page=1" aria-label="Page 1" aria-current="page">1</a>`+
		`<a href="/list?page=2" aria-label="Page 2">2</a><span class="gap" aria-hidden="true">…</span>`+
		`<a href="/list?page=10" aria-label="Page 10">10</a><span class="of">Page 1 of 10</span>`)
	assert.Contains(t, out, `<a class="step" id="pager-next" href="/list?page=2" rel="next">Next<svg class="icon"`)
	assert.NotContains(t, out, `id="pager-prev"`)
	assert.Equal(t, 1, strings.Count(out, `aria-current="page"`))

	out = pager(10)
	assert.Contains(t, out, `<a class="step" id="pager-prev" href="/list?page=9" rel="prev"><svg class="icon"`)
	assert.NotContains(t, out, `id="pager-next"`)
	assert.Contains(t, out, `<span class="step" aria-disabled="true">Next<svg class="icon"`)
	assert.NotContains(t, out, `rel="next"`)
	assert.Contains(t, out, `<span class="of">Page 10 of 10</span>`)
}
