package ui

import (
	"math"
	"slices"
)

// A numbered list shows a page of its rows at a time, the pages counted
// from 1: a list whose rows never change between reads, such as a
// clustering run's interests, where page N names the same rows every time.
// The pager partial renders its Pager, and a page past the last its
// PageOutOfRange. A live list pages by cursor instead (Load more).

// PageOffset is how many rows come before page (from 1) of a list of size
// rows a page, size at least 1: (page-1)×size, or math.MaxInt when that
// overflows, which is past the end of any list.
func PageOffset(page, size int) int {
	if page <= 1 {
		return 0
	}
	if page-1 > math.MaxInt/size {
		return math.MaxInt
	}
	return (page - 1) * size
}

// pageCount is how many pages of size rows total rows fill: 0 for none.
func pageCount(total, size int) int {
	if total <= 0 {
		return 0
	}
	return (total-1)/size + 1
}

// Pager is a numbered list's pager (the pager partial): which rows the
// page shows of how many, and links to the page before, the page after,
// the first and last pages and the pages around this one.
type Pager struct {
	Summary     string // which rows the page shows: "Interests 25–48 of 222"
	Page, Pages int
	// Prev and Next are the pages before and after, "" on the first and
	// last page.
	Prev, Next string
	Items      []PagerItem
}

// PagerItem is a link to one page, or a gap for pages left out.
type PagerItem struct {
	Page    int // 0 for a gap
	Href    string
	Current bool
}

// Gap reports whether the item stands for pages left out.
func (i PagerItem) Gap() bool { return i.Page == 0 }

// pageSpan is a page of a numbered list, as its pager counts it.
type pageSpan struct {
	Page  int // from 1
	Size  int // rows a page holds
	Shown int // rows this page shows
	Total int // rows the list holds
	// Noun names the rows in the summary, and Suffix follows it:
	// "Documents 51–84 of 84, most similar first".
	Noun, Suffix string
	Href         func(page int) string
}

// newPager is s's pager: nil when the list fits on one page or the page
// shows no rows, which leaves nowhere to go.
func newPager(s pageSpan) *Pager {
	pages := pageCount(s.Total, s.Size)
	if pages <= 1 || s.Shown <= 0 {
		return nil
	}
	first := PageOffset(s.Page, s.Size) + 1
	p := &Pager{
		Summary: s.Noun + " " + num(first) + "–" + num(first+s.Shown-1) + " of " + num(s.Total) + s.Suffix,
		Page:    s.Page,
		Pages:   pages,
	}
	if s.Page > 1 {
		p.Prev = s.Href(s.Page - 1)
	}
	if s.Page < pages {
		p.Next = s.Href(s.Page + 1)
	}
	link := func(page int) PagerItem {
		return PagerItem{Page: page, Href: s.Href(page), Current: page == s.Page}
	}
	// The first and last pages, the current one and its neighbours; the
	// pages between them are a gap, but a gap of one page is that page.
	last := 0
	for _, page := range pagerPages(s.Page, pages) {
		switch page - last {
		case 1:
		case 2:
			p.Items = append(p.Items, link(last+1))
		default:
			p.Items = append(p.Items, PagerItem{})
		}
		p.Items = append(p.Items, link(page))
		last = page
	}
	return p
}

// pagerPages are the pages of pages a pager links by number whatever
// else it leaves out, in order: the first and last, page and its
// neighbours.
func pagerPages(page, pages int) []int {
	out := make([]int, 0, 5)
	for _, p := range []int{1, page - 1, page, page + 1, pages} {
		if p >= 1 && p <= pages {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// PageOutOfRange is a page past a numbered list's last (the out-of-range
// partial): the page asked for, how many there are, and links to the
// first and last.
type PageOutOfRange struct {
	Page, Pages int // Pages is at least 1: a list of no rows is one empty page
	First, Last string
}

// outOfRange is page's PageOutOfRange in a list of pages, or nil when
// the list has that page.
func outOfRange(page, pages int, href func(page int) string) *PageOutOfRange {
	pages = max(pages, 1)
	if page <= pages {
		return nil
	}
	return &PageOutOfRange{Page: page, Pages: pages, First: href(1), Last: href(pages)}
}
